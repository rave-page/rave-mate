package automation

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/governor"
	"rave.page/mate/internal/store"
)

// Service ties the persisted automations/schedules to the engine, fs-watcher, and scheduler.
// It implements Manager and runs as a daemon module (Start/Stop). A nil store yields a
// degraded Service (in-memory only); a nil worker means runs fail at the first media step.
type Service struct {
	st      *store.Store
	w       Worker
	presets PresetResolver
	log     Logger

	sched   *Scheduler
	watcher *Watcher

	bus *eventBus // run-event fan-out to studio subscribers

	coord *coordinator // serializes/prioritizes runs (self-coalesce, path, heavy-slot, governor)

	bgMu    sync.Mutex // background credentials for rename-from-event / listEvents
	bgBase  string
	bgToken string

	runsMu sync.Mutex
	active map[string]*runContext // in-flight interactive runs (manual step-gating)

	changeMu  sync.Mutex
	changeSub map[int]func() // CRUD-change subscribers (UI live-refresh)
	changeSeq int

	// Change-aware caches for the ~1Hz webui Automations tab. List/ListSchedules/Runs each
	// full-scan bbolt (ForEach-copy + unmarshal-all; Runs also sorts); cache the built, sorted
	// result and invalidate on any write to the matching bucket. Guarded by cacheMu. Callers get a
	// SHALLOW copy of the slice: safe to reorder/append, but a returned element's nested slices
	// (Automation.Actions / Match.Extensions / Run.Steps) still alias the master - do NOT mutate a
	// returned element or its inner slices (all current consumers are read-only or build fresh copies).
	cacheMu    sync.Mutex
	autosCache []Automation
	autosOK    bool
	schedCache []Schedule
	schedOK    bool
	runsCache  []Run // full history, sorted newest-first; Runs(limit) returns a limited copy
	runsOK     bool

	// version bumps on every autos/scheds/runs cache invalidation (i.e. any change the webui
	// Automations tab can render). The ~1Hz webui tick reads it to skip the full autoBody
	// rebuild+patch when nothing changed. Atomic: read lock-free off the UI tick.
	version atomic.Uint64

	mu     sync.Mutex
	seq    int64
	ctx    context.Context
	cancel context.CancelFunc
}

var _ Manager = (*Service)(nil)

// NewManager builds the automation facade. Call Start to begin watching/scheduling.
func NewManager(st *store.Store, w Worker, presets PresetResolver, log Logger) *Service {
	m := &Service{st: st, w: w, presets: presets, log: log, bus: newEventBus(), active: map[string]*runContext{}}
	m.coord = newCoordinator(defaultMaxHeavyRuns, func() { m.version.Add(1) })
	return m
}

// SetMaxHeavyRuns sets how many heavy (ffmpeg) runs may execute concurrently (MaxHeavyRuns knob;
// default 1). Rebuilds the coordinator; call before Start.
func (m *Service) SetMaxHeavyRuns(n int) {
	if m.coord != nil {
		m.coord.stop()
	}
	m.coord = newCoordinator(n, func() { m.version.Add(1) })
}

// OnEvent subscribes to interactive run events; returns an unsubscribe func.
func (m *Service) OnEvent(fn func(RunEvent)) func() { return m.bus.on(fn) }

// OnChange subscribes to automation/schedule CRUD changes - including changes made by a remote
// controller over remotectl - so the local UI live-refreshes. Fires after persistence + rearm.
// Returns an unsubscribe func.
func (m *Service) OnChange(fn func()) func() {
	m.changeMu.Lock()
	if m.changeSub == nil {
		m.changeSub = map[int]func(){}
	}
	id := m.changeSeq
	m.changeSeq++
	m.changeSub[id] = fn
	m.changeMu.Unlock()
	return func() {
		m.changeMu.Lock()
		delete(m.changeSub, id)
		m.changeMu.Unlock()
	}
}

func (m *Service) fireChange() {
	m.changeMu.Lock()
	subs := make([]func(), 0, len(m.changeSub))
	for _, fn := range m.changeSub {
		subs = append(subs, fn)
	}
	m.changeMu.Unlock()
	for _, fn := range subs {
		fn()
	}
}

// SetBackgroundCredentials sets (or clears, on "") the apiBaseUrl+token used by
// rename-from-event + listEvents. The studio channel injects the desktop's own token.
func (m *Service) SetBackgroundCredentials(apiBaseURL, token string) {
	m.bgMu.Lock()
	m.bgBase, m.bgToken = apiBaseURL, token
	m.bgMu.Unlock()
}

func (m *Service) bgCreds() (string, string) {
	m.bgMu.Lock()
	defer m.bgMu.Unlock()
	return m.bgBase, m.bgToken
}

// ProbeSilence runs the leading/trailing silence probe → web SilenceProbeResult.
func (m *Service) ProbeSilence(ctx context.Context, path string, thresholdDb, minSilence float64) (SilenceResult, error) {
	if m.w == nil {
		return SilenceResult{}, fmt.Errorf("worker unavailable")
	}
	if thresholdDb == 0 {
		thresholdDb = defaultThresholdDb
	}
	if minSilence == 0 {
		minSilence = defaultMinSilenceSecs
	}
	lead, trail, dur, err := cachedSilenceProbe(ctx, m.st, m.w, path, thresholdDb, minSilence)
	if err != nil {
		return SilenceResult{}, err
	}
	res := SilenceResult{LeadingSilenceSeconds: lead, TrailingSilenceSeconds: trail, Regions: []SilenceRegion{}}
	if dur > 0 {
		d := dur
		res.DurationSeconds = &d
	}
	if lead > 0 {
		res.Regions = append(res.Regions, SilenceRegion{StartSeconds: 0, EndSeconds: lead, DurationSeconds: lead})
	}
	if trail > 0 && dur > 0 {
		res.Regions = append(res.Regions, SilenceRegion{StartSeconds: dur - trail, EndSeconds: dur, DurationSeconds: trail})
	}
	return res, nil
}

// ListEvents matches the caller's booked events against a recording's mtime. An unparseable
// mtime returns the full involved-events list (web behavior).
func (m *Service) ListEvents(ctx context.Context, mtimeISO string, bufferMinutes int) ([]MatchedEvent, error) {
	base, token := m.bgCreds()
	events, err := fetchUserEvents(ctx, base, token)
	if err != nil {
		return nil, err
	}
	if bufferMinutes == 0 {
		bufferMinutes = defaultBufferMinutes
	}
	fileMs, ok := parseMs(mtimeISO)
	if !ok {
		out := make([]MatchedEvent, 0, len(events))
		for i := range events {
			if events[i].idStr() != "" {
				out = append(out, *toMatched(&events[i]))
			}
		}
		return out, nil
	}
	if mev := pickMatchingEvent(events, fileMs, bufferMinutes); mev != nil {
		return []MatchedEvent{*mev}, nil
	}
	return []MatchedEvent{}, nil
}

// Start arms the watcher + scheduler from persisted state (daemon module entry point).
func (m *Service) Start(ctx context.Context) error {
	m.mu.Lock()
	m.ctx, m.cancel = context.WithCancel(ctx)
	m.mu.Unlock()

	m.sched = NewScheduler(m.log, m.onSchedule)
	w, err := NewWatcher(m.log, m.onWatchFile)
	if err != nil {
		m.log.Warn(source, "file watcher unavailable", map[string]any{"error": err.Error()})
	} else {
		m.watcher = w
	}
	// Wake the coordinator when the good-neighbour governor flips (a stream ending releases the
	// heavy slot's streaming-deferral). Reuses the ONE governor gate - no second gate.
	if m.coord != nil {
		governor.OnChange(func(governor.Signals) { m.coord.wake() })
	}
	m.rearm()
	return nil
}

// Stop tears down the watcher + scheduler.
func (m *Service) Stop() {
	m.mu.Lock()
	cancel := m.cancel
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if m.watcher != nil {
		m.watcher.Stop()
	}
	if m.sched != nil {
		m.sched.Stop()
	}
	if m.coord != nil {
		m.coord.stop()
	}
}

// rearm re-applies the enabled automations/schedules to the watcher + scheduler.
func (m *Service) rearm() {
	autos := m.List()
	if m.watcher != nil {
		m.watcher.Set(autos)
	}
	if m.sched != nil {
		m.sched.Set(m.ListSchedules())
	}
	m.fireChange() // notify UI subscribers (covers local + remote-controller CRUD)
}

func (m *Service) baseCtx() context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ctx != nil {
		return m.ctx
	}
	return context.Background()
}

func (m *Service) nextID(prefix string) string {
	m.mu.Lock()
	m.seq++
	n := m.seq
	m.mu.Unlock()
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), n)
}

// ── automations CRUD ─────────────────────────────────────────────────────────

func (m *Service) List() []Automation {
	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()
	if !m.autosOK {
		raws, _ := m.st.ListJSON(store.BucketAutomations)
		out := make([]Automation, 0, len(raws))
		for _, raw := range raws {
			var a Automation
			if json.Unmarshal(raw, &a) == nil {
				out = append(out, a)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
		m.autosCache, m.autosOK = out, true
	}
	return append([]Automation(nil), m.autosCache...) // defensive copy; master stays immutable
}

// invalidate* clear a cache AND bump version (bump after the store write in every caller, so a
// tick observing the new version also observes the invalidated cache → rereads fresh data).
func (m *Service) invalidateAutos() {
	m.cacheMu.Lock()
	m.autosOK = false
	m.cacheMu.Unlock()
	m.version.Add(1)
}
func (m *Service) invalidateScheds() {
	m.cacheMu.Lock()
	m.schedOK = false
	m.cacheMu.Unlock()
	m.version.Add(1)
}
func (m *Service) invalidateRuns() {
	m.cacheMu.Lock()
	m.runsOK = false
	m.cacheMu.Unlock()
	m.version.Add(1)
}

// Version is a cheap monotonic counter of automations/schedules/runs changes (webui tick gate).
func (m *Service) Version() uint64 { return m.version.Load() }

func (m *Service) Get(id string) (Automation, bool) {
	var a Automation
	ok, _ := m.st.GetJSON(store.BucketAutomations, id, &a)
	return a, ok
}

func (m *Service) Save(a Automation) (Automation, error) {
	// Refuse a DEFINITE feedback loop up front: a chain that writes a matching file back into the
	// watched folder re-triggers itself forever. Engine-level backstop for the wire/studio paths;
	// the webui editor refuses with a localized message before it ever reaches here.
	if r := CheckLoop(a, m.presets); r.Kind == LoopDefinite {
		return a, loopSaveError(r)
	}
	if a.ID == "" {
		a.ID = m.nextID("auto")
		a.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if err := m.st.PutJSON(store.BucketAutomations, a.ID, a); err != nil {
		return a, err
	}
	m.invalidateAutos()
	m.rearm()
	return a, nil
}

// Delete removes an automation AND cascades to every schedule pointing at it. Without the cascade
// the schedules outlive their target and the scheduler keeps firing them - onSchedule then skips
// each fire (the automation is gone), so they are invisible work forever. A schedule has no meaning
// without its automation: deleting one is deleting the other's trigger.
func (m *Service) Delete(id string) error {
	if err := m.st.Delete(store.BucketAutomations, id); err != nil {
		return err
	}
	var orphans []string
	for _, s := range m.ListSchedules() {
		if s.AutomationID == id {
			orphans = append(orphans, s.ID)
		}
	}
	for _, sid := range orphans {
		if err := m.st.Delete(store.BucketSchedules, sid); err != nil {
			// Report, but keep going: the automation is already gone, so every schedule left behind
			// is an orphan - delete as many as the store allows rather than stopping at the first.
			// The webui renders any survivor with an orphan warning + a working Delete.
			m.log.Warn(source, "schedule cascade delete failed", map[string]any{"scheduleId": sid, "error": err.Error()})
		}
	}
	m.invalidateAutos()
	if len(orphans) > 0 {
		m.invalidateScheds() // the schedule cache + Version() readers must see the cascade
	}
	m.rearm()
	return nil
}

// ── schedules CRUD ───────────────────────────────────────────────────────────

func (m *Service) ListSchedules() []Schedule {
	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()
	if !m.schedOK {
		raws, _ := m.st.ListJSON(store.BucketSchedules)
		out := make([]Schedule, 0, len(raws))
		for _, raw := range raws {
			var s Schedule
			if json.Unmarshal(raw, &s) == nil {
				out = append(out, s)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
		m.schedCache, m.schedOK = out, true
	}
	return append([]Schedule(nil), m.schedCache...) // defensive copy; master stays immutable
}

func (m *Service) SaveSchedule(s Schedule) (Schedule, error) {
	if s.ID == "" {
		s.ID = m.nextID("sched")
	}
	if err := m.st.PutJSON(store.BucketSchedules, s.ID, s); err != nil {
		return s, err
	}
	m.invalidateScheds()
	m.rearm()
	return s, nil
}

func (m *Service) DeleteSchedule(id string) error {
	if err := m.st.Delete(store.BucketSchedules, id); err != nil {
		return err
	}
	m.invalidateScheds()
	m.rearm()
	return nil
}

// ── runs ─────────────────────────────────────────────────────────────────────

// Runs returns recent runs newest-first (limit<=0 = all). NOTE: run keys are
// "<automationID>-<sha256hex>" (chainID) - NOT time-sortable - so a reverse bbolt cursor can't
// cheaply return the newest N; instead the full (capped ≤500 by pruneRuns) sorted history is
// cached and invalidated on every run-record/prune, bounding the 1Hz render path to a copy.
func (m *Service) Runs(limit int) []Run {
	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()
	if !m.runsOK {
		raws, _ := m.st.ListJSON(store.BucketRuns)
		out := make([]Run, 0, len(raws))
		for _, raw := range raws {
			var r Run
			if json.Unmarshal(raw, &r) == nil {
				out = append(out, r)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].StartedAt > out[j].StartedAt }) // newest first
		m.runsCache, m.runsOK = out, true
	}
	n := len(m.runsCache)
	if limit > 0 && limit < n {
		n = limit
	}
	return append([]Run(nil), m.runsCache[:n]...) // defensive copy; master stays immutable
}

// previewFileCap bounds the SweepPreview.Files listing (the UI shows a handful + "and N more").
// Total/TotalBytes still count EVERY match; only the returned per-file slice is capped.
const previewFileCap = 200

// Preview lists the files a sweep would act on now - the same match rules over the same watch dir
// a schedule fire uses (Run-now's rules-first mode renders this). Read-only; no run recorded.
func (m *Service) Preview(id string) (SweepPreview, error) {
	a, ok := m.Get(id)
	if !ok {
		return SweepPreview{}, fmt.Errorf("automation %q not found", id)
	}
	return m.previewOf(a), nil
}

// previewOf builds the bounded preview from the full match set (one ReadDir + stat per entry,
// shared with the sweep run path via sweepEntries - never a second eligibility implementation).
func (m *Service) previewOf(a Automation) SweepPreview {
	entries := m.sweepEntries(a)
	p := SweepPreview{AutomationID: a.ID, WatchDir: a.WatchDir, Match: a.Match, Total: len(entries)}
	for i := range entries {
		p.TotalBytes += entries[i].Size
	}
	if len(entries) > previewFileCap {
		entries = entries[:previewFileCap]
	}
	p.Files = entries
	return p
}

// RunSweep runs the automation over EVERY currently-matching file on demand - the DEFAULT Run-now,
// byte-for-byte the same work a schedule fire does (both call sweepRun via the coordinator),
// differing only in the trigger label. Coordinated: a sweep of an already-running automation is
// COALESCED; a sweep blocked by a path/heavy/streaming conflict is QUEUED (returns immediately with
// Queued=true, runs when the conflict clears); otherwise it runs and returns one Run per file.
func (m *Service) RunSweep(ctx context.Context, id string) (SweepResult, error) {
	a, ok := m.Get(id)
	if !ok {
		return SweepResult{}, fmt.Errorf("automation %q not found", id)
	}
	if m.coord == nil {
		return m.sweepRun(ctx, a, "manual"), nil
	}
	j := &coordJob{
		autoID: a.ID, label: a.Label, trigger: "manual", heavy: chainClass(a.Actions) == classHeavy,
		dirs: chainDirs(a), coalescable: true, ctx: ctx,
		run: func(rctx context.Context) []Run { return m.sweepRun(rctx, a, "manual").Runs },
	}
	res := m.coord.submit(j)
	switch {
	case res.coalesced:
		return SweepResult{AutomationID: a.ID, Trigger: "manual", Coalesced: true}, nil
	case res.queued:
		return SweepResult{AutomationID: a.ID, Trigger: "manual", Queued: true, QueueReason: res.reason}, nil
	default:
		<-res.done // started immediately: wait for completion + return the recorded runs
		return SweepResult{AutomationID: a.ID, Trigger: "manual", Runs: j.runs}, nil
	}
}

// RunManual runs the chain over one hand-picked file, bypassing the match rules (no eligible()
// check) - the SECONDARY Run-now path, trigger "manual-file". Coordinated too: it serializes behind
// a conflicting/overlapping run rather than racing it, but never coalesces (a specific file the user
// picked is not a duplicate of a sweep). Blocks until the run completes and returns its Run.
func (m *Service) RunManual(ctx context.Context, id, filePath string) (Run, error) {
	a, ok := m.Get(id)
	if !ok {
		return Run{}, fmt.Errorf("automation %q not found", id)
	}
	if m.coord == nil {
		return m.execute(ctx, a, filePath, "manual-file"), nil
	}
	var out Run
	j := &coordJob{
		autoID: a.ID, label: a.Label, trigger: "manual-file", file: filePath,
		heavy: chainClass(a.Actions) == classHeavy, dirs: chainDirs(a), ctx: ctx,
		run: func(rctx context.Context) []Run { out = m.execute(rctx, a, filePath, "manual-file"); return []Run{out} },
	}
	res := m.coord.submit(j)
	if res.done != nil {
		<-res.done // wait whether it started now or waited for a slot first
	}
	return out, nil
}

// CoordStatus snapshots what the coordinator is running + has queued (UI status region).
func (m *Service) CoordStatus() CoordStatus {
	if m.coord == nil {
		return CoordStatus{}
	}
	return m.coord.Status()
}

// CoordConflict reports whether a rules sweep of id would wait right now, and on what (Run-now
// conflict line). ok=false when the automation is unknown.
func (m *Service) CoordConflict(id string) (SweepConflict, bool) {
	a, ok := m.Get(id)
	if !ok || m.coord == nil {
		return SweepConflict{}, false
	}
	return m.coord.conflict(a), true
}

// execute runs the chain, persists the Run + the automation's last-run summary.
// engine bundles the run dependencies both chain paths share, so background/scheduled runs and
// interactive runs provably take their worker/presets/credentials from one place.
func (m *Service) engine() engine {
	return engine{st: m.st, w: m.w, presets: m.presets, creds: m.bgCreds}
}

func (m *Service) execute(ctx context.Context, a Automation, filePath, trigger string) Run {
	run := runChain(ctx, m.engine(), m.log, a, filePath, trigger)
	if run.ID == "" {
		run.ID = m.nextID("run")
	}
	_ = m.st.PutJSON(store.BucketRuns, run.ID, run)
	m.pruneRuns() // owns the runs-cache invalidation (fresh read + post-delete)

	a.LastRunAt = run.FinishedAt
	a.LastStatus = run.Status
	a.LastError = ""
	for _, s := range run.Steps {
		if !s.OK {
			a.LastError = s.Error
		}
	}
	_ = m.st.PutJSON(store.BucketAutomations, a.ID, a)
	m.invalidateAutos() // last-run summary changed
	return run
}

// pruneRuns caps run history (keep the newest 500). Sole owner of the runs-cache invalidation
// on a run-record: the leading invalidate forces m.Runs(0) to reflect the just-inserted run
// (a webui tick may have re-warmed a pre-insert cache); the trailing one covers the deletes.
func (m *Service) pruneRuns() {
	const keep = 500
	m.invalidateRuns()
	all := m.Runs(0)
	if len(all) <= keep {
		return // rebuilt cache already reflects the insert; no deletes → leave it valid
	}
	for _, r := range all[keep:] {
		_ = m.st.Delete(store.BucketRuns, r.ID)
	}
	m.invalidateRuns()
}

// ── triggers ─────────────────────────────────────────────────────────────────

// onWatchFile fires when a new file lands in a watched dir (debounced by the watcher). Runs through
// the coordinator so an arriving file serializes behind a conflicting run instead of racing it.
func (m *Service) onWatchFile(automationID, path string) {
	a, ok := m.Get(automationID)
	if !ok || !a.Enabled || !m.eligible(a, path) {
		return
	}
	// A stored definite loop: the arriving file is what the last run produced. Don't drive it (the
	// watcher is the loop's motor). Log-only - recording a Run per watch event would spam.
	if CheckLoop(a, m.presets).Kind == LoopDefinite {
		m.log.Warn(source, "watch skipped (feedback loop)", map[string]any{"automationId": a.ID, "file": path})
		return
	}
	if m.coord == nil {
		go func() {
			defer debuglog.Recover(nil, source, false) // nil bus: service decoupled via Logger iface
			m.execute(m.baseCtx(), a, path, "watch")
		}()
		return
	}
	m.coord.submit(&coordJob{
		autoID: a.ID, label: a.Label, trigger: "watch", file: path,
		heavy: chainClass(a.Actions) == classHeavy, dirs: chainDirs(a), ctx: m.baseCtx(),
		run: func(rctx context.Context) []Run { return []Run{m.execute(rctx, a, path, "watch")} },
	})
}

// onSchedule fires on a timer: sweep the automation's watch dir for eligible files.
func (m *Service) onSchedule(scheduleID string) {
	var s Schedule
	if ok, _ := m.st.GetJSON(store.BucketSchedules, scheduleID, &s); !ok || !s.Enabled {
		return
	}
	// The automation's own switch is the master one: "enabled off" means it does not run, whatever
	// started it - a timer no more than an arriving file (onWatchFile checks the same flag). Checked
	// at FIRE time rather than by disarming the timer, so the schedule's own switch stays the truth
	// about whether it is armed and re-enabling the automation resumes it with no re-save.
	a, ok := m.Get(s.AutomationID)
	if !ok || !a.Enabled {
		m.log.Info(source, "schedule skipped", map[string]any{
			"scheduleId": s.ID, "label": s.Label, "automationId": s.AutomationID,
			"reason": "automation not found or disabled",
		})
		return
	}
	// Recorded only for a fire that actually sweeps - same as a gate-blocked tick, which never
	// reaches this callback at all. "Last fired" naming a run that never happened is a lie.
	s.LastFiredAt = time.Now().UTC().Format(time.RFC3339)
	_ = m.st.PutJSON(store.BucketSchedules, s.ID, s)
	m.invalidateScheds() // LastFiredAt changed

	if m.coord == nil {
		m.sweepRun(m.baseCtx(), a, "schedule")
		return
	}
	// Fire-and-forget through the coordinator: a fire while the automation is still running is
	// coalesced into one pending sweep, and a path/heavy/streaming conflict queues it - the shared
	// scheduler eval loop is never blocked waiting on a run.
	m.coord.submit(&coordJob{
		autoID: a.ID, label: a.Label, trigger: "schedule", heavy: chainClass(a.Actions) == classHeavy,
		dirs: chainDirs(a), coalescable: true, ctx: m.baseCtx(),
		run: func(rctx context.Context) []Run { return m.sweepRun(rctx, a, "schedule").Runs },
	})
}

// sweepRun is the ONE sweep→execute loop schedule fires and manual Run-now sweeps both take, so
// the two can never diverge: list the watch dir's matching files, run the chain over each,
// recording one Run per file. trigger is the ONLY difference ("schedule" vs "manual"). Stops early
// if ctx is cancelled.
func (m *Service) sweepRun(ctx context.Context, a Automation, trigger string) SweepResult {
	// A stored definite loop (saved before this check existed) must not fire: it would sweep the
	// files it produced last time, forever. Record the skip so the reason is visible.
	if CheckLoop(a, m.presets).Kind == LoopDefinite {
		m.recordLoopSkip(a, trigger)
		return SweepResult{AutomationID: a.ID, Trigger: trigger}
	}
	res := SweepResult{AutomationID: a.ID, Trigger: trigger}
	for _, f := range m.sweep(a) {
		if ctx.Err() != nil {
			return res
		}
		res.Runs = append(res.Runs, m.execute(ctx, a, f, trigger))
	}
	return res
}

// sweep lists eligible files directly under the automation's watch dir (paths only). The single
// canonical listing is sweepEntries (one stat per file, feeding both the run and the preview).
func (m *Service) sweep(a Automation) []string {
	ents := m.sweepEntries(a)
	out := make([]string, 0, len(ents))
	for i := range ents {
		out = append(out, ents[i].Path)
	}
	return out
}

// sweepEntries is the canonical watch-dir match: ReadDir, stat each regular file once, keep those
// passing a.Match. It backs both the sweep run (sweep) and the preview (previewOf) so a sweep and
// its preview can never disagree about which files match. The stat also carries the size/mtime the
// preview renders - no second stat.
func (m *Service) sweepEntries(a Automation) []SweepFile {
	ents, err := os.ReadDir(a.WatchDir)
	if err != nil {
		return nil
	}
	var out []SweepFile
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		p := filepath.Join(a.WatchDir, e.Name())
		fi, err := os.Stat(p)
		if err != nil || fi.IsDir() {
			continue
		}
		if a.Match.matches(strings.ToLower(filepath.Ext(p)), filepath.Base(p), fi.Size(), fi.ModTime()) {
			out = append(out, SweepFile{Path: p, Name: e.Name(), Size: fi.Size(), ModTime: fi.ModTime()})
		}
	}
	return out
}

// eligible reports whether path passes the automation's Match (extension + min size + min age).
// The mtime comes from the stat this already does - no extra stat on the watch/sweep path.
func (m *Service) eligible(a Automation, path string) bool {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return false
	}
	return a.Match.matches(strings.ToLower(filepath.Ext(path)), filepath.Base(path), fi.Size(), fi.ModTime())
}

// loopSaveError is the engine-level (English) refusal for a definite feedback loop. The webui editor
// refuses first with a localized message; this backstops the wire/studio Save path.
func loopSaveError(r LoopReport) error {
	ext := r.Ext
	if ext == "" {
		ext = "the output"
	}
	return fmt.Errorf("automation would re-trigger itself: step %d (%s) writes %s into the watched "+
		"folder, where it matches the rule and starts another run - point that step at an output folder "+
		"outside the watched folder, or exclude %s from the match", r.Step, r.StepType, ext, ext)
}

// recordLoopSkip persists a single error Run + last-run summary so a skipped-loop fire is visible
// (the card shows the error and the reason).
func (m *Service) recordLoopSkip(a Automation, trigger string) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	run := Run{
		ID: chainID(a.ID, "", now), AutomationID: a.ID, Trigger: trigger,
		StartedAt: now, FinishedAt: now, Status: "error",
		Steps: []StepResult{{Error: "skipped: this automation writes back into its own watched folder " +
			"and would re-trigger itself (feedback loop) - fix it in the editor"}},
	}
	if run.ID == "" {
		run.ID = m.nextID("run")
	}
	_ = m.st.PutJSON(store.BucketRuns, run.ID, run)
	m.pruneRuns()
	a.LastRunAt = run.FinishedAt
	a.LastStatus = "error"
	a.LastError = run.Steps[0].Error
	_ = m.st.PutJSON(store.BucketAutomations, a.ID, a)
	m.invalidateAutos()
	m.log.Warn(source, "automation skipped (feedback loop)", map[string]any{"automationId": a.ID, "trigger": trigger})
}

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
	return &Service{st: st, w: w, presets: presets, log: log, bus: newEventBus(), active: map[string]*runContext{}}
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

// RunManual runs an automation over one file on demand.
func (m *Service) RunManual(ctx context.Context, id, filePath string) (Run, error) {
	a, ok := m.Get(id)
	if !ok {
		return Run{}, fmt.Errorf("automation %q not found", id)
	}
	return m.execute(ctx, a, filePath, "manual"), nil
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

// onWatchFile fires when a new file lands in a watched dir (debounced by the watcher).
func (m *Service) onWatchFile(automationID, path string) {
	a, ok := m.Get(automationID)
	if !ok || !a.Enabled || !m.eligible(a, path) {
		return
	}
	go func() {
		defer debuglog.Recover(nil, source, false) // nil bus: service decoupled via Logger iface
		m.execute(m.baseCtx(), a, path, "watch")
	}()
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

	ctx := m.baseCtx()
	for _, f := range m.sweep(a) {
		if ctx.Err() != nil {
			return
		}
		m.execute(ctx, a, f, "schedule")
	}
}

// sweep lists eligible files directly under the automation's watch dir.
func (m *Service) sweep(a Automation) []string {
	ents, err := os.ReadDir(a.WatchDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		p := filepath.Join(a.WatchDir, e.Name())
		if m.eligible(a, p) {
			out = append(out, p)
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

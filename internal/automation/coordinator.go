package automation

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/governor"
)

// The run coordinator serializes and prioritizes automation runs so schedules and manual runs
// behave the same AND stay good neighbours:
//
//   - an automation never overlaps ITSELF: a second sweep while one is running (or already pending)
//     is COALESCED into a single pending sweep, recorded, never silently multiplied;
//   - PATH conflicts serialize: two automations whose watch/output dirs are equal or nested never
//     run at once (the second waits, reason "path");
//   - RESOURCE classes: a chain with a heavy action (transcode/trim-silence → spawns ffmpeg) takes
//     one of MaxHeavyRuns heavy slots and also defers while the governor says background work is not
//     allowed (a live stream); light chains (move/copy/rename/delete) run alongside;
//   - the queue is BOUNDED (queueCap) and coalesced per automation, so it can never grow with load.
//
// Every run path funnels here: schedule sweep, watch-file, manual sweep (RunSweep), manual file
// (RunManual), and the attended interactive StartRun (registered so unattended runs defer to it,
// admitted immediately since a human is driving it).
//
// A tiny dispatcher goroutine owns the queue; jobs run on their own goroutines. All state changes
// bump the Service version so the ~1 Hz Automations tick repaints the status region + card badges.

// defaultMaxHeavyRuns is the default MaxHeavyRuns knob: at most one heavy (ffmpeg) run at a time.
const defaultMaxHeavyRuns = 1

// coordQueueCap bounds the pending queue (coalesced per automation, so this is a hard safety cap
// far above the realistic distinct-automation count, never a per-load growth point).
const coordQueueCap = 64

// reason codes for a queued/blocked run (the UI maps them to localized text). Exported aliases
// (Reason*) let the webui map a CoordQueued.Reason / SweepConflict.Reason to i18n text.
const (
	reasonCurrent   = "current"   // this automation's own current run must finish first
	reasonPath      = "path"      // a path-overlapping automation is running
	reasonHeavy     = "heavy"     // the heavy (transcode) slots are full
	reasonStreaming = "streaming" // heavy work deferred while a stream is live

	ReasonCurrent   = reasonCurrent
	ReasonPath      = reasonPath
	ReasonHeavy     = reasonHeavy
	ReasonStreaming = reasonStreaming
)

// runClass is a run's resource class, derived from its action types.
type runClass int

const (
	classLight runClass = iota // move/copy/rename/delete only
	classHeavy                 // contains transcode/trim-silence (spawns ffmpeg)
)

// chainClass reports whether a chain does heavy (ffmpeg-spawning) work.
func chainClass(acts []Action) runClass {
	for _, a := range acts {
		if a.Type == ActionTranscode || a.Type == ActionTrimSilence {
			return classHeavy
		}
	}
	return classLight
}

// chainDirs returns the dirs a run touches, for path-conflict detection: the watch dir plus any
// output/move/copy target dir the chain writes to.
func chainDirs(a Automation) []string {
	dirs := make([]string, 0, 1+len(a.Actions))
	if a.WatchDir != "" {
		dirs = append(dirs, a.WatchDir)
	}
	for _, act := range a.Actions {
		if act.OutputDir != "" {
			dirs = append(dirs, act.OutputDir)
		}
	}
	return dirs
}

// CoordRunning is one in-flight run in the coordinator status (UI status region).
type CoordRunning struct {
	AutomationID string `json:"automationId"`
	Label        string `json:"label"`
	File         string `json:"file"` // "" for a whole-dir sweep
	Trigger      string `json:"trigger"`
	Heavy        bool   `json:"heavy"`
	StartedAt    string `json:"startedAt"`
}

// CoordQueued is one waiting run and why (UI status region).
type CoordQueued struct {
	AutomationID string `json:"automationId"`
	Label        string `json:"label"`
	Trigger      string `json:"trigger"`
	Reason       string `json:"reason"`     // one of the reason* codes
	BlockLabel   string `json:"blockLabel"` // the automation it waits on (path/current)
	Coalesced    bool   `json:"coalesced"`
	CoalescedAt  string `json:"coalescedAt,omitempty"` // "15:04"
}

// CoordStatus is a snapshot of what the coordinator is running + has queued.
type CoordStatus struct {
	Running []CoordRunning `json:"running"`
	Queued  []CoordQueued  `json:"queued"`
}

// SweepConflict describes why a rules sweep of an automation would wait right now (Run-now modal
// conflict line). Blocked=false ⇒ it would start immediately.
type SweepConflict struct {
	Blocked    bool   `json:"blocked"`
	Reason     string `json:"reason"` // reason* code
	OtherLabel string `json:"otherLabel"`
	OtherFile  string `json:"otherFile"`
	OtherHeavy bool   `json:"otherHeavy"`
}

// coordJob is one unit of coordinated work.
type coordJob struct {
	autoID  string
	label   string
	trigger string
	file    string // "" for a sweep
	heavy   bool
	dirs    []string
	run     func(ctx context.Context) []Run // the actual work (sweepRun / execute)
	ctx     context.Context

	coalescable bool // true for sweeps (fold into one pending); false for single-file/interactive
	interactive bool // attended: admitted immediately, never queued

	startedAt   time.Time
	coalescedAt time.Time
	reason      string
	blockLabel  string

	done chan struct{}
	runs []Run
}

// submitResult is what submit tells the caller happened.
type submitResult struct {
	coalesced bool
	queued    bool
	reason    string
	job       *coordJob
	done      chan struct{}
}

type coordinator struct {
	mu          sync.Mutex
	running     []*coordJob
	waiting     []*coordJob // FIFO; ≤1 coalescable job per automation
	heavyActive int
	maxHeavy    int
	queueCap    int
	bgAllowed   func() bool
	bump        func()
	now         func() time.Time
	wakeCh      chan struct{}
	quit        chan struct{}
	stopped     bool
}

// newCoordinator builds a coordinator and starts its dispatcher goroutine. bump is called on every
// state change (nil = no-op). bgAllowed defaults to governor.BackgroundAllowed.
func newCoordinator(maxHeavy int, bump func()) *coordinator {
	if maxHeavy < 1 {
		maxHeavy = 1
	}
	if bump == nil {
		bump = func() {}
	}
	c := &coordinator{
		maxHeavy:  maxHeavy,
		queueCap:  coordQueueCap,
		bgAllowed: governor.BackgroundAllowed,
		bump:      bump,
		now:       time.Now,
		wakeCh:    make(chan struct{}, 1),
		quit:      make(chan struct{}),
	}
	go c.loop()
	return c
}

func (c *coordinator) stop() {
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return
	}
	c.stopped = true
	close(c.quit)
	// Release anyone blocked on a queued job's completion (RunManual): the run will never happen.
	for _, w := range c.waiting {
		if w.done != nil {
			close(w.done)
		}
	}
	c.waiting = nil
	c.mu.Unlock()
}

// wake nudges the dispatcher to re-evaluate the queue (e.g. the governor changed).
func (c *coordinator) wake() { c.nudge() }

func (c *coordinator) nudge() {
	select {
	case c.wakeCh <- struct{}{}:
	default:
	}
}

func (c *coordinator) bumpLocked() { c.bump() }

// loop is the dispatcher: on each nudge, admit as many waiting jobs as conditions now allow.
func (c *coordinator) loop() {
	for {
		select {
		case <-c.quit:
			return
		case <-c.wakeCh:
		}
		c.mu.Lock()
		c.dropCancelledLocked()
		for {
			j := c.pickAdmittableLocked()
			if j == nil {
				break
			}
			c.removeWaitingLocked(j)
			c.startLocked(j)
		}
		c.mu.Unlock()
	}
}

// dropCancelledLocked removes waiting jobs whose run context is already done (the caller cancelled
// - e.g. the window closed), releasing anyone blocked on them. Caller holds mu.
func (c *coordinator) dropCancelledLocked() {
	kept := c.waiting[:0]
	for _, w := range c.waiting {
		if w.ctx != nil && w.ctx.Err() != nil {
			if w.done != nil {
				close(w.done)
			}
			c.bumpLocked()
			continue
		}
		kept = append(kept, w)
	}
	c.waiting = kept
}

// submit places a job. Coalescable sweeps of the same automation fold into one pending run;
// otherwise the job starts now (no conflict) or waits (conflict), always returning immediately.
func (c *coordinator) submit(j *coordJob) submitResult {
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return submitResult{coalesced: true}
	}
	if j.coalescable {
		if w := c.pendingSweepOfLocked(j.autoID); w != nil {
			w.coalescedAt = c.now()
			c.bumpLocked()
			c.mu.Unlock()
			return submitResult{coalesced: true}
		}
		if c.runningOfLocked(j.autoID) != nil {
			j.coalescedAt = c.now() // becomes the single pending follow-up of the current run
		}
	}
	if len(c.waiting) >= c.queueCap {
		c.mu.Unlock()
		return submitResult{coalesced: true} // bounded: fold overflow rather than grow
	}
	j.done = make(chan struct{})
	if reason, ok := c.admittableLocked(j); ok {
		c.startLocked(j)
		c.mu.Unlock()
		return submitResult{queued: false, job: j, done: j.done}
	} else {
		j.reason = reason
		c.waiting = append(c.waiting, j)
		c.bumpLocked()
		c.mu.Unlock()
		c.nudge()
		return submitResult{queued: true, reason: reason, job: j, done: j.done}
	}
}

// trackInteractive registers an attended interactive run as running immediately (never queued), so
// unattended runs defer to it. The returned job is released with done().
func (c *coordinator) trackInteractive(a Automation, file string) *coordJob {
	j := &coordJob{
		autoID: a.ID, label: a.Label, trigger: "manual", file: file,
		heavy: chainClass(a.Actions) == classHeavy, dirs: chainDirs(a), interactive: true,
	}
	c.mu.Lock()
	c.startLocked(j)
	c.mu.Unlock()
	return j
}

// startLocked moves a job to running and, unless interactive (already invoked by its own caller),
// launches its work goroutine. Caller holds mu.
func (c *coordinator) startLocked(j *coordJob) {
	j.startedAt = c.now()
	j.reason, j.blockLabel = "", ""
	c.running = append(c.running, j)
	if j.heavy {
		c.heavyActive++
	}
	c.bumpLocked()
	if !j.interactive {
		go c.execute(j)
	}
}

// execute runs a job's work off the lock, then releases its slot and re-evaluates the queue.
func (c *coordinator) execute(j *coordJob) {
	var runs []Run
	if j.run != nil {
		runs = j.run(j.ctx)
	}
	c.finish(j, runs)
}

// finish releases j's running slot (used by execute AND by done() for interactive runs).
func (c *coordinator) finish(j *coordJob, runs []Run) {
	c.mu.Lock()
	j.runs = runs
	c.removeRunningLocked(j)
	if j.heavy {
		c.heavyActive--
	}
	c.bumpLocked()
	c.mu.Unlock()
	if j.done != nil {
		close(j.done)
	}
	c.nudge()
}

// done releases an interactive run's slot.
func (c *coordinator) done(j *coordJob) { c.finish(j, nil) }

// admittableLocked reports whether j may start now, else a reason code. Caller holds mu.
func (c *coordinator) admittableLocked(j *coordJob) (string, bool) {
	if r := c.runningOfLocked(j.autoID); r != nil {
		j.blockLabel = r.label
		return reasonCurrent, false
	}
	for _, r := range c.running {
		if dirsConflict(j.dirs, r.dirs) {
			j.blockLabel = r.label
			return reasonPath, false
		}
	}
	if j.heavy {
		if !c.bgAllowed() {
			return reasonStreaming, false
		}
		if c.heavyActive >= c.maxHeavy {
			// name a heavy run it waits behind, for the UI.
			for _, r := range c.running {
				if r.heavy {
					j.blockLabel = r.label
					break
				}
			}
			return reasonHeavy, false
		}
	}
	return "", true
}

// pickAdmittableLocked returns the first waiting job that can start now, refreshing the reasons of
// those that cannot. Caller holds mu.
func (c *coordinator) pickAdmittableLocked() *coordJob {
	for _, j := range c.waiting {
		if reason, ok := c.admittableLocked(j); ok {
			return j
		} else {
			j.reason = reason
		}
	}
	return nil
}

func (c *coordinator) runningOfLocked(autoID string) *coordJob {
	for _, r := range c.running {
		if r.autoID == autoID {
			return r
		}
	}
	return nil
}

func (c *coordinator) pendingSweepOfLocked(autoID string) *coordJob {
	for _, w := range c.waiting {
		if w.autoID == autoID && w.coalescable {
			return w
		}
	}
	return nil
}

func (c *coordinator) removeRunningLocked(j *coordJob) {
	for i, r := range c.running {
		if r == j {
			c.running = append(c.running[:i], c.running[i+1:]...)
			return
		}
	}
}

func (c *coordinator) removeWaitingLocked(j *coordJob) {
	for i, w := range c.waiting {
		if w == j {
			c.waiting = append(c.waiting[:i], c.waiting[i+1:]...)
			return
		}
	}
}

// Status snapshots running + queued for the UI.
func (c *coordinator) Status() CoordStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := CoordStatus{
		Running: make([]CoordRunning, 0, len(c.running)),
		Queued:  make([]CoordQueued, 0, len(c.waiting)),
	}
	for _, r := range c.running {
		st.Running = append(st.Running, CoordRunning{
			AutomationID: r.autoID, Label: r.label, File: baseName(r.file), Trigger: r.trigger,
			Heavy: r.heavy, StartedAt: r.startedAt.Format("15:04"),
		})
	}
	for _, w := range c.waiting {
		q := CoordQueued{AutomationID: w.autoID, Label: w.label, Trigger: w.trigger, Reason: w.reason, BlockLabel: w.blockLabel}
		if !w.coalescedAt.IsZero() {
			q.Coalesced, q.CoalescedAt = true, w.coalescedAt.Format("15:04")
		}
		st.Queued = append(st.Queued, q)
	}
	sort.SliceStable(st.Running, func(i, j int) bool { return st.Running[i].Label < st.Running[j].Label })
	sort.SliceStable(st.Queued, func(i, j int) bool { return st.Queued[i].Label < st.Queued[j].Label })
	return st
}

// conflict reports whether a rules sweep of a would wait now (Run-now modal), naming the blocker.
func (c *coordinator) conflict(a Automation) SweepConflict {
	probe := &coordJob{autoID: a.ID, heavy: chainClass(a.Actions) == classHeavy, dirs: chainDirs(a)}
	c.mu.Lock()
	defer c.mu.Unlock()
	reason, ok := c.admittableLocked(probe)
	if ok {
		return SweepConflict{}
	}
	sc := SweepConflict{Blocked: true, Reason: reason, OtherLabel: probe.blockLabel}
	if r := c.blockerLocked(probe, reason); r != nil {
		sc.OtherLabel, sc.OtherFile, sc.OtherHeavy = r.label, baseName(r.file), r.heavy
	}
	return sc
}

// blockerLocked returns the running job most responsible for reason (for the conflict message).
func (c *coordinator) blockerLocked(j *coordJob, reason string) *coordJob {
	switch reason {
	case reasonCurrent:
		return c.runningOfLocked(j.autoID)
	case reasonPath:
		for _, r := range c.running {
			if dirsConflict(j.dirs, r.dirs) {
				return r
			}
		}
	case reasonHeavy:
		for _, r := range c.running {
			if r.heavy {
				return r
			}
		}
	}
	return nil
}

// dirsConflict reports whether any dir in a is equal to or nested with any dir in b.
func dirsConflict(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if dirsOverlap(x, y) {
				return true
			}
		}
	}
	return false
}

// dirsOverlap reports whether x and y are the same dir or one is nested in the other (lexical;
// filepath.Rel is case-insensitive on Windows).
func dirsOverlap(x, y string) bool {
	x, y = filepath.Clean(x), filepath.Clean(y)
	if x == "." || y == "." {
		return false
	}
	return under(x, y) || under(y, x)
}

// under reports whether child == parent or child sits under parent.
func under(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func baseName(p string) string {
	if p == "" {
		return ""
	}
	return filepath.Base(p)
}

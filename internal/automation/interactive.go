package automation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/store"
)

// Interactive run engine - the studio "Automations" surface. Port of the Electron runChain
// (electron/src/main/ipc/automations.ts): emits a RunEvent per step, and in manual mode
// pauses each step with a proposal awaiting commit/skip/abort. background/once never pause.

type decision int

const (
	decCommit decision = iota
	decSkip
	decAbort
)

// runContext is one in-flight interactive run.
type runContext struct {
	runID string
	auto  Automation
	mode  RunMode
	// origPath is the chain's input file, fixed for the run's lifetime; currentPath is the
	// working file threaded through the steps. They diverge the moment a step relocates or
	// transcodes. delete resolves against origPath ONLY (buildDelete/commitStepSideEffects).
	origPath    string
	currentPath string
	// surv gates the delete the same way runChain's does: it may only erase origPath if a
	// producing step actually wrote a distinct file. A manual SKIP of the trim/transcode counts
	// as producing nothing - the human declining the step must not cost them the recording.
	// Touched only from runInteractive's own goroutine (buildStep/commit run inline there).
	surv    survivors
	aborted atomic.Bool

	mu      sync.Mutex
	pending chan decision // non-nil only while a manual step awaits a decision
}

func newRunID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "run-" + hex.EncodeToString(b[:])
}

// StartRun launches an async run over filePath; returns its runId immediately.
func (m *Service) StartRun(mode RunMode, id, filePath string) (string, error) {
	a, ok := m.Get(id)
	if !ok {
		return "", fmt.Errorf("automation %q not found", id)
	}
	if _, err := os.Stat(filePath); err != nil {
		return "", fmt.Errorf("file not found: %s", filePath)
	}
	rc := &runContext{runID: newRunID(), auto: a, mode: mode, origPath: filePath, currentPath: filePath}
	m.runsMu.Lock()
	if m.active == nil {
		m.active = map[string]*runContext{}
	}
	m.active[rc.runID] = rc
	m.runsMu.Unlock()
	go func() {
		defer debuglog.Recover(nil, source, false) // nil bus: service decoupled via Logger iface
		m.runInteractive(m.baseCtx(), rc, string(mode))
	}()
	return rc.runID, nil
}

func (m *Service) getRun(runID string) (*runContext, bool) {
	m.runsMu.Lock()
	defer m.runsMu.Unlock()
	rc, ok := m.active[runID]
	return rc, ok
}

// decide delivers a commit/skip/abort to an awaiting manual step.
func (m *Service) decide(runID string, d decision) error {
	rc, ok := m.getRun(runID)
	if !ok {
		return fmt.Errorf("run %q not active", runID)
	}
	if d == decAbort {
		rc.aborted.Store(true)
	}
	rc.mu.Lock()
	ch := rc.pending
	rc.mu.Unlock()
	if ch == nil {
		return fmt.Errorf("run %q not awaiting a step", runID)
	}
	select {
	case ch <- d:
	default:
	}
	return nil
}

func (m *Service) CommitStep(runID string) error { return m.decide(runID, decCommit) }
func (m *Service) SkipStep(runID string) error   { return m.decide(runID, decSkip) }
func (m *Service) AbortRun(runID string) error   { return m.decide(runID, decAbort) }

// emit broadcasts one run event (fills the run-scoped fields).
func (m *Service) emit(rc *runContext, ev RunEvent) {
	ev.AutomationID = rc.auto.ID
	ev.RunID = rc.runID
	ev.FilePath = rc.currentPath
	ev.TotalSteps = len(rc.auto.Actions)
	m.bus.emit(ev)
}

// runInteractive executes the action chain with per-step events + manual gating.
func (m *Service) runInteractive(ctx context.Context, rc *runContext, trigger string) {
	defer func() {
		m.runsMu.Lock()
		delete(m.active, rc.runID)
		m.runsMu.Unlock()
	}()

	started := time.Now().UTC().Format(time.RFC3339Nano)
	run := Run{ID: rc.runID, AutomationID: rc.auto.ID, Trigger: trigger, FilePath: rc.origPath, StartedAt: started, Status: "running"}
	m.emit(rc, RunEvent{Step: -1, State: StateStarting, Message: "Run started for " + filepath.Base(rc.origPath)})

	status := "success"
	var runErr string

	for i, act := range rc.auto.Actions {
		if rc.aborted.Load() || ctx.Err() != nil {
			m.emit(rc, RunEvent{Step: i, State: StateAborted, Message: "Run aborted."})
			run.Steps = append(run.Steps, StepResult{Type: act.Type, Error: "aborted"})
			status = "partial"
			break
		}
		m.emit(rc, RunEvent{Step: i, State: StateRunning, ActionType: act.Type, Message: "Running " + string(act.Type)})

		proposal, proposed, skipMsg, err := m.buildStep(ctx, rc, act)
		if err != nil {
			m.emit(rc, RunEvent{Step: i, State: StateError, ActionType: act.Type, Message: err.Error()})
			run.Steps = append(run.Steps, StepResult{Type: act.Type, Error: err.Error()})
			status, runErr = "error", err.Error()
			break
		}
		if skipMsg != "" {
			rc.surv.step(act, true, skipMsg)
			m.emit(rc, RunEvent{Step: i, State: StateSkipped, ActionType: act.Type, Message: skipMsg})
			run.Steps = append(run.Steps, StepResult{Type: act.Type, OK: true, OutputPath: rc.currentPath})
			if status == "success" {
				status = "partial"
			}
			continue
		}

		if rc.mode == ModeManual && proposal != nil {
			// Arm the pending-decision channel BEFORE announcing awaiting: a client reacting
			// instantly to the StateAwaiting event must not commit before we're listening, else
			// decide() sees a nil rc.pending ("not awaiting a step"). The channel is buffered so a
			// commit landing between arm and the select below is still delivered.
			ch := m.arm(rc)
			m.emit(rc, RunEvent{Step: i, State: StateAwaiting, ActionType: act.Type, AwaitingConfirmation: true, Proposal: proposal, OutputPath: proposed})
			switch m.waitDecision(ctx, rc, ch) {
			case decAbort:
				rc.aborted.Store(true)
				m.emit(rc, RunEvent{Step: i, State: StateAborted, ActionType: act.Type, Message: "User aborted."})
				run.Steps = append(run.Steps, StepResult{Type: act.Type, Error: "aborted"})
				status = "partial"
				goto done
			case decSkip:
				// Declining the step produced nothing - a later delete must see that.
				rc.surv.step(act, true, "The step that would have produced a new file was skipped.")
				m.emit(rc, RunEvent{Step: i, State: StateSkipped, ActionType: act.Type, Message: "User skipped step."})
				run.Steps = append(run.Steps, StepResult{Type: act.Type, OK: true, OutputPath: rc.currentPath})
				if status == "success" {
					status = "partial"
				}
				continue
			}
		}

		out, err := m.commitStepSideEffects(ctx, rc, act, proposal, proposed)
		if err != nil {
			m.emit(rc, RunEvent{Step: i, State: StateError, ActionType: act.Type, Message: err.Error()})
			run.Steps = append(run.Steps, StepResult{Type: act.Type, Error: err.Error()})
			status, runErr = "error", err.Error()
			break
		}
		rc.surv.step(act, false, "")
		if act.Type == ActionDelete {
			// Delete is terminal: it consumed the chain's INPUT, so any trailing step would run
			// against a file whose provenance is now ambiguous - report them skipped instead.
			// (ValidateActions rejects trailing steps up front; this covers chains persisted
			// before that check.) The message names origPath - the file actually erased - and
			// rc.currentPath when it differs, since that one SURVIVES (a transcode/copy output).
			run.Steps = append(run.Steps, StepResult{Type: act.Type, OK: true}) // no output path
			msg := "Deleted " + filepath.Base(rc.origPath)
			if rc.currentPath != "" && rc.currentPath != rc.origPath {
				msg += " - " + filepath.Base(rc.currentPath) + " remains."
			}
			m.emit(rc, RunEvent{Step: i, State: StateCompleted, ActionType: act.Type, Message: msg})
			for j := i + 1; j < len(rc.auto.Actions); j++ {
				t := rc.auto.Actions[j].Type
				m.emit(rc, RunEvent{Step: j, State: StateSkipped, ActionType: t,
					Message: "Skipped: an earlier delete step consumed the chain's input file."})
				run.Steps = append(run.Steps, StepResult{Type: t, OK: true})
				status = "partial"
			}
			break
		}
		rc.currentPath = out
		run.Steps = append(run.Steps, StepResult{Type: act.Type, OK: true, OutputPath: out})
		m.emit(rc, RunEvent{Step: i, State: StateCompleted, ActionType: act.Type, OutputPath: out,
			Message: fmt.Sprintf("Step %d/%d done.", i+1, len(rc.auto.Actions))})
	}

done:
	terminal := StateCompleted
	if status == "error" {
		terminal = StateError
	}
	// Persist BEFORE the terminal event: consumers react to completed/error by reading Runs() -
	// emitting first raced them against the store write (flaked TestStartRunOnceEmitsEvents on CI).
	run.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	run.Status = status
	m.persistRun(rc.auto, run, runErr)
	m.emit(rc, RunEvent{Step: len(rc.auto.Actions), State: terminal, Message: orStr(runErr, "Run finished."), OutputPath: rc.currentPath})
}

// arm registers a fresh buffered pending-decision channel and returns it. Called BEFORE the
// StateAwaiting event is emitted so decide() always sees a non-nil rc.pending.
func (m *Service) arm(rc *runContext) chan decision {
	ch := make(chan decision, 1)
	rc.mu.Lock()
	rc.pending = ch
	rc.mu.Unlock()
	return ch
}

// waitDecision blocks on the pre-armed channel until commit/skip/abort (or ctx cancel → abort),
// then clears rc.pending.
func (m *Service) waitDecision(ctx context.Context, rc *runContext, ch chan decision) decision {
	defer func() {
		rc.mu.Lock()
		rc.pending = nil
		rc.mu.Unlock()
	}()
	select {
	case d := <-ch:
		return d
	case <-ctx.Done():
		return decAbort
	}
}

// persistRun writes the Run to history and updates the automation's last-run summary.
func (m *Service) persistRun(a Automation, run Run, runErr string) {
	if run.ID == "" {
		run.ID = m.nextID("run")
	}
	_ = m.st.PutJSON(store.BucketRuns, run.ID, run)
	m.pruneRuns() // owns the runs-cache invalidation (fresh read + post-delete)
	a.LastRunAt = run.FinishedAt
	a.LastStatus = run.Status
	a.LastError = runErr
	_ = m.st.PutJSON(store.BucketAutomations, a.ID, a)
	m.invalidateAutos() // last-run summary changed
}

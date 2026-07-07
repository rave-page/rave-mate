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
	runID       string
	auto        Automation
	mode        RunMode
	currentPath string
	aborted     atomic.Bool

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
	rc := &runContext{runID: newRunID(), auto: a, mode: mode, currentPath: filePath}
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
	run := Run{ID: rc.runID, AutomationID: rc.auto.ID, Trigger: trigger, FilePath: rc.currentPath, StartedAt: started, Status: "running"}
	m.emit(rc, RunEvent{Step: -1, State: StateStarting, Message: "Run started for " + filepath.Base(rc.currentPath)})

	status := "success"
	var runErr string

	// Look-ahead: if a later step moves the file, earlier outputs land there directly.
	moveStep, moveDir := -1, ""
	for i, a := range rc.auto.Actions {
		if a.Type == ActionMove && a.OutputDir != "" {
			moveStep, moveDir = i, a.OutputDir
			break
		}
	}

	for i, act := range rc.auto.Actions {
		if rc.aborted.Load() || ctx.Err() != nil {
			m.emit(rc, RunEvent{Step: i, State: StateAborted, Message: "Run aborted."})
			run.Steps = append(run.Steps, StepResult{Type: act.Type, Error: "aborted"})
			status = "partial"
			break
		}
		m.emit(rc, RunEvent{Step: i, State: StateRunning, ActionType: act.Type, Message: "Running " + string(act.Type)})

		hintDir := ""
		if moveStep > i {
			hintDir = moveDir
		}
		proposal, proposed, skipMsg, err := m.buildStep(ctx, rc, act, hintDir)
		if err != nil {
			m.emit(rc, RunEvent{Step: i, State: StateError, ActionType: act.Type, Message: err.Error()})
			run.Steps = append(run.Steps, StepResult{Type: act.Type, Error: err.Error()})
			status, runErr = "error", err.Error()
			break
		}
		if skipMsg != "" {
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
	m.pruneRuns()
	a.LastRunAt = run.FinishedAt
	a.LastStatus = run.Status
	a.LastError = runErr
	_ = m.st.PutJSON(store.BucketAutomations, a.ID, a)
}

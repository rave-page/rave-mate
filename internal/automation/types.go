// Package automation is rave-mate's local media-automation engine: file-arrival watchers
// and time schedules that run an ordered action chain (trim-silence → transcode → move/copy)
// over media files, unattended, in the daemon. Modeled on the Electron client's Automations
// (electron/src/main/ipc/automations.ts) minus the rave.page event-rename step. All state is
// persisted in the bbolt store; runs are recorded as history.
package automation

import (
	"context"
	"encoding/json"
	"regexp"
	"slices"

	"rave.page/mate/internal/transcode"
	"rave.page/mate/internal/worker"
)

// ActionType enumerates the chain steps an automation can perform.
type ActionType string

const (
	ActionRename      ActionType = "rename-from-event" // rename from matched booked event
	ActionTrimSilence ActionType = "trim-silence"      // detect leading silence + transcode trimmed
	ActionTranscode   ActionType = "transcode"         // run a preset
	ActionMove        ActionType = "move-to"           // move the (current) file into OutputDir
	ActionCopy        ActionType = "copy-to"           // copy the (current) file into OutputDir
)

// Action is one step. Fields are interpreted per Type (others ignored).
type Action struct {
	Type        ActionType `json:"type"`
	PresetID    string     `json:"presetId,omitempty"`    // transcode / trim-silence
	OutputDir   string     `json:"outputDir,omitempty"`   // transcode out dir (default: alongside) + move/copy dest
	ThresholdDb float64    `json:"thresholdDb,omitempty"` // trim-silence (default -50)
	// trim-silence
	MinSilenceSeconds float64 `json:"minSilenceSeconds,omitempty"` // default 2
	TrimStart         *bool   `json:"trimStart,omitempty"`         // default true
	TrimEnd           *bool   `json:"trimEnd,omitempty"`           // default true
	// rename-from-event
	BufferMinutes int    `json:"bufferMinutes,omitempty"` // default 180
	Template      string `json:"template,omitempty"`      // {YYYY-MM-DD}/{venueSlug}/{eventSlug}/...
}

// Match decides whether a file is eligible for an automation.
type Match struct {
	Extensions      []string `json:"extensions,omitempty"` // lower-case, dot-prefixed e.g. ".wav"; empty = any
	MinSizeBytes    int64    `json:"minSizeBytes,omitempty"`
	FilenamePattern string   `json:"filenamePattern,omitempty"` // regex over basename; empty = any
}

// Automation = a watched directory + match rules + an ordered action chain.
type Automation struct {
	ID         string   `json:"id"`
	Label      string   `json:"label"`
	WatchDir   string   `json:"watchDir"`
	Enabled    bool     `json:"enabled"`
	Match      Match    `json:"match"`
	Actions    []Action `json:"actions"`
	CreatedAt  string   `json:"createdAt"`
	LastRunAt  string   `json:"lastRunAt,omitempty"`
	LastStatus string   `json:"lastStatus,omitempty"` // success|error|partial
	LastError  string   `json:"lastError,omitempty"`
}

// ScheduleKind is how a Schedule recurs.
type ScheduleKind string

const (
	ScheduleInterval ScheduleKind = "interval" // every IntervalMinutes
	ScheduleDaily    ScheduleKind = "daily"    // at AtHour:AtMinute local time
	ScheduleCron     ScheduleKind = "cron"     // 5-field cron expr (min hour dom month dow)
	ScheduleIdle     ScheduleKind = "idle"     // when the system has been idle ≥ IdleMinutes
)

// Schedule periodically re-runs an automation over the current contents of its WatchDir
// (so a folder gets swept on a timer, not only on file-arrival). The trigger is one of the
// ScheduleKind variants; the Require*/Exclude* fields are gates that apply to ANY kind - a tick
// only fires the run when every gate passes (idle/process gating is Windows-only today).
type Schedule struct {
	ID              string       `json:"id"`
	Label           string       `json:"label"`
	Enabled         bool         `json:"enabled"`
	AutomationID    string       `json:"automationId"`
	Kind            ScheduleKind `json:"kind"`
	IntervalMinutes int          `json:"intervalMinutes,omitempty"`
	AtHour          int          `json:"atHour,omitempty"`
	AtMinute        int          `json:"atMinute,omitempty"`
	CronExpr        string       `json:"cronExpr,omitempty"`    // ScheduleCron: "*/15 * * * *"
	IdleMinutes     int          `json:"idleMinutes,omitempty"` // ScheduleIdle: idle threshold to fire

	// Gates (any kind). Empty = no gate.
	RequireIdleMinutes int      `json:"requireIdleMinutes,omitempty"` // only fire if idle ≥ this
	RequireAppsRunning []string `json:"requireAppsRunning,omitempty"` // only fire if ALL are running
	ExcludeAppsRunning []string `json:"excludeAppsRunning,omitempty"` // skip if ANY is running

	LastFiredAt string `json:"lastFiredAt,omitempty"`
}

// StepResult records one action's outcome within a run.
type StepResult struct {
	Type       ActionType `json:"type"`
	OK         bool       `json:"ok"`
	OutputPath string     `json:"outputPath,omitempty"`
	Error      string     `json:"error,omitempty"`
}

// Run is one automation execution over one file (history, persisted).
type Run struct {
	ID           string       `json:"id"`
	AutomationID string       `json:"automationId"`
	Trigger      string       `json:"trigger"` // watch|schedule|manual
	FilePath     string       `json:"filePath"`
	StartedAt    string       `json:"startedAt"`
	FinishedAt   string       `json:"finishedAt,omitempty"`
	Status       string       `json:"status"` // running|success|error|partial
	Steps        []StepResult `json:"steps,omitempty"`
}

// Worker runs media jobs on the worker subprocess pool (*worker.Supervisor satisfies it).
type Worker interface {
	RunStream(ctx context.Context, typ, method string, params any, onProgress worker.ProgressFunc) (json.RawMessage, error)
}

// PresetResolver resolves a preset id → preset (builtins + the user's custom presets).
type PresetResolver func(id string) (transcode.Preset, bool)

// Logger is the subset of logbus the engine uses (decoupled for testing).
type Logger interface {
	Info(source, msg string, fields map[string]any)
	Warn(source, msg string, fields map[string]any)
}

// Manager is the facade the UI + studio channel bind to (concrete impl: *Service).
type Manager interface {
	List() []Automation
	Get(id string) (Automation, bool)
	Save(a Automation) (Automation, error) // empty ID → create (generates id+createdAt); else update
	Delete(id string) error
	RunManual(ctx context.Context, id, filePath string) (Run, error)
	Runs(limit int) []Run // recent runs across all automations, newest first
	ListSchedules() []Schedule
	SaveSchedule(s Schedule) (Schedule, error)
	DeleteSchedule(id string) error
	Version() uint64 // monotonic; bumps on any automations/schedules/runs change (webui tick gate)

	// Interactive run engine (the studio "Automations" surface, parity w/ Electron).
	OnEvent(fn func(RunEvent)) func()                           // subscribe to run events; returns unsub
	StartRun(mode RunMode, id, filePath string) (string, error) // async; returns runId. mode once|manual
	CommitStep(runID string) error                              // confirm the awaiting manual step
	SkipStep(runID string) error                                // skip the awaiting manual step
	AbortRun(runID string) error                                // abort the run
	ProbeSilence(ctx context.Context, path string, thresholdDb, minSilence float64) (SilenceResult, error)
	ListEvents(ctx context.Context, mtimeISO string, bufferMinutes int) ([]MatchedEvent, error)
	SetBackgroundCredentials(apiBaseURL, token string) // "" clears
}

// RunMode selects run behavior. background fires from a watcher; once runs without
// confirmation prompts; manual pauses each step with a proposal awaiting commit/skip/abort.
type RunMode string

const (
	ModeBackground RunMode = "background"
	ModeOnce       RunMode = "once"
	ModeManual     RunMode = "manual"
)

// RunState is a run/step lifecycle state (web AutomationRunState).
type RunState string

const (
	StateStarting  RunState = "starting"
	StateRunning   RunState = "running"
	StateCompleted RunState = "completed"
	StateError     RunState = "error"
	StateSkipped   RunState = "skipped"
	StateAwaiting  RunState = "awaiting-confirmation"
	StateAborted   RunState = "aborted"
)

// RunEvent is one progress notification (byte-shape = web AutomationRunEvent).
type RunEvent struct {
	AutomationID         string     `json:"automationId"`
	RunID                string     `json:"runId"`
	FilePath             string     `json:"filePath"`
	Step                 int        `json:"step"`
	TotalSteps           int        `json:"totalSteps"`
	State                RunState   `json:"state"`
	ActionType           ActionType `json:"actionType,omitempty"`
	Message              string     `json:"message,omitempty"`
	OutputPath           string     `json:"outputPath,omitempty"`
	Proposal             any        `json:"proposal,omitempty"` // web AutomationStepProposal
	AwaitingConfirmation bool       `json:"awaitingConfirmation,omitempty"`
}

// MatchedEvent is a booked event matched to a recording (web AutomationMatchedEvent).
type MatchedEvent struct {
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	StartsAt  *string `json:"startsAt"`
	EndsAt    *string `json:"endsAt"`
	VenueName *string `json:"venueName"`
	Slug      *string `json:"slug,omitempty"`
}

// SilenceRegion is one detected silence span (web SilenceProbeResult.regions[]).
type SilenceRegion struct {
	StartSeconds    float64 `json:"startSeconds"`
	EndSeconds      float64 `json:"endSeconds"`
	DurationSeconds float64 `json:"durationSeconds"`
}

// SilenceResult is the leading/trailing silence probe (web SilenceProbeResult).
type SilenceResult struct {
	LeadingSilenceSeconds  float64         `json:"leadingSilenceSeconds"`
	TrailingSilenceSeconds float64         `json:"trailingSilenceSeconds"`
	DurationSeconds        *float64        `json:"durationSeconds"`
	Regions                []SilenceRegion `json:"regions"`
}

// Action-chain defaults (parity with electron/src/shared/automationTypes.ts).
const (
	defaultBufferMinutes  = 180
	defaultMinSilenceSecs = 2.0
	defaultThresholdDb    = -50.0
	defaultRenameTemplate = "{YYYY-MM-DD}_{venueSlug}_{eventSlug}{ext}"
	// noEventSlug fills {venueSlug}/{eventSlug} when no booking matched the recording's
	// timestamp, so the rename still proceeds instead of being skipped.
	noEventSlug = "no-event"
)

const source = "automation"

// matches reports whether a file (ext lower-case dot-prefixed, base name, size bytes) is
// eligible under m. An invalid filenamePattern regex fails closed (no match).
func (m Match) matches(ext, base string, size int64) bool {
	if m.MinSizeBytes > 0 && size < m.MinSizeBytes {
		return false
	}
	if len(m.Extensions) > 0 && !slices.Contains(m.Extensions, ext) {
		return false
	}
	if m.FilenamePattern != "" {
		re, err := regexp.Compile(m.FilenamePattern)
		if err != nil || !re.MatchString(base) {
			return false
		}
	}
	return true
}

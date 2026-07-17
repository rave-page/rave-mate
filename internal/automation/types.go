// Package automation is rave-mate's local media-automation engine: file-arrival watchers
// and time schedules that run an ordered action chain (trim-silence → transcode → move/copy)
// over media files, unattended, in the daemon. Modeled on the Electron client's Automations
// (electron/src/main/ipc/automations.ts) minus the rave.page event-rename step. All state is
// persisted in the bbolt store; runs are recorded as history.
package automation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"

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
	ActionDelete      ActionType = "delete"            // delete the chain's ORIGINAL input file - TERMINAL, see ValidateActions
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
	// transcode loudness override (applyActionLoudness). LoudnessOn=true REPLACES the resolved
	// preset's loudness settings; false = leave the preset's own loudness untouched (so chains
	// saved before these fields existed behave identically). There is deliberately no "force
	// off": to skip normalization, point PresetID at a preset that doesn't normalize.
	LoudnessOn        bool    `json:"loudnessOn,omitempty"`
	LoudnessI         float64 `json:"loudnessI,omitempty"`         // integrated target (LUFS); 0 = defaultLoudnessI
	LoudnessTP        float64 `json:"loudnessTP,omitempty"`        // true-peak ceiling (dBTP); 0 = transcode.DefaultLoudnessTP
	LoudnessRaiseOnly bool    `json:"loudnessRaiseOnly,omitempty"` // never turn an already-loud track down
}

// Match decides whether a file is eligible for an automation.
type Match struct {
	Extensions      []string `json:"extensions,omitempty"` // lower-case, dot-prefixed e.g. ".wav"; empty = any
	MinSizeBytes    int64    `json:"minSizeBytes,omitempty"`
	FilenamePattern string   `json:"filenamePattern,omitempty"` // regex over basename; empty = any
	// MinAgeDays gates on the file's mtime: it must be at least this old. 0 = off. Meant for
	// scheduled sweeps ("archive/delete recordings older than 30 days") - a watch trigger fires
	// on a fresh file, which by definition never passes a >0 age gate.
	MinAgeDays int `json:"minAgeDays,omitempty"`
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
	AutomationID string `json:"automationId"`
	RunID        string `json:"runId"`
	// FilePath is the run's WORKING file, which moves as steps relocate/transcode it. It is NOT
	// what a delete step erases - render Proposal.currentPath for a delete gate, never this.
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
	// trimPresetID backs a trim-silence step with no preset of its own: cutting needs a re-encode,
	// and remux cuts without touching the audio (-c copy), so nothing is lost by default.
	trimPresetID = "remux"
	// noEventSlug fills {venueSlug}/{eventSlug} when no booking matched the recording's
	// timestamp, so the rename still proceeds instead of being skipped.
	noEventSlug = "no-event"
	// defaultLoudnessI backs Action.LoudnessI == 0. Aliases the encoder's own default so the step
	// proposal can't drift from what the worker will really apply.
	defaultLoudnessI = transcode.DefaultLoudnessI
)

const source = "automation"

// ValidateActions checks an action chain the engine can actually run; UI/wire layers should
// call it before Save. The engine independently fails closed at run time (delete-is-terminal;
// delete stats its target; delete refuses unless a distinct artifact survived it - survivors),
// since chains persisted before these checks existed can still carry trailing steps or a move
// ahead of a delete, and since a step that VALIDATES as producing can still skip at run time.
func ValidateActions(acts []Action) error {
	if len(acts) == 0 {
		return errors.New("automation must have at least one action")
	}
	for i, a := range acts {
		switch a.Type {
		case ActionRename, ActionTrimSilence:
		case ActionTranscode:
			if a.PresetID == "" {
				return errors.New("transcode action requires a preset")
			}
		case ActionMove, ActionCopy:
			if a.OutputDir == "" {
				return fmt.Errorf("%s action requires an output directory", a.Type)
			}
		case ActionDelete:
			// Terminal: delete consumes the chain's INPUT file (not its working file - a
			// transcode/copy output survives), so any later step would act on a file whose
			// provenance is now ambiguous.
			if i != len(acts)-1 {
				return errors.New("delete must be the last action in the chain")
			}
			// Delete may only erase the original if a distinct artifact actually survives it.
			// The structural half of that: reject iff the ORIGINAL itself gets relocated first,
			// which leaves the delete nothing at the path it resolves AND nothing in its place.
			// Whether a move-to does that depends on WHERE in the chain it sits, not on its type -
			// see relocatesOriginal.
			if j, bad := relocatesOriginal(acts[:i]); bad {
				return fmt.Errorf("delete cannot follow a %s (step %d): at that point the chain is still working on the recording it started from, so that step relocates the recording ITSELF - and delete erases exactly that original, which would no longer be there, with nothing produced to take its place. Either produce a new file first (transcode or trim-silence), so the move relocates that output and delete clears the source behind it, or use copy-to instead of move-to - a copy leaves the original in place for delete to clear", acts[j].Type, j+1)
			}
		default:
			return fmt.Errorf("unknown automation action: %s", a.Type)
		}
	}
	return nil
}

// relocatesOriginal walks a delete's prefix tracking what the working file IS, and reports the
// index of the first step that relocates the chain's original. Step type alone cannot answer
// this and pattern-matching on it was wrong: the same move-to relocates the ORIGINAL as step 1
// (fatal - delete would erase nothing and leave nothing) but relocates a transcode's OUTPUT
// after a producing step (harmless - the original stayed put, which is what delete wants).
//
// The model each step is scored against, in terms of the working file ("current"):
//   - PRODUCING (transcode, trim-silence): writes a NEW file; original stays; current moves off it.
//   - RELOCATING (move-to, rename-from-event): moves whatever current points at.
//   - COPYING (copy-to): writes a copy elsewhere; current STAYS on the original.
//
// So [transcode, move, delete] and [copy, delete] pass; [move, delete], [rename, delete] and
// [copy, move, delete] don't.
func relocatesOriginal(prefix []Action) (int, bool) {
	onOriginal := true // current == the chain's input until a step produces a new file
	for i, a := range prefix {
		switch a.Type {
		case ActionTranscode, ActionTrimSilence:
			onOriginal = false
		case ActionMove, ActionRename:
			if onOriginal {
				return i, true
			}
		}
	}
	return 0, false
}

// supersedesOriginal reports whether a step, when it runs, stops the original from being the sole
// copy - either by writing a distinct new file (transcode/trim-silence) or by relocating the
// original itself (rename-from-event). The run-time survivor gate counts these: if one is present
// but SKIPS (no silence to cut; no matched event/credentials to rename from), the original is
// still the only copy and a following delete would erase it. Validate cannot see a skip - only run
// time can. copy/move are excluded: copy leaves the original in place as the surviving source and
// never skips; move always relocates the original, so a delete after it fails closed on the
// vanished path.
func supersedesOriginal(a Action) bool {
	return a.Type == ActionTranscode || a.Type == ActionTrimSilence || a.Type == ActionRename
}

// ValidateLoudness rejects a loudness override on a step whose preset cannot normalize.
// Normalizing rewrites the samples, so a copy/none audio codec drops it (transcode.LoudnessAppliesTo
// / NormalizePreset) - the target would be stored, handed back on the next read, and silently do
// nothing forever.
//
// Split from ValidateActions because it needs preset resolution, which the structural rules do not.
// presets == nil ⇒ skip: a surface that cannot resolve must not guess. EVERY surface that can
// resolve calls this, so a chain one accepts they all accept - the webui editor and the studio wire
// disagreeing would make an automation authored in one un-editable from the other.
func ValidateLoudness(acts []Action, presets PresetResolver) error {
	if presets == nil {
		return nil
	}
	for i, a := range acts {
		if !a.LoudnessOn {
			continue
		}
		dflt := ""
		switch a.Type {
		case ActionTrimSilence:
			dflt = trimPresetID
		case ActionTranscode:
		default:
			continue // no other step transcodes, so no other step reads the loudness fields
		}
		// resolvePreset applies the override then NormalizePreset - the exact coercion the worker
		// performs - so this reads the codec the encode will really use, not the preset as written.
		p, ok := resolvePreset(presets, a, dflt)
		if !ok {
			continue // unknown preset: ValidateActions + the engine own that verdict, not this one
		}
		if !transcode.LoudnessAppliesTo(p.AudioCodec) {
			return fmt.Errorf("step %d (%s): the %q preset copies the audio instead of re-encoding it, and loudness normalization can only run on a re-encode - so this target would be saved and then silently ignored on every run. Pick a preset that re-encodes the audio, or switch normalization off for this step", i+1, a.Type, p.Label)
		}
	}
	return nil
}

// matches reports whether a file (ext lower-case dot-prefixed, base name, size bytes, mtime) is
// eligible under m. An invalid filenamePattern regex fails closed (no match); so does a zero
// mtime against a MinAgeDays gate (age unprovable → never delete/archive on a guess).
func (m Match) matches(ext, base string, size int64, mtime time.Time) bool {
	if m.MinSizeBytes > 0 && size < m.MinSizeBytes {
		return false
	}
	if m.MinAgeDays > 0 {
		if mtime.IsZero() || time.Since(mtime) < time.Duration(m.MinAgeDays)*24*time.Hour {
			return false
		}
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

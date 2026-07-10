package gridfix

import "time"

// BatchPhase is the coarse state of a batch run.
type BatchPhase string

const (
	PhaseScanning  BatchPhase = "scanning"  // parsing the collection + picking tracks
	PhaseAnalyzing BatchPhase = "analyzing" // per-track detect → fit → plan
	PhaseDone      BatchPhase = "done"
	PhaseCancelled BatchPhase = "cancelled"
	PhaseFailed    BatchPhase = "failed"
)

// TrackResult is one track's outcome in a batch run. The batch is READ-ONLY - it
// plans; a separate apply step (writeback layer) mutates the collection.
type TrackResult struct {
	Path      string  `json:"path"`
	Title     string  `json:"title"`  // display: "Artist - Title" when known
	Plan      Plan    `json:"plan"`   // zero when Err != ""
	OldBPM    float64 `json:"oldBpm"` // stored BPM before the fix (0 = none)
	Err       string  `json:"err,omitempty"`
	FromCache bool    `json:"fromCache"` // detection served from the analysis cache
	ElapsedMS int64   `json:"elapsedMs"`
	Beats     int     `json:"beats"` // detected beat count (diagnostics)
}

// BatchProgress is the UI-facing snapshot of a running batch (poll- or callback-fed).
type BatchProgress struct {
	Phase     BatchPhase    `json:"phase"`
	Total     int           `json:"total"` // tracks selected for the run
	Done      int           `json:"done"`
	Fixed     int           `json:"fixed"`   // Plan.Status == FIX
	OK        int           `json:"ok"`      // already aligned
	Skipped   int           `json:"skipped"` // needs manual gridding
	Failed    int           `json:"failed"`  // decode/analyze errors
	Cached    int           `json:"cached"`  // served from cache (subset of Done)
	Current   string        `json:"current"` // track being analyzed
	Err       string        `json:"err,omitempty"`
	StartedAt time.Time     `json:"startedAt"`
	Elapsed   time.Duration `json:"elapsed"`
	ETA       time.Duration `json:"eta"` // 0 = unknown
}

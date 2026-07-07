package remotectl

import (
	"time"

	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/musiclib"
	"rave.page/mate/internal/session/sinks/recorder"
	"rave.page/mate/internal/transcode"
)

// ── app.logs ──────────────────────────────────────────────────────────────────

// LogsParams selects the peer's log tail: at most Max lines (0 = server default), keeping only
// lines containing Filter (case-insensitive substring; empty = all).
type LogsParams struct {
	Max    int    `json:"max"`
	Filter string `json:"filter"`
}

// ── localMedia ────────────────────────────────────────────────────────────────

type ListDirParams struct {
	Path          string `json:"path"`
	IncludeHidden bool   `json:"includeHidden"`
}

// RenameParams renames a file/dir in place (NewName = base name only).
type RenameParams struct {
	Path    string `json:"path"`
	NewName string `json:"newName"`
}

// MoveParams moves Path into DestDir (same base name).
type MoveParams struct {
	Path    string `json:"path"`
	DestDir string `json:"destDir"`
}

// PathResult carries a resulting filesystem path (rename/move/duplicate).
type PathResult struct {
	Path string `json:"path"`
}

// ── automations ───────────────────────────────────────────────────────────────

type RunsParams struct {
	Limit int `json:"limit"`
}

type IDParam struct {
	ID string `json:"id"`
}

type SetEnabledParams struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
}

type RunManualParams struct {
	ID       string `json:"id"`
	FilePath string `json:"filePath"`
}

// OK is the ack result for void mutations.
type OK struct {
	OK bool `json:"ok"`
}

// ── library ───────────────────────────────────────────────────────────────────

// LibInfo summarizes the peer's primary collection.
type LibInfo struct {
	HasSource bool   `json:"hasSource"`
	App       string `json:"app"`
	Version   string `json:"version"`
	Path      string `json:"path"`
	Total     int    `json:"total"`
}

type TracksParams struct {
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
	Query  string `json:"query"`
}

// TracksResult is one page of the (optionally filtered) collection.
type TracksResult struct {
	Tracks []musiclib.Track `json:"tracks"`
	Total  int              `json:"total"` // matched count (pre-paging)
	Offset int              `json:"offset"`
}

type PathParam struct {
	Path string `json:"path"`
}

// WriteResult reports how many tag fields were written into the file.
type WriteResult struct {
	Written int `json:"written"`
}

// ── recorder (drive the peer's publish cockpit) ─────────────────────────────────

// RecMeta summarizes one recorded set for the sets list (no tracks → small frame).
type RecMeta struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	StartedAt    time.Time `json:"startedAt"`
	EndedAt      time.Time `json:"endedAt,omitzero"`
	TrackCount   int       `json:"trackCount"`
	ReconciledAt time.Time `json:"reconciledAt,omitzero"`
}

// RecListParams pages the peer's recorded-set summaries (Limit≤0 ⇒ server default).
type RecListParams struct {
	Offset int `json:"offset"`
	Limit  int `json:"limit"`
}

// RecListResult is one page of set summaries (newest first).
type RecListResult struct {
	Sets   []RecMeta `json:"sets"`
	Total  int       `json:"total"`
	Offset int       `json:"offset"`
}

// RecTracklistParams pages one set's tracklist.
type RecTracklistParams struct {
	ID     string `json:"id"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

// RecTracklistResult is one page of a set's tracks + the set start (so the controller can render
// per-track offsets locally).
type RecTracklistResult struct {
	Tracks    []recorder.Track `json:"tracks"`
	Total     int              `json:"total"`
	Offset    int              `json:"offset"`
	Name      string           `json:"name"`
	StartedAt time.Time        `json:"startedAt"`
}

// RecCapturesParams selects the peer's captured audio/video files (Limit≤0 ⇒ server default).
type RecCapturesParams struct {
	Limit int `json:"limit"`
}

// RecCapturesResult carries the peer's captured set-recording rows.
type RecCapturesResult struct {
	Captures []libdb.SetRecording `json:"captures"`
}

// RecExportParams renders set ID's tracklist in Format (recorder.FormatText/CSV/JSON).
type RecExportParams struct {
	ID     string `json:"id"`
	Format string `json:"format"`
}

// RecExportResult carries the rendered tracklist text.
type RecExportResult struct {
	Content string `json:"content"`
}

// RecMatchResult carries the reconciled set summary after matchHistory (so the controller can
// refresh the row's track count / matched badge without re-listing).
type RecMatchResult struct {
	Set RecMeta `json:"set"`
}

// ── media (transcode over the peer) ─────────────────────────────────────────────

// TranscodeParams asks the peer to transcode one of its own files with the given preset. The
// peer ignores EncoderOverride (its HW differs from the controller's) and encodes with the
// software encoder for the codec.
type TranscodeParams struct {
	Input     string           `json:"input"`
	Preset    transcode.Preset `json:"preset"`
	TrimStart float64          `json:"trimStart"`
	TrimEnd   float64          `json:"trimEnd"`
}

// TranscodeResult is the path the peer wrote the output to (on the controlled machine).
type TranscodeResult struct {
	Output string `json:"output"`
}

// ── screenshot (capture the controlled machine's surfaces) ───────────────────────

// ScreenshotResult carries a captured image, base64-encoded (remotectl frames are JSON). The app
// window is PNG (sharp UI); the VR-View mirror is JPEG (photographic → ~10× smaller). Bounded by
// maxControlFrame (24 MiB) - both fit comfortably. The field name is historical; bytes may be either.
type ScreenshotResult struct {
	PNGBase64 string `json:"pngBase64"`
}

// ── motion sync (replicate Motion Studio recordings across paired peers) ─────────

// MotionMeta identifies one recording by name + content hash, for name+sha256 diffing.
type MotionMeta struct {
	Name   string `json:"name"`   // base name, no .json extension
	Size   int64  `json:"size"`   // file bytes
	SHA256 string `json:"sha256"` // hex sha256 of the file bytes
}

// MotionListResult enumerates the peer's recordings.
type MotionListResult struct {
	Items []MotionMeta `json:"items"`
}

// MotionGetParams selects one recording by base name (no path).
type MotionGetParams struct {
	Name string `json:"name"`
}

// MotionGetResult carries one recording's JSON, base64-encoded (remotectl frames are JSON).
type MotionGetResult struct {
	JSONBase64 string `json:"jsonBase64"`
}

// ── vrm sync (replicate VRChat/VRM avatar models across paired peers) ─────────────

// VRMMeta identifies one avatar model by full filename + content hash. Unlike motion (stripped .json),
// the name keeps its extension - avatars are multi-ext (.vrm/.glb/.gltf).
type VRMMeta struct {
	Name   string `json:"name"`   // full filename incl. extension
	Size   int64  `json:"size"`   // file bytes
	SHA256 string `json:"sha256"` // hex sha256 of the file bytes
}

// VRMListResult enumerates the peer's avatar models.
type VRMListResult struct {
	Items []VRMMeta `json:"items"`
}

// VRMGetChunkParams reads [Offset, Offset+Len) of one avatar file. Avatars are too large for whole-file
// base64 in one 24 MiB control frame → they transfer in chunks (server clamps Len to its max).
type VRMGetChunkParams struct {
	Name   string `json:"name"`
	Offset int64  `json:"offset"`
	Len    int    `json:"len"`
}

// VRMGetChunkResult carries one chunk (base64) + EOF set when the read reached end-of-file.
type VRMGetChunkResult struct {
	DataBase64 string `json:"dataBase64"`
	EOF        bool   `json:"eof"`
}

// ── pprof (profile the controlled machine) ────────────────────────────────────

// PprofCPUParams sets the CPU capture window (seconds; the server clamps to its safe range so
// the blocking handler finishes inside serveTimeout).
type PprofCPUParams struct {
	Seconds int `json:"seconds"`
}

// PprofResult carries one runtime/pprof profile, base64-encoded (remotectl frames are JSON).
// Profiles are KBs–low MBs - far under maxControlFrame.
type PprofResult struct {
	DataBase64 string `json:"dataBase64"`
}

// ── vr diagnostics ───────────────────────────────────────────────────────────

// TextResult carries a plain-text payload (e.g. the VR input/binding diagnostic dump).
type TextResult struct {
	Text string `json:"text"`
}

package remotectl

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/musiclib"
	"rave.page/mate/internal/session/sinks/recorder"
	"rave.page/mate/internal/transcode"
)

// CueStateSHA is the TrackDetail.StateSHA canonicalization - BOTH ends must reproduce it.
// sha256 hex over the compact JSON of {"cues":[…],"beatgrid":[…],"drops":[…]} with nil
// slices normalized to [] (field order fixed by this struct; Go's json.Marshal emits
// floats shortest-round-trip, so ms values survive bit-exact across peers).
func CueStateSHA(cues []musiclib.CuePoint, grid []musiclib.GridMarker, drops []float64) string {
	s := struct {
		Cues     []musiclib.CuePoint   `json:"cues"`
		Beatgrid []musiclib.GridMarker `json:"beatgrid"`
		Drops    []float64             `json:"drops"`
	}{cues, grid, drops}
	if s.Cues == nil {
		s.Cues = []musiclib.CuePoint{}
	}
	if s.Beatgrid == nil {
		s.Beatgrid = []musiclib.GridMarker{}
	}
	if s.Drops == nil {
		s.Drops = []float64{}
	}
	b, _ := json.Marshal(s)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

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

// ── library cue editing (remote cue/beatgrid/drop editing over the link) ────────

// TrackDetailParams selects one collection track by exact path.
type TrackDetailParams struct {
	Path string `json:"path"`
}

// TrackDetail is one track's full cue-edit state: the collection row (cues+beatgrid
// included), its drop markers, the audio file's size/mtime (for the chunked pull), and
// StateSHA - the optimistic-concurrency baseline a controller echoes back as BaseSHA.
type TrackDetail struct {
	Track     musiclib.Track `json:"track"`
	Drops     []float64      `json:"drops"`
	SizeBytes int64          `json:"sizeBytes"`
	MTimeUnix int64          `json:"mtimeUnix"`
	StateSHA  string         `json:"stateSha"`
}

// FileChunkParams reads [Offset, Offset+Len) of a library track's audio file. The path
// MUST be a known library track (TrackByPath) - never an arbitrary filesystem path.
type FileChunkParams struct {
	Path   string `json:"path"`
	Offset int64  `json:"offset"`
	Len    int    `json:"len"`
}

// FileChunkResult carries one chunk (base64) + EOF on the last read, plus the file's
// total size + mtime so the controller can verify the pull stayed coherent.
type FileChunkResult struct {
	DataBase64 string `json:"dataBase64"`
	EOF        bool   `json:"eof"`
	Total      int64  `json:"total"`
	MTimeUnix  int64  `json:"mtimeUnix"`
}

// WriteCueDataParams replaces a track's cue data on the peer. Cues is the full
// replacement list; Beatgrid nil = leave unchanged; Drops applies only when DropsSet
// (nil+set = clear). BaseSHA is the TrackDetail.StateSHA the edit was based on - a
// mismatch returns Conflict (no write) unless Force.
type WriteCueDataParams struct {
	Path     string                `json:"path"`
	Cues     []musiclib.CuePoint   `json:"cues"`
	Beatgrid []musiclib.GridMarker `json:"beatgrid,omitempty"`
	Drops    []float64             `json:"drops"`
	DropsSet bool                  `json:"dropsSet"`
	BaseSHA  string                `json:"baseSha"`
	Force    bool                  `json:"force"`
}

// WriteCueDataResult: OK on a landed write, Conflict when BaseSHA was stale (nothing
// written). Detail is the peer's fresh state either way - the controller rebases on it.
type WriteCueDataResult struct {
	OK       bool        `json:"ok"`
	Conflict bool        `json:"conflict"`
	Detail   TrackDetail `json:"detail"`
}

// CueTarget is one DJ software detected on the peer as a cue write-back destination.
type CueTarget struct {
	Key   string `json:"key"`   // "traktor" | "rekordbox" | "virtualdj" | "serato"
	Label string `json:"label"` // product name (not translated)
	Path  string `json:"path"`  // file (NML/XML) or _Serato_ dir on the PEER
}

// CueTargetsResult enumerates the peer's detected write-back targets.
type CueTargetsResult struct {
	Targets []CueTarget `json:"targets"`
}

// WriteCuesToParams routes the named tracks' cue sets into Software's library ON THE
// PEER (backup-first; tracks without musical cues are skipped like the local router).
// GridAnchor (Traktor only) re-anchors the beatgrid on each track's earliest hotcue.
type WriteCuesToParams struct {
	Software   string   `json:"software"`
	Paths      []string `json:"paths"`
	GridAnchor bool     `json:"gridAnchor,omitempty"`
}

// PlaylistTracksParams selects one peer playlist by id.
type PlaylistTracksParams struct {
	ID int64 `json:"id"`
}

// PlaylistTracksResult is the playlist's track paths in order + its display name (empty
// from peers predating the set-session header, #90).
type PlaylistTracksResult struct {
	Paths []string `json:"paths"`
	Name  string   `json:"name,omitempty"`
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
// Line/NoHeader style the text format (controller's saved template); older peers ignore them.
type RecExportParams struct {
	ID       string `json:"id"`
	Format   string `json:"format"`
	Line     string `json:"line,omitempty"`
	NoHeader bool   `json:"noHeader,omitempty"`
}

// RecExportResult carries the rendered tracklist text.
type RecExportResult struct {
	Content string `json:"content"`
}

// RecRenameParams sets set ID's display name. The peer's recorder enforces the real bounds
// (non-empty after trim, ≤200 runes) - the controller doesn't pre-judge them.
type RecRenameParams struct {
	ID   string `json:"id"`
	Name string `json:"name"`
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

// ── vrchat federation (one linked peer serves friends/groups to the pair) ─────

// VrcStatus reports whether this instance holds a live VRChat session. Every peer answers
// (Linked=false without one) so a controller can pick the serving peer.
type VrcStatus struct {
	Linked      bool   `json:"linked"`
	UserID      string `json:"userId,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
}

// VrcFriendsParams pages the linked peer's friends list (offline=true pages the offline tab).
type VrcFriendsParams struct {
	Offset  int  `json:"offset"`
	N       int  `json:"n"`
	Offline bool `json:"offline"`
}

// VrcSearchGroupsParams runs a group search on the linked peer.
type VrcSearchGroupsParams struct {
	Query  string `json:"query"`
	Offset int    `json:"offset"`
	N      int    `json:"n"`
}

// VrcGroupRolesParams lists a group's roles.
type VrcGroupRolesParams struct {
	GroupID string `json:"groupId"`
}

// VrcGroupMembersParams pages a group's members (RoleID "" = all members).
type VrcGroupMembersParams struct {
	GroupID string `json:"groupId"`
	RoleID  string `json:"roleId,omitempty"`
	Offset  int    `json:"offset"`
	N       int    `json:"n"`
}

// VrcProxyParams is one tunneled VRChat API call (full vrchat federation).
// PathQuery is API-relative ("/users/usr_x/groups?n=60"); the serving peer
// joins it to its own API base and executes with its own session cookies.
type VrcProxyParams struct {
	Method      string `json:"method"`
	PathQuery   string `json:"pathQuery"`
	BodyB64     string `json:"bodyB64,omitempty"`
	ContentType string `json:"contentType,omitempty"`
}

// VrcProxyResult carries the upstream VRChat response (status + raw body).
type VrcProxyResult struct {
	Status  int    `json:"status"`
	BodyB64 string `json:"bodyB64,omitempty"`
}

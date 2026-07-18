package remotectl

import (
	"context"
	"encoding/base64"

	"rave.page/mate/internal/automation"
	"rave.page/mate/internal/localmedia"
	"rave.page/mate/internal/transcode"
	"rave.page/mate/internal/vrchat"
)

// Client is a typed view of one paired peer over the endpoint. All calls block on a network
// round-trip - invoke off the UI thread. Bind via NewClient(endpoint, peerNodeID).
type Client struct {
	e      *Endpoint
	nodeID string
}

// NewClient binds an endpoint to one target peer. Returns nil if either is empty.
func NewClient(e *Endpoint, nodeID string) *Client {
	if e == nil || nodeID == "" {
		return nil
	}
	return &Client{e: e, nodeID: nodeID}
}

// NodeID is the target peer this client drives.
func (c *Client) NodeID() string { return c.nodeID }

// ── localMedia (streamed remote file browse) ─────────────────────────────────

func (c *Client) ListDirectory(ctx context.Context, path string, includeHidden bool) (localmedia.Listing, error) {
	return Do[localmedia.Listing](ctx, c.e, c.nodeID, MethodListDirectory, ListDirParams{Path: path, IncludeHidden: includeHidden})
}

func (c *Client) GetDefaults(ctx context.Context) (localmedia.DefaultPaths, error) {
	return Do[localmedia.DefaultPaths](ctx, c.e, c.nodeID, MethodGetDefaults, nil)
}

// RenamePath renames a file/dir on the peer (newName = base name only); returns the new path.
func (c *Client) RenamePath(ctx context.Context, path, newName string) (string, error) {
	r, err := Do[PathResult](ctx, c.e, c.nodeID, MethodFileRename, RenameParams{Path: path, NewName: newName})
	return r.Path, err
}

// MovePath moves a file/dir on the peer into destDir; returns the new path.
func (c *Client) MovePath(ctx context.Context, path, destDir string) (string, error) {
	r, err := Do[PathResult](ctx, c.e, c.nodeID, MethodFileMove, MoveParams{Path: path, DestDir: destDir})
	return r.Path, err
}

// DuplicatePath copies a file beside itself on the peer; returns the copy's path.
func (c *Client) DuplicatePath(ctx context.Context, path string) (string, error) {
	r, err := Do[PathResult](ctx, c.e, c.nodeID, MethodFileDuplicate, PathParam{Path: path})
	return r.Path, err
}

// DeletePath deletes a file (or dir, recursively) on the peer.
func (c *Client) DeletePath(ctx context.Context, path string) error {
	_, err := Do[OK](ctx, c.e, c.nodeID, MethodFileDelete, PathParam{Path: path})
	return err
}

// ── automations ──────────────────────────────────────────────────────────────

func (c *Client) ListAutomations(ctx context.Context) ([]automation.Automation, error) {
	return Do[[]automation.Automation](ctx, c.e, c.nodeID, MethodAutoList, nil)
}

func (c *Client) ListSchedules(ctx context.Context) ([]automation.Schedule, error) {
	return Do[[]automation.Schedule](ctx, c.e, c.nodeID, MethodAutoSchedules, nil)
}

func (c *Client) Runs(ctx context.Context, limit int) ([]automation.Run, error) {
	return Do[[]automation.Run](ctx, c.e, c.nodeID, MethodAutoRuns, RunsParams{Limit: limit})
}

func (c *Client) SaveAutomation(ctx context.Context, a automation.Automation) (automation.Automation, error) {
	return Do[automation.Automation](ctx, c.e, c.nodeID, MethodAutoSave, a)
}

func (c *Client) DeleteAutomation(ctx context.Context, id string) error {
	_, err := Do[OK](ctx, c.e, c.nodeID, MethodAutoDelete, IDParam{ID: id})
	return err
}

func (c *Client) SetEnabled(ctx context.Context, id string, enabled bool) (automation.Automation, error) {
	return Do[automation.Automation](ctx, c.e, c.nodeID, MethodAutoSetEnabled, SetEnabledParams{ID: id, Enabled: enabled})
}

func (c *Client) RunManual(ctx context.Context, id, filePath string) (automation.Run, error) {
	return Do[automation.Run](ctx, c.e, c.nodeID, MethodAutoRunManual, RunManualParams{ID: id, FilePath: filePath})
}

func (c *Client) SaveSchedule(ctx context.Context, s automation.Schedule) (automation.Schedule, error) {
	return Do[automation.Schedule](ctx, c.e, c.nodeID, MethodAutoSaveSchedule, s)
}

func (c *Client) DeleteSchedule(ctx context.Context, id string) error {
	_, err := Do[OK](ctx, c.e, c.nodeID, MethodAutoDeleteSchedule, IDParam{ID: id})
	return err
}

// ── library ──────────────────────────────────────────────────────────────────

func (c *Client) LibraryInfo(ctx context.Context) (LibInfo, error) {
	return Do[LibInfo](ctx, c.e, c.nodeID, MethodLibInfo, nil)
}

func (c *Client) LibraryTracks(ctx context.Context, offset, limit int, query string) (TracksResult, error) {
	return Do[TracksResult](ctx, c.e, c.nodeID, MethodLibTracks, TracksParams{Offset: offset, Limit: limit, Query: query})
}

func (c *Client) WriteTags(ctx context.Context, path string) (WriteResult, error) {
	return Do[WriteResult](ctx, c.e, c.nodeID, MethodLibWriteTags, PathParam{Path: path})
}

func (c *Client) RevertTags(ctx context.Context, path string) error {
	_, err := Do[OK](ctx, c.e, c.nodeID, MethodLibRevertTags, PathParam{Path: path})
	return err
}

// ── library cue editing (remote cue/beatgrid/drop editing) ──────────────────────

// LibraryTrackDetail fetches one track's cue-edit state + the StateSHA write baseline.
func (c *Client) LibraryTrackDetail(ctx context.Context, path string) (TrackDetail, error) {
	return Do[TrackDetail](ctx, c.e, c.nodeID, MethodLibTrackDetail, TrackDetailParams{Path: path})
}

// LibraryFileChunk pulls [offset, offset+n) of a library track's audio file (base64;
// server clamps n to its max). Loop until EOF to replicate the file.
func (c *Client) LibraryFileChunk(ctx context.Context, path string, offset int64, n int) (FileChunkResult, error) {
	return Do[FileChunkResult](ctx, c.e, c.nodeID, MethodLibFileChunk, FileChunkParams{Path: path, Offset: offset, Len: n})
}

// WriteCueData writes an edited cue/beatgrid/drop set back to the peer. Check
// result.Conflict: the peer's state moved under the edit (rebase on result.Detail).
func (c *Client) WriteCueData(ctx context.Context, p WriteCueDataParams) (WriteCueDataResult, error) {
	return Do[WriteCueDataResult](ctx, c.e, c.nodeID, MethodLibWriteCueData, p)
}

// CueWriteTargets lists the DJ softwares detected on the peer as write-back destinations.
func (c *Client) CueWriteTargets(ctx context.Context) ([]CueTarget, error) {
	r, err := Do[CueTargetsResult](ctx, c.e, c.nodeID, MethodLibCueTargets, nil)
	return r.Targets, err
}

// WriteCuesTo routes the named tracks' cues into software's library ON THE PEER
// (backup-first there); returns how many tracks the write updated.
func (c *Client) WriteCuesTo(ctx context.Context, software string, paths []string, gridAnchor bool) (WriteResult, error) {
	return Do[WriteResult](ctx, c.e, c.nodeID, MethodLibWriteCuesTo, WriteCuesToParams{Software: software, Paths: paths, GridAnchor: gridAnchor})
}

// LibraryPlaylistTracks resolves a peer playlist to its track paths in order.
func (c *Client) LibraryPlaylistTracks(ctx context.Context, id int64) ([]string, error) {
	r, err := c.LibraryPlaylistTracksInfo(ctx, id)
	return r.Paths, err
}

// LibraryPlaylistTracksInfo: ordered paths + display name (Name empty from older peers).
func (c *Client) LibraryPlaylistTracksInfo(ctx context.Context, id int64) (PlaylistTracksResult, error) {
	return Do[PlaylistTracksResult](ctx, c.e, c.nodeID, MethodLibPlaylistTracks, PlaylistTracksParams{ID: id})
}

// ── recorder (drive the peer's publish cockpit) ─────────────────────────────────

// RecList pages the peer's recorded-set summaries (newest first).
func (c *Client) RecList(ctx context.Context, offset, limit int) (RecListResult, error) {
	return Do[RecListResult](ctx, c.e, c.nodeID, MethodRecList, RecListParams{Offset: offset, Limit: limit})
}

// RecTracklist pages one set's tracklist.
func (c *Client) RecTracklist(ctx context.Context, id string, offset, limit int) (RecTracklistResult, error) {
	return Do[RecTracklistResult](ctx, c.e, c.nodeID, MethodRecTracklist, RecTracklistParams{ID: id, Offset: offset, Limit: limit})
}

// RecCaptures lists the peer's captured audio/video files.
func (c *Client) RecCaptures(ctx context.Context, limit int) (RecCapturesResult, error) {
	return Do[RecCapturesResult](ctx, c.e, c.nodeID, MethodRecCaptures, RecCapturesParams{Limit: limit})
}

// RecExport renders set id's tracklist in format (recorder.FormatText/CSV/JSON); returns the text.
func (c *Client) RecExport(ctx context.Context, id, format string) (string, error) {
	r, err := Do[RecExportResult](ctx, c.e, c.nodeID, MethodRecExport, RecExportParams{ID: id, Format: format})
	return r.Content, err
}

// RecRename sets a finished set's display name on the peer.
func (c *Client) RecRename(ctx context.Context, id, name string) error {
	_, err := Do[OK](ctx, c.e, c.nodeID, MethodRecRename, RecRenameParams{ID: id, Name: name})
	return err
}

// RecDelete deletes a finished set on the peer.
func (c *Client) RecDelete(ctx context.Context, id string) error {
	_, err := Do[OK](ctx, c.e, c.nodeID, MethodRecDelete, IDParam{ID: id})
	return err
}

// RecMatchHistory reconciles set id against the peer's Traktor history; returns the updated summary.
func (c *Client) RecMatchHistory(ctx context.Context, id string) (RecMeta, error) {
	r, err := Do[RecMatchResult](ctx, c.e, c.nodeID, MethodRecMatch, IDParam{ID: id})
	return r.Set, err
}

// ── media ──────────────────────────────────────────────────────────────────────

// Transcode runs a one-shot transcode of input (a path on the peer) with preset, blocking
// until the peer's worker finishes. Pass a generous ctx timeout - transcode chains are slow.
func (c *Client) Transcode(ctx context.Context, input string, preset transcode.Preset, trimStart, trimEnd float64) (TranscodeResult, error) {
	return Do[TranscodeResult](ctx, c.e, c.nodeID, MethodMediaTranscode,
		TranscodeParams{Input: input, Preset: preset, TrimStart: trimStart, TrimEnd: trimEnd})
}

// ── screenshot ──────────────────────────────────────────────────────────────────

// ScreenshotApp captures the peer's app window and returns the raw PNG bytes.
func (c *Client) ScreenshotApp(ctx context.Context) ([]byte, error) {
	return c.screenshot(ctx, MethodScreenshotApp)
}

// ScreenshotVR captures the peer's SteamVR VR-View mirror (opt-in on the peer) and returns PNG bytes.
func (c *Client) ScreenshotVR(ctx context.Context) ([]byte, error) {
	return c.screenshot(ctx, MethodScreenshotVR)
}

func (c *Client) screenshot(ctx context.Context, method string) ([]byte, error) {
	r, err := Do[ScreenshotResult](ctx, c.e, c.nodeID, method, nil)
	if err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(r.PNGBase64)
}

// ── motion sync ─────────────────────────────────────────────────────────────────

// MotionList enumerates the peer's Motion Studio recordings (name + size + sha256).
func (c *Client) MotionList(ctx context.Context) (MotionListResult, error) {
	return Do[MotionListResult](ctx, c.e, c.nodeID, MethodMotionList, nil)
}

// MotionGet fetches one recording's JSON (base64) by base name.
func (c *Client) MotionGet(ctx context.Context, name string) (MotionGetResult, error) {
	return Do[MotionGetResult](ctx, c.e, c.nodeID, MethodMotionGet, MotionGetParams{Name: name})
}

// VRMList enumerates the peer's avatar models (full name incl. ext + size + sha256).
func (c *Client) VRMList(ctx context.Context) (VRMListResult, error) {
	return Do[VRMListResult](ctx, c.e, c.nodeID, MethodVRMList, nil)
}

// VRMGetChunk fetches [offset, offset+n) of one avatar file (base64). Server clamps n to its max.
func (c *Client) VRMGetChunk(ctx context.Context, name string, offset int64, n int) (VRMGetChunkResult, error) {
	return Do[VRMGetChunkResult](ctx, c.e, c.nodeID, MethodVRMGetChunk, VRMGetChunkParams{Name: name, Offset: offset, Len: n})
}

// VRInputDiag fetches the peer's SteamVR Input action/binding diagnostic (plain text).
func (c *Client) VRInputDiag(ctx context.Context) (string, error) {
	r, err := Do[TextResult](ctx, c.e, c.nodeID, MethodVRInputDiag, nil)
	if err != nil {
		return "", err
	}
	return r.Text, nil
}

// Perf fetches the peer's perf-diagnosis report (plain text; the peer samples ~1s before replying).
func (c *Client) Perf(ctx context.Context) (string, error) {
	r, err := Do[TextResult](ctx, c.e, c.nodeID, MethodAppPerf, nil)
	if err != nil {
		return "", err
	}
	return r.Text, nil
}

// Logs fetches the peer's recent log tail (plain text): at most max lines, keeping only lines
// containing filter (case-insensitive; "" = all).
func (c *Client) Logs(ctx context.Context, max int, filter string) (string, error) {
	r, err := Do[TextResult](ctx, c.e, c.nodeID, MethodAppLogs, LogsParams{Max: max, Filter: filter})
	if err != nil {
		return "", err
	}
	return r.Text, nil
}

// EncoderScan fetches the peer's live encoder-utilization scan + placement plan (plain text; the
// peer samples ~300ms of PDH GPU-engine counters before replying - read-only, no GPU encode).
func (c *Client) EncoderScan(ctx context.Context) (string, error) {
	r, err := Do[TextResult](ctx, c.e, c.nodeID, MethodAppEncoderScan, nil)
	if err != nil {
		return "", err
	}
	return r.Text, nil
}

// PprofCPU captures seconds of the peer's CPU profile and returns the raw pprof bytes. The peer
// blocks for the capture window - budget ctx at seconds + margin.
func (c *Client) PprofCPU(ctx context.Context, seconds int) ([]byte, error) {
	return c.pprof(ctx, MethodAppPprofCPU, PprofCPUParams{Seconds: seconds})
}

// PprofHeap captures the peer's heap profile (raw pprof bytes).
func (c *Client) PprofHeap(ctx context.Context) ([]byte, error) {
	return c.pprof(ctx, MethodAppPprofHeap, nil)
}

func (c *Client) pprof(ctx context.Context, method string, params any) ([]byte, error) {
	r, err := Do[PprofResult](ctx, c.e, c.nodeID, method, params)
	if err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(r.DataBase64)
}

// Goroutines fetches the peer's grouped goroutine dump (plain text).
func (c *Client) Goroutines(ctx context.Context) (string, error) {
	r, err := Do[TextResult](ctx, c.e, c.nodeID, MethodAppGoroutines, nil)
	if err != nil {
		return "", err
	}
	return r.Text, nil
}

// SelfUpdate triggers the peer's self-update (download+apply+relaunch of the rave-mate app only -
// not a PC restart). Returns the peer's status ("updating"/"disabled"/"already updating"); the reply
// arrives before the relaunch.
func (c *Client) SelfUpdate(ctx context.Context) (string, error) {
	r, err := Do[TextResult](ctx, c.e, c.nodeID, MethodSelfUpdate, nil)
	if err != nil {
		return "", err
	}
	return r.Text, nil
}

// ── vrchat federation ─────────────────────────────────────────────────────────

// VrcStatus reports whether the peer holds a live VRChat session.
func (c *Client) VrcStatus(ctx context.Context) (VrcStatus, error) {
	return Do[VrcStatus](ctx, c.e, c.nodeID, MethodVrcStatus, nil)
}

// VrcFriends pages the peer's VRChat friends list.
func (c *Client) VrcFriends(ctx context.Context, offset, n int, offline bool) ([]vrchat.Friend, error) {
	return Do[[]vrchat.Friend](ctx, c.e, c.nodeID, MethodVrcFriends, VrcFriendsParams{Offset: offset, N: n, Offline: offline})
}

// VrcUserGroups lists the groups of the peer's linked VRChat account.
func (c *Client) VrcUserGroups(ctx context.Context) ([]vrchat.Group, error) {
	return Do[[]vrchat.Group](ctx, c.e, c.nodeID, MethodVrcUserGroups, nil)
}

// VrcSearchGroups searches groups through the peer's session.
func (c *Client) VrcSearchGroups(ctx context.Context, query string, offset, n int) ([]vrchat.Group, error) {
	return Do[[]vrchat.Group](ctx, c.e, c.nodeID, MethodVrcSearchGroups, VrcSearchGroupsParams{Query: query, Offset: offset, N: n})
}

// VrcGroupRoles lists a group's roles through the peer's session.
func (c *Client) VrcGroupRoles(ctx context.Context, groupID string) ([]vrchat.GroupRole, error) {
	return Do[[]vrchat.GroupRole](ctx, c.e, c.nodeID, MethodVrcGroupRoles, VrcGroupRolesParams{GroupID: groupID})
}

// VrcGroupMembers pages a group's members through the peer's session (roleID "" = all).
func (c *Client) VrcGroupMembers(ctx context.Context, groupID, roleID string, offset, n int) ([]vrchat.GroupMember, error) {
	return Do[[]vrchat.GroupMember](ctx, c.e, c.nodeID, MethodVrcGroupMembers, VrcGroupMembersParams{GroupID: groupID, RoleID: roleID, Offset: offset, N: n})
}

// VrcProxy tunnels one VRChat API call through the peer's session (full
// vrchat federation - the app-side RoundTripper is the only caller).
func (c *Client) VrcProxy(ctx context.Context, method, pathQuery string, body []byte, contentType string) (int, []byte, error) {
	p := VrcProxyParams{Method: method, PathQuery: pathQuery, ContentType: contentType}
	if len(body) > 0 {
		p.BodyB64 = base64.StdEncoding.EncodeToString(body)
	}
	r, err := Do[VrcProxyResult](ctx, c.e, c.nodeID, MethodVrcProxy, p)
	if err != nil {
		return 0, nil, err
	}
	var respBody []byte
	if r.BodyB64 != "" {
		if respBody, err = base64.StdEncoding.DecodeString(r.BodyB64); err != nil {
			return 0, nil, err
		}
	}
	return r.Status, respBody, nil
}

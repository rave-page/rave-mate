package remotectl

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"rave.page/mate/internal/automation"
	"rave.page/mate/internal/cuewriteback"
	"rave.page/mate/internal/jobs"
	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/localmedia"
	"rave.page/mate/internal/musiclib"
	"rave.page/mate/internal/session/sinks/recorder"
	"rave.page/mate/internal/tagsync"
	"rave.page/mate/internal/tagwrite"
	"rave.page/mate/internal/vrchat"
)

// remoteJobSeq names peer-driven transcode jobs uniquely on the controlled machine.
var remoteJobSeq atomic.Uint64

// tagApplier writes/reverts a track's analysis into its file (tagsync.Apply/Revert). Decoupled
// for tests; *libdb.DB-backed in prod.
type tagApplier func(t musiclib.Track) (written int, err error)
type tagReverter func(path string) error

// RegisterBrowse installs the streamed file-browse handlers (the controlled machine's
// filesystem, never a native dialog). Shared with the studio surface via internal/localmedia.
func RegisterBrowse(e *Endpoint) {
	if e == nil {
		return
	}
	e.Register(MethodListDirectory, func(_ context.Context, _ string, raw json.RawMessage) (any, error) {
		var p ListDirParams
		_ = json.Unmarshal(raw, &p)
		return localmedia.ListDirectory(p.Path, p.IncludeHidden), nil
	})
	e.Register(MethodGetDefaults, func(_ context.Context, _ string, _ json.RawMessage) (any, error) {
		return localmedia.Defaults(), nil
	})
	// File ops (paired peers are trusted-on-pair): same localmedia implementation the local
	// Library uses, so remote rows behave identically.
	e.Register(MethodFileRename, func(_ context.Context, _ string, raw json.RawMessage) (any, error) {
		var p RenameParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		np, err := localmedia.Rename(p.Path, p.NewName)
		if err != nil {
			return nil, err
		}
		return PathResult{Path: np}, nil
	})
	e.Register(MethodFileMove, func(_ context.Context, _ string, raw json.RawMessage) (any, error) {
		var p MoveParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		np, err := localmedia.Move(p.Path, p.DestDir)
		if err != nil {
			return nil, err
		}
		return PathResult{Path: np}, nil
	})
	e.Register(MethodFileDuplicate, func(_ context.Context, _ string, raw json.RawMessage) (any, error) {
		var p PathParam
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		np, err := localmedia.Duplicate(p.Path)
		if err != nil {
			return nil, err
		}
		return PathResult{Path: np}, nil
	})
	e.Register(MethodFileDelete, func(_ context.Context, _ string, raw json.RawMessage) (any, error) {
		var p PathParam
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		if err := localmedia.Delete(p.Path); err != nil {
			return nil, err
		}
		return OK{OK: true}, nil
	})
}

// RegisterAutomations exposes the peer's automation.Manager to controllers. Runs execute over
// the controlled machine's own filesystem + worker pool.
func RegisterAutomations(e *Endpoint, m automation.Manager) {
	if e == nil || m == nil {
		return
	}
	e.Register(MethodAutoList, func(_ context.Context, _ string, _ json.RawMessage) (any, error) {
		return m.List(), nil
	})
	e.Register(MethodAutoSchedules, func(_ context.Context, _ string, _ json.RawMessage) (any, error) {
		return m.ListSchedules(), nil
	})
	e.Register(MethodAutoRuns, func(_ context.Context, _ string, raw json.RawMessage) (any, error) {
		var p RunsParams
		_ = json.Unmarshal(raw, &p)
		if p.Limit <= 0 {
			p.Limit = 50
		}
		return m.Runs(p.Limit), nil
	})
	e.Register(MethodAutoSave, func(_ context.Context, _ string, raw json.RawMessage) (any, error) {
		var a automation.Automation
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, err
		}
		return m.Save(a)
	})
	e.Register(MethodAutoDelete, func(_ context.Context, _ string, raw json.RawMessage) (any, error) {
		var p IDParam
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		if err := m.Delete(p.ID); err != nil {
			return nil, err
		}
		return OK{OK: true}, nil
	})
	e.Register(MethodAutoSetEnabled, func(_ context.Context, _ string, raw json.RawMessage) (any, error) {
		var p SetEnabledParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		a, ok := m.Get(p.ID)
		if !ok {
			return nil, errors.New("automation not found")
		}
		a.Enabled = p.Enabled
		return m.Save(a)
	})
	e.Register(MethodAutoRunManual, func(ctx context.Context, _ string, raw json.RawMessage) (any, error) {
		var p RunManualParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		return m.RunManual(ctx, p.ID, p.FilePath)
	})
	e.Register(MethodAutoSaveSchedule, func(_ context.Context, _ string, raw json.RawMessage) (any, error) {
		var s automation.Schedule
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, err
		}
		return m.SaveSchedule(s)
	})
	e.Register(MethodAutoDeleteSchedule, func(_ context.Context, _ string, raw json.RawMessage) (any, error) {
		var p IDParam
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		if err := m.DeleteSchedule(p.ID); err != nil {
			return nil, err
		}
		return OK{OK: true}, nil
	})
}

// RegisterLibrary exposes the peer's persisted collection (browse + tag-edit). Decoupled tag
// applier/reverter so the heavy file-write path is testable; prod uses tagsync over *libdb.DB.
func RegisterLibrary(e *Endpoint, lib *libdb.DB) {
	if e == nil || lib == nil {
		return
	}
	apply := func(t musiclib.Track) (int, error) {
		w, err := tagsync.Apply(lib, t)
		return len(w), err
	}
	revert := func(path string) error { return tagsync.Revert(lib, path) }
	registerLibrary(e, lib, apply, revert)
}

// libTracksCache fronts lib.LoadTracks(srcID) with an in-proc snapshot keyed by (srcID,
// TracksVersion): the full-table SELECT + per-row cue/beatgrid JSON unmarshal reruns only when a
// tracks-row content mutation bumps TracksVersion. Keyed on TracksVersion (NOT LibraryVersion):
// pure deletes (DeleteTracksByPaths / sync removals) and "set" reverts don't append change_log, so
// LibraryVersion (MAX(seq)) would keep serving deleted/pre-revert rows. TracksVersion is an atomic
// load; the scan it gates is the expensive part.
//
// The returned slice is shared READ-ONLY — the library.tracks handler only filterTracks (fresh
// slice on non-empty query, else aliases), pageTracks (sub-slice window), and json-marshals it;
// nothing mutates elements or the backing array, so no clone is taken. A reload swaps the field
// for a NEW array, so a concurrent reader holding the prior snapshot stays valid.
type libTracksCache struct {
	lib    *libdb.DB
	mu     sync.Mutex
	srcID  int64
	ver    int64
	loaded bool
	tracks []musiclib.Track
}

func (c *libTracksCache) load(srcID int64) ([]musiclib.Track, error) {
	ver := c.lib.TracksVersion()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loaded && c.srcID == srcID && c.ver == ver {
		return c.tracks, nil
	}
	fresh, err := c.lib.LoadTracks(srcID)
	if err != nil {
		return nil, err
	}
	c.tracks, c.srcID, c.ver, c.loaded = fresh, srcID, ver, true
	return fresh, nil
}

func registerLibrary(e *Endpoint, lib *libdb.DB, apply tagApplier, revert tagReverter) {
	tracksCache := &libTracksCache{lib: lib}
	e.Register(MethodLibInfo, func(_ context.Context, _ string, _ json.RawMessage) (any, error) {
		src, ok, err := lib.FirstSource()
		if err != nil {
			return nil, err
		}
		if !ok {
			return LibInfo{}, nil
		}
		total, _ := lib.CountTracks(src.ID)
		return LibInfo{HasSource: true, App: src.App, Version: src.Version, Path: src.Path, Total: total}, nil
	})
	e.Register(MethodLibTracks, func(_ context.Context, _ string, raw json.RawMessage) (any, error) {
		var p TracksParams
		_ = json.Unmarshal(raw, &p)
		src, ok, err := lib.FirstSource()
		if err != nil {
			return nil, err
		}
		if !ok {
			return TracksResult{Tracks: []musiclib.Track{}}, nil
		}
		all, err := tracksCache.load(src.ID)
		if err != nil {
			return nil, err
		}
		matched := filterTracks(all, p.Query)
		page := pageTracks(matched, p.Offset, p.Limit)
		return TracksResult{Tracks: page, Total: len(matched), Offset: p.Offset}, nil
	})
	e.Register(MethodLibWriteTags, func(_ context.Context, _ string, raw json.RawMessage) (any, error) {
		var p PathParam
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		t, ok, err := lib.TrackByPath(p.Path)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errors.New("track not found")
		}
		n, err := apply(t)
		if err != nil {
			return nil, err
		}
		return WriteResult{Written: n}, nil
	})
	e.Register(MethodLibRevertTags, func(_ context.Context, _ string, raw json.RawMessage) (any, error) {
		var p PathParam
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		if err := revert(p.Path); err != nil {
			return nil, err
		}
		return OK{OK: true}, nil
	})
}

// PublishFunc emits an eventbus topic (eventbus.Bus.Publish; nil = no bus). Decoupled so
// the cue-edit handlers stay testable without a bus.
type PublishFunc func(topic string, data json.RawMessage)

// RegisterLibraryCueEdit exposes remote cue/beatgrid/drop editing of the peer's collection:
// trackDetail (state + StateSHA baseline), fileChunk (chunked audio pull, library tracks
// only), writeCueData (optimistic-concurrency write, mirrors the local cue editor's
// persistence), cueWriteTargets/writeCuesTo (route cues into THIS machine's DJ software via
// cuewriteback), playlistTracks. nmlOverride = configured Traktor collection path getter
// ("" = auto-discover); backupRoot receives pre-write library backups.
func RegisterLibraryCueEdit(e *Endpoint, lib *libdb.DB, pub PublishFunc, nmlOverride func() string, backupRoot string) {
	if e == nil || lib == nil {
		return
	}
	if nmlOverride == nil {
		nmlOverride = func() string { return "" }
	}
	e.Register(MethodLibTrackDetail, func(_ context.Context, _ string, raw json.RawMessage) (any, error) {
		var p TrackDetailParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		return libTrackDetail(lib, p.Path)
	})
	e.Register(MethodLibFileChunk, func(_ context.Context, _ string, raw json.RawMessage) (any, error) {
		var p FileChunkParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		// SECURITY: serve only known library tracks - never arbitrary filesystem paths.
		if _, ok, err := lib.TrackByPath(p.Path); err != nil {
			return nil, err
		} else if !ok {
			return nil, errors.New("track not found")
		}
		return readLibChunk(p)
	})
	e.Register(MethodLibWriteCueData, func(_ context.Context, peerNodeID string, raw json.RawMessage) (any, error) {
		var p WriteCueDataParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		cur, err := libTrackDetail(lib, p.Path)
		if err != nil {
			return nil, err
		}
		if !p.Force && cur.StateSHA != p.BaseSHA {
			return WriteCueDataResult{Conflict: true, Detail: cur}, nil // state moved - no write
		}
		// Same sequence the local cue editor persists (webui library_cueedit.go):
		// cues → beatgrid (only when sent) → drops (only when sent, + file-tag mirror).
		t := cur.Track
		if err := lib.UpdateTrackCues(t, p.Cues); err != nil {
			return nil, err
		}
		if p.Beatgrid != nil {
			if err := lib.UpdateTrackBeatgrid(t, p.Beatgrid); err != nil {
				return nil, err
			}
		}
		if p.DropsSet {
			if err := lib.SetDrops(p.Path, t.Artist, t.Title, t.DurationSec, p.Drops); err != nil {
				return nil, err
			}
			if tagwrite.Supported(p.Path) {
				_ = tagwrite.WriteDrops(p.Path, p.Drops) // best-effort, like the local editor (toast + continue)
			}
		}
		fresh, err := libTrackDetail(lib, p.Path)
		if err != nil {
			return nil, err
		}
		if pub != nil {
			data, _ := json.Marshal(libdb.TrackChangedEvent{Path: p.Path, Origin: "peer:" + peerNodeID})
			pub(libdb.TopicTrackChanged, data)
		}
		return WriteCueDataResult{OK: true, Detail: fresh}, nil
	})
	e.Register(MethodLibCueTargets, func(context.Context, string, json.RawMessage) (any, error) {
		det := cuewriteback.DetectTargets(nmlOverride())
		out := make([]CueTarget, len(det))
		for i, t := range det {
			out[i] = CueTarget{Key: t.Key, Label: t.Label, Path: t.Path}
		}
		return CueTargetsResult{Targets: out}, nil
	})
	e.Register(MethodLibWriteCuesTo, func(_ context.Context, _ string, raw json.RawMessage) (any, error) {
		var p WriteCuesToParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		var target *cuewriteback.Target
		for _, t := range cuewriteback.DetectTargets(nmlOverride()) {
			if t.Key == p.Software {
				tc := t
				target = &tc
				break
			}
		}
		if target == nil {
			return nil, fmt.Errorf("write target not detected: %q", p.Software)
		}
		var updates []musiclib.CueUpdate
		for _, path := range p.Paths {
			t, ok, err := lib.TrackByPath(path)
			if err != nil {
				return nil, err
			}
			if !ok || musiclib.MusicalCues(t.Cues) == 0 {
				continue // mirror the local router: only tracks with ≥1 musical cue
			}
			updates = append(updates, musiclib.CueUpdate{Path: path, BPM: t.BPM, Cues: t.Cues})
		}
		if len(updates) == 0 {
			return nil, errors.New("no tracks with musical cues")
		}
		res, err := cuewriteback.ApplyCues(*target, updates, backupRoot)
		if err != nil {
			return nil, err
		}
		return WriteResult{Written: res.Updated}, nil
	})
	e.Register(MethodLibPlaylistTracks, func(_ context.Context, _ string, raw json.RawMessage) (any, error) {
		var p PlaylistTracksParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		paths, err := lib.PlaylistTracks(p.ID)
		if err != nil {
			return nil, err
		}
		if paths == nil {
			paths = []string{}
		}
		name := ""
		if row, ok, _ := lib.PlaylistByID(p.ID); ok {
			name = row.Name
		}
		return PlaylistTracksResult{Paths: paths, Name: name}, nil
	})
}

// libTrackDetail assembles a track's cue-edit state + StateSHA. File size/mtime are
// best-effort (zero when the audio is offline - cue-only edits still work).
func libTrackDetail(lib *libdb.DB, path string) (TrackDetail, error) {
	t, ok, err := lib.TrackByPath(path)
	if err != nil {
		return TrackDetail{}, err
	}
	if !ok {
		return TrackDetail{}, errors.New("track not found")
	}
	drops, err := lib.Drops(path)
	if err != nil {
		return TrackDetail{}, err
	}
	if drops == nil {
		drops = []float64{}
	}
	d := TrackDetail{Track: t, Drops: drops, StateSHA: CueStateSHA(t.Cues, t.Beatgrid, drops)}
	if fi, serr := os.Stat(path); serr == nil {
		d.SizeBytes, d.MTimeUnix = fi.Size(), fi.ModTime().Unix()
	}
	return d, nil
}

// readLibChunk reads [Offset, Offset+min(Len,vrmChunkMax)) of the track's audio file,
// base64. Same frame math as vrm.getChunk (8 MiB raw stays under maxControlFrame).
func readLibChunk(p FileChunkParams) (FileChunkResult, error) {
	if p.Offset < 0 || p.Len <= 0 {
		return FileChunkResult{}, errors.New("invalid chunk range")
	}
	f, err := os.Open(p.Path)
	if err != nil {
		return FileChunkResult{}, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return FileChunkResult{}, err
	}
	buf := make([]byte, min(p.Len, vrmChunkMax))
	read, err := f.ReadAt(buf, p.Offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return FileChunkResult{}, err
	}
	// ReadAt reports io.EOF when it fills fewer than len(buf) bytes at end-of-file → last chunk.
	return FileChunkResult{
		DataBase64: base64.StdEncoding.EncodeToString(buf[:read]),
		EOF:        errors.Is(err, io.EOF),
		Total:      fi.Size(),
		MTimeUnix:  fi.ModTime().Unix(),
	}, nil
}

// RecorderSource is the subset of *recorder.Recorder a controller drives over the link:
// list/get recorded sets, export a tracklist, rename or delete a finished set. Decoupled for tests.
type RecorderSource interface {
	List() []recorder.Recording
	Get(id string) (recorder.Recording, bool)
	Export(id, format string) (string, error)
	Rename(id, name string) error
	Delete(id string) error
}

// SetCaptureSource lists the controlled machine's captured set-audio/video rows (*libdb.DB).
type SetCaptureSource interface {
	ListSetRecordings(limit int) ([]libdb.SetRecording, error)
}

// RegisterRecorder exposes the peer's recording/publish cockpit (read + rename/export/delete) so a
// paired controller can drive the remote Publish tab. Sets list as summaries (paged); one set's
// tracklist pages separately so a monster set stays under maxControlFrame. caps (nil-ok) lists the
// peer's captured audio/video files. No live start/finish over the link - recording control stays
// local.
func RegisterRecorder(e *Endpoint, rec RecorderSource, caps SetCaptureSource) {
	if e == nil || rec == nil {
		return
	}
	e.Register(MethodRecList, func(_ context.Context, _ string, raw json.RawMessage) (any, error) {
		var p RecListParams
		_ = json.Unmarshal(raw, &p)
		all := rec.List()
		metas := make([]RecMeta, len(all))
		for i, r := range all {
			metas[i] = RecMeta{ID: r.ID, Name: r.Name, StartedAt: r.StartedAt, EndedAt: r.EndedAt, TrackCount: len(r.Tracks), ReconciledAt: r.ReconciledAt}
		}
		return RecListResult{Sets: pageRecMeta(metas, p.Offset, p.Limit), Total: len(metas), Offset: p.Offset}, nil
	})
	e.Register(MethodRecTracklist, func(_ context.Context, _ string, raw json.RawMessage) (any, error) {
		var p RecTracklistParams
		_ = json.Unmarshal(raw, &p)
		r, ok := rec.Get(p.ID)
		if !ok {
			return nil, errors.New("recording not found")
		}
		return RecTracklistResult{Tracks: pageRecTracks(r.Tracks, p.Offset, p.Limit), Total: len(r.Tracks), Offset: p.Offset, Name: r.Name, StartedAt: r.StartedAt}, nil
	})
	e.Register(MethodRecExport, func(_ context.Context, _ string, raw json.RawMessage) (any, error) {
		var p RecExportParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		out, err := rec.Export(p.ID, p.Format)
		if err != nil {
			return nil, err
		}
		return RecExportResult{Content: out}, nil
	})
	e.Register(MethodRecRename, func(_ context.Context, _ string, raw json.RawMessage) (any, error) {
		var p RecRenameParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		// Name bounds (trim/empty/length) are the recorder's call - it owns the invariant locally
		// and over the link alike.
		if err := rec.Rename(p.ID, p.Name); err != nil {
			return nil, err
		}
		return OK{OK: true}, nil
	})
	e.Register(MethodRecDelete, func(_ context.Context, _ string, raw json.RawMessage) (any, error) {
		var p IDParam
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		if err := rec.Delete(p.ID); err != nil {
			return nil, err
		}
		return OK{OK: true}, nil
	})
	if caps != nil {
		e.Register(MethodRecCaptures, func(_ context.Context, _ string, raw json.RawMessage) (any, error) {
			var p RecCapturesParams
			_ = json.Unmarshal(raw, &p)
			limit := p.Limit
			if limit <= 0 {
				limit = 300
			}
			list, err := caps.ListSetRecordings(limit)
			if err != nil {
				return nil, err
			}
			if list == nil {
				list = []libdb.SetRecording{}
			}
			return RecCapturesResult{Captures: list}, nil
		})
	}
}

// RecorderMatcher reconciles a finished set against the controlled machine's Traktor history and
// returns the reconciled set. The app supplies its OWN history dir + metadata resolver (they live
// on the controlled box, not on the controller) - decoupled here to avoid an app/config import.
type RecorderMatcher func(id string) (recorder.Recording, error)

// RegisterRecorderMatch exposes recorder.matchHistory so a paired controller can trigger the peer's
// Traktor-history reconciliation for a finished set (the heavy match runs on the peer). Returns the
// reconciled set summary. No-op if match is nil (recorder disabled / no history configured).
func RegisterRecorderMatch(e *Endpoint, match RecorderMatcher) {
	if e == nil || match == nil {
		return
	}
	e.Register(MethodRecMatch, func(_ context.Context, _ string, raw json.RawMessage) (any, error) {
		var p IDParam
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		r, err := match(p.ID)
		if err != nil {
			return nil, err
		}
		return RecMatchResult{Set: RecMeta{ID: r.ID, Name: r.Name, StartedAt: r.StartedAt, EndedAt: r.EndedAt, TrackCount: len(r.Tracks), ReconciledAt: r.ReconciledAt}}, nil
	})
}

// pageRecMeta slices [offset, offset+limit) of set summaries (limit≤0 ⇒ 200). Always non-nil.
func pageRecMeta(in []RecMeta, offset, limit int) []RecMeta {
	if limit <= 0 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= len(in) {
		return []RecMeta{}
	}
	return in[offset:min(offset+limit, len(in))]
}

// pageRecTracks slices [offset, offset+limit) of a set's tracks (limit≤0 ⇒ 500). Always non-nil.
func pageRecTracks(in []recorder.Track, offset, limit int) []recorder.Track {
	if limit <= 0 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= len(in) {
		return []recorder.Track{}
	}
	return in[offset:min(offset+limit, len(in))]
}

// Screenshotter captures the controlled machine's surfaces to a PNG file at path (false on
// failure). Implemented by app.appControl: Screenshot grabs the Fyne window, ScreenshotVR grabs
// the SteamVR VR-View mirror (opt-in, Windows-only). Decoupled here to avoid an app import cycle.
type Screenshotter interface {
	Screenshot(path string) bool
	ScreenshotVR(path string) bool
}

// RegisterScreenshot exposes capturing the controlled machine's app window + SteamVR VR-View as a
// base64 PNG over the peer link. The PNG is written to a temp file we read back (remotectl frames
// are JSON, no binary channel); a large grab may exceed maxControlFrame and fail gracefully.
func RegisterScreenshot(e *Endpoint, shot Screenshotter) {
	if e == nil || shot == nil {
		return
	}
	e.Register(MethodScreenshotApp, func(context.Context, string, json.RawMessage) (any, error) {
		return captureToBase64(shot.Screenshot, ".png") // UI window → PNG (sharp text, compresses well)
	})
	e.Register(MethodScreenshotVR, func(context.Context, string, json.RawMessage) (any, error) {
		return captureToBase64(shot.ScreenshotVR, ".jpg") // VR mirror → JPEG (photographic, fits the frame)
	})
}

// VRDiagnostic exposes the controlled machine's SteamVR Input action state (bound origins + live
// state) as text. Implemented by app.appControl over the vroverlay manager.
type VRDiagnostic interface {
	VRInputDiag() string
}

// RegisterVRDiag exposes the peer's VR input/binding diagnostic over the peer link (read-only).
func RegisterVRDiag(e *Endpoint, d VRDiagnostic) {
	if e == nil || d == nil {
		return
	}
	e.Register(MethodVRInputDiag, func(context.Context, string, json.RawMessage) (any, error) {
		return TextResult{Text: d.VRInputDiag()}, nil
	})
}

// PerfSource exposes the machine's perf-diagnosis report (text). Implemented by app.appControl
// over the perfmon monitor + probe registry.
type PerfSource interface {
	Perf() string
}

// RegisterPerf exposes the peer's perf report over the peer link (read-only). The handler blocks
// ~1s for the system/per-process CPU sampling pass - well inside serveTimeout.
func RegisterPerf(e *Endpoint, p PerfSource) {
	if e == nil || p == nil {
		return
	}
	e.Register(MethodAppPerf, func(context.Context, string, json.RawMessage) (any, error) {
		return TextResult{Text: p.Perf()}, nil
	})
}

// LogSource exposes the machine's recent formatted log tail. Implemented by app.appControl.
type LogSource interface {
	LogTail(max int, filter string) string
}

// RegisterLogs exposes the peer's log tail over the peer link (read-only) - the remote half of
// ctl remote-logs.
func RegisterLogs(e *Endpoint, s LogSource) {
	if e == nil || s == nil {
		return
	}
	e.Register(MethodAppLogs, func(_ context.Context, _ string, raw json.RawMessage) (any, error) {
		var p LogsParams
		_ = json.Unmarshal(raw, &p)
		return TextResult{Text: s.LogTail(p.Max, p.Filter)}, nil
	})
}

// EncoderScanSource exposes the machine's live encoder-utilization scan + placement plan (text).
// Implemented by app.appControl over the encoderscan library.
type EncoderScanSource interface {
	EncoderScan() string
}

// RegisterEncoderScan exposes the peer's encoder scan over the peer link (read-only). The handler
// blocks ~300ms for the PDH GPU-engine sampling pass - well inside serveTimeout, no GPU encode.
func RegisterEncoderScan(e *Endpoint, s EncoderScanSource) {
	if e == nil || s == nil {
		return
	}
	e.Register(MethodAppEncoderScan, func(context.Context, string, json.RawMessage) (any, error) {
		return TextResult{Text: s.EncoderScan()}, nil
	})
}

// Profiler captures this process's runtime/pprof artifacts. Implemented by app.appControl.
// CPUProfile must clamp seconds so the (blocking) capture finishes inside serveTimeout, and
// honor ctx cancellation by stopping early.
type Profiler interface {
	CPUProfile(ctx context.Context, seconds int) ([]byte, error)
	HeapProfile() ([]byte, error)
	Goroutines() string
}

// RegisterPprof exposes CPU/heap profiles + goroutine dumps over the peer link (read-only) - the
// remote halves of ctl remote-pprof-cpu / remote-pprof-heap / remote-goroutines.
func RegisterPprof(e *Endpoint, p Profiler) {
	if e == nil || p == nil {
		return
	}
	e.Register(MethodAppPprofCPU, func(ctx context.Context, _ string, raw json.RawMessage) (any, error) {
		var pr PprofCPUParams
		_ = json.Unmarshal(raw, &pr)
		b, err := p.CPUProfile(ctx, pr.Seconds)
		if err != nil {
			return nil, err
		}
		return PprofResult{DataBase64: base64.StdEncoding.EncodeToString(b)}, nil
	})
	e.Register(MethodAppPprofHeap, func(context.Context, string, json.RawMessage) (any, error) {
		b, err := p.HeapProfile()
		if err != nil {
			return nil, err
		}
		return PprofResult{DataBase64: base64.StdEncoding.EncodeToString(b)}, nil
	})
	e.Register(MethodAppGoroutines, func(context.Context, string, json.RawMessage) (any, error) {
		return TextResult{Text: p.Goroutines()}, nil
	})
}

// Updater triggers a self-update (+relaunch) on the controlled machine.
type Updater interface {
	SelfUpdate() string // returns a status ("updating"/"disabled"/"already updating")
}

// RegisterUpdater lets a paired peer trigger this machine's self-update+relaunch - so a desk
// instance can update a headset PC the instant a CI build lands. SelfUpdate returns immediately
// (the download/apply/relaunch runs in the background), so the RPC reply arrives before restart.
func RegisterUpdater(e *Endpoint, u Updater) {
	if e == nil || u == nil {
		return
	}
	e.Register(MethodSelfUpdate, func(context.Context, string, json.RawMessage) (any, error) {
		return TextResult{Text: u.SelfUpdate()}, nil
	})
}

// RegisterMotionSync exposes read-only pull of this machine's Motion Studio recordings
// (dir/*.json) so a paired peer can replicate them. list returns name+size+sha256 for diffing;
// get returns one recording's JSON base64. Names are base filenames guarded against path
// traversal (get can only read within dir). No-op if dir is empty.
func RegisterMotionSync(e *Endpoint, dir string) {
	if e == nil || dir == "" {
		return
	}
	hashes := newFileHashCache()
	e.Register(MethodMotionList, func(context.Context, string, json.RawMessage) (any, error) {
		return hashes.motionList(dir)
	})
	e.Register(MethodMotionGet, func(_ context.Context, _ string, raw json.RawMessage) (any, error) {
		var p MotionGetParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		name, ok := safeMotionName(p.Name)
		if !ok {
			return nil, errors.New("invalid motion name")
		}
		b, err := os.ReadFile(filepath.Join(dir, name+".json"))
		if err != nil {
			return nil, err
		}
		return MotionGetResult{JSONBase64: base64.StdEncoding.EncodeToString(b)}, nil
	})
}

// motionList enumerates dir/*.json → name+size+sha256. Missing dir ⇒ empty (not an error).
// sha256 is served from the file-hash cache (re-read+hash only when size/mtime move) so the
// per-connect/per-Sync list RPC no longer re-hashes every recording.
func (c *fileHashCache) motionList(dir string) (MotionListResult, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return MotionListResult{Items: []MotionMeta{}}, nil
		}
		return MotionListResult{}, err
	}
	items := make([]MotionMeta, 0, len(ents))
	for _, ent := range ents {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".json") {
			continue
		}
		fi, err := ent.Info()
		if err != nil {
			continue // entry vanished between ReadDir and Info
		}
		sha, size, err := c.hash(filepath.Join(dir, ent.Name()), fi)
		if err != nil {
			continue // skip unreadable entries; a partial list still lets the peer sync the rest
		}
		items = append(items, MotionMeta{
			Name:   strings.TrimSuffix(ent.Name(), ".json"),
			Size:   size,
			SHA256: sha,
		})
	}
	return MotionListResult{Items: items}, nil
}

// safeMotionName accepts a base recording name only: non-empty, no path separators, no "..".
// Blocks traversal so get can't escape the recordings dir.
func safeMotionName(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return "", false
	}
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") || filepath.Base(name) != name {
		return "", false
	}
	return name, true
}

// vrmExts are the avatar-model extensions VRM sync serves + pulls (keep in sync with
// assetsync.vrmExts). ".json" = physbones sidecars (vrmdyn) riding along with models.
var vrmExts = []string{".vrm", ".glb", ".gltf", ".fbx", ".json"}

// vrmChunkMax caps one getChunk read so its base64 result stays under maxControlFrame (24 MiB): 8 MiB
// raw → ~10.7 MiB base64 + envelope.
const vrmChunkMax = 8 << 20

// RegisterVRMSync exposes read-only CHUNKED pull of this machine's avatar models (dir/*.vrm|glb|gltf|fbx)
// so a paired peer can replicate them. list returns name+size+sha256 for diffing; getChunk returns one
// [offset,len) slice base64 (files are too large for whole-file base64 in a 24 MiB frame). Names are
// base filenames guarded against traversal (reads stay within dir). No-op if dir is empty.
func RegisterVRMSync(e *Endpoint, dir string) {
	if e == nil || dir == "" {
		return
	}
	hashes := newFileHashCache()
	e.Register(MethodVRMList, func(context.Context, string, json.RawMessage) (any, error) {
		return hashes.vrmList(dir)
	})
	e.Register(MethodVRMGetChunk, func(_ context.Context, _ string, raw json.RawMessage) (any, error) {
		var p VRMGetChunkParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		return readVRMChunk(dir, p)
	})
}

// readVRMChunk reads [Offset, Offset+min(Len,vrmChunkMax)) of dir/<name> and base64-encodes it. EOF is
// set when the read reached end-of-file (the last chunk). Name is traversal-guarded to stay within dir.
func readVRMChunk(dir string, p VRMGetChunkParams) (VRMGetChunkResult, error) {
	name, ok := safeVRMName(p.Name)
	if !ok {
		return VRMGetChunkResult{}, errors.New("invalid vrm name")
	}
	if p.Offset < 0 || p.Len <= 0 {
		return VRMGetChunkResult{}, errors.New("invalid chunk range")
	}
	f, err := os.Open(filepath.Join(dir, name))
	if err != nil {
		return VRMGetChunkResult{}, err
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, min(p.Len, vrmChunkMax))
	read, err := f.ReadAt(buf, p.Offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return VRMGetChunkResult{}, err
	}
	// ReadAt reports io.EOF when it fills fewer than len(buf) bytes at end-of-file → this is the last chunk.
	return VRMGetChunkResult{DataBase64: base64.StdEncoding.EncodeToString(buf[:read]), EOF: errors.Is(err, io.EOF)}, nil
}

// vrmList enumerates dir/*.vrm|glb|gltf → name(with ext)+size+sha256. Missing dir ⇒ empty (not error).
// sha256 is served from the file-hash cache (re-read+hash only when size/mtime move) so the
// per-connect/per-Sync list RPC no longer re-hashes every (large) avatar model.
func (c *fileHashCache) vrmList(dir string) (VRMListResult, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return VRMListResult{Items: []VRMMeta{}}, nil
		}
		return VRMListResult{}, err
	}
	items := make([]VRMMeta, 0, len(ents))
	for _, ent := range ents {
		if ent.IsDir() || !hasVRMExt(ent.Name()) {
			continue
		}
		fi, err := ent.Info()
		if err != nil {
			continue // entry vanished between ReadDir and Info
		}
		sha, size, err := c.hash(filepath.Join(dir, ent.Name()), fi)
		if err != nil {
			continue // skip unreadable; a partial list still lets the peer sync the rest
		}
		items = append(items, VRMMeta{Name: ent.Name(), Size: size, SHA256: sha})
	}
	return VRMListResult{Items: items}, nil
}

// vrmList (back-compat / test entry): fresh cache, no cross-call memo.
func vrmList(dir string) (VRMListResult, error) { return newFileHashCache().vrmList(dir) }

// fileHashCache memoizes sha256(file) keyed by (path,size,mtime). The peer avatar/motion listing
// re-runs on every peer connect/disconnect + manual Sync; re-hashing tens-hundreds of MB per model
// each time is the cost this removes. In-proc + session-scoped: RegisterVRMSync/RegisterMotionSync
// carry no *store.Store (their signatures stay stable, tests unbroken), so it doesn't survive a
// restart — acceptable, the churn it targets is within a session. Stale entries are replaced when
// size/mtime move; a deleted file leaves one small path-keyed entry (bounded dir → negligible).
type fileHashCache struct {
	mu sync.Mutex
	m  map[string]fileHashEntry
}

type fileHashEntry struct {
	size  int64 // bytes hashed (== stat size for a fully-read regular file)
	mtime int64 // unix seconds
	sha   string
}

func newFileHashCache() *fileHashCache { return &fileHashCache{m: map[string]fileHashEntry{}} }

// hash returns sha256(path) hex + the hashed byte count, reusing the cached value when fi's
// size+mtime match the stored key. fi is the caller's single stat (ent.Info()) — TOCTOU-safe:
// one FileInfo gates both the cache key and, on a miss, the read. (size,sha) are always a
// consistent pair from the same read; a short read self-heals (stored size < next stat size ⇒
// re-hash).
func (c *fileHashCache) hash(path string, fi os.FileInfo) (sha string, size int64, err error) {
	fsize, mtime := fi.Size(), fi.ModTime().Unix()
	c.mu.Lock()
	if e, ok := c.m[path]; ok && e.size == fsize && e.mtime == mtime {
		c.mu.Unlock()
		return e.sha, e.size, nil
	}
	c.mu.Unlock()
	b, err := os.ReadFile(path)
	if err != nil {
		return "", 0, err
	}
	sum := sha256.Sum256(b)
	sha = hex.EncodeToString(sum[:])
	size = int64(len(b))
	c.mu.Lock()
	c.m[path] = fileHashEntry{size: size, mtime: mtime, sha: sha}
	c.mu.Unlock()
	return sha, size, nil
}

func hasVRMExt(name string) bool {
	return slices.Contains(vrmExts, strings.ToLower(filepath.Ext(name)))
}

// safeVRMName accepts a base filename with an allowed avatar extension: no path separators, no "..".
func safeVRMName(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") || filepath.Base(name) != name {
		return "", false
	}
	if !hasVRMExt(name) {
		return "", false
	}
	return name, true
}

// captureToBase64 runs capture into a temp PNG, reads it back, and base64-encodes it.
func captureToBase64(capture func(path string) bool, ext string) (any, error) {
	f, err := os.CreateTemp("", "rave-mate-shot-*"+ext)
	if err != nil {
		return nil, err
	}
	path := f.Name()
	_ = f.Close()
	defer func() { _ = os.Remove(path) }()
	if !capture(path) {
		return nil, errors.New("capture failed (no UI / vr-view disabled / unsupported / not found)")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ScreenshotResult{PNGBase64: base64.StdEncoding.EncodeToString(raw)}, nil
}

// filterTracks keeps tracks whose title/artist/album contains query (case-insensitive). Empty
// query keeps all.
func filterTracks(in []musiclib.Track, query string) []musiclib.Track {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return in
	}
	out := make([]musiclib.Track, 0, len(in))
	for _, t := range in {
		if strings.Contains(strings.ToLower(t.Title), q) ||
			strings.Contains(strings.ToLower(t.Artist), q) ||
			strings.Contains(strings.ToLower(t.Album), q) {
			out = append(out, t)
		}
	}
	return out
}

// pageTracks slices [offset, offset+limit) defensively (limit≤0 ⇒ 200). Always non-nil.
func pageTracks(in []musiclib.Track, offset, limit int) []musiclib.Track {
	if limit <= 0 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= len(in) {
		return []musiclib.Track{}
	}
	end := min(offset+limit, len(in))
	return in[offset:end]
}

// RegisterMedia exposes ad-hoc transcode over the peer's worker pool (the controlled machine's
// own filesystem). One blocking call per transcode: the controller waits on the result with a
// generous timeout (mirrors automations.runManual). Output lands in a 'rave-mate-transcoded'
// folder beside the source - the original is untouched.
func RegisterMedia(e *Endpoint, hub *jobs.Hub) {
	if e == nil || hub == nil {
		return
	}
	e.Register(MethodMediaTranscode, func(ctx context.Context, _ string, raw json.RawMessage) (any, error) {
		var p TranscodeParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.Input) == "" {
			return nil, errors.New("input required")
		}
		pr := p.Preset
		// Drop the controller's resolved encoder name (its GPU isn't ours); keep pr.Accel so
		// the worker re-resolves against THIS (controlled) machine's hardware.
		pr.EncoderOverride = ""
		id := pr.ID
		if id == "" {
			id = "custom"
		}
		base := strings.TrimSuffix(filepath.Base(p.Input), filepath.Ext(p.Input))
		out := filepath.Join(filepath.Dir(p.Input), "rave-mate-transcoded", base+"-"+id+pr.Ext())
		params := map[string]any{
			"input": p.Input, "output": out, "preset": pr,
			"trimStart": p.TrimStart, "trimEnd": p.TrimEnd,
		}
		jobID := fmt.Sprintf("remote-%d", remoteJobSeq.Add(1))
		done := make(chan jobs.EndResult, 1)
		hub.Start(jobID, params, nil, func(r jobs.EndResult) { done <- r })
		select {
		case <-ctx.Done():
			hub.Cancel(jobID)
			return nil, ctx.Err()
		case r := <-done:
			switch {
			case r.Canceled:
				return nil, errors.New("canceled")
			case !r.OK:
				return nil, errors.New(r.Error)
			default:
				return TranscodeResult{Output: out}, nil
			}
		}
	})
}

// ── vrchat federation (read-only proxy over the local VRChat session) ─────────

// VrcSource is the narrow manager view the vrchat.* handlers serve from
// (satisfied by *vrchat.Manager). The session itself never crosses the link.
type VrcSource interface {
	State() vrchat.State
	Client() *vrchat.Client
	CurrentUserID() string
}

// RegisterVrchat serves this instance's VRChat link to paired peers: status always answers
// (linked=false without a session) so controllers can discover the serving peer; the data
// methods mirror the exact reads the Worlds surfaces make locally (friends browser, group
// pickers, publish-time group-role expansion). Read-only - no status writes, no auth verbs.
func RegisterVrchat(e *Endpoint, src VrcSource) {
	if e == nil || src == nil {
		return
	}
	cli := func() (*vrchat.Client, error) {
		if !src.State().LoggedIn || src.Client() == nil {
			return nil, fmt.Errorf("vrchat not linked on this peer")
		}
		return src.Client(), nil
	}
	e.Register(MethodVrcStatus, func(context.Context, string, json.RawMessage) (any, error) {
		st := src.State()
		return VrcStatus{Linked: st.LoggedIn, UserID: st.UserID, DisplayName: st.DisplayName}, nil
	})
	e.Register(MethodVrcFriends, func(ctx context.Context, _ string, raw json.RawMessage) (any, error) {
		c, err := cli()
		if err != nil {
			return nil, err
		}
		var p VrcFriendsParams
		_ = json.Unmarshal(raw, &p)
		return c.Friends(ctx, p.Offset, p.N, p.Offline)
	})
	e.Register(MethodVrcUserGroups, func(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
		c, err := cli()
		if err != nil {
			return nil, err
		}
		return c.UserGroups(ctx, src.CurrentUserID())
	})
	e.Register(MethodVrcSearchGroups, func(ctx context.Context, _ string, raw json.RawMessage) (any, error) {
		c, err := cli()
		if err != nil {
			return nil, err
		}
		var p VrcSearchGroupsParams
		_ = json.Unmarshal(raw, &p)
		return c.SearchGroups(ctx, p.Query, p.Offset, p.N)
	})
	e.Register(MethodVrcGroupRoles, func(ctx context.Context, _ string, raw json.RawMessage) (any, error) {
		c, err := cli()
		if err != nil {
			return nil, err
		}
		var p VrcGroupRolesParams
		_ = json.Unmarshal(raw, &p)
		return c.GroupRoles(ctx, p.GroupID)
	})
	e.Register(MethodVrcGroupMembers, func(ctx context.Context, _ string, raw json.RawMessage) (any, error) {
		c, err := cli()
		if err != nil {
			return nil, err
		}
		var p VrcGroupMembersParams
		_ = json.Unmarshal(raw, &p)
		return c.GroupMembers(ctx, p.GroupID, p.RoleID, p.Offset, p.N)
	})
	e.Register(MethodVrcProxy, func(ctx context.Context, _ string, raw json.RawMessage) (any, error) {
		c, err := cli()
		if err != nil {
			return nil, err
		}
		var p VrcProxyParams
		_ = json.Unmarshal(raw, &p)
		m := strings.ToUpper(strings.TrimSpace(p.Method))
		switch m {
		case "GET", "POST", "PUT", "DELETE":
		default:
			return nil, fmt.Errorf("vrchat.proxy: method %q not allowed", p.Method)
		}
		pq := p.PathQuery
		if !strings.HasPrefix(pq, "/") || strings.Contains(pq, "://") {
			return nil, fmt.Errorf("vrchat.proxy: path must be API-relative")
		}
		// auth flows stay local-only: a peer must never re-auth, verify 2FA, or
		// kill the serving session. GET /auth/user is the one exception - the
		// pure session read every client uses to fetch the current user (no
		// Authorization header ever crosses the proxy, so it cannot re-login).
		low := strings.ToLower(pq)
		isUserRead := m == "GET" && (low == "/auth/user" || strings.HasPrefix(low, "/auth/user?"))
		if !isUserRead && (strings.HasPrefix(low, "/auth") || strings.HasPrefix(low, "/logout")) {
			return nil, fmt.Errorf("vrchat.proxy: auth endpoints are local-only")
		}
		var body []byte
		if p.BodyB64 != "" {
			if body, err = base64.StdEncoding.DecodeString(p.BodyB64); err != nil {
				return nil, fmt.Errorf("vrchat.proxy: bad body: %w", err)
			}
		}
		status, respBody, err := c.Raw(ctx, m, pq, body, p.ContentType)
		if err != nil {
			return nil, err
		}
		return VrcProxyResult{Status: status, BodyB64: base64.StdEncoding.EncodeToString(respBody)}, nil
	})
}

package playsync

// Media sync: uploads analyzed waveform peak overviews (PUT …/waveform) + embedded cover art
// (PUT …/artwork) for server library rows. Separate from SyncLibrary because waveform probing
// is an ffmpeg decode per track (hours for a full library) - runs budgeted + resumable, with
// the libdb.media_sync ledger making re-runs cheap (already-synced tracks skip the probe
// entirely). Probes run sequentially: the background probe worker pool is capped at one
// process, so a local pool would only queue on it anyway.
//
// Privacy invariant (commit 17c358c): local file paths NEVER go on the wire - payloads are
// peak buckets, durations, and raw image bytes only. Paths stay between us and the worker.

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"

	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/musiclib"
)

const (
	// DefaultWaveformBudget caps ffmpeg peak probes per run (decode-expensive; artwork is
	// cheap and always processed in full).
	DefaultWaveformBudget = 500
	peaksBuckets          = 8192
	listPage              = 200 // GET /library page size (backend cap)
	maxListPages          = 2000

	// media_sync sentinels: non-empty = "done", skip the probe on re-runs.
	artNone        = "none"        // no embedded picture
	artUnsupported = "unsupported" // can't recompress to a permitted type/size
)

// MediaResult reports one media sync run (also the live-progress snapshot).
type MediaResult struct {
	Candidates int `json:"total_candidates"` // local tracks with a file on disk (deduped)
	Waveforms  int `json:"waveforms_uploaded"`
	Artwork    int `json:"artwork_uploaded"`
	Skipped    int `json:"skipped"`             // already synced / no lib id / no art
	Failed     int `json:"failed"`              // probe or upload failures (after one retry)
	Remaining  int `json:"remaining"`           // waveforms still pending after this run
	Unmatched  int `json:"unmatched,omitempty"` // backfill: no server row matched
}

// SyncMedia probes + uploads waveforms (budget per run; <=0 = DefaultWaveformBudget) and
// artwork (all) for every local track with a known server library row and a file on disk.
// Deterministic order (artist, title), idempotent via media_sync, one retry per upload.
// onProgress (may be nil) receives periodic snapshots for live status.
func (s *Syncer) SyncMedia(ctx context.Context, budget int, onProgress func(MediaResult)) (MediaResult, error) {
	if s == nil || s.lib == nil {
		return MediaResult{}, fmt.Errorf("playsync: no library")
	}
	if s.probe == nil {
		return MediaResult{}, fmt.Errorf("playsync: no probe worker")
	}
	token, err := s.tok()
	if err != nil {
		return MediaResult{}, err
	}
	if budget <= 0 {
		budget = DefaultWaveformBudget
	}

	tracks, err := s.lib.LoadAllTracks()
	if err != nil {
		return MediaResult{}, err
	}
	type cand struct {
		hash string
		t    musiclib.Track
	}
	var cands []cand
	seen := map[string]bool{}
	for _, t := range tracks { // LoadAllTracks orders by artist, title → deterministic + resumable
		hash := libdb.TrackHash(t.Artist, t.Title, 0)
		if t.Title == "" || seen[hash] || t.Path == "" {
			continue
		}
		if _, err := os.Stat(t.Path); err != nil {
			continue // file gone - nothing to probe
		}
		seen[hash] = true
		cands = append(cands, cand{hash: hash, t: t})
	}
	res := MediaResult{Candidates: len(cands)}

	libIDs, err := s.lib.LibraryTrackIDs()
	if err != nil {
		return res, err
	}
	// Libraries synced before lib ids were persisted have links but no lib_… ids - backfill
	// from GET /library, matched by the shared dedup identity (TrackHash over artist|title).
	missing := 0
	for _, c := range cands {
		if libIDs[c.hash] == "" {
			missing++
		}
	}
	if missing > 0 {
		matched, err := s.backfillLibraryIDs(ctx, token, libIDs)
		if err != nil {
			s.warn("library id backfill", err)
		}
		res.Unmatched = 0
		for _, c := range cands {
			if libIDs[c.hash] == "" {
				res.Unmatched++
			}
		}
		s.info("library id backfill", map[string]any{"missing": missing, "matched": matched, "unmatched": res.Unmatched})
	}

	media, err := s.lib.MediaSyncRows()
	if err != nil {
		return res, err
	}
	pendingWave := 0
	for _, c := range cands {
		if libIDs[c.hash] != "" && media[c.hash].WaveformHash == "" {
			pendingWave++
		}
	}
	s.info("media sync started", map[string]any{
		"candidates": len(cands), "pendingWaveforms": pendingWave, "budget": budget,
	})

	waveAttempts := 0
	progress := func() {
		res.Remaining = max(pendingWave-res.Waveforms, 0)
		if onProgress != nil {
			onProgress(res)
		}
	}
	for i, c := range cands {
		if ctx.Err() != nil {
			break
		}
		libID := libIDs[c.hash]
		if libID == "" {
			res.Skipped++
			continue
		}
		row := media[c.hash]
		did := false
		// Artwork first - cheap (tag read), always full coverage.
		if row.ArtworkHash == "" {
			did = true
			s.syncArtwork(ctx, token, c.hash, c.t.Path, libID, &res)
		}
		// Waveform - ffmpeg decode; budgeted (failures consume budget so a broken
		// decoder can't burn the whole library in one run).
		if row.WaveformHash == "" && waveAttempts < budget {
			did = true
			waveAttempts++
			s.syncWaveform(ctx, token, c.hash, c.t.Path, libID, &res)
		}
		if !did {
			res.Skipped++
		}
		if (i+1)%25 == 0 {
			progress()
			s.info("media sync progress", map[string]any{
				"done": i + 1, "of": len(cands), "waveforms": res.Waveforms, "artwork": res.Artwork, "failed": res.Failed,
			})
		}
	}
	progress()
	s.info("media synced", map[string]any{
		"candidates": res.Candidates, "waveforms": res.Waveforms, "artwork": res.Artwork,
		"skipped": res.Skipped, "failed": res.Failed, "remaining": res.Remaining,
	})
	return res, nil
}

// syncWaveform probes peak buckets for one file and uploads them (one retry). Ledger is
// written only on success, so failures retry on the next run.
func (s *Syncer) syncWaveform(ctx context.Context, token, hash, path, libID string, res *MediaResult) {
	raw, err := s.probe.RunBackground(ctx, "probe", "probe.peaks", map[string]any{"path": path, "buckets": peaksBuckets})
	if err != nil {
		res.Failed++
		s.warn("waveform probe", err)
		return
	}
	var r struct {
		Peaks  string  `json:"peaks"`
		DurSec float64 `json:"durationSeconds"`
	}
	if json.Unmarshal(raw, &r) != nil || r.Peaks == "" {
		res.Failed++
		s.warn("waveform probe", fmt.Errorf("empty peaks for %q", path))
		return
	}
	peaks, err := base64.StdEncoding.DecodeString(r.Peaks)
	if err != nil || len(peaks) == 0 || len(peaks) > 1<<16 {
		res.Failed++
		s.warn("waveform probe", fmt.Errorf("bad peaks size %d", len(peaks)))
		return
	}
	sum := sha256.Sum256(peaks)
	wfHash := hex.EncodeToString(sum[:])
	durMs := int(r.DurSec * 1000)
	if _, err := s.api.UploadTrackWaveform(ctx, token, libID, r.Peaks, durMs); err != nil {
		if _, err = s.api.UploadTrackWaveform(ctx, token, libID, r.Peaks, durMs); err != nil {
			res.Failed++
			s.warn("waveform upload", err)
			return
		}
	}
	if err := s.lib.SaveMediaSync(hash, wfHash, ""); err != nil {
		s.warn("save media_sync", err)
	}
	res.Waveforms++
}

// syncArtwork extracts embedded cover art for one file and uploads it (one retry). No art /
// un-recompressable art records a sentinel so re-runs skip the extraction.
func (s *Syncer) syncArtwork(ctx context.Context, token, hash, path, libID string, res *MediaResult) {
	raw, err := s.probe.RunBackground(ctx, "probe", "probe.artwork", map[string]any{"path": path})
	if err != nil {
		res.Failed++
		s.warn("artwork probe", err)
		return
	}
	var r struct {
		Mime string `json:"mime"`
		Data []byte `json:"data"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		res.Failed++
		s.warn("artwork probe", err)
		return
	}
	if len(r.Data) == 0 { // nothing embedded - remember so re-runs stay cheap
		if err := s.lib.SaveMediaSync(hash, "", artNone); err != nil {
			s.warn("save media_sync", err)
		}
		return
	}
	data, ct, ok, reason := prepareArtwork(r.Data)
	if !ok {
		s.info("artwork skipped", map[string]any{"reason": reason, "bytes": len(r.Data)})
		if err := s.lib.SaveMediaSync(hash, "", artUnsupported); err != nil {
			s.warn("save media_sync", err)
		}
		return
	}
	sum := sha256.Sum256(data)
	artHash := hex.EncodeToString(sum[:])
	if err := s.api.UploadTrackArtwork(ctx, token, libID, ct, data); err != nil {
		if err = s.api.UploadTrackArtwork(ctx, token, libID, ct, data); err != nil {
			res.Failed++
			s.warn("artwork upload", err)
			return
		}
	}
	if err := s.lib.SaveMediaSync(hash, "", artHash); err != nil {
		s.warn("save media_sync", err)
	}
	res.Artwork++
}

// backfillLibraryIDs pages GET /library and maps server rows to local identities by the shared
// dedup key (duration-0 TrackHash over artist_text|title; local rows carry no ISRC). Matches
// are persisted and merged into ids in place. Returns the number matched.
func (s *Syncer) backfillLibraryIDs(ctx context.Context, token string, ids map[string]string) (int, error) {
	serverByHash := map[string]string{}
	for page := 0; page < maxListPages; page++ {
		rows, err := s.api.ListLibraryTracks(ctx, token, listPage, page*listPage)
		if err != nil {
			return 0, err
		}
		for _, r := range rows {
			if r.ID == "" || r.Title == "" {
				continue
			}
			h := libdb.TrackHash(r.ArtistText, r.Title, 0)
			if _, dup := serverByHash[h]; !dup {
				serverByHash[h] = r.ID
			}
		}
		if len(rows) < listPage {
			break
		}
	}
	found := map[string]string{}
	for h, id := range serverByHash {
		if ids[h] == "" {
			found[h] = id
		}
	}
	if len(found) == 0 {
		return 0, nil
	}
	if err := s.lib.SaveLibraryTrackIDs(found); err != nil {
		return 0, err
	}
	for h, id := range found {
		ids[h] = id
	}
	return len(found), nil
}

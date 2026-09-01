package playsync

// Library metadata sync: uploads the full local library's tags to the backend in batches
// (POST /library/tracks/bulk), incrementally - the libdb.library_sync payload-hash ledger
// skips unchanged tracks on re-runs - and caches returned canonical links in track_links.
// Tracks are keyed by the duration-0 TrackHash, the same identity the identify flow stores
// and setfp keys fingerprints on, so links from either path coexist.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"rave.page/mate/internal/api"
	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/musiclib"
)

// libraryBatch is the per-request upload row cap (backend caps at 500).
const libraryBatch = 200

// maxLibraryBatchBytes is a conservative client-side ceiling on one bulk request body. Metadata-
// only rows are small (a few hundred bytes), so a full libraryBatch (200) is comfortably under
// 1 MiB. A Chromaprint fingerprint_b64 adds ~2-6 KB per row, so once coverage exists a 200-row
// batch could carry ~1-2 MB - past a typical server/proxy request-body limit. We therefore close
// a batch early once its accumulated JSON would cross this budget (see libraryBatches). This only
// shrinks print-bearing batches; a print-less sync still ships the full 200 rows per request.
const maxLibraryBatchBytes = 1 << 20 // 1 MiB

// item is one queued upload row: its identity hash, the payload change hash (skip-unchanged
// ledger), the wire payload, and its marshaled JSON size (byte-aware batching).
type item struct {
	hash, payloadHash string
	payload           api.LibraryTrack
	size              int
}

// LibraryResult reports one library sync run.
type LibraryResult struct {
	Total    int `json:"total"`
	Uploaded int `json:"uploaded"`
	Skipped  int `json:"skipped"`
	Linked   int `json:"linked"`
	Failed   int `json:"failed"`
}

// SyncLibrary uploads every local track's metadata, skipping rows whose payload is unchanged
// since the last run. Returns ErrUnauthenticated when no token. Per-batch failures retry once,
// then count as failed and the sync continues. Read-only over tracks (writes only library_sync
// + track_links) - safe alongside normal app use.
func (s *Syncer) SyncLibrary(ctx context.Context) (LibraryResult, error) {
	if s == nil || s.lib == nil {
		return LibraryResult{}, fmt.Errorf("playsync: no library")
	}
	token, err := s.tok()
	if err != nil {
		return LibraryResult{}, err
	}
	tracks, err := s.lib.LoadAllTracks()
	if err != nil {
		return LibraryResult{}, err
	}
	synced, err := s.lib.LibrarySyncHashes()
	if err != nil {
		return LibraryResult{}, err
	}
	dropRows, err := s.lib.DropRows() // incl. cleared tombstones - see libraryPayload send rule
	if err != nil {
		return LibraryResult{}, err
	}

	res := LibraryResult{Total: len(tracks)}
	var queue []item
	seen := make(map[string]bool, len(tracks))
	for _, t := range tracks {
		hash := libdb.TrackHash(t.Artist, t.Title, 0)
		if t.Title == "" || seen[hash] { // untitled can't sync; dup hashes upload once
			res.Skipped++
			continue
		}
		seen[hash] = true
		fp, _, _ := s.lib.FingerprintForTrack(hash)
		drops, hasDrops := dropRows[t.Path]
		p := libraryPayload(t, fp, drops, hasDrops)
		pb, ph := marshalPayload(p)
		if synced[hash] == ph {
			res.Skipped++
			continue
		}
		queue = append(queue, item{hash: hash, payloadHash: ph, payload: p, size: len(pb)})
	}

	batchList := libraryBatches(queue)
	s.info("library sync started", map[string]any{"tracks": res.Total, "toUpload": len(queue), "batches": len(batchList)})
	processed := 0 // rows in batches already begun; the rest count as failed on ctx cancel
	for bi, batch := range batchList {
		if ctx.Err() != nil {
			res.Failed += len(queue) - processed
			break
		}
		processed += len(batch)
		payloads := make([]api.LibraryTrack, len(batch))
		for i, it := range batch {
			payloads[i] = it.payload
		}
		resp, err := s.api.UploadLibraryTracks(ctx, token, payloads)
		if err != nil { // one retry, then count + continue
			resp, err = s.api.UploadLibraryTracks(ctx, token, payloads)
		}
		if err != nil {
			res.Failed += len(batch)
			s.warn("library batch upload", err)
			continue
		}
		now := time.Now()
		ok := make(map[string]libdb.LibrarySyncRow, len(batch))
		for _, r := range resp.Results {
			if r.Index < 0 || r.Index >= len(batch) {
				continue
			}
			it := batch[r.Index]
			if r.Status == "error" {
				res.Failed++
				if res.Failed <= 8 { // surface the first few per-row rejections
					s.info("library row rejected", map[string]any{
						"title": it.payload.Title, "artist": it.payload.ArtistText, "err": r.Error,
					})
				}
				continue
			}
			res.Uploaded++
			// library_track_id (lib_…) is the address for media PUTs (waveform/artwork).
			ok[it.hash] = libdb.LibrarySyncRow{PayloadHash: it.payloadHash, LibraryTrackID: r.LibraryTrackID}
			if r.CanonicalTrackID != "" {
				if err := s.lib.SaveTrackLink(libdb.TrackLink{
					TrackHash: it.hash, TrackID: r.CanonicalTrackID,
					Confidence: r.MatchConfidence, SyncedAt: now,
				}); err != nil {
					s.warn("save track link", err)
				} else {
					res.Linked++
				}
			}
		}
		if err := s.lib.SaveLibrarySyncBatch(ok); err != nil {
			s.warn("save library_sync ledger", err)
		}
		if (bi+1)%10 == 0 {
			s.info("library sync progress", map[string]any{
				"batch": bi + 1, "of": len(batchList), "uploaded": res.Uploaded, "linked": res.Linked, "failed": res.Failed,
			})
		}
	}
	s.info("library synced", map[string]any{
		"total": res.Total, "uploaded": res.Uploaded, "skipped": res.Skipped, "linked": res.Linked, "failed": res.Failed,
	})
	return res, nil
}

// libraryPayload maps a local track to its wire payload. fp may be "" (omitted).
// Drops send rule: drops_ms goes on the wire iff the track has a track_drops row
// (hasDrops) - including a cleared `[]` tombstone, which the API treats as "remove all
// drops"; tracks without a row omit the field (server keeps whatever it has). Drop edits
// need no change-log plumbing: drops are part of this payload, so the payload-hash ledger
// re-uploads the track on any drop change.
func libraryPayload(t musiclib.Track, fp string, drops []float64, hasDrops bool) api.LibraryTrack {
	p := api.LibraryTrack{
		Title: t.Title, ArtistText: t.Artist, Album: t.Album, Label: t.Label, Genre: t.Genre,
		BPM: t.BPM, Key: t.Key, FingerprintB64: fp, // path stays local - too personal for the wire
		Cues: wireCues(t.Cues), Beatgrid: wireGrid(t.Beatgrid),
		Rating: normRating(t.Rating), PlayCount: t.PlayCount, Comment: t.Comment,
		ReleaseYear: releaseYear(t.ReleaseDate),
		// no ISRC in local libraries - omitted
	}
	if hasDrops {
		p.DropsMs = wireDrops(drops)
	}
	if t.DurationSec > 0 {
		p.DurationMs = int(math.Round(t.DurationSec * 1000))
	}
	if ts, ok := parseSourceDate(t.LastPlayed); ok {
		p.LastPlayedAt = ts.UTC().Format(time.RFC3339)
	}
	return p
}

// wireCues maps local cue points to the wire shape (nil stays nil for payload-hash stability).
func wireCues(cs []musiclib.CuePoint) []api.LibraryCue {
	if len(cs) == 0 {
		return nil
	}
	out := make([]api.LibraryCue, len(cs))
	for i, c := range cs {
		out[i] = api.LibraryCue{
			Name: c.Name, Kind: string(c.Kind), Type: c.Type,
			StartMs: c.StartMs, LenMs: c.LenMs, Hotcue: c.Hotcue,
		}
	}
	return out
}

// wireDrops maps local drop markers (float ms) to wire ints: rounded, negatives dropped,
// sorted asc, deduped. Always non-nil - callers gate on track_drops row presence, so an
// empty result marshals as `[]` (explicit clear) rather than being omitted.
func wireDrops(ds []float64) []int64 {
	out := make([]int64, 0, len(ds))
	for _, d := range ds {
		if v := int64(math.Round(d)); v >= 0 {
			out = append(out, v)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// wireGrid maps local grid markers to the wire shape. Beat=1: Traktor grid markers anchor downbeats.
func wireGrid(gs []musiclib.GridMarker) []api.LibraryGridMarker {
	if len(gs) == 0 {
		return nil
	}
	out := make([]api.LibraryGridMarker, len(gs))
	for i, g := range gs {
		out[i] = api.LibraryGridMarker{PositionMs: int64(math.Round(g.PositionMs)), BPM: g.BPM, Beat: 1}
	}
	return out
}

// normRating maps source ratings to the wire's 0-5 stars. Traktor stores 0-255 (51/star);
// values ≤5 pass through.
func normRating(r int) int {
	if r <= 5 {
		if r < 0 {
			return 0
		}
		return r
	}
	if n := (r + 25) / 51; n < 5 {
		return n
	}
	return 5
}

// marshalPayload marshals a payload once, returning the bytes and their sha256-hex change hash.
// Callers needing only the hash use payloadHash; the byte length feeds byte-aware batching so a
// single marshal serves both the change-ledger and the request-size accounting.
func marshalPayload(p api.LibraryTrack) ([]byte, string) {
	b, _ := json.Marshal(p)
	sum := sha256.Sum256(b)
	return b, hex.EncodeToString(sum[:])
}

// payloadHash is the change detector: sha256 hex over the payload's JSON. Field order is fixed
// by the struct, so identical payloads hash identical across runs.
func payloadHash(p api.LibraryTrack) string {
	_, h := marshalPayload(p)
	return h
}

// libraryBatches splits the upload queue into request batches capped by BOTH the row count
// (libraryBatch) and the JSON byte budget (maxLibraryBatchBytes). The byte cap only bites once
// rows carry fingerprints - see maxLibraryBatchBytes. A single row larger than the budget still
// ships alone (batches are never empty). Row order is preserved so item indices stay meaningful.
func libraryBatches(queue []item) [][]item {
	var batches [][]item
	start, bytes := 0, 0
	for i := range queue {
		rowBytes := queue[i].size + 1 // +1 approximates the JSON array separator
		if i > start && (i-start >= libraryBatch || bytes+rowBytes > maxLibraryBatchBytes) {
			batches = append(batches, queue[start:i])
			start, bytes = i, 0
		}
		bytes += rowBytes
	}
	if start < len(queue) {
		batches = append(batches, queue[start:])
	}
	return batches
}

// sourceDateLayouts covers the raw DJ-software date formats we import: Traktor NML "2024/3/10",
// ISO date / datetime, RFC3339.
var sourceDateLayouts = []string{time.RFC3339, "2006/1/2", "2006-1-2", "2006-1-2 15:04:05"}

// parseSourceDate parses a raw source date defensively; ok=false when unparsable (callers omit
// the field). All-digit strings > 1e8 are unix seconds (VirtualDJ LastPlay).
func parseSourceDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if n > 1e8 { // below = bare year / compact date / junk, not an epoch
			return time.Unix(n, 0), true
		}
		return time.Time{}, false
	}
	for _, l := range sourceDateLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// releaseYear extracts a plausible year from a raw release date ("2024/3/10", "2021",
// RFC3339, …); 0 when absent/unparsable.
func releaseYear(s string) int {
	if t, ok := parseSourceDate(s); ok {
		return t.Year()
	}
	s = strings.TrimSpace(s)
	for i := 0; i+4 <= len(s); i++ { // first standalone 4-digit run in 1000-2999
		y, err := strconv.Atoi(s[i : i+4])
		if err != nil || y < 1000 || y > 2999 {
			continue
		}
		if (i == 0 || !isDigit(s[i-1])) && (i+4 == len(s) || !isDigit(s[i+4])) {
			return y
		}
	}
	return 0
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

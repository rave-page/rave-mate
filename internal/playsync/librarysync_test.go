package playsync

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"rave.page/mate/internal/api"
	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/musiclib"
)

func (f *fakeAPI) UploadLibraryTracks(_ context.Context, _ string, tracks []api.LibraryTrack) (api.LibraryBulkResp, error) {
	f.libCalls++
	if f.libErr != nil {
		return api.LibraryBulkResp{}, f.libErr
	}
	f.libTracks = append(f.libTracks, tracks...)
	var resp api.LibraryBulkResp
	for i, t := range tracks {
		r := api.LibraryBulkResult{Index: i, LibraryTrackID: "lib_x", Status: "created"}
		if id, ok := f.libCanon[t.Title]; ok {
			r.CanonicalTrackID, r.MatchConfidence = id, 0.93
		}
		resp.Results = append(resp.Results, r)
	}
	return resp, nil
}

func seedLibrary(t *testing.T, d *libdb.DB, tracks ...musiclib.Track) {
	t.Helper()
	src, err := d.UpsertSource(musiclib.Source{App: "traktor", Path: "collection.nml"}, 1)
	if err != nil {
		t.Fatalf("source: %v", err)
	}
	sy, err := d.BeginTrackSync(src.ID)
	if err != nil {
		t.Fatalf("begin sync: %v", err)
	}
	for _, tr := range tracks {
		if err := sy.Add(tr); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	if _, err := sy.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func TestLibraryPayloadMapping(t *testing.T) {
	p := libraryPayload(musiclib.Track{
		Path: `C:\Music\a.mp3`, Title: "T", Artist: "A", Album: "Al", Genre: "Techno",
		Label: "L", Comment: "c", Key: "Ebm", BPM: 128.5, DurationSec: 200.4,
		PlayCount: 7, Rating: 4, ReleaseDate: "2021/3/10", LastPlayed: "2024/3/10",
		Cues:     []musiclib.CuePoint{{Name: "Drop", Kind: "cue", Type: 0, StartMs: 31250.5, Hotcue: 1}},
		Beatgrid: []musiclib.GridMarker{{PositionMs: 312.4, BPM: 128}},
	}, "FPB64", nil, false)
	if len(p.Cues) != 1 || p.Cues[0].StartMs != 31250.5 || p.Cues[0].Hotcue != 1 {
		t.Fatalf("cues wrong: %+v", p.Cues)
	}
	if len(p.Beatgrid) != 1 || p.Beatgrid[0].PositionMs != 312 || p.Beatgrid[0].Beat != 1 {
		t.Fatalf("beatgrid wrong: %+v", p.Beatgrid)
	}
	if p.DurationMs != 200400 {
		t.Fatalf("duration_ms = %d, want 200400", p.DurationMs)
	}
	if p.LastPlayedAt != "2024-03-10T00:00:00Z" {
		t.Fatalf("last_played_at = %q", p.LastPlayedAt)
	}
	if p.ReleaseYear != 2021 {
		t.Fatalf("release_year = %d", p.ReleaseYear)
	}
	if p.FingerprintB64 != "FPB64" || p.Rating != 4 || p.PlayCount != 7 {
		t.Fatalf("payload wrong: %+v", p)
	}
	// local path must never reach the wire
	if b, _ := json.Marshal(p); strings.Contains(string(b), "a.mp3") {
		t.Fatalf("payload leaks file path: %s", b)
	}
	if p.ISRC != "" {
		t.Fatalf("isrc should be omitted locally")
	}
}

func TestParseSourceDateForms(t *testing.T) {
	for _, tc := range []struct {
		in   string
		ok   bool
		date string // UTC date of the parse, when ok
	}{
		{"2024/3/10", true, "2024-03-10"},
		{"2024-03-10", true, "2024-03-10"},
		{"2024-03-10T12:30:00Z", true, "2024-03-10"},
		{"1672531200", true, "2023-01-01"}, // VirtualDJ unix seconds
		{"2021", false, ""},                // bare year is not a date
		{"garbage", false, ""},
		{"", false, ""},
	} {
		got, ok := parseSourceDate(tc.in)
		if ok != tc.ok {
			t.Fatalf("parse(%q) ok=%v, want %v", tc.in, ok, tc.ok)
		}
		if ok && got.UTC().Format("2006-01-02") != tc.date {
			t.Fatalf("parse(%q) = %v, want %s", tc.in, got, tc.date)
		}
	}
}

func TestReleaseYear(t *testing.T) {
	for in, want := range map[string]int{
		"2021/3/10": 2021, "2021": 2021, "2021-03-10": 2021,
		"released 2019 (remaster)": 2019, "20240310": 0, "": 0, "n/a": 0,
	} {
		if got := releaseYear(in); got != want {
			t.Fatalf("releaseYear(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestNormRating(t *testing.T) {
	for in, want := range map[int]int{
		-1: 0, 0: 0, 3: 3, 5: 5, // pass-through
		51: 1, 102: 2, 153: 3, 204: 4, 255: 5, 999: 5, // Traktor 51/star
	} {
		if got := normRating(in); got != want {
			t.Fatalf("normRating(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestPayloadHashStability(t *testing.T) {
	tr := musiclib.Track{Title: "T", Artist: "A", BPM: 128, DurationSec: 100}
	h1, h2 := payloadHash(libraryPayload(tr, "", nil, false)), payloadHash(libraryPayload(tr, "", nil, false))
	if h1 != h2 {
		t.Fatalf("equal payloads hash differently")
	}
	tr2 := tr
	tr2.PlayCount = 1
	if payloadHash(libraryPayload(tr, "", nil, false)) == payloadHash(libraryPayload(tr2, "", nil, false)) {
		t.Fatalf("changed payload hashes equal")
	}
	if payloadHash(libraryPayload(tr, "", nil, false)) == payloadHash(libraryPayload(tr, "FP", nil, false)) {
		t.Fatalf("fingerprint change not detected")
	}
	// drop edits must flip the hash: add, change, clear (tombstone) are all distinct
	none := payloadHash(libraryPayload(tr, "", nil, false))
	set := payloadHash(libraryPayload(tr, "", []float64{61000}, true))
	moved := payloadHash(libraryPayload(tr, "", []float64{62000}, true))
	cleared := payloadHash(libraryPayload(tr, "", nil, true))
	if none == set || set == moved || set == cleared || none == cleared {
		t.Fatalf("drop change not detected: none=%s set=%s moved=%s cleared=%s", none, set, moved, cleared)
	}
}

// Golden wire JSON: drops_ms present+sorted+deduped with a row, `[]` for a cleared
// tombstone, absent without a row.
func TestLibraryPayloadDropsGolden(t *testing.T) {
	tr := musiclib.Track{Title: "T", Artist: "A", DurationSec: 2}
	for name, tc := range map[string]struct {
		drops []float64
		has   bool
		want  string
	}{
		"with drops": {[]float64{183000.4, 61000.2, 61000, -5}, true,
			`{"title":"T","artist_text":"A","duration_ms":2000,"drops_ms":[61000,183000]}`},
		"cleared tombstone": {nil, true,
			`{"title":"T","artist_text":"A","duration_ms":2000,"drops_ms":[]}`},
		"no row": {nil, false,
			`{"title":"T","artist_text":"A","duration_ms":2000}`},
	} {
		b, err := json.Marshal(libraryPayload(tr, "", tc.drops, tc.has))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if string(b) != tc.want {
			t.Fatalf("%s:\n got  %s\n want %s", name, b, tc.want)
		}
	}
}

func TestSyncLibraryUploadsDrops(t *testing.T) {
	d := openDB(t)
	seedLibrary(t, d,
		musiclib.Track{Path: "a.mp3", Title: "T1", Artist: "A1", DurationSec: 200},
		musiclib.Track{Path: "b.mp3", Title: "T2", Artist: "A2", DurationSec: 300},
	)
	if err := d.SetDrops("a.mp3", "A1", "T1", 200, []float64{61000, 183000}); err != nil {
		t.Fatal(err)
	}
	f := &fakeAPI{}
	s := New(f, d, nil, tokenFn("tok"))
	if _, err := s.SyncLibrary(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	byTitle := map[string]api.LibraryTrack{}
	for _, p := range f.libTracks {
		byTitle[p.Title] = p
	}
	if got := byTitle["T1"].DropsMs; len(got) != 2 || got[0] != 61000 || got[1] != 183000 {
		t.Fatalf("T1 drops_ms = %v", got)
	}
	if byTitle["T2"].DropsMs != nil {
		t.Fatalf("T2 should omit drops_ms: %v", byTitle["T2"].DropsMs)
	}

	// clear → tombstone → only T1 re-uploads, with explicit []
	if err := d.SetDrops("a.mp3", "A1", "T1", 200, nil); err != nil {
		t.Fatal(err)
	}
	f.libTracks = nil
	res, err := s.SyncLibrary(context.Background())
	if err != nil {
		t.Fatalf("re-sync: %v", err)
	}
	if res.Uploaded != 1 || res.Skipped != 1 {
		t.Fatalf("clear re-sync wrong: %+v", res)
	}
	if len(f.libTracks) != 1 || f.libTracks[0].Title != "T1" {
		t.Fatalf("wrong track re-uploaded: %+v", f.libTracks)
	}
	if ds := f.libTracks[0].DropsMs; ds == nil || len(ds) != 0 {
		t.Fatalf("cleared drops must wire as []: %v", ds)
	}
	if b, _ := json.Marshal(f.libTracks[0]); !strings.Contains(string(b), `"drops_ms":[]`) {
		t.Fatalf("clear not explicit on the wire: %s", b)
	}
}

func TestSyncLibraryUploadsLinksAndSkips(t *testing.T) {
	d := openDB(t)
	seedLibrary(t, d,
		musiclib.Track{Path: "a.mp3", Title: "T1", Artist: "A1", DurationSec: 200, PlayCount: 3},
		musiclib.Track{Path: "b.mp3", Title: "T2", Artist: "A2", DurationSec: 300},
		musiclib.Track{Path: "c.mp3", Title: "", Artist: "A3"}, // untitled → skipped
	)
	f := &fakeAPI{libCanon: map[string]string{"T1": "trk_canon1"}}
	s := New(f, d, nil, tokenFn("tok"))

	res, err := s.SyncLibrary(context.Background())
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.Total != 3 || res.Uploaded != 2 || res.Skipped != 1 || res.Linked != 1 || res.Failed != 0 {
		t.Fatalf("result wrong: %+v", res)
	}
	link, ok, err := d.GetTrackLink(libdb.TrackHash("A1", "T1", 0))
	if err != nil || !ok || link.TrackID != "trk_canon1" || link.Provisional || link.Confidence != 0.93 {
		t.Fatalf("canonical link not saved: ok=%v err=%v %+v", ok, err, link)
	}

	// Re-run: nothing changed → all skipped, no API call.
	calls := f.libCalls
	res2, err := s.SyncLibrary(context.Background())
	if err != nil {
		t.Fatalf("re-sync: %v", err)
	}
	if res2.Uploaded != 0 || res2.Skipped != 3 || f.libCalls != calls {
		t.Fatalf("incremental re-run wrong: %+v calls=%d→%d", res2, calls, f.libCalls)
	}

	// Mutate one track (full re-import - TrackSync removes unseen paths) → only it re-uploads.
	seedLibrary(t, d,
		musiclib.Track{Path: "a.mp3", Title: "T1", Artist: "A1", DurationSec: 200, PlayCount: 4},
		musiclib.Track{Path: "b.mp3", Title: "T2", Artist: "A2", DurationSec: 300},
		musiclib.Track{Path: "c.mp3", Title: "", Artist: "A3"},
	)
	res3, err := s.SyncLibrary(context.Background())
	if err != nil {
		t.Fatalf("delta sync: %v", err)
	}
	if res3.Uploaded != 1 || res3.Skipped != 2 {
		t.Fatalf("delta run wrong: %+v", res3)
	}
}

// libraryBatches must cap by row count for tiny (print-less) rows and by byte budget once rows
// carry fingerprints, never emit an empty batch, and preserve every row.
func TestLibraryBatchesByteAware(t *testing.T) {
	// Row-count cap: 250 tiny rows → 200 + 50.
	small := make([]item, 250)
	for i := range small {
		small[i].size = 100
	}
	b := libraryBatches(small)
	if len(b) != 2 || len(b[0]) != libraryBatch || len(b[1]) != 50 {
		t.Fatalf("count-cap split wrong: %d batches, sizes %v", len(b), batchSizes(b))
	}

	// Byte cap: ~6 KB print-bearing rows must batch well under libraryBatch rows and every batch
	// must stay under the byte budget. 500 rows total, all accounted for.
	big := make([]item, 500)
	for i := range big {
		big[i].size = 6000
	}
	bb := libraryBatches(big)
	total := 0
	for _, batch := range bb {
		sum := 0
		for _, it := range batch {
			sum += it.size + 1
		}
		if sum > maxLibraryBatchBytes {
			t.Fatalf("batch exceeds byte budget: %d > %d", sum, maxLibraryBatchBytes)
		}
		if len(batch) > libraryBatch {
			t.Fatalf("batch exceeds row cap: %d", len(batch))
		}
		total += len(batch)
	}
	if total != 500 {
		t.Fatalf("rows lost in batching: %d, want 500", total)
	}
	if len(bb[0]) >= libraryBatch {
		t.Fatalf("byte cap did not bite on 6KB rows: first batch has %d rows", len(bb[0]))
	}

	// A single row larger than the whole budget still ships alone (never an empty batch).
	one := libraryBatches([]item{{size: maxLibraryBatchBytes + 10}, {size: 50}})
	if len(one) != 2 || len(one[0]) != 1 || len(one[1]) != 1 {
		t.Fatalf("oversized single row not isolated: %v", batchSizes(one))
	}

	if got := libraryBatches(nil); got != nil {
		t.Fatalf("empty queue should batch to nil, got %v", got)
	}
}

func batchSizes(bs [][]item) []int {
	out := make([]int, len(bs))
	for i, b := range bs {
		out[i] = len(b)
	}
	return out
}

// A local print in the libdb (change_log fingerprint event) must ride along on the synced row;
// a track without one omits fingerprint_b64.
func TestSyncLibraryCarriesLocalFingerprint(t *testing.T) {
	d := openDB(t)
	seedLibrary(t, d,
		musiclib.Track{Path: "a.mp3", Title: "T1", Artist: "A1", DurationSec: 200},
		musiclib.Track{Path: "b.mp3", Title: "T2", Artist: "A2", DurationSec: 300},
	)
	// Record a print for T1 only (same shape internal/setfp writes).
	h1 := libdb.TrackHash("A1", "T1", 0)
	nv, _ := json.Marshal("CHROMAPRINT_T1")
	if err := d.AppendChanges([]libdb.ChangeEvent{{
		TrackHash: h1, TrackFP: "CHROMAPRINT_T1",
		Field: "fingerprint", Op: "set", Origin: "fingerprint", NewValue: string(nv),
	}}); err != nil {
		t.Fatalf("append print: %v", err)
	}

	f := &fakeAPI{}
	s := New(f, d, nil, tokenFn("tok"))
	if _, err := s.SyncLibrary(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}

	byTitle := map[string]api.LibraryTrack{}
	for _, p := range f.libTracks {
		byTitle[p.Title] = p
	}
	if got := byTitle["T1"].FingerprintB64; got != "CHROMAPRINT_T1" {
		t.Fatalf("T1 fingerprint_b64 = %q, want CHROMAPRINT_T1", got)
	}
	if got := byTitle["T2"].FingerprintB64; got != "" {
		t.Fatalf("T2 has no local print, want fingerprint_b64 omitted, got %q", got)
	}
	// The wire row for T2 must not carry the key at all (omitempty).
	if b, _ := json.Marshal(byTitle["T2"]); strings.Contains(string(b), "fingerprint_b64") {
		t.Fatalf("T2 leaked fingerprint_b64 key: %s", b)
	}
}

func TestSyncLibraryUnauthed(t *testing.T) {
	d := openDB(t)
	s := New(&fakeAPI{}, d, nil, tokenFn(""))
	if _, err := s.SyncLibrary(context.Background()); err != ErrUnauthenticated {
		t.Fatalf("want ErrUnauthenticated, got %v", err)
	}
}

func TestSyncLibraryBatchFailureCounts(t *testing.T) {
	d := openDB(t)
	seedLibrary(t, d, musiclib.Track{Path: "a.mp3", Title: "T1", Artist: "A1"})
	f := &fakeAPI{libErr: context.DeadlineExceeded}
	s := New(f, d, nil, tokenFn("tok"))
	res, err := s.SyncLibrary(context.Background())
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.Failed != 1 || res.Uploaded != 0 {
		t.Fatalf("failed batch not counted: %+v", res)
	}
	if f.libCalls != 2 { // initial + one retry
		t.Fatalf("retry count = %d, want 2", f.libCalls)
	}
}

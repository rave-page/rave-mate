package libdb

import (
	"testing"
	"time"
)

func TestTrackLinkUpsertAndGet(t *testing.T) {
	d := openTmp(t)
	h := TrackHash("Deadmau5", "Strobe", 0)

	if _, ok, err := d.GetTrackLink(h); err != nil || ok {
		t.Fatalf("expected no link initially, ok=%v err=%v", ok, err)
	}
	if err := d.SaveTrackLink(TrackLink{TrackHash: h, TrackID: "trk_1", Confidence: 0.91}); err != nil {
		t.Fatalf("save: %v", err)
	}
	// Upsert: a later resolution (e.g. provisional → canonical merge) overwrites.
	if err := d.SaveTrackLink(TrackLink{TrackHash: h, TrackID: "trk_2", Provisional: true, ISRC: "USABC1234567"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, ok, err := d.GetTrackLink(h)
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if got.TrackID != "trk_2" || !got.Provisional || got.ISRC != "USABC1234567" {
		t.Fatalf("link row wrong: %+v", got)
	}
	if got.SyncedAt.IsZero() {
		t.Fatalf("synced_at not stamped")
	}
}

func TestSetUploadUpsertAndGet(t *testing.T) {
	d := openTmp(t)
	if _, ok, _ := d.GetSetUpload("rec_1"); ok {
		t.Fatalf("expected no upload initially")
	}
	if err := d.SaveSetUpload(SetUpload{RecordingID: "rec_1", StreamID: "strm_9", TrackCount: 12}); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, ok, err := d.GetSetUpload("rec_1")
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if got.StreamID != "strm_9" || got.TrackCount != 12 || got.UploadedAt.IsZero() {
		t.Fatalf("upload row wrong: %+v", got)
	}
}

func TestLibrarySyncLedger(t *testing.T) {
	d := openTmp(t)
	if m, err := d.LibrarySyncHashes(); err != nil || len(m) != 0 {
		t.Fatalf("expected empty ledger, got %v err=%v", m, err)
	}
	h1, h2 := TrackHash("A", "T1", 0), TrackHash("B", "T2", 0)
	if err := d.SaveLibrarySyncBatch(map[string]LibrarySyncRow{
		h1: {PayloadHash: "p1", LibraryTrackID: "lib_1"}, h2: {PayloadHash: "p2"},
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	// Upsert: changed payload hash overwrites; empty lib id preserves the stored one.
	if err := d.SaveLibrarySyncBatch(map[string]LibrarySyncRow{h1: {PayloadHash: "p1b"}}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	m, err := d.LibrarySyncHashes()
	if err != nil || len(m) != 2 || m[h1] != "p1b" || m[h2] != "p2" {
		t.Fatalf("ledger wrong: %v err=%v", m, err)
	}
	ids, err := d.LibraryTrackIDs()
	if err != nil || len(ids) != 1 || ids[h1] != "lib_1" {
		t.Fatalf("lib ids wrong: %v err=%v", ids, err)
	}
	// Backfill fills h2 + inserts a placeholder row for an unseen hash.
	h3 := TrackHash("C", "T3", 0)
	if err := d.SaveLibraryTrackIDs(map[string]string{h2: "lib_2", h3: "lib_3"}); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	ids, err = d.LibraryTrackIDs()
	if err != nil || len(ids) != 3 || ids[h2] != "lib_2" || ids[h3] != "lib_3" {
		t.Fatalf("backfilled ids wrong: %v err=%v", ids, err)
	}
	// Placeholder row has empty payload_hash → next SyncLibrary still uploads metadata.
	if m, _ := d.LibrarySyncHashes(); m[h3] != "" {
		t.Fatalf("placeholder payload hash should be empty, got %q", m[h3])
	}
}

func TestMediaSyncLedger(t *testing.T) {
	d := openTmp(t)
	h := TrackHash("A", "T1", 0)
	if m, err := d.MediaSyncRows(); err != nil || len(m) != 0 {
		t.Fatalf("expected empty ledger, got %v err=%v", m, err)
	}
	// Waveform and artwork progress independently; empty fields preserve stored values.
	if err := d.SaveMediaSync(h, "wf1", ""); err != nil {
		t.Fatalf("save wf: %v", err)
	}
	if err := d.SaveMediaSync(h, "", "art1"); err != nil {
		t.Fatalf("save art: %v", err)
	}
	m, err := d.MediaSyncRows()
	if err != nil || m[h].WaveformHash != "wf1" || m[h].ArtworkHash != "art1" {
		t.Fatalf("ledger wrong: %+v err=%v", m[h], err)
	}
	if err := d.SaveMediaSync(h, "wf2", ""); err != nil {
		t.Fatalf("update wf: %v", err)
	}
	m, _ = d.MediaSyncRows()
	if m[h].WaveformHash != "wf2" || m[h].ArtworkHash != "art1" {
		t.Fatalf("partial update wrong: %+v", m[h])
	}
}

func TestFingerprintForTrack(t *testing.T) {
	d := openTmp(t)
	h := TrackHash("Artist", "Title", 0)
	if _, ok, _ := d.FingerprintForTrack(h); ok {
		t.Fatalf("expected no fingerprint initially")
	}
	if err := d.AppendChanges([]ChangeEvent{{
		TrackHash: h, TrackFP: "AQADtM...", Field: "fingerprint", Op: "set", Origin: "fingerprint",
		NewValue: `"AQADtM..."`, TS: time.Now().UTC().Format(time.RFC3339),
	}}); err != nil {
		t.Fatalf("append: %v", err)
	}
	fp, ok, err := d.FingerprintForTrack(h)
	if err != nil || !ok || fp != "AQADtM..." {
		t.Fatalf("fingerprint = %q ok=%v err=%v", fp, ok, err)
	}
}

func TestFingerprintedHashes(t *testing.T) {
	d := openTmp(t)
	if got, err := d.FingerprintedHashes(); err != nil || len(got) != 0 {
		t.Fatalf("initially empty: got %v err=%v", got, err)
	}
	hA := TrackHash("A", "T1", 0)
	hB := TrackHash("B", "T2", 0)
	// A gets a real print; B's fingerprint event carries an EMPTY track_fp (must be excluded,
	// matching FingerprintForTrack which returns ok only for a non-empty print).
	if err := d.AppendChanges([]ChangeEvent{
		{TrackHash: hA, TrackFP: "FPA", Field: "fingerprint", Op: "set", Origin: "fingerprint", NewValue: `"FPA"`},
		{TrackHash: hB, TrackFP: "", Field: "fingerprint", Op: "set", Origin: "fingerprint", NewValue: `""`},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	got, err := d.FingerprintedHashes()
	if err != nil {
		t.Fatal(err)
	}
	if !got[hA] || got[hB] || len(got) != 1 {
		t.Fatalf("want {A} only, got %v", got)
	}
	// Newest event wins: blank A's print → it drops out, staying consistent with FingerprintForTrack.
	if err := d.AppendChanges([]ChangeEvent{
		{TrackHash: hA, TrackFP: "", Field: "fingerprint", Op: "set", Origin: "fingerprint", NewValue: `""`},
	}); err != nil {
		t.Fatal(err)
	}
	got, _ = d.FingerprintedHashes()
	if got[hA] {
		t.Fatalf("A's newest print is empty, must be excluded: %v", got)
	}
	if _, ok, _ := d.FingerprintForTrack(hA); ok {
		t.Fatalf("FingerprintForTrack disagrees with FingerprintedHashes for A")
	}
}

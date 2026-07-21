package recorder

import (
	"rave.page/mate/internal/store"
	"strings"
	"testing"
	"time"
)

func fixRec(start time.Time, offsets ...time.Duration) Recording {
	rec := Recording{ID: "rec_fix_1", Name: "Fix set", StartedAt: start, EndedAt: start.Add(2 * time.Hour)}
	for i, off := range offsets {
		t := Track{Title: "T" + string(rune('A'+i)), Artist: "A", StartedAt: start.Add(off)}
		if i > 0 {
			rec.Tracks[i-1].EndedAt = t.StartedAt
		}
		rec.Tracks = append(rec.Tracks, t)
	}
	return rec
}

func TestPlanTimeFixLoopedFirstTrack(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	// Track 1 "started" at t=0 (looping); capture began at t=15m, real audio at 15m+90s.
	rec := fixRec(base, 0, 25*time.Minute, 40*time.Minute)
	capStart := base.Add(15 * time.Minute)
	fix, ok := PlanTimeFix(rec, capStart, time.Time{}, 90*time.Second, -1)
	if !ok {
		t.Fatal("expected a fix")
	}
	audio := capStart.Add(90 * time.Second)
	if !fix.NewStart.Equal(audio) || fix.Opener != 0 {
		t.Fatalf("NewStart = %v opener %d, want %v / 0", fix.NewStart, fix.Opener, audio)
	}
	if got := fix.TrackStarts[0]; !got.Equal(audio) {
		t.Fatalf("track 0 start = %v, want %v", got, audio)
	}
	if _, moved := fix.TrackStarts[1]; moved {
		t.Fatal("track 1 (already after audio start) must not move")
	}
	if len(fix.RemoveTracks) != 0 {
		t.Fatalf("nothing to remove, got %v", fix.RemoveTracks)
	}
}

// Deck-prep phantoms: tracks 1-2 carry deck-play times from BEFORE the capture began.
// The deck timeline says track 2 was playing when sound appears → auto opener = track 2;
// track 1 was over before the capture rolled → removed.
func TestPlanTimeFixPhantomEarlyTracks(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	rec := fixRec(base, 21*time.Minute, 24*time.Minute, 36*time.Minute, 38*time.Minute)
	capStart := base.Add(31 * time.Minute)
	capEnd := base.Add(91 * time.Minute)
	fix, ok := PlanTimeFix(rec, capStart, capEnd, 5*time.Minute, -1)
	if !ok {
		t.Fatal("phantom early tracks must still plan")
	}
	audio := capStart.Add(5 * time.Minute) // = base+36m
	if !fix.NewStart.Equal(audio) || fix.Opener != 1 {
		t.Fatalf("NewStart = %v opener %d, want %v / 1", fix.NewStart, fix.Opener, audio)
	}
	if len(fix.RemoveTracks) != 1 || fix.RemoveTracks[0] != 0 {
		t.Fatalf("track 0 (over before capture) must be removed, got %v", fix.RemoveTracks)
	}
	if !fix.TrackStarts[1].Equal(audio) {
		t.Fatalf("opener must start at %v, got %v", audio, fix.TrackStarts[1])
	}
	if _, moved := fix.TrackStarts[2]; moved {
		t.Fatal("track at the audible start must not move")
	}

	// Hand-picked earlier opener: track 1 opens instead; nothing removed, track 2 clamps
	// to 0:00 for manual adjustment.
	fix, ok = PlanTimeFix(rec, capStart, capEnd, 5*time.Minute, 0)
	if !ok || fix.Opener != 0 {
		t.Fatalf("explicit opener must plan (opener %d)", fix.Opener)
	}
	if len(fix.RemoveTracks) != 0 {
		t.Fatalf("explicit first opener removes nothing, got %v", fix.RemoveTracks)
	}
	if !fix.TrackStarts[0].Equal(audio) || !fix.TrackStarts[1].Equal(audio) {
		t.Fatalf("opener + later phantom must clamp to %v: %v", audio, fix.TrackStarts)
	}
}

func TestPlanTimeFixRejects(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	live := fixRec(base, 0)
	live.EndedAt = time.Time{}
	if _, ok := PlanTimeFix(live, base, time.Time{}, 0, -1); ok {
		t.Fatal("live set must not plan")
	}
	rec := fixRec(base, 0, 5*time.Minute)
	// Silence running to the capture's end = nothing audible to anchor on.
	if _, ok := PlanTimeFix(rec, base, base.Add(10*time.Minute), 10*time.Minute, -1); ok {
		t.Fatal("entirely-silent capture must not plan")
	}
	// Audio start past the set end can't align.
	if _, ok := PlanTimeFix(rec, base.Add(3*time.Hour), time.Time{}, 0, -1); ok {
		t.Fatal("capture past set end must not plan")
	}
	// Aligned already → no-op.
	if _, ok := PlanTimeFix(rec, base, time.Time{}, 0, -1); ok {
		t.Fatal("aligned set must not plan")
	}
}

func TestApplyTimeFixRemovesPhantoms(t *testing.T) {
	r := newStored(t)
	base := time.Unix(1_700_000_000, 0)
	rec := fixRec(base, 21*time.Minute, 24*time.Minute, 36*time.Minute)
	if err := r.st.PutJSON(store.BucketRecordings, rec.ID, &rec); err != nil {
		t.Fatal(err)
	}
	fix, ok := PlanTimeFix(rec, base.Add(31*time.Minute), time.Time{}, 5*time.Minute, -1)
	if !ok {
		t.Fatal("expected fix")
	}
	got, err := r.ApplyTimeFix(rec.ID, fix)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Tracks) != 2 {
		t.Fatalf("removed track must be gone, got %d tracks", len(got.Tracks))
	}
	audio := base.Add(36 * time.Minute)
	if got.Tracks[0].Title != "TB" || !got.Tracks[0].StartedAt.Equal(audio) {
		t.Fatalf("opener = %q @ %v, want TB @ %v", got.Tracks[0].Title, got.Tracks[0].StartedAt, audio)
	}
	stored, ok := r.Get(rec.ID)
	if !ok || len(stored.Tracks) != 2 {
		t.Fatalf("persisted tracks = %d, want 2", len(stored.Tracks))
	}
}

func TestRemoveTrack(t *testing.T) {
	r := newStored(t)
	base := time.Unix(1_700_000_000, 0)
	rec := fixRec(base, 0, 10*time.Minute, 20*time.Minute)
	if err := r.st.PutJSON(store.BucketRecordings, rec.ID, &rec); err != nil {
		t.Fatal(err)
	}
	got, err := r.RemoveTrack(rec.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Tracks) != 2 || got.Tracks[1].Title != "TC" {
		t.Fatalf("track 1 must be gone: %+v", got.Tracks)
	}
	if _, err := r.RemoveTrack(rec.ID, 9); err == nil {
		t.Fatal("out-of-range removal must error")
	}
}

func TestApplyTimeFixPersists(t *testing.T) {
	r := newStored(t)
	base := time.Unix(1_700_000_000, 0)
	rec := fixRec(base, 0, 20*time.Minute)
	if err := r.st.PutJSON(store.BucketRecordings, rec.ID, &rec); err != nil {
		t.Fatal(err)
	}
	capStart := base.Add(10 * time.Minute)
	fix, ok := PlanTimeFix(rec, capStart, time.Time{}, 30*time.Second, -1)
	if !ok {
		t.Fatal("expected fix")
	}
	before := r.RecordingsVersion()
	got, err := r.ApplyTimeFix(rec.ID, fix)
	if err != nil {
		t.Fatal(err)
	}
	audio := capStart.Add(30 * time.Second)
	if !got.StartedAt.Equal(audio) || !got.Tracks[0].StartedAt.Equal(audio) {
		t.Fatalf("apply result start=%v t0=%v, want %v", got.StartedAt, got.Tracks[0].StartedAt, audio)
	}
	if r.RecordingsVersion() == before {
		t.Fatal("RecordingsVersion must bump on a time fix")
	}
	stored, ok := r.Get(rec.ID)
	if !ok || !stored.StartedAt.Equal(audio) {
		t.Fatalf("persisted start = %v, want %v", stored.StartedAt, audio)
	}
}

func TestSetTrackStartSyncsNeighbourEnd(t *testing.T) {
	r := newStored(t)
	base := time.Unix(1_700_000_000, 0)
	rec := fixRec(base, 0, 10*time.Minute, 20*time.Minute)
	if err := r.st.PutJSON(store.BucketRecordings, rec.ID, &rec); err != nil {
		t.Fatal(err)
	}
	ns := base.Add(12 * time.Minute)
	got, err := r.SetTrackStart(rec.ID, 1, ns)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Tracks[1].StartedAt.Equal(ns) {
		t.Fatalf("track 1 start = %v, want %v", got.Tracks[1].StartedAt, ns)
	}
	if !got.Tracks[0].EndedAt.Equal(ns) {
		t.Fatalf("track 0 synced end = %v, want %v", got.Tracks[0].EndedAt, ns)
	}
	if _, err := r.SetTrackStart(rec.ID, 99, ns); err == nil {
		t.Fatal("out-of-range index must error")
	}
	if _, err := r.SetTrackStart("rec_missing", 0, ns); err == nil {
		t.Fatal("unknown id must error")
	}
}

func TestParseClock(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"0:00", 0, true},
		{"1:23", 83 * time.Second, true},
		{"1:02:03", 3723 * time.Second, true},
		{" 90 ", 90 * time.Second, true},
		{"75:00", 75 * time.Minute, true},
		{"", 0, false},
		{"a:b", 0, false},
		{"-1:00", 0, false},
		{"1:2:3:4", 0, false},
	} {
		got, err := ParseClock(tc.in)
		if tc.ok != (err == nil) || got != tc.want {
			t.Errorf("ParseClock(%q) = %v, %v; want %v ok=%v", tc.in, got, err, tc.want, tc.ok)
		}
	}
}

func TestExportTextTemplates(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	rec := Recording{Name: "My set", StartedAt: base, Tracks: []Track{
		{Title: "One", Artist: "AA", BPM: 174, Key: "8A", StartedAt: base},
		{Title: "Two", StartedAt: base.Add(3*time.Minute + 4*time.Second)},
	}}

	// Default template stays byte-identical to the classic export.
	classic, err := rec.Export(FormatText)
	if err != nil {
		t.Fatal(err)
	}
	if classic != rec.ExportText(DefaultTextOptions()) {
		t.Fatal("default ExportText must match classic Export text")
	}
	if !strings.Contains(classic, "1. [0:00] AA - One\n") || !strings.Contains(classic, "2. [3:04] Two\n") {
		t.Fatalf("classic output unexpected:\n%s", classic)
	}

	out := rec.ExportText(TextOptions{Line: "{offset} {track}"})
	if strings.Contains(out, "My set") {
		t.Fatal("empty header must suppress the header block")
	}
	if !strings.HasPrefix(out, "0:00 AA - One\n") {
		t.Fatalf("youtube-style output unexpected:\n%s", out)
	}

	out = rec.ExportText(TextOptions{Line: "{nn}|{bpm}|{key}|{title}", Header: "{name} ({count})"})
	if !strings.HasPrefix(out, "My set (2)\n\n01|174|8A|One\n") || !strings.Contains(out, "02|||Two\n") {
		t.Fatalf("placeholder output unexpected:\n%s", out)
	}
}

package recorder

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/session"
	"rave.page/mate/internal/store"
)

// newStored builds a recorder over a real bbolt store (rename hits the persist path).
func newStored(t *testing.T) *Recorder {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "rec.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return New(logbus.New(64), st, nil, 10)
}

func deckState(deck, title, artist string, playing bool) session.UnifiedState {
	chForDeck := map[string]string{"A": "1", "B": "2", "C": "3", "D": "4"}
	return session.UnifiedState{
		Decks: map[string]map[string]session.FieldValue{
			deck: {
				session.FieldIsPlaying: {Value: playing},
				session.FieldTitle:     {Value: title, Source: session.SourceTraktor},
				session.FieldArtist:    {Value: artist},
				session.FieldBPM:       {Value: 128.0},
			},
		},
		Channels: map[string]map[string]session.FieldValue{
			chForDeck[deck]: {session.FieldFader: {Value: 1.0}},
		},
	}
}

func TestFindByWindow(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r := New(logbus.New(64), nil, nil, 10)
	r.clock = func() time.Time { return now }
	r.StartRecording("set", "") // active recording, StartedAt = now, open-ended

	// A capture overlapping the in-progress set links to it.
	got, ok := r.FindByWindow(now.Add(time.Minute), now.Add(10*time.Minute))
	if !ok || got.Name != "set" {
		t.Fatalf("overlapping window should match the active set: ok=%v rec=%+v", ok, got)
	}
	// A capture entirely before the set matches nothing.
	if _, ok := r.FindByWindow(now.Add(-time.Hour), now.Add(-30*time.Minute)); ok {
		t.Fatal("disjoint window must not match")
	}
}

func TestRecorderConfirmsAndSwitches(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r := New(logbus.New(64), nil, nil, 10) // confirm = 10s
	r.clock = func() time.Time { return now }

	r.StartRecording("Test set", "stream-1")

	stepAt := func(sec int, st session.UnifiedState) {
		now = time.Unix(1_700_000_000, 0).Add(time.Duration(sec) * time.Second)
		r.step(now, st)
	}

	a := deckState("A", "Track A", "Artist A", true)
	b := deckState("B", "Track B", "Artist B", true)

	stepAt(0, a)  // begin pending A
	stepAt(5, a)  // > switchDebounce → A becomes current (firstSeen backdated to t=0)
	stepAt(11, a) // > confirm(10) from t=0 → A committed
	stepAt(12, b) // begin pending B
	stepAt(17, b) // commit switch → A ends at t=12, B current from t=12
	stepAt(23, b) // confirm B

	now = time.Unix(1_700_000_000, 0).Add(25 * time.Second)
	rec := r.StopRecording()
	if rec == nil {
		t.Fatal("StopRecording returned nil")
	}
	if len(rec.Tracks) != 2 {
		t.Fatalf("want 2 tracks, got %d: %+v", len(rec.Tracks), rec.Tracks)
	}
	if rec.Tracks[0].Title != "Track A" || rec.Tracks[1].Title != "Track B" {
		t.Fatalf("track order wrong: %+v", rec.Tracks)
	}
	base := time.Unix(1_700_000_000, 0)
	if !rec.Tracks[0].StartedAt.Equal(base) {
		t.Fatalf("A start = %v, want %v", rec.Tracks[0].StartedAt, base)
	}
	if !rec.Tracks[0].EndedAt.Equal(base.Add(12 * time.Second)) {
		t.Fatalf("A end = %v, want t+12", rec.Tracks[0].EndedAt)
	}
	if !rec.Tracks[1].StartedAt.Equal(base.Add(12 * time.Second)) {
		t.Fatalf("B start = %v, want t+12", rec.Tracks[1].StartedAt)
	}
	if !rec.Tracks[1].EndedAt.Equal(base.Add(25 * time.Second)) {
		t.Fatalf("B end = %v, want t+25", rec.Tracks[1].EndedAt)
	}
	if rec.StreamID != "stream-1" {
		t.Fatalf("streamID = %q", rec.StreamID)
	}
}

func TestRecorderTracksLastFaderDown(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	now := base
	r := New(logbus.New(64), nil, nil, 10)
	r.clock = func() time.Time { return now }
	r.StartRecording("set", "")

	stepAt := func(sec int, st session.UnifiedState) {
		now = base.Add(time.Duration(sec) * time.Second)
		r.step(now, st)
	}

	a := deckState("A", "Track A", "Artist A", true) // fader up (1.0)
	down := deckState("A", "Track A", "Artist A", true)
	down.Channels["1"] = map[string]session.FieldValue{session.FieldFader: {Value: 0.0}} // fader pulled to 0

	stepAt(0, a)     // pending A
	stepAt(10, a)    // A current/confirmed, fader up → LastFaderAt advances to t+10
	stepAt(20, down) // fader below threshold → LastFaderAt must NOT advance

	if r.active == nil {
		t.Fatal("no active recording")
	}
	if want := base.Add(10 * time.Second); !r.active.LastFaderAt.Equal(want) {
		t.Fatalf("LastFaderAt = %v, want t+10 (last instant a fader was up)", r.active.LastFaderAt)
	}
}

func TestPendingAndCandidateFill(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r := New(logbus.New(64), nil, nil, 10)
	r.clock = func() time.Time { return now }
	r.StartRecording("set", "")

	stepAt := func(sec int, st session.UnifiedState) {
		now = time.Unix(1_700_000_000, 0).Add(time.Duration(sec) * time.Second)
		r.step(now, st)
	}

	a := deckState("A", "Track A", "Artist A", true)
	stepAt(0, a)
	stepAt(5, a) // past debounce → candidate
	p, ok := r.Pending()
	if !ok || p.Track.Title != "Track A" {
		t.Fatalf("pending = %+v ok=%v", p, ok)
	}
	base := time.Unix(1_700_000_000, 0)
	if !p.FirstSeen.Equal(base) || !p.ConfirmAt.Equal(base.Add(10*time.Second)) {
		t.Fatalf("pending window wrong: %+v", p)
	}
	if p.Track.Key != "" {
		t.Fatalf("key should be empty pre-fill: %+v", p.Track)
	}

	// Key arrives during the confirm window → candidate fills before commit.
	withKey := deckState("A", "Track A", "Artist A", true)
	withKey.Decks["A"][session.FieldKey] = session.FieldValue{Value: "8A"}
	stepAt(7, withKey)
	stepAt(11, withKey) // confirms
	if _, ok := r.Pending(); ok {
		t.Fatal("pending must clear after confirm")
	}
	act := r.Active()
	if len(act.Tracks) != 1 || act.Tracks[0].Key != "8A" {
		t.Fatalf("confirmed track missing filled key: %+v", act.Tracks)
	}
}

func TestRecorderIgnoresBriefFlap(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r := New(logbus.New(64), nil, nil, 10)
	r.clock = func() time.Time { return now }
	r.StartRecording("flap", "")

	a := deckState("A", "Track A", "Artist A", true)
	b := deckState("B", "Track B", "Artist B", true)
	step := func(sec int, st session.UnifiedState) {
		now = time.Unix(1_700_000_000, 0).Add(time.Duration(sec) * time.Second)
		r.step(now, st)
	}

	step(0, a)
	step(5, a)  // A current
	step(11, a) // A confirmed
	step(12, b) // pending B begins
	step(14, a) // back to A before switchDebounce(4s) elapses → flap ignored
	step(20, a) // still A

	rec := r.Active()
	if len(rec.Tracks) != 1 || rec.Tracks[0].Title != "Track A" {
		t.Fatalf("brief flap should not add a track: %+v", rec.Tracks)
	}
	if !rec.Tracks[0].EndedAt.IsZero() {
		t.Fatalf("A should not have ended during a flap: %v", rec.Tracks[0].EndedAt)
	}
}

func TestStopDoesNotRestartSameTrack(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r := New(logbus.New(64), nil, nil, 10)
	r.clock = func() time.Time { return now }

	a := deckState("A", "Track A", "Artist A", true)
	b := deckState("B", "Track B", "Artist B", true)
	step := func(sec int, st session.UnifiedState) {
		now = time.Unix(1_700_000_000, 0).Add(time.Duration(sec) * time.Second)
		r.step(now, st)
	}

	step(0, a)  // always-on: A auto-starts a set + begins pending A
	step(5, a)  // A becomes current
	step(11, a) // A confirmed
	if r.Active() == nil {
		t.Fatal("expected an active recording after A confirmed")
	}

	now = time.Unix(1_700_000_000, 0).Add(12 * time.Second)
	if r.StopRecording() == nil {
		t.Fatal("StopRecording returned nil")
	}
	if r.Active() != nil {
		t.Fatal("StopRecording should clear the active recording")
	}

	// A is still on the deck (track hasn't changed): no new set may auto-start.
	step(13, a)
	step(20, a)
	if r.Active() != nil {
		t.Fatalf("must not auto-restart while the same track is still playing: %+v", r.Active())
	}

	// A genuinely different track becomes audible → a fresh set starts.
	step(25, b)
	if r.Active() == nil {
		t.Fatal("a new track should start a fresh set after a manual stop")
	}
}

func TestStopGuardClearsOnSilence(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r := New(logbus.New(64), nil, nil, 10)
	r.clock = func() time.Time { return now }

	a := deckState("A", "Track A", "Artist A", true)
	silent := session.UnifiedState{}
	step := func(sec int, st session.UnifiedState) {
		now = time.Unix(1_700_000_000, 0).Add(time.Duration(sec) * time.Second)
		r.step(now, st)
	}

	step(0, a)
	step(5, a)
	step(11, a)
	now = time.Unix(1_700_000_000, 0).Add(12 * time.Second)
	r.StopRecording()

	// The deck goes quiet, clearing the hold-off; A returning then counts as a new play.
	step(13, silent)
	step(14, a)
	if r.Active() == nil {
		t.Fatal("silence should clear the stop hold-off so a later play starts a new set")
	}
}

func TestRenameValidation(t *testing.T) {
	r := newStored(t)
	rec := r.StartRecording("Set A", "")

	for _, tc := range []struct{ name, in string }{
		{"empty", ""},
		{"whitespace", "   \t\n "},
		{"too long", strings.Repeat("x", maxRecordingName+1)},
	} {
		if err := r.Rename(rec.ID, tc.in); err == nil {
			t.Fatalf("%s name must be rejected", tc.name)
		}
	}
	if got := r.Active().Name; got != "Set A" {
		t.Fatalf("rejected rename must not mutate the name: %q", got)
	}
	// Exactly at the cap is allowed; runes are counted, not bytes (a 200-rune emoji name fits).
	if err := r.Rename(rec.ID, strings.Repeat("🎧", maxRecordingName)); err != nil {
		t.Fatalf("%d-rune name must be accepted: %v", maxRecordingName, err)
	}
	if err := r.Rename("rec_does_not_exist", "Nope"); err == nil {
		t.Fatal("unknown id must error")
	}
}

func TestRenameActiveSurvivesQueuedSnapshot(t *testing.T) {
	r := newStored(t)
	v0 := r.RecordingsVersion()
	rec := r.StartRecording("Set A", "") // queues a put carrying "Set A"

	// Rename before that snapshot is known to have flushed: this is the regression the persist
	// queue invites - a stale queued snapshot landing after the rename would revert the name.
	if err := r.Rename(rec.ID, "  Peak Time  "); err != nil {
		t.Fatal(err)
	}
	if got := r.Active().Name; got != "Peak Time" {
		t.Fatalf("active name = %q, want trimmed %q", got, "Peak Time")
	}
	got, ok := r.Get(rec.ID)
	if !ok || got.Name != "Peak Time" {
		t.Fatalf("Get after rename = %+v ok=%v", got, ok)
	}
	if r.RecordingsVersion() == v0 {
		t.Fatal("RecordingsVersion must bump (webui Publish list caches List() by it)")
	}

	r.drainPersist() // let every queued snapshot land
	list := r.List()
	if len(list) != 1 || list[0].Name != "Peak Time" {
		t.Fatalf("queued snapshot reverted the name on flush: %+v", list)
	}
}

func TestRenamePersisted(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	now := base
	r := newStored(t)
	r.clock = func() time.Time { return now }

	a := deckState("A", "Track A", "Artist A", true)
	step := func(sec int, st session.UnifiedState) {
		now = base.Add(time.Duration(sec) * time.Second)
		r.step(now, st)
	}
	step(0, a)
	step(5, a)
	step(11, a) // A confirmed
	now = base.Add(12 * time.Second)
	done := r.StopRecording() // no longer active; StopRecording drains
	if done == nil {
		t.Fatal("StopRecording returned nil")
	}

	v0 := r.RecordingsVersion()
	if err := r.Rename(done.ID, "Closing Set"); err != nil {
		t.Fatal(err)
	}
	if r.RecordingsVersion() == v0 {
		t.Fatal("RecordingsVersion must bump after renaming a persisted recording")
	}
	got, ok := r.Get(done.ID)
	if !ok || got.Name != "Closing Set" {
		t.Fatalf("Get = %+v ok=%v", got, ok)
	}
	// The read-modify-write must carry the tracklist through, not just the name.
	if len(got.Tracks) != 1 || got.Tracks[0].Title != "Track A" {
		t.Fatalf("rename clobbered the tracklist: %+v", got.Tracks)
	}
	if got.EndedAt.IsZero() {
		t.Fatal("rename clobbered EndedAt")
	}
	list := r.List()
	if len(list) != 1 || list[0].Name != "Closing Set" {
		t.Fatalf("List = %+v", list)
	}
}

func TestExportText(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	rec := Recording{
		Name: "My Set", StartedAt: base,
		Tracks: []Track{
			{Title: "One", Artist: "A", StartedAt: base},
			{Title: "Two", Artist: "B", StartedAt: base.Add(90 * time.Second)},
		},
	}
	out, err := rec.Export(FormatText)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "1. [0:00] A - One") || !strings.Contains(out, "2. [1:30] B - Two") {
		t.Fatalf("text export:\n%s", out)
	}
	csv, err := rec.Export(FormatCSV)
	if err != nil || !strings.Contains(csv, "A,One") {
		t.Fatalf("csv export err=%v:\n%s", err, csv)
	}
}

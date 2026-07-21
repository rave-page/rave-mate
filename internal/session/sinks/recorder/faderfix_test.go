package recorder

import (
	"strings"
	"testing"
	"time"
)

func bp(b bool) *bool       { return &b }
func fp(f float64) *float64 { return &f }

// The UMC-shaped scenario: opener looped + next track cue-previewed before the capture,
// both replayed for real once the mix went live - fader history reconstructs both.
func TestPlanFaderFixReconstructs(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	at := func(m float64) time.Time { return base.Add(time.Duration(m * float64(time.Minute))) }
	rec := fixRec(base, 21*time.Minute, 24*time.Minute, 36*time.Minute+30*time.Second)
	rec.Tracks[0].Deck, rec.Tracks[1].Deck, rec.Tracks[2].Deck = "A", "B", "C"
	capStart, capEnd := at(31), at(92)

	evs := []DeckEvent{
		// prep: opener looped on A (pre-capture), next track previewed on B
		{At: at(21), Deck: "A", Playing: bp(true)},
		{At: at(22), Deck: "A", Fader: fp(0.7)},
		{At: at(23.9), Deck: "A", Fader: fp(0)},
		{At: at(24), Deck: "A", Playing: bp(false)},
		{At: at(24), Deck: "B", Playing: bp(true)},
		{At: at(24.1), Deck: "B", Fader: fp(0.9)},
		{At: at(26), Deck: "B", Fader: fp(0)},
		{At: at(27), Deck: "B", Playing: bp(false)},
		// the real opening: A replays + fader up right at the audible start
		{At: at(35.9), Deck: "A", Playing: bp(true)},
		{At: at(36.05), Deck: "A", Fader: fp(0.6)},
		// B replays for real before the first post-audio track
		{At: at(36.1), Deck: "B", Playing: bp(true)},
		{At: at(36.2), Deck: "B", Fader: fp(1.0)},
	}
	fix, ok := PlanFaderFix(rec, capStart, capEnd, 5*time.Minute, evs) // audio = base+36m
	if !ok {
		t.Fatal("expected a fader fix")
	}
	audio := at(36)
	if !fix.NewStart.Equal(audio) || fix.Opener != 0 {
		t.Fatalf("NewStart %v opener %d, want %v / 0", fix.NewStart, fix.Opener, audio)
	}
	if len(fix.RemoveTracks) != 0 {
		t.Fatalf("both tracks replayed - nothing removed, got %v", fix.RemoveTracks)
	}
	if !fix.TrackStarts[0].Equal(audio) {
		t.Fatalf("opener start = %v, want audible start %v", fix.TrackStarts[0], audio)
	}
	if !fix.TrackStarts[1].Equal(at(36.2)) {
		t.Fatalf("track 2 start = %v, want its fader-up %v", fix.TrackStarts[1], at(36.2))
	}
	if _, moved := fix.TrackStarts[2]; moved {
		t.Fatal("post-audio track must keep its recorded time")
	}
}

// A cue preview that never went back on air during the capture is removed.
func TestPlanFaderFixRemovesNeverAired(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	at := func(m float64) time.Time { return base.Add(time.Duration(m * float64(time.Minute))) }
	rec := fixRec(base, 21*time.Minute, 24*time.Minute, 36*time.Minute+30*time.Second)
	rec.Tracks[0].Deck, rec.Tracks[1].Deck, rec.Tracks[2].Deck = "A", "B", "C"
	evs := []DeckEvent{
		{At: at(24), Deck: "B", Playing: bp(true)},
		{At: at(24.1), Deck: "B", Fader: fp(0.9)}, // pre-capture preview only
		{At: at(26), Deck: "B", Fader: fp(0)},
		{At: at(27), Deck: "B", Playing: bp(false)},
		{At: at(35.9), Deck: "A", Playing: bp(true)},
		{At: at(36.05), Deck: "A", Fader: fp(0.6)},
	}
	fix, ok := PlanFaderFix(rec, at(31), at(92), 5*time.Minute, evs)
	if !ok {
		t.Fatal("expected a fader fix")
	}
	if fix.Opener != 0 || len(fix.RemoveTracks) != 1 || fix.RemoveTracks[0] != 1 {
		t.Fatalf("preview-only track must be removed (opener %d, removed %v)", fix.Opener, fix.RemoveTracks)
	}
}

// State carried from before the search window counts at the window start (faders already
// up when the capture began).
func TestFirstOnAirCarriedState(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	evs := []DeckEvent{
		{At: base, Deck: "A", Playing: bp(true)},
		{At: base.Add(time.Minute), Deck: "A", Fader: fp(0.8)},
	}
	from := base.Add(5 * time.Minute)
	got, ok := firstOnAir(evs, "A", true, from, base.Add(time.Hour))
	if !ok || !got.Equal(from) {
		t.Fatalf("carried on-air state must fire at window start: %v %v", got, ok)
	}
	if _, ok := firstOnAir(evs, "B", true, from, base.Add(time.Hour)); ok {
		t.Fatal("deck with no events must not be on air")
	}
}

// History that doesn't span the audible moment is rejected (wrong night / logging off).
func TestPlanFaderFixRejectsNonCovering(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	rec := fixRec(base, 0, 5*time.Minute)
	evs := []DeckEvent{{At: base.Add(-2 * time.Hour), Deck: "A", Playing: bp(true)}}
	if _, ok := PlanFaderFix(rec, base, time.Time{}, 0, evs); ok {
		t.Fatal("non-covering history must not plan")
	}
}

func TestParseTraktorPayloadLog(t *testing.T) {
	base := time.Date(2026, 7, 17, 21, 0, 0, 0, time.UTC)
	log := `{"payload":{"isPlaying":true},"ts":"2026-07-17T21:01:59Z","url":"/updateDeck/A"}
{"payload":{"onAirLevel":0.445},"ts":"2026-07-17T21:02:04Z","url":"/updateChannel/1"}
{"payload":{"tempo":0.99},"ts":"2026-07-17T21:02:05Z","url":"/updateDeck/A"}
{"payload":{"onAirLevel":1},"ts":"2026-07-17T23:59:00Z","url":"/updateChannel/2"}
not json at all
{"payload":{"onAirLevel":0.5},"ts":"2026-07-17T21:03:00Z","url":"/updateChannel/9"}`
	evs := ParseTraktorPayloadLog(strings.NewReader(log), base, base.Add(30*time.Minute))
	if len(evs) != 2 {
		t.Fatalf("want 2 events (play + mapped fader), got %d: %+v", len(evs), evs)
	}
	if evs[0].Deck != "A" || evs[0].Playing == nil || !*evs[0].Playing {
		t.Fatalf("event 0 = %+v, want deck A playing", evs[0])
	}
	if evs[1].Deck != "A" || evs[1].Fader == nil || *evs[1].Fader != 0.445 {
		t.Fatalf("event 1 = %+v, want deck A fader 0.445", evs[1])
	}
}

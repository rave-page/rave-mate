package recorder

import (
	"testing"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/session"
)

// mixState builds a multi-deck state: decks deck→(title, playing), channels deck→fader.
func mixState(decks map[string][2]any, faders map[string]float64) session.UnifiedState {
	chForDeck := map[string]string{"A": "1", "B": "2", "C": "3", "D": "4"}
	st := session.UnifiedState{
		Decks:    map[string]map[string]session.FieldValue{},
		Channels: map[string]map[string]session.FieldValue{},
	}
	for deck, tp := range decks {
		st.Decks[deck] = map[string]session.FieldValue{
			session.FieldIsPlaying: {Value: tp[1].(bool)},
			session.FieldTitle:     {Value: tp[0].(string), Source: session.SourceTraktor},
			session.FieldArtist:    {Value: "Artist " + deck},
		}
	}
	for deck, f := range faders {
		st.Channels[chForDeck[deck]] = map[string]session.FieldValue{session.FieldFader: {Value: f}}
	}
	return st
}

// A cued/looped deck with the fader down must not start a set or confirm a track; the
// fader-up moment starts both.
func TestOnAirGateBlocksFadedDeck(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r := New(logbus.New(64), nil, nil, 10)
	r.clock = func() time.Time { return now }

	step := func(sec int, st session.UnifiedState) {
		now = time.Unix(1_700_000_000, 0).Add(time.Duration(sec) * time.Second)
		r.step(now, st)
	}
	cued := mixState(map[string][2]any{"A": {"Track A", true}}, map[string]float64{"A": 0})
	live := mixState(map[string][2]any{"A": {"Track A", true}}, map[string]float64{"A": 0.9})

	for s := 0; s <= 600; s += 5 {
		step(s, cued) // 10 minutes of looping with the fader down
	}
	if r.Active() != nil {
		t.Fatal("faded deck must not start a set")
	}

	step(605, live) // fader up → set starts
	a := r.Active()
	if a == nil {
		t.Fatal("fader-up must start the set")
	}
	step(610, live)
	step(616, live) // confirm (10s) from the fader-up step
	a = r.Active()
	if len(a.Tracks) != 1 {
		t.Fatalf("track must confirm after fader-up, got %d", len(a.Tracks))
	}
	faderUp := time.Unix(1_700_000_000, 0).Add(605 * time.Second)
	if got := a.Tracks[0].StartedAt; !got.Equal(faderUp) {
		t.Fatalf("track start = %v, want fader-up %v", got, faderUp)
	}
}

// During a blend the incoming deck's fader comes up well before it becomes the loudest
// deck - the confirmed start must be the measured fader-up, not the loudest-switch.
func TestBlendStartIsFaderUp(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r := New(logbus.New(64), nil, nil, 10)
	r.clock = func() time.Time { return now }
	step := func(sec int, st session.UnifiedState) {
		now = time.Unix(1_700_000_000, 0).Add(time.Duration(sec) * time.Second)
		r.step(now, st)
	}

	aOnly := mixState(map[string][2]any{"A": {"Track A", true}}, map[string]float64{"A": 1})
	bCued := mixState(map[string][2]any{"A": {"Track A", true}, "B": {"Track B", true}},
		map[string]float64{"A": 1, "B": 0})
	blend := mixState(map[string][2]any{"A": {"Track A", true}, "B": {"Track B", true}},
		map[string]float64{"A": 1, "B": 0.6})
	bLoud := mixState(map[string][2]any{"A": {"Track A", true}, "B": {"Track B", true}},
		map[string]float64{"A": 0, "B": 0.9})

	step(0, aOnly)
	step(5, aOnly)
	step(11, aOnly) // A confirmed
	step(20, bCued) // B cued, fader down - no mark
	step(30, blend) // B fader up → measured on-air mark at t=30
	step(35, blend)
	step(40, bLoud) // A pulled down → B becomes the pick (pendingSince=40)
	step(45, bLoud) // debounce → B current, firstSeen=40
	step(51, bLoud) // confirm (10s from firstSeen=40 → t=50)

	a := r.Active()
	if a == nil || len(a.Tracks) != 2 {
		t.Fatalf("expected 2 confirmed tracks, got %+v", a)
	}
	faderUp := time.Unix(1_700_000_000, 0).Add(30 * time.Second)
	if got := a.Tracks[1].StartedAt; !got.Equal(faderUp) {
		t.Fatalf("blend start = %v, want fader-up %v", got, faderUp)
	}
}

// Without any fader feed the gate fails open and starts stay at the switch time.
func TestNoFaderDataFailsOpen(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r := New(logbus.New(64), nil, nil, 10)
	r.clock = func() time.Time { return now }
	step := func(sec int, st session.UnifiedState) {
		now = time.Unix(1_700_000_000, 0).Add(time.Duration(sec) * time.Second)
		r.step(now, st)
	}
	noFader := mixState(map[string][2]any{"A": {"Track A", true}}, nil)
	step(0, noFader)
	step(5, noFader)
	step(11, noFader)
	a := r.Active()
	if a == nil || len(a.Tracks) != 1 {
		t.Fatalf("mixer-less rig must keep recording, got %+v", a)
	}
	if got := a.Tracks[0].StartedAt; !got.Equal(time.Unix(1_700_000_000, 0)) {
		t.Fatalf("assumed (unmeasured) on-air must not move the start: %v", got)
	}
}

package midimap

import (
	"testing"

	"rave.page/mate/internal/session"
)

// The send map must equal the documented RavePage-State receive contract, control-for-control.
func TestControlsCCContract(t *testing.T) {
	want := map[string]struct {
		cc        byte
		field     string
		momentary bool
		deckScope bool
	}{
		"eqHigh": {24, session.FieldEQHigh, false, false},
		"eqMid":  {25, session.FieldEQMid, false, false},
		"eqLow":  {26, session.FieldEQLow, false, false},
		"filter": {27, session.FieldFilter, false, false},
		"trim":   {29, "", false, false}, // send-only
		"fader":  {23, session.FieldFader, false, false},
		"cue":    {28, session.FieldCue, true, false},
		"play":   {20, session.FieldIsPlaying, true, true},
	}
	if len(Controls) != len(want) {
		t.Fatalf("controls count = %d, want %d", len(Controls), len(want))
	}
	seenCC := map[byte]string{}
	for _, c := range Controls {
		w, ok := want[c.ID]
		if !ok {
			t.Fatalf("unexpected control %q", c.ID)
		}
		if c.CC != w.cc {
			t.Errorf("%s CC = %d, want %d", c.ID, c.CC, w.cc)
		}
		if c.Field != w.field {
			t.Errorf("%s field = %q, want %q", c.ID, c.Field, w.field)
		}
		if (c.Kind == Momentary) != w.momentary {
			t.Errorf("%s momentary = %v, want %v", c.ID, c.Kind == Momentary, w.momentary)
		}
		if c.DeckScope != w.deckScope {
			t.Errorf("%s deckScope = %v, want %v", c.ID, c.DeckScope, w.deckScope)
		}
		if prev, dup := seenCC[c.CC]; dup {
			t.Errorf("CC %d shared by %s and %s", c.CC, prev, c.ID)
		}
		seenCC[c.CC] = c.ID
	}
}

func TestWireChannel(t *testing.T) {
	cases := []struct {
		ch   int
		wire byte
	}{
		{0, 0}, // clamps up to 1 -> wire 0
		{1, 0},
		{2, 1},
		{4, 3},
		{8, 7},
		{9, 7}, // clamps down to MaxChannels
	}
	for _, c := range cases {
		if got := WireChannel(c.ch); got != c.wire {
			t.Errorf("WireChannel(%d) = %d, want %d", c.ch, got, c.wire)
		}
	}
}

func TestClampChannels(t *testing.T) {
	cases := []struct{ in, out int }{
		{0, DefaultChannels},
		{-3, DefaultChannels},
		{1, 1},
		{4, 4},
		{8, 8},
		{99, MaxChannels},
	}
	for _, c := range cases {
		if got := ClampChannels(c.in); got != c.out {
			t.Errorf("ClampChannels(%d) = %d, want %d", c.in, got, c.out)
		}
	}
}

func TestLettersCoverMaxChannels(t *testing.T) {
	if len(Letters) != MaxChannels {
		t.Fatalf("Letters len = %d, want %d", len(Letters), MaxChannels)
	}
}

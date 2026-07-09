package midisrc

import (
	"testing"
	"time"

	"rave.page/mate/internal/midi"
	"rave.page/mate/internal/session"
)

// collect returns every observation the decoder emits for m.
func collect(d decoder, m midi.Message) []session.Observation {
	var out []session.Observation
	d.handle(time.Now(), m, func(o session.Observation) { out = append(out, o) })
	return out
}

func TestLearnedDecoderContinuousChannel(t *testing.T) {
	// EQ-high on channel 2, learned as CC 12 on MIDI channel 3 (status 0xB2).
	d := &learnedDecoder{name: "test", bindings: []LearnedBinding{
		{Control: "eqHigh", Channel: 2, Status: 0xB2, Data1: 12},
	}}
	// Matching CC at half → channel "2" eqHigh ≈ 0.5.
	got := collect(d, midi.Message{Status: 0xB2, Data1: 12, Data2: 64})
	if len(got) != 1 {
		t.Fatalf("want 1 obs, got %d", len(got))
	}
	if got[0].Scope.Kind != session.ScopeChannel || got[0].Scope.ID != "2" {
		t.Fatalf("scope = %v/%q, want channel/2", got[0].Scope.Kind, got[0].Scope.ID)
	}
	v, ok := got[0].Fields[session.FieldEQHigh].(float64)
	if !ok || v < 0.49 || v > 0.51 {
		t.Fatalf("eqHigh = %v (ok=%v), want ~0.5", got[0].Fields[session.FieldEQHigh], ok)
	}
	// Wrong MIDI channel (0xB0) must not match.
	if n := len(collect(d, midi.Message{Status: 0xB0, Data1: 12, Data2: 64})); n != 0 {
		t.Fatalf("wrong midi-channel matched: %d obs", n)
	}
	// Wrong data byte must not match.
	if n := len(collect(d, midi.Message{Status: 0xB2, Data1: 13, Data2: 64})); n != 0 {
		t.Fatalf("wrong data1 matched: %d obs", n)
	}
}

func TestLearnedDecoderInvert(t *testing.T) {
	d := &learnedDecoder{name: "t", bindings: []LearnedBinding{
		{Control: "fader", Channel: 1, Status: 0xB0, Data1: 7, Invert: true},
	}}
	got := collect(d, midi.Message{Status: 0xB0, Data1: 7, Data2: 0}) // min → inverted to 1.0
	if len(got) != 1 || got[0].Fields[session.FieldFader].(float64) < 0.99 {
		t.Fatalf("inverted fader at 0 = %v, want ~1.0", got)
	}
}

func TestLearnedDecoderPlayNoteToDeck(t *testing.T) {
	// Play is deck-scope boolean via Note. Channel 1 → deck A.
	d := &learnedDecoder{name: "t", bindings: []LearnedBinding{
		{Control: "play", Channel: 1, Status: 0x90, Data1: 20},
	}}
	on := collect(d, midi.Message{Status: 0x90, Data1: 20, Data2: 127}) // note-on → playing true
	if len(on) != 1 || on[0].Scope.Kind != session.ScopeDeck || on[0].Scope.ID != "A" {
		t.Fatalf("play-on scope = %v, want deck/A", on)
	}
	if v, _ := on[0].Fields[session.FieldIsPlaying].(bool); !v {
		t.Fatalf("play-on isPlaying = %v, want true", on[0].Fields[session.FieldIsPlaying])
	}
	off := collect(d, midi.Message{Status: 0x80, Data1: 20, Data2: 0}) // note-off → playing false
	if v, ok := off[0].Fields[session.FieldIsPlaying].(bool); !ok || v {
		t.Fatalf("play-off isPlaying = %v, want false", off[0].Fields[session.FieldIsPlaying])
	}
}

func TestLearnedDecoderTrimHasField(t *testing.T) {
	// Trim is send-only in the shared map but a learned controller reports it (FieldTrim).
	d := &learnedDecoder{name: "t", bindings: []LearnedBinding{
		{Control: "trim", Channel: 3, Status: 0xB0, Data1: 5},
	}}
	got := collect(d, midi.Message{Status: 0xB0, Data1: 5, Data2: 127})
	if len(got) != 1 || got[0].Fields[session.FieldTrim] == nil {
		t.Fatalf("trim obs = %v, want a FieldTrim value on channel 3", got)
	}
}

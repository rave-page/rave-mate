package midisrc

import (
	"testing"
	"time"

	"rave.page/mate/internal/midi"
	"rave.page/mate/internal/session"
)

// cc builds a Control Change message on channel ch (0-based) for controller/value.
func cc(ch int, controller, value byte) midi.Message {
	return midi.Message{Status: 0xB0 | byte(ch), Data1: controller, Data2: value}
}

func TestCustomDecoderDeckAndChannel(t *testing.T) {
	var got []session.Observation
	emit := func(o session.Observation) { got = append(got, o) }
	d := customDecoder{}
	now := time.Now()

	// Deck A (channel 0) play on (CC20, value 127) → deck-scope isPlaying=true.
	d.handle(now, cc(0, 20, 127), emit)
	// Channel 2 fader (CC23, value 64) → channel-scope fader ≈ 0.5.
	d.handle(now, cc(1, 23, 64), emit)

	if len(got) != 2 {
		t.Fatalf("want 2 observations, got %d", len(got))
	}
	if got[0].Scope.Kind != session.ScopeDeck || got[0].Scope.ID != "A" || got[0].Fields[session.FieldIsPlaying] != true {
		t.Fatalf("deck obs wrong: %+v", got[0])
	}
	if got[0].Source != session.SourceMIDICustom {
		t.Fatalf("source = %q", got[0].Source)
	}
	if got[1].Scope.Kind != session.ScopeChannel || got[1].Scope.ID != "2" {
		t.Fatalf("channel obs wrong: %+v", got[1])
	}
	if f, _ := got[1].Fields[session.FieldFader].(float64); f < 0.5 || f > 0.51 {
		t.Fatalf("fader = %v, want ~0.5", got[1].Fields[session.FieldFader])
	}
}

func TestCustomDecoderIgnoresUnmappedAndHighChannels(t *testing.T) {
	var got []session.Observation
	emit := func(o session.Observation) { got = append(got, o) }
	d := customDecoder{}
	now := time.Now()
	d.handle(now, cc(0, 99, 127), emit)                                    // unmapped CC
	d.handle(now, cc(8, 20, 127), emit)                                    // channel beyond A..H (0..7)
	d.handle(now, midi.Message{Status: 0x90, Data1: 60, Data2: 100}, emit) // note-on, not CC
	if len(got) != 0 {
		t.Fatalf("expected no observations, got %+v", got)
	}
}

// Extended decks E..H (channels 4..7) decode after the A..H range extension; ch1..4 unchanged.
func TestCustomDecoderExtendedChannels(t *testing.T) {
	var got []session.Observation
	emit := func(o session.Observation) { got = append(got, o) }
	d := customDecoder{}
	now := time.Now()
	// Deck F = channel 5: play on (CC20) -> deck-scope isPlaying=true, ID "F".
	d.handle(now, cc(5, 20, 127), emit)
	// Channel 8 filter (CC27) -> channel-scope filter, ID "8".
	d.handle(now, cc(7, 27, 127), emit)
	if len(got) != 2 {
		t.Fatalf("want 2 observations, got %d: %+v", len(got), got)
	}
	if got[0].Scope.Kind != session.ScopeDeck || got[0].Scope.ID != "F" || got[0].Fields[session.FieldIsPlaying] != true {
		t.Fatalf("deck F obs wrong: %+v", got[0])
	}
	if got[1].Scope.Kind != session.ScopeChannel || got[1].Scope.ID != "8" {
		t.Fatalf("channel 8 obs wrong: %+v", got[1])
	}
}

// A custom decoder with maxCh set rejects channels at/above the cap.
func TestCustomDecoderMaxChannelsCap(t *testing.T) {
	var got []session.Observation
	emit := func(o session.Observation) { got = append(got, o) }
	d := customDecoder{maxCh: 4}
	now := time.Now()
	d.handle(now, cc(3, 23, 64), emit) // deck D (index 3) accepted
	d.handle(now, cc(4, 23, 64), emit) // deck E (index 4) rejected by the cap
	if len(got) != 1 || got[0].Scope.ID != "4" {
		t.Fatalf("expected one channel-4 obs, got %+v", got)
	}
}

func TestDenonDecoderReconstructsText(t *testing.T) {
	d := newDenonDecoder()
	now := time.Unix(1_700_000_000, 0)
	// "deadmau5 - Strobe" streamed to deck A (channel 0), one CC per character slot.
	text := "deadmau5 - Strobe"
	for i := 0; i < len(text); i++ {
		d.handle(now, cc(0, byte(denonCCBase+i), text[i]), nil)
	}
	// Before idle elapses, nothing flushes.
	var got []session.Observation
	emit := func(o session.Observation) { got = append(got, o) }
	d.tick(now.Add(100*time.Millisecond), emit)
	if len(got) != 0 {
		t.Fatalf("flushed too early: %+v", got)
	}
	// After idle, the deck text flushes split into artist/title.
	d.tick(now.Add(denonFlushIdle+time.Millisecond), emit)
	if len(got) != 1 {
		t.Fatalf("want 1 flush, got %d", len(got))
	}
	o := got[0]
	if o.Source != session.SourceMIDIDenon || o.Scope.ID != "A" {
		t.Fatalf("denon obs scope/source wrong: %+v", o)
	}
	if o.Fields[session.FieldArtist] != "deadmau5" || o.Fields[session.FieldTitle] != "Strobe" {
		t.Fatalf("denon split wrong: %+v", o.Fields)
	}
}

func TestDenonDecoderTitleOnly(t *testing.T) {
	d := newDenonDecoder()
	now := time.Unix(1_700_000_000, 0)
	text := "No Separator Here"
	for i := 0; i < len(text); i++ {
		d.handle(now, cc(1, byte(denonCCBase+i), text[i]), nil)
	}
	var got []session.Observation
	d.tick(now.Add(denonFlushIdle+time.Millisecond), func(o session.Observation) { got = append(got, o) })
	if len(got) != 1 || got[0].Scope.ID != "B" || got[0].Fields[session.FieldTitle] != "No Separator Here" {
		t.Fatalf("title-only decode wrong: %+v", got)
	}
}

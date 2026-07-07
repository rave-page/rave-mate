package midisrc

import (
	"strconv"
	"time"

	"rave.page/mate/internal/midi"
	"rave.page/mate/internal/session"
)

// confidenceCustom: live but coarse (normalized knob/button positions).
const confidenceCustom = 0.7

// ccSpec maps one of our custom-TSI CC numbers to a canonical field. continuous values are
// scaled 0..1 from the 0..127 MIDI value; booleans are true at any nonzero value (0 = off/release,
// anything else = on) - Traktor sends 127, rekordbox indicator-out may send a small on-value. The mixer
// continuous controls live on the channel scope (channel N = deck N); the transport boolean
// lives on the deck scope. See docs/MIDI_MAPPING.md - this layout is the contract the
// shipped RavePage-State.tsi must match.
type ccSpec struct {
	field   string
	scope   session.ScopeKind
	boolean bool
}

// customCC is the canonical custom map. MIDI channels 1..4 (index 0..3) = decks A..D.
var customCC = map[byte]ccSpec{
	20: {session.FieldIsPlaying, session.ScopeDeck, true},
	23: {session.FieldFader, session.ScopeChannel, false},
	24: {session.FieldEQHigh, session.ScopeChannel, false},
	25: {session.FieldEQMid, session.ScopeChannel, false},
	26: {session.FieldEQLow, session.ScopeChannel, false},
	27: {session.FieldFilter, session.ScopeChannel, false},
	28: {session.FieldCue, session.ScopeChannel, true},
}

var deckLetters = []string{"A", "B", "C", "D"}

// customDecoder decodes our custom CC map into per-deck/channel state observations.
type customDecoder struct{}

func (customDecoder) id() string { return "custom" }

// handle decodes one message and emits an observation if it matches the custom map.
func (customDecoder) handle(_ time.Time, m midi.Message, emit func(session.Observation)) {
	if !m.IsCC() {
		return
	}
	ch := m.Channel()
	if ch > 3 {
		return // we only map decks A..D
	}
	spec, ok := customCC[m.Controller()]
	if !ok {
		return
	}
	var value any
	if spec.boolean {
		value = m.Value() > 0
	} else {
		value = float64(m.Value()) / 127.0
	}
	scope := session.Scope{Kind: spec.scope}
	switch spec.scope {
	case session.ScopeDeck:
		scope.ID = deckLetters[ch]
	case session.ScopeChannel:
		scope.ID = strconv.Itoa(ch + 1)
	}
	emit(session.Observation{
		Source:     session.SourceMIDICustom,
		Scope:      scope,
		Fields:     map[string]any{spec.field: value},
		Confidence: confidenceCustom,
	})
}

// tick is a no-op for the custom decoder (stateless) but satisfies the decoder interface.
func (customDecoder) tick(_ time.Time, _ func(session.Observation)) {}

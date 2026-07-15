package midisrc

import (
	"strconv"
	"time"

	"rave.page/mate/internal/midi"
	"rave.page/mate/internal/midimap"
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

// customCC is the canonical custom map, derived from the shared midimap so send (mixer UI) and
// receive (this decoder) can never drift. Send-only controls (Trim) have no field and are skipped.
// MIDI channels 1..N (index 0..N-1) = decks A..H (see deckLetters / midimap.Letters).
var customCC = buildCustomCC()

func buildCustomCC() map[byte]ccSpec {
	m := make(map[byte]ccSpec, len(midimap.Controls))
	for _, c := range midimap.Controls {
		if c.Field == "" {
			continue // send-only (Trim): no overlay field consumes it
		}
		scope := session.ScopeChannel
		if c.DeckScope {
			scope = session.ScopeDeck
		}
		m[c.CC] = ccSpec{field: c.Field, scope: scope, boolean: c.Kind == midimap.Momentary}
	}
	return m
}

// deckLetters names decks by 0-based MIDI channel (shared with the mixer UI + denon decoder).
var deckLetters = midimap.Letters

// customDecoder decodes our custom CC map into per-deck/channel state observations. maxCh caps the
// accepted MIDI channel count (1..len(deckLetters)); 0 = accept every mapped deck (A..H).
type customDecoder struct{ maxCh int }

func (customDecoder) id() string { return "custom" }

// handle decodes one message and emits an observation if it matches the custom map. CC feeds every
// mapped field (Traktor RavePage-State.tsi + any CC feedback); Note On/Off feeds the momentary
// booleans (Play/Cue) - the mixer sends buttons as notes, and a mixer-learned Rekordbox/Serato echoes
// them back as notes. The send/receive numbers are identical (midimap), so notes and CC round-trip.
func (d customDecoder) handle(_ time.Time, m midi.Message, emit func(session.Observation)) {
	ch := int(m.Channel())
	mc := d.maxCh
	if mc <= 0 || mc > len(deckLetters) {
		mc = len(deckLetters)
	}
	if ch < 0 || ch >= mc {
		return // channel beyond the addressable deck range (A..H)
	}

	var (
		spec  ccSpec
		ok    bool
		value any
	)
	switch {
	case m.IsCC():
		spec, ok = customCC[m.Controller()]
		if !ok {
			return
		}
		if spec.boolean {
			value = m.Value() > 0
		} else {
			value = float64(m.Value()) / 127.0
		}
	case m.IsNoteOn() || m.IsNoteOff():
		// Only the boolean button controls (Play/Cue) map from notes; a note on a continuous
		// control's number is ignored (EQ/filter/fader are CC-only).
		spec, ok = customCC[m.Note()]
		if !ok || !spec.boolean {
			return
		}
		// A momentary Play button pulses on-press then off-release; the release is NOT
		// "stopped playing". Emitting isPlaying=false on key-up dropped the deck from
		// now-playing one frame after it appeared (flash-then-empty). Key-up is a no-op for
		// Play; the deck stays now-playing until a load/staleness/real-time source clears it.
		if m.IsNoteOff() && spec.field == session.FieldIsPlaying {
			return
		}
		value = m.IsNoteOn()
	default:
		return
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

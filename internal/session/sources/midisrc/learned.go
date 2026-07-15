package midisrc

import (
	"strconv"
	"time"

	"rave.page/mate/internal/midi"
	"rave.page/mate/internal/midimap"
	"rave.page/mate/internal/session"
)

// controlMeta resolves a midimap Control ID to its session field/scope/kind for the learned
// decoder. Trim is send-only in the shared map (no overlay field); a learned controller CAN
// report it, so it maps to FieldTrim here (channel scope).
type controlMeta struct {
	field   string
	scope   session.ScopeKind
	boolean bool
}

var controlByID = buildControlByID()

func buildControlByID() map[string]controlMeta {
	m := make(map[string]controlMeta, len(midimap.Controls))
	for _, c := range midimap.Controls {
		field := c.Field
		if field == "" && c.ID == "trim" {
			field = session.FieldTrim
		}
		scope := session.ScopeChannel
		if c.DeckScope {
			scope = session.ScopeDeck
		}
		m[c.ID] = controlMeta{field: field, scope: scope, boolean: c.Kind == midimap.Momentary}
	}
	return m
}

// LearnedBinding is one learned MIDI→control mapping. Status carries the type nibble + MIDI
// channel captured at learn; a CC binding matches CC messages, a Note binding matches Note-On/
// Off. Channel is the 1-based deck/mixer channel the value is applied to. Decoupled from
// config so this package doesn't import config.
type LearnedBinding struct {
	Control string
	Channel int
	Status  byte
	Data1   byte
	Invert  bool
}

// matches reports whether m is the learned message: same data byte, same MIDI channel, same
// type family (CC vs Note - Note-On and Note-Off both match a Note binding).
func (b LearnedBinding) matches(m midi.Message) bool {
	if b.Data1 != m.Data1 {
		return false
	}
	if (b.Status & 0x0F) != (m.Status & 0x0F) {
		return false
	}
	switch b.Status & 0xF0 {
	case 0xB0:
		return m.IsCC()
	case 0x80, 0x90:
		return m.IsNoteOn() || m.IsNoteOff()
	}
	return false
}

// learnedDecoder applies one controller's learned bindings to the shared deck/channel model.
// Stateless (no per-message state), so it's safe to run across ports and the inject path. All
// controllers emit under SourceMIDICustom so they fuse into one model (higher-confidence/newer
// wins) - "all drive the same system".
type learnedDecoder struct {
	name     string
	bindings []LearnedBinding
}

func (d *learnedDecoder) id() string { return "learn:" + d.name }

func (d *learnedDecoder) handle(_ time.Time, m midi.Message, emit func(session.Observation)) {
	if m.IsSystem() {
		return
	}
	for _, b := range d.bindings {
		if !b.matches(m) {
			continue
		}
		meta, ok := controlByID[b.Control]
		if !ok || meta.field == "" {
			continue
		}
		if b.Channel < 1 || b.Channel > len(deckLetters) {
			continue
		}
		var value any
		if meta.boolean {
			switch {
			case m.IsCC():
				value = m.Value() > 0
			case m.IsNoteOn():
				value = true
			default: // note-off
				// A momentary Play button sends on-press THEN off-release (a pulse); the
				// release is NOT "stopped playing". Emitting isPlaying=false here dropped the
				// deck from DeriveNowPlaying one frame after it appeared - the flash-then-empty
				// bug. A button can't express sustained transport, so key-up is a no-op; the
				// deck stays now-playing until a new load, staleness, or a real-time source
				// (Serato Remote / feedback) says otherwise.
				if meta.field == session.FieldIsPlaying {
					continue
				}
				value = false
			}
		} else {
			v := float64(m.Value()) / 127.0
			if b.Invert {
				v = 1 - v
			}
			value = v
		}
		scope := session.Scope{Kind: meta.scope}
		if meta.scope == session.ScopeDeck {
			scope.ID = deckLetters[b.Channel-1]
		} else {
			scope.ID = strconv.Itoa(b.Channel)
		}
		emit(session.Observation{
			Source:     session.SourceMIDICustom,
			Scope:      scope,
			Fields:     map[string]any{meta.field: value},
			Confidence: confidenceCustom,
		})
	}
}

func (d *learnedDecoder) tick(_ time.Time, _ func(session.Observation)) {}

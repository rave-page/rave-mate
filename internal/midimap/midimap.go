// Package midimap is the single source of truth for rave-mate's virtual-mixer MIDI map: the
// CC each mixer control SENDS (webui mixer surface -> loopback port) is byte-identical to what
// the RECEIVE decoder (session/sources/midisrc/custom.go) reads back. That equality is what
// closes the learn round-trip: the user wiggles a control here so their DJ software MIDI-learns
// rave-mate's outgoing CC; they then point that software control's output/feedback at the SAME
// CC, so its value echoes back and the decoder drives the live overlay.
//
// All controls are Control Change (not note) messages - the decoder only reads CC, so a note
// would never round-trip. MIDI wire channel = deck index (channel 1 / deck A = wire 0). Trim is
// send-only (no overlay field yet): the software can learn it, nothing consumes it.
package midimap

import "rave.page/mate/internal/session"

// Letters names decks A..H by 0-based wire channel. Length caps the addressable channel/deck
// count; both the mixer UI and the receive decoder derive their range from it.
var Letters = []string{"A", "B", "C", "D", "E", "F", "G", "H"}

const (
	// MaxChannels is the mixer channel/deck cap (decks A..H = wire MIDI channels 0..7).
	MaxChannels = 8
	// DefaultChannels is the out-of-box channel count.
	DefaultChannels = 4
)

// Kind classifies a control's send behaviour.
type Kind int

const (
	// Continuous sends a live 0..127 value as it moves (EQ / filter / trim / fader).
	Continuous Kind = iota
	// Momentary sends an on (127) then off (0) pulse (Play / Cue).
	Momentary
)

// Control describes one mixer control: the CC it sends on, the canonical session field it
// echoes into on receive (session.Field*; "" = send-only), whether it pulses, and its scope.
type Control struct {
	ID        string // stable UI id (eqHigh, eqMid, eqLow, filter, trim, fader, cue, play)
	LabelKey  string // i18n key suffix -> midictl.ctl.<LabelKey>
	CC        byte
	Field     string // session.Field* the receive decoder writes; "" = send-only (no overlay)
	Kind      Kind
	DeckScope bool // true = deck-scope boolean (Play); false = channel scope
}

// Controls is the ordered mixer strip (top -> bottom). SINGLE SOURCE OF TRUTH for send + receive.
// CC numbers MUST match the RavePage-State mapping contract (docs/MIDI_MAPPING.md).
var Controls = []Control{
	{ID: "eqHigh", LabelKey: "eqHigh", CC: 24, Field: session.FieldEQHigh, Kind: Continuous},
	{ID: "eqMid", LabelKey: "eqMid", CC: 25, Field: session.FieldEQMid, Kind: Continuous},
	{ID: "eqLow", LabelKey: "eqLow", CC: 26, Field: session.FieldEQLow, Kind: Continuous},
	{ID: "filter", LabelKey: "filter", CC: 27, Field: session.FieldFilter, Kind: Continuous},
	{ID: "trim", LabelKey: "trim", CC: 29, Field: "", Kind: Continuous}, // send-only: no overlay field
	{ID: "fader", LabelKey: "fader", CC: 23, Field: session.FieldFader, Kind: Continuous},
	{ID: "cue", LabelKey: "cue", CC: 28, Field: session.FieldCue, Kind: Momentary},
	{ID: "play", LabelKey: "play", CC: 20, Field: session.FieldIsPlaying, Kind: Momentary, DeckScope: true},
}

// WireChannel maps a 1-based mixer channel to its 0-based MIDI wire channel (ch 1 -> 0). Values
// outside 1..MaxChannels clamp into range.
func WireChannel(ch int) byte {
	if ch < 1 {
		ch = 1
	}
	if ch > MaxChannels {
		ch = MaxChannels
	}
	return byte(ch - 1)
}

// ClampChannels bounds a configured channel count to 1..MaxChannels (0/negative -> DefaultChannels).
func ClampChannels(n int) int {
	if n <= 0 {
		return DefaultChannels
	}
	if n > MaxChannels {
		return MaxChannels
	}
	return n
}

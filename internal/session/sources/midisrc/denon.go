package midisrc

import (
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/midi"
	"rave.page/mate/internal/session"
)

// confidenceDenon: delayed + charset-limited text, lower than a live HTTP/QML title.
const confidenceDenon = 0.6

const (
	denonFlushIdle = 300 * time.Millisecond // emit once the text stops changing
	denonCCBase    = 1                      // CC number of character slot 0
)

// denonDecoder reconstructs deck A/B track text from the Denon HC4500 stock mapping. The
// mapping streams the LCD text over CC: channel 0 → deck A, channel 1 → deck B, the CC
// number is the character slot, and the value carries the character.
//
// BEST-EFFORT: the exact slot layout (and whether a firmware splits a char across two
// nibble messages) varies; this decodes the value as a direct 7-bit ASCII code, which is
// the common case when Traktor's MIDI Out is pointed at a virtual port. If a setup uses
// nibble-pair encoding, swap decodeChar (see docs/MIDI_MAPPING.md). Validate against real
// hardware before trusting decks-A/B titles from this source.
type denonDecoder struct {
	mu    sync.Mutex
	decks map[int]*denonBuf
}

type denonBuf struct {
	chars    map[int]byte
	maxSlot  int
	lastSeen time.Time
	dirty    bool
}

func newDenonDecoder() *denonDecoder { return &denonDecoder{decks: map[int]*denonBuf{}} }

func (d *denonDecoder) id() string { return "denon" }

func (d *denonDecoder) handle(now time.Time, m midi.Message, _ func(session.Observation)) {
	if !m.IsCC() {
		return
	}
	ch := m.Channel()
	if ch != 0 && ch != 1 { // deck A / deck B only
		return
	}
	slot := int(m.Controller()) - denonCCBase
	if slot < 0 {
		return
	}
	d.mu.Lock()
	b := d.decks[ch]
	if b == nil {
		b = &denonBuf{chars: map[int]byte{}}
		d.decks[ch] = b
	}
	b.chars[slot] = decodeChar(m.Value())
	if slot > b.maxSlot {
		b.maxSlot = slot
	}
	b.lastSeen = now
	b.dirty = true
	d.mu.Unlock()
}

// tick flushes any deck whose text has settled (no new chars for denonFlushIdle).
func (d *denonDecoder) tick(now time.Time, emit func(session.Observation)) {
	d.mu.Lock()
	type pending struct {
		ch   int
		text string
	}
	var out []pending
	for ch, b := range d.decks {
		if b.dirty && now.Sub(b.lastSeen) >= denonFlushIdle {
			out = append(out, pending{ch, assemble(b)})
			b.dirty = false
		}
	}
	d.mu.Unlock()

	for _, p := range out {
		if p.text == "" {
			continue
		}
		emit(observationFromText(p.ch, p.text))
	}
}

// decodeChar maps a 7-bit MIDI value to a character (direct ASCII).
func decodeChar(v byte) byte { return v & 0x7F }

// assemble builds the buffered text from slot 0..maxSlot, dropping control bytes.
func assemble(b *denonBuf) string {
	var sb strings.Builder
	for i := 0; i <= b.maxSlot; i++ {
		c := b.chars[i]
		if c == 0 {
			sb.WriteByte(' ') // gap in the slot map
			continue
		}
		if c < 0x20 { // skip other control chars
			continue
		}
		sb.WriteByte(c)
	}
	return strings.TrimSpace(sb.String())
}

// observationFromText turns reconstructed deck text into a title/artist observation. Denon
// LCDs commonly show "Artist - Title"; split on the first " - " when present.
func observationFromText(ch int, text string) session.Observation {
	deck := deckLetters[ch]
	fields := map[string]any{}
	if i := strings.Index(text, " - "); i > 0 {
		fields[session.FieldArtist] = strings.TrimSpace(text[:i])
		fields[session.FieldTitle] = strings.TrimSpace(text[i+3:])
	} else {
		fields[session.FieldTitle] = text
	}
	return session.Observation{
		Source:     session.SourceMIDIDenon,
		Scope:      session.Scope{Kind: session.ScopeDeck, ID: deck},
		Fields:     fields,
		Confidence: confidenceDenon,
	}
}

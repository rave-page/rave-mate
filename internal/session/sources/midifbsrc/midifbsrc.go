// Package midifbsrc derives real-time per-deck play/pause from the ravemidi driver's
// LED-feedback stream. DJ software (Serato) FLASHES a deck's play LED while paused/cued and
// holds it SOLID (or quiet) while playing; the driver captures those app→device writes as
// FeedbackOut trace entries (IOCTL_RAVEMIDI_QUERY_TRACE). We poll them and map a SUSTAINED
// flash → isPlaying=false, a settled-lit LED → isPlaying=true.
//
// Why: for a MIDI-only Serato rig this is the ONLY real-time play/pause signal - the History
// file lags (and only records play-commits, never pause), the momentary Play button can't
// express sustained transport, and Serato Remote is discontinued. Confirmed on the wire:
// a paused DJ2GO2 deck streams NoteOn note0/1 alternating vel 127↔1 at ~2 Hz on its deck
// channel; both decks playing → the flash stops entirely.
//
// Heuristic + controller-specific (the flash pattern / play-LED note varies per controller
// mapping), so it self-rates below explicit real-time protocols but ABOVE the momentary Play
// button, which it corrects. Windows-only in effect: QueryDriverInputs/QueryDriverTrace stub
// out to ErrUnsupported elsewhere, so the poll loop no-ops.
package midifbsrc

import (
	"context"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/midi"
	"rave.page/mate/internal/session"
)

const (
	confidence = 0.85                   // authoritative for play/pause (Serato's own LED); metadata comes from elsewhere
	pollEvery  = 350 * time.Millisecond // fast enough for sub-second play/pause; QUERY_TRACE is a cheap read
	refreshTTL = 3 * time.Second        // re-assert a held state under FieldIsPlaying's 5s merge ttl so it stays authoritative
	maxDeckCh  = 4                      // only decks A..D (MIDI ch 1..4); higher channels are non-deck LEDs (VU/fx) - ignore
	// flashStreak: consecutive polls with NEW feedback that mark a SUSTAINED flash (=paused).
	// A play-start "go solid" is a one-shot NoteOn (a single active poll), so requiring two
	// distinguishes a flashing (paused) LED from a settling (playing) one.
	flashStreak = 2
)

// Source polls the ravemidi driver's feedback trace and emits deck play/pause.
type Source struct {
	log *logbus.Bus
	det detector
}

// New builds the source.
func New(log *logbus.Bus) *Source { return &Source{log: log, det: newDetector()} }

// ID implements session.Source.
func (s *Source) ID() string { return session.SourceMIDIFeedback }

// Capabilities implements session.Source: deck play-state only (no metadata).
func (s *Source) Capabilities() []session.Capability {
	return []session.Capability{
		{Scope: session.ScopeDeck, IDs: []string{"A", "B", "C", "D"}, Fields: []string{session.FieldIsPlaying}},
	}
}

// Start polls until ctx is cancelled. No-op (blocks) when the ravemidi driver isn't installed
// (nothing to read - the feedback path only exists behind the kernel driver).
func (s *Source) Start(ctx context.Context, emit func(session.Observation)) error {
	if !midi.DriverInstalled() {
		<-ctx.Done()
		return nil
	}
	t := time.NewTicker(pollEvery)
	defer t.Stop()
	last := map[string]bool{}     // deck → last-emitted isPlaying
	haveLast := map[string]bool{} // deck → we've emitted at least once
	lastAt := map[string]time.Time{}
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-t.C:
			for deck, playing := range s.det.step(collect()) {
				if haveLast[deck] && last[deck] == playing && now.Sub(lastAt[deck]) < refreshTTL {
					continue // unchanged + still fresh under the merge ttl - skip
				}
				last[deck], haveLast[deck], lastAt[deck] = playing, true, now
				emit(session.Observation{
					Source:     session.SourceMIDIFeedback,
					Scope:      session.Scope{Kind: session.ScopeDeck, ID: deck},
					Fields:     map[string]any{session.FieldIsPlaying: playing},
					Confidence: confidence,
				})
			}
		}
	}
}

// fbEvent is one FeedbackOut note event on a deck channel (on = velocity > 0 = LED lit).
type fbEvent struct {
	seq  uint64
	deck string
	on   bool
}

// collect reads every managed input's reserved-port trace and returns the FeedbackOut note
// events on deck channels A..D. Errors/absent driver → empty (poll no-ops).
func collect() []fbEvent {
	inputs, err := midi.QueryDriverInputs()
	if err != nil || len(inputs) == 0 {
		return nil
	}
	var out []fbEvent
	for _, in := range inputs {
		if in.ReservedPortID == 0 {
			continue
		}
		es, err := midi.QueryDriverTrace(in.ReservedPortID)
		if err != nil {
			continue
		}
		for _, e := range es {
			if e.Dir != midi.TraceDirFeedbackOut || len(e.Bytes) < 3 {
				continue
			}
			hi := e.Bytes[0] & 0xF0
			if hi != 0x90 && hi != 0x80 { // Note-On / Note-Off only (LED writes)
				continue
			}
			ch := int(e.Bytes[0] & 0x0F) // 0-based MIDI channel → deck index
			if ch >= maxDeckCh {
				continue
			}
			on := hi == 0x90 && e.Bytes[2] > 0 // NoteOff or vel0 = LED off
			out = append(out, fbEvent{seq: e.Seq, deck: deckLetter(ch), on: on})
		}
	}
	return out
}

// detector tracks cross-poll flash state. Pure (no driver deps) so step() is unit-testable.
type detector struct {
	seq    map[string]uint64 // deck → highest FeedbackOut Seq processed (per owning port; one controller per deck)
	streak map[string]int    // deck → consecutive polls with new feedback (the flash detector)
	ledOn  map[string]bool   // deck → last-known LED lit state
}

func newDetector() detector {
	return detector{seq: map[string]uint64{}, streak: map[string]int{}, ledOn: map[string]bool{}}
}

// step folds one poll's feedback events into play/pause per deck that shows LED activity.
// Sustained flash (>=flashStreak active polls) = paused; settled + lit = playing; LED off =
// not playing. A deck absent from evs keeps its prior state (its stale ring entries reappear
// each poll while a track is loaded, so "playing" decks stay present with no new Seq).
func (d *detector) step(evs []fbEvent) map[string]bool {
	type agg struct {
		maxSeq  uint64
		hasNew  bool
		onAtMax bool
	}
	per := map[string]*agg{}
	for _, e := range evs {
		a := per[e.deck]
		if a == nil {
			a = &agg{}
			per[e.deck] = a
		}
		if e.seq > d.seq[e.deck] {
			a.hasNew = true
		}
		if e.seq >= a.maxSeq {
			a.maxSeq, a.onAtMax = e.seq, e.on
		}
	}
	out := map[string]bool{}
	for deck, a := range per {
		if a.hasNew {
			d.streak[deck]++
			d.ledOn[deck] = a.onAtMax
			d.seq[deck] = a.maxSeq
		} else {
			d.streak[deck] = 0
		}
		switch {
		case d.streak[deck] >= flashStreak:
			out[deck] = false // sustained flash = paused/cued
		case d.streak[deck] == 0:
			out[deck] = d.ledOn[deck] // settled: lit = playing, dark = stopped
		default:
			// transient (LED just changed, not yet a sustained flash): hold the prior
			// classification instead of flipping - kills the startup/transition blip where
			// a paused deck's first flash frame would momentarily read as playing.
			continue
		}
	}
	return out
}

// deckLetter maps a 0-based deck channel to a letter (0→A … 3→D).
func deckLetter(ch int) string { return string(rune('A' + ch)) }

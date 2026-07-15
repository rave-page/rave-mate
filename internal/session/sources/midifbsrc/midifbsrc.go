// Package midifbsrc derives real-time per-deck play/pause from the ravemidi driver's
// LED-feedback stream. DJ software (Serato) FLASHES a deck's play LED while paused/cued/ended
// and goes SILENT while playing; the driver captures those app→device writes as FeedbackOut
// trace entries (IOCTL_RAVEMIDI_QUERY_TRACE).
//
// Confirmed live on the wire (DJ2GO2 + Serato): a paused deck streams ~8-24 Note events/s on
// its deck channel (notes 0/1, vel 127↔1); a PLAYING deck emits NOTHING at all (silent). So
// the signal is FLASH vs NO-FLASH - NOT the LED on/off level. A deck that was flashing (a
// loaded, paused deck) and then goes silent = playing; while it flashes = paused. We never
// key off velocity, so it's robust to however Serato ends the transition (NoteOff vs solid on).
//
// Why: for a MIDI-only Serato rig this is the ONLY real-time play/pause signal - the History
// file lags and never records a pause, the momentary Play button can't express sustained
// transport, and Serato Remote is discontinued. Heuristic + controller-specific (flash
// pattern varies), so it self-rates below explicit real-time protocols but ABOVE the momentary
// Play button, which it corrects. Windows-only in effect: QueryDriverInputs/QueryDriverTrace
// stub to ErrUnsupported elsewhere, so the poll loop no-ops.
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
	// One poll of activity (a transition twitch) isn't enough; two rules out one-shot events.
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

// fbEvent is one FeedbackOut note event on a deck channel (velocity is irrelevant - presence
// of the sustained stream is the signal).
type fbEvent struct {
	seq  uint64
	deck string
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
			if e.Dir != midi.TraceDirFeedbackOut || len(e.Bytes) < 1 {
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
			out = append(out, fbEvent{seq: e.Seq, deck: deckLetter(ch)})
		}
	}
	return out
}

// detector tracks cross-poll flash state. Pure (no driver deps) so step() is unit-testable.
type detector struct {
	seq         map[string]uint64 // deck → highest FeedbackOut Seq processed (per owning port; one controller per deck)
	streak      map[string]int    // deck → consecutive polls with new feedback (the flash detector)
	everFlashed map[string]bool   // deck → has ever sustained a flash (=a loaded deck we've seen paused)
}

func newDetector() detector {
	return detector{seq: map[string]uint64{}, streak: map[string]int{}, everFlashed: map[string]bool{}}
}

// step folds one poll's feedback events into play/pause per deck. Signal = flash vs silence:
//   - SUSTAINED flash (new feedback across >=flashStreak polls) → paused/cued/ended (false).
//   - a deck that HAS flashed (loaded, seen paused) and is now SETTLED (no new feedback) →
//     playing (true) - a playing deck emits nothing, so silence-after-flash IS the play signal.
//   - a transient twitch, or a deck we've never seen flash → unclassified (defer to the Play
//     button + History); we don't guess about decks we've never observed paused.
//
// Every ever-flashed deck is re-evaluated each poll (not just decks with events this poll),
// since a playing deck produces no events and must be HELD, not dropped.
func (d *detector) step(evs []fbEvent) map[string]bool {
	type agg struct {
		maxSeq uint64
		hasNew bool
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
		if e.seq > a.maxSeq {
			a.maxSeq = e.seq
		}
	}
	decks := map[string]struct{}{}
	for dk := range d.everFlashed {
		decks[dk] = struct{}{}
	}
	for dk := range per {
		decks[dk] = struct{}{}
	}
	out := map[string]bool{}
	for deck := range decks {
		if a := per[deck]; a != nil && a.hasNew {
			d.streak[deck]++
			d.seq[deck] = a.maxSeq
		} else {
			d.streak[deck] = 0
		}
		switch {
		case d.streak[deck] >= flashStreak:
			d.everFlashed[deck] = true
			out[deck] = false // sustained flash = paused/cued/ended
		case d.streak[deck] == 0 && d.everFlashed[deck]:
			out[deck] = true // was flashing, now silent = playing
		default:
			// transient (1..flashStreak-1), or a deck never seen flashing: don't classify.
		}
	}
	return out
}

// deckLetter maps a 0-based deck channel to a letter (0→A … 3→D).
func deckLetter(ch int) string { return string(rune('A' + ch)) }

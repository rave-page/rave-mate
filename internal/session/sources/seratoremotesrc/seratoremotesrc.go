// Package seratoremotesrc adapts the real-time Serato Remote protocol (internal/seratoremote)
// into a session Source. Serato DJ Pro connects to our advertised _SeratoIOSRemote._tcp
// service and streams per-deck state; we normalize it to deck/channel/master Observations
// at HIGH confidence (real-time, pushed by Serato itself - richer + faster than the
// file-tail seratosrc). Deck indices are 0-based 0..3 → deck letters A..D / channels 1..4.
//
// Field caveats (UNVERIFIED, see internal/seratoremote docs): the three Playhead floats are
// best-guessed (position, length, bpm); play state isn't an explicit protocol message, so
// isPlaying is DERIVED from playhead advancement. Loop + crossfader ride as passthrough
// fields (no canonical vocabulary entry).
package seratoremotesrc

import (
	"context"
	"strconv"
	"sync"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/seratoremote"
	"rave.page/mate/internal/session"
)

const confidence = 0.9 // real-time, pushed by Serato; above the file-tail seratosrc (0.8)

// Config controls the source.
type Config struct {
	Debug bool // log every inbound OSC frame to the logbus (handshake capture)
}

// Source streams Serato Remote now-playing into the merger.
type Source struct {
	log *logbus.Bus
	cfg Config

	mu       sync.Mutex
	lastPath [seratoremote.NumDecks]string  // per-deck last FilePath (loaded boundary)
	lastPos  [seratoremote.NumDecks]float32 // per-deck last playhead position (isPlaying derive)
}

// New builds the source.
func New(log *logbus.Bus, cfg Config) *Source { return &Source{log: log, cfg: cfg} }

// ID implements session.Source.
func (s *Source) ID() string { return session.SourceSeratoRemote }

// Capabilities implements session.Source.
func (s *Source) Capabilities() []session.Capability {
	return []session.Capability{
		{Scope: session.ScopeDeck, IDs: []string{"A", "B", "C", "D"}, Fields: []string{
			session.FieldTitle, session.FieldArtist, session.FieldPath,
			session.FieldIsPlaying, session.FieldElapsedTime, session.FieldTrackLength, session.FieldBPM,
		}},
		{Scope: session.ScopeChannel, IDs: []string{"1", "2", "3", "4"}, Fields: []string{session.FieldFader}},
	}
}

// Start advertises the service, accepts Serato's connections, and emits Observations until
// ctx is cancelled. Blocks (the Receiver runs its loops in panic-guarded goroutines and
// returns when ctx is done).
func (s *Source) Start(ctx context.Context, emit func(session.Observation)) error {
	cb := seratoremote.Callbacks{
		OnPaired:     func(addr string) { s.log.Info("serato.remote", "paired", map[string]any{"peer": addr}) },
		OnDeckChange: func(d seratoremote.DeckChange) { s.onDeck(d, emit) },
		OnPlayhead:   func(p seratoremote.PlayheadEvent) { s.onPlayhead(p, emit) },
		OnLoop:       func(l seratoremote.LoopEvent) { s.onLoop(l, emit) },
		OnMixer:      func(m seratoremote.MixerEvent) { s.onMixer(m, emit) },
	}
	if s.cfg.Debug {
		cb.OnFrame = func(f seratoremote.FrameEvent) {
			s.log.Debug("serato.remote", "frame", map[string]any{"path": f.Path, "tag": f.TypeTag, "args": f.Args, "hex": f.Hex})
		}
	}
	r := seratoremote.New(seratoremote.Options{Debug: s.cfg.Debug}, cb, s.log)
	return r.Start(ctx)
}

func (s *Source) onDeck(d seratoremote.DeckChange, emit func(session.Observation)) {
	deck := deckLetter(d.Deck)
	if d.Track == nil { // eject: Loaded boundary clears prior winners
		s.mu.Lock()
		s.lastPath[d.Deck] = ""
		s.mu.Unlock()
		emit(session.Observation{Source: session.SourceSeratoRemote, Scope: session.Scope{Kind: session.ScopeDeck, ID: deck}, Confidence: confidence, Loaded: true})
		return
	}
	fields := map[string]any{}
	if d.Track.Title != "" {
		fields[session.FieldTitle] = d.Track.Title
	}
	if d.Track.Artist != "" {
		fields[session.FieldArtist] = d.Track.Artist
	}
	if d.Track.FilePath != "" {
		fields[session.FieldPath] = d.Track.FilePath
	}
	// A changed file path is the authoritative "new track" signal → Loaded boundary.
	s.mu.Lock()
	loaded := d.Track.FilePath != "" && d.Track.FilePath != s.lastPath[d.Deck]
	if loaded {
		s.lastPath[d.Deck] = d.Track.FilePath
		s.lastPos[d.Deck] = 0
	}
	s.mu.Unlock()
	if len(fields) == 0 {
		return
	}
	emit(session.Observation{Source: session.SourceSeratoRemote, Scope: session.Scope{Kind: session.ScopeDeck, ID: deck}, Fields: fields, Confidence: confidence, Loaded: loaded})
}

func (s *Source) onPlayhead(p seratoremote.PlayheadEvent, emit func(session.Observation)) {
	// isPlaying DERIVED: position advanced since the last coalesced sample → playing.
	s.mu.Lock()
	playing := p.PositionSeconds > s.lastPos[p.Deck]
	s.lastPos[p.Deck] = p.PositionSeconds
	s.mu.Unlock()
	fields := map[string]any{
		session.FieldElapsedTime: float64(p.PositionSeconds),
		session.FieldIsPlaying:   playing,
	}
	if p.LengthSeconds > 0 {
		fields[session.FieldTrackLength] = float64(p.LengthSeconds)
	}
	if p.BPM > 0 {
		fields[session.FieldBPM] = float64(p.BPM)
	}
	emit(session.Observation{Source: session.SourceSeratoRemote, Scope: session.Scope{Kind: session.ScopeDeck, ID: deckLetter(p.Deck)}, Fields: fields, Confidence: confidence})
}

func (s *Source) onLoop(l seratoremote.LoopEvent, emit func(session.Observation)) {
	// Passthrough (no canonical loop vocabulary); consumers that care read these keys.
	emit(session.Observation{Source: session.SourceSeratoRemote, Scope: session.Scope{Kind: session.ScopeDeck, ID: deckLetter(l.Deck)}, Fields: map[string]any{
		"loopActive": l.AutoLoopOn, "loopBeats": float64(l.BeatLength), "loopRoll": l.LoopRollOn,
	}, Confidence: confidence})
}

func (s *Source) onMixer(m seratoremote.MixerEvent, emit func(session.Observation)) {
	switch {
	case m.HasUpfader:
		emit(session.Observation{Source: session.SourceSeratoRemote, Scope: session.Scope{Kind: session.ScopeChannel, ID: strconv.Itoa(m.Deck + 1)}, Fields: map[string]any{session.FieldFader: float64(m.Upfader)}, Confidence: confidence})
	case m.HasCrossfader:
		emit(session.Observation{Source: session.SourceSeratoRemote, Scope: session.Scope{Kind: session.ScopeMaster}, Fields: map[string]any{"crossfader": float64(m.Crossfader)}, Confidence: confidence})
	}
}

// deckLetter maps a 0-based deck index to a letter (0→A … 3→D), falling back to a number.
func deckLetter(i int) string {
	if i >= 0 && i < 26 {
		return string(rune('A' + i))
	}
	return strconv.Itoa(i)
}

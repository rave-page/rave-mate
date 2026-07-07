// Package seratosrc adapts the Serato decoder (internal/serato) into a session Source.
// It loads the collection (database V2 + crates) for libsync and, when now-playing is
// enabled, polls the newest History session file by mtime and emits the latest played
// entry to the merger. Sessions carry a deck number, so observations target a deck scope
// (1→A …) when known, else master. Delayed (file-poll) but metadata-rich.
package seratosrc

import (
	"context"
	"os"
	"strconv"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/serato"
	"rave.page/mate/internal/session"
)

const (
	confidence   = 0.8                     // metadata-accurate, slightly delayed (file poll)
	pollInterval = 1500 * time.Millisecond // newest-session mtime poll cadence
)

// Source streams Serato History now-playing into the merger.
type Source struct {
	log        *logbus.Bus
	dir        string
	nowPlaying bool

	lastFile string    // last polled session path
	lastMod  time.Time // last polled session mtime
	lastKey  string    // identity of the last emitted track (for the Loaded boundary)
}

// New builds the source; seratoDir "" resolves to serato.DefaultDir().
func New(log *logbus.Bus, seratoDir string, nowPlaying bool) *Source {
	if seratoDir == "" {
		if d, err := serato.DefaultDir(); err == nil {
			seratoDir = d
		}
	}
	return &Source{log: log, dir: seratoDir, nowPlaying: nowPlaying}
}

// ID implements session.Source.
func (s *Source) ID() string { return session.SourceSerato }

// Capabilities implements session.Source: deck-or-master metadata (no live mixer state).
func (s *Source) Capabilities() []session.Capability {
	fields := []string{
		session.FieldTitle, session.FieldArtist, session.FieldAlbum,
		session.FieldGenre, session.FieldBPM, session.FieldKey, session.FieldPath,
	}
	return []session.Capability{
		{Scope: session.ScopeDeck, IDs: []string{"A", "B", "C", "D"}, Fields: fields},
		{Scope: session.ScopeMaster, Fields: fields},
	}
}

// Start polls the newest History session until ctx is cancelled (no-op if now-playing off).
func (s *Source) Start(ctx context.Context, emit func(session.Observation)) error {
	if !s.nowPlaying {
		<-ctx.Done()
		return nil
	}
	t := time.NewTicker(pollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			s.poll(emit)
		}
	}
}

// poll re-reads the newest session when its file changed and emits the latest played entry.
func (s *Source) poll(emit func(session.Observation)) {
	path, mod, err := serato.NewestSession(s.dir)
	if err != nil {
		return // no sessions yet - silent; Serato may not be running
	}
	if path == s.lastFile && !mod.After(s.lastMod) {
		return
	}
	s.lastFile, s.lastMod = path, mod

	f, err := os.Open(path)
	if err != nil {
		s.log.Debug(session.SourceSerato, "open session failed", map[string]any{"path": path, "err": err.Error()})
		return
	}
	tracks, err := serato.ParseSession(f)
	_ = f.Close()
	if err != nil {
		s.log.Warn(session.SourceSerato, "parse session failed", map[string]any{"path": path, "err": err.Error()})
		return
	}
	latest := latestPlayed(tracks)
	if latest == nil {
		return
	}
	key := latest.Path + "|" + latest.Artist + " - " + latest.Title
	loaded := key != s.lastKey
	s.lastKey = key
	emit(observation(*latest, loaded))
}

// latestPlayed returns the last actually-played entry, else the last entry (played flag
// is unreliable across Serato versions - fall back so now-playing still surfaces).
func latestPlayed(tracks []serato.Track) *serato.Track {
	for i := len(tracks) - 1; i >= 0; i-- {
		if tracks[i].Played {
			return &tracks[i]
		}
	}
	if len(tracks) > 0 {
		return &tracks[len(tracks)-1]
	}
	return nil
}

// observation builds a deck (or master) Observation from a session track.
func observation(t serato.Track, loaded bool) session.Observation {
	fields := map[string]any{}
	if t.Title != "" {
		fields[session.FieldTitle] = t.Title
	}
	if t.Artist != "" {
		fields[session.FieldArtist] = t.Artist
	}
	if t.Album != "" {
		fields[session.FieldAlbum] = t.Album
	}
	if t.Genre != "" {
		fields[session.FieldGenre] = t.Genre
	}
	if t.Key != "" {
		fields[session.FieldKey] = t.Key
	}
	if t.BPM > 0 {
		fields[session.FieldBPM] = t.BPM
	}
	if t.Path != "" {
		fields[session.FieldPath] = t.Path
	}
	scope := session.Scope{Kind: session.ScopeMaster}
	if t.Deck > 0 {
		scope = session.Scope{Kind: session.ScopeDeck, ID: deckID(t.Deck)}
	}
	return session.Observation{
		Source:     session.SourceSerato,
		Scope:      scope,
		Fields:     fields,
		Confidence: confidence,
		Loaded:     loaded,
	}
}

// deckID maps a deck number to a deck letter (1→A…26→Z), falling back to the number.
func deckID(deck int) string {
	if deck >= 1 && deck <= 26 {
		return string(rune('A' + deck - 1))
	}
	return strconv.Itoa(deck)
}

// LoadLibrary loads the Serato collection (database V2 + crates) for libsync wiring.
func LoadLibrary(seratoDir string) ([]serato.Track, error) {
	if seratoDir == "" {
		d, err := serato.DefaultDir()
		if err != nil {
			return nil, err
		}
		seratoDir = d
	}
	tracks, _, err := serato.LoadCollection(seratoDir)
	return tracks, err
}

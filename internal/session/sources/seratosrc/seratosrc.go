// Package seratosrc adapts the Serato decoder (internal/serato) into a session Source.
// It loads the collection (database V2 + crates) for libsync and, when now-playing is
// enabled, emits the CURRENT track on EACH deck to the merger. Now-playing is read
// SQLite-first, binary-fallback: Serato 4.x writes live history to master.sqlite (V4 path),
// so each poll reads it first; Serato ≤3.x wrote binary History\Sessions\*.session files, the
// fallback when no V4 DB exists. Either way Serato logs a per-deck entry per played track; the
// live track on a deck is its latest entry with no endtime. Observations target a deck scope
// (1→A …) with isPlaying set, so concurrent decks surface independently (else master when
// Serato writes no deck number). Delayed (poll) but metadata-rich; no mixer/fader state.
package seratosrc

import (
	"context"
	"os"
	"path/filepath"
	"sort"
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

	lastFile string            // last polled binary session path
	lastMod  time.Time         // last polled binary session mtime
	lastKey  map[string]string // per-scope (deck/master) last-emitted track key (Loaded boundary)

	v4Path      string    // resolved Serato 4 master.sqlite (cached; "" = none)
	v4Resolved  bool      // v4Path resolution done
	lastV4Mod   time.Time // last read master.sqlite mtime (change-detect gate)
	lastV4File  string    // last read master.sqlite path
	v4ErrLogged bool      // V4 read error already logged (throttle)
}

// New builds the source; seratoDir "" resolves to serato.DefaultDir().
func New(log *logbus.Bus, seratoDir string, nowPlaying bool) *Source {
	if seratoDir == "" {
		if d, err := serato.DefaultDir(); err == nil {
			seratoDir = d
		}
	}
	return &Source{log: log, dir: seratoDir, nowPlaying: nowPlaying, lastKey: map[string]string{}}
}

// ID implements session.Source.
func (s *Source) ID() string { return session.SourceSerato }

// Capabilities implements session.Source: deck-or-master metadata + play state (no mixer/fader).
func (s *Source) Capabilities() []session.Capability {
	fields := []string{
		session.FieldTitle, session.FieldArtist, session.FieldAlbum,
		session.FieldGenre, session.FieldBPM, session.FieldKey, session.FieldPath,
		session.FieldIsPlaying,
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

// poll reads current now-playing: Serato 4 SQLite first, then the binary session fallback.
func (s *Source) poll(emit func(session.Observation)) {
	if s.pollV4(emit) {
		return // V4 DB present: authoritative for Serato 4 (binary .session isn't written)
	}
	// Binary fallback (Serato ≤3.x): re-read the newest session when its file changed.
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
	s.emitByDeck(emit, tracks)
}

// pollV4 reads the Serato 4 master.sqlite (change-detected by mtime) and emits per-deck. Returns
// true when a V4 DB exists (poll fully handled - don't read the binary format, which Serato 4
// doesn't write), false when none is found (caller falls back to the binary session). A read
// error is logged once and still returns true: a transient lock resolves on the next poll (the
// mtime gate only advances on success), and there's no valid binary source to fall to on V4.
func (s *Source) pollV4(emit func(session.Observation)) bool {
	path := s.v4DBPath()
	if !serato.HasV4DB(path) {
		return false
	}
	fi, err := os.Stat(path)
	if err != nil {
		return false // vanished between HasV4DB and Stat: let the binary path try
	}
	mod := fi.ModTime()
	if path == s.lastV4File && !mod.After(s.lastV4Mod) {
		return true // unchanged since last poll
	}
	tracks, err := serato.ReadLatestV4Session(path)
	if err != nil {
		if !s.v4ErrLogged {
			s.log.Debug(session.SourceSerato, "read serato 4 db failed", map[string]any{"path": path, "err": err.Error()})
			s.v4ErrLogged = true
		}
		return true // present but unreadable this tick; retry next poll (gate not advanced)
	}
	s.v4ErrLogged = false
	s.lastV4File, s.lastV4Mod = path, mod
	s.emitByDeck(emit, tracks)
	return true
}

// v4DBPath resolves + caches the Serato 4 master.sqlite path: <dir>\Library\master.sqlite when
// the configured Serato dir points at the library root, else the per-OS default. "" when none.
func (s *Source) v4DBPath() string {
	if s.v4Resolved {
		return s.v4Path
	}
	s.v4Resolved = true
	if s.dir != "" {
		if cand := filepath.Join(s.dir, "Library", "master.sqlite"); serato.HasV4DB(cand) {
			s.v4Path = cand
			return s.v4Path
		}
	}
	if p, err := serato.DefaultV4DBPath(); err == nil {
		s.v4Path = p
	}
	return s.v4Path
}

// emitByDeck emits one deck-scoped observation per active deck (concurrent decks surface
// independently), or a single master observation when no deck numbers are present. Serato logs
// a per-deck entry per played track; the live track on a deck is its latest entry, playing iff
// endtime is unset.
func (s *Source) emitByDeck(emit func(session.Observation), tracks []serato.Track) {
	byDeck := currentByDeck(tracks)
	if len(byDeck) > 0 {
		decks := make([]int, 0, len(byDeck))
		for d := range byDeck {
			decks = append(decks, d)
		}
		sort.Ints(decks) // deterministic emit order
		for _, d := range decks {
			s.emitTrack(emit, session.Scope{Kind: session.ScopeDeck, ID: deckID(d)}, byDeck[d])
		}
		return
	}
	// No deck numbers (older/single-deck Serato): single master now-playing.
	if latest := latestEntry(tracks); latest != nil {
		s.emitTrack(emit, session.Scope{Kind: session.ScopeMaster}, *latest)
	}
}

// emitTrack builds + emits an observation for one scope, keying the Loaded boundary on a
// per-scope track identity so a track change on one deck doesn't reset the other.
func (s *Source) emitTrack(emit func(session.Observation), scope session.Scope, t serato.Track) {
	key := t.Path + "|" + t.Artist + " - " + t.Title
	sk := scope.Key()
	loaded := key != s.lastKey[sk]
	s.lastKey[sk] = key
	emit(observation(scope, t, loaded))
}

// currentByDeck returns, per deck number, that deck's latest history entry (last in file
// order). Entries with no title/path are skipped. Playing state is derived downstream from
// EndedAt. Deck 0 (no deck number) is excluded - handled by the master fallback.
func currentByDeck(tracks []serato.Track) map[int]serato.Track {
	cur := map[int]serato.Track{}
	for _, t := range tracks {
		if t.Deck <= 0 || (t.Title == "" && t.Path == "") {
			continue
		}
		cur[t.Deck] = t // last entry for the deck wins
	}
	return cur
}

// latestEntry returns the last entry carrying a title/path (deck-less fallback), else nil.
func latestEntry(tracks []serato.Track) *serato.Track {
	for i := len(tracks) - 1; i >= 0; i-- {
		if tracks[i].Title != "" || tracks[i].Path != "" {
			return &tracks[i]
		}
	}
	return nil
}

// observation builds a scoped Observation from a session track. isPlaying = the track is
// still on the deck (no endtime); once Serato writes an endtime the deck is idle.
func observation(scope session.Scope, t serato.Track, loaded bool) session.Observation {
	fields := map[string]any{session.FieldIsPlaying: t.EndedAt == 0}
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

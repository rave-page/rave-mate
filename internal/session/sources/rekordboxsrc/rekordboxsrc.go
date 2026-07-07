// Package rekordboxsrc is a session Source for Rekordbox LIVE now-playing. Collection
// read/write lives in internal/rekordboxdb + internal/musiclib; this package adds only the
// live feed, via up to two backends emitted under distinct provenance Source IDs so the
// merger can rank them:
//
//   - master.db history poll (session.SourceRekordboxDB, conf 0.6) - SAFE baseline. Polls
//     djmdSongHistory's newest play. Rekordbox marks a track "played" ~1min in, so this is
//     RECENTLY-PLAYED with ~60s lag (not real-time). Works on every OS, Rekordbox running.
//   - process-memory read (session.SourceRekordboxMem, conf 0.9) - Windows-only, real-time
//     per-deck BPM/play/title/artist via per-version memory offsets (rkbx_link technique).
//     Fragile: offsets must be seeded from a real build and break on Rekordbox updates.
//
// The Pioneer-hardware LAN path stays in session/sources/prodjlinksrc; NewResolver here lets
// that source show track text by resolving a rekordbox track id → metadata from master.db.
package rekordboxsrc

import (
	"context"
	"sync"

	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/session"
)

const (
	logSource = "rekordboxsrc"

	confDB  = 0.6 // recently-played, ~60s lag
	confMem = 0.9 // real-time process read
)

// Config selects + parameterizes the backends.
type Config struct {
	DBPath     string // "" = auto-detect master.db (rekordboxdb.DiscoverRekordboxMasterDB)
	DBKey      string // "" = RAVE_REKORDBOX_KEY env / key file / built-in default
	DBPoll     bool   // enable the master.db history poll (safe, ~60s lag)
	MemoryRead bool   // enable the process-memory read (real-time, Windows-only, fragile)
}

// Source streams Rekordbox live now-playing into the merger.
type Source struct {
	log *logbus.Bus
	cfg Config
}

// New builds the source.
func New(log *logbus.Bus, cfg Config) *Source { return &Source{log: log, cfg: cfg} }

// ID implements session.Source (liveness key shared by both backends).
func (s *Source) ID() string { return session.SourceRekordbox }

// Capabilities implements session.Source: DB poll → master title/artist/bpm/key (+path);
// memory read → per-deck bpm/isPlaying/title/artist.
func (s *Source) Capabilities() []session.Capability {
	var caps []session.Capability
	if s.cfg.DBPoll {
		caps = append(caps, session.Capability{Scope: session.ScopeMaster, Fields: []string{
			session.FieldTitle, session.FieldArtist, session.FieldBPM, session.FieldKey, session.FieldPath,
		}})
	}
	if s.cfg.MemoryRead {
		caps = append(caps, session.Capability{Scope: session.ScopeDeck, IDs: []string{"A", "B", "C", "D"}, Fields: []string{
			session.FieldBPM, session.FieldIsPlaying, session.FieldTitle, session.FieldArtist,
		}})
	}
	return caps
}

// Start launches each enabled backend in a guarded goroutine and returns when ctx is done.
func (s *Source) Start(ctx context.Context, emit func(session.Observation)) error {
	if !s.cfg.DBPoll && !s.cfg.MemoryRead {
		s.log.Warn(logSource, "rekordbox source enabled with no backend (set dbPoll and/or memoryRead)", nil)
		<-ctx.Done()
		return nil
	}
	if s.cfg.DBPoll {
		debuglog.Go(s.log, logSource, func() { s.runDBPoll(ctx, emit) })
	}
	if s.cfg.MemoryRead {
		debuglog.Go(s.log, logSource, func() { s.runMemory(ctx, emit) })
	}
	<-ctx.Done()
	return nil
}

// onceLog logs a message only when its state changes (error→recover→error) so a persistently
// locked/contended DB or a failing memory read logs once, not every tick.
type onceLog struct {
	mu   sync.Mutex
	last string
}

// changed reports whether s differs from the last seen state (and records it).
func (o *onceLog) changed(s string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.last == s {
		return false
	}
	o.last = s
	return true
}

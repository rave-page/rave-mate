// Package mixxxsrc is the Mixxx session Source. It polls Mixxx's library DB (mixxxdb.sqlite)
// set-log/history playlists for the newest played track and emits MASTER-scope now-playing.
// Mixxx's DB carries NO per-deck state, so there is deliberately no deck scope here - master only
// (honest; not faked). Delayed (a ~3s DB poll), so it ranks at the low DB-poll confidence tier.
// On the FIRST successful read it SEEDS the last-track identity WITHOUT emitting - the newest
// set-log entry at startup is a PAST session, not fresh now-playing - and emits only when the
// track identity CHANGES thereafter (never re-asserts a stale track as fresh).
package mixxxsrc

import (
	"context"
	"os"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/mixxx"
	"rave.page/mate/internal/session"
)

const (
	logSource    = "mixxx"
	confidence   = 0.5 // delayed DB poll (virtualdj.history / rekordbox.db tier)
	pollInterval = 3 * time.Second
)

// Source streams Mixxx now-playing (master scope) into the merger.
type Source struct {
	log    *logbus.Bus
	dbPath string // config override; "" = mixxx.DefaultDBPath()

	resolved bool      // db-path resolution attempted (cached)
	path     string    // resolved db path ("" = none)
	seeded   bool      // first successful read seen (seed-not-emit boundary)
	lastKey  string    // last-seen track identity (Loaded/change boundary)
	lastMod  time.Time // last-read db mtime (change-detect gate)
}

// New builds the source. dbPath "" auto-detects mixxxdb.sqlite at Start.
func New(log *logbus.Bus, dbPath string) *Source { return &Source{log: log, dbPath: dbPath} }

// ID implements session.Source.
func (s *Source) ID() string { return session.SourceMixxx }

// Capabilities implements session.Source: master-scope metadata + play state only (Mixxx's DB
// exposes no per-deck data).
func (s *Source) Capabilities() []session.Capability {
	return []session.Capability{{Scope: session.ScopeMaster, Fields: []string{
		session.FieldTitle, session.FieldArtist, session.FieldAlbum, session.FieldGenre,
		session.FieldBPM, session.FieldKey, session.FieldPath, session.FieldIsPlaying,
	}}}
}

// Start polls the Mixxx DB until ctx is cancelled.
func (s *Source) Start(ctx context.Context, emit func(session.Observation)) error {
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

// poll resolves the DB, reads the newest play-event track when the DB mtime advanced, and emits a
// master observation on a track change. The first successful read only SEEDS lastKey so a stale
// past-session track is never asserted as fresh now-playing at startup.
func (s *Source) poll(emit func(session.Observation)) {
	path := s.resolvePath()
	if path == "" || !mixxx.HasDB(path) {
		return
	}
	fi, err := os.Stat(path)
	if err != nil {
		return
	}
	mod := fi.ModTime()
	if s.seeded && !mod.After(s.lastMod) {
		return // unchanged since last successful read
	}

	np, ok, rerr := mixxx.ReadNowPlaying(path)
	if rerr != nil {
		s.log.Debug(logSource, "read mixxxdb failed", map[string]any{"path": path, "err": rerr.Error()})
		return // retry next tick (mtime gate not advanced)
	}
	s.lastMod = mod
	if !ok {
		return
	}

	key := np.Artist + "\x00" + np.Title + "\x00" + np.Path
	if !s.seeded {
		s.seeded = true
		s.lastKey = key
		return // seed only: don't announce a past-session track as fresh now-playing
	}
	if key == s.lastKey {
		return
	}
	s.lastKey = key
	emit(observation(np))
	s.log.Info(logSource, "now playing", map[string]any{"artist": np.Artist, "title": np.Title})
}

// resolvePath resolves + caches the DB path: the config override, else mixxx.DefaultDBPath().
func (s *Source) resolvePath() string {
	if s.resolved {
		return s.path
	}
	s.resolved = true
	if s.dbPath != "" {
		s.path = s.dbPath
		return s.path
	}
	if p, err := mixxx.DefaultDBPath(); err == nil {
		s.path = p
	} else {
		s.log.Debug(logSource, "no mixxxdb.sqlite default path", map[string]any{"err": err.Error()})
	}
	return s.path
}

// observation builds the master-scope Observation for a Mixxx play. isPlaying=true: a fresh
// set-log entry means the track just started on Mixxx's (single) master output. Loaded=true: a
// track change is a new-now-playing boundary.
func observation(np *mixxx.NowPlaying) session.Observation {
	fields := map[string]any{session.FieldIsPlaying: true}
	if np.Title != "" {
		fields[session.FieldTitle] = np.Title
	}
	if np.Artist != "" {
		fields[session.FieldArtist] = np.Artist
	}
	if np.Album != "" {
		fields[session.FieldAlbum] = np.Album
	}
	if np.Genre != "" {
		fields[session.FieldGenre] = np.Genre
	}
	if np.Key != "" {
		fields[session.FieldKey] = np.Key
	}
	if np.Path != "" {
		fields[session.FieldPath] = np.Path
	}
	if np.BPM > 0 {
		fields[session.FieldBPM] = np.BPM
	}
	return session.Observation{
		Source:     session.SourceMixxx,
		Scope:      session.Scope{Kind: session.ScopeMaster},
		Fields:     fields,
		Confidence: confidence,
		Loaded:     true,
	}
}

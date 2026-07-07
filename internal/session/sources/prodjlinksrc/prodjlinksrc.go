// Package prodjlinksrc adapts the passive Pro DJ Link listener (internal/prodjlink) into a
// session Source. Each CDJ/XDJ player maps to a deck (player 1→A …); the listener's live
// BPM + play state feed the merger. Track title/artist aren't in the wire protocol - an
// optional resolver maps the rekordbox track id to metadata from an imported library.
package prodjlinksrc

import (
	"context"
	"strconv"
	"sync"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/prodjlink"
	"rave.page/mate/internal/session"
)

const confidence = 0.85 // live + reliable, just under Traktor's direct feed

// Resolver maps a rekordbox track id to display metadata (from an imported Rekordbox library).
type Resolver func(trackID uint32) (title, artist, key string, ok bool)

// Source streams Pro DJ Link now-playing into the merger.
type Source struct {
	log *logbus.Bus

	mu      sync.RWMutex
	resolve Resolver
	last    map[int]uint32 // player → last seen track id (for the Loaded boundary)
}

// New builds the source.
func New(log *logbus.Bus) *Source {
	return &Source{log: log, last: map[int]uint32{}}
}

// SetResolver installs (or clears) the rekordbox-id → metadata resolver.
func (s *Source) SetResolver(r Resolver) {
	s.mu.Lock()
	s.resolve = r
	s.mu.Unlock()
}

// ID implements session.Source.
func (s *Source) ID() string { return session.SourceProDJLink }

// Capabilities implements session.Source.
func (s *Source) Capabilities() []session.Capability {
	return []session.Capability{
		{Scope: session.ScopeDeck, IDs: []string{"A", "B", "C", "D"}, Fields: []string{
			session.FieldBPM, session.FieldIsPlaying, session.FieldTitle, session.FieldArtist, session.FieldKey,
		}},
	}
}

// Start runs the listener until ctx is cancelled, emitting a deck Observation per status packet.
func (s *Source) Start(ctx context.Context, emit func(session.Observation)) error {
	return prodjlink.Listen(ctx, func(st prodjlink.Status) {
		if st.Player < 1 {
			return
		}
		fields := map[string]any{session.FieldIsPlaying: st.Playing}
		if st.EffectiveBPM > 0 {
			fields[session.FieldBPM] = st.EffectiveBPM
		}

		s.mu.Lock()
		prev, seen := s.last[st.Player]
		loaded := !seen || prev != st.TrackID
		s.last[st.Player] = st.TrackID
		resolve := s.resolve
		s.mu.Unlock()

		if resolve != nil && st.TrackID != 0 && st.Type == prodjlink.TrackRekordbox {
			if title, artist, key, ok := resolve(st.TrackID); ok {
				if title != "" {
					fields[session.FieldTitle] = title
				}
				if artist != "" {
					fields[session.FieldArtist] = artist
				}
				if key != "" {
					fields[session.FieldKey] = key
				}
			}
		}

		emit(session.Observation{
			Source:     session.SourceProDJLink,
			Scope:      session.Scope{Kind: session.ScopeDeck, ID: deckID(st.Player)},
			Fields:     fields,
			Confidence: confidence,
			Loaded:     loaded,
		})
	})
}

// deckID maps a player number to a deck letter (1→A…4→D), falling back to the number.
func deckID(player int) string {
	if player >= 1 && player <= 26 {
		return string(rune('A' + player - 1))
	}
	return strconv.Itoa(player)
}

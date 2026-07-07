// Package traktorsrc adapts the Traktor HTTP listener (internal/traktor) into a session
// Source. Most of Traktor's POSTed state keys already match the canonical field vocabulary;
// a few differ and are renamed (traktorRename). deck.loaded becomes a Loaded boundary so the
// merger does a full replacement (parity with the web overlay).
package traktorsrc

import (
	"context"

	"rave.page/mate/internal/session"
	"rave.page/mate/internal/traktor"
)

// confidence is high: the HTTP feed (QML mod / broadcast) is reliable when present.
const confidence = 0.9

// traktorRename maps Traktor HTTP payload keys that differ from the canonical vocabulary:
//   - filePath: the loaded track's local path (deckLoaded) → drives cover art + lookups.
//   - onAirLevel: the post-fader/crossfader audible level (updateChannel, 0..1) → our deck
//     "fader"/level meter. Traktor's HTTP feed sends this, not a raw channel fader/EQ.
var traktorRename = map[string]string{
	"filePath":   session.FieldPath,
	"onAirLevel": session.FieldFader,
}

// Source bridges a *traktor.Server's event bus into the merger.
type Source struct {
	srv *traktor.Server
}

// New wraps a Traktor listener.
func New(srv *traktor.Server) *Source { return &Source{srv: srv} }

// ID implements session.Source.
func (s *Source) ID() string { return session.SourceTraktor }

// Capabilities implements session.Source.
func (s *Source) Capabilities() []session.Capability {
	return []session.Capability{
		{Scope: session.ScopeDeck, IDs: []string{"A", "B", "C", "D"}, Fields: []string{
			session.FieldTitle, session.FieldArtist, session.FieldAlbum, session.FieldGenre,
			session.FieldBPM, session.FieldKey, session.FieldIsPlaying, session.FieldElapsedTime,
			session.FieldTrackLength, session.FieldDeckType, session.FieldLoadedAt, session.FieldPath,
		}},
		{Scope: session.ScopeChannel, IDs: []string{"1", "2", "3", "4"}, Fields: []string{
			session.FieldFader, session.FieldEQHigh, session.FieldEQMid, session.FieldEQLow,
			session.FieldFilter, session.FieldCue,
		}},
		{Scope: session.ScopeMaster, Fields: []string{session.FieldBPM, session.FieldPhase}},
	}
}

// Start seeds the merger from the current snapshot, then streams future events until ctx
// is cancelled.
func (s *Source) Start(ctx context.Context, emit func(session.Observation)) error {
	snap := s.srv.Snapshot()
	for deck, st := range snap.Decks {
		if len(st) > 0 {
			emit(observation(session.Scope{Kind: session.ScopeDeck, ID: deck}, st, false))
		}
	}
	for ch, st := range snap.Channels {
		if len(st) > 0 {
			emit(observation(session.Scope{Kind: session.ScopeChannel, ID: ch}, st, false))
		}
	}
	if len(snap.Master) > 0 {
		emit(observation(session.Scope{Kind: session.ScopeMaster}, snap.Master, false))
	}

	events, unsub := s.srv.Subscribe()
	defer unsub()
	for {
		select {
		case <-ctx.Done():
			return nil
		case e, ok := <-events:
			if !ok {
				return nil
			}
			emit(translate(e))
		}
	}
}

// translate maps a Traktor event to an Observation.
func translate(e traktor.Event) session.Observation {
	var scope session.Scope
	switch e.Type {
	case traktor.DeckLoaded, traktor.DeckUpdate:
		scope = session.Scope{Kind: session.ScopeDeck, ID: e.Deck}
	case traktor.ChannelUpdate:
		scope = session.Scope{Kind: session.ScopeChannel, ID: e.Channel}
	case traktor.MasterClock:
		scope = session.Scope{Kind: session.ScopeMaster}
	}
	return observation(scope, e.State, e.Type == traktor.DeckLoaded)
}

func observation(scope session.Scope, state map[string]any, loaded bool) session.Observation {
	return session.Observation{
		Source:     session.SourceTraktor,
		Scope:      scope,
		Fields:     normalize(state),
		Confidence: confidence,
		Loaded:     loaded,
	}
}

// normalize renames Traktor-specific payload keys to the canonical vocabulary. Returns the
// input unchanged (no allocation) when no aliased key is present.
func normalize(state map[string]any) map[string]any {
	need := false
	for alias := range traktorRename {
		if _, ok := state[alias]; ok {
			need = true
			break
		}
	}
	if !need {
		return state
	}
	out := make(map[string]any, len(state))
	for k, v := range state {
		if canon, ok := traktorRename[k]; ok {
			out[canon] = v
		} else {
			out[k] = v
		}
	}
	return out
}

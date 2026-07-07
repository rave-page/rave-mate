// Package qmlsrc is a planned source: the richest Traktor feed, from a QML modification that
// POSTs full per-deck/channel/master JSON at sub-second rate. Opt-in and risky - it edits
// files inside the signed Traktor bundle and must be re-derived per Traktor version (the
// failure mode is a corrupted Traktor install), so it's a power-user tier-3 source, never a
// default. Stub for now - declares its (broad) capability for the Session tab; Start is
// inert. When implemented it will likely reuse the internal/traktor HTTP listener but emit
// under the qml source ID (top merge priority).
package qmlsrc

import (
	"context"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/session"
)

// Source is the (not-yet-implemented) QML HTTP source.
type Source struct{ log *logbus.Bus }

// New constructs the stub.
func New(log *logbus.Bus) *Source { return &Source{log: log} }

// ID implements session.Source.
func (s *Source) ID() string { return session.SourceQML }

// Capabilities implements session.Source: full per-deck + channel + master state.
func (s *Source) Capabilities() []session.Capability {
	return []session.Capability{
		{Scope: session.ScopeDeck, IDs: []string{"A", "B", "C", "D"}, Fields: []string{
			session.FieldTitle, session.FieldArtist, session.FieldAlbum, session.FieldGenre,
			session.FieldBPM, session.FieldKey, session.FieldIsPlaying, session.FieldElapsedTime,
			session.FieldTrackLength, session.FieldDeckType,
		}},
		{Scope: session.ScopeChannel, IDs: []string{"1", "2", "3", "4"}, Fields: []string{
			session.FieldFader, session.FieldEQHigh, session.FieldEQMid, session.FieldEQLow, session.FieldFilter,
		}},
		{Scope: session.ScopeMaster, Fields: []string{session.FieldBPM, session.FieldPhase}},
	}
}

// Start is inert until the QML bridge is implemented (see package doc).
func (s *Source) Start(ctx context.Context, _ func(session.Observation)) error {
	s.log.Info("qml", "source not yet implemented (planned, opt-in tier-3)", nil)
	<-ctx.Done()
	return nil
}

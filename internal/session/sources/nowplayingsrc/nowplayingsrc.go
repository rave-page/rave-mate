// Package nowplayingsrc is a planned macOS-only source: reads the system "Now Playing"
// info (the feed behind the media keys / Control Center) for the master mix title/artist
// via the MediaRemote private framework. macOS + master-only. Stub for now - declares its
// capability for the Session tab; Start is inert. A darwin build-tagged implementation
// lands later; on other platforms it stays a no-op.
package nowplayingsrc

import (
	"context"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/session"
)

// Source is the (not-yet-implemented) macOS Now Playing source.
type Source struct{ log *logbus.Bus }

// New constructs the stub.
func New(log *logbus.Bus) *Source { return &Source{log: log} }

// ID implements session.Source.
func (s *Source) ID() string { return session.SourceNowPlaying }

// Capabilities implements session.Source: master mix title/artist (macOS).
func (s *Source) Capabilities() []session.Capability {
	return []session.Capability{{Scope: session.ScopeMaster, Fields: []string{session.FieldTitle, session.FieldArtist}}}
}

// Start is inert until the macOS implementation lands (see package doc).
func (s *Source) Start(ctx context.Context, _ func(session.Observation)) error {
	s.log.Info("nowplaying", "source not yet implemented (planned, macOS)", nil)
	<-ctx.Done()
	return nil
}

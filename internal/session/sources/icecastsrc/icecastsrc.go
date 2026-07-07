// Package icecastsrc adapts the local Icecast-source receiver (internal/icecast) into a
// session Source: it turns the broadcast's now-playing metadata (in-band Ogg Vorbis
// comments / the /admin/metadata side channel) into master-mix title/artist observations.
// The receiver itself captures the set audio to disk; this source only surfaces its
// metadata feed to the merger. Delayed + master-only, so it ranks below the live decks.
package icecastsrc

import (
	"context"

	"rave.page/mate/internal/icecast"
	"rave.page/mate/internal/session"
)

// confidence is low: the broadcast feed is master-only and ~seconds delayed behind the decks.
const confidence = 0.4

// Source bridges an *icecast.Receiver's metadata feed into the merger.
type Source struct {
	rcv *icecast.Receiver
}

// New wraps an Icecast receiver.
func New(rcv *icecast.Receiver) *Source { return &Source{rcv: rcv} }

// ID implements session.Source.
func (s *Source) ID() string { return session.SourceIcecast }

// Capabilities implements session.Source: master mix title/artist.
func (s *Source) Capabilities() []session.Capability {
	return []session.Capability{{Scope: session.ScopeMaster, Fields: []string{session.FieldTitle, session.FieldArtist}}}
}

// Start seeds from the current snapshot then streams metadata updates until ctx is cancelled.
func (s *Source) Start(ctx context.Context, emit func(session.Observation)) error {
	if st := s.rcv.Snapshot(); st.Title != "" || st.Artist != "" {
		emit(observation(icecast.Meta{Title: st.Title, Artist: st.Artist}))
	}
	ch, unsub := s.rcv.SubscribeMeta()
	defer unsub()
	for {
		select {
		case <-ctx.Done():
			return nil
		case m, ok := <-ch:
			if !ok {
				return nil
			}
			emit(observation(m))
		}
	}
}

func observation(m icecast.Meta) session.Observation {
	fields := map[string]any{}
	if m.Title != "" {
		fields[session.FieldTitle] = m.Title
	}
	if m.Artist != "" {
		fields[session.FieldArtist] = m.Artist
	}
	return session.Observation{
		Source:     session.SourceIcecast,
		Scope:      session.Scope{Kind: session.ScopeMaster},
		Fields:     fields,
		Confidence: confidence,
	}
}

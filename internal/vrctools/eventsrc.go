package vrctools

import (
	"context"
	"sync"
	"time"

	"rave.page/mate/internal/api"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/vrcphotos"
)

// apiEventSource is a vrcphotos.EventSource backed by the rave.page events API (GET /events,
// EventOut.starts_at/ends_at). Events are cached + refreshed on a TTL; EventAt matches a photo's
// capture time against each event window. Best-effort: a fetch error keeps the last cache (or none
// → no matches → organizing falls back to the world timeline).
type apiEventSource struct {
	api   *api.Client
	token func() string // access token provider; "" = anonymous (public events)
	log   *logbus.Bus
	ttl   time.Duration

	mu      sync.Mutex
	events  []vrcphotos.Event
	fetched time.Time
}

// NewAPIEventSource builds an event source over the API client + access-token provider.
func NewAPIEventSource(c *api.Client, token func() string, log *logbus.Bus) vrcphotos.EventSource {
	return &apiEventSource{api: c, token: token, log: log, ttl: 5 * time.Minute}
}

// EventAt returns the event whose window contains t (inclusive), ok=false if none.
func (s *apiEventSource) EventAt(t time.Time) (vrcphotos.Event, bool) {
	for _, e := range s.snapshot() {
		if !t.Before(e.Start) && !t.After(e.End) {
			return e, true
		}
	}
	return vrcphotos.Event{}, false
}

// snapshot returns the cached events, refreshing once past the TTL (failures back off a full TTL,
// so a down API isn't hammered).
func (s *apiEventSource) snapshot() []vrcphotos.Event {
	s.mu.Lock()
	stale := time.Since(s.fetched) >= s.ttl
	ev := s.events
	s.mu.Unlock()
	if !stale {
		return ev
	}
	return s.refresh()
}

func (s *apiEventSource) refresh() []vrcphotos.Event {
	if s.api == nil {
		return nil
	}
	tok := ""
	if s.token != nil {
		tok = s.token()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	evs, err := s.api.ListEvents(ctx, tok, "", "", 200)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.fetched = time.Now()
	if err != nil {
		if s.log != nil {
			s.log.Warn(logTag, "event fetch failed: "+err.Error(), nil)
		}
		return s.events // keep last good cache
	}
	out := make([]vrcphotos.Event, 0, len(evs))
	for _, e := range evs {
		out = append(out, vrcphotos.Event{ID: e.ID, Name: e.Title, Start: e.Start, End: e.End})
	}
	s.events = out
	return out
}

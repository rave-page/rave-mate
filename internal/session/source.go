package session

import "context"

// Source is one connection method to DJ software or a controller. Start runs until ctx is
// cancelled, pushing normalized Observations via emit (non-blocking; the merger fan-out
// drops on overflow). Capabilities self-describe which fields the source can supply so the
// UI can show coverage gaps ("your setup gives A/B titles but not C/D - enable X").
type Source interface {
	ID() string
	Capabilities() []Capability
	Start(ctx context.Context, emit func(Observation)) error
}

// Sink consumes the merged session. The aggregator subscribes each Sink to the Merger and
// runs it until ctx is cancelled. (The stream publisher is driven separately by the UI.)
type Sink interface {
	ID() string
	Start(ctx context.Context, m *Merger) error
}

package medialink

import "context"

// MediaControl is the daemon/UI-facing surface of the media route plane - satisfied by the
// in-proc *RouteManager and by a subprocess proxy. Frame-bearing internals (RegisterSource/Sink,
// OfferRoute, CloseRoute, RemoteAdverts) are deliberately NOT here: their func callbacks cannot
// cross a process boundary, so they stay in-proc inside the media child (called only by
// mediaroute/webcam, which live in the same child).
type MediaControl interface {
	Start(ctx context.Context) error
	Stop()
	Advertise()
	SetCodecCaps(encoders, decoders []string)
	SetSyncPeer(nodeID string)
	Encoders() []string
	Stats() []RouteStat
	SyncStats() []SyncStat
	ClockQuality() ClockQuality
}

var _ MediaControl = (*RouteManager)(nil)

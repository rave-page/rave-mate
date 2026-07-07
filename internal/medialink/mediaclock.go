package medialink

import (
	"sync/atomic"
	"time"
)

// ClockSource yields the shared media-clock time in nanoseconds - the PTS domain every frame is
// stamped on (§2.3). Selection is detect-then-configure PTP > software-sync > follow-master; P1
// ships ONLY the monotonic tier (raw local clock, no cross-node discipline). P2 implements the
// software-sync tier and P8 the PTP + follow-master tiers, all BEHIND this seam - the transport +
// timecode plane consume ClockSource.Now() and never learn which tier is live.
type ClockSource interface {
	Now() int64 // media-clock nanoseconds
	Quality() ClockQuality
}

// ClockTier names the active clock discipline (telemetry, §7).
type ClockTier string

const (
	TierMonotonic    ClockTier = "monotonic"     // P1: raw local monotonic, offset 0, no cross-node sync
	TierSoftware     ClockTier = "software"      // P2: NTP-style pairwise offset slew (RFC 5905 on-wire)
	TierPTP          ClockTier = "ptp"           // P8: IEEE 1588 disciplined clock (read, never implemented)
	TierFollowMaster ClockTier = "follow-master" // P8: slave to master audio-device clock
)

// ClockQuality is the sync-state telemetry that gates the timecode plane (§2.3): TC slaves
// freewheel (holdover) when Locked drops.
type ClockQuality struct {
	Tier     ClockTier
	Locked   bool
	OffsetNs int64 // applied offset vs raw local monotonic (0 on the monotonic tier)
}

// MonotonicClock is the P1 ClockSource: local monotonic since construction plus a settable offset.
// The offset is the software-sync tier's slew hook (P2) - kept here so P2 can discipline the same
// clock object without a transport/timecode change. Safe for concurrent use.
type MonotonicClock struct {
	start  time.Time
	offset atomic.Int64 // ns, applied by the (future) software-sync tier
}

// NewMonotonicClock starts a monotonic media clock at Now()==0.
func NewMonotonicClock() *MonotonicClock { return &MonotonicClock{start: time.Now()} }

// Now returns media-clock nanoseconds (monotonic elapsed + slew offset).
func (c *MonotonicClock) Now() int64 { return int64(time.Since(c.start)) + c.offset.Load() }

// SetOffset applies a slew offset (P2 software-sync hook; no-op on the monotonic tier itself).
func (c *MonotonicClock) SetOffset(ns int64) { c.offset.Store(ns) }

// Quality reports the monotonic tier (always locked; offset = current slew).
func (c *MonotonicClock) Quality() ClockQuality {
	return ClockQuality{Tier: TierMonotonic, Locked: true, OffsetNs: c.offset.Load()}
}

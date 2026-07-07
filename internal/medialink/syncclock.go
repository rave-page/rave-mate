package medialink

import (
	"sync"
	"sync/atomic"
	"time"
)

// syncclock.go is the §2.3 tier-2 software clock sync: NTP-style pairwise offset measurement
// (SyncPing/SyncPong on the meta stream, RFC 5905 on-wire semantics) filtered Cristian/NTP-style -
// the min-RTT sample over a sliding window wins; samples with RTT > 2× the window minimum are
// disqualified as queueing. SoftwareClock slews a monotonic base by the filtered estimate. The OS
// clock is never touched. PTP + follow-master tiers land P8 behind the same ClockSource seam.

const (
	syncWindow     = 32               // sliding sample window
	syncMaxAge     = 60 * time.Second // samples older than this age out
	syncLockMin    = 3                // qualifying samples needed to declare lock
	syncStaleAfter = 30 * time.Second // freshest sample older than this → lock lost (holdover)
)

type offsetSample struct {
	offset int64 // remote − local, ns
	rtt    int64 // round-trip, ns
	at     time.Time
}

// SyncEstimate is the filtered pairwise clock-sync state (telemetry, §7).
type SyncEstimate struct {
	OffsetNs     int64 // winning (min-RTT) sample's offset
	RTTNs        int64 // winning sample's RTT
	DispersionNs int64 // offset spread across qualifying samples (quality hint)
	Samples      int   // samples in window after aging
	Locked       bool  // ≥ syncLockMin qualifying samples and fresh
	LastAt       time.Time
}

// OffsetEstimator is the clock filter: a sliding window of offset/RTT samples with min-RTT
// selection + 2×-min disqualification (§2.3). Safe for concurrent use.
type OffsetEstimator struct {
	mu   sync.Mutex
	ring [syncWindow]offsetSample
	n    int // filled
	next int // ring cursor
}

// Add records one measurement (offset = remote − local at measurement time).
func (e *OffsetEstimator) Add(offsetNs, rttNs int64, now time.Time) {
	if rttNs < 0 {
		return // clock stepped mid-probe; unusable
	}
	e.mu.Lock()
	e.ring[e.next] = offsetSample{offset: offsetNs, rtt: rttNs, at: now}
	e.next = (e.next + 1) % syncWindow
	if e.n < syncWindow {
		e.n++
	}
	e.mu.Unlock()
}

// Estimate runs the clock filter over the (aged) window. ok=false when no usable sample remains.
func (e *OffsetEstimator) Estimate(now time.Time) (SyncEstimate, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	cutoff := now.Add(-syncMaxAge)
	live := make([]offsetSample, 0, e.n)
	for i := 0; i < e.n; i++ {
		if s := e.ring[i]; s.at.After(cutoff) {
			live = append(live, s)
		}
	}
	if len(live) == 0 {
		return SyncEstimate{}, false
	}
	minRTT := live[0].rtt
	for _, s := range live[1:] {
		if s.rtt < minRTT {
			minRTT = s.rtt
		}
	}
	var (
		best               offsetSample
		qual               int
		lastAt             time.Time
		minOff, maxOff     int64
		haveBest, haveQual bool
	)
	for _, s := range live {
		if s.at.After(lastAt) {
			lastAt = s.at
		}
		if s.rtt > 2*minRTT {
			continue // queueing-delayed: disqualified (§2.3)
		}
		qual++
		if !haveQual || s.offset < minOff {
			minOff = s.offset
		}
		if !haveQual || s.offset > maxOff {
			maxOff = s.offset
		}
		haveQual = true
		if !haveBest || s.rtt < best.rtt {
			best, haveBest = s, true
		}
	}
	return SyncEstimate{
		OffsetNs:     best.offset,
		RTTNs:        best.rtt,
		DispersionNs: maxOff - minOff,
		Samples:      len(live),
		Locked:       qual >= syncLockMin && now.Sub(lastAt) <= syncStaleAfter,
		LastAt:       lastAt,
	}, true
}

// DisciplinedClock is a ClockSource that accepts pairwise sync samples. AddSample takes the offset
// measured against its own Now() (a residual - the clock converts to an absolute slew internally)
// plus the probe RTT. SoftwareClock implements it; the RouteManager feeds it samples from the
// pinned sync peer (Options.SyncPeer; auto-election is the timecode-plane phase).
type DisciplinedClock interface {
	ClockSource
	AddSample(offsetNs, rttNs int64)
}

// SoftwareClock is the §2.3 tier-2 ClockSource: local monotonic base + an offset slewed from
// filtered sync samples. Safe for concurrent use.
type SoftwareClock struct {
	start  time.Time
	offset atomic.Int64
	locked atomic.Bool
	est    OffsetEstimator
}

// NewSoftwareClock starts an undisciplined software clock at Now()==0 (quality unlocked until
// samples arrive).
func NewSoftwareClock() *SoftwareClock { return &SoftwareClock{start: time.Now()} }

// Now returns media-clock nanoseconds (monotonic elapsed + disciplined offset).
func (c *SoftwareClock) Now() int64 { return int64(time.Since(c.start)) + c.offset.Load() }

// AddSample feeds one residual measurement (remote − Now() at probe time). Residuals are converted
// to absolute offsets against the applied slew so the filter window stays consistent across slews.
func (c *SoftwareClock) AddSample(offsetNs, rttNs int64) {
	now := time.Now()
	c.est.Add(offsetNs+c.offset.Load(), rttNs, now)
	if est, ok := c.est.Estimate(now); ok {
		c.offset.Store(est.OffsetNs)
		c.locked.Store(est.Locked)
	}
}

// Quality reports the software tier's lock state + applied slew (gates the timecode plane, §2.3).
func (c *SoftwareClock) Quality() ClockQuality {
	return ClockQuality{Tier: TierSoftware, Locked: c.locked.Load(), OffsetNs: c.offset.Load()}
}

// SetOffset force-applies a disciplined offset (ns vs raw monotonic) computed elsewhere. Used when
// the sync discipline runs in another process (the media child, #44) and this clock only MIRRORS the
// result so the timecode plane - which reads this daemon-side clock - stays in the same domain. Do
// not mix with AddSample on the same clock: this is the mirror side, that is the discipline side.
func (c *SoftwareClock) SetOffset(offsetNs int64, locked bool) {
	c.offset.Store(offsetNs)
	c.locked.Store(locked)
}

// MirrorNow slews so Now() == targetNs at call time - mirrors a clock disciplined in ANOTHER process
// (the media child) whose monotonic base differs from ours. It sets offset = target − our-raw-elapsed,
// absorbing both the process base skew and the remote discipline offset in one shot. Both clocks then
// advance at real-time rate between mirrors, so ~1 Hz mirroring holds them frame-locked; the only
// error is the (sub-ms) transport lag of the target value. Mirror side only - don't mix with AddSample.
func (c *SoftwareClock) MirrorNow(targetNs int64, locked bool) {
	raw := int64(time.Since(c.start))
	c.offset.Store(targetNs - raw)
	c.locked.Store(locked)
}

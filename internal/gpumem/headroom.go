package gpumem

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Headroom is the primary adapter's live VRAM headroom - the congestion governor's input.
// Present=false means "no reading" (off Windows, or before the first sample): a consumer MUST
// treat that as "unknown, do not gate" (fail open, matching this package's philosophy), never as
// "zero free".
type Headroom struct {
	Present  bool
	Name     string
	BudgetMB uint64
	UsedMB   uint64
	FreeMB   uint64
	At       time.Time
}

// forceFreeEnv forces a low reading through the whole degradation ladder WITHOUT a saturated card,
// for off-set verification (the crash only bites at real saturation): set
// RAVE_MATE_GPUMEM_FORCE_FREE_MB=<n> and Headroom reports n MB free (Present=true) regardless of
// the real card. Empty/unset/invalid = no override. Read per-call (cheap; the governor polls at
// its control cadence, never per frame) so it can be toggled live via a relaunch.
const forceFreeEnv = "RAVE_MATE_GPUMEM_FORCE_FREE_MB"

func forcedFreeMB() (uint64, bool) {
	s := strings.TrimSpace(os.Getenv(forceFreeEnv))
	if s == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return uint64(n), true
}

// Headroom returns the primary adapter's headroom from the last stored sample, with the
// force-override applied. Safe to call concurrently. Consumed by the governor in the daemon.
func (m *Monitor) Headroom() Headroom {
	m.mu.Lock()
	var h Headroom
	for _, a := range m.last { // rankAdapters puts Primary first; scan defensively
		if a.Primary {
			h = Headroom{Present: true, Name: a.Name, BudgetMB: a.BudgetMB, UsedMB: a.UsedMB, FreeMB: a.FreeMB, At: m.lastAt}
			break
		}
	}
	m.mu.Unlock()
	return applyForce(h)
}

// ReadHeadroom samples s once and returns the primary adapter's headroom (force-override applied).
// For a process with no Monitor (the media featurehost child, where the allocation-gate decisions
// run): one cheap read-only Sample(), same kernel stats the daemon's Monitor reads. nil Sampler or
// a failed sample => Present=false unless the force-override is set.
func ReadHeadroom(s Sampler) Headroom {
	var h Headroom
	if s != nil {
		if ads, err := s.Sample(); err == nil {
			for _, a := range rankAdapters(ads) {
				if a.Primary {
					h = Headroom{Present: true, Name: a.Name, BudgetMB: a.BudgetMB, UsedMB: a.UsedMB, FreeMB: a.FreeMB, At: time.Now()}
					break
				}
			}
		}
	}
	return applyForce(h)
}

// applyForce overlays the env override onto a real (or empty) reading.
func applyForce(h Headroom) Headroom {
	free, ok := forcedFreeMB()
	if !ok {
		return h
	}
	h.Present = true
	if free > h.BudgetMB {
		h.BudgetMB = free // no real budget known (or forced above it): make free the whole budget
	}
	h.FreeMB = free
	h.UsedMB = h.BudgetMB - free
	if h.Name == "" {
		h.Name = "forced"
	}
	h.At = time.Now()
	return h
}

// Retained-doc patch-channel accounting (B7 increment ii) - UNTAGGED like fallback.go/perf.go, so
// webui reads one counter set in both builds. The stateless RZW1 path stays the default and the
// fallback; this channel is an opt-in optimization tier for high-cadence patch sites and every
// way it can decline is counted SEPARATELY, because "it fell back" is exactly the failure mode
// that hid the JSON-null bug for a whole tab.
//
// A cap breach is its own counter (not lumped into the generic decline) because it is the only
// decline that must change behaviour: three breaches on one surface in a session make that
// surface sticky-stateless for the rest of the process (webui side, patch_chan.go).
package zigui

import (
	"sync"
	"sync/atomic"
)

// PatchStatus is what a patch export reports back. Values are the ABI contract with
// native/zigui/src/retain.zig - append only.
type PatchStatus uint8

const (
	PatchOK        PatchStatus = 0 // merged (or seeded) + rendered
	PatchMalformed PatchStatus = 1 // document rejected before any state was touched
	PatchDesync    PatchStatus = 2 // handle/gen/base-hash/locale-gen mismatch → slot dropped, reseed
	PatchCapBreach PatchStatus = 3 // slot table full, or the retained state outgrew the per-slot cap
	PatchError     PatchStatus = 4 // merge/clone/render/OOM → slot dropped
	PatchStub      PatchStatus = 5 // no lib linked (untagged build); nothing was attempted
)

// String names the status for counters + logs.
func (s PatchStatus) String() string {
	switch s {
	case PatchOK:
		return "ok"
	case PatchMalformed:
		return "malformed"
	case PatchDesync:
		return "desync"
	case PatchCapBreach:
		return "cap"
	case PatchError:
		return "error"
	case PatchStub:
		return "stub"
	}
	return "?"
}

// PatchStat is one surface's patch-channel tally.
type PatchStat struct {
	Seeds      uint64 // full-doc reseeds (first send + every send after a decline)
	Deltas     uint64 // delta documents accepted
	Desync     uint64 // guard mismatches (each forces a reseed)
	CapBreach  uint64 // slot-table / per-slot-byte refusals
	Malformed  uint64 // documents refused before any state was touched
	Errors     uint64 // merge/clone/render failures
	Sticky     uint64 // 1 once the surface gave up on the channel for this session
	SeedBytes  uint64 // bytes of full-state reseeds
	DeltaBytes uint64 // bytes of deltas - SeedBytes/Seeds vs DeltaBytes/Deltas IS the live
	// delta/full ratio, the one number that decides whether a surface belongs on this channel
}

var (
	pcMu     sync.Mutex
	pcCounts = map[string]*PatchStat{}
)

// NotePatch records one patch-channel outcome for surface name. seed=true when the document was a
// full-state reseed. bytes = the document size (0 for a call that never reached the ABI).
func NotePatch(name string, st PatchStatus, seed bool, bytes int) {
	pcMu.Lock()
	defer pcMu.Unlock()
	s := pcCounts[name]
	if s == nil {
		s = &PatchStat{}
		pcCounts[name] = s
	}
	switch st {
	case PatchOK:
		if seed {
			s.Seeds++
			s.SeedBytes += uint64(max(bytes, 0))
		} else {
			s.Deltas++
			s.DeltaBytes += uint64(max(bytes, 0))
		}
	case PatchDesync:
		s.Desync++
	case PatchCapBreach:
		s.CapBreach++
	case PatchMalformed:
		s.Malformed++
	case PatchError:
		s.Errors++
	}
}

// NotePatchSticky records that a surface fell back to stateless for the rest of the session.
func NotePatchSticky(name string) {
	pcMu.Lock()
	defer pcMu.Unlock()
	s := pcCounts[name]
	if s == nil {
		s = &PatchStat{}
		pcCounts[name] = s
	}
	s.Sticky = 1
}

// PatchCounts snapshots the per-surface tallies (empty = the channel never ran).
func PatchCounts() map[string]PatchStat {
	pcMu.Lock()
	defer pcMu.Unlock()
	out := make(map[string]PatchStat, len(pcCounts))
	for k, v := range pcCounts {
		out[k] = *v
	}
	return out
}

// ResetPatchCounts clears the tallies (tests only - the live app never resets a counter).
func ResetPatchCounts() {
	pcMu.Lock()
	pcCounts = map[string]*PatchStat{}
	pcMu.Unlock()
}

// localeGen is the i18n generation folded into every retained slot's guard. Go OWNS it: one
// monotonic counter, bumped when the user switches language, and a slot seeded under an older
// generation declines its next delta (→ full-doc reseed carrying the new strings). Increment (iii)
// reuses this plumbing for the catalog handshake; (ii) carries NO catalog payload.
var localeGen atomic.Uint32

func init() { localeGen.Store(1) } // 0 is reserved: a zeroed slot can never match a live document

// LocaleGen returns the current i18n generation.
func LocaleGen() uint32 { return localeGen.Load() }

// BumpLocaleGen advances the i18n generation (called on a locale switch).
func BumpLocaleGen() uint32 { return localeGen.Add(1) }

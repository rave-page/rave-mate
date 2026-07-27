//go:build windows && cgo

package mfenc

// affinity: which ADAPTER can open a given video-share sender's shared texture (zigmedia
// increment 3, item 3 - risk R7).
//
// `OpenSharedResource` only works on the adapter that created the texture, so a sender produced by
// an app on GPU B is invisible to an encoder child pinned to GPU A: the child refuses with
// `open_shared` and the route drops to the readback path, paying the full GPU→CPU copy the
// zero-copy path exists to remove. Measured on the dev rig (M4): with two adapters present, one
// ACCEPTS the same sender and the other REFUSES it.
//
// Nothing in DXGI answers "which adapter owns this share handle", so the resolution is a bounded
// PROBE: try the other adapters once, remember the answer per sender, and never probe that sender
// again. Constraints from R7, kept literally:
//
//   - **Never silently move adapters.** A move happens only when the caller passes candidates,
//     which mediapipe does only under DevicePolicy auto (an explicitly pinned device is user
//     policy and is never overridden), and every move logs once.
//   - The probe is bounded: at most one attempt per candidate adapter, and the result - INCLUDING
//     the negative - is cached, so a sender no adapter can open costs the probe exactly once and
//     goes straight to the readback path afterwards.

import (
	"errors"
	"fmt"
	"sync"
)

// affinityNone marks "probed, no adapter could open this sender's texture". Distinct from "unknown"
// so a negative result is remembered instead of re-probed on every route open.
const affinityNone int64 = -1

var (
	affinityMu    sync.Mutex
	senderAdapter = map[string]int64{} // sender name → LUID that opened it, or affinityNone
	adapterMoves  int                  // successful re-places (diagnostic, process-wide)
)

// AdapterAffinity returns the cached adapter for a sender: (luid, true) when known and usable,
// (0, false) when unknown or known-unusable.
func AdapterAffinity(sender string) (int64, bool) {
	affinityMu.Lock()
	defer affinityMu.Unlock()
	l, ok := senderAdapter[sender]
	if !ok || l == affinityNone {
		return 0, false
	}
	return l, true
}

// AdapterAffinityProbed reports whether this sender was already probed (positive or negative).
func AdapterAffinityProbed(sender string) bool {
	affinityMu.Lock()
	defer affinityMu.Unlock()
	_, ok := senderAdapter[sender]
	return ok
}

// AdapterMoves is the process-wide count of sessions re-placed onto another adapter (diagnostic:
// a rig that constantly re-places is telling you its device policy fights its sender layout).
func AdapterMoves() int {
	affinityMu.Lock()
	defer affinityMu.Unlock()
	return adapterMoves
}

func recordAffinity(sender string, luid int64) {
	if sender == "" {
		return
	}
	affinityMu.Lock()
	senderAdapter[sender] = luid
	affinityMu.Unlock()
}

// resetAffinity clears the cache (tests only).
func resetAffinity() {
	affinityMu.Lock()
	senderAdapter = map[string]int64{}
	adapterMoves = 0
	affinityMu.Unlock()
}

// affinityCandidates orders the adapters to probe for a sender whose texture `primary` refused:
// the CACHED adapter first when there is one (so the second route on the same sender pays no
// probe at all), then every other candidate in enumeration order, `primary` excluded and
// duplicates dropped. An empty result means "do not move" - the caller keeps today's downgrade.
func affinityCandidates(sender string, primary int64, all []int64) []int64 {
	var out []int64
	seen := map[int64]bool{primary: true}
	if l, ok := AdapterAffinity(sender); ok && !seen[l] {
		out = append(out, l)
		seen[l] = true
	}
	for _, l := range all {
		if seen[l] {
			continue
		}
		seen[l] = true
		out = append(out, l)
	}
	return out
}

// replaceOnAffineAdapter retries a zero-copy open on the candidate adapters after `primary`
// refused the SOURCE. Returns the live session and the adapter it landed on.
//
// Bounded: one attempt per candidate, and only source-side refusals are retried - an encoder-side
// failure (no MFT, poisoned tuple, crash-looping child) is NOT an affinity problem and stops the
// loop immediately, so a broken rig cannot turn one open into N child spawns.
func replaceOnAffineAdapter(o ProcOpts, candidates []int64, openOn func(ProcOpts) (*ProcSession, error)) (*ProcSession, int64, error) {
	if o.Spout == nil || len(candidates) == 0 {
		return nil, 0, ErrZeroCopyRefused
	}
	lastErr := error(ErrZeroCopyRefused)
	tried := 0
	for _, luid := range candidates {
		tried++
		alt := o
		alt.LUID = luid
		s, err := openOn(alt)
		if err == nil {
			recordAffinity(o.Spout.Name, luid)
			affinityMu.Lock()
			adapterMoves++
			affinityMu.Unlock()
			s.movedFrom = o.LUID
			Warnf("mfenc: sender %q is not on adapter %#x - zero-copy session re-placed on adapter %#x (affinity resolved, R7). Encode now runs on that adapter.",
				o.Spout.Name, uint64(o.LUID), uint64(luid))
			return s, luid, nil
		}
		lastErr = err
		if !errors.Is(err, ErrZeroCopyRefused) {
			// Not a source refusal: the encoder side is unhappy on this adapter. Probing further
			// adapters cannot fix that and would just spawn more children.
			break
		}
	}
	// Remember the negative so the next route on this sender goes straight to the readback path.
	recordAffinity(o.Spout.Name, affinityNone)
	return nil, 0, fmt.Errorf("%w: no adapter could open sender %q (probed %d/%d, last: %v)",
		ErrZeroCopyRefused, o.Spout.Name, tried, len(candidates), lastErr)
}

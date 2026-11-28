package medialink

// Per-source caps (receiver side): the user caps one received source's bitrate and frame rate on the
// RECEIVING instance (config RememberedReceive.MaxKbps/MaxFPS). Bitrate reaches the sender twice - in
// the Offer (OfferOptions.BitrateKbps, the encoder's starting ceiling) and in every governor MetaRate
// hint; fps only through the MetaRate hint (Offer carries no fps). Both governors read the caps EVERY
// tick, so a change on a running route applies within one report interval without a re-offer.

// sourceCapsFn returns the live cap reader for one recv route (nil = unwired: no per-source caps).
func (rm *RouteManager) sourceCapsFn(peer, sourceID string) func() (kbps, fps int) {
	if rm.routeCaps == nil {
		return nil
	}
	return func() (int, int) { return rm.routeCaps(peer, sourceID) }
}

// readSourceCaps reads a cap reader (nil-safe; negatives = none).
func readSourceCaps(caps func() (int, int)) (kbps, fps int) {
	if caps == nil {
		return 0, 0
	}
	k, f := caps()
	return max(k, 0), max(f, 0)
}

// applySourceCaps lowers h to the per-source caps (0 = no cap on that axis). Applied LAST, after the
// VRAM ladder / congestion controller / decode + coded-size caps, so the user cap always binds.
func applySourceCaps(h *RateHint, kbps, fps int) {
	h.MaxBitrateKbps = minCap(h.MaxBitrateKbps, kbps)
	h.MaxFPS = minCap(h.MaxFPS, fps)
}

// sourceCapReason overrides the budget reason when the per-source cap is what binds.
func sourceCapReason(h RateHint, kbps, fps int, reason string) string {
	switch {
	case kbps > 0 && h.MaxBitrateKbps == kbps:
		return "source-cap (user)"
	case fps > 0 && h.MaxFPS == fps && reason == "quality-model (target met)":
		return "quality-model, source fps cap (user)"
	}
	return reason
}

// CapOfferKbps applies a per-source bitrate cap to an offer's base budget (both bitrate modes:
// auto = the quality-model target capped; fixed = min(global, per-source)). 0 = no cap.
func CapOfferKbps(baseKbps, capKbps int) int { return minCap(baseKbps, capKbps) }

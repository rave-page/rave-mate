package mediaroute

// unsaturatedFrac: a route whose compare-measured rate is below this fraction of its tick rate saw
// duplicate ticks, so the tick outran the producer and SrcFPS IS the producer's rate. At or above it
// the producer is at least as fast as the tick and its real rate is unknown (could be higher).
const unsaturatedFrac = 0.9

// frameDetectCompare mirrors mfenc.DetectGPUCompare (mediaroute does not import the cgo encoder).
const frameDetectCompare = "gpu-compare"

// compareFPS reads sender n's producer rate from the encoder child's GPU frame compare on its live
// zero-copy send routes - the measurement for senders with no Spout frame counter, which needs no
// user setting. fps = the highest UNSATURATED route measurement (0 = none); saturated = some route
// ticks at or below the producer, i.e. the producer may be faster than any advert derived from it.
// Scan cadence only, never on a frame path.
func (m *Manager) compareFPS(n string) (fps float64, saturated bool) {
	id := "spout:" + n
	for _, s := range m.router.Stats() {
		p := s.Pipe
		if s.Direction != "send" || s.Source != id || p == nil || p.FrameDetect != frameDetectCompare || p.TickFPS <= 0 {
			continue
		}
		if p.SrcFPS >= unsaturatedFrac*p.TickFPS {
			saturated = true
			continue
		}
		fps = max(fps, p.SrcFPS)
	}
	return fps, saturated
}

// compareObs folds one compareFPS reading into o (m.mu held). A real unsaturated rate replaces the
// last one; a saturated reading with no usable rate clears it (back to the assumed ceiling, so a
// route negotiated at a lower rate cannot pin the advert there); a static picture or no live route
// keeps the last measured rate.
func compareObs(o *fpsObs, fps float64, saturated bool) {
	switch {
	case fps >= minMeasuredFPS/2:
		o.fps = fps
	case saturated:
		o.fps = 0
	}
}

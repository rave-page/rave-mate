package medialink

// SMPTE LTC (Linear TimeCode): one 80-bit word per frame, biphase-mark (Manchester) encoded as an
// audio signal - the standard way to distribute timecode to DAWs / lighting / Resolume. EncodeLTC
// generates a real, spec-layout LTC audio frame (drop-frame flag, sync word, phase-correction/parity
// bit) so external gear can chase rave-mate; DecodeLTC recovers a Timecode so rave-mate can chase an
// external LTC master. Biphase mark: a transition at every bit boundary, plus a mid-bit transition
// for a '1'.

const ltcAmp = 0x4000 // signal amplitude (int16)

// ltcSync is the 16-bit sync word (bits 64..79) in transmit (LSB-first) order: 0x3FFD.
var ltcSync = [16]int{0, 0, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 0, 1}

// ltcBits builds the 80-bit LTC word (transmit order, bit0 first) for tc: BCD time fields in their
// SMPTE positions, drop flag, sync word, user bits 0, and the phase-correction bit set for even
// parity (bit 59 for 25fps, else bit 27).
func ltcBits(tc Timecode) [80]int {
	var b [80]int
	put := func(lsb, width, val int) {
		for i := 0; i < width; i++ {
			b[lsb+i] = (val >> i) & 1
		}
	}
	put(0, 4, tc.F%10) // frame units
	put(8, 2, tc.F/10) // frame tens
	if tc.Rate.Drop {  // drop-frame flag
		b[10] = 1
	}
	put(16, 4, tc.S%10) // seconds units
	put(24, 3, tc.S/10) // seconds tens
	put(32, 4, tc.M%10) // minutes units
	put(40, 3, tc.M/10) // minutes tens
	put(48, 4, tc.H%10) // hours units
	put(56, 2, tc.H/10) // hours tens
	for i, v := range ltcSync {
		b[64+i] = v
	}
	// Phase-correction (parity) bit → even total number of 1s.
	parityPos := 27
	if tc.Rate.Nominal == 25 {
		parityPos = 59
	}
	ones := 0
	for i, v := range b {
		if i != parityPos {
			ones += v
		}
	}
	if ones%2 == 1 {
		b[parityPos] = 1
	}
	return b
}

// EncodeLTC renders tc as one frame of LTC audio (mono int16 PCM) at sampleRate. Frame length =
// sampleRate × den/num samples (drop-frame aware). Concatenate successive frames for a running LTC.
// Returns nil for rates SMPTE 12M-1 LTC can't carry (>30 fps need 12M-2 field-doubling).
func EncodeLTC(tc Timecode, sampleRate int) []int16 {
	return EncodeLTCInto(tc, sampleRate, ltcAmp, nil)
}

// EncodeLTCInto is EncodeLTC with a configurable peak amplitude (amp, int16) and an optional
// cross-frame sub-sample accumulator: pass a persistent *acc when rendering a continuous stream so
// the fractional samples/frame (e.g. 1601.6 @48k/29.97) never drift over the long run - the DAC
// stays frame-locked. nil acc = per-frame (self-contained, like EncodeLTC). Reuses the ltcBits
// spec layout (single encoder). Returns nil for rates LTC can't carry.
func EncodeLTCInto(tc Timecode, sampleRate int, amp int16, acc *float64) []int16 {
	if !tc.Rate.ltcSupported() {
		return nil
	}
	bits := ltcBits(tc)
	num, den := tc.Rate.exact()
	if num == 0 {
		return nil
	}
	samplesPerHalf := float64(sampleRate) * float64(den) / float64(num) / 160 // 80 bits × 2 halves
	out := make([]int16, 0, sampleRate/int(num)+160)
	level := amp
	var local float64
	a := &local
	if acc != nil {
		a = acc
	}
	emitHalf := func() {
		*a += samplesPerHalf
		n := int(*a + 1e-9)
		*a -= float64(n)
		for k := 0; k < n; k++ {
			out = append(out, level)
		}
	}
	for _, bit := range bits {
		level = -level // transition at every bit boundary
		emitHalf()
		if bit == 1 {
			level = -level // mid-bit transition = '1'
		}
		emitHalf()
	}
	return out
}

// DecodeLTC recovers a Timecode from LTC audio (mono int16 PCM). rate supplies the nominal fps
// (LTC's wire form doesn't carry it - chase software is configured to the project fps); the drop
// flag is read from the signal. ok=false if no valid LTC frame is found.
func DecodeLTC(pcm []int16, rate Rate) (Timecode, bool) {
	// Edges = sign changes of the biphase square wave.
	var gaps []int
	last := -1
	prevPos := false
	for i, s := range pcm {
		pos := s >= 0
		if i == 0 {
			prevPos = pos
			last = 0
			continue
		}
		if pos != prevPos {
			if last >= 0 {
				gaps = append(gaps, i-last)
			}
			last = i
			prevPos = pos
		}
	}
	if len(gaps) < 80 {
		return Timecode{}, false
	}
	short := gaps[0]
	for _, g := range gaps {
		if g > 0 && g < short {
			short = g
		}
	}
	if short <= 0 {
		return Timecode{}, false
	}
	thr := short*3/2 + 1 // > thr = long (one full bit, '0'); <= thr = short (pair = '1')

	// Biphase decode → bitstream.
	var bits []int
	for i := 0; i < len(gaps); {
		if gaps[i] > thr {
			bits = append(bits, 0)
			i++
		} else if i+1 < len(gaps) && gaps[i+1] <= thr {
			bits = append(bits, 1)
			i += 2
		} else {
			i++ // stray half at stream end
		}
	}

	// Find the sync word; the 64 data bits precede it.
	for p := 64; p+16 <= len(bits); p++ {
		if !matchSync(bits[p : p+16]) {
			continue
		}
		if tc, ok := parseLTC(bits[p-64:p], rate); ok {
			return tc, true
		}
	}
	return Timecode{}, false
}

func matchSync(b []int) bool {
	for i, v := range ltcSync {
		if b[i] != v {
			return false
		}
	}
	return true
}

// parseLTC reads BCD time fields from the 64 data bits; false if any field is out of range.
func parseLTC(d []int, rate Rate) (Timecode, bool) {
	val := func(lsb, width int) int {
		v := 0
		for i := 0; i < width; i++ {
			v |= d[lsb+i] << i
		}
		return v
	}
	f := val(8, 2)*10 + val(0, 4)
	s := val(24, 3)*10 + val(16, 4)
	m := val(40, 3)*10 + val(32, 4)
	h := val(56, 2)*10 + val(48, 4)
	if f > 59 || s > 59 || m > 59 || h > 23 {
		return Timecode{}, false
	}
	r := rate
	r.Drop = d[10] == 1
	return Timecode{H: h, M: m, S: s, F: f, Rate: r}, true
}

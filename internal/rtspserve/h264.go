package rtspserve

// H.264 Annex-B plumbing: incremental NAL splitting, access-unit assembly, RFC 6184 RTP
// payloadization. Pure passthrough - no decode.

// NAL unit types we act on.
const (
	nalSliceNonIDR = 1
	nalSliceIDR    = 5
	nalSEI         = 6
	nalSPS         = 7
	nalPPS         = 8
	nalAUD         = 9
)

const splitterMaxBuf = 8 << 20 // runaway guard: no sane NAL between start codes exceeds this

// nalSplitter incrementally splits an Annex-B byte stream (00 00 01 / 00 00 00 01 start
// codes) into NAL units. Feed with Write; emits complete units via onNAL (slice is owned
// by the callee).
type nalSplitter struct {
	buf   []byte
	onNAL func(nal []byte)
}

// Write consumes stream bytes, emitting every NAL terminated by a following start code.
func (s *nalSplitter) Write(p []byte) (int, error) {
	s.buf = append(s.buf, p...)
	for {
		st := findStartCode(s.buf, 0)
		if st < 0 {
			if len(s.buf) > 3 {
				s.buf = s.buf[len(s.buf)-3:] // keep a possible split start code, drop garbage
			}
			return len(p), nil
		}
		next := findStartCode(s.buf, st+3)
		if next < 0 {
			if st > 0 {
				s.buf = append(s.buf[:0], s.buf[st:]...) // drop pre-stream garbage
			}
			if len(s.buf) > splitterMaxBuf {
				s.buf = s.buf[:0]
			}
			return len(p), nil
		}
		s.emit(s.buf[st+3 : next])
		s.buf = append(s.buf[:0], s.buf[next:]...)
	}
}

// Flush emits the trailing NAL at end of stream.
func (s *nalSplitter) Flush() {
	if st := findStartCode(s.buf, 0); st >= 0 {
		s.emit(s.buf[st+3:])
	}
	s.buf = nil
}

// emit strips trailing zero bytes (they belong to the next start code / trailing zeros)
// and hands a copy to the callback.
func (s *nalSplitter) emit(nal []byte) {
	for len(nal) > 0 && nal[len(nal)-1] == 0 {
		nal = nal[:len(nal)-1]
	}
	if len(nal) == 0 {
		return
	}
	cp := make([]byte, len(nal))
	copy(cp, nal)
	s.onNAL(cp)
}

// findStartCode returns the index of the first 00 00 01 sequence at or after from (-1 if none).
func findStartCode(b []byte, from int) int {
	for i := from; i+2 < len(b); i++ {
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
			return i
		}
	}
	return -1
}

// accessUnit is one coded video frame: its NALs (no AUD) + whether it contains an IDR.
type accessUnit struct {
	nals [][]byte
	key  bool
}

// auAssembler groups NALs into access units. Boundaries: an AUD (the encode chain inserts
// them via h264_metadata=aud=insert), or - fallback for AUD-less streams - a first-slice
// VCL / parameter-set NAL following a VCL NAL. Latest SPS/PPS are captured for the SDP.
type auAssembler struct {
	onAU     func(au accessUnit)
	onParams func(sps, pps []byte)

	cur     [][]byte
	key     bool
	prevVCL bool
	sps     []byte
	pps     []byte
}

// addNAL feeds one NAL unit.
func (a *auAssembler) addNAL(nal []byte) {
	if len(nal) == 0 {
		return
	}
	t := nal[0] & 0x1F
	switch t {
	case nalSPS:
		a.sps = nal
		a.notifyParams()
	case nalPPS:
		a.pps = nal
		a.notifyParams()
	}
	boundary := t == nalAUD ||
		(a.prevVCL && (t == nalSPS || t == nalPPS || t == nalSEI)) ||
		(a.prevVCL && (t == nalSliceNonIDR || t == nalSliceIDR) && firstMBInSliceZero(nal))
	if boundary {
		a.emit()
	}
	if t == nalAUD {
		return // delimiter only - not forwarded
	}
	a.cur = append(a.cur, nal)
	if t == nalSliceIDR {
		a.key = true
	}
	a.prevVCL = t == nalSliceNonIDR || t == nalSliceIDR
}

// Flush emits a pending access unit at end of stream.
func (a *auAssembler) Flush() { a.emit() }

func (a *auAssembler) emit() {
	if len(a.cur) > 0 {
		a.onAU(accessUnit{nals: a.cur, key: a.key})
	}
	a.cur, a.key, a.prevVCL = nil, false, false
}

func (a *auAssembler) notifyParams() {
	if a.onParams != nil && len(a.sps) > 0 && len(a.pps) > 0 {
		a.onParams(a.sps, a.pps)
	}
}

// firstMBInSliceZero reports whether a slice NAL starts a new picture: first_mb_in_slice
// is the first ue(v) of the slice header; value 0 encodes as a leading '1' bit.
func firstMBInSliceZero(nal []byte) bool {
	return len(nal) > 1 && nal[1]&0x80 != 0
}

// rtpPayload is one RTP packet payload + its marker bit (last packet of the access unit).
type rtpPayload struct {
	data   []byte
	marker bool
}

const rtpMaxPayload = 1400 // fits typical MTU; irrelevant for TCP but keeps packets modest

// payloadize renders an access unit's NALs into RTP payloads per RFC 6184: single-NAL
// packets when they fit, FU-A fragmentation otherwise. Marker set on the AU's last packet.
func payloadize(nals [][]byte, maxPayload int) []rtpPayload {
	var out []rtpPayload
	for _, nal := range nals {
		if len(nal) == 0 {
			continue
		}
		if len(nal) <= maxPayload {
			out = append(out, rtpPayload{data: nal})
			continue
		}
		indicator := nal[0]&0xE0 | 28 // F+NRI preserved, type 28 = FU-A
		typ := nal[0] & 0x1F
		rest := nal[1:]
		first := true
		for len(rest) > 0 {
			n := maxPayload - 2
			if n > len(rest) {
				n = len(rest)
			}
			hdr := typ
			if first {
				hdr |= 0x80 // S
			}
			if n == len(rest) {
				hdr |= 0x40 // E
			}
			pkt := make([]byte, 0, n+2)
			pkt = append(pkt, indicator, hdr)
			pkt = append(pkt, rest[:n]...)
			out = append(out, rtpPayload{data: pkt})
			rest = rest[n:]
			first = false
		}
	}
	if len(out) > 0 {
		out[len(out)-1].marker = true
	}
	return out
}

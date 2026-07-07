package mediapipe

// ausplit.go - pure access-unit framing of the encode child's stdout byte stream.
// H.264/HEVC: Annex-B with INSERTED AUDs (encodeArgs adds the metadata bsf) - every AU starts
// with an AUD NAL, so the stream splits at AUD start codes. Keyframe = IDR/IRAP VCL present;
// config-only AU (params, no VCL) = FlagConfig. MJPEG: SOI…EOI markers frame each picture.

// auInfo classifies one emitted access unit.
type auInfo struct {
	Keyframe bool
	Config   bool // parameter-set-only AU (no VCL slice)
}

// auEmit receives each complete access unit (au is a fresh copy, caller-owned).
type auEmit func(au []byte, info auInfo)

// auSplitter splits an Annex-B byte stream at AUD boundaries.
type auSplitter struct {
	hevc bool
	buf  []byte
	emit auEmit
}

func newAUSplitter(hevc bool, emit auEmit) *auSplitter { return &auSplitter{hevc: hevc, emit: emit} }

const (
	naluAUDH264 = 9
	naluAUDHEVC = 35
)

// auSplitBufCap bounds the pending buffer (an AUD-less stream would otherwise grow forever).
const auSplitBufCap = 16 << 20

// Write feeds stream bytes; complete AUs are emitted synchronously.
func (s *auSplitter) Write(p []byte) (int, error) {
	s.buf = append(s.buf, p...)
	for {
		// Find the FIRST AUD start (AU begin) and the NEXT one (AU end).
		first := s.findAUD(0)
		if first < 0 {
			if len(s.buf) > auSplitBufCap { // hostile/AUD-less stream guard
				s.buf = append(s.buf[:0], s.buf[len(s.buf)/2:]...)
			}
			return len(p), nil
		}
		next := s.findAUD(first + 4)
		if next < 0 {
			// Head garbage before the first AUD (stream join mid-AU) is dropped once bounded.
			if first > 0 {
				s.buf = append(s.buf[:0], s.buf[first:]...)
			}
			return len(p), nil
		}
		au := make([]byte, next-first)
		copy(au, s.buf[first:next])
		s.buf = append(s.buf[:0], s.buf[next:]...)
		s.emit(au, s.classify(au))
	}
}

// flush emits the buffered tail as a final AU (stream end - no next AUD will arrive).
func (s *auSplitter) flush() {
	first := s.findAUD(0)
	if first < 0 {
		s.buf = s.buf[:0]
		return
	}
	au := make([]byte, len(s.buf)-first)
	copy(au, s.buf[first:])
	s.buf = s.buf[:0]
	s.emit(au, s.classify(au))
}

// findAUD returns the index of the start code beginning an AUD NAL at/after from, or -1.
func (s *auSplitter) findAUD(from int) int {
	b := s.buf
	for i := from; i+4 < len(b); i++ {
		if b[i] != 0 || b[i+1] != 0 {
			continue
		}
		// 3- or 4-byte start code.
		var hdr int
		if b[i+2] == 1 {
			hdr = i + 3
		} else if b[i+2] == 0 && i+4 < len(b) && b[i+3] == 1 {
			hdr = i + 4
		} else {
			continue
		}
		if hdr >= len(b) {
			return -1 // header byte not buffered yet
		}
		if s.nalType(b[hdr]) == s.audType() {
			return i
		}
	}
	return -1
}

func (s *auSplitter) audType() int {
	if s.hevc {
		return naluAUDHEVC
	}
	return naluAUDH264
}

func (s *auSplitter) nalType(hdr byte) int {
	if s.hevc {
		return int(hdr>>1) & 0x3f
	}
	return int(hdr) & 0x1f
}

// classify scans an AU's NALs for keyframe / config-only.
func (s *auSplitter) classify(au []byte) auInfo {
	var key, vcl bool
	forEachNAL(au, func(hdr byte) {
		t := s.nalType(hdr)
		if s.hevc {
			switch {
			case t >= 16 && t <= 23: // BLA/IDR/CRA (IRAP)
				key, vcl = true, true
			case t <= 9: // non-IRAP VCL
				vcl = true
			}
			return
		}
		switch t {
		case 5:
			key, vcl = true, true
		case 1, 2, 3, 4:
			vcl = true
		}
	})
	return auInfo{Keyframe: key, Config: !vcl}
}

// forEachNAL invokes fn with each NAL's first header byte.
func forEachNAL(b []byte, fn func(hdr byte)) {
	for i := 0; i+3 < len(b); i++ {
		if b[i] != 0 || b[i+1] != 0 {
			continue
		}
		var hdr int
		if b[i+2] == 1 {
			hdr = i + 3
		} else if b[i+2] == 0 && i+4 < len(b) && b[i+3] == 1 {
			hdr = i + 4
		} else {
			continue
		}
		if hdr < len(b) {
			fn(b[hdr])
			i = hdr
		}
	}
}

// jpegSplitter splits an MJPEG stream at SOI…EOI (FFD8…FFD9) markers. Every picture is a
// keyframe (intra codec).
type jpegSplitter struct {
	buf  []byte
	emit auEmit
}

func newJPEGSplitter(emit auEmit) *jpegSplitter { return &jpegSplitter{emit: emit} }

func (s *jpegSplitter) Write(p []byte) (int, error) {
	s.buf = append(s.buf, p...)
	for {
		soi := indexMarker(s.buf, 0xD8, 0)
		if soi < 0 {
			// No SOI: drop garbage, keep a possible split marker tail.
			if n := len(s.buf); n > 0 && s.buf[n-1] == 0xFF {
				s.buf[0] = 0xFF
				s.buf = s.buf[:1]
			} else {
				s.buf = s.buf[:0]
			}
			return len(p), nil
		}
		eoi := indexMarker(s.buf, 0xD9, soi+2)
		if eoi < 0 {
			if soi > 0 {
				s.buf = append(s.buf[:0], s.buf[soi:]...)
			}
			return len(p), nil
		}
		au := make([]byte, eoi+2-soi)
		copy(au, s.buf[soi:eoi+2])
		s.buf = append(s.buf[:0], s.buf[eoi+2:]...)
		s.emit(au, auInfo{Keyframe: true})
	}
}

// indexMarker finds the JPEG marker FF<code> at/after from, or -1.
func indexMarker(b []byte, code byte, from int) int {
	for i := from; i+1 < len(b); i++ {
		if b[i] == 0xFF && b[i+1] == code {
			return i
		}
	}
	return -1
}

package audio

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
)

// aiffDecoder reads AIFF + AIFF-C: big-endian signed PCM (NONE/twos), little-endian signed
// (sowt), and IEEE float (fl32/FL32/fl64/FL64). Seek is O(1): ssndStart + frame*blockAlign.
type aiffDecoder struct {
	r          io.ReadSeekCloser
	format     Format
	ssndStart  int64 // byte offset of the first sample
	total      int64 // frames
	blockAlign int
	bits       int
	isFloat    bool
	littleEnd  bool // sowt
	cur        int64
	buf        []byte
}

func newAIFFDecoder(r io.ReadSeekCloser) (Decoder, error) {
	var hdr [12]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		_ = r.Close()
		return nil, fmt.Errorf("aiff: header: %w", err)
	}
	form := string(hdr[8:12])
	if string(hdr[0:4]) != "FORM" || (form != "AIFF" && form != "AIFC") {
		_ = r.Close()
		return nil, fmt.Errorf("aiff: not a FORM/AIFF file")
	}
	d := &aiffDecoder{r: r}
	off := int64(12)
	var haveComm, haveSSND bool
	for {
		var ch [8]byte
		if _, err := io.ReadFull(r, ch[:]); err != nil {
			break
		}
		off += 8
		id := string(ch[0:4])
		sz := int64(binary.BigEndian.Uint32(ch[4:8]))
		switch id {
		case "COMM":
			body := make([]byte, sz)
			if _, err := io.ReadFull(r, body); err != nil {
				_ = r.Close()
				return nil, fmt.Errorf("aiff: COMM: %w", err)
			}
			if err := d.parseComm(body, form == "AIFC"); err != nil {
				_ = r.Close()
				return nil, err
			}
			haveComm = true
			off += sz + (sz & 1)
			if sz&1 == 1 {
				if _, err := r.Seek(1, io.SeekCurrent); err != nil {
					break
				}
			}
		case "SSND":
			var so [8]byte
			if _, err := io.ReadFull(r, so[:]); err != nil {
				_ = r.Close()
				return nil, fmt.Errorf("aiff: SSND header: %w", err)
			}
			soundOff := int64(binary.BigEndian.Uint32(so[0:4]))
			d.ssndStart = off + 8 + soundOff
			haveSSND = true
			// skip the rest of the SSND payload
			rest := sz - 8 + (sz & 1)
			if _, err := r.Seek(rest, io.SeekCurrent); err != nil {
				break
			}
			off += sz + (sz & 1)
		default:
			skip := sz + (sz & 1)
			if _, err := r.Seek(skip, io.SeekCurrent); err != nil {
				break
			}
			off += skip
		}
		if haveComm && haveSSND {
			break
		}
	}
	if !haveComm || !haveSSND {
		_ = r.Close()
		return nil, fmt.Errorf("aiff: missing COMM or SSND chunk")
	}
	if d.format.Channels <= 0 || d.format.SampleRate <= 0 || d.blockAlign <= 0 {
		_ = r.Close()
		return nil, fmt.Errorf("aiff: bad channels/rate/blockalign")
	}
	if _, err := r.Seek(d.ssndStart, io.SeekStart); err != nil {
		_ = r.Close()
		return nil, err
	}
	return d, nil
}

func (d *aiffDecoder) parseComm(b []byte, aifc bool) error {
	if len(b) < 18 {
		return fmt.Errorf("aiff: COMM too short")
	}
	d.format.Channels = int(int16(binary.BigEndian.Uint16(b[0:2])))
	d.total = int64(binary.BigEndian.Uint32(b[2:6]))
	d.bits = int(int16(binary.BigEndian.Uint16(b[6:8])))
	d.format.SampleRate = int(extended80ToFloat(b[8:18]))
	d.isFloat = false
	if aifc && len(b) >= 22 {
		switch string(b[18:22]) {
		case "NONE", "twos":
			d.littleEnd = false
		case "sowt":
			d.littleEnd = true
		case "fl32", "FL32":
			d.isFloat, d.bits = true, 32
		case "fl64", "FL64":
			d.isFloat, d.bits = true, 64
		default:
			return fmt.Errorf("aiff: unsupported AIFC compression %q (use ffmpeg)", string(b[18:22]))
		}
	}
	if d.bits != 8 && d.bits != 16 && d.bits != 24 && d.bits != 32 && d.bits != 64 {
		return fmt.Errorf("aiff: unsupported depth %d", d.bits)
	}
	d.blockAlign = d.format.Channels * d.bits / 8
	return nil
}

func (d *aiffDecoder) Format() Format     { return d.format }
func (d *aiffDecoder) TotalFrames() int64 { return d.total }
func (d *aiffDecoder) Close() error       { return d.r.Close() }

func (d *aiffDecoder) SeekTo(frame int64) error {
	if frame < 0 {
		frame = 0
	}
	if frame > d.total {
		frame = d.total
	}
	if _, err := d.r.Seek(d.ssndStart+frame*int64(d.blockAlign), io.SeekStart); err != nil {
		return err
	}
	d.cur = frame
	return nil
}

func (d *aiffDecoder) ReadFrames(dst []float32) (int, error) {
	if d.cur >= d.total {
		return 0, io.EOF
	}
	ch := d.format.Channels
	wantFrames := len(dst) / ch
	if remain := d.total - d.cur; int64(wantFrames) > remain {
		wantFrames = int(remain)
	}
	if wantFrames == 0 {
		return 0, io.EOF
	}
	need := wantFrames * d.blockAlign
	if cap(d.buf) < need {
		d.buf = make([]byte, need)
	}
	b := d.buf[:need]
	got, err := io.ReadFull(d.r, b)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return 0, err
	}
	frames := got / d.blockAlign
	bps := d.bits / 8
	for i := 0; i < frames; i++ {
		base := i * d.blockAlign
		for c := 0; c < ch; c++ {
			s := b[base+c*bps : base+c*bps+bps]
			if d.littleEnd {
				dst[i*ch+c] = decodeSample(s, d.bits, d.isFloat)
			} else {
				dst[i*ch+c] = decodeSampleBE(s, d.bits, d.isFloat)
			}
		}
	}
	d.cur += int64(frames)
	if frames == 0 {
		return 0, io.EOF
	}
	return frames, nil
}

// decodeSampleBE converts one big-endian PCM sample to float32 in [-1,1].
func decodeSampleBE(b []byte, bits int, isFloat bool) float32 {
	if isFloat {
		switch bits {
		case 32:
			return math.Float32frombits(binary.BigEndian.Uint32(b))
		case 64:
			return float32(math.Float64frombits(binary.BigEndian.Uint64(b)))
		}
		return 0
	}
	switch bits {
	case 8:
		return float32(int8(b[0])) / 128
	case 16:
		return float32(int16(binary.BigEndian.Uint16(b))) / 32768
	case 24:
		v := int32(b[2]) | int32(b[1])<<8 | int32(b[0])<<16
		if v&0x800000 != 0 {
			v |= ^0xFFFFFF
		}
		return float32(v) / 8388608
	case 32:
		return float32(int32(binary.BigEndian.Uint32(b))) / 2147483648
	}
	return 0
}

// extended80ToFloat decodes an 80-bit IEEE 754 extended-precision float (AIFF sample rate).
func extended80ToFloat(b []byte) float64 {
	if len(b) < 10 {
		return 0
	}
	sign := 1.0
	if b[0]&0x80 != 0 {
		sign = -1.0
	}
	exp := int(binary.BigEndian.Uint16(b[0:2]) & 0x7FFF)
	mant := binary.BigEndian.Uint64(b[2:10])
	if exp == 0 && mant == 0 {
		return 0
	}
	return sign * float64(mant) * math.Pow(2, float64(exp-16383-63))
}

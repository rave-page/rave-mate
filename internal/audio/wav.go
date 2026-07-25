package audio

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
)

// wavDecoder reads RIFF/WAVE PCM: int 16/24/32, IEEE float 32/64, WAVE_FORMAT_EXTENSIBLE.
// Seek is O(1): dataStart + frame*blockAlign. No rescan, sample-accurate.
type wavDecoder struct {
	r          io.ReadSeekCloser
	format     Format
	dataStart  int64 // byte offset of the data chunk payload
	total      int64 // frames
	blockAlign int   // bytes per frame (all channels)
	bits       int   // bits per sample
	isFloat    bool
	cur        int64 // current frame
	buf        []byte
}

const (
	wavFmtPCM        = 0x0001
	wavFmtFloat      = 0x0003
	wavFmtExtensible = 0xFFFE
)

func newWAVDecoder(r io.ReadSeekCloser) (Decoder, error) {
	var hdr [12]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		_ = r.Close()
		return nil, fmt.Errorf("wav: header: %w", err)
	}
	if string(hdr[0:4]) != "RIFF" || string(hdr[8:12]) != "WAVE" {
		_ = r.Close()
		return nil, fmt.Errorf("wav: not a RIFF/WAVE file")
	}
	d := &wavDecoder{r: r}
	off := int64(12)
	var haveFmt, haveData bool
	for {
		var ch [8]byte
		if _, err := io.ReadFull(r, ch[:]); err != nil {
			break // no more chunks
		}
		off += 8
		id := string(ch[0:4])
		sz := int64(binary.LittleEndian.Uint32(ch[4:8]))
		switch id {
		case "fmt ":
			body := make([]byte, sz)
			if _, err := io.ReadFull(r, body); err != nil {
				_ = r.Close()
				return nil, fmt.Errorf("wav: fmt chunk: %w", err)
			}
			if err := d.parseFmt(body); err != nil {
				_ = r.Close()
				return nil, err
			}
			haveFmt = true
			off += sz
		case "data":
			d.dataStart = off
			if !haveFmt || d.blockAlign == 0 {
				_ = r.Close()
				return nil, fmt.Errorf("wav: data before fmt")
			}
			d.total = sz / int64(d.blockAlign)
			haveData = true
			// Don't read the payload — seek past it to look for later chunks only if we must;
			// we have everything, so stop scanning.
			if _, err := r.Seek(d.dataStart, io.SeekStart); err != nil {
				_ = r.Close()
				return nil, err
			}
			off = d.dataStart
			// data found + fmt found => ready.
		default:
			// skip unknown chunk (RIFF pads odd sizes to even)
			skip := sz + (sz & 1)
			if _, err := r.Seek(skip, io.SeekCurrent); err != nil {
				break
			}
			off += skip
		}
		if haveFmt && haveData {
			break
		}
	}
	if !haveFmt || !haveData {
		_ = r.Close()
		return nil, fmt.Errorf("wav: missing fmt or data chunk")
	}
	if d.format.Channels <= 0 || d.format.SampleRate <= 0 {
		_ = r.Close()
		return nil, fmt.Errorf("wav: bad channels/rate")
	}
	return d, nil
}

func (d *wavDecoder) parseFmt(b []byte) error {
	if len(b) < 16 {
		return fmt.Errorf("wav: fmt too short")
	}
	tag := binary.LittleEndian.Uint16(b[0:2])
	d.format.Channels = int(binary.LittleEndian.Uint16(b[2:4]))
	d.format.SampleRate = int(binary.LittleEndian.Uint32(b[4:8]))
	d.blockAlign = int(binary.LittleEndian.Uint16(b[12:14]))
	d.bits = int(binary.LittleEndian.Uint16(b[14:16]))
	if tag == wavFmtExtensible {
		if len(b) < 40 {
			return fmt.Errorf("wav: extensible fmt too short")
		}
		// SubFormat GUID's first 2 bytes carry the real format tag.
		tag = binary.LittleEndian.Uint16(b[24:26])
	}
	switch tag {
	case wavFmtPCM:
		d.isFloat = false
		if d.bits != 16 && d.bits != 24 && d.bits != 32 && d.bits != 8 {
			return fmt.Errorf("wav: unsupported PCM depth %d", d.bits)
		}
	case wavFmtFloat:
		d.isFloat = true
		if d.bits != 32 && d.bits != 64 {
			return fmt.Errorf("wav: unsupported float depth %d", d.bits)
		}
	default:
		return fmt.Errorf("wav: unsupported format tag 0x%04x (compressed WAV not native — use ffmpeg)", tag)
	}
	if d.blockAlign == 0 {
		d.blockAlign = d.format.Channels * d.bits / 8
	}
	// blockAlign may exceed the frame size (padded frames) but never undercut it —
	// decode would index past the block (crafted-file crash guard; Zig mirrors).
	if d.blockAlign < d.format.Channels*d.bits/8 {
		return fmt.Errorf("wav: block align %d smaller than frame size", d.blockAlign)
	}
	return nil
}

func (d *wavDecoder) Format() Format     { return d.format }
func (d *wavDecoder) TotalFrames() int64 { return d.total }
func (d *wavDecoder) Close() error       { return d.r.Close() }

func (d *wavDecoder) SeekTo(frame int64) error {
	if frame < 0 {
		frame = 0
	}
	if frame > d.total {
		frame = d.total
	}
	if _, err := d.r.Seek(d.dataStart+frame*int64(d.blockAlign), io.SeekStart); err != nil {
		return err
	}
	d.cur = frame
	return nil
}

func (d *wavDecoder) ReadFrames(dst []float32) (int, error) {
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
	pcmToF32(b, frames, ch, d.blockAlign, d.bits, d.isFloat, false, dst)
	d.cur += int64(frames)
	if frames == 0 {
		return 0, io.EOF
	}
	return frames, nil
}

// decodeSample converts one little-endian PCM sample to float32 in [-1,1].
func decodeSample(b []byte, bits int, isFloat bool) float32 {
	if isFloat {
		switch bits {
		case 32:
			return math.Float32frombits(binary.LittleEndian.Uint32(b))
		case 64:
			return float32(math.Float64frombits(binary.LittleEndian.Uint64(b)))
		}
		return 0
	}
	switch bits {
	case 8: // unsigned
		return (float32(b[0]) - 128) / 128
	case 16:
		return float32(int16(binary.LittleEndian.Uint16(b))) / 32768
	case 24:
		v := int32(b[0]) | int32(b[1])<<8 | int32(b[2])<<16
		if v&0x800000 != 0 {
			v |= ^0xFFFFFF // sign-extend
		}
		return float32(v) / 8388608
	case 32:
		return float32(int32(binary.LittleEndian.Uint32(b))) / 2147483648
	}
	return 0
}

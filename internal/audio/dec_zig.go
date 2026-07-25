package audio

import (
	"fmt"
	"io"

	"rave.page/mate/internal/zignative"
)

// zigPCMDecoder wraps the Zig-side WAV/AIFF container decoder (rz_wavdec/
// rz_aiffdec): Go owns file I/O; Zig owns chunk parsing, frame math and
// PCM→f32. Dispatched from Open when zignative.Available(); the Go decoders
// stay as fallback + golden reference (parity gate: dec_zig_test.go).
type zigPCMDecoder struct {
	r      io.ReadSeekCloser
	h      *zignative.PCMDec
	format Format
	total  int64
	buf    []byte // read buffer, grown on demand (Go-side GC amortization)
}

// maxZigHeaderChunk caps one header-chunk window; larger → Zig open fails and
// Open falls back to the Go decoder (which streams it Go-side).
const maxZigHeaderChunk = 16 << 20

// openZig tries the Zig decoder when linked. (nil, nil) = fall through to the
// Go decoder: not linked, or Zig open failed (f rewound; the Go decoder is
// authoritative on accept/reject and error text).
func openZig(f io.ReadSeekCloser, h *zignative.PCMDec) (Decoder, error) {
	if !zignative.Available() {
		return nil, nil
	}
	d, err := newZigPCMDecoder(f, h)
	if err == nil {
		return d, nil
	}
	if _, serr := f.Seek(0, io.SeekStart); serr != nil {
		_ = f.Close()
		return nil, serr
	}
	return nil, nil
}

// newZigPCMDecoder parses the header via the Zig feed protocol. On error the
// reader is NOT closed (caller rewinds and falls back to the Go decoder).
func newZigPCMDecoder(r io.ReadSeekCloser, h *zignative.PCMDec) (Decoder, error) {
	if h == nil {
		return nil, fmt.Errorf("audio: zig decoder unavailable")
	}
	ok := false
	defer func() {
		if !ok {
			h.Free()
		}
	}()
	var buf []byte
	st, off, ln := h.Feed(nil)
	for st == zignative.DecNeed {
		if ln > maxZigHeaderChunk {
			return nil, fmt.Errorf("audio: zig decode: header chunk too large")
		}
		if off > 1<<62 {
			return nil, fmt.Errorf("audio: zig decode: bad chunk offset")
		}
		if _, err := r.Seek(int64(off), io.SeekStart); err != nil {
			return nil, err
		}
		if uint64(cap(buf)) < ln {
			buf = make([]byte, ln)
		}
		b := buf[:ln]
		got, err := io.ReadFull(r, b)
		if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
			return nil, err
		}
		st, off, ln = h.Feed(b[:got])
	}
	if st != zignative.DecOK {
		return nil, fmt.Errorf("audio: zig decode: malformed header")
	}
	info := h.Info()
	if _, err := r.Seek(int64(info.DataStart), io.SeekStart); err != nil {
		return nil, err
	}
	d := &zigPCMDecoder{
		r:      r,
		h:      h,
		format: Format{SampleRate: int(info.SampleRate), Channels: info.Channels},
		total:  info.TotalFrames,
	}
	ok = true
	return d, nil
}

func (d *zigPCMDecoder) Format() Format     { return d.format }
func (d *zigPCMDecoder) TotalFrames() int64 { return d.total }

func (d *zigPCMDecoder) Close() error {
	d.h.Free()
	return d.r.Close()
}

func (d *zigPCMDecoder) SeekTo(frame int64) error {
	clamped, off := d.h.SeekOff(frame)
	if _, err := d.r.Seek(int64(off), io.SeekStart); err != nil {
		return err
	}
	d.h.SetPos(clamped)
	return nil
}

func (d *zigPCMDecoder) ReadFrames(dst []float32) (int, error) {
	want, need := d.h.Plan(len(dst))
	if want == 0 {
		return 0, io.EOF
	}
	if cap(d.buf) < need {
		d.buf = make([]byte, need)
	}
	b := d.buf[:need]
	got, err := io.ReadFull(d.r, b)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return 0, err
	}
	n := d.h.Decode(b[:got], dst)
	if n == 0 {
		return 0, io.EOF
	}
	return n, nil
}

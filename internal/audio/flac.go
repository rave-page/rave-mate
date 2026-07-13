package audio

import (
	"io"

	"github.com/mewkiz/flac"
)

// flacDecoder wraps mewkiz/flac. NewSeek gives sample-accurate seek via the file's SEEKTABLE, or
// an index it builds on demand for seektable-less files (the recorder-capture case that froze
// beep) — so a seek is O(log n), never a full rescan. Subframe samples are int32 at the stream
// bit depth; scaled to float32 [-1,1].
type flacDecoder struct {
	rc     io.Closer
	st     *flac.Stream
	format Format
	total  int64
	scale  float32
	cur    int64

	buf []float32 // decoded interleaved f32 from the current frame
	off int       // consumed offset into buf (samples)
}

func newFLACDecoder(r io.ReadSeekCloser) (Decoder, error) {
	st, err := flac.NewSeek(r)
	if err != nil {
		_ = r.Close()
		return nil, err
	}
	info := st.Info
	d := &flacDecoder{
		rc:     r,
		st:     st,
		format: Format{SampleRate: int(info.SampleRate), Channels: int(info.NChannels)},
		total:  int64(info.NSamples),
		scale:  1.0 / float32(int64(1)<<(info.BitsPerSample-1)),
	}
	if d.total <= 0 {
		d.total = -1
	}
	return d, nil
}

func (d *flacDecoder) Format() Format     { return d.format }
func (d *flacDecoder) TotalFrames() int64 { return d.total }
func (d *flacDecoder) Close() error       { return d.rc.Close() }

// fill decodes the next FLAC frame into buf (interleaved f32). Returns io.EOF at stream end.
func (d *flacDecoder) fill() error {
	f, err := d.st.ParseNext()
	if err != nil {
		return err
	}
	ch := d.format.Channels
	bs := int(f.BlockSize)
	if cap(d.buf) < bs*ch {
		d.buf = make([]float32, bs*ch)
	}
	d.buf = d.buf[:bs*ch]
	for i := 0; i < bs; i++ {
		for c := 0; c < ch; c++ {
			d.buf[i*ch+c] = float32(f.Subframes[c].Samples[i]) * d.scale
		}
	}
	d.off = 0
	return nil
}

func (d *flacDecoder) ReadFrames(dst []float32) (int, error) {
	ch := d.format.Channels
	want := len(dst) / ch
	wrote := 0
	for wrote < want {
		if d.off >= len(d.buf) {
			if err := d.fill(); err != nil {
				if err == io.EOF {
					break
				}
				if wrote > 0 {
					break
				}
				return 0, err
			}
		}
		avail := (len(d.buf) - d.off) / ch
		take := want - wrote
		if take > avail {
			take = avail
		}
		copy(dst[wrote*ch:], d.buf[d.off:d.off+take*ch])
		d.off += take * ch
		wrote += take
	}
	d.cur += int64(wrote)
	if wrote == 0 {
		return 0, io.EOF
	}
	return wrote, nil
}

func (d *flacDecoder) SeekTo(frame int64) error {
	if frame < 0 {
		frame = 0
	}
	if d.total > 0 && frame > d.total {
		frame = d.total
	}
	got, err := d.st.Seek(uint64(frame))
	if err != nil {
		return err
	}
	d.buf, d.off, d.cur = d.buf[:0], 0, int64(got)
	// mewkiz lands on the frame boundary <= target; decode-discard forward to the exact sample.
	ch := d.format.Channels
	for d.cur < frame {
		if d.off >= len(d.buf) {
			if err := d.fill(); err != nil {
				break // EOF before target (shouldn't happen for in-range frames)
			}
		}
		avail := (len(d.buf) - d.off) / ch
		skip := int(frame - d.cur)
		if skip > avail {
			skip = avail
		}
		d.off += skip * ch
		d.cur += int64(skip)
	}
	return nil
}

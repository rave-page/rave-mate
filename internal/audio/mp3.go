package audio

import (
	"encoding/binary"
	"io"

	"github.com/hajimehoshi/go-mp3"
)

// mp3Decoder wraps hajimehoshi/go-mp3. go-mp3 always emits S16LE stereo at the file's rate and
// builds its own frame index, so Seek is byte-accurate in the output PCM stream (offset =
// frame*4) with no full rescan. Output is always stereo (mono is duplicated by the lib).
type mp3Decoder struct {
	rc      io.Closer
	dec     *mp3.Decoder
	format  Format
	total   int64
	cur     int64
	scratch []byte
}

func newMP3Decoder(r io.ReadSeekCloser) (Decoder, error) {
	dec, err := mp3.NewDecoder(r) // needs a ReadSeeker for Length()/Seek (os.File is)
	if err != nil {
		_ = r.Close()
		return nil, err
	}
	d := &mp3Decoder{
		rc:     r,
		dec:    dec,
		format: Format{SampleRate: dec.SampleRate(), Channels: 2},
	}
	if l := dec.Length(); l > 0 {
		d.total = l / 4 // 4 bytes/frame (S16LE stereo)
	} else {
		d.total = -1
	}
	return d, nil
}

func (d *mp3Decoder) Format() Format     { return d.format }
func (d *mp3Decoder) TotalFrames() int64 { return d.total }
func (d *mp3Decoder) Close() error       { return d.rc.Close() }

func (d *mp3Decoder) ReadFrames(dst []float32) (int, error) {
	want := len(dst) / 2
	if want == 0 {
		return 0, nil
	}
	need := want * 4
	if cap(d.scratch) < need {
		d.scratch = make([]byte, need)
	}
	b := d.scratch[:need]
	got, err := io.ReadFull(d.dec, b)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return 0, err
	}
	frames := got / 4
	for i := 0; i < frames; i++ {
		l := int16(binary.LittleEndian.Uint16(b[i*4:]))
		rr := int16(binary.LittleEndian.Uint16(b[i*4+2:]))
		dst[i*2] = float32(l) / 32768
		dst[i*2+1] = float32(rr) / 32768
	}
	d.cur += int64(frames)
	if frames == 0 {
		return 0, io.EOF
	}
	return frames, nil
}

func (d *mp3Decoder) SeekTo(frame int64) error {
	if frame < 0 {
		frame = 0
	}
	if d.total > 0 && frame > d.total {
		frame = d.total
	}
	if _, err := d.dec.Seek(frame*4, io.SeekStart); err != nil {
		return err
	}
	d.cur = frame
	return nil
}

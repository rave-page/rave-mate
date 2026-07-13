package audio

import (
	"io"

	"github.com/jfreymuth/oggvorbis"
)

// vorbisDecoder wraps jfreymuth/oggvorbis. It already yields interleaved float32, and SetPosition
// gives sample-accurate seek via the Ogg page/granule index (no full rescan).
type vorbisDecoder struct {
	rc     io.Closer
	r      *oggvorbis.Reader
	format Format
	total  int64
}

func newVorbisDecoder(r io.ReadSeekCloser) (Decoder, error) {
	vr, err := oggvorbis.NewReader(r)
	if err != nil {
		_ = r.Close()
		return nil, err
	}
	d := &vorbisDecoder{
		rc:     r,
		r:      vr,
		format: Format{SampleRate: vr.SampleRate(), Channels: vr.Channels()},
		total:  vr.Length(),
	}
	if d.total <= 0 {
		d.total = -1
	}
	return d, nil
}

func (d *vorbisDecoder) Format() Format     { return d.format }
func (d *vorbisDecoder) TotalFrames() int64 { return d.total }
func (d *vorbisDecoder) Close() error       { return d.rc.Close() }

func (d *vorbisDecoder) ReadFrames(dst []float32) (int, error) {
	// oggvorbis.Read fills interleaved f32 and returns SAMPLES (not frames); len(dst) must be a
	// multiple of Channels, which the engine guarantees.
	n, err := d.r.Read(dst)
	frames := n / d.format.Channels
	if err == io.EOF && frames == 0 {
		return 0, io.EOF
	}
	if err != nil && err != io.EOF {
		return frames, err
	}
	if frames == 0 {
		return 0, io.EOF
	}
	return frames, nil
}

func (d *vorbisDecoder) SeekTo(frame int64) error {
	if frame < 0 {
		frame = 0
	}
	if d.total > 0 && frame > d.total {
		frame = d.total
	}
	return d.r.SetPosition(frame)
}

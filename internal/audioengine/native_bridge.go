package audioengine

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/gopxl/beep/v2"

	"rave.page/mate/internal/audio"
)

// Native bridge: when features.player.nativeDecode is on, the player child routes EVERYTHING
// through the native audio.Engine's single oto context (oto.NewContext is once-per-process, so
// beep's 100ms speaker must never init in that mode). Formats the native path can't decode
// (AAC/M4A/opus/… — audio.ErrUnsupported, codec-license decision pending) still decode through
// ffmpeg, wrapped as an audio.Decoder so they share the same low-latency output + transport.

// FFmpegPlayable reports whether the ffmpeg fallback can serve this extension (used by the
// native-mode IsPlayable check in the player child).
func FFmpegPlayable(path string) bool {
	ext := lowerExt(path)
	return (playableExt[ext] || ffmpegPlayable[ext]) && ffmpegAvailable()
}

func lowerExt(path string) string { return strings.ToLower(filepath.Ext(path)) }

// NewFFmpegDecoder opens path via the ffmpeg subprocess decode (48k stereo f32) presented as an
// audio.Decoder. SeekTo respawns ffmpeg at -ss (~230ms) — acceptable for the AAC-only fallback;
// native formats never take this path.
func NewFFmpegDecoder(path string) (audio.Decoder, error) {
	if !ffmpegAvailable() {
		return nil, fmt.Errorf("ffmpeg not found (install it from Settings → Transcode)")
	}
	s, format, err := newFFmpegSource(path, 0)
	if err != nil {
		return nil, err
	}
	return &beepDecoder{s: s, format: audio.Format{SampleRate: int(format.SampleRate), Channels: format.NumChannels}}, nil
}

// beepDecoder adapts a beep.StreamSeekCloser (the ffmpegSource) to audio.Decoder.
type beepDecoder struct {
	s      beep.StreamSeekCloser
	format audio.Format
	buf    [][2]float64 // transient stream scratch (bounded by the caller's dst size)
}

func (d *beepDecoder) Format() audio.Format { return d.format }
func (d *beepDecoder) TotalFrames() int64   { return int64(d.s.Len()) }
func (d *beepDecoder) Close() error         { return d.s.Close() }

func (d *beepDecoder) ReadFrames(dst []float32) (int, error) {
	want := len(dst) / d.format.Channels
	if want == 0 {
		return 0, nil
	}
	if cap(d.buf) < want {
		d.buf = make([][2]float64, want)
	}
	n, ok := d.s.Stream(d.buf[:want])
	for i := 0; i < n; i++ {
		dst[i*2] = float32(d.buf[i][0])
		dst[i*2+1] = float32(d.buf[i][1])
	}
	if !ok && n == 0 {
		return 0, io.EOF
	}
	return n, nil
}

func (d *beepDecoder) SeekTo(frame int64) error {
	if es, ok := d.s.(interface{ SeekExplicit(p int) error }); ok {
		return es.SeekExplicit(int(frame))
	}
	return d.s.Seek(int(frame))
}

package webcam

import (
	"image"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/videoshare"
)

// spoutSink presents captured frames as a named local Spout sender - a medialink.Sink over the
// existing videoshare.FrameSender (reuses the spout shim; build tag `spout` selects the real
// backend, the default build errors on open so the feature degrades with a clear reason).
type spoutSink struct {
	fs   videoshare.FrameSender
	w, h int
}

// newSpoutSink opens the named video-share sender. Errors when no backend is compiled in /
// SpoutLibrary.dll is absent - the caller surfaces the reason string.
func newSpoutSink(log *logbus.Bus, name string, w, h int) (*spoutSink, error) {
	fs, err := videoshare.NewFrameSender(log, name)
	if err != nil {
		return nil, err
	}
	return &spoutSink{fs: fs, w: w, h: h}, nil
}

// Write implements medialink.Sink: wrap the RGBA payload (no copy - capture hands off fresh
// buffers) and publish. Undersized/foreign frames are skipped, never fatal.
func (s *spoutSink) Write(f *medialink.Frame) error {
	if f.Kind != medialink.KindVideo || len(f.Payload) < s.w*s.h*bytesPerPixel {
		return nil
	}
	return s.fs.Send(&image.NRGBA{Pix: f.Payload, Stride: s.w * bytesPerPixel,
		Rect: image.Rect(0, 0, s.w, s.h)})
}

// Close implements medialink.Sink.
func (s *spoutSink) Close() error { s.fs.Close(); return nil }

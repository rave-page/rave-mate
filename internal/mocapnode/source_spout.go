package mocapnode

// source_spout.go - the DIRECT camera-node ingest: VRChat Stream Camera (Spout2 out) ->
// videoshare.FrameReceiver -> Frames. No OBS, no virtual camera, no ffmpeg - a GPU shared
// texture pulled in-process, the cheapest and cleanest capture path (contract 8b camera node).
// The videoshare backend is build-tagged (SPOUT=1); without it NewFrameReceiver errors and
// this source fails fatally with that error - callers fall back to dshow/desktop.

import (
	"context"
	"fmt"
	"image"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/videoshare"
	"rave.page/mate/internal/zignative"
)

// SpoutSource pulls frames from a named local Spout sender (e.g. the VRChat camera).
// Sender discovery: videoshare.ListSenders(); empty name = error listing the candidates.
type SpoutSource struct {
	Log    *logbus.Bus
	Sender string // exact sender name, e.g. "VRChat-StreamCamera"
}

// Frames pulls until ctx cancel. The receiver channel is newest-wins already, so no
// additional dropping is needed here; conversion reuses one buffer (Frame contract).
func (s *SpoutSource) Frames(ctx context.Context, emit func(Frame)) error {
	if s.Sender == "" {
		return fmt.Errorf("mocapnode: spout sender name required; senders: %v", videoshare.ListSenders())
	}
	log := s.Log
	if log == nil {
		log = logbus.New(64) // receiver backends log through the bus; never hand them nil
	}
	rx, err := videoshare.NewFrameReceiver(log, s.Sender)
	if err != nil {
		return fmt.Errorf("mocapnode: spout receiver: %w", err)
	}
	defer rx.Close()

	var buf []byte
	for {
		select {
		case <-ctx.Done():
			return nil
		case img, ok := <-rx.Frames():
			if !ok {
				return fmt.Errorf("mocapnode: spout sender %q closed", s.Sender)
			}
			f, n := frameFromNRGBA(img, &buf)
			// The receiver hands over a POOLED readback buffer; frameFromNRGBA copied what it
			// needs into our own RGB24 buffer, so give it straight back (without this the pool
			// starves and every readback allocates a fresh full frame).
			videoshare.PutPix(img.Pix)
			if !n {
				continue
			}
			emit(f)
		}
	}
}

// frameFromNRGBA adapts an *image.NRGBA into a Frame without copying when the layout already
// matches (NRGBA is RGBA order, 4 bpp - we expose it as BGRA-format only if swapped, so copy
// into an RGB24 buffer instead: predictable for the sampler and drops the alpha lane).
func frameFromNRGBA(img *image.NRGBA, buf *[]byte) (Frame, bool) {
	if img == nil {
		return Frame{}, false
	}
	w, h := img.Rect.Dx(), img.Rect.Dy()
	if w <= 0 || h <= 0 {
		return Frame{}, false
	}
	need := w * h * 3
	if cap(*buf) < need {
		*buf = make([]byte, need)
	}
	dst := (*buf)[:need]
	if !(zignative.Available() && zignative.RGBAToRGB24(img.Pix, img.Stride, w, h, dst)) {
		rgbaToRGB24Go(img.Pix, img.Stride, w, h, dst)
	}
	return Frame{Pix: dst, W: w, H: h, Stride: w * 3, Fmt: FmtRGB24}, true
}

// rgbaToRGB24Go is the pure-Go strided RGBA→RGB24 copy (fallback + golden reference
// for the rz_rgba_to_rgb24 kernel).
func rgbaToRGB24Go(pix []byte, stride, w, h int, dst []byte) {
	for y := 0; y < h; y++ {
		src := pix[y*stride : y*stride+w*4]
		row := dst[y*w*3 : (y+1)*w*3]
		for x := 0; x < w; x++ {
			row[x*3+0] = src[x*4+0]
			row[x*3+1] = src[x*4+1]
			row[x*3+2] = src[x*4+2]
		}
	}
}

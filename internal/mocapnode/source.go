package mocapnode

// source.go - the capture Source seam + the Frame it delivers. Sources deliver raw RGB/BGRA
// bytes at a target fps; the node never touches a video codec (raw pipes only, same noise model
// as the panel contract).

import "context"

// PixFmt says how Frame.Pix packs a pixel.
type PixFmt int

const (
	FmtRGB24 PixFmt = iota // 3 bytes/px: R G B (decoded images, raw .rgb fixtures)
	FmtBGRA                // 4 bytes/px: B G R A, A ignored (ffmpeg -pix_fmt bgra rawvideo)
)

// Bpp returns bytes per pixel.
func (f PixFmt) Bpp() int {
	if f == FmtBGRA {
		return 4
	}
	return 3
}

// Frame is one captured frame. Pix holds H rows of Stride bytes; a Frame (and its Pix) is only
// valid for the duration of the emit call that delivered it - sources reuse buffers.
type Frame struct {
	Pix    []byte
	W, H   int
	Stride int // bytes per row
	Fmt    PixFmt
}

// RGB reads the pixel at (x,y). Caller keeps coordinates in bounds.
func (f *Frame) RGB(x, y int) (r, g, b uint8) {
	i := y*f.Stride + x*f.Fmt.Bpp()
	if f.Fmt == FmtBGRA {
		return f.Pix[i+2], f.Pix[i+1], f.Pix[i]
	}
	return f.Pix[i], f.Pix[i+1], f.Pix[i+2]
}

// Source delivers captured frames. Frames blocks, calling emit once per frame, until ctx is
// cancelled (returns nil) or the source fails fatally (bad config, unreadable file). Transient
// capture failures are the source's job to ride out (ffmpeg sources restart with backoff).
type Source interface {
	Frames(ctx context.Context, emit func(Frame)) error
}

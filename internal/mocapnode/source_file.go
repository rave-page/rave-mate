package mocapnode

// source_file.go - FileSource: PNG (single frame, repeated at fps) or raw .rgb (tightly packed
// RGB24 frames, looped). Tests + replay of captured fixtures; no ffmpeg in the path.

import (
	"context"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FileSource replays a fixture file as a capture stream.
type FileSource struct {
	Path  string // .png (any decodable size) or raw .rgb (tightly packed RGB24 frames)
	W, H  int    // raw .rgb frame geometry (required); ignored for PNG
	FPS   int    // emit rate; <=0 = 30
	Count int    // total frames to emit; <=0 = until ctx cancel
}

// Frames implements Source: loads the file once, then emits frames at FPS (a PNG repeats its
// single frame; a multi-frame .rgb loops).
func (s *FileSource) Frames(ctx context.Context, emit func(Frame)) error {
	frames, err := s.load()
	if err != nil {
		return err
	}
	fps := s.FPS
	if fps <= 0 {
		fps = 30
	}
	tick := time.NewTicker(time.Second / time.Duration(fps))
	defer tick.Stop()
	for n := 0; s.Count <= 0 || n < s.Count; n++ {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			emit(frames[n%len(frames)])
		}
	}
	return nil
}

// load reads the fixture into memory (fixtures are small; replay never re-hits the disk).
func (s *FileSource) load() ([]Frame, error) {
	switch strings.ToLower(filepath.Ext(s.Path)) {
	case ".png":
		f, err := os.Open(s.Path)
		if err != nil {
			return nil, err
		}
		defer func() { _ = f.Close() }()
		img, err := png.Decode(f)
		if err != nil {
			return nil, fmt.Errorf("mocapnode: png %s: %w", s.Path, err)
		}
		return []Frame{imageToFrame(img)}, nil
	case ".rgb":
		if s.W <= 0 || s.H <= 0 {
			return nil, fmt.Errorf("mocapnode: raw .rgb needs W/H")
		}
		raw, err := os.ReadFile(s.Path)
		if err != nil {
			return nil, err
		}
		size := s.W * s.H * 3
		if len(raw) == 0 || len(raw)%size != 0 {
			return nil, fmt.Errorf("mocapnode: %s: %d bytes not a multiple of %dx%dx3", s.Path, len(raw), s.W, s.H)
		}
		frames := make([]Frame, 0, len(raw)/size)
		for off := 0; off < len(raw); off += size {
			frames = append(frames, Frame{Pix: raw[off : off+size], W: s.W, H: s.H, Stride: s.W * 3, Fmt: FmtRGB24})
		}
		return frames, nil
	default:
		return nil, fmt.Errorf("mocapnode: unsupported fixture %s (want .png or .rgb)", s.Path)
	}
}

// imageToFrame converts a decoded image to a tightly packed RGB24 frame.
func imageToFrame(img image.Image) Frame {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	pix := make([]byte, w*h*3)
	i := 0
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			pix[i], pix[i+1], pix[i+2] = uint8(r>>8), uint8(g>>8), uint8(bl>>8)
			i += 3
		}
	}
	return Frame{Pix: pix, W: w, H: h, Stride: w * 3, Fmt: FmtRGB24}
}

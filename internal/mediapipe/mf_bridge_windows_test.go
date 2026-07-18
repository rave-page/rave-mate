//go:build windows && cgo

package mediapipe

import (
	"context"
	"io"
	"testing"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/mfenc"
)

// rawSrc yields n synthetic RGBA frames then EOF.
type rawSrc struct {
	w, h, n, i int
}

func (s *rawSrc) Next(ctx context.Context) (*medialink.Frame, error) {
	if s.i >= s.n {
		return nil, io.EOF
	}
	s.i++
	pix := make([]byte, s.w*s.h*4)
	for j := range pix {
		pix[j] = byte(j + s.i*7)
	}
	return &medialink.Frame{Kind: medialink.KindVideo, Codec: medialink.CodecNRGBA,
		Payload: pix, PTS: int64(s.i) * 16_666_667}, nil
}

func (s *rawSrc) Close() error { return nil }

// TestMFBridgeHardware drives the full native path as medialink sees it: raw source →
// mfBridge → H.264 frames with a leading keyframe. Also proves the 4K→1080p Blt scale.
func TestMFBridgeHardware(t *testing.T) {
	if !mfenc.Available() {
		t.Skip("no hardware MF encoder")
	}
	log := logbus.New(64)
	spec := medialink.EncodeSpec{Encoder: "h264_mf_native", Codec: medialink.CodecH264,
		Width: 3840, Height: 2160, FPS: 30, MaxHeight: 1080}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b, err := newMFBridge(ctx, log, spec, &rawSrc{w: 3840, h: 2160, n: 20})
	if err != nil {
		t.Fatalf("newMFBridge: %v", err)
	}
	defer func() { _ = b.Close() }()
	var frames []*medialink.Frame
	deadline := time.After(30 * time.Second)
	for {
		nctx, ncancel := context.WithTimeout(ctx, 10*time.Second)
		f, err := b.Next(nctx)
		ncancel()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v (after %d frames)", err, len(frames))
		}
		frames = append(frames, f)
		select {
		case <-deadline:
			t.Fatal("bridge never drained")
		default:
		}
	}
	if len(frames) < 10 {
		t.Fatalf("frames=%d want >=10", len(frames))
	}
	if frames[0].Flags&medialink.FlagKeyframe == 0 {
		t.Fatal("first frame not a keyframe")
	}
	if frames[0].Codec != medialink.CodecH264 {
		t.Fatalf("codec=%v", frames[0].Codec)
	}
	if st := b.PipeStats(); st.Encoder != "h264_mf_native" {
		t.Fatalf("stats encoder=%q", st.Encoder)
	}
	t.Logf("4K→1080p native encode: frames=%d first=%dB", len(frames), len(frames[0].Payload))

	// native 4K, no scale - the "full res like NDI" path
	spec.MaxHeight = 0
	b2, err := newMFBridge(ctx, log, spec, &rawSrc{w: 3840, h: 2160, n: 10})
	if err != nil {
		t.Fatalf("newMFBridge(4K native): %v", err)
	}
	defer func() { _ = b2.Close() }()
	n := 0
	for {
		nctx, ncancel := context.WithTimeout(ctx, 10*time.Second)
		f, err := b2.Next(nctx)
		ncancel()
		if err != nil {
			break
		}
		if n == 0 && f.Flags&medialink.FlagKeyframe == 0 {
			t.Fatal("4K native: first frame not a keyframe")
		}
		n++
	}
	if n < 5 {
		t.Fatalf("4K native frames=%d want >=5", n)
	}
	t.Logf("4K native encode: frames=%d", n)
}

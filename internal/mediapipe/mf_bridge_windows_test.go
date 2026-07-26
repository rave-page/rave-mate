//go:build windows && cgo

package mediapipe

import (
	"context"
	"io"
	"testing"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/mediatools"
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

// TestNativeOpenFailDegradesToFfmpeg proves the P0 degrade chain BY EXECUTION: an
// EncoderMFNative spec whose native open FAILS (RAVE_MATE_MFENC_OPEN_FAIL - same clean-error
// path a shim/driver failure takes) must come back as a LIVE ffmpeg H.264 encoder, wire codec
// unchanged (the peer was answered H.264) - never an error, never a dead route.
func TestNativeOpenFailDegradesToFfmpeg(t *testing.T) {
	ffmpeg, ok := mediatools.Resolve("ffmpeg")
	if !ok {
		t.Skip("no ffmpeg")
	}
	t.Setenv("RAVE_MATE_MFENC_OPEN_FAIL", "1")
	// Seed the validated probe cache - substitution reads Cached(), never probes.
	probeMu.Lock()
	saved := probeCached
	probeCached = map[string]Caps{ffmpeg: {Encoders: []string{"libx264"}, Validated: true}}
	probeMu.Unlock()
	defer func() { probeMu.Lock(); probeCached = saved; probeMu.Unlock() }()

	log := logbus.New(64)
	encF, _ := Factories(log)
	spec := medialink.EncodeSpec{Encoder: medialink.EncoderMFNative, Codec: medialink.CodecH264,
		Width: 128, Height: 96, FPS: 30, BitrateKbps: 300}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	src, err := encF(ctx, spec, &rawSrc{w: 128, h: 96, n: 60})
	if err != nil {
		t.Fatalf("factory must degrade, got error: %v", err)
	}
	defer func() { _ = src.Close() }()
	pr, ok := src.(medialink.PipelineReporter)
	if !ok {
		t.Fatal("substituted source lacks PipelineReporter")
	}
	if st := pr.PipeStats(); st.Encoder != "libx264" {
		t.Fatalf("substituted encoder=%q want libx264", st.Encoder)
	}
	for got := 0; got < 3; got++ {
		f, err := src.Next(ctx)
		if err != nil {
			t.Fatalf("Next after %d frames: %v", got, err)
		}
		if f.Kind != medialink.KindVideo || f.Codec != medialink.CodecH264 {
			t.Fatalf("frame kind=%v codec=%v want video H.264", f.Kind, f.Codec)
		}
	}
}

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

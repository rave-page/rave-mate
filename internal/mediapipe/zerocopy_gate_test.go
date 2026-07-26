package mediapipe

import (
	"context"
	"io"
	"testing"

	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/mfenc"
)

// zcSrc is a raw-video Source that also exposes a shared texture.
type zcSrc struct {
	h        uint64
	fmt      uint32
	w, hh    int
	name     string
	ok       bool
	nextCall int
}

func (s *zcSrc) Next(context.Context) (*medialink.Frame, error) {
	s.nextCall++
	return nil, io.EOF
}
func (s *zcSrc) Close() error { return nil }
func (s *zcSrc) SharedTexture() (uint64, uint32, int, int, string, bool) {
	return s.h, s.fmt, s.w, s.hh, s.name, s.ok
}

// plainSrc has no shared texture at all (webcam/ffmpeg-fed routes).
type plainSrc struct{}

func (plainSrc) Next(context.Context) (*medialink.Frame, error) { return nil, io.EOF }
func (plainSrc) Close() error                                   { return nil }

// TestZeroCopyGate: every arm of the §7.2 gate. Anything short of ALL conditions holding must
// leave opts.Spout nil, i.e. the route runs today's readback path byte for byte.
func TestZeroCopyGate(t *testing.T) {
	orig := ZeroCopyCapture
	defer func() { ZeroCopyCapture = orig }()
	spec := medialink.EncodeSpec{Width: 1920, Height: 1080}
	good := func() *zcSrc {
		return &zcSrc{h: 0x1234, fmt: 87, w: 1920, hh: 1080, name: "OBS", ok: true}
	}

	cases := []struct {
		name    string
		flag    bool
		src     medialink.Source
		wantZC  bool
		wantDwn int
	}{
		{"flag off: never requested", false, good(), false, 0},
		{"no shared texture on the source type", true, plainSrc{}, false, 0},
		{"source says no texture", true, &zcSrc{ok: false}, false, 0},
		{"zero handle (DX9 / memoryshare sender)", true, &zcSrc{h: 0, fmt: 87, w: 1920, hh: 1080, name: "x", ok: true}, false, 0},
		{"geometry moved under us", true, &zcSrc{h: 0x1234, fmt: 87, w: 1280, hh: 720, name: "x", ok: true}, false, 1},
		{"all conditions hold", true, good(), true, 0},
	}
	for _, c := range cases {
		ZeroCopyCapture = func() bool { return c.flag }
		opts := mfenc.ProcOpts{}
		zc, dwn := zeroCopyOpts(&opts, spec, c.src)
		if zc != c.wantZC {
			t.Errorf("%s: zeroCopy=%v want %v", c.name, zc, c.wantZC)
		}
		if dwn != c.wantDwn {
			t.Errorf("%s: downgrades=%d want %d", c.name, dwn, c.wantDwn)
		}
		if (opts.Spout != nil) != c.wantZC {
			t.Errorf("%s: opts.Spout set=%v want %v", c.name, opts.Spout != nil, c.wantZC)
		}
		if zc {
			// The resolver must re-read the source, not cache the open-time scalars.
			h, f, w, hh, ok := opts.Spout.Resolve()
			if !ok || h != 0x1234 || f != 87 || w != 1920 || hh != 1080 {
				t.Errorf("%s: resolver returned %#x %d %dx%d %v", c.name, h, f, w, hh, ok)
			}
			if opts.Spout.Name != "OBS" {
				t.Errorf("%s: sender name = %q", c.name, opts.Spout.Name)
			}
		}
	}
}

// TestZeroCopyGateNeverPulls: resolving the shared texture must never pull a frame - an eager
// Next here would start the readback the increment exists to remove.
func TestZeroCopyGateNeverPulls(t *testing.T) {
	orig := ZeroCopyCapture
	defer func() { ZeroCopyCapture = orig }()
	ZeroCopyCapture = func() bool { return true }
	src := &zcSrc{h: 0x99, fmt: 87, w: 640, hh: 480, name: "s", ok: true}
	opts := mfenc.ProcOpts{}
	if zc, _ := zeroCopyOpts(&opts, medialink.EncodeSpec{Width: 640, Height: 480}, src); !zc {
		t.Fatal("gate refused a valid source")
	}
	_, _, _, _, _ = opts.Spout.Resolve()
	if src.nextCall != 0 {
		t.Fatalf("the zero-copy gate called Next %d time(s) - that is a readback", src.nextCall)
	}
}

// TestCaptureLabel keeps the log wording honest (it is the field's only quick answer to "which
// path did this route actually take?").
func TestCaptureLabel(t *testing.T) {
	if captureLabel(true) != "zerocopy" || captureLabel(false) != "readback" {
		t.Fatal("capture label wording changed")
	}
}

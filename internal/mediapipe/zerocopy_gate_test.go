package mediapipe

import (
	"context"
	"io"
	"strings"
	"testing"

	"rave.page/mate/internal/encoderscan"
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

// TestZeroCopyGate: every arm of the §7.2 gate, which is also zigmedia inc-5 promotion gate 1 (the
// policy must be decided PER SOURCE and never assumed). Anything short of ALL conditions holding
// must leave opts.Spout nil, i.e. the route runs the readback path byte for byte.
//
// `applicable` is the half that matters once zero-copy is the DEFAULT: it separates "this source
// could never have done zero-copy" (a webcam - silent, uncounted, the readback is the only path
// that ever existed) from "this source could have and did not" (one WARN + a counted downgrade, so
// a rig that always falls back is visible instead of mysteriously slow).
func TestZeroCopyGate(t *testing.T) {
	orig := ZeroCopyCapture
	defer func() { ZeroCopyCapture = orig }()
	spec := medialink.EncodeSpec{Width: 1920, Height: 1080}
	good := func() *zcSrc {
		return &zcSrc{h: 0x1234, fmt: 87, w: 1920, hh: 1080, name: "OBS", ok: true}
	}

	cases := []struct {
		name      string
		flag      bool
		src       medialink.Source
		wantZC    bool
		wantAppl  bool
		reasonHas string
	}{
		{"flag off: never requested", false, good(), false, true, "disabled"},
		{"webcam / DirectShow: not applicable at all", true, plainSrc{}, false, false, "no GPU shared texture"},
		{"webcam with the flag OFF is still not applicable", false, plainSrc{}, false, false, "no GPU shared texture"},
		{"source says no texture", true, &zcSrc{ok: false}, false, true, "no DX11 shared texture"},
		{"zero handle (DX9 / memoryshare sender)", true, &zcSrc{h: 0, fmt: 87, w: 1920, hh: 1080, name: "x", ok: true}, false, true, "no DX11 shared texture"},
		{"geometry moved under us", true, &zcSrc{h: 0x1234, fmt: 87, w: 1280, hh: 720, name: "x", ok: true}, false, true, "resized"},
		{"all conditions hold", true, good(), true, true, ""},
	}
	for _, c := range cases {
		ZeroCopyCapture = func() bool { return c.flag }
		opts := mfenc.ProcOpts{}
		v := zeroCopyOpts(&opts, spec, c.src)
		if v.request != c.wantZC {
			t.Errorf("%s: request=%v want %v", c.name, v.request, c.wantZC)
		}
		if v.applicable != c.wantAppl {
			t.Errorf("%s: applicable=%v want %v (this decides whether it is logged + counted as a downgrade)",
				c.name, v.applicable, c.wantAppl)
		}
		if c.reasonHas != "" && !strings.Contains(v.reason, c.reasonHas) {
			t.Errorf("%s: reason %q does not name %q", c.name, v.reason, c.reasonHas)
		}
		if c.wantZC && v.reason != "" {
			t.Errorf("%s: a granted request carries reason %q", c.name, v.reason)
		}
		if (opts.Spout != nil) != c.wantZC {
			t.Errorf("%s: opts.Spout set=%v want %v", c.name, opts.Spout != nil, c.wantZC)
		}
		if v.request {
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

// TestZeroCopyPolicyIsPerSource is promotion gate 1 stated as one assertion: with the flag ON (the
// inc-5 default), a Spout source and a webcam source resolved by the SAME process take different
// paths. A policy derived from the flag, the config or the peer's advert instead of from the source
// itself would give both the same answer - and handing a webcam route a zero-copy request is a
// black route, because there is no texture behind it.
func TestZeroCopyPolicyIsPerSource(t *testing.T) {
	orig := ZeroCopyCapture
	defer func() { ZeroCopyCapture = orig }()
	ZeroCopyCapture = func() bool { return true }
	spec := medialink.EncodeSpec{Width: 1920, Height: 1080}

	var spoutOpts, camOpts mfenc.ProcOpts
	spoutV := zeroCopyOpts(&spoutOpts, spec, &zcSrc{h: 0xdead, fmt: 87, w: 1920, hh: 1080, name: "OBS", ok: true})
	camV := zeroCopyOpts(&camOpts, spec, plainSrc{})

	if !spoutV.request || spoutOpts.Spout == nil {
		t.Fatal("the Spout source was not granted zero-copy with the flag on")
	}
	if camV.request || camOpts.Spout != nil {
		t.Fatal("the webcam source was handed a zero-copy request - there is no texture behind it")
	}
	if camV.applicable {
		t.Error("a webcam route counts as a zero-copy DOWNGRADE - it would log a warning on every camera route")
	}
	// And the flag alone must not be able to explain the difference: same flag, same spec, same
	// call, two answers.
	if spoutV.request == camV.request {
		t.Fatal("both sources got the same verdict: the policy is not per-source")
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
	if v := zeroCopyOpts(&opts, medialink.EncodeSpec{Width: 640, Height: 480}, src); !v.request {
		t.Fatalf("gate refused a valid source: %s", v.reason)
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

// ── increment 3: adapter-affinity candidates (risk R7) ──

func withAffinityGate(t *testing.T, on bool) {
	t.Helper()
	prev := ZeroCopyAffinity
	ZeroCopyAffinity = func() bool { return on }
	t.Cleanup(func() { ZeroCopyAffinity = prev })
}

// TestAffinityCandidatesNoneWhenGateOff: flag off = mfenc gets no candidates = today's downgrade.
func TestAffinityCandidatesNoneWhenGateOff(t *testing.T) {
	withAffinityGate(t, false)
	if got := affinityCandidates(medialink.EncodeSpec{}); got != nil {
		t.Fatalf("candidates with the gate OFF: %#x", got)
	}
}

// TestAffinityCandidatesNoneWhenDevicePinned is R7's hard rule at the policy layer: a resolved
// encode device is USER/GOVERNOR policy and must never be overridden by an optimisation.
func TestAffinityCandidatesNoneWhenDevicePinned(t *testing.T) {
	withAffinityGate(t, true)
	spec := medialink.EncodeSpec{DeviceLUID: "0x00000000_0x00010540", DeviceIndex: 0}
	if _, _, resolved := spec.Device(); !resolved {
		t.Fatal("test setup: the spec should resolve a device")
	}
	if got := affinityCandidates(spec); got != nil {
		t.Fatalf("candidates for a PINNED device: %#x - adapters must never move silently", got)
	}
}

// TestAffinityCandidatesOnlyWithTwoAdapters: on a single-GPU box there is nothing to move to, so the
// probe must not even be offered (it would cost a child spawn for nothing).
func TestAffinityCandidatesOnlyWithTwoAdapters(t *testing.T) {
	withAffinityGate(t, true)
	got := affinityCandidates(medialink.EncodeSpec{})
	if n := len(encoderscan.Adapters()); n < 2 {
		if got != nil {
			t.Fatalf("%d adapter(s) but candidates %#x", n, got)
		}
		t.Skipf("single-adapter host: the two-adapter arm needs a second GPU")
	}
	if len(got) < 2 {
		t.Fatalf("with %d adapters the candidate list is %#x", len(encoderscan.Adapters()), got)
	}
}

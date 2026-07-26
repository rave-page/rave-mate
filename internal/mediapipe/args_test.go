package mediapipe

import (
	"strings"
	"testing"

	"rave.page/mate/internal/medialink"
)

// Downscale ceiling: 4K input + MaxHeight 1080 → scale filter + bitrate from the OUTPUT
// pixel rate (1080p60 = the 20 Mbps reference, not the 80 Mbps 4K clamp).
func TestEncodeArgsDownscale(t *testing.T) {
	args := strings.Join(encodeArgs(medialink.EncodeSpec{Encoder: "libx264", Codec: medialink.CodecH264,
		Width: 3840, Height: 2160, FPS: 60, MaxHeight: 1080}, false), " ")
	if !strings.Contains(args, "-vf scale=-2:1080") {
		t.Fatalf("missing scale filter: %s", args)
	}
	if !strings.Contains(args, "-b:v 20000k") {
		t.Fatalf("bitrate not derived from scaled output: %s", args)
	}
	// input at/below the ceiling = no filter, native bitrate math
	native := strings.Join(encodeArgs(medialink.EncodeSpec{Encoder: "libx264", Codec: medialink.CodecH264,
		Width: 1280, Height: 720, FPS: 30, MaxHeight: 1080}, false), " ")
	if strings.Contains(native, "scale=") {
		t.Fatalf("unexpected scale on native-size input: %s", native)
	}
}

// Explicit MaxHeight on a HW tier scales on the GPU (scale_cuda / scale_qsv), not on the cores OBS
// wants; AMF + software keep swscale; forceSW pins swscale everywhere.
func TestEncodeArgsHWScaler(t *testing.T) {
	spec := func(enc string) medialink.EncodeSpec {
		return medialink.EncodeSpec{Encoder: enc, Codec: medialink.CodecH264,
			Width: 3840, Height: 2160, FPS: 60, MaxHeight: 1080}
	}
	cases := []struct {
		enc          string
		wantVF       string
		wantInit     bool
		wantHWFamily bool
	}{
		{"h264_nvenc", "format=nv12,hwupload_cuda,scale_cuda=-2:1080", true, true},
		{"hevc_nvenc", "format=nv12,hwupload_cuda,scale_cuda=-2:1080", true, true},
		{"h264_qsv", "format=nv12,hwupload=extra_hw_frames=16,scale_qsv=-2:1080", true, true},
		{"h264_amf", "scale=-2:1080", false, false}, // scale_amf is ffmpeg 7.1+ - not assumable
		{"libx264", "scale=-2:1080", false, false},  // software tier: swscale is the only option
		{"mjpeg", "scale=-2:1080", false, false},
	}
	for _, c := range cases {
		args := encodeArgs(spec(c.enc), false)
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "-vf "+c.wantVF) {
			t.Errorf("%s: filter chain = %q, want -vf %s", c.enc, joined, c.wantVF)
		}
		hasInit := strings.Contains(joined, "-init_hw_device") && strings.Contains(joined, "-filter_hw_device")
		if hasInit != c.wantInit {
			t.Errorf("%s: device init present = %v, want %v (%s)", c.enc, hasInit, c.wantInit, joined)
		}
		if got := hwScaleFamily(c.enc); got != c.wantHWFamily {
			t.Errorf("%s: hwScaleFamily = %v, want %v", c.enc, got, c.wantHWFamily)
		}
		// Device flags must precede the input, and there must be exactly one pair of them.
		if c.wantInit {
			if idx, in := indexOf(args, "-init_hw_device"), indexOf(args, "-i"); idx > in {
				t.Errorf("%s: -init_hw_device at %d comes after -i at %d", c.enc, idx, in)
			}
			if n := count(args, "-init_hw_device"); n != 1 {
				t.Errorf("%s: %d -init_hw_device flags, want 1", c.enc, n)
			}
		}
		// Demotion path: forceSW drops the GPU chain entirely.
		sw := strings.Join(encodeArgs(spec(c.enc), true), " ")
		if !strings.Contains(sw, "-vf scale=-2:1080") {
			t.Errorf("%s: forceSW filter chain = %q, want swscale", c.enc, sw)
		}
		if strings.Contains(sw, "hwupload") || strings.Contains(sw, "-init_hw_device") {
			t.Errorf("%s: forceSW still carries GPU flags: %s", c.enc, sw)
		}
	}
	// No explicit cap at native size = no filter and no device flags on any tier.
	for _, enc := range []string{"h264_nvenc", "h264_qsv", "libx264"} {
		joined := strings.Join(encodeArgs(medialink.EncodeSpec{Encoder: enc, Codec: medialink.CodecH264,
			Width: 1920, Height: 1080, FPS: 60}, false), " ")
		if strings.Contains(joined, "-vf") || strings.Contains(joined, "-init_hw_device") {
			t.Errorf("%s: unscaled route carries filter/device flags: %s", enc, joined)
		}
	}
}

func indexOf(args []string, want string) int {
	for i, a := range args {
		if a == want {
			return i
		}
	}
	return -1
}

func count(args []string, want string) int {
	n := 0
	for _, a := range args {
		if a == want {
			n++
		}
	}
	return n
}

// The GPU-scaler demotion predicate: only armed when an explicit cap actually scales on a family
// with a GPU scaler, and it disarms once swscale is pinned (so the demotion happens exactly once).
func TestEncoderHWScalingPredicate(t *testing.T) {
	mk := func(enc string, h, maxH int) *encoder {
		return &encoder{spec: medialink.EncodeSpec{Encoder: enc, Width: 3840, Height: h, MaxHeight: maxH}}
	}
	if e := mk("h264_nvenc", 2160, 1080); !e.hwScaling() {
		t.Error("nvenc 4K→1080 must use the GPU scaler")
	}
	if e := mk("h264_nvenc", 1080, 1080); e.hwScaling() {
		t.Error("no downscale needed = no GPU scaler")
	}
	if e := mk("h264_nvenc", 2160, 0); e.hwScaling() {
		t.Error("auto (0) MaxHeight on a HW tier never scales")
	}
	if e := mk("libx264", 2160, 1080); e.hwScaling() {
		t.Error("software tier has no GPU scaler")
	}
	e := mk("h264_qsv", 2160, 1080)
	e.swScale.Store(true)
	if e.hwScaling() {
		t.Error("swScale pinned must disarm the predicate (one demotion, not a loop)")
	}
}

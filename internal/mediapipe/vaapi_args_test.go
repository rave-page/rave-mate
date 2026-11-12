package mediapipe

import (
	"strings"
	"testing"

	"rave.page/mate/internal/medialink"
)

// VA-API encode argv: global -vaapi_device render node, CPU->NV12->GPU upload filter, low-latency
// knobs (constrained VBR - budget as ceiling, no CBR padding of flat content - async_depth 1, no
// B-frames), and the Annex-B framing the AU splitter expects
// (dump_extra + {codec}_metadata=aud). The device flag is global, before -i.
func TestEncodeArgsVAAPI(t *testing.T) {
	base := medialink.EncodeSpec{
		Encoder: "h264_vaapi", Codec: medialink.CodecH264,
		Width: 1920, Height: 1080, FPS: 60, VAAPIRenderNode: "/dev/dri/renderD128",
	}
	args := encodeArgs(base, false)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-vaapi_device /dev/dri/renderD128",
		"-vf format=nv12,hwupload",
		"-c:v h264_vaapi",
		"-rc_mode VBR",
		"-async_depth 1",
		"-bf 0",
		"-bsf:v dump_extra=freq=keyframe,h264_metadata=aud=insert",
		"-f h264",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("h264_vaapi argv missing %q in %q", want, joined)
		}
	}
	// -vaapi_device is a GLOBAL option: it must precede -i, and there is exactly one of it.
	if dev, in := indexOf(args, "-vaapi_device"), indexOf(args, "-i"); dev < 0 || dev > in {
		t.Errorf("-vaapi_device at %d not before -i at %d", dev, in)
	}
	if n := count(args, "-vaapi_device"); n != 1 {
		t.Errorf("%d -vaapi_device flags, want 1", n)
	}
	// No nvenc/qsv-style -init_hw_device for VA-API (the encoder inherits the device from the
	// hwupload frames context).
	if strings.Contains(joined, "-init_hw_device") {
		t.Errorf("VA-API argv must not carry -init_hw_device: %s", joined)
	}
}

// An unset VAAPIRenderNode falls back to the default node so the argv is always well-formed.
func TestEncodeArgsVAAPIDefaultNode(t *testing.T) {
	args := strings.Join(encodeArgs(medialink.EncodeSpec{
		Encoder: "h264_vaapi", Codec: medialink.CodecH264, Width: 1280, Height: 720, FPS: 30,
	}, false), " ")
	if !strings.Contains(args, "-vaapi_device "+defaultVAAPINode) {
		t.Errorf("empty node should fall back to %s: %s", defaultVAAPINode, args)
	}
}

// An explicit probe-picked node is honoured (the discrete GPU may not be renderD128).
func TestEncodeArgsVAAPIPickedNode(t *testing.T) {
	args := strings.Join(encodeArgs(medialink.EncodeSpec{
		Encoder: "hevc_vaapi", Codec: medialink.CodecHEVC, Width: 1920, Height: 1080, FPS: 60,
		VAAPIRenderNode: "/dev/dri/renderD129",
	}, false), " ")
	for _, want := range []string{
		"-vaapi_device /dev/dri/renderD129",
		"-c:v hevc_vaapi",
		"-bsf:v dump_extra=freq=keyframe,hevc_metadata=aud=insert",
		"-f hevc",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("hevc_vaapi argv missing %q in %q", want, args)
		}
	}
}

// MaxHeight on a VA-API route scales on the CPU BEFORE hwupload (scale_vaapi is not assumable).
func TestEncodeArgsVAAPIScale(t *testing.T) {
	args := strings.Join(encodeArgs(medialink.EncodeSpec{
		Encoder: "h264_vaapi", Codec: medialink.CodecH264,
		Width: 3840, Height: 2160, FPS: 60, MaxHeight: 1080, VAAPIRenderNode: "/dev/dri/renderD128",
	}, false), " ")
	if !strings.Contains(args, "-vf scale=-2:1080,format=nv12,hwupload") {
		t.Errorf("scaled VA-API filter chain wrong: %s", args)
	}
	// Bitrate derives from the scaled 1080p output, like the other tiers.
	if !strings.Contains(args, "-b:v 20000k") {
		t.Errorf("scaled VA-API bitrate not from output pixel rate: %s", args)
	}
}

// vaapiTestEncodeArgs: the probe's VA-API test-encode names the node + hwupload (the generic
// probe would false-negative every *_vaapi encoder).
func TestVAAPITestEncodeArgs(t *testing.T) {
	args := strings.Join(vaapiTestEncodeArgs("/dev/dri/renderD128", "h264_vaapi"), " ")
	for _, want := range []string{"-vaapi_device /dev/dri/renderD128", "format=nv12,hwupload", "-c:v h264_vaapi", "-f null"} {
		if !strings.Contains(args, want) {
			t.Errorf("vaapi test argv missing %q in %q", want, args)
		}
	}
}

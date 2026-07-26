package mediapipe

import (
	"strings"
	"testing"

	"rave.page/mate/internal/medialink"
)

// argIdx returns the index of flag in args, or -1.
func argIdx(args []string, flag string) int {
	for i, a := range args {
		if a == flag {
			return i
		}
	}
	return -1
}

func argVal(t *testing.T, args []string, flag string) string {
	t.Helper()
	i := argIdx(args, flag)
	if i < 0 || i+1 >= len(args) {
		t.Fatalf("%s missing in %v", flag, args)
	}
	return args[i+1]
}

func baseSpec(enc string, codec medialink.Codec) medialink.EncodeSpec {
	return medialink.EncodeSpec{Encoder: enc, Codec: codec, Width: 1920, Height: 1080, FPS: 60,
		BitrateKbps: 20000, DeviceIndex: -1}
}

// The AUTO path must emit no device flags at all - a build without d3d11va can only be affected by
// an explicit pin, never by the default.
func TestEncodeArgsNoDeviceByDefault(t *testing.T) {
	for _, enc := range []string{"h264_nvenc", "hevc_nvenc", "h264_qsv", "hevc_amf", "libx264", "mjpeg"} {
		args := encodeArgs(baseSpec(enc, medialink.CodecH264))
		for _, flag := range []string{"-init_hw_device", "-filter_hw_device", "-gpu", "-qsv_device"} {
			if argIdx(args, flag) >= 0 {
				t.Errorf("%s: %s emitted with no device pinned", enc, flag)
			}
		}
	}
}

func TestEncodeArgsDeviceFlags(t *testing.T) {
	pin := func(s medialink.EncodeSpec, idx int) medialink.EncodeSpec {
		s.DeviceLUID, s.DeviceIndex = "0x00000000_0x0000c34f", idx
		return s
	}
	// NVENC: d3d11 context on the adapter + the encoder's own -gpu ordinal.
	args := encodeArgs(pin(baseSpec("h264_nvenc", medialink.CodecH264), 1))
	if got := argVal(t, args, "-init_hw_device"); got != "d3d11va=ml:1" {
		t.Fatalf("-init_hw_device = %q", got)
	}
	if got := argVal(t, args, "-filter_hw_device"); got != hwFilterDevice {
		t.Fatalf("-filter_hw_device = %q", got)
	}
	if got := argVal(t, args, "-gpu"); got != "1" {
		t.Fatalf("-gpu = %q", got)
	}
	// The hw device must be initialised BEFORE the input (it is a global option).
	if argIdx(args, "-init_hw_device") > argIdx(args, "-i") {
		t.Fatal("-init_hw_device must precede -i")
	}
	// ...and the private -gpu option AFTER -c:v.
	if argIdx(args, "-gpu") < argIdx(args, "-c:v") {
		t.Fatal("-gpu must follow -c:v")
	}
	// QSV: child-device ordinal.
	args = encodeArgs(pin(baseSpec("h264_qsv", medialink.CodecH264), 2))
	if got := argVal(t, args, "-qsv_device"); got != "2" {
		t.Fatalf("-qsv_device = %q", got)
	}
	// AMF: no per-encoder selector exists in ffmpeg, but it is a D3D11 family → context only.
	args = encodeArgs(pin(baseSpec("hevc_amf", medialink.CodecHEVC), 1))
	if argIdx(args, "-init_hw_device") < 0 {
		t.Fatal("amf: expected the d3d11va context")
	}
	if argIdx(args, "-gpu") >= 0 || argIdx(args, "-qsv_device") >= 0 {
		t.Fatal("amf: no per-encoder device selector may be emitted")
	}
	// Software + intra tiers: nothing at all (no GPU involved).
	for _, enc := range []string{"libx264", "mjpeg"} {
		args = encodeArgs(pin(baseSpec(enc, medialink.CodecH264), 1))
		if argIdx(args, "-init_hw_device") >= 0 || argIdx(args, "-gpu") >= 0 {
			t.Errorf("%s: device flags emitted for a CPU encoder", enc)
		}
	}
	// AMF's parameter-set handling must survive the device flags (regression guard).
	args = encodeArgs(pin(baseSpec("hevc_amf", medialink.CodecHEVC), 0))
	if argVal(t, args, "-header_insertion_mode") != "idr" {
		t.Fatal("amf header_insertion_mode lost")
	}
	if !strings.Contains(strings.Join(args, " "), "dump_extra=freq=keyframe") {
		t.Fatal("amf dump_extra lost")
	}
}

// Engine keying: ONLY the native engine's own capability name runs mfenc. The pre-fix rule keyed on
// Codec == H264, so a negotiated libx264/h264_nvenc silently ran on the MF hardware pipeline while
// the Answer + route stats described a different engine (and SWOnly stopped forcing software).
func TestEncodeEngineKeyedOnEncoderName(t *testing.T) {
	for _, tc := range []struct {
		enc   string
		codec medialink.Codec
		want  encodeEngineKind
	}{
		{medialink.EncoderMFNative, medialink.CodecH264, engineMFNative},
		{"libx264", medialink.CodecH264, engineFfmpegChild}, // the SWOnly / tier-4 bug
		{"h264_nvenc", medialink.CodecH264, engineFfmpegChild},
		{"h264_mf", medialink.CodecH264, engineFfmpegChild}, // ffmpeg's MF wrapper is NOT the native engine
		{"hevc_nvenc", medialink.CodecHEVC, engineFfmpegChild},
		{"mjpeg", medialink.CodecJPEG, engineFfmpegChild},
	} {
		if got := encodeEngine(baseSpec(tc.enc, tc.codec)); got != tc.want {
			t.Errorf("encodeEngine(%s/%v) = %d, want %d", tc.enc, tc.codec, got, tc.want)
		}
	}
}

// A spec that somehow keeps the native engine's capability name must still produce valid ffmpeg argv
// (h264_mf = the Media Foundation wrapper), never "-c:v h264_mf_native".
func TestEncodeArgsNativeEngineNameNeverReachesFfmpeg(t *testing.T) {
	args := encodeArgs(baseSpec(medialink.EncoderMFNative, medialink.CodecH264))
	if got := argVal(t, args, "-c:v"); got != "h264_mf" {
		t.Fatalf("-c:v = %q, want h264_mf", got)
	}
	if strings.Contains(strings.Join(args, " "), medialink.EncoderMFNative) {
		t.Fatalf("native capability name leaked into argv: %v", args)
	}
}

// The ffmpeg substitute for a failed native engine must stay H.264 (the peer was already answered
// H.264) and prefer hardware over the CPU.
func TestFfmpegH264FallbackOrder(t *testing.T) {
	prev := probeCached
	t.Cleanup(func() { probeCached = prev })
	for _, tc := range []struct {
		have []string
		want string
	}{
		{[]string{"libx264", "h264_qsv", "hevc_nvenc"}, "h264_qsv"},
		{[]string{"libx264", "h264_amf"}, "h264_amf"},
		{[]string{"libx264", "mjpeg"}, "libx264"},
		{[]string{"h264_nvenc", "h264_qsv"}, "h264_nvenc"},
	} {
		got, ok := pickH264(tc.have)
		if !ok || got != tc.want {
			t.Errorf("have %v → %q ok=%v, want %q", tc.have, got, ok, tc.want)
		}
	}
	if _, ok := pickH264([]string{"hevc_nvenc", "mjpeg"}); ok {
		t.Error("no H.264 encoder must not resolve a substitute")
	}
}

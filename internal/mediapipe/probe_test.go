package mediapipe

import (
	"context"
	"reflect"
	"testing"

	"rave.page/mate/internal/medialink"
)

// Fixture: real `ffmpeg -encoders` column format (leading flags, two spaces, name, description).
const encodersFixture = `Encoders:
 V..... = Video
 A..... = Audio
 ------
 V....D libx264              libx264 H.264 / AVC / MPEG-4 AVC (codec h264)
 V....D libx265              libx265 H.265 / HEVC (codec hevc)
 V....D h264_nvenc           NVIDIA NVENC H.264 encoder (codec h264)
 V....D hevc_nvenc           NVIDIA NVENC hevc encoder (codec hevc)
 V....D h264_amf             AMD AMF H.264 Encoder (codec h264)
 V....D mjpeg                MJPEG (Motion JPEG)
 A....D aac                  AAC (Advanced Audio Coding)
`

const decodersFixture = `Decoders:
 V..... = Video
 ------
 V....D h264                 H.264 / AVC / MPEG-4 AVC / MPEG-4 part 10
 V....D hevc                 HEVC (High Efficiency Video Coding)
 V....D mjpeg                MJPEG (Motion JPEG)
 V....D av1                  Alliance for Open Media AV1
`

const hwaccelsFixture = `Hardware acceleration methods:
vdpau
cuda
vaapi
qsv
d3d11va
dxva2
`

func TestDiscoverVideoEncoders(t *testing.T) {
	got := discoverVideoEncoders(encodersFixture)
	set := map[string]bool{}
	for _, e := range got {
		set[e] = true
	}
	// HW encoders present in the build + software baselines, discovered vendor-neutrally.
	for _, want := range []string{"h264_nvenc", "hevc_nvenc", "h264_amf", "libx264", "libx265", "mjpeg"} {
		if !set[want] {
			t.Errorf("missing %s in %v", want, got)
		}
	}
	// Not in the fixture build → must NOT be probed (no false candidates).
	for _, absent := range []string{"h264_qsv", "hevc_qsv", "hevc_amf", "h264_vaapi"} {
		if set[absent] {
			t.Errorf("phantom %s", absent)
		}
	}
	// Audio + legend rows are never encoder candidates.
	if set["aac"] || set["="] || set["Video"] {
		t.Errorf("non-video-encoder leaked into candidates: %v", got)
	}
	// HW encoders rank before software baselines.
	if idx(got, "h264_nvenc") > idx(got, "libx264") {
		t.Errorf("HW should precede SW baseline: %v", got)
	}
}

// A custom encoder card registering as a Media Foundation MFT (h264_mf) must be discovered.
func TestDiscoverVideoEncodersMediaFoundation(t *testing.T) {
	fixture := " V....D h264_mf              H264 via MediaFoundation (codec h264)\n" +
		" V....D hevc_mf              HEVC via MediaFoundation (codec hevc)\n"
	got := discoverVideoEncoders(fixture)
	if len(got) != 2 || got[0] != "h264_mf" {
		t.Fatalf("media-foundation encoders not discovered: %v", got)
	}
}

func idx(xs []string, v string) int {
	for i, x := range xs {
		if x == v {
			return i
		}
	}
	return -1
}

func TestParseDecoders(t *testing.T) {
	got := parseDecoders(decodersFixture)
	want := []string{medialink.DecodeH264, medialink.DecodeHEVC, medialink.DecodeJPEG}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decoders = %v, want %v", got, want)
	}
	// AV1 stays excluded in P4 (no OBU/IVF framing) even when the build decodes it.
	for _, c := range got {
		if c == medialink.DecodeAV1 {
			t.Fatal("av1 must not be advertised in P4")
		}
	}
	if parseDecoders("") != nil {
		t.Fatal("empty listing must yield no caps")
	}
}

func TestParseHWAccels(t *testing.T) {
	got := parseHWAccels(hwaccelsFixture)
	want := []string{"cuda", "qsv", "d3d11va", "dxva2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("hwaccels = %v, want %v (preference order)", got, want)
	}
	if parseHWAccels("") != nil {
		t.Fatal("empty listing must yield none")
	}
}

func TestEncodeArgsShapes(t *testing.T) {
	spec := medialink.EncodeSpec{Encoder: "hevc_nvenc", Codec: medialink.CodecHEVC,
		Width: 1920, Height: 1080, FPS: 60, BitrateKbps: 15000}
	args := encodeArgs(spec)
	for _, want := range []string{"-f", "rawvideo", "-pix_fmt", "rgba", "1920x1080",
		"hevc_nvenc", "-b:v", "15000k", "-g", "120", "-bf",
		"dump_extra=freq=keyframe,hevc_metadata=aud=insert"} { // dump_extra first: AMF has no in-band VPS
		if !contains(args, want) {
			t.Errorf("missing %q in %v", want, args)
		}
	}
	// Default bitrate scales with pixel rate, clamped.
	if kb := defaultBitrateKbps(1920, 1080, 60); kb != 20_000 {
		t.Fatalf("1080p60 default = %d", kb)
	}
	if kb := defaultBitrateKbps(320, 240, 15); kb != 2_000 {
		t.Fatalf("floor = %d", kb)
	}
	if kb := defaultBitrateKbps(7680, 4320, 60); kb != 80_000 {
		t.Fatalf("cap = %d", kb)
	}
	// MJPEG: no bsf, self-framing output.
	jargs := encodeArgs(medialink.EncodeSpec{Encoder: "mjpeg", Codec: medialink.CodecJPEG,
		Width: 640, Height: 480, FPS: 30})
	if contains(jargs, "-bsf:v") || !contains(jargs, "mjpeg") {
		t.Fatalf("mjpeg args: %v", jargs)
	}
}

func TestDecodeArgsShapes(t *testing.T) {
	spec := medialink.DecodeSpec{Codec: medialink.CodecH264, Width: 1280, Height: 720, FPS: 60}
	sw := decodeArgs(spec, "")
	if contains(sw, "-hwaccel") || !contains(sw, "h264") || !contains(sw, "1280x720") {
		t.Fatalf("sw decode args: %v", sw)
	}
	hw := decodeArgs(spec, "cuda")
	if !contains(hw, "-hwaccel") || !contains(hw, "cuda") {
		t.Fatalf("hw decode args: %v", hw)
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// ProbeListing must stay test-encode-free (no NVENC session taken mid-stream) and must NOT poison
// the validated cache - Cached() may only report a set that actually test-encoded.
func TestProbeListingIsUnvalidatedAndDoesNotPoisonCache(t *testing.T) {
	if vc, cached := Cached(); cached {
		// Another test in this package already ran the full probe. Assert the documented preference
		// (a validated result beats a listing) and stop - the caches are process-global.
		c, ok := ProbeListing(context.Background(), nil)
		if !ok || !c.Validated || !reflect.DeepEqual(c.Encoders, vc.Encoders) {
			t.Errorf("with a validated probe cached, ProbeListing must return it verbatim: %+v", c)
		}
		return
	}
	c, ok := ProbeListing(context.Background(), nil)
	if !ok {
		t.Skip("ffmpeg unavailable")
	}
	if c.Validated {
		t.Error("listing probe must report Validated=false")
	}
	if len(c.Encoders) == 0 || !reflect.DeepEqual(c.Encoders, c.InBuild) {
		t.Errorf("listing Encoders must equal the in-build candidates, got %v / %v", c.Encoders, c.InBuild)
	}
	if len(c.Errors) != 0 {
		t.Errorf("listing probe cannot know encode errors, got %v", c.Errors)
	}
	if _, cached := Cached(); cached {
		t.Error("Cached() must stay empty after a listing-only probe (no test encodes ran)")
	}
	// second call is served from the listing cache
	c2, _ := ProbeListing(context.Background(), nil)
	if !reflect.DeepEqual(c.Encoders, c2.Encoders) {
		t.Error("listing cache must be stable")
	}
}

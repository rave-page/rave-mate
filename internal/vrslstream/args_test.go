package vrslstream

import (
	"strings"
	"testing"

	"rave.page/mate/internal/config"
)

// argIndex returns the position of tok in args (-1 if absent).
func argIndex(args []string, tok string) int {
	for i, a := range args {
		if a == tok {
			return i
		}
	}
	return -1
}

func hasArg(args []string, tok string) bool { return argIndex(args, tok) >= 0 }

func TestFFmpegArgsRawRGBAInput(t *testing.T) {
	cfg := config.StreamFeature{URL: "rtmp://x/app", Mode: "standard"}
	args := ffmpegArgs(cfg, 1920, 1080)
	joined := strings.Join(args, " ")
	// raw RGBA stdin
	if !strings.Contains(joined, "-f rawvideo") || !strings.Contains(joined, "-pix_fmt rgba") {
		t.Fatalf("missing rawvideo/rgba input: %s", joined)
	}
	if !strings.Contains(joined, "-video_size 1920x1080") {
		t.Fatalf("missing video_size: %s", joined)
	}
	if argIndex(args, "-i") < 0 || args[argIndex(args, "-i")+1] != "-" {
		t.Fatalf("stdin input '-i -' missing: %s", joined)
	}
}

func TestFFmpegArgsNoColorspaceFilter(t *testing.T) {
	// LINEAR/no-gamma is load-bearing: NO -vf colorspace/gamma filter may appear.
	args := ffmpegArgs(config.StreamFeature{URL: "rtmp://x/app"}, 1920, 1080)
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "-vf") {
		t.Fatalf("unexpected -vf (would skew raw DMX bytes): %s", joined)
	}
	for _, bad := range []string{"colorspace", "gamma", "colormatrix", "lut", "eq="} {
		if strings.Contains(joined, bad) {
			t.Fatalf("unexpected color filter %q: %s", bad, joined)
		}
	}
	// Full range so byte v -> luma v (no limited-range squish).
	if !strings.Contains(joined, "-color_range pc") {
		t.Fatalf("missing full-range flag: %s", joined)
	}
	if !strings.Contains(joined, "-pix_fmt yuv420p") {
		t.Fatalf("missing yuv420p output (Quest AVPro needs it): %s", joined)
	}
}

func TestFFmpegArgsShortGopNoBFrames(t *testing.T) {
	cfg := config.StreamFeature{URL: "rtmp://x/app", FPS: 30}
	args := ffmpegArgs(cfg, 1920, 1080)
	// -g == fps (1s GOP), -bf 0 (no B-frames so channel bytes don't drift).
	gi := argIndex(args, "-g")
	if gi < 0 || args[gi+1] != "30" {
		t.Fatalf("expected -g 30: %v", args)
	}
	bi := argIndex(args, "-bf")
	if bi < 0 || args[bi+1] != "0" {
		t.Fatalf("expected -bf 0: %v", args)
	}
}

func TestFFmpegArgsEncoderMapping(t *testing.T) {
	cases := map[string]string{
		"x264":  "libx264",
		"nvenc": "h264_nvenc",
		"qsv":   "h264_qsv",
		"amf":   "h264_amf",
		"auto":  "libx264",
		"":      "libx264",
	}
	for tok, want := range cases {
		args := ffmpegArgs(config.StreamFeature{URL: "rtmp://x/a", Encoder: tok}, 1920, 1080)
		ci := argIndex(args, "-c:v")
		if ci < 0 || args[ci+1] != want {
			t.Fatalf("encoder %q -> %v, want -c:v %s", tok, args, want)
		}
	}
}

func TestFFmpegArgsRTMPvsWHIP(t *testing.T) {
	rtmp := ffmpegArgs(config.StreamFeature{URL: "rtmp://h/app", StreamKey: "sk", Transport: "rtmp"}, 1920, 1080)
	if !hasArg(rtmp, "flv") {
		t.Fatalf("rtmp should use -f flv: %v", rtmp)
	}
	if rtmp[len(rtmp)-1] != "rtmp://h/app/sk" {
		t.Fatalf("rtmp output = %q, want rtmp://h/app/sk", rtmp[len(rtmp)-1])
	}
	whip := ffmpegArgs(config.StreamFeature{URL: "https://h/whip/endpoint", Transport: "whip"}, 1920, 1080)
	if !hasArg(whip, "whip") {
		t.Fatalf("whip should use -f whip: %v", whip)
	}
	if whip[len(whip)-1] != "https://h/whip/endpoint" {
		t.Fatalf("whip output = %q, want the URL verbatim", whip[len(whip)-1])
	}
}

func TestOutputURL(t *testing.T) {
	cases := []struct {
		url, key, transport, want string
	}{
		{"rtmp://h/app", "sk", "rtmp", "rtmp://h/app/sk"},
		{"rtmp://h/app/", "sk", "rtmp", "rtmp://h/app/sk"}, // trailing slash trimmed
		{"rtmp://h/app", "", "rtmp", "rtmp://h/app"},       // no key
		{"https://h/whip", "sk", "whip", "https://h/whip"}, // whip ignores key
	}
	for _, c := range cases {
		got := outputURL(config.StreamFeature{URL: c.url, StreamKey: c.key, Transport: c.transport})
		if got != c.want {
			t.Fatalf("outputURL(%q,%q,%q) = %q, want %q", c.url, c.key, c.transport, got, c.want)
		}
	}
}

func TestBitrateOverrideAndDerive(t *testing.T) {
	// explicit bitrate honoured
	args := ffmpegArgs(config.StreamFeature{URL: "rtmp://x/a", BitrateKbps: 4000}, 1920, 1080)
	bi := argIndex(args, "-b:v")
	if bi < 0 || args[bi+1] != "4000k" {
		t.Fatalf("expected -b:v 4000k: %v", args)
	}
	// derived bitrate is within the clamp
	d := deriveBitrateKbps(1920, 1080, 30)
	if d < 2000 || d > 20000 {
		t.Fatalf("derived bitrate %d out of clamp", d)
	}
}

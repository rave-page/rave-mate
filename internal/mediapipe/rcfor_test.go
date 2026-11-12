package mediapipe

import (
	"strings"
	"testing"

	"rave.page/mate/internal/medialink"
)

func join(a []string) string { return strings.Join(a, " ") }

func TestQpFromQuality(t *testing.T) {
	// Monotonic: higher quality => lower QP; 70 => ~24; clamped to [18,38].
	if qpFromQuality(70) != 25 {
		t.Fatalf("q70 => %d, want 25", qpFromQuality(70))
	}
	if qpFromQuality(100) != 18 || qpFromQuality(1) != 38 {
		t.Fatalf("bounds wrong: q100=%d q1=%d", qpFromQuality(100), qpFromQuality(1))
	}
	if !(qpFromQuality(30) > qpFromQuality(60) && qpFromQuality(60) > qpFromQuality(90)) {
		t.Fatal("QP must fall as quality rises")
	}
	if qpFromQuality(0) != 24 {
		t.Fatal("qt<=0 => default 24")
	}
}

func TestRcForQualityMode(t *testing.T) {
	const kbps, gop = 35000, 120
	// Fixed/CBR when qt=0: b:v == maxrate, no -cq/-crf.
	cbr := join(rcFor("h264_nvenc", kbps, 0, gop))
	if !strings.Contains(cbr, "-b:v 35000k") || !strings.Contains(cbr, "-maxrate 35000k") || strings.Contains(cbr, "-cq") {
		t.Fatalf("qt=0 must be CBR ceiling: %s", cbr)
	}
	cases := []struct {
		enc  string
		want []string // substrings the quality RC must contain
	}{
		{"h264_nvenc", []string{"-rc vbr", "-cq 25", "-b:v 0", "-maxrate 35000k"}},
		{"hevc_amf", []string{"-rc qvbr", "-qvbr_quality_level 25", "-maxrate 35000k"}},
		{"h264_qsv", []string{"-global_quality 25", "-maxrate 35000k"}},
		{"libx264", []string{"-crf 25", "-maxrate 35000k"}},
		{"libx265", []string{"-crf 25", "-maxrate 35000k"}},
		{"libsvtav1", []string{"-crf 25"}},
	}
	for _, c := range cases {
		got := join(rcFor(c.enc, kbps, 70, gop))
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s quality RC missing %q: %s", c.enc, w, got)
			}
		}
		if strings.Contains(got, "-b:v 35000k") { // quality mode must not pin CBR bitrate
			t.Errorf("%s quality RC pinned CBR bitrate: %s", c.enc, got)
		}
	}
	// VA-API + MF keep the ceiling form (the Linux agent / Zig own their quality mode).
	if va := join(rcFor("h264_vaapi", kbps, 70, gop)); !strings.Contains(va, "-b:v 35000k") {
		t.Fatalf("vaapi must keep the ceiling form: %s", va)
	}
}

func TestEncodeArgsQualityThreadsThrough(t *testing.T) {
	spec := medialink.EncodeSpec{Encoder: "h264_nvenc", Codec: medialink.CodecH264,
		Width: 3840, Height: 2160, FPS: 60, BitrateKbps: 35000, QualityTarget: 70}
	got := join(encodeArgs(spec, false))
	if !strings.Contains(got, "-cq 25") || !strings.Contains(got, "-maxrate 35000k") {
		t.Fatalf("encodeArgs did not thread quality mode through: %s", got)
	}
}

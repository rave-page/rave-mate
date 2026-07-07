package ui

import (
	"testing"

	"rave.page/mate/internal/webcam"
)

func TestFmtCamStatus(t *testing.T) {
	cases := []struct {
		st   webcam.Status
		want string
	}{
		{webcam.Status{Running: true, W: 1280, H: 720, FPS: 30}, "LIVE - 1280x720 @ 30"},
		{webcam.Status{Running: true, W: 640, H: 480, Err: "restarting"}, "LIVE - 640x480 · restarting"},
		{webcam.Status{Err: "ffmpeg not found"}, "ffmpeg not found"},
		{webcam.Status{}, "No camera selected."},
		{webcam.Status{Device: "Brio"}, "Ready - Brio"},
	}
	for _, c := range cases {
		if got := fmtCamStatus(c.st); got != c.want {
			t.Errorf("fmtCamStatus(%+v) = %q, want %q", c.st, got, c.want)
		}
	}
}

func TestCamModeRoundTrip(t *testing.T) {
	modes := []webcam.Mode{{W: 1920, H: 1080, FPS: 30}, {W: 1920, H: 1080, FPS: 29.97}, {W: 640, H: 480, FPS: 30}}
	opts := camModeStrings(modes)
	// 29.97 rounds to 30 → dedup with the first entry
	if len(opts) != 2 || opts[0] != "1920x1080 @ 30" || opts[1] != "640x480 @ 30" {
		t.Fatalf("camModeStrings: %v", opts)
	}
	w, h, fps := parseCamMode(opts[0])
	if w != 1920 || h != 1080 || fps != 30 {
		t.Fatalf("parse: %dx%d@%d", w, h, fps)
	}
	if w, h, fps := parseCamMode(""); w != 0 || h != 0 || fps != 0 {
		t.Fatal("blank must yield zeros (device default)")
	}
	if w, h, fps := parseCamMode("garbage"); w != 0 || h != 0 || fps != 0 {
		t.Fatal("garbage must yield zeros")
	}
	if w, h, _ := parseCamMode("800x600"); w != 800 || h != 600 {
		t.Fatal("fps-less mode must parse")
	}
}

func TestCamTopoSig(t *testing.T) {
	p := []webcam.PropState{{ID: "zoom", Min: 100, Max: 400, Step: 10, Value: 100}}
	a := camTopoSig("Brio", p)
	p2 := []webcam.PropState{{ID: "zoom", Min: 100, Max: 400, Step: 10, Value: 250}}
	if a != camTopoSig("Brio", p2) {
		t.Fatal("value change must not change topology")
	}
	if a == camTopoSig("Other", p) {
		t.Fatal("device must be part of the signature")
	}
	p3 := []webcam.PropState{{ID: "zoom", Min: 0, Max: 400, Step: 10}}
	if a == camTopoSig("Brio", p3) {
		t.Fatal("range change must change topology")
	}
}

package ui

import (
	"testing"

	"rave.page/mate/internal/mediatools"
)

func TestIsPlayable(t *testing.T) {
	// Native-decoder tier: always playable (AIFF has a native decoder now, no ffmpeg needed).
	for _, p := range []string{"a.mp3", "A.WAV", "x.flac", "y.ogg", "z.oga", "a.aiff", "a.aif"} {
		if !isPlayable(p) {
			t.Errorf("isPlayable(%q) = false, want true", p)
		}
	}
	// ffmpeg-decoded tier: playable exactly when ffmpeg resolves on this box.
	_, ffmpeg := mediatools.Resolve("ffmpeg")
	for _, p := range []string{"a.m4a", "a.aac", "a.opus"} {
		if isPlayable(p) != ffmpeg {
			t.Errorf("isPlayable(%q) = %v, want %v (ffmpeg=%v)", p, !ffmpeg, ffmpeg, ffmpeg)
		}
	}
	for _, p := range []string{"a.mp4", "a.mkv", "a.txt"} {
		if isPlayable(p) {
			t.Errorf("isPlayable(%q) = true, want false (external Open)", p)
		}
	}
}

func TestIsMediaPath(t *testing.T) {
	if !isMediaPath("clip.MP4") || !isMediaPath("song.aiff") {
		t.Error("video/aiff should count as media (waveform/Open)")
	}
	if isMediaPath("notes.txt") || isMediaPath("cover.jpg") {
		t.Error("non-media should not count")
	}
}

func TestIsVideoPath(t *testing.T) {
	for _, p := range []string{"set.MP4", "clip.mkv", "a.mov", "b.webm", "c.avi", "d.m4v", "e.wmv", "f.flv"} {
		if !isVideoPath(p) {
			t.Errorf("isVideoPath(%q) = false, want true", p)
		}
	}
	// Audio + non-media must not be treated as video (waveform stays enabled for audio).
	for _, p := range []string{"song.mp3", "a.wav", "x.flac", "y.ogg", "z.m4a", "notes.txt"} {
		if isVideoPath(p) {
			t.Errorf("isVideoPath(%q) = true, want false", p)
		}
	}
}

func TestFmtClock(t *testing.T) {
	cases := map[float64]string{0: "0:00", 5: "0:05", 65: "1:05", 600: "10:00", 3661: "1:01:01"}
	for in, want := range cases {
		if got := fmtClock(in); got != want {
			t.Errorf("fmtClock(%v) = %q, want %q", in, got, want)
		}
	}
}

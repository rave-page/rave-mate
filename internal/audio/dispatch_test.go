package audio

import (
	"errors"
	"os"
	"testing"
)

func TestSniffAndOpenable(t *testing.T) {
	cases := []struct {
		magic []byte
		path  string
		want  string
	}{
		{[]byte("RIFF\x00\x00\x00\x00WAVE"), "x.wav", "wav"},
		{[]byte("FORM\x00\x00\x00\x00AIFF"), "x.aif", "aiff"},
		{[]byte("FORM\x00\x00\x00\x00AIFC"), "x.aifc", "aiff"},
		{[]byte("fLaC\x00\x00\x00\x00\x00\x00\x00\x00"), "x.flac", "flac"},
		{[]byte("OggS\x00\x00\x00\x00\x00\x00\x00\x00"), "x.ogg", "ogg"},
		{[]byte("ID3\x04\x00\x00\x00\x00\x00\x00\x00\x00"), "x.mp3", "mp3"},
		{[]byte{0xFF, 0xFB, 0x90, 0x00, 0, 0, 0, 0, 0, 0, 0, 0}, "x.mp3", "mp3"}, // MPEG frame sync, no ID3
		{[]byte{0xFF, 0xF1, 0x00, 0x00, 0, 0, 0, 0, 0, 0, 0, 0}, "x.aac", "aac"}, // ADTS
		{[]byte("\x00\x00\x00\x18ftypM4A "), "x.m4a", "aac"},
		{nil, "x.unknown", ""},
	}
	for _, c := range cases {
		if got := sniff(c.magic, c.path); got != c.want {
			t.Errorf("sniff(%q, %q) = %q want %q", c.magic, c.path, got, c.want)
		}
	}

	for _, ext := range []string{"a.wav", "a.aiff", "a.flac", "a.mp3", "a.ogg", "a.oga"} {
		if !Openable(ext) {
			t.Errorf("Openable(%q) = false", ext)
		}
	}
	for _, ext := range []string{"a.aac", "a.m4a", "a.opus", "a.mkv", "a.txt"} {
		if Openable(ext) {
			t.Errorf("Openable(%q) = true (native path must not claim it)", ext)
		}
	}
}

func TestOpenUnsupportedIsErrUnsupported(t *testing.T) {
	// A .aac file (even nonexistent-content) must classify to the AAC branch => ErrUnsupported,
	// so callers keep the ffmpeg fallback rather than erroring hard.
	// Use a temp file with an ADTS header.
	dir := t.TempDir()
	p := dir + "/x.aac"
	if err := os.WriteFile(p, []byte{0xFF, 0xF1, 0x00, 0x00}, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Open(p)
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Open(.aac) err = %v, want ErrUnsupported", err)
	}
}

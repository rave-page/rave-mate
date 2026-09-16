package webui

import (
	"strings"
	"testing"

	"rave.page/mate/internal/localmedia"
)

func pkSeed(u *UI, kind string) *pkState {
	s := u.pk()
	s.mu.Lock()
	s.open, s.kind, s.sortBy = true, kind, "name"
	s.entries = []localmedia.Entry{
		{Name: "apps", IsDirectory: true, Kind: "directory"},
		{Name: "beat.mp3", Kind: "audio", Extension: "mp3"},
		{Name: "cymbal.wav", Kind: "audio", Extension: "wav"},
		{Name: "drum.flac", Kind: "audio", Extension: "flac"},
	}
	s.mu.Unlock()
	return s
}

func TestPkKeyMovesHighlight(t *testing.T) {
	u, _ := newTestHeadless(t)
	s := pkSeed(u, "file")
	// dirs sort first: [apps, beat.mp3, cymbal.wav, drum.flac]
	u.pkKey("down")
	u.pkKey("down")
	if s.hlIdx != 2 {
		t.Fatalf("after 2x down hlIdx=%d want 2", s.hlIdx)
	}
	u.pkKey("up")
	if s.hlIdx != 1 {
		t.Fatalf("after up hlIdx=%d want 1", s.hlIdx)
	}
	u.pkKey("end")
	if s.hlIdx != 3 {
		t.Fatalf("end hlIdx=%d want 3", s.hlIdx)
	}
	u.pkKey("down") // clamps at last
	if s.hlIdx != 3 {
		t.Fatalf("down at end hlIdx=%d want 3 (clamped)", s.hlIdx)
	}
	u.pkKey("home")
	if s.hlIdx != 0 {
		t.Fatalf("home hlIdx=%d want 0", s.hlIdx)
	}
}

func TestPkJumpToPrefix(t *testing.T) {
	u, _ := newTestHeadless(t)
	s := pkSeed(u, "file")
	u.pkJump("c") // cymbal.wav at index 2
	if s.hlIdx != 2 {
		t.Fatalf("jump 'c' hlIdx=%d want 2", s.hlIdx)
	}
	// a fresh prefix "d" (>1s apart would reset, but immediate append here) -> "cd" matches nothing,
	// so the highlight stays. Reset the buffer to test a clean single-char jump.
	s.mu.Lock()
	s.jumpBuf = ""
	s.mu.Unlock()
	u.pkJump("a") // apps at index 0
	if s.hlIdx != 0 {
		t.Fatalf("jump 'a' hlIdx=%d want 0", s.hlIdx)
	}
}

// TestPickerKeyWiringInRuntime pins the shell.go keydown wiring that ctl cannot inject (no raw
// key-injection command): the picker is scoped by .pk-wrap and forwards pk-key / pk-jump.
func TestPickerKeyWiringInRuntime(t *testing.T) {
	for _, want := range []string{".pk-wrap", "pk-key", "pk-jump", "modal-close", "type-to-jump"} {
		if !strings.Contains(runtimeJS, want) {
			t.Errorf("runtimeJS is missing the picker key wiring %q", want)
		}
	}
}

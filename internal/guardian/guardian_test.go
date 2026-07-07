package guardian

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAllowRestartBrake(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	for i := range loopMax {
		if !allowRestart(dir, now.Add(time.Duration(i)*time.Second)) {
			t.Fatalf("restart %d blocked, want allowed", i+1)
		}
	}
	if allowRestart(dir, now.Add(time.Duration(loopMax)*time.Second)) {
		t.Fatalf("restart %d allowed, want crash-loop brake", loopMax+1)
	}
	// Outside the window the old stamps age out.
	if !allowRestart(dir, now.Add(loopWindow+time.Minute)) {
		t.Fatal("restart after window blocked, want allowed")
	}
}

func TestAllowRestartCorruptStateFailsOpen(t *testing.T) {
	dir := t.TempDir()
	if err := writeCorrupt(dir); err != nil {
		t.Fatal(err)
	}
	if !allowRestart(dir, time.Now()) {
		t.Fatal("corrupt state must fail open")
	}
}

func writeCorrupt(dir string) error {
	return os.WriteFile(filepath.Join(dir, restartState), []byte("{not json"), 0o644)
}

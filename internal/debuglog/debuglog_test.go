package debuglog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRecoverLogsPanic verifies Recover captures a panic + stack into the debug file
// (fatal=false contains it). This is the mechanism that would have surfaced the silent
// GUI-build crash.
func TestRecoverLogsPanic(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "dbg.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() // close before t.TempDir's RemoveAll (Windows can't unlink an open file)
	logFile = f
	t.Cleanup(func() { logFile = nil })

	func() {
		defer Recover(nil, "unit", false)
		panic("boom")
	}()

	data, _ := os.ReadFile(f.Name())
	s := string(data)
	if !strings.Contains(s, "panic: boom") {
		t.Errorf("missing panic message:\n%s", s)
	}
	if !strings.Contains(s, "[unit]") {
		t.Errorf("missing source tag:\n%s", s)
	}
	if !strings.Contains(s, "debuglog.TestRecoverLogsPanic") {
		t.Errorf("missing stack trace:\n%s", s)
	}
}

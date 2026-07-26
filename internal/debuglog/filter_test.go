package debuglog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rave.page/mate/internal/logbus"
)

// runMirror feeds entries through mirror into a temp file and returns its content.
func runMirror(t *testing.T, entries []logbus.Entry) string {
	t.Helper()
	f, err := os.Create(filepath.Join(t.TempDir(), "dbg.log"))
	if err != nil {
		t.Fatal(err)
	}
	ch := make(chan logbus.Entry, len(entries))
	for _, e := range entries {
		ch <- e
	}
	close(ch)
	mirror(ch, f) // synchronous: channel pre-closed
	_ = f.Close() // close before TempDir cleanup (Windows unlink)
	data, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestFileLevelFilter proves Debug entries are NOT mirrored to disk by default (the
// per-frame-failure → per-frame-disk-write fix) while Info+ still land, and that
// SetFileLevel(Debug) re-enables everything for field debugging.
func TestFileLevelFilter(t *testing.T) {
	now := time.Now()
	entries := []logbus.Entry{
		{Time: now, Level: logbus.Debug, Source: "frame", Msg: "per-frame-noise"},
		{Time: now, Level: logbus.Info, Source: "app", Msg: "kept-info"},
		{Time: now, Level: logbus.Warn, Source: "app", Msg: "kept-warn"},
		{Time: now, Level: logbus.Error, Source: "app", Msg: "kept-error"},
	}

	out := runMirror(t, entries)
	if strings.Contains(out, "per-frame-noise") {
		t.Errorf("Debug written to disk by default:\n%s", out)
	}
	for _, want := range []string{"kept-info", "kept-warn", "kept-error"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}

	SetFileLevel(logbus.Debug)
	t.Cleanup(func() { SetFileLevel(logbus.Info) })
	out = runMirror(t, entries)
	if !strings.Contains(out, "per-frame-noise") {
		t.Errorf("SetFileLevel(Debug) must re-enable Debug-to-disk:\n%s", out)
	}
}

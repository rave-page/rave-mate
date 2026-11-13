package debuglog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rave.page/mate/internal/logbus"
)

// Size rotation keeps at most keep files, each bounded; the newest content stays in the base file.
func TestRotatorBoundsFilesAndSize(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, LogName)
	r, err := openRotator(p, 1000, 3)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200; i++ {
		fmt.Fprintf(r, "line %03d %s\n", i, strings.Repeat("x", 40))
	}
	_ = r.f.Close()
	ents, _ := os.ReadDir(dir)
	if len(ents) != 3 {
		t.Fatalf("want 3 files (base + .1 + .2), got %d", len(ents))
	}
	for _, e := range ents {
		if st, _ := os.Stat(filepath.Join(dir, e.Name())); st.Size() > 1000 {
			t.Fatalf("%s exceeds the size cap: %d", e.Name(), st.Size())
		}
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "line 199") {
		t.Fatal("newest line must be in the base file")
	}
}

// Every mirrored line carries the full date (the incident had to infer dates from start banners).
func TestMirrorLinesAreDated(t *testing.T) {
	when := time.Date(2026, 11, 13, 3, 37, 1, 0, time.Local)
	out := runMirror(t, []logbus.Entry{{Time: when, Level: logbus.Info, Source: "gpumem", Msg: "x"}})
	if !strings.HasPrefix(out, "2026-11-13T03:37:01.000 ") {
		t.Fatalf("line not dated: %q", out)
	}
}

func TestLogDirOverride(t *testing.T) {
	t.Setenv("RAVE_MATE_LOG_DIR", `C:\x\logs`)
	if LogDir() != `C:\x\logs` {
		t.Fatal("RAVE_MATE_LOG_DIR must win")
	}
}

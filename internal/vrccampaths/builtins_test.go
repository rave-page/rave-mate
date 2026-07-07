package vrccampaths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuiltinDJPathsValid(t *testing.T) {
	ps := BuiltinDJPaths()
	if len(ps) < 5 {
		t.Fatalf("expected several builtin paths, got %d", len(ps))
	}
	for _, b := range ps {
		if b.Name == "" || b.Preset == "" || len(b.Points) == 0 {
			t.Errorf("incomplete builtin: %+v", b.Name)
		}
		for _, pt := range b.Points {
			if !pt.IsLocal {
				t.Errorf("%s: builtin paths must be player-relative", b.Name)
			}
		}
	}
}

func TestWritePathRoundTrip(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "p.json")
	orig := BuiltinDJPaths()[0].Points
	if err := WritePath(f, orig); err != nil {
		t.Fatal(err)
	}
	// lowercase vector keys (VRChat format)
	raw, _ := os.ReadFile(f)
	if !strings.Contains(string(raw), "\"x\":") || strings.Contains(string(raw), "\"X\":") {
		t.Errorf("vector keys must be lowercase x/y/z, got:\n%s", raw[:min(len(raw), 200)])
	}
	got, err := LoadPoints(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(orig) {
		t.Fatalf("roundtrip point count %d != %d", len(got), len(orig))
	}
	for i := range got {
		if got[i].Position != orig[i].Position || got[i].Zoom != orig[i].Zoom || got[i].FocalDistance != orig[i].FocalDistance {
			t.Errorf("point %d mismatch: %+v vs %+v", i, got[i], orig[i])
		}
		if got[i].Index != i {
			t.Errorf("point %d index = %d", i, got[i].Index)
		}
	}
}

func TestInstallBuiltins(t *testing.T) {
	dir := t.TempDir()
	n, dst, err := InstallBuiltins(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(BuiltinDJPaths()) {
		t.Errorf("installed %d, want %d", n, len(BuiltinDJPaths()))
	}
	ents, _ := os.ReadDir(dst)
	if len(ents) != n {
		t.Errorf("folder has %d files, want %d", len(ents), n)
	}
}

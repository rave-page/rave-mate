package unityproj

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteWorldSyncSources(t *testing.T) {
	dir := t.TempDir()
	if err := WriteWorldSyncSources(dir, []byte("{}")); err == nil {
		t.Fatal("want error for non-Unity dir")
	}
	for _, d := range []string{"Assets", "ProjectSettings"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := WriteWorldSyncSources(dir, []byte(`{"sources":[]}`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := os.ReadFile(WorldSyncSourcesPath(dir))
	if err != nil || string(got) != `{"sources":[]}` {
		t.Fatalf("read back: %s %v", got, err)
	}
}

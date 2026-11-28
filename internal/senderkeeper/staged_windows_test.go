//go:build windows

package senderkeeper

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStageIntoExtractsReusesAndPrunes(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "rave-mate-keeper-000000000000.exe")
	if err := os.WriteFile(stale, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := stageInto(dir, []byte("keeper-v2"))
	if b, err := os.ReadFile(p); err != nil || string(b) != "keeper-v2" {
		t.Fatalf("staged %q: %v %q", p, err, b)
	}
	if _, err := os.Stat(stale); err == nil {
		t.Fatal("superseded keeper not pruned")
	}
	if p2 := stageInto(dir, []byte("keeper-v2")); p2 != p {
		t.Fatalf("same content restaged to %q, want %q", p2, p)
	}
	if p3 := stageInto(dir, []byte("keeper-v3")); p3 == p || p3 == "" {
		t.Fatalf("new content must get a new name, got %q", p3)
	}
	if stageEmbedded(nil) != "" {
		t.Fatal("empty embed must not stage")
	}
}

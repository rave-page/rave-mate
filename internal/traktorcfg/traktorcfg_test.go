package traktorcfg

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBackupRoundTrip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "Traktor Settings.tsi")
	content := []byte("DIOM... fake tsi bytes")
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}

	bak, err := Backup(src)
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	got, err := os.ReadFile(bak)
	if err != nil || string(got) != string(content) {
		t.Fatalf("backup content mismatch: %q err=%v", got, err)
	}
	if list := ListBackups(src); len(list) != 1 || list[0] != bak {
		t.Fatalf("ListBackups = %v, want [%s]", list, bak)
	}

	// The original file is never touched by a backup.
	if orig, _ := os.ReadFile(src); string(orig) != string(content) {
		t.Fatal("backup must not modify the original")
	}
}

func TestNewestWithSettings(t *testing.T) {
	// Discover yields newest-first; the chosen install must be the newest WITH a Settings.tsi.
	all := []Install{
		{Version: "4.2.0", Settings: "/ni/4.2.0/Traktor Settings.tsi"},
		{Version: "4.1.1", Settings: "/ni/4.1.1/Traktor Settings.tsi"},
		{Version: "3.11.1", Settings: "/ni/3.11.1/Traktor Settings.tsi"},
	}
	got, ok := newestWithSettings(all)
	if !ok || got.Version != "4.2.0" {
		t.Fatalf("newestWithSettings = %q ok=%v, want 4.2.0", got.Version, ok)
	}

	// Newest has no .tsi → fall through to the next newest that does.
	all[0].Settings = ""
	if got, ok := newestWithSettings(all); !ok || got.Version != "4.1.1" {
		t.Fatalf("with 4.2.0 lacking settings = %q ok=%v, want 4.1.1", got.Version, ok)
	}

	if _, ok := newestWithSettings([]Install{{Version: "4.2.0"}}); ok {
		t.Fatal("no install has settings → ok must be false")
	}
}

func TestIsTraktorExe(t *testing.T) {
	for _, name := range []string{"Traktor Pro 4.exe", "Traktor.exe", "Traktor Pro 3.exe", "traktor pro 4.exe"} {
		if !isTraktorExe(name) {
			t.Errorf("isTraktorExe(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"Traktor Kontrol S4 MK3 ASIO Driver", "explorer.exe", "NIHardwareAgent.exe", ""} {
		if isTraktorExe(name) {
			t.Errorf("isTraktorExe(%q) = true, want false", name)
		}
	}
}

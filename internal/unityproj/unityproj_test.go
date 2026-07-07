package unityproj

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"rave.page/mate/internal/vrmotion"
)

// mkUnityProject makes a minimal fake Unity project (Assets/ + ProjectSettings/).
func mkUnityProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, sub := range []string{"Assets", "ProjectSettings"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestIsUnityProject(t *testing.T) {
	good := mkUnityProject(t)
	if !IsUnityProject(good) {
		t.Error("expected valid Unity project")
	}
	bad := t.TempDir() // empty dir
	if IsUnityProject(bad) {
		t.Error("empty dir should not be a Unity project")
	}
	// Only Assets, no ProjectSettings.
	half := t.TempDir()
	if err := os.MkdirAll(filepath.Join(half, "Assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if IsUnityProject(half) {
		t.Error("Assets-only dir should not be a Unity project")
	}
}

func TestInstallPlugin(t *testing.T) {
	// Non-Unity dir → error.
	if err := InstallPlugin(t.TempDir()); err == nil {
		t.Error("expected error installing into non-Unity dir")
	}

	proj := mkUnityProject(t)
	if err := InstallPlugin(proj); err != nil {
		t.Fatalf("install: %v", err)
	}
	pkg := filepath.Join(proj, "Packages", PluginName, "package.json")
	if _, err := os.Stat(pkg); err != nil {
		t.Fatalf("package.json not installed: %v", err)
	}
	// Editor C# landed too.
	cs := filepath.Join(proj, "Packages", PluginName, "Editor", "RaveMateControl.cs")
	if _, err := os.Stat(cs); err != nil {
		t.Fatalf("editor source not installed: %v", err)
	}

	// Inspect reflects installed state.
	p := Inspect(proj)
	if !p.Valid || !p.HasPlugin {
		t.Errorf("Inspect = %+v, want Valid+HasPlugin", p)
	}

	// Re-install is idempotent.
	if err := InstallPlugin(proj); err != nil {
		t.Fatalf("reinstall: %v", err)
	}
}

func TestExportTake(t *testing.T) {
	proj := mkUnityProject(t)
	rec := &vrmotion.Recording{
		Name: "spin",
		Hz:   30,
		Frames: []vrmotion.Frame{
			{T: 0, Poses: map[int]vrmotion.Pose{0: {Pos: [3]float32{0, 1, 0}, Rot: [4]float32{0, 0, 0, 1}}}},
			{T: 0.5, Poses: map[int]vrmotion.Pose{0: {Pos: [3]float32{0, 1, 1}, Rot: [4]float32{0, 0, 0, 1}}}},
		},
		Duration: 0.5,
	}
	path, err := ExportTake(proj, rec)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if filepath.Base(path) != "spin.anim" {
		t.Errorf("path = %q, want spin.anim", path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf(".anim not written: %v", err)
	}

	// Unnamed take → "take.anim".
	path2, err := ExportTake(proj, &vrmotion.Recording{Hz: 30})
	if err != nil {
		t.Fatalf("export unnamed: %v", err)
	}
	if filepath.Base(path2) != "take.anim" {
		t.Errorf("path = %q, want take.anim", path2)
	}
}

func TestDiscoverVCCProjects(t *testing.T) {
	la := t.TempDir()
	t.Setenv("LOCALAPPDATA", la)

	want := []string{`B:\VRChatContent\proj1`, `C:\stuff\proj2`}
	settingsDir := filepath.Join(la, "VRChatCreatorCompanion")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(struct {
		UserProjects []string `json:"userProjects"`
	}{want})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settingsDir, "settings.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}

	got := DiscoverVCCProjects()
	if len(got) != len(want) {
		t.Fatalf("got %d projects, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("project[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	// Missing file → nil.
	t.Setenv("LOCALAPPDATA", t.TempDir())
	if DiscoverVCCProjects() != nil {
		t.Error("expected nil for missing settings.json")
	}
}

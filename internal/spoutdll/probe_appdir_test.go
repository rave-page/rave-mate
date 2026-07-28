package spoutdll

import (
	"os"
	"path/filepath"
	"testing"

	"rave.page/mate/internal/appdir"
)

// TestProbeFindsSidecarBesideTheInstalledExe reproduces #49: in a featurehost child, os.Executable()
// is a per-role hardlink under %LocalAppData%\rave-mate\proc, so probing "beside the exe" looked in
// a directory that never holds SpoutLibrary.dll. Every receive then failed with "video share
// unavailable" regardless of source (Spout AND webcam), and only worked when the inherited current
// directory happened to be the install dir.
func TestProbeFindsSidecarBesideTheInstalledExe(t *testing.T) {
	install := t.TempDir()
	dll := filepath.Join(install, DLLName)
	if err := os.WriteFile(dll, []byte("not a real dll"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Point the managed bin dir somewhere empty so only the install dir can satisfy the probe.
	t.Setenv("RAVE_MATE_CONFIG_DIR", t.TempDir())
	t.Setenv(appdir.EnvVar, install)

	st := Probe()
	if !st.Installed {
		t.Fatalf("Probe() reported not installed with %s present in the published app dir", DLLName)
	}
	if st.Path != dll {
		t.Errorf("Probe().Path = %q, want %q", st.Path, dll)
	}
}

// TestCandidatePathsPrefersInstallDir pins the order: install dir first, managed bin dir second.
// Both are preloaded by absolute path, so the order decides which copy wins when both exist.
func TestCandidatePathsPrefersInstallDir(t *testing.T) {
	install := t.TempDir()
	t.Setenv(appdir.EnvVar, install)
	t.Setenv("RAVE_MATE_CONFIG_DIR", t.TempDir())

	ps := candidatePaths()
	if len(ps) == 0 {
		t.Fatal("candidatePaths() is empty")
	}
	if want := filepath.Join(install, DLLName); ps[0] != want {
		t.Errorf("candidatePaths()[0] = %q, want %q", ps[0], want)
	}
	for _, p := range ps {
		if p == filepath.Join(mustExeDir(t), DLLName) && filepath.Dir(p) != install {
			t.Errorf("candidate %q still points beside THIS process's exe (the hardlink trap)", p)
		}
	}
}

func mustExeDir(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("os.Executable: %v", err)
	}
	return filepath.Dir(exe)
}

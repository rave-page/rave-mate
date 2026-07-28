package appdir

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDirPrefersPublishedValueOverOwnExe(t *testing.T) {
	// The defect this guards: a featurehost child's os.Executable() is a per-role HARDLINK in the
	// proc cache dir, which holds no sidecar DLLs. The published install dir must win.
	want := filepath.Join(t.TempDir(), "install")
	t.Setenv(EnvVar, want)
	if got := Dir(); got != want {
		t.Errorf("Dir() = %q, want the published dir %q", got, want)
	}
	exeDir := ""
	if exe, err := os.Executable(); err == nil {
		exeDir = filepath.Dir(exe)
	}
	if got := Dir(); got == exeDir && exeDir != want {
		t.Errorf("Dir() fell back to this process's exe dir %q despite %s being set", exeDir, EnvVar)
	}
}

func TestDirFallsBackToOwnExeDir(t *testing.T) {
	t.Setenv(EnvVar, "")
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("os.Executable: %v", err)
	}
	if got, want := Dir(), filepath.Dir(exe); got != want {
		t.Errorf("Dir() = %q, want %q", got, want)
	}
}

func TestDirIgnoresBlankValue(t *testing.T) {
	// A blank/whitespace value must not resolve sidecars to the filesystem root.
	t.Setenv(EnvVar, "   ")
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("os.Executable: %v", err)
	}
	if got, want := Dir(), filepath.Dir(exe); got != want {
		t.Errorf("Dir() = %q, want the exe dir %q", got, want)
	}
}

func TestPublishIsInheritanceSafe(t *testing.T) {
	// A child inherits the daemon's value; if the child also calls Publish it must NOT overwrite it
	// with its own hardlink dir - that would reintroduce the bug one process deeper.
	parent := filepath.Join(t.TempDir(), "install")
	t.Setenv(EnvVar, parent)
	Publish()
	if got := os.Getenv(EnvVar); got != parent {
		t.Errorf("Publish clobbered the inherited value: got %q, want %q", got, parent)
	}
}

func TestPublishSetsOwnExeDirWhenUnset(t *testing.T) {
	t.Setenv(EnvVar, "")
	Publish()
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("os.Executable: %v", err)
	}
	if got, want := os.Getenv(EnvVar), filepath.Dir(exe); got != want {
		t.Errorf("Publish set %q, want %q", got, want)
	}
}

func TestSidecarPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvVar, dir)
	if got, want := SidecarPath("SpoutLibrary.dll"), filepath.Join(dir, "SpoutLibrary.dll"); got != want {
		t.Errorf("SidecarPath = %q, want %q", got, want)
	}
}

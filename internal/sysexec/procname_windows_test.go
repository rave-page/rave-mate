//go:build windows

package sysexec

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSanitizeLabel(t *testing.T) {
	cases := map[string]string{
		"feature-player": "feature-player",
		"worker-probe":   "worker-probe",
		"a b:c/d\\e":     "a-b-c-d-e",
	}
	for in, want := range cases {
		if got := sanitizeLabel(in); got != want {
			t.Errorf("sanitizeLabel(%q)=%q want %q", in, got, want)
		}
	}
}

func TestEnsureProcLink(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "rave-mate.exe")
	if err := os.WriteFile(exe, []byte("v1-binary-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	link := ensureProcLinkIn(dir, exe, "feature-player")
	if link == "" {
		t.Fatal("first ensureProcLinkIn returned empty")
	}
	if filepath.Base(link) != "rave-mate-feature-player.exe" {
		t.Errorf("link name = %q", filepath.Base(link))
	}
	li, err := os.Stat(link)
	if err != nil {
		t.Fatal(err)
	}
	ei, _ := os.Stat(exe)
	if !os.SameFile(ei, li) {
		t.Error("link is not a hardlink of exe")
	}

	// idempotent: second call returns the same path, no error
	if l2 := ensureProcLinkIn(dir, exe, "feature-player"); l2 != link {
		t.Errorf("second call = %q want %q", l2, link)
	}

	// after an "app update" (exe replaced → fresh inode), the stale link must be refreshed.
	if err := os.Remove(exe); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("v2-different-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	l3 := ensureProcLinkIn(dir, exe, "feature-player")
	if l3 == "" {
		t.Fatal("refresh returned empty")
	}
	li3, _ := os.Stat(l3)
	ei2, _ := os.Stat(exe)
	if !os.SameFile(ei2, li3) {
		t.Error("refreshed link is not a hardlink of the new exe")
	}
}

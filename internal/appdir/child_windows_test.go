//go:build windows

// External test package so it can import sysexec (the package that creates the hardlinks) without
// making the production appdir package depend on it.
package appdir_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"rave.page/mate/internal/appdir"
	"rave.page/mate/internal/spoutdll"
	"rave.page/mate/internal/sysexec"
)

const childEnv = "RAVE_MATE_APPDIR_CHILD_TEST"

// TestMain doubles as the child: re-executed through sysexec.Named it reports what it resolved.
func TestMain(m *testing.M) {
	if os.Getenv(childEnv) == "1" {
		exe, _ := os.Executable()
		dll := "absent"
		if st := spoutdll.Probe(); st.Installed {
			dll = st.Path
		}
		// One line per fact so the parent can assert on each.
		os.Stdout.WriteString("exe=" + exe + "\n")
		os.Stdout.WriteString("appdir=" + appdir.Dir() + "\n")
		os.Stdout.WriteString("dll=" + dll + "\n")
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// TestHardlinkedChildResolvesInstallDirSidecar is the end-to-end guard for #49. It launches this
// test binary the way every featurehost child is launched - through sysexec.Named, i.e. from a
// per-role HARDLINK in the proc cache dir - and asserts the child still finds a sidecar DLL sitting
// beside the INSTALLED exe. Before the fix the child probed beside its own hardlink and found
// nothing, so video share failed for every source and only worked when the inherited current
// directory happened to be the install dir.
func TestHardlinkedChildResolvesInstallDirSidecar(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Skipf("os.Executable: %v", err)
	}
	root := t.TempDir()
	install := filepath.Join(root, "Programs", "rave-mate")
	if err := os.MkdirAll(install, 0o755); err != nil {
		t.Fatal(err)
	}
	// A fake "installed exe" (copy of this test binary) + the sidecar beside it, as the installer
	// ships them. Hardlinks require the same volume, which TempDir satisfies.
	installedExe := filepath.Join(install, "rave-mate.exe")
	if err := copyFile(self, installedExe); err != nil {
		t.Skipf("copy test binary: %v", err)
	}
	dllPath := filepath.Join(install, spoutdll.DLLName)
	if err := os.WriteFile(dllPath, []byte("not a real dll"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Private proc-link dir and a cwd with NO sidecar, so a cwd-based load cannot mask a failure.
	localAppData := filepath.Join(root, "LocalAppData")
	cwd := filepath.Join(root, "elsewhere")
	for _, d := range []string{localAppData, cwd} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Must be set in THIS process: sysexec.Named picks the proc dir via os.UserCacheDir() in the
	// PARENT, so setting it only in cmd.Env would hardlink into the running app's real proc dir.
	t.Setenv("LOCALAPPDATA", localAppData)

	cmd := exec.Command(installedExe)
	sysexec.Named(cmd, "feature-appdirtest") // the hardlink relaunch under test
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(),
		childEnv+"=1",
		"RAVE_MATE_CONFIG_DIR="+filepath.Join(root, "cfg"), // managed bin dir: empty
		appdir.EnvVar+"="+install,                          // what daemon Publish() hands down
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
	got := parseKV(string(out))

	if got["exe"] == "" {
		t.Fatalf("child reported no exe path; output:\n%s", out)
	}
	// Precondition: the mechanism really is in play. If Named silently fell back to the real exe
	// there is no hardlink and the test proves nothing, so require the redirect.
	if filepath.Dir(got["exe"]) == install {
		t.Skipf("sysexec.Named did not redirect to a hardlink (exe=%s) - nothing to prove", got["exe"])
	}
	if want := filepath.Join(localAppData, "rave-mate", "proc"); !strings.EqualFold(filepath.Dir(got["exe"]), want) {
		t.Fatalf("child exe = %q, expected it under the proc dir %q", got["exe"], want)
	}
	if !strings.EqualFold(got["appdir"], install) {
		t.Errorf("child appdir.Dir() = %q, want the install dir %q", got["appdir"], install)
	}
	if !strings.EqualFold(got["dll"], dllPath) {
		t.Errorf("child Probe() = %q, want %q (the sidecar beside the installed exe)", got["dll"], dllPath)
	}
}

// TestHardlinkedChildWithoutPublishedDirMissesTheSidecar reproduces the ORIGINAL defect, so the
// test above cannot silently stop proving anything: with nothing published, the child falls back to
// its own exe dir - the proc hardlink dir - and the installed sidecar is invisible. This is exactly
// what shipped, and why the DLL appeared to load "sometimes" (only when cwd was the install dir).
func TestHardlinkedChildWithoutPublishedDirMissesTheSidecar(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Skipf("os.Executable: %v", err)
	}
	root := t.TempDir()
	install := filepath.Join(root, "Programs", "rave-mate")
	if err := os.MkdirAll(install, 0o755); err != nil {
		t.Fatal(err)
	}
	installedExe := filepath.Join(install, "rave-mate.exe")
	if err := copyFile(self, installedExe); err != nil {
		t.Skipf("copy test binary: %v", err)
	}
	if err := os.WriteFile(filepath.Join(install, spoutdll.DLLName), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	localAppData := filepath.Join(root, "LocalAppData")
	cwd := filepath.Join(root, "elsewhere")
	for _, d := range []string{localAppData, cwd} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("LOCALAPPDATA", localAppData)

	cmd := exec.Command(installedExe)
	sysexec.Named(cmd, "feature-appdirtest2")
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(),
		childEnv+"=1",
		"RAVE_MATE_CONFIG_DIR="+filepath.Join(root, "cfg"),
		appdir.EnvVar+"=", // NOT published - the pre-fix world
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
	got := parseKV(string(out))
	if filepath.Dir(got["exe"]) == install {
		t.Skip("sysexec.Named did not redirect to a hardlink - nothing to reproduce")
	}
	if got["dll"] != "absent" {
		t.Errorf("child found %q without a published app dir; the fix is no longer what makes the "+
			"happy-path test pass", got["dll"])
	}
}

func parseKV(s string) map[string]string {
	m := map[string]string{}
	for ln := range strings.SplitSeq(s, "\n") {
		if k, v, ok := strings.Cut(strings.TrimRight(ln, "\r"), "="); ok {
			m[k] = v
		}
	}
	return m
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o755)
}

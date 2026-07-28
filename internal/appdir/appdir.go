// Package appdir resolves the directory holding the app's REAL executable - the one the installer
// drops runtime-loaded sidecars beside (SpoutLibrary.dll, openvr_api.dll).
//
// os.Executable() is NOT that directory in a child process: sysexec.Named relaunches every
// featurehost/worker child from a per-role hardlink in %LocalAppData%\rave-mate\proc so it gets a
// distinct image name in Task Manager. A child asking os.Executable() therefore gets the proc dir,
// which holds no sidecars - so "beside the exe" silently meant "beside the hardlink", every
// sidecar probe in a child said not-installed, and the DLLs only ever loaded because Windows also
// searches the CURRENT DIRECTORY, which happened to be the install dir when the app was launched
// from its shortcut. A self-update relaunch (or any launcher with a different cwd) broke video
// share completely - the field symptom was "receiving" with no media on every source (#49).
//
// The daemon Publish()es its own exe dir; children inherit it through the environment, so this
// works for grandchildren too and does not depend on how each spawn site builds cmd.Env.
package appdir

import (
	"os"
	"path/filepath"
	"strings"
)

// EnvVar carries the install dir from the daemon to every descendant.
const EnvVar = "RAVE_MATE_APP_DIR"

// Publish records this process's exe dir for children to inherit. Idempotent and inheritance-safe:
// an already-set value wins, so a child that calls this cannot overwrite the daemon's value with
// its own hardlink dir.
func Publish() {
	if strings.TrimSpace(os.Getenv(EnvVar)) != "" {
		return
	}
	if exe, err := os.Executable(); err == nil {
		_ = os.Setenv(EnvVar, filepath.Dir(exe))
	}
}

// Dir returns the app's install directory ("" only if it cannot be determined).
func Dir() string {
	if d := strings.TrimSpace(os.Getenv(EnvVar)); d != "" {
		return d
	}
	if exe, err := os.Executable(); err == nil {
		return filepath.Dir(exe)
	}
	return ""
}

// SidecarPath is Dir() joined with a runtime-loaded sidecar's filename ("" if Dir is unknown).
func SidecarPath(name string) string {
	d := Dir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, name)
}

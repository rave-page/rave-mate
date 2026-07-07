package ui

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"rave.page/mate/internal/sysexec"
)

// unsafeArg rejects empty paths or ones that would be smuggled as a CLI flag (leading
// "-"). Library paths are absolute (volume/`/` prefixed) so they never lead with "-";
// this guards against a crafted/imported NML path injecting an argv flag into the opener.
func unsafeArg(p string) bool { return p == "" || strings.HasPrefix(p, "-") }

// startHidden launches a console opener with no flashing console window (Windows GUI app).
func startHidden(name string, args ...string) {
	cmd := exec.Command(name, args...)
	sysexec.Hide(cmd)
	_ = cmd.Start()
}

// openFile opens a file with the OS default application (playback for media).
func openFile(path string) {
	if unsafeArg(path) {
		return
	}
	switch runtime.GOOS {
	case "windows":
		startHidden("rundll32", "url.dll,FileProtocolHandler", path)
	case "darwin":
		startHidden("open", path) // `open` ignores `--`; the leading-dash guard above covers it
	default:
		startHidden("xdg-open", "--", path)
	}
}

// openDir opens a folder in the OS file manager. On Windows `explorer <dir>` is the reliable
// way (rundll32 FileProtocolHandler, used by openFile, doesn't open folders dependably).
func openDir(dir string) {
	if unsafeArg(dir) {
		return
	}
	switch runtime.GOOS {
	case "windows":
		startHidden("explorer", dir) // explorer exits 1 even on success; error is ignored
	case "darwin":
		startHidden("open", dir)
	default:
		startHidden("xdg-open", "--", dir)
	}
}

// revealFile shows a file in the OS file manager (selected when supported).
func revealFile(path string) {
	if unsafeArg(path) {
		return
	}
	switch runtime.GOOS {
	case "windows":
		startHidden("explorer", "/select,"+path)
	case "darwin":
		startHidden("open", "-R", path)
	default:
		dir := filepath.Dir(path)
		if unsafeArg(dir) {
			return
		}
		startHidden("xdg-open", "--", dir)
	}
}

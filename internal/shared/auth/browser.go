package auth

import (
	"os/exec"
	"runtime"
)

// openBrowser opens url in the user's default browser. Uses the OS handler so the existing
// browser session (and its Zitadel cookie) is reused - that's the whole point of the bridge
// flow. hideCmd suppresses a console flash on Windows (GUI-subsystem host spawning rundll32).
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		// rundll32 avoids cmd.exe quoting pitfalls with & in query strings.
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	hideCmd(cmd)
	return cmd.Start()
}

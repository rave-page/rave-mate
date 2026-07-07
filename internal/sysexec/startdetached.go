package sysexec

import (
	"fmt"
	"os/exec"
)

// StartDetached launches a GUI app fully detached from this process - deliberately NOT assigned
// to the kill-on-close job and NOT console-hidden. The child is released immediately (no Wait),
// so it keeps running after rave-mate exits or crashes. This is the crash-recovery path for
// relaunching a DJ-rig app set: recovered apps (SteamVR, Parsec, VRChat) MUST outlive rave-mate.
// workDir "" inherits this process's working directory.
func StartDetached(path string, args []string, workDir string) error {
	if path == "" {
		return fmt.Errorf("sysexec: empty path")
	}
	c := exec.Command(path, args...)
	c.Dir = workDir
	if err := c.Start(); err != nil {
		return err
	}
	// Release the child handle without waiting (fire-and-forget); no job assignment on purpose.
	_ = c.Process.Release()
	return nil
}

//go:build windows

package auth

import (
	"os/exec"
	"syscall"
)

const createNoWindow = 0x08000000 // CREATE_NO_WINDOW

// hideCmd stops a console window from flashing when a GUI-subsystem process spawns a
// console helper (inlined from rave-mate's sysexec.Hide to keep shared dep-free).
func hideCmd(c *exec.Cmd) {
	if c.SysProcAttr == nil {
		c.SysProcAttr = &syscall.SysProcAttr{}
	}
	c.SysProcAttr.HideWindow = true
	c.SysProcAttr.CreationFlags |= createNoWindow
}

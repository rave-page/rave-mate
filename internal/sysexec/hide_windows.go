//go:build windows

package sysexec

import (
	"os/exec"
	"syscall"
)

const (
	createNoWindow    = 0x08000000 // CREATE_NO_WINDOW
	idlePriorityClass = 0x00000040 // IDLE_PRIORITY_CLASS
)

// Hide stops a console window from popping up when this GUI-subsystem process spawns a
// console tool (ffmpeg / ffprobe / ffplay / fpcalc / taskkill / shell openers).
func Hide(c *exec.Cmd) {
	if c.SysProcAttr == nil {
		c.SysProcAttr = &syscall.SysProcAttr{}
	}
	c.SysProcAttr.HideWindow = true
	c.SysProcAttr.CreationFlags |= createNoWindow
}

// LowPriority starts c in Windows' idle priority class.
func LowPriority(c *exec.Cmd) {
	if c.SysProcAttr == nil {
		c.SysProcAttr = &syscall.SysProcAttr{}
	}
	c.SysProcAttr.CreationFlags |= idlePriorityClass
}

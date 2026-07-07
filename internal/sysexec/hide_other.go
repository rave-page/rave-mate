//go:build !windows

package sysexec

import "os/exec"

// Hide is a no-op off Windows (console-window allocation is a Windows-only behaviour).
func Hide(c *exec.Cmd) {}

// LowPriority is a no-op off Windows for now.
func LowPriority(c *exec.Cmd) {}

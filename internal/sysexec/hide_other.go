//go:build !windows && !linux

package sysexec

import "os/exec"

// Hide is a no-op off Windows (console-window allocation is a Windows-only behaviour). Linux has
// its own hide_linux.go that repurposes this pre-start seam for process-group + Pdeathsig setup.
func Hide(c *exec.Cmd) {}

// LowPriority is a no-op off Windows for now.
func LowPriority(c *exec.Cmd) {}

// BelowNormalPriority is a no-op off Windows for now.
func BelowNormalPriority(c *exec.Cmd) {}

//go:build !windows

package auth

import "os/exec"

// hideCmd is a no-op off Windows (no console-flash problem).
func hideCmd(c *exec.Cmd) {}

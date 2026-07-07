//go:build darwin

package sysnotify

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// osSend shows a notification via osascript (best-effort, ~5s cap). No shell is involved (argv), so
// only the AppleScript string literal needs escaping (backslash + double-quote).
func osSend(title, body string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	script := "display notification \"" + esc(body) + "\" with title \"" + esc(title) + "\""
	return exec.CommandContext(ctx, "osascript", "-e", script).Run()
}

func esc(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	return strings.ReplaceAll(s, "\"", "\\\"")
}

//go:build linux

package sysnotify

import (
	"context"
	"os/exec"
	"time"
)

// osSend shows a notification via notify-send (argv - no shell, no injection). "--" stops option
// parsing so a title/body starting with "-" isn't read as a flag. Errors (e.g. notify-send absent)
// are returned for the caller to ignore.
func osSend(title, body string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "notify-send", "--", title, body).Run()
}

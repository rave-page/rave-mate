//go:build !windows && !darwin && !linux

package sysnotify

// osSend is a no-op on platforms without a known desktop-notification mechanism.
func osSend(title, body string) error { return nil }

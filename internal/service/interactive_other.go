//go:build !windows

package service

// interactive: non-Windows services are user-scoped (systemd --user / launchd LaunchAgent),
// so no elevation is needed - run fn directly.
func interactive(_ string, fn func() error) error { return fn() }

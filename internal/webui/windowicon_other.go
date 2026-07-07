//go:build !windows

package webui

// Window icon comes from the platform bundle on macOS/Linux; only Windows needs WM_SETICON.
func setWindowIcon(uintptr) {}

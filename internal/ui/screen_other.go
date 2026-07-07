//go:build !windows

package ui

// screenSizeDIP is Windows-only; elsewhere the caller falls back to a fixed default size.
func screenSizeDIP() (w, h float32, ok bool) { return 0, 0, false }

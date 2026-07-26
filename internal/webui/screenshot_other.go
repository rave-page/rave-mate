//go:build !windows

package webui

import "fmt"

// OS window capture is Windows-only for now (macOS/Linux webview capture is a follow-up).
func showWindow(uintptr)   {}
func revealWindow(uintptr) {}

func (u *UI) captureRegionLocal(path string, x, y, w, h int) error {
	return captureHWND(0, path, x, y, w, h)
}

func captureHWND(_ uintptr, _ string, _, _, _, _ int) error {
	return fmt.Errorf("webview screenshot unsupported on this platform")
}

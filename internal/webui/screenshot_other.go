//go:build !windows

package webui

import "fmt"

// OS window capture is Windows-only for now (macOS/Linux webview capture is a follow-up).
func showWindow(uintptr) {}

func (u *UI) captureRegion(path string, x, y, w, h int) error {
	return fmt.Errorf("webview screenshot unsupported on this platform")
}

//go:build !cgo || !windows

package webui

// shellAvailable is false unless this is a cgo Windows build. The webview host needs the native
// WebView2 (cgo) binding, which we only compile on Windows; every other target (any non-Windows
// OS, or a pure-Go cross-compile) falls back to Fyne. Keeping the host Windows-only also stops the
// linux CI jobs (lint/build:linux/test) from dragging in webkit2gtk.
const shellAvailable = false

func newShell(_ string, _, _ int, _ func(string), _ func()) (shell, bool) { return nil, false }

// probeWebview reports whether the webview runtime is usable. Always false off the cgo-Windows
// path (the host isn't compiled in), so the seam picks Fyne.
func probeWebview() bool { return false }

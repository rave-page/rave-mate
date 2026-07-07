//go:build !windows

package tray

// Start is a no-op off Windows - macOS requires the tray on the main thread (taken by the webview)
// and Linux needs a StatusNotifierItem/AppIndicator dep. The returned stop is safe to call.
func Start(Options) (stop func(), err error) { return func() {}, nil }

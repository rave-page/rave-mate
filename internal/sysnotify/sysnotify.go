// Package sysnotify sends a best-effort native OS desktop notification (stdlib only). A failed
// send never blocks, panics, or crashes the caller - callers may discard the returned error.
package sysnotify

import "sync"

var (
	mu     sync.RWMutex
	native func(title, body string) error // reliable platform path (e.g. Windows tray balloon)
)

// SetNative registers a platform-reliable notification path, overriding the generic osSend
// fallback (the Windows tray registers its Shell_NotifyIcon balloon here). Pass nil to clear.
func SetNative(fn func(title, body string) error) {
	mu.Lock()
	native = fn
	mu.Unlock()
}

// Send shows a native desktop notification (best-effort). Uses the registered native path if set,
// else the per-OS fallback. Returns the underlying error for logging; callers may ignore it.
func Send(title, body string) error {
	mu.RLock()
	fn := native
	mu.RUnlock()
	if fn != nil {
		return fn(title, body)
	}
	return osSend(title, body)
}

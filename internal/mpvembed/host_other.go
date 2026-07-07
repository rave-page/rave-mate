//go:build !windows

package mpvembed

import "errors"

// Host is a no-op on non-Windows platforms (window embedding unsupported → caller uses the popout).
type Host struct{}

// Supported reports whether window embedding is available (non-Windows: no).
func Supported() bool { return false }

// Create always fails off Windows; the caller falls back to mpv's own window.
func Create(parent uintptr) (*Host, error) { return nil, errors.New("mpvembed: unsupported platform") }

// CreateHosted always fails off Windows (see Create).
func CreateHosted(parent uintptr) (*Host, error) { return Create(parent) }

// WID returns 0 (no host window).
func (h *Host) WID() uintptr { return 0 }

// Move is a no-op.
func (h *Host) Move(x, y, w, ht int) {}

// Show is a no-op.
func (h *Host) Show() {}

// Hide is a no-op.
func (h *Host) Hide() {}

// Destroy is a no-op.
func (h *Host) Destroy() {}

//go:build !abletonlink || !cgo

package abletonlink

// NewLink returns ErrUnavailable in the default build (no `abletonlink` tag, or cgo off).
// The featurehost child falls back to reporting the feature unavailable; the daemon and UI
// stay fully functional. Build with `-tags abletonlink` (cgo on) for the real backend.
func NewLink(quantum float64) (Session, error) { return nil, ErrUnavailable }

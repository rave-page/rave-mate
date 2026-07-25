//go:build !zigvr || !cgo

// Stub when built without -tags zigvr: vroverlay keeps the pure-Go raster path.
package zigvr

import "errors"

// Available reports the Zig raster lib is linked (never, in stub builds).
func Available() bool { return false }

// Render always errors in stub builds (callers gate on Available first).
func Render(pix []byte, w, h int, l *List) error { return errors.New("zigvr: not built") }

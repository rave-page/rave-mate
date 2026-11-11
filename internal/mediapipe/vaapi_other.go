//go:build !linux

package mediapipe

import "context"

// vaapi_other.go - VA-API render-node selection is Linux-only; everywhere else these are no-ops.
// A non-Linux ffmpeg build normally has no *_vaapi encoders, so these are never reached in
// practice - they exist so the shared probe/encode code compiles on every target.

func ensureVAAPINode(context.Context, string) string { return "" }

func vaapiDevice() string { return "" }

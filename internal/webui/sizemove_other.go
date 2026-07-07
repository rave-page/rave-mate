//go:build !windows

package webui

// Only Windows has a modal size-move loop worth pausing ticks for.
func inSizeMove() bool            { return false }
func installSizeMoveHook(uintptr) {}

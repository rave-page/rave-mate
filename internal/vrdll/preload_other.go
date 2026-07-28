//go:build !windows

package vrdll

// Preload is Windows-only: elsewhere the loader takes openvr from the system paths / rpath.
func Preload() {}

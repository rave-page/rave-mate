//go:build !windows

package webcam

import "errors"

var errUVCWindowsOnly = errors.New("webcam: UVC control is Windows-only (DirectShow)")

// uvcProps is the non-Windows stub.
func uvcProps(string) ([]PropState, error) { return nil, errUVCWindowsOnly }

// uvcSet is the non-Windows stub.
func uvcSet(string, string, int32, bool) error { return errUVCWindowsOnly }

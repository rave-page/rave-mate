//go:build !windows

package senderkeeper

// ExeName is the keeper sidecar name (Windows-only feature).
const ExeName = "rave-mate-keeper"

// PipeName is empty off Windows (Spout is Windows-only).
func PipeName() string { return "" }

// DialPipe is unavailable off Windows.
func DialPipe() (Conn, error) { return nil, ErrUnavailable }

// StartDetached is unavailable off Windows.
func StartDetached() error { return ErrUnavailable }

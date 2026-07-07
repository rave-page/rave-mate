//go:build !windows

package service

import "context"

// RunWindowsServiceIfNeeded is a no-op off Windows (the SCM doesn't exist).
func RunWindowsServiceIfNeeded(func(ctx context.Context) error) bool { return false }

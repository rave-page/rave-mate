//go:build !windows

package gpuwatch

import "context"

// start is a no-op off Windows (TDR + hung-window detection are Win32-specific).
func start(ctx context.Context, opt Options) {}

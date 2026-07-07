//go:build !windows

package rekordboxsrc

import (
	"context"
	"runtime"

	"rave.page/mate/internal/session"
)

// runMemory is unsupported off Windows: process-memory reads need Windows OpenProcess +
// ReadProcessMemory. Logs once and idles so enabling memoryRead elsewhere is harmless.
func (s *Source) runMemory(ctx context.Context, _ func(session.Observation)) {
	s.log.Warn(logSource, "memory read not supported on "+runtime.GOOS+" - Windows only", nil)
	<-ctx.Done()
}

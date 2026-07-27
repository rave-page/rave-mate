//go:build !spout

package videoshare

import "errors"

// RecvDiagSample / RecvDiag mirror the spout surface so callers compile everywhere.
type RecvDiagSample struct {
	RecvOK, Updated, FrameNew, Connected bool
	SenderW, SenderH, SenderFmt          uint32
	SenderCPU, SenderGLDX                bool
	SenderFrame                          int64
	SenderHandle                         uint64
	Canary, Zeros, Other                 int
}

func RecvDiag(string, int, int, int, byte, func()) ([]RecvDiagSample, error) {
	return nil, errors.New("no video-share backend compiled in (build -tags spout)")
}

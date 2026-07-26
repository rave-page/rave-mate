//go:build !windows || !cgo

// Package mfenc (stub): the Media Foundation hardware encoder exists only on Windows
// cgo builds; everywhere else the ffmpeg child path is the sole encoder.
package mfenc

import "errors"

// ErrUnsupported marks the non-Windows / non-cgo stub.
var ErrUnsupported = errors.New("mfenc: unsupported on this platform")

// AU is one encoded access unit (annex-B).
type AU struct {
	Data     []byte
	PTSNs    int64
	Keyframe bool
}

// Encoder is unavailable on this platform.
type Encoder struct{}

func Available() bool { return false }

func New(inW, inH, outW, outH int, fps float64, bitrateKbps, gopFrames int) (*Encoder, error) {
	return nil, ErrUnsupported
}

// NewOn is unavailable on this platform (no Media Foundation, no adapter pinning).
func NewOn(adapterLUID int64, inW, inH, outW, outH int, fps float64, bitrateKbps, gopFrames int) (*Encoder, error) {
	return nil, ErrUnsupported
}

func (e *Encoder) Output() <-chan AU              { return nil }
func (e *Encoder) Name() string                   { return "" }
func (e *Encoder) InputIsBGRA() bool              { return false }
func (e *Encoder) Encode(_ []byte, _ int64) error { return ErrUnsupported }
func (e *Encoder) ForceKeyframe()                 {}
func (e *Encoder) Close()                         {}

// Warnf is the supervisor warning seam (no-op off Windows).
var Warnf = func(format string, args ...any) {}

// ProcSession is unavailable on this platform (no Zig MF encoder child).
type ProcSession struct{}

func OpenProcSession(int64, int, int, int, int, float64, int, int) (*ProcSession, error) {
	return nil, ErrUnsupported
}

func (s *ProcSession) Encode(_ []byte, _ int64) error { return ErrUnsupported }
func (s *ProcSession) Output() <-chan AU              { return nil }
func (s *ProcSession) ForceKeyframe()                 {}
func (s *ProcSession) SetBitrate(int)                 {}
func (s *ProcSession) Close()                         {}
func (s *ProcSession) Name() string                   { return "" }
func (s *ProcSession) InputIsBGRA() bool              { return false }
func (s *ProcSession) Stats() ProcStats               { return ProcStats{} }

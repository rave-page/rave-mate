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

func (e *Encoder) Output() <-chan AU              { return nil }
func (e *Encoder) Name() string                   { return "" }
func (e *Encoder) InputIsBGRA() bool              { return false }
func (e *Encoder) Encode(_ []byte, _ int64) error { return ErrUnsupported }
func (e *Encoder) ForceKeyframe()                 {}
func (e *Encoder) Close()                         {}

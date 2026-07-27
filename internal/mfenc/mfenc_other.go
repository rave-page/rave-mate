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

// ChildAvailable: no native engine / encoder child on this platform.
func ChildAvailable() bool { return false }

// RefreshChildAvailable: no-op stub.
func RefreshChildAvailable() bool { return false }

// HasEmbeddedChild: never on this platform.
func HasEmbeddedChild() bool { return false }

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

// SpoutSource / ProcOpts mirror the Windows surface so callers compile everywhere.
type SpoutSource struct {
	Name    string
	Resolve func() (handle uint64, dxgiFormat uint32, w, h int, ok bool)
}

type ProcOpts struct {
	LUID                 int64
	InW, InH, OutW, OutH int
	FPS                  float64
	Kbps, Gop            int
	Spout                *SpoutSource
}

// ErrZeroCopyRefused never fires here (no zero-copy path off Windows).
var ErrZeroCopyRefused = errors.New("mfenc: zero-copy source refused")

func OpenProcSession(int64, int, int, int, int, float64, int, int) (*ProcSession, error) {
	return nil, ErrUnsupported
}

func OpenProcSessionOpts(ProcOpts) (*ProcSession, error) { return nil, ErrUnsupported }

// ZeroCopyPinnedToReadback: nothing to pin without a zero-copy path.
func ZeroCopyPinnedToReadback(string) bool { return false }

func (s *ProcSession) IsZeroCopy() bool { return false }

func (s *ProcSession) Encode(_ []byte, _ int64) error { return ErrUnsupported }
func (s *ProcSession) Output() <-chan AU              { return nil }
func (s *ProcSession) ForceKeyframe()                 {}
func (s *ProcSession) SetBitrate(int)                 {}
func (s *ProcSession) Close()                         {}
func (s *ProcSession) Name() string                   { return "" }
func (s *ProcSession) InputIsBGRA() bool              { return false }
func (s *ProcSession) Stats() ProcStats               { return ProcStats{} }

// ── receive side (zigmedia inc 2) - stubs: no Media Foundation decoder here either ──

// DecodeDest / ProcDecOpts / ProcDecStats mirror the Windows surface so callers compile everywhere.
type DecodeDest struct {
	Name    string
	Resolve func() (handle uint64, dxgiFormat uint32, w, h int, ok bool)
}

type ProcDecOpts struct {
	LUID       int64
	InW, InH   int
	OutW, OutH int
	FPS        float64
	HEVC       bool
	KbpsHint   int
	Dest       *DecodeDest
}

type ProcDecStats struct {
	Name        string
	DecFrames   uint64
	DecFPS      float64
	DecBusyMs   float64
	InDropped   uint64
	DecDropped  uint64
	DecErrors   uint64
	MtxTimeouts uint64
	DecStaleMs  float64
	DecFlags    uint32
	QueueDepth  int
	Restarts    int
	ChildCPUPct float64
	Downgrades  int
}

// ProcDecSession is unavailable on this platform (no Zig MF decode child).
type ProcDecSession struct{}

// ErrDecodeRefused never fires here (no native decode path off Windows).
var ErrDecodeRefused = errors.New("mfenc: native decode refused")

func OpenProcDecSession(ProcDecOpts) (*ProcDecSession, error) { return nil, ErrUnsupported }

// DecodePinnedToFrames: nothing to pin without a native decode path.
func DecodePinnedToFrames(string) bool { return false }

func (d *ProcDecSession) Decode(_ []byte, _ int64, _ bool) error { return ErrUnsupported }
func (d *ProcDecSession) Stats() ProcDecStats                    { return ProcDecStats{} }
func (d *ProcDecSession) Name() string                           { return "" }
func (d *ProcDecSession) IsHardware() bool                       { return false }
func (d *ProcDecSession) Close()                                 {}

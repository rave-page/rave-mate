//go:build !vr

package vroverlay

import (
	"image"

	"rave.page/mate/internal/vrstats"
)

// stubRuntime is the no-op backend for builds without the `vr` tag. Available() is false so the
// Manager stays idle (and the UI explains a `vr` build + SteamVR are required).
type stubRuntime struct{}

// NewRuntime returns the stub on non-vr builds.
func NewRuntime() Runtime { return stubRuntime{} }

func (stubRuntime) Available() bool                        { return false }
func (stubRuntime) Init() error                            { return nil }
func (stubRuntime) EnsureOverlay(string, string) error     { return nil }
func (stubRuntime) SetTexture(string, *image.NRGBA) error  { return nil }
func (stubRuntime) SetTransform(string, Transform) error   { return nil }
func (stubRuntime) Show(string, bool) error                { return nil }
func (stubRuntime) DestroyOverlay(string) error            { return nil }
func (stubRuntime) Shutdown()                              {}
func (stubRuntime) RuntimeInstalled() bool                 { return false }
func (stubRuntime) PollQuit() QuitReason                   { return QuitNone }
func (stubRuntime) RegisterApp(string, string, bool) error { return nil }
func (stubRuntime) PerfStats() (vrstats.PerfStats, bool)   { return vrstats.PerfStats{}, false }

package app

import (
	"rave.page/mate/internal/featurehost"
	"rave.page/mate/internal/vroverlay"
)

// vrSurface routes the VR overlay control plane (vroverlay.Surface) to whichever backend owns
// OpenVR right now: the supervised `feature vr` child (default on vr builds) or the in-proc
// Manager (fallback: non-vr build, InProc opt-out, or the proxy failed to construct). UI,
// keybinds, and ctl diagnostics call this and never care which mode is live.
type vrSurface struct {
	mgr      *vroverlay.Manager
	proxy    *featurehost.VrOverlayProxy // nil if construction failed
	useProxy func() bool
}

var _ vroverlay.Surface = (*vrSurface)(nil)

func (s *vrSurface) sel() vroverlay.Surface {
	if s.proxy != nil && s.useProxy != nil && s.useProxy() {
		return s.proxy
	}
	return s.mgr
}

func (s *vrSurface) Available() bool                        { return s.sel().Available() }
func (s *vrSurface) BindingStatus() vroverlay.BindingStatus { return s.sel().BindingStatus() }
func (s *vrSurface) OpenBindingUI() error                   { return s.sel().OpenBindingUI() }
func (s *vrSurface) ActionBinding(action string) string     { return s.sel().ActionBinding(action) }
func (s *vrSurface) InputDiag() string                      { return s.sel().InputDiag() }
func (s *vrSurface) ToggleAllOverlays()                     { s.sel().ToggleAllOverlays() }
func (s *vrSurface) ToggleHidden(id string)                 { s.sel().ToggleHidden(id) }
func (s *vrSurface) SetHidden(id string, hidden bool)       { s.sel().SetHidden(id, hidden) }
func (s *vrSurface) RequestEditorToggle()                   { s.sel().RequestEditorToggle() }
func (s *vrSurface) PerfProbe() string                      { return s.sel().PerfProbe() }

package vroverlay

import (
	"encoding/json"

	"rave.page/mate/internal/eventbus"
)

// Surface is the daemon-facing VR overlay control plane: everything the UI, the keybind
// dispatcher, and ctl/peer diagnostics call. Implemented by *Manager (in-proc) and by the
// featurehost VrOverlayProxy (subprocess mode), so call sites don't care where OpenVR runs.
type Surface interface {
	Available() bool
	BindingStatus() BindingStatus
	OpenBindingUI() error
	ActionBinding(action string) string
	InputDiag() string
	ToggleAllOverlays()
	ToggleHidden(id string)
	SetHidden(id string, hidden bool)
	RequestEditorToggle()
	PerfProbe() string
}

var _ Surface = (*Manager)(nil)

// Bus is the pub/sub surface the Manager consumes - *eventbus.Bus in-proc; in the vr feature
// child a pipe bridge that preserves Origin/Local across the subprocess boundary.
type Bus interface {
	Subscribe(topic string, fn func(eventbus.Event)) func()
	Publish(topic string, data json.RawMessage)
}

//go:build !spout && !syphon && !pipewire

package videoshare

import (
	"errors"

	"rave.page/mate/internal/logbus"
)

// backendName is the compiled-in transport label. The default build has no native backend.
const backendName = "none"

// newSender returns the no-op sender: the sink runs (gate + render) but publishes nothing. Build
// with -tags spout|syphon|pipewire (and the platform SDK) for a real transport.
func newSender(_ *logbus.Bus) (Sender, error) { return noopSender{}, nil }

// newFrameSender has no backend in the default build → error so callers fall back (PNG file).
func newFrameSender(_ *logbus.Bus, _ string) (FrameSender, error) {
	return nil, errors.New("no video-share backend compiled in (build -tags spout)")
}

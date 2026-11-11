//go:build !spout && !syphon

package videoshare

import (
	"errors"

	"rave.page/mate/internal/logbus"
)

// backendName is the compiled-in transport label. The default build has no native backend.
// NOTE: `pipewire` is intentionally NOT excluded here. The Linux `-tags pipewire` build is the
// PipeWire video-node CAPTURE source (internal/pwcapture), not a videoshare pipewire SENDER; the
// sender stays the no-op until a real sender_pipewire.go lands, so `-tags pipewire` BUILDS (it was
// unbuildable before: the tag excluded this file but no sender_pipewire.go exists).
const backendName = "none"

// newSender returns the no-op sender: the sink runs (gate + render) but publishes nothing. Build
// with -tags spout|syphon|pipewire (and the platform SDK) for a real transport.
func newSender(_ *logbus.Bus) (Sender, error) { return noopSender{}, nil }

// newFrameSender has no backend in the default build → error so callers fall back (PNG file).
func newFrameSender(_ *logbus.Bus, _ string) (FrameSender, error) {
	return nil, errors.New("no video-share backend compiled in (build -tags spout)")
}

// newSharedSender: no backend → no GPU destination texture, so the native decode session is never
// requested and the ffmpeg decode path stands.
func newSharedSender(_ *logbus.Bus, _ string, _, _ int) (SharedSender, error) {
	return nil, errors.New("no video-share backend compiled in (build -tags spout)")
}

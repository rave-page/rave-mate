package videoshare

import (
	"image"

	"rave.page/mate/internal/logbus"
)

// FrameSender publishes frames under a single fixed sender name over the platform video-share
// backend (Spout). Unlike the per-deck Sender, it carries one arbitrary named stream - used by the
// VRSL DMX grid ("rave-mate-vrsl"). The transport is chosen at build time; the default (no-tag)
// build has no backend and NewFrameSender errors so callers fall back (e.g. to a PNG file).
type FrameSender interface {
	// Send publishes img. CONTRACT: img.Pix is the caller's again the moment Send returns, so an
	// implementation MUST NOT retain it - a backend that uploads on its own thread has to wait for
	// that read (internal/videoshare/handoff.go). mediaroute's receive sink passes mediapipe's
	// decode buffer straight through and the decoder recycles it as soon as Write returns; an
	// implementation that reads later ships torn frames.
	Send(img *image.NRGBA) error
	Close()
}

// NewFrameSender opens a platform video-share sender publishing under name. Errors when no backend
// is compiled in / available (missing SpoutLibrary.dll, no GPU).
func NewFrameSender(log *logbus.Bus, name string) (FrameSender, error) {
	return newFrameSender(log, name)
}

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
	Send(img *image.NRGBA) error // owned by the caller after return
	Close()
}

// NewFrameSender opens a platform video-share sender publishing under name. Errors when no backend
// is compiled in / available (missing SpoutLibrary.dll, no GPU).
func NewFrameSender(log *logbus.Bus, name string) (FrameSender, error) {
	return newFrameSender(log, name)
}

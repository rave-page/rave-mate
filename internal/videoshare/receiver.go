package videoshare

import (
	"image"

	"rave.page/mate/internal/logbus"
)

// receiver.go - platform video-share INGEST (medialink P4): enumerate local senders and pull
// frames from one, the mirror of FrameSender. Backend chosen at build time like the sender
// (spout tag = Windows Spout2; default build has none and degrades to empty/error).

// FrameReceiver pulls frames from one named local video-share sender on its own worker thread.
type FrameReceiver interface {
	// Frames yields received frames, newest-wins (a slow consumer never backs up the poller).
	// The channel closes when the receiver is closed.
	Frames() <-chan *image.NRGBA
	Close()
}

// ListSenders returns the currently registered video-share sender names (nil without a backend).
func ListSenders() []string { return listSenders() }

// SenderSize returns a named sender's current dimensions (ok=false when absent / no backend).
func SenderSize(name string) (w, h int, ok bool) { return senderSize(name) }

// NewFrameReceiver opens a receiver bound to the named sender. Errors when no backend is
// compiled in / the runtime library is missing.
func NewFrameReceiver(log *logbus.Bus, name string) (FrameReceiver, error) {
	return newFrameReceiver(log, name)
}

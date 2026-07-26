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

// FPSLimiter is an optional FrameReceiver extension: change the capture rate live. Implemented by
// backends whose poll loop can skip the readback (Spout). A shared capture uses it to run at the
// highest rate any of its consumers still wants.
type FPSLimiter interface {
	SetMaxFPS(fps float64)
}

// RecvOptions tunes a receiver. Zero value = previous behaviour (uncapped).
type RecvOptions struct {
	// MaxFPS caps the CAPTURE rate: over-budget polls skip ReceiveImage entirely, so a 120 fps
	// source capped to 60 pays 60 readbacks/s instead of 120. <= 0 = uncapped.
	MaxFPS float64
}

// NewFrameReceiver opens an uncapped receiver bound to the named sender. Errors when no backend is
// compiled in / the runtime library is missing.
func NewFrameReceiver(log *logbus.Bus, name string) (FrameReceiver, error) {
	return newFrameReceiver(log, name, RecvOptions{})
}

// NewFrameReceiverOpts is NewFrameReceiver with a capture-rate cap (see RecvOptions).
func NewFrameReceiverOpts(log *logbus.Bus, name string, o RecvOptions) (FrameReceiver, error) {
	return newFrameReceiver(log, name, o)
}

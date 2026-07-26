package videoshare

import (
	"errors"

	"rave.page/mate/internal/logbus"
)

// errShareDims: geometry outside the frame bounds (MaxFrameDim / MaxFrameBytes).
var errShareDims = errors.New("videoshare: shared sender geometry out of bounds")

// sharedsender.go - a FrameSender whose destination GPU texture is EXPOSED (zigmedia inc 2).
//
// The receive side's mirror of share.go: on the send side a foreign encoder opens the sender's
// shared texture to READ it; here a foreign decoder opens it to WRITE into it, so a decoded frame
// never crosses a pipe or the Go heap. Go moves the same two scalars (handle + DXGI format) and
// still owns discovery, naming and lifetime - it just never touches a pixel.
//
// The sender must EXIST before the handle can be handed out, so unlike NewFrameSender this
// initialises the sender at open (one zeroed frame). It is still a full FrameSender: when the
// native decode session refuses, the same object publishes the ffmpeg path's frames, so the
// fallback never needs a second Spout sender under the same name.

// SharedSender is a FrameSender that also publishes its destination shared-texture identity.
type SharedSender interface {
	FrameSender
	// Handle is the DX11 shared-texture handle a foreign device may open (0 = none).
	Handle() uint64
	// Format is the texture's DXGI format.
	Format() uint32
}

// NewSharedSender opens a sender of exactly w×h and returns it with its shared-texture handle.
// Errors when no backend is compiled in, SpoutLibrary.dll is absent, no GL context can be made, or
// the backend produced no DX11 shared texture (CPU/memoryshare sender) - every case means "use the
// frame path", never a dead route.
func NewSharedSender(log *logbus.Bus, name string, w, h int) (SharedSender, error) {
	if _, ok := FrameBytes(w, h); !ok {
		return nil, errShareDims
	}
	return newSharedSender(log, name, w, h)
}

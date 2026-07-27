//go:build !spout

package videoshare

import (
	"errors"
	"image"

	"rave.page/mate/internal/logbus"
)

// No-backend receiver stubs (mirrors sender_noop): enumeration is empty, opening errors so the
// caller reports a reason instead of silently idling.

func scanSenders() []SenderInfo { return nil }
func newFrameReceiver(*logbus.Bus, string, RecvOptions) (FrameReceiver, error) {
	return nil, errors.New("no video-share backend compiled in (build -tags spout)")
}

// grabSenderFrame has no backend in the default build.
func grabSenderFrame(string, int, int) (*image.NRGBA, error) {
	return nil, errors.New("no video-share backend compiled in (build -tags spout)")
}

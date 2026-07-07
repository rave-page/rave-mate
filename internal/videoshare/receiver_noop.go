//go:build !spout

package videoshare

import (
	"errors"

	"rave.page/mate/internal/logbus"
)

// No-backend receiver stubs (mirrors sender_noop): enumeration is empty, opening errors so the
// caller reports a reason instead of silently idling.

func listSenders() []string              { return nil }
func senderSize(string) (int, int, bool) { return 0, 0, false }
func newFrameReceiver(*logbus.Bus, string) (FrameReceiver, error) {
	return nil, errors.New("no video-share backend compiled in (build -tags spout)")
}

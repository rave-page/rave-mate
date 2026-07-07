//go:build !spout

package vrslgrid

import "rave.page/mate/internal/logbus"

// newPublisher (default build): no GPU-share backend compiled in → PNG-file fallback.
func newPublisher(log *logbus.Bus, _ /*spoutName*/, pngPath string) Publisher {
	return newPNGPublisher(log, pngPath)
}

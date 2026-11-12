//go:build !(linux && pipewire)

package pwcapture

import (
	"context"
	"errors"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/medialink"
)

// capture_noop.go - the default everywhere except a Linux `-tags pipewire` build. Discovery
// (pw-dump) still works for ctl, but no node is ever registered (captureAvailable=false), so a
// machine without the capture shim degrades cleanly: it launches, lists nothing routable, and a
// stray route request fails fast instead of crashing.

func captureAvailable() bool { return false }

// VAEncodeAvailable is always false without the Linux -tags pipewire capture shim.
func VAEncodeAvailable() bool { return false }

func openCapture(context.Context, *logbus.Bus, string) (medialink.Source, error) {
	return nil, errors.New("pwcapture: PipeWire capture not built (needs linux + -tags pipewire)")
}

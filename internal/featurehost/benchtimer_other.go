//go:build !windows || !cgo

package featurehost

import (
	"context"
	"errors"

	"rave.page/mate/internal/gpubench"
)

// encodeBenchTimer is unavailable without the Windows MF encode child; the Runner gets a nil timer.
func encodeBenchTimer(context.Context, gpubench.Target) (float64, error) {
	return 0, errors.New("gpubench: native encode timer unsupported on this platform")
}

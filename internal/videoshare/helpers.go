package videoshare

import (
	"image"
	"image/jpeg"
	"os"
	"strconv"
	"strings"
	"time"
)

// closeJoin bounds how long Close waits for a worker to release its Spout handle + GL
// context. Long enough for ReleaseReceiver/ReleaseSender + CloseOpenGL; short enough
// that a worker wedged inside a blocking driver call can never hang teardown.
const closeJoin = time.Second

// waitAll waits up to timeout for every chan to close and returns how many did NOT
// (0 = clean join). Replaces the old fixed 150ms sleep in Close: that returned whether
// or not the worker had actually run ReleaseReceiver/ReleaseSender + CloseOpenGL, so a
// worker stuck in a driver call orphaned a GL context + DXGI shared handle per route
// churn. Callers log loudly on a non-zero return; the wedged worker is abandoned, never
// waited on forever.
func waitAll(stopped []<-chan struct{}, timeout time.Duration) (stuck int) {
	t := time.NewTimer(timeout)
	defer t.Stop()
	expired := false
	for _, ch := range stopped {
		if !expired {
			select {
			case <-ch:
				continue
			case <-t.C:
				expired = true
			}
		}
		select { // deadline spent: tally the rest without waiting
		case <-ch:
		default:
			stuck++
		}
	}
	return stuck
}

// decodeJPEG decodes a cached cover JPEG; nil on any error.
func decodeJPEG(path string) image.Image {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	img, err := jpeg.Decode(f)
	if err != nil {
		return nil
	}
	return img
}

// writeF appends a float (prec decimals) + a separator to the signature builder.
func writeF(b *strings.Builder, v float64, prec int) {
	b.WriteString(strconv.FormatFloat(v, 'f', prec, 64))
	b.WriteByte('|')
}

// writeB appends a bool + separator.
func writeB(b *strings.Builder, v bool) {
	if v {
		b.WriteByte('1')
	} else {
		b.WriteByte('0')
	}
	b.WriteByte('|')
}

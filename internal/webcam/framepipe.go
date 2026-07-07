package webcam

import (
	"fmt"
	"io"
)

// Raw-frame pipe framing: ffmpeg `-f rawvideo -pix_fmt rgba` emits exactly w*h*4 bytes per frame,
// no header - frame boundaries are pure stride math. Pure (io.Reader in), unit-tested.

const bytesPerPixel = 4 // RGBA/BGRA

// frameSize returns the byte length of one raw frame; error on degenerate dimensions.
func frameSize(w, h int) (int, error) {
	if w <= 0 || h <= 0 || w > 16384 || h > 16384 {
		return 0, fmt.Errorf("webcam: bad frame size %dx%d", w, h)
	}
	return w * h * bytesPerPixel, nil
}

// readFrames reads consecutive size-byte frames from r into FRESH buffers (each emitted frame is
// handed off - never reused) until EOF, a read error, or emit returning false. A trailing partial
// frame (pipe severed mid-frame) is dropped silently; other errors are returned.
func readFrames(r io.Reader, size int, emit func(buf []byte) bool) error {
	for {
		buf := make([]byte, size)
		n, err := io.ReadFull(r, buf)
		if err != nil {
			if err == io.EOF || (err == io.ErrUnexpectedEOF && n < size) {
				return nil // clean end / torn final frame
			}
			return err
		}
		if !emit(buf) {
			return nil
		}
	}
}

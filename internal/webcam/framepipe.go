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

// framePipe reassembles fixed-size raw frames from a reader using CALLER-SUPPLIED buffers.
//
// Buffers used to be a fresh make() per frame - ~250 MB/s of garbage at 1080p30, the opposite
// policy from the rest of the media plane (design §12.4 item 1). The buffer source is the caller's
// now, so the capture hands out recycled ones from the bounded pixel pool; framePipe itself stays
// pure and knows nothing about pooling.
//
// Ownership: emit TAKES the buffer (never touched here again). Any buffer NOT emitted - a torn
// trailing frame, or the one in flight when a read fails - goes to recycle, so a pooled buffer is
// never lost on a shutdown path.
type framePipe struct {
	size    int
	alloc   func() []byte         // next frame's buffer, exactly size bytes
	emit    func(buf []byte) bool // takes ownership; false = stop reading
	recycle func(buf []byte)      // buffer that was never emitted
}

// run reads frames until EOF, a read error, or emit returning false. A trailing partial frame
// (pipe severed mid-frame) is dropped silently; other errors are returned.
func (p framePipe) run(r io.Reader) error {
	for {
		buf := p.alloc()
		n, err := io.ReadFull(r, buf)
		if err != nil {
			p.recycle(buf)
			if err == io.EOF || (err == io.ErrUnexpectedEOF && n < p.size) {
				return nil // clean end / torn final frame
			}
			return err
		}
		if !p.emit(buf) {
			return nil // emit owns this buffer; it decides its fate
		}
	}
}

package medialink

import (
	"context"
	"errors"
	"io"
)

// Source produces media frames (a Spout receiver, webcam, or audio capture, or the far end of a
// Conn). Next blocks until the next frame; io.EOF ends the stream.
type Source interface {
	Next(ctx context.Context) (*Frame, error)
	Close() error
}

// Sink consumes media frames (a Spout sender, an audio output device, or a Conn to a peer).
type Sink interface {
	// Write consumes one frame. It must NOT retain f.Payload past the call - the decode path hands
	// out recycled full-frame buffers (8 MB at 1080p, 33 MB at 4K) and reuses them immediately.
	// Copy what you need (the Spout sink's SendImage does).
	Write(*Frame) error
	Close() error
}

// Pump moves frames source→sink until EOF, an error, or ctx cancel. This is the any-to-any glue:
// e.g. a webcam Source → a Conn (send to peer), and on the peer a Conn (Source) → a Spout Sink.
func Pump(ctx context.Context, src Source, dst Sink) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		f, err := src.Next(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if err := dst.Write(f); err != nil {
			return err
		}
	}
}

// Conn is both a Source and a Sink (the network endpoint of a route).

// Next reads the next frame from the peer. A blocked read is released by Close; ctx is checked
// between frames by Pump.
func (c *Conn) Next(_ context.Context) (*Frame, error) { return c.ReadFrame() }

// Write sends a frame to the peer.
func (c *Conn) Write(f *Frame) error { return c.WriteFrame(f) }

// ChanSource adapts a channel of frames (fed by a hardware capture goroutine) to a Source. Close
// the channel to signal end of stream.
type ChanSource struct{ ch <-chan *Frame }

// NewChanSource wraps a receive channel as a Source.
func NewChanSource(ch <-chan *Frame) *ChanSource { return &ChanSource{ch: ch} }

// Next returns the next frame, ctx.Err() on cancel, or io.EOF when the channel closes.
func (s *ChanSource) Next(ctx context.Context) (*Frame, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case f, ok := <-s.ch:
		if !ok {
			return nil, io.EOF
		}
		return f, nil
	}
}

// Close is a no-op; the producer owns the channel's lifetime.
func (s *ChanSource) Close() error { return nil }

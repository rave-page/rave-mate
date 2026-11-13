package medialink

import (
	"errors"
	"net"
	"os"
	"testing"
	"time"
)

// TestConnIdleTimeoutEndsHalfOpenRoute: a receive route whose peer went silent without a FIN (NIC
// link reset = half-open TCP) ends after the idle timeout instead of blocking forever.
func TestConnIdleTimeoutEndsHalfOpenRoute(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	c := NewPlainConn(a)
	c.SetIdleTimeout(150 * time.Millisecond)
	go func() { // one frame, then silence (connection never closed)
		_ = NewPlainConn(b).WriteFrame(&Frame{Kind: KindVideo, Codec: CodecHEVC, Seq: 1, Payload: []byte{1, 2, 3}})
	}()
	if _, err := c.ReadFrame(); err != nil {
		t.Fatalf("first frame: %v", err)
	}
	t0 := time.Now()
	_, err := c.ReadFrame()
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("want deadline exceeded, got %v", err)
	}
	if d := time.Since(t0); d > time.Second {
		t.Fatalf("idle timeout too slow: %v", d)
	}
}

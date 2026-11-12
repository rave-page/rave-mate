package medialink

import (
	"io"
	"testing"
)

type fakeConn struct{ closed bool }

func (c *fakeConn) WriteFrame(*Frame) error    { return nil }
func (c *fakeConn) ReadFrame() (*Frame, error) { return nil, io.EOF }
func (c *fakeConn) Close() error               { c.closed = true; return nil }

// TestDropDuplicateRecv: a new recv route for a (peer, source) atomically replaces any existing recv
// route for the same pair - never two live routes (+ two decoders/sinks) for one source on a
// receiver - while leaving other sources, other peers, and send routes alone.
func TestDropDuplicateRecv(t *testing.T) {
	rm := New(Options{Self: "X", Bus: &busView{&hub{}, "X"}, Secrets: fakeSecrets{key: testKey()}, Ports: []int{0}})
	mk := func(session, peer, src, dir string) (*fakeConn, *bool) {
		fc := &fakeConn{}
		canceled := false
		rm.active[session] = &activeRoute{cancel: func() { canceled = true }, conn: fc, dir: dir, peer: peer, sourceID: src}
		return fc, &canceled
	}
	dupConn, dupCancel := mk("A", "peerB", "mic", "recv") // the duplicate to be replaced
	mk("B", "peerB", "mic", "recv")                       // the keeper (new route)
	mk("C", "peerB", "cam", "recv")                       // different source
	mk("D", "peerB", "mic", "send")                       // send route (not a recv dedup target)
	mk("E", "peerZ", "mic", "recv")                       // different peer

	rm.dropDuplicateRecv("peerB", "mic", "B")

	if _, ok := rm.active["A"]; ok {
		t.Fatal("duplicate recv route A not removed")
	}
	if !dupConn.closed || !*dupCancel {
		t.Fatalf("duplicate not torn down (closed=%v canceled=%v)", dupConn.closed, *dupCancel)
	}
	for _, keep := range []string{"B", "C", "D", "E"} {
		if _, ok := rm.active[keep]; !ok {
			t.Fatalf("route %s wrongly dropped", keep)
		}
	}
}

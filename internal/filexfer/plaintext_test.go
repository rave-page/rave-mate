package filexfer

import (
	"bytes"
	"io"
	"net"
	"testing"
)

// plainPair returns an UNENCRYPTED sender/receiver fconn pair over net.Pipe (both-opted-out plane).
func plainPair(t *testing.T) (send, recv *fconn) {
	t.Helper()
	a, b := net.Pipe()
	s := newPlainFConn(a)
	r := newPlainFConn(b)
	t.Cleanup(func() { _ = s.Close(); _ = r.Close() })
	return s, r
}

func TestPlainConnRoundTrip(t *testing.T) {
	s, r := plainPair(t)
	done := make(chan error, 1)
	go func() {
		if err := s.writeCtl(ctlMsg{T: ctlGet, Index: 3, Offset: 77}); err != nil {
			done <- err
			return
		}
		done <- s.writeChunk(3, 77, []byte("payload"))
	}()
	msg, err := r.readCtl()
	if err != nil || msg.T != ctlGet || msg.Index != 3 || msg.Offset != 77 {
		t.Fatalf("ctl round-trip: %+v err=%v", msg, err)
	}
	typ, body, err := r.read()
	if err != nil || typ != frameChunk {
		t.Fatalf("chunk frame: typ=%d err=%v", typ, err)
	}
	idx, off, data, err := parseChunk(body)
	if err != nil || idx != 3 || off != 77 || string(data) != "payload" {
		t.Fatalf("chunk parse: idx=%d off=%d data=%q err=%v", idx, off, data, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

type writeOnly struct{ w *bytes.Buffer }

func (writeOnly) Read([]byte) (int, error)      { return 0, io.EOF }
func (o writeOnly) Write(p []byte) (int, error) { return o.w.Write(p) }
func (writeOnly) Close() error                  { return nil }

// TestPlainConnClearOnWire: a plaintext control frame leaves its JSON on the wire (not sealed).
func TestPlainConnClearOnWire(t *testing.T) {
	var buf bytes.Buffer
	c := newPlainFConn(writeOnly{&buf})
	if err := c.writeCtl(ctlMsg{T: ctlErr, Reason: "PLAINWIRE-marker"}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("PLAINWIRE-marker")) {
		t.Fatal("plaintext control frame was not clear on the wire")
	}
}

// TestFilePlaneEncryptMatrix pins the files-plane decision for every pref combination.
func TestFilePlaneEncryptMatrix(t *testing.T) {
	cases := []struct {
		a, b string
		enc  bool
	}{
		{EncOn, EncOn, true},
		{EncOn, EncOff, true},
		{EncOff, EncOn, true},
		{EncOff, EncOff, false},
		{"", EncOff, true}, // absent = ON: an older peer never downgrades this plane
		{EncOff, "", true},
		{"", "", true},
	}
	for _, c := range cases {
		if got := planeEncrypt(c.a, c.b); got != c.enc {
			t.Errorf("planeEncrypt(%q,%q)=%v want %v", c.a, c.b, got, c.enc)
		}
		if planeEncrypt(c.a, c.b) != planeEncrypt(c.b, c.a) {
			t.Errorf("planeEncrypt not symmetric for (%q,%q)", c.a, c.b)
		}
	}
}

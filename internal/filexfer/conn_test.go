package filexfer

import (
	"bytes"
	"io"
	"net"
	"testing"
)

var testMaster = bytes.Repeat([]byte{0x42}, 32)

// pipePair returns a sealed sender/receiver fconn pair over net.Pipe.
func pipePair(t *testing.T, id string) (send, recv *fconn) {
	t.Helper()
	a, b := net.Pipe()
	s, err := newFConn(a, testMaster, id, false) // sender = responder
	if err != nil {
		t.Fatal(err)
	}
	r, err := newFConn(b, testMaster, id, true) // receiver = initiator (dialer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(); _ = r.Close() })
	return s, r
}

func TestConnRoundTrip(t *testing.T) {
	s, r := pipePair(t, "x1")
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

// Different transfer ids must yield different keys (per-transfer HKDF salt).
func TestConnSaltSeparatesKeys(t *testing.T) {
	var wire bytes.Buffer
	w, err := newFConn(nopRWC{&wire}, testMaster, "id-A", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.writeCtl(ctlMsg{T: ctlDone}); err != nil {
		t.Fatal(err)
	}
	r, err := newFConn(nopRWC{&wire}, testMaster, "id-B", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.readCtl(); err == nil {
		t.Fatal("frame sealed under id-A opened under id-B")
	}
}

// A tampered ciphertext byte must be rejected (AEAD open fails → conn dead).
func TestConnRejectsCorruptFrame(t *testing.T) {
	var wire bytes.Buffer
	w, err := newFConn(nopRWC{&wire}, testMaster, "x2", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.writeChunk(0, 0, []byte("chunk-data")); err != nil {
		t.Fatal(err)
	}
	raw := wire.Bytes()
	raw[len(raw)-1] ^= 0x01 // flip one payload byte
	r, err := newFConn(nopRWC{bytes.NewBuffer(raw)}, testMaster, "x2", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.read(); err == nil {
		t.Fatal("corrupt chunk accepted")
	}
}

func TestPreambleRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := writePreamble(&buf, "abc123"); err != nil {
		t.Fatal(err)
	}
	id, err := readPreamble(&buf)
	if err != nil || id != "abc123" {
		t.Fatalf("got %q err=%v", id, err)
	}
	if err := writePreamble(io.Discard, ""); err == nil {
		t.Fatal("empty preamble accepted")
	}
}

// nopRWC adapts a buffer to io.ReadWriteCloser for wire-tamper tests.
type nopRWC struct{ rw io.ReadWriter }

func (n nopRWC) Read(p []byte) (int, error)  { return n.rw.Read(p) }
func (n nopRWC) Write(p []byte) (int, error) { return n.rw.Write(p) }
func (n nopRWC) Close() error                { return nil }

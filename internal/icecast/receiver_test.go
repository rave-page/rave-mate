package icecast

import (
	"bufio"
	"context"
	"encoding/base64"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"rave.page/mate/internal/logbus"
)

// startReceiver boots a receiver on an ephemeral loopback port and returns it + the addr.
func startReceiver(t *testing.T, cfg Config) (*Receiver, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	cfg.Addr = addr

	r := New(logbus.New(50), cfg)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := r.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(r.Stop)
	return r, addr
}

func basic(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

func recvMeta(t *testing.T, ch <-chan Meta, d time.Duration) Meta {
	t.Helper()
	select {
	case m := <-ch:
		return m
	case <-time.After(d):
		t.Fatal("timed out waiting for meta")
		return Meta{}
	}
}

func recvCapture(t *testing.T, ch <-chan Capture, d time.Duration) Capture {
	t.Helper()
	select {
	case c := <-ch:
		return c
	case <-time.After(d):
		t.Fatal("timed out waiting for capture event")
		return Capture{}
	}
}

// TestSourceCaptureAndOggMeta: a SOURCE upload of Ogg with a comment header is authed,
// captured to a file, and its now-playing metadata is surfaced.
func TestSourceCaptureAndOggMeta(t *testing.T) {
	dir := t.TempDir()
	r, addr := startReceiver(t, Config{Mount: "/stream", Username: "source", Password: "hunter2", SetsDir: dir})

	metaCh, unMeta := r.SubscribeMeta()
	defer unMeta()
	capCh, unCap := r.SubscribeCapture()
	defer unCap()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	req := "SOURCE /stream HTTP/1.0\r\n" +
		"Authorization: " + basic("source", "hunter2") + "\r\n" +
		"Content-Type: audio/ogg\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("write req: %v", err)
	}

	// Read the 200 acknowledgement line.
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("read ack: %v", err)
	}
	if want := "HTTP/1.0 200 OK"; line[:len(want)] != want {
		t.Fatalf("ack = %q want 200 OK", line)
	}

	started := recvCapture(t, capCh, 10*time.Second)
	if started.Format != "ogg" || started.Mount != "/stream" || !started.EndedAt.IsZero() {
		t.Fatalf("capture-start record: %+v", started)
	}

	// Stream a page carrying a comment header, then some filler "audio".
	page := buildOggPage(1, true, buildVorbisCommentPacket("ARTIST=Deadmau5", "TITLE=Strobe"))
	if _, err := conn.Write(page); err != nil {
		t.Fatalf("write page: %v", err)
	}
	m := recvMeta(t, metaCh, 10*time.Second)
	if m.Artist != "Deadmau5" || m.Title != "Strobe" {
		t.Fatalf("meta: %+v", m)
	}
	if _, err := conn.Write([]byte("PCMFILLERPCMFILLER")); err != nil {
		t.Fatalf("write filler: %v", err)
	}

	// Closing the source ends the capture; the file must hold what we streamed.
	_ = conn.Close()
	ended := recvCapture(t, capCh, 10*time.Second)
	if ended.EndedAt.IsZero() || ended.Bytes == 0 {
		t.Fatalf("capture-end record: %+v", ended)
	}
	data, err := os.ReadFile(ended.Path)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	if int64(len(data)) != ended.Bytes {
		t.Fatalf("file %d bytes, record says %d", len(data), ended.Bytes)
	}
	if filepath.Dir(ended.Path) != dir {
		t.Fatalf("capture not in sets dir: %s", ended.Path)
	}
}

// TestAdminMetadataUpdate: the MP3 metadata side channel updates now-playing.
func TestAdminMetadataUpdate(t *testing.T) {
	r, addr := startReceiver(t, Config{Username: "source", Password: "pw", SetsDir: t.TempDir()})
	metaCh, un := r.SubscribeMeta()
	defer un()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	req := "GET /admin/metadata?mode=updinfo&mount=/stream&song=Adam%20Beyer%20-%20Your%20Mind HTTP/1.0\r\n" +
		"Authorization: " + basic("source", "pw") + "\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("write: %v", err)
	}
	m := recvMeta(t, metaCh, 10*time.Second)
	if m.Artist != "Adam Beyer" || m.Title != "Your Mind" {
		t.Fatalf("admin meta: %+v", m)
	}
}

// TestSourceAuthRejected: a wrong source password is refused and no file is written.
func TestSourceAuthRejected(t *testing.T) {
	dir := t.TempDir()
	_, addr := startReceiver(t, Config{Username: "source", Password: "right", SetsDir: dir})

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	req := "SOURCE /stream HTTP/1.0\r\n" +
		"Authorization: " + basic("source", "wrong") + "\r\n" +
		"Content-Type: audio/ogg\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("write: %v", err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if want := "HTTP/1.0 401"; line[:len(want)] != want {
		t.Fatalf("expected 401, got %q", line)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("rejected source must not write a file, found %d", len(entries))
	}
}

// TestSecondSourceRejected: a second concurrent source is refused while one is capturing.
func TestSecondSourceRejected(t *testing.T) {
	r, addr := startReceiver(t, Config{Username: "source", Password: "pw", SetsDir: t.TempDir()})
	capCh, un := r.SubscribeCapture()
	defer un()

	c1, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial1: %v", err)
	}
	defer func() { _ = c1.Close() }()
	src := "SOURCE /stream HTTP/1.0\r\nAuthorization: " + basic("source", "pw") + "\r\nContent-Type: audio/ogg\r\n\r\n"
	if _, err := c1.Write([]byte(src)); err != nil {
		t.Fatalf("write1: %v", err)
	}
	if _, err := bufio.NewReader(c1).ReadString('\n'); err != nil {
		t.Fatalf("ack1: %v", err)
	}
	recvCapture(t, capCh, 10*time.Second) // first capture is now active

	c2, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial2: %v", err)
	}
	defer func() { _ = c2.Close() }()
	if _, err := c2.Write([]byte(src)); err != nil {
		t.Fatalf("write2: %v", err)
	}
	line, err := bufio.NewReader(c2).ReadString('\n')
	if err != nil {
		t.Fatalf("ack2: %v", err)
	}
	if want := "HTTP/1.0 403"; line[:len(want)] != want {
		t.Fatalf("expected 403 for second source, got %q", line)
	}
}

// TestSingleFileCoalescesReconnect: in single-file mode a source that drops and reconnects
// within the grace window resumes the same capture file (one start + one end event), with
// bytes accumulating across the gap - so a transient drop doesn't chop the set.
func TestSingleFileCoalescesReconnect(t *testing.T) {
	dir := t.TempDir()
	r, addr := startReceiver(t, Config{
		Mount: "/stream", Username: "source", Password: "pw", SetsDir: dir,
		SingleFile: true, ReconnectGrace: 500 * time.Millisecond,
	})
	capCh, un := r.SubscribeCapture()
	defer un()

	dialSource := func() net.Conn {
		t.Helper()
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		req := "SOURCE /stream HTTP/1.0\r\nAuthorization: " + basic("source", "pw") +
			"\r\nContent-Type: audio/mpeg\r\n\r\n"
		if _, err := conn.Write([]byte(req)); err != nil {
			t.Fatalf("write req: %v", err)
		}
		if _, err := bufio.NewReader(conn).ReadString('\n'); err != nil {
			t.Fatalf("ack: %v", err)
		}
		return conn
	}

	c1 := dialSource()
	start := recvCapture(t, capCh, 10*time.Second)
	if !start.EndedAt.IsZero() {
		t.Fatalf("expected a start event, got %+v", start)
	}
	if _, err := c1.Write([]byte("AAAAAAAA")); err != nil {
		t.Fatalf("write1: %v", err)
	}
	time.Sleep(60 * time.Millisecond)
	_ = c1.Close()

	// Reconnect well within the grace window → resumes the same file (no new start event).
	time.Sleep(120 * time.Millisecond)
	c2 := dialSource()
	if _, err := c2.Write([]byte("BBBBBBBB")); err != nil {
		t.Fatalf("write2: %v", err)
	}
	time.Sleep(60 * time.Millisecond)
	_ = c2.Close()

	// After the grace expires with no further reconnect, the single capture finalizes.
	end := recvCapture(t, capCh, 10*time.Second)
	if end.EndedAt.IsZero() {
		t.Fatalf("expected an end event, got %+v", end)
	}
	if end.ID != start.ID || end.Path != start.Path {
		t.Fatalf("reconnect must resume the same capture: start=%+v end=%+v", start, end)
	}
	if end.Bytes != 16 {
		t.Fatalf("bytes should accumulate across the reconnect, got %d", end.Bytes)
	}
	data, err := os.ReadFile(end.Path)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	if string(data) != "AAAAAAAABBBBBBBB" {
		t.Fatalf("file should hold both segments in order, got %q", data)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("single-file mode must produce exactly one file, got %d", len(entries))
	}
}

// TestSnapshotReflectsCapture: Snapshot exposes connected state for the UI indicator.
func TestSnapshotReflectsCapture(t *testing.T) {
	r, addr := startReceiver(t, Config{Username: "source", Password: "pw", SetsDir: t.TempDir()})
	if st := r.Snapshot(); !st.Listening || st.Connected {
		t.Fatalf("idle snapshot: %+v", st)
	}
	capCh, un := r.SubscribeCapture()
	defer un()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	req := "SOURCE /set HTTP/1.0\r\nAuthorization: " + basic("source", "pw") + "\r\nContent-Type: audio/mpeg\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := bufio.NewReader(conn).ReadString('\n'); err != nil {
		t.Fatalf("ack: %v", err)
	}
	recvCapture(t, capCh, 10*time.Second)
	// Poll briefly: connected flips on once the capture is registered.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if st := r.Snapshot(); st.Connected && st.Format == "mp3" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("snapshot never showed connected mp3: %+v", r.Snapshot())
}

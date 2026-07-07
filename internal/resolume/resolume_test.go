package resolume

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClipConnectAddr(t *testing.T) {
	if got := ClipConnectAddr(2, 5); got != "/composition/layers/2/clips/5/connect" {
		t.Errorf("ClipConnectAddr = %q", got)
	}
}

func TestNewDefaults(t *testing.T) {
	c := New("", 0, 0)
	if c.host != "127.0.0.1" || c.oscPort != 7000 || c.restPort != 8080 {
		t.Errorf("defaults wrong: %+v", c)
	}
	if c.base != "http://127.0.0.1:8080/api/v1" {
		t.Errorf("base = %q", c.base)
	}
}

// TestOSCSendReceives dials a real UDP listener and asserts the OSC address of a clip trigger
// arrives (exercises the lazy dial + osc encode path end to end).
func TestOSCSendReceives(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pc.Close() }()
	port := pc.LocalAddr().(*net.UDPAddr).Port

	c := New("127.0.0.1", port, 0)
	defer func() { _ = c.Close() }()
	if err := c.ConnectClip(3, 7); err != nil {
		t.Fatalf("ConnectClip: %v", err)
	}
	_ = pc.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 1024)
	n, _, err := pc.ReadFrom(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.HasPrefix(string(buf[:n]), "/composition/layers/3/clips/7/connect") {
		t.Errorf("unexpected OSC packet: %q", buf[:n])
	}
}

func TestTempoREST(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/composition" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"tempocontroller":{"tempo":{"value":128.5}}}`))
	}))
	defer srv.Close()

	c := clientForURL(srv.URL)
	bpm, err := c.Tempo(context.Background())
	if err != nil {
		t.Fatalf("Tempo: %v", err)
	}
	if bpm != 128.5 {
		t.Errorf("bpm = %v, want 128.5", bpm)
	}
}

func TestConnectClipREST(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			gotPath = r.URL.Path
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c := clientForURL(srv.URL)
	if err := c.ConnectClipREST(context.Background(), 1, 4); err != nil {
		t.Fatalf("ConnectClipREST: %v", err)
	}
	if gotPath != "/api/v1/composition/layers/1/clips/4/connect" {
		t.Errorf("POST path = %q", gotPath)
	}
}

// clientForURL points a Client's REST base at a test server (host:port from the URL).
func clientForURL(url string) *Client {
	c := New("127.0.0.1", 0, 0)
	c.base = strings.TrimSuffix(url, "/") + "/api/v1"
	return c
}

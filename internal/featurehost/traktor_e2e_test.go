package featurehost

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/session"
)

// freePort grabs an ephemeral port (tiny reuse race - fine for a local test).
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// TestTraktorFeatureE2E runs the traktor feature in a real child process, POSTs a deck
// payload to its HTTP listener, and asserts the Observation arrives through the proxy.
func TestTraktorFeatureE2E(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	port := freePort(t)
	log := logbus.New(500)
	mon := logbus.New(100)
	p, err := NewTraktorProxy(log, mon, func() TraktorConfig {
		return TraktorConfig{Addr: fmt.Sprintf("127.0.0.1:%d", port)}
	})
	if err != nil {
		t.Fatal(err)
	}
	p.host.command = func() *exec.Cmd {
		cmd := exec.Command(exe)
		cmd.Env = append(os.Environ(), "RAVE_MATE_TEST_FEATURE=traktor")
		return cmd
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := p.Host().Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer p.Host().Stop()

	obs := make(chan session.Observation, 16)
	go func() { _ = p.Start(ctx, func(o session.Observation) { obs <- o }) }()

	waitFor(t, "listening", 15*time.Second, p.Listening)

	body := []byte(`{"title":"Test Track","artist":"Tester","bpm":128}`)
	resp, err := http.Post(fmt.Sprintf("http://127.0.0.1:%d/updateDeck/A", port), "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	_ = resp.Body.Close()

	select {
	case o := <-obs:
		if o.Source != session.SourceTraktor || o.Scope.Kind != session.ScopeDeck || o.Scope.ID != "A" {
			t.Fatalf("unexpected observation %+v", o)
		}
		if o.Fields["title"] != "Test Track" || o.Fields["bpm"] != float64(128) {
			t.Fatalf("fields %+v", o.Fields)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no observation arrived")
	}

	// Monitor line forwarded into the traktor monitor bus.
	waitFor(t, "monitor entry", 5*time.Second, func() bool {
		for _, e := range mon.Snapshot() {
			if e.Source == "/updateDeck/A" {
				return true
			}
		}
		return false
	})
}

package overlayserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/overlayart"
	"rave.page/mate/internal/session"
	"rave.page/mate/internal/waveform"
)

// freePort grabs an ephemeral loopback port so parallel/sequential tests never collide on a
// shared fixed port (which flaked under full-suite load when a prior server hadn't released it).
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return p
}

func startTestServer(t *testing.T) (*session.Merger, string) {
	t.Helper()
	m := session.NewMerger()
	dir := t.TempDir()
	art := overlayart.New(filepath.Join(dir, "art"), logbus.New(16))
	wave := waveform.New(filepath.Join(dir, "wave"), logbus.New(16))
	waveFn := func() config.OverlayWaveformFeature {
		return config.OverlayWaveformFeature{Enabled: true, ZoomSeconds: 20, PlayheadPct: 1.0 / 3.0}
	}
	port := freePort(t)
	s := New(logbus.New(16), func() int { return port }, art, wave, waveFn, filepath.Join(dir, "layout.json"))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = s.Start(ctx, m) }()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	// Wait for the listener.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get(base + "/state"); err == nil {
			_ = resp.Body.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	return m, base
}

func TestServerStateReflectsDecks(t *testing.T) {
	m, base := startTestServer(t)

	m.Apply(session.Observation{
		Source: session.SourceTraktor,
		Scope:  session.Scope{Kind: session.ScopeDeck, ID: "A"},
		Fields: map[string]any{session.FieldTitle: "Strobe", session.FieldArtist: "deadmau5", session.FieldIsPlaying: true},
	})
	m.Apply(session.Observation{
		Source: session.SourceTraktor,
		Scope:  session.Scope{Kind: session.ScopeChannel, ID: "1"},
		Fields: map[string]any{session.FieldFader: 0.9},
	})

	// /state is pull-based off the latest snapshot; allow the pump a flush.
	var ov session.Overlay
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/state")
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		_ = json.Unmarshal(raw, &ov)
		// Deck A (deck obs) and its fader (separate channel obs) merge via the
		// pump independently; wait for the snapshot to CONVERGE, not just for
		// the deck to appear - else a Linux-timing read lands before the
		// channel fader is merged (flake). Deadline below is the real boundary.
		if len(ov.Decks) == 1 && ov.Decks[0].Fader == 0.9 && ov.Decks[0].OnAir {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(ov.Decks) != 1 || ov.Decks[0].Deck != "A" {
		t.Fatalf("expected deck A in /state, got %+v", ov.Decks)
	}
	if ov.Decks[0].Fader != 0.9 || !ov.Decks[0].OnAir {
		t.Errorf("deck A fader/on-air wrong: %+v", ov.Decks[0])
	}
}

func TestServerServesIndexAndLayout(t *testing.T) {
	_, base := startTestServer(t)

	// Index HTML.
	resp, err := http.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || len(body) == 0 {
		t.Fatalf("index: status=%d len=%d", resp.StatusCode, len(body))
	}

	// Layout default is empty object, then POST round-trips.
	post, _ := http.Post(base+"/layout", "application/json", strings.NewReader(`{"A":{"x":10,"y":20}}`))
	if post.StatusCode != http.StatusNoContent {
		t.Fatalf("layout POST status=%d", post.StatusCode)
	}
	_ = post.Body.Close()

	get, _ := http.Get(base + "/layout")
	raw, _ := io.ReadAll(get.Body)
	_ = get.Body.Close()
	var layout map[string]map[string]int
	if err := json.Unmarshal(raw, &layout); err != nil {
		t.Fatalf("layout GET not json: %v (%s)", err, raw)
	}
	if layout["A"]["x"] != 10 || layout["A"]["y"] != 20 {
		t.Errorf("layout not persisted: %v", layout)
	}
}

func TestServerArt404WhenNone(t *testing.T) {
	_, base := startTestServer(t)
	resp, err := http.Get(base + "/art/nope.jpg")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown art key should 404, got %d", resp.StatusCode)
	}
}

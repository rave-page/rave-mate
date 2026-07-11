package webui

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/ui"
)

// fakeWire captures host-side outbound frames.
type fakeWire struct {
	mu     sync.Mutex
	frames []ruiMsg
}

func (w *fakeWire) send(_ string, payload []byte) error {
	var m ruiMsg
	if err := json.Unmarshal(payload, &m); err != nil {
		return err
	}
	w.mu.Lock()
	w.frames = append(w.frames, m)
	w.mu.Unlock()
	return nil
}

func (w *fakeWire) wait(t *testing.T, kind, substr string) ruiMsg {
	t.Helper()
	var r ruiReasm
	deadline := time.Now().Add(3 * time.Second)
	idx := 0
	for time.Now().Before(deadline) {
		w.mu.Lock()
		frames := w.frames[idx:]
		idx = len(w.frames)
		w.mu.Unlock()
		for _, f := range frames {
			if f.T != kind {
				continue
			}
			if full, ok := r.feed(f); ok && strings.Contains(full.Data, substr) {
				return full
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no %s frame containing %q", kind, substr)
	return ruiMsg{}
}

func newTestHub(t *testing.T) (*ruiHub, *fakeWire) {
	t.Helper()
	u := &UI{svc: ui.Services{Cfg: &config.Config{}}, active: "live", stop: make(chan struct{})}
	w := &fakeWire{}
	h := newRuiHub(u)
	h.sendTo = w.send
	t.Cleanup(func() {
		h.mu.Lock()
		peers := make([]string, 0, len(h.host))
		for p := range h.host {
			peers = append(peers, p)
		}
		h.mu.Unlock()
		for _, p := range peers {
			h.closeHost(p, "", "", false)
		}
	})
	return h, w
}

func TestHostOpenStreamsDocAndActs(t *testing.T) {
	h, w := newTestHub(t)
	open, _ := ruiEncode(ruiMsg{T: ruiKindOpen, SID: "s1"})
	h.onInbound("peerB", open)
	doc := w.wait(t, ruiKindDoc, "lib-body")
	if doc.SID != "s1" {
		t.Fatalf("doc sid = %q", doc.SID)
	}
	// input replays into the headless session and re-renders the Library body
	act, _ := ruiEncode(ruiMsg{T: ruiKindAct, SID: "s1", Data: `{"act":"lib-section:presets"}`})
	h.onInbound("peerB", act)
	w.wait(t, ruiKindEval, "__patch('main'")
	h.mu.Lock()
	s := h.host["peerB"]
	h.mu.Unlock()
	if s == nil || s.hu.libSectionOr() != "presets" {
		t.Fatal("act did not reach the headless session")
	}
}

func TestHostCloseTearsDown(t *testing.T) {
	h, w := newTestHub(t)
	open, _ := ruiEncode(ruiMsg{T: ruiKindOpen, SID: "s2"})
	h.onInbound("peerB", open)
	w.wait(t, ruiKindDoc, "lib-body")
	h.mu.Lock()
	hu := h.host["peerB"].hu
	h.mu.Unlock()
	cls, _ := ruiEncode(ruiMsg{T: ruiKindClose, SID: "s2"})
	h.onInbound("peerB", cls)
	deadline := time.Now().Add(3 * time.Second)
	for {
		h.mu.Lock()
		gone := h.host["peerB"] == nil
		h.mu.Unlock()
		if gone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("session not removed on close")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !hu.stopped() {
		t.Fatal("headless UI still running after close")
	}
	libStMu.Lock()
	_, leaked := libSts[hu]
	libStMu.Unlock()
	if leaked {
		t.Fatal("per-UI state leaked after close")
	}
}

func TestHostReplacesSessionPerPeer(t *testing.T) {
	h, w := newTestHub(t)
	for _, sid := range []string{"a", "b"} {
		open, _ := ruiEncode(ruiMsg{T: ruiKindOpen, SID: sid})
		h.onInbound("peerB", open)
		w.wait(t, ruiKindDoc, "lib-body")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.host) != 1 || h.host["peerB"].sid != "b" {
		t.Fatalf("expected single replaced session, got %+v", h.host)
	}
}

func TestChunkRoundTrip(t *testing.T) {
	var parts []ruiMsg
	payload := strings.Repeat("x", 2500)
	err := ruiSendChunked(func(m ruiMsg) error { parts = append(parts, m); return nil },
		ruiMsg{T: ruiKindEval, SID: "s", Data: payload}, 1000, "m1")
	if err != nil || len(parts) != 3 {
		t.Fatalf("chunking: err=%v parts=%d", err, len(parts))
	}
	var r ruiReasm
	for i, p := range parts {
		full, ok := r.feed(p)
		if i < len(parts)-1 && ok {
			t.Fatal("premature completion")
		}
		if i == len(parts)-1 {
			if !ok || full.Data != payload || full.N != 0 {
				t.Fatal("reassembly mismatch")
			}
		}
	}
	// lost part drops the message
	r.reset()
	if _, ok := r.feed(parts[1]); ok {
		t.Fatal("out-of-order part must not complete")
	}
}

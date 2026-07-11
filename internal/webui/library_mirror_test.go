package webui

import (
	"strings"
	"testing"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/ui"
)

// pump delivers frames to a hub serialized in send order (chunk order matters on the wire).
func pump(t *testing.T, dst *ruiHub, fromPeer string) func(string, []byte) error {
	t.Helper()
	ch := make(chan []byte, 4096)
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	go func() {
		for {
			select {
			case <-done:
				return
			case p := <-ch:
				dst.onInbound(fromPeer, p)
			}
		}
	}()
	return func(_ string, payload []byte) error {
		cp := append([]byte(nil), payload...)
		select {
		case ch <- cp:
			return nil
		case <-done:
			return nil
		}
	}
}

// TestMirrorLoopback drives a full controller↔host session across two in-process hubs.
func TestMirrorLoopback(t *testing.T) {
	svc := ui.Services{Cfg: &config.Config{}}
	capA := &capture{}
	uA := newHeadlessUI(svc, capA.html, capA.eval) // controller window stand-in
	t.Cleanup(func() { uA.Stop(); releaseUIState(uA) })
	hubA := newRuiHub(uA)
	uA.rui = hubA
	hubA.setMirrorSink(uA.onMirrorMsg)

	uB := &UI{svc: svc, active: "live", stop: make(chan struct{})}
	hubB := newRuiHub(uB)
	t.Cleanup(func() { hubB.closeHost("nodeA", "", "", false) })

	hubA.sendTo = pump(t, hubB, "nodeA") // A's frames arrive at B tagged nodeA
	hubB.sendTo = pump(t, hubA, "nodeB")

	body := uA.libMirrorBody("nodeB")
	if !strings.Contains(body, "__rmirror") || !strings.Contains(body, "rmirror-banner") {
		t.Fatalf("mirror body missing frame/banner: %s", body)
	}
	// host doc lands and is applied into the iframe via srcdoc
	capA.waitEval(t, "srcdoc=")
	st := uA.mirror()
	st.mu.Lock()
	status := st.status
	st.mu.Unlock()
	if status != mirrorLive {
		t.Fatalf("status = %q, want live", status)
	}

	// forwarded input executes on the host session and the patch streams back
	uA.onAction(`{"act":"rmirror-post","form":"{\"act\":\"lib-section:presets\"}"}`)
	capA.waitEval(t, "__rmirrorFwd")
	deadline := time.Now().Add(3 * time.Second)
	for {
		hubB.mu.Lock()
		s := hubB.host["nodeA"]
		hubB.mu.Unlock()
		if s != nil && s.hu.libSectionOr() == "presets" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("forwarded act never reached the host session")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// host-side teardown degrades the controller banner
	hubB.closeHost("nodeA", "", "peer gone", true)
	capA.waitEval(t, "rmirror-banner")
	st.mu.Lock()
	status, msg := st.status, st.errMsg
	st.mu.Unlock()
	if status != mirrorError || msg != "peer gone" {
		t.Fatalf("status=%q msg=%q, want error/peer gone", status, msg)
	}
}

func TestInjectMirrorBridge(t *testing.T) {
	doc := "<html><head></head><body><main id=main>x</main></body></html>"
	out := injectMirrorBridge(doc)
	if !strings.Contains(out, "window.__rx=") || !strings.Contains(out, "parent.__rmirrorPost") {
		t.Fatal("bridge not injected")
	}
	if i := strings.Index(out, "window.__rx="); i > strings.Index(out, "</body>") {
		t.Fatal("bridge must land before </body>")
	}
}

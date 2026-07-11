package webui

import (
	"strings"
	"sync"
	"testing"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/ui"
)

// capture collects emitted docs/evals for assertions.
type capture struct {
	mu    sync.Mutex
	htmls []string
	evals []string
}

func (c *capture) html(s string) { c.mu.Lock(); c.htmls = append(c.htmls, s); c.mu.Unlock() }
func (c *capture) eval(s string) { c.mu.Lock(); c.evals = append(c.evals, s); c.mu.Unlock() }

func (c *capture) waitEval(t *testing.T, substr string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		for _, e := range c.evals {
			if strings.Contains(e, substr) {
				c.mu.Unlock()
				return
			}
		}
		c.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("eval containing %q never emitted", substr)
}

func newTestHeadless(t *testing.T) (*UI, *capture) {
	t.Helper()
	c := &capture{}
	u := newHeadlessUI(ui.Services{Cfg: &config.Config{}}, c.html, c.eval)
	t.Cleanup(func() { u.Stop(); releaseUIState(u) })
	return u, c
}

func TestHeadlessDocRendersLibrary(t *testing.T) {
	u, _ := newTestHeadless(t)
	doc := u.headlessDocHTML()
	for _, want := range []string{"<main id=main>", "lib-body", "id=__modal"} {
		if !strings.Contains(doc, want) {
			t.Fatalf("headless doc missing %q", want)
		}
	}
	if strings.Contains(doc, "<nav id=nav>") {
		t.Fatal("headless doc must not carry the nav rail")
	}
}

func TestHeadlessEvalStreamAndInput(t *testing.T) {
	u, c := newTestHeadless(t)
	// Go-driven patch flows through the eval queue into the sink.
	u.eval("window.__patch('lib-body','probe-fragment')")
	c.waitEval(t, "probe-fragment")
	// remote input replays through onAction (serialized on the virtual act worker).
	vs := u.shell.(*virtualShell)
	if !vs.post(`{"act":"lib-section:collection"}`) {
		t.Fatal("post rejected")
	}
	deadline := time.Now().Add(3 * time.Second)
	for u.libSectionOr() != "collection" {
		if time.Now().After(deadline) {
			t.Fatalf("section never switched, got %q", u.libSectionOr())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHeadlessTabPinned(t *testing.T) {
	u, _ := newTestHeadless(t)
	u.setTab("live")
	if got := u.activeTab(); got != "library" {
		t.Fatalf("pinned headless UI switched tab to %q", got)
	}
}

func TestVirtualShellPostBounded(t *testing.T) {
	block := make(chan struct{})
	vs := newVirtualShell(func(string) { <-block }, func(string) {}, func(string) {})
	defer vs.terminate()
	defer close(block)
	// worker is blocked on the first payload; fill the queue, then expect drop-newest.
	for i := 0; i <= vsActQueueCap; i++ {
		vs.post("x")
	}
	time.Sleep(20 * time.Millisecond) // let the worker take one off the queue
	for i := 0; i < 2; i++ {
		vs.post("x") // refill any slot the worker freed
	}
	if vs.post("overflow") {
		t.Fatal("expected drop-newest on a full input queue")
	}
}

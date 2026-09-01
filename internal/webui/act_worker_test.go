package webui

// Regression gate for the 2026-09-01 incident: page acts used to run INLINE on featurehost's reader
// goroutine (the procShell path had no daemon-side act lane), so a handler that blocked on the DB
// froze the reader, the window child's heartbeats went unread, and the Host killed the healthy child
// as "hung". The serial act worker (ui.go) decouples the two. Driven through the real loopback child
// so the blocking act arrives on the reader exactly as in production.

import (
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"rave.page/mate/internal/logbus"
)

func TestProcShellActWorkerServesReaderWhileHandlerBlocks(t *testing.T) {
	const (
		blockAct = "__test-actworker-block"
		recAct   = "__test-actworker-rec"
	)

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var relOnce sync.Once
	rel := func() { relOnce.Do(func() { close(release) }) }
	defer rel() // never leave the worker wedged, even on a failed assertion

	var recMu sync.Mutex
	var recorded []string
	onExact(blockAct, func(_ *UI, _ actMsg) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
	})
	onExact(recAct, func(_ *UI, m actMsg) {
		recMu.Lock()
		recorded = append(recorded, m.Val)
		recMu.Unlock()
	})

	u, _ := procTestUI(t, procModeNormal)

	// Phase 1: send the blocking act THROUGH the child, so it reaches the worker via the reader's
	// evAction - the production path. Wait until the worker is parked inside the handler.
	if !u.Act(blockAct, "") {
		t.Fatal("Act refused")
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("blocking act never reached the act worker")
	}

	// Phase 2: with the worker parked, the reader must still serve. A direct-lane eval round-trip is
	// answered by the reader (evEvalRes); pre-fix it ran onActMsg inline and this would hang.
	got := make(chan bool, 1)
	go func() {
		_, ok := u.evalValue("return window.__read(" + jsQuote("volume") + ")")
		got <- ok
	}()
	select {
	case ok := <-got:
		if !ok {
			t.Fatal("eval round-trip did not answer while an act handler was blocked")
		}
	case <-time.After(evalTimeout + 2*time.Second):
		t.Fatal("featurehost reader blocked: eval round-trip stalled behind a blocked act handler")
	}

	// Phase 3a: overflow. The worker is still parked, so the queue can only fill. Enqueue exactly the
	// cap (drained FIFO later), then one more that must be dropped-newest with a Warn. onAction is
	// precisely what the reader's evAction calls, so driving it directly is faithful to production.
	for i := 0; i < maxActQueue; i++ {
		u.onAction(`{"act":"` + recAct + `","val":"` + strconv.Itoa(i) + `"}`)
	}
	u.onAction(`{"act":"` + recAct + `","val":"OVERFLOW"}`)

	if !warnSeen(u.log, "act queue full") {
		t.Fatal("overflow did not log the drop warning")
	}

	// Phase 3b: release and assert FIFO drain of the capped acts, with the overflow act absent.
	rel()
	deadline := time.Now().Add(10 * time.Second)
	for {
		recMu.Lock()
		n := len(recorded)
		recMu.Unlock()
		if n >= maxActQueue {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker drained only %d/%d queued acts", n, maxActQueue)
		}
		time.Sleep(10 * time.Millisecond)
	}
	recMu.Lock()
	defer recMu.Unlock()
	if len(recorded) != maxActQueue {
		t.Fatalf("recorded %d acts, want exactly the cap %d (overflow act must have dropped)", len(recorded), maxActQueue)
	}
	for i, v := range recorded {
		if v != strconv.Itoa(i) {
			t.Fatalf("act worker reordered: position %d carried %q, want %d", i, v, i)
		}
	}
	for _, v := range recorded {
		if v == "OVERFLOW" {
			t.Fatal("the dropped-newest overflow act was executed")
		}
	}
}

// warnSeen reports whether a Warn entry whose message contains sub is in the bus ring.
func warnSeen(bus *logbus.Bus, sub string) bool {
	if bus == nil {
		return false
	}
	for _, e := range bus.Snapshot() {
		if e.Level == logbus.Warn && strings.Contains(e.Msg, sub) {
			return true
		}
	}
	return false
}

package featurehost

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"testing"
	"time"
)

// stubFeature: configurable test double for serveFeature.
type stubFeature struct {
	initErr  error
	startErr error
	rt       *Runtime
	slow     chan struct{} // released to unblock the "slow" method
}

func (s *stubFeature) Init(_ json.RawMessage, rt *Runtime) error {
	s.rt = rt
	return s.initErr
}

func (s *stubFeature) Start(ctx context.Context) error {
	if s.startErr != nil {
		return s.startErr
	}
	<-ctx.Done()
	return nil
}

func (s *stubFeature) Handle(_ context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	switch method {
	case "echo":
		return params, nil
	case "slow":
		<-s.slow
		return json.RawMessage(`"slow done"`), nil
	case "boom":
		panic("stub boom")
	case "emit":
		s.rt.Emit("custom", map[string]string{"hi": "there"})
		return nil, nil
	}
	return nil, fmt.Errorf("unknown %s", method)
}

// harness wires serveFeature to in-mem pipes and gives the test a parent-side codec.
type harness struct {
	enc  *json.Encoder
	dec  *json.Decoder
	in   *io.PipeWriter
	code chan int
}

func newHarness(f Feature) *harness {
	childInR, childInW := io.Pipe()
	childOutR, childOutW := io.Pipe()
	h := &harness{
		enc:  json.NewEncoder(childInW),
		dec:  json.NewDecoder(bufio.NewReader(childOutR)),
		in:   childInW,
		code: make(chan int, 1),
	}
	go func() { h.code <- serveFeature(f, "stub", childInR, childOutW) }()
	return h
}

func (h *harness) send(t *testing.T, f frame) {
	t.Helper()
	if err := h.enc.Encode(&f); err != nil {
		t.Fatalf("send: %v", err)
	}
}

// next reads frames until pred matches (skipping unrelated events), with a deadline.
func (h *harness) next(t *testing.T, pred func(frame) bool) frame {
	t.Helper()
	got := make(chan frame, 1)
	go func() {
		for {
			var fr frame
			if err := h.dec.Decode(&fr); err != nil {
				return
			}
			if pred(fr) {
				got <- fr
				return
			}
		}
	}()
	select {
	case fr := <-got:
		return fr
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for frame")
		return frame{}
	}
}

func (h *harness) initOK(t *testing.T) {
	t.Helper()
	h.send(t, frame{ID: "1", Method: methodInit, Params: json.RawMessage(`{}`)})
	fr := h.next(t, func(f frame) bool { return f.ID == "1" })
	if !fr.OK {
		t.Fatalf("init failed: %s", fr.Error)
	}
}

func (h *harness) exitCode(t *testing.T) int {
	t.Helper()
	select {
	case c := <-h.code:
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("serveFeature didn't exit")
		return -1
	}
}

func TestServeInitEchoStop(t *testing.T) {
	h := newHarness(&stubFeature{})
	h.initOK(t)

	h.send(t, frame{ID: "2", Method: "echo", Params: json.RawMessage(`{"x":1}`)})
	fr := h.next(t, func(f frame) bool { return f.ID == "2" })
	if !fr.OK || string(fr.Result) != `{"x":1}` {
		t.Fatalf("echo: ok=%v result=%s err=%s", fr.OK, fr.Result, fr.Error)
	}

	h.send(t, frame{ID: "3", Method: methodStop})
	if fr := h.next(t, func(f frame) bool { return f.ID == "3" }); !fr.OK {
		t.Fatalf("stop: %s", fr.Error)
	}
	if c := h.exitCode(t); c != 0 {
		t.Fatalf("exit code %d, want 0", c)
	}
}

func TestServeConcurrentHandlers(t *testing.T) {
	stub := &stubFeature{slow: make(chan struct{})}
	h := newHarness(stub)
	h.initOK(t)

	h.send(t, frame{ID: "2", Method: "slow"})
	h.send(t, frame{ID: "3", Method: "echo", Params: json.RawMessage(`"fast"`)})
	// echo must answer while slow is still blocked.
	if fr := h.next(t, func(f frame) bool { return f.ID == "3" }); !fr.OK {
		t.Fatalf("echo blocked behind slow: %s", fr.Error)
	}
	close(stub.slow)
	if fr := h.next(t, func(f frame) bool { return f.ID == "2" }); !fr.OK {
		t.Fatalf("slow: %s", fr.Error)
	}
}

func TestServeEOFCleanExit(t *testing.T) {
	h := newHarness(&stubFeature{})
	h.initOK(t)
	_ = h.in.Close() // daemon gone
	if c := h.exitCode(t); c != 0 {
		t.Fatalf("exit code %d, want 0", c)
	}
}

func TestServeInitError(t *testing.T) {
	h := newHarness(&stubFeature{initErr: fmt.Errorf("bind clash")})
	h.send(t, frame{ID: "1", Method: methodInit, Params: json.RawMessage(`{}`)})
	fr := h.next(t, func(f frame) bool { return f.ID == "1" })
	if fr.OK || fr.Error != "bind clash" {
		t.Fatalf("want init error, got ok=%v err=%q", fr.OK, fr.Error)
	}
	if c := h.exitCode(t); c != 1 {
		t.Fatalf("exit code %d, want 1", c)
	}
}

func TestServeStartError(t *testing.T) {
	h := newHarness(&stubFeature{startErr: fmt.Errorf("no device")})
	h.initOK(t)
	if c := h.exitCode(t); c != 1 {
		t.Fatalf("exit code %d, want 1", c)
	}
}

func TestServeHandlerPanicRespondsThenDies(t *testing.T) {
	h := newHarness(&stubFeature{})
	h.initOK(t)
	h.send(t, frame{ID: "2", Method: "boom"})
	fr := h.next(t, func(f frame) bool { return f.ID == "2" })
	if fr.OK || fr.Error == "" {
		t.Fatalf("want panic error response, got ok=%v err=%q", fr.OK, fr.Error)
	}
	if c := h.exitCode(t); c != 1 {
		t.Fatalf("exit code %d, want 1", c)
	}
}

func TestServeEmitAndLogEvents(t *testing.T) {
	h := newHarness(&stubFeature{})
	h.initOK(t)

	h.send(t, frame{ID: "2", Method: "emit"})
	ev := h.next(t, func(f frame) bool { return f.Event == "custom" })
	var data map[string]string
	if err := json.Unmarshal(ev.Data, &data); err != nil || data["hi"] != "there" {
		t.Fatalf("custom event data %s err=%v", ev.Data, err)
	}
}

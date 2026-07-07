// Package featurehost runs feature modules as supervised child processes
// (`rave-mate feature <name>`): true OS isolation, so a panic, cgo fault, or runaway
// loop in one feature kills only its child - the daemon logs it, alerts, and restarts
// with backoff. Duplex newline-JSON over stdio, same discrimination as the worker
// protocol: method!="" = request, event!="" = unsolicited event, else response.
package featurehost

import (
	"bufio"
	"encoding/json"
	"io"
	"sync"

	"rave.page/mate/internal/logbus"
)

// frame is the single wire envelope, both directions.
type frame struct {
	ID     string          `json:"id,omitempty"`
	Method string          `json:"method,omitempty"` // request
	Params json.RawMessage `json:"params,omitempty"`
	Event  string          `json:"event,omitempty"` // unsolicited event (no response expected)
	Data   json.RawMessage `json:"data,omitempty"`
	OK     bool            `json:"ok,omitempty"` // response
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// Built-in request methods (parent→child).
const (
	methodInit = "init" // params = feature config; response = ready signal
	methodStop = "stop" // graceful shutdown; child exits 0 after responding
)

// EventLog is the built-in child→parent log-forwarding event payload: the child's logbus
// entries re-emitted into the daemon's bus with their original source.
const EventLog = "log"

// EventHeartbeat is the built-in child→parent liveness ping. A feature calls rt.Beat() from the
// TOP of its main work loop; if the loop wedges (e.g. a cgo GPU call deadlocks) beats stop and the
// host force-restarts the child. Beat() coalesces, so calling it every tick is cheap.
const EventHeartbeat = "heartbeat"

// logEvent carries one child logbus entry over the wire.
type logEvent struct {
	Level  uint8          `json:"level"`
	Source string         `json:"source"`
	Msg    string         `json:"msg"`
	Fields map[string]any `json:"fields,omitempty"`
}

// wire serializes frame writes (encoder+flush under one lock) so concurrent emitters
// can't interleave bytes on the shared stdio stream.
type wire struct {
	mu  sync.Mutex
	bw  *bufio.Writer
	enc *json.Encoder
}

func newWire(out io.Writer) *wire {
	bw := bufio.NewWriter(out)
	return &wire{bw: bw, enc: json.NewEncoder(bw)}
}

func (w *wire) send(f *frame) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.enc.Encode(f); err != nil {
		return err
	}
	return w.bw.Flush()
}

func (w *wire) event(event string, data any) {
	raw, err := json.Marshal(data)
	if err != nil {
		return
	}
	_ = w.send(&frame{Event: event, Data: raw})
}

func (w *wire) respond(id string, result json.RawMessage, err error) {
	f := &frame{ID: id}
	if err != nil {
		f.Error = err.Error()
	} else {
		f.OK = true
		f.Result = result
	}
	_ = w.send(f)
}

func entryToLogEvent(e logbus.Entry) logEvent {
	return logEvent{Level: uint8(e.Level), Source: e.Source, Msg: e.Msg, Fields: e.Fields}
}

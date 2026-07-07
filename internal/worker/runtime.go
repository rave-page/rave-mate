package worker

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// registry maps worker type → its method handlers. Each `rave-mate worker <type>` child
// serves exactly one type.
var registry = map[string]map[string]Handler{
	"probe":       probeHandlers(),
	"fingerprint": fingerprintHandlers(),
	"transcode":   transcodeHandlers(),
	"render":      renderHandlers(),
}

// KnownType reports whether type has registered handlers (used by the supervisor to
// reject typos before spawning).
func KnownType(typ string) bool {
	_, ok := registry[typ]
	return ok
}

// RunWorker is the child entrypoint: serve the type's handlers over stdio until stdin
// closes (the daemon closed the pipe → exit). Returns a process exit code. Diagnostics go
// to stderr only; stdout is the pure JSON channel.
func RunWorker(typ string) int {
	handlers, ok := registry[typ]
	if !ok {
		fmt.Fprintln(os.Stderr, "worker: unknown type "+typ)
		return 2
	}
	return serve(handlers, os.Stdin, os.Stdout)
}

// serve runs the request/response loop over arbitrary streams (testable).
func serve(handlers map[string]Handler, in io.Reader, out io.Writer) int {
	bw := bufio.NewWriter(out)
	dec := json.NewDecoder(bufio.NewReader(in))
	enc := json.NewEncoder(bw)
	for {
		var req Request
		if err := dec.Decode(&req); err != nil {
			if err == io.EOF {
				return 0
			}
			fmt.Fprintln(os.Stderr, "worker: decode: "+err.Error())
			return 1
		}
		resp := Response{ID: req.ID}
		if h := handlers[req.Method]; h != nil {
			// emit streams progress events (same ID) before the terminal Response.
			emit := func(event string, data any) {
				raw, _ := json.Marshal(data)
				if enc.Encode(&Event{ID: req.ID, Event: event, Data: raw}) == nil {
					_ = bw.Flush()
				}
			}
			res, err := h(req.Params, emit)
			if err != nil {
				resp.Error = err.Error()
			} else {
				resp.OK = true
				resp.Result = res
			}
		} else {
			resp.Error = "unknown method " + req.Method
		}
		if enc.Encode(&resp) != nil || bw.Flush() != nil {
			return 1
		}
	}
}

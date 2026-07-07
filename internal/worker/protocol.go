// Package worker is the subprocess job system: the daemon (main process) spawns
// `rave-mate worker <type>` children on demand, talks to them over newline-delimited JSON
// on stdio, pools + idle-reaps them, and restarts on crash. Heavy/isolated work (ffmpeg
// transcode, media probing, future VRChat polling) runs out-of-process so a crash or a
// runaway ffmpeg can't take down the daemon, and a disabled feature spawns nothing.
package worker

import "encoding/json"

// Request is one job sent to a worker (one in flight per worker process).
type Request struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Response is the terminal result of a Request.
type Response struct {
	ID     string          `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// Event is a non-terminal progress message a handler emits before its Response (same ID).
type Event struct {
	ID    string          `json:"id"`
	Event string          `json:"event"` // distinguishes an Event from a Response on the wire
	Data  json.RawMessage `json:"data,omitempty"`
}

// EmitFunc lets a handler stream progress events to the daemon before returning. data is
// JSON-marshaled. No-op for unary handlers.
type EmitFunc func(event string, data any)

// Handler runs one job method, optionally emitting progress, and returns the JSON result
// or an error.
type Handler func(params json.RawMessage, emit EmitFunc) (json.RawMessage, error)

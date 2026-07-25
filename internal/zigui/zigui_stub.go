//go:build !zigui || !cgo

// Stub when built without -tags zigui: webui keeps the Go renderers.
package zigui

// Available reports the Zig UI lib is linked (never, in stub builds).
func Available() bool { return false }

func RenderAppGroups(stateJSON []byte) (string, bool)     { return "", false }
func RenderAppGroupsBody(stateJSON []byte) (string, bool) { return "", false }
func RenderLogs(stateJSON []byte) (string, bool)          { return "", false }
func RenderLogsLines(stateJSON []byte) (string, bool)     { return "", false }

// ── midi ──

func RenderMIDIMon(stateJSON []byte) (string, bool)     { return "", false }
func RenderMIDIMonRows(stateJSON []byte) (string, bool) { return "", false }
func RenderMIDITrace(stateJSON []byte) (string, bool)   { return "", false }
func RenderMIDICtl(stateJSON []byte) (string, bool)     { return "", false }
func RenderMIDIActive(stateJSON []byte) (string, bool)  { return "", false }
func RenderMIDICtlStat(stateJSON []byte) (string, bool) { return "", false }

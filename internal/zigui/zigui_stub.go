//go:build !zigui || !cgo

// Stub when built without -tags zigui: webui keeps the Go renderers.
package zigui

// Available reports the Zig UI lib is linked (never, in stub builds).
func Available() bool { return false }

func RenderAppGroups(stateJSON []byte) (string, bool)     { return "", false }
func RenderAppGroupsBody(stateJSON []byte) (string, bool) { return "", false }
func RenderLogs(stateJSON []byte) (string, bool)          { return "", false }
func RenderLogsLines(stateJSON []byte) (string, bool)     { return "", false }

// --- motion + live (fleet: live batch) ---

func RenderMotion(stateJSON []byte) (string, bool)     { return "", false }
func RenderMotionBody(stateJSON []byte) (string, bool) { return "", false }

// --- end motion + live ---

//go:build !zigui || !cgo

// Stub when built without -tags zigui: webui keeps the Go renderers.
package zigui

// Available reports the Zig UI lib is linked (never, in stub builds).
func Available() bool { return false }

func RenderAppGroups(stateJSON []byte) (string, bool)     { return "", false }
func RenderAppGroupsBody(stateJSON []byte) (string, bool) { return "", false }
func RenderLogs(stateJSON []byte) (string, bool)          { return "", false }
func RenderLogsLines(stateJSON []byte) (string, bool)     { return "", false }

// --- media --- (automations, overlays, twitch, editor)

func RenderAutomations(stateJSON []byte) (string, bool)     { return "", false }
func RenderAutomationsBody(stateJSON []byte) (string, bool) { return "", false }

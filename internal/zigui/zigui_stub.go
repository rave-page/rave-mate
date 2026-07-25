//go:build !zigui || !cgo

// Stub when built without -tags zigui: webui keeps the Go renderers.
package zigui

// Available reports the Zig UI lib is linked (never, in stub builds).
func Available() bool { return false }

func RenderAppGroups(stateJSON []byte) (string, bool)     { return "", false }
func RenderAppGroupsBody(stateJSON []byte) (string, bool) { return "", false }
func RenderLogs(stateJSON []byte) (string, bool)          { return "", false }
func RenderLogsLines(stateJSON []byte) (string, bool)     { return "", false }

// --- vrchat ---
func RenderVRChat(stateJSON []byte) (string, bool)         { return "", false }
func RenderVRChatStatus(stateJSON []byte) (string, bool)   { return "", false }
func RenderVRChatEditor(stateJSON []byte) (string, bool)   { return "", false }
func RenderVRChatCampaths(stateJSON []byte) (string, bool) { return "", false }
func RenderVRChatPhotos(stateJSON []byte) (string, bool)   { return "", false }
func RenderVRCGroups(stateJSON []byte) (string, bool)      { return "", false }

// --- worlds ---
func RenderWorlds(stateJSON []byte) (string, bool)          { return "", false }
func RenderWorldsLinkHint(stateJSON []byte) (string, bool)  { return "", false }
func RenderWorldsGitHub(stateJSON []byte) (string, bool)    { return "", false }
func RenderWorldsStatus(stateJSON []byte) (string, bool)    { return "", false }
func RenderWorldsUnityRows(stateJSON []byte) (string, bool) { return "", false }

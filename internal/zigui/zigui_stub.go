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

func RenderMotion(stateJSON []byte) (string, bool)                { return "", false }
func RenderMotionBody(stateJSON []byte) (string, bool)            { return "", false }
func RenderLive(stateJSON []byte) (string, bool)                  { return "", false }
func RenderLiveFrag(kind string, stateJSON []byte) (string, bool) { return "", false }

// --- end motion + live ---

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

// ── midi ──

func RenderMIDIMon(stateJSON []byte) (string, bool)     { return "", false }
func RenderMIDIMonRows(stateJSON []byte) (string, bool) { return "", false }
func RenderMIDITrace(stateJSON []byte) (string, bool)   { return "", false }
func RenderMIDICtl(stateJSON []byte) (string, bool)     { return "", false }
func RenderMIDIActive(stateJSON []byte) (string, bool)  { return "", false }
func RenderMIDICtlStat(stateJSON []byte) (string, bool) { return "", false }

// --- media --- (automations, overlays, twitch, editor)

func RenderAutomations(stateJSON []byte) (string, bool)        { return "", false }
func RenderAutomationsBody(stateJSON []byte) (string, bool)    { return "", false }
func RenderOverlays(stateJSON []byte) (string, bool)           { return "", false }
func RenderOverlaysAppearance(stateJSON []byte) (string, bool) { return "", false }
func RenderOverlaysSpout(stateJSON []byte) (string, bool)      { return "", false }
func RenderOverlaysStrip(stateJSON []byte) (string, bool)      { return "", false }
func RenderOverlaysStatus(stateJSON []byte) (string, bool)     { return "", false }
func RenderTwitch(stateJSON []byte) (string, bool)             { return "", false }
func RenderTwitchObs(stateJSON []byte) (string, bool)          { return "", false }
func RenderTwitchPresets(stateJSON []byte) (string, bool)      { return "", false }
func RenderTwitchFeed(stateJSON []byte) (string, bool)         { return "", false }
func RenderEditor(stateJSON []byte) (string, bool)             { return "", false }
func RenderEditorPreview(stateJSON []byte) (string, bool)      { return "", false }

// --- peers ---

func RenderPeers(stateJSON []byte) (string, bool)     { return "", false }
func RenderPeersBody(stateJSON []byte) (string, bool) { return "", false }

// --- library_remote ---

func RenderLibRemote(stateJSON []byte) (string, bool) { return "", false }

// --- publish ---

func RenderPublish(stateJSON []byte) (string, bool)       { return "", false }
func RenderPublishHero(stateJSON []byte) (string, bool)   { return "", false }
func RenderPublishRemote(stateJSON []byte) (string, bool) { return "", false }

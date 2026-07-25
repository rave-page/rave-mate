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

// --- settings ---

func RenderSettings(stateJSON []byte) (string, bool)        { return "", false }
func RenderSettingsContent(stateJSON []byte) (string, bool) { return "", false }
func RenderSettingsStatus(stateJSON []byte) (string, bool)  { return "", false }

// --- library ---

func RenderLibrary(stateJSON []byte) (string, bool)        { return "", false }
func RenderLibraryBody(stateJSON []byte) (string, bool)    { return "", false }
func RenderLibraryDetail(stateJSON []byte) (string, bool)  { return "", false }
func RenderLibraryQueue(stateJSON []byte) (string, bool)   { return "", false }
func RenderLibraryCueCell(stateJSON []byte) (string, bool) { return "", false }

// --- cueedit ---

func RenderCueEditTopbar(stateJSON []byte) (string, bool) { return "", false }
func RenderCueEditWave(stateJSON []byte) (string, bool)   { return "", false }
func RenderCueEditRail(stateJSON []byte) (string, bool)   { return "", false }

// --- libviews ---

func RenderLibMirror(stateJSON []byte) (string, bool)       { return "", false }
func RenderLibMirrorBanner(stateJSON []byte) (string, bool) { return "", false }
func RenderRCEBody(stateJSON []byte) (string, bool)         { return "", false }
func RenderRCEInfo(stateJSON []byte) (string, bool)         { return "", false }
func RenderRCESave(stateJSON []byte) (string, bool)         { return "", false }
func RenderLibSmartModal(stateJSON []byte) (string, bool)   { return "", false }
func RenderLibRelocModal(stateJSON []byte) (string, bool)   { return "", false }

// --- libfixers ---

func RenderLibFixNavRail(stateJSON []byte) (string, bool) { return "", false }
func RenderLibFixPrep(stateJSON []byte) (string, bool)    { return "", false }
func RenderLibFixGFRail(stateJSON []byte) (string, bool)  { return "", false }
func RenderLibFixGFLive(stateJSON []byte) (string, bool)  { return "", false }
func RenderLibFixResults(stateJSON []byte) (string, bool) { return "", false }
func RenderLibFixTagEdit(stateJSON []byte) (string, bool) { return "", false }
func RenderLibFixCompat(stateJSON []byte) (string, bool)  { return "", false }

// --- settings-sub ---

func RenderSettingsGridfix(stateJSON []byte) (string, bool)      { return "", false }
func RenderSettingsGridfixModel(stateJSON []byte) (string, bool) { return "", false }
func RenderSettingsBridge(stateJSON []byte) (string, bool)       { return "", false }
func RenderSettingsUpdFlow(stateJSON []byte) (string, bool)      { return "", false }

// --- end settings-sub ---

// --- dialogs-b ---

func RenderVgRoleBody(stateJSON []byte) (string, bool)      { return "", false }
func RenderVgInviteList(stateJSON []byte) (string, bool)    { return "", false }
func RenderVgRolesModal(stateJSON []byte) (string, bool)    { return "", false }
func RenderVgInviteModal(stateJSON []byte) (string, bool)   { return "", false }
func RenderVgMemberConfirm(stateJSON []byte) (string, bool) { return "", false }
func RenderVgPostConfirm(stateJSON []byte) (string, bool)   { return "", false }

func RenderWsListEditor(stateJSON []byte) (string, bool)   { return "", false }
func RenderWsPosterEditor(stateJSON []byte) (string, bool) { return "", false }
func RenderWsFriendPicker(stateJSON []byte) (string, bool) { return "", false }
func RenderWsFriendList(stateJSON []byte) (string, bool)   { return "", false }
func RenderWsGroupPicker(stateJSON []byte) (string, bool)  { return "", false }
func RenderWsGroupList(stateJSON []byte) (string, bool)    { return "", false }
func RenderWsRolePicker(stateJSON []byte) (string, bool)   { return "", false }
func RenderWsRoleList(stateJSON []byte) (string, bool)     { return "", false }
func RenderWsDevice(stateJSON []byte) (string, bool)       { return "", false }

func RenderAutoEditor(stateJSON []byte) (string, bool) { return "", false }

func RenderAutoRunNow(stateJSON []byte) (string, bool)   { return "", false }
func RenderAutoSchedule(stateJSON []byte) (string, bool) { return "", false }

func RenderPCViewer(stateJSON []byte) (string, bool) { return "", false }
func RenderPCGpu(stateJSON []byte) (string, bool)    { return "", false }

// --- end dialogs-b ---

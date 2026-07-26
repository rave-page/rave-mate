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

// --- player ---

func RenderPlayer(stateJSON []byte) (string, bool)       { return "", false }
func RenderPlayerRoot(stateJSON []byte) (string, bool)   { return "", false }
func RenderPlayerVid(stateJSON []byte) (string, bool)    { return "", false }
func RenderPlayerWave(stateJSON []byte) (string, bool)   { return "", false }
func RenderPlayerTp(stateJSON []byte) (string, bool)     { return "", false }
func RenderPlayerEdit(stateJSON []byte) (string, bool)   { return "", false }
func RenderPlayerExport(stateJSON []byte) (string, bool) { return "", false }
func RenderPlayerRO(stateJSON []byte) (string, bool)     { return "", false }
func RenderPlayerHov(stateJSON []byte) (string, bool)    { return "", false }

// --- end player ---

// --- dialogs-a ---

func RenderDlgChoice(stateJSON []byte) (string, bool)     { return "", false }
func RenderDlgTxtExport(stateJSON []byte) (string, bool)  { return "", false }
func RenderDlgExportPrev(stateJSON []byte) (string, bool) { return "", false }
func RenderDlgRename(stateJSON []byte) (string, bool)     { return "", false }
func RenderDlgFix(stateJSON []byte) (string, bool)        { return "", false }
func RenderDlgPreset(stateJSON []byte) (string, bool)     { return "", false }
func RenderDlgPatMgr(stateJSON []byte) (string, bool)     { return "", false }

// --- end dialogs-a ---

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

// --- phaseb-tip ---

func RenderTip(stateJSON []byte) (string, bool) { return "", false }

// --- end phaseb-tip ---
// --- phaseb-wire ---

func RenderAppGroupsV2(state []byte) (string, bool)     { return "", false }
func RenderAppGroupsBodyV2(state []byte) (string, bool) { return "", false }
func RenderLogsV2(state []byte) (string, bool)          { return "", false }
func RenderLogsLinesV2(state []byte) (string, bool)     { return "", false }

func RenderLiveV2(state []byte) (string, bool)                  { return "", false }
func RenderLiveFragV2(kind string, state []byte) (string, bool) { return "", false }

func RenderMotionV2(state []byte) (string, bool)     { return "", false }
func RenderMotionBodyV2(state []byte) (string, bool) { return "", false }

func RenderPublishV2(state []byte) (string, bool)     { return "", false }
func RenderPublishHeroV2(state []byte) (string, bool) { return "", false }

func RenderSettingsV2(state []byte) (string, bool)        { return "", false }
func RenderSettingsContentV2(state []byte) (string, bool) { return "", false }
func RenderSettingsStatusV2(state []byte) (string, bool)  { return "", false }

func RenderLibraryV2(state []byte) (string, bool)        { return "", false }
func RenderLibraryBodyV2(state []byte) (string, bool)    { return "", false }
func RenderLibraryDetailV2(state []byte) (string, bool)  { return "", false }
func RenderLibraryQueueV2(state []byte) (string, bool)   { return "", false }
func RenderLibraryCueCellV2(state []byte) (string, bool) { return "", false }

func RenderPlayerV2(state []byte) (string, bool)       { return "", false }
func RenderPlayerRootV2(state []byte) (string, bool)   { return "", false }
func RenderPlayerVidV2(state []byte) (string, bool)    { return "", false }
func RenderPlayerWaveV2(state []byte) (string, bool)   { return "", false }
func RenderPlayerTpV2(state []byte) (string, bool)     { return "", false }
func RenderPlayerEditV2(state []byte) (string, bool)   { return "", false }
func RenderPlayerExportV2(state []byte) (string, bool) { return "", false }
func RenderPlayerROV2(state []byte) (string, bool)     { return "", false }
func RenderPlayerHovV2(state []byte) (string, bool)    { return "", false }

func RenderAutomationsV2(state []byte) (string, bool)     { return "", false }
func RenderAutomationsBodyV2(state []byte) (string, bool) { return "", false }

func RenderPeersV2(state []byte) (string, bool)     { return "", false }
func RenderPeersBodyV2(state []byte) (string, bool) { return "", false }

func RenderOverlaysV2(state []byte) (string, bool)           { return "", false }
func RenderOverlaysAppearanceV2(state []byte) (string, bool) { return "", false }
func RenderOverlaysSpoutV2(state []byte) (string, bool)      { return "", false }
func RenderOverlaysStatusV2(state []byte) (string, bool)     { return "", false }
func RenderOverlaysStripV2(state []byte) (string, bool)      { return "", false }

func RenderTwitchV2(state []byte) (string, bool)        { return "", false }
func RenderTwitchObsV2(state []byte) (string, bool)     { return "", false }
func RenderTwitchPresetsV2(state []byte) (string, bool) { return "", false }
func RenderTwitchFeedV2(state []byte) (string, bool)    { return "", false }

func RenderMIDICtlV2(state []byte) (string, bool)     { return "", false }
func RenderMIDIActiveV2(state []byte) (string, bool)  { return "", false }
func RenderMIDICtlStatV2(state []byte) (string, bool) { return "", false }
func RenderMIDIMonRowsV2(state []byte) (string, bool) { return "", false }
func RenderPCViewerV2(state []byte) (string, bool)    { return "", false }
func RenderPCGpuV2(state []byte) (string, bool)       { return "", false }

func RenderVRChatV2(state []byte) (string, bool)          { return "", false }
func RenderVRChatStatusV2(state []byte) (string, bool)    { return "", false }
func RenderVRChatEditorV2(state []byte) (string, bool)    { return "", false }
func RenderVRChatCampathsV2(state []byte) (string, bool)  { return "", false }
func RenderVRChatPhotosV2(state []byte) (string, bool)    { return "", false }
func RenderVRCGroupsV2(state []byte) (string, bool)       { return "", false }
func RenderVgRoleBodyV2(state []byte) (string, bool)      { return "", false }
func RenderVgInviteListV2(state []byte) (string, bool)    { return "", false }
func RenderVgRolesModalV2(state []byte) (string, bool)    { return "", false }
func RenderVgInviteModalV2(state []byte) (string, bool)   { return "", false }
func RenderVgMemberConfirmV2(state []byte) (string, bool) { return "", false }
func RenderVgPostConfirmV2(state []byte) (string, bool)   { return "", false }

func RenderWorldsV2(state []byte) (string, bool)          { return "", false }
func RenderWorldsLinkHintV2(state []byte) (string, bool)  { return "", false }
func RenderWorldsGitHubV2(state []byte) (string, bool)    { return "", false }
func RenderWorldsStatusV2(state []byte) (string, bool)    { return "", false }
func RenderWorldsUnityRowsV2(state []byte) (string, bool) { return "", false }
func RenderWsListEditorV2(state []byte) (string, bool)    { return "", false }
func RenderWsPosterEditorV2(state []byte) (string, bool)  { return "", false }
func RenderWsFriendPickerV2(state []byte) (string, bool)  { return "", false }
func RenderWsFriendListV2(state []byte) (string, bool)    { return "", false }
func RenderWsGroupPickerV2(state []byte) (string, bool)   { return "", false }
func RenderWsGroupListV2(state []byte) (string, bool)     { return "", false }
func RenderWsRolePickerV2(state []byte) (string, bool)    { return "", false }
func RenderWsRoleListV2(state []byte) (string, bool)      { return "", false }
func RenderWsDeviceV2(state []byte) (string, bool)        { return "", false }

func RenderLibMirrorV2(state []byte) (string, bool)       { return "", false }
func RenderLibMirrorBannerV2(state []byte) (string, bool) { return "", false }
func RenderRCEInfoV2(state []byte) (string, bool)         { return "", false }
func RenderRCEBodyV2(state []byte) (string, bool)         { return "", false }
func RenderRCESaveV2(state []byte) (string, bool)         { return "", false }
func RenderEditorPreviewV2(state []byte) (string, bool)   { return "", false }
func RenderEditorV2(state []byte) (string, bool)          { return "", false }
func RenderCueEditTopbarV2(state []byte) (string, bool)   { return "", false }
func RenderCueEditWaveV2(state []byte) (string, bool)     { return "", false }
func RenderCueEditRailV2(state []byte) (string, bool)     { return "", false }
func RenderLibFixGFLiveV2(state []byte) (string, bool)    { return "", false }
func RenderLibSmartModalV2(state []byte) (string, bool)   { return "", false }
func RenderLibRelocModalV2(state []byte) (string, bool)   { return "", false }
func RenderLibRemoteV2(state []byte) (string, bool)       { return "", false }

func RenderDlgChoiceV2(state []byte) (string, bool)       { return "", false }
func RenderDlgTxtExportV2(state []byte) (string, bool)    { return "", false }
func RenderDlgExportPrevV2(state []byte) (string, bool)   { return "", false }
func RenderDlgRenameV2(state []byte) (string, bool)       { return "", false }
func RenderDlgFixV2(state []byte) (string, bool)          { return "", false }
func RenderDlgPresetV2(state []byte) (string, bool)       { return "", false }
func RenderDlgPatMgrV2(state []byte) (string, bool)       { return "", false }
func RenderAutoEditorV2(state []byte) (string, bool)      { return "", false }
func RenderAutoRunNowV2(state []byte) (string, bool)      { return "", false }
func RenderAutoScheduleV2(state []byte) (string, bool)    { return "", false }
func RenderPublishRemoteV2(state []byte) (string, bool)   { return "", false }
func RenderSettingsUpdFlowV2(state []byte) (string, bool) { return "", false }

// --- end phaseb-wire ---
// --- phaseb-sched ---

// Frag mirrors the tagged build's type so webui compiles either way; the stub never returns any
// (Available()=false keeps the scheduler branch unreachable and the legacy tick path in use).
type Frag struct {
	ID   string
	Hash uint64
	HTML string
}

func TickLive(state []byte) ([]Frag, bool) { return nil, false }
func TickLogs(state []byte) ([]Frag, bool) { return nil, false }

// --- end phaseb-sched ---
// --- phaseb-retain ---

// Retained-doc delta channel (B7 increment ii). The stub never retains anything: RetainNew hands
// back handle 0 and every patch reports PatchStub, so webui's patch channels stay unseeded and the
// stateless Go renderers run - the same shape as Available()=false everywhere else here.

// Handle names one retained slot in the tagged build.
type Handle uint64

func RetainNew(msgID uint16) Handle { return 0 }
func RetainFree(h Handle)           {}

func RetainStats() (live, seeded uint32, bytes uint64) { return 0, 0, 0 }

func PatchTwitchFeed(doc []byte) (string, PatchStatus)    { return "", PatchStub }
func PatchMIDIMonRows(doc []byte) (string, PatchStatus)   { return "", PatchStub }
func PatchMIDICtlStat(doc []byte) (string, PatchStatus)   { return "", PatchStub }
func PatchCueEditTopbar(doc []byte) (string, PatchStatus) { return "", PatchStub }
func PatchTickLive(doc []byte) ([]Frag, PatchStatus)      { return nil, PatchStub }
func PatchTickLogs(doc []byte) ([]Frag, PatchStatus)      { return nil, PatchStub }

// --- end phaseb-retain ---

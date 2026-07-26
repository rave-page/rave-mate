//go:build zigui

package webui

import (
	"fmt"
	"testing"

	"rave.page/mate/internal/zigui"
)

// B7 fan-out registry + three-way gates (root ids 45-99). Same contract as wave B-2
// (zigui_wire_b2_test.go): every _v2 export and every base document registers here, the
// mutation fuzz cross-feeds all of them, and each tab gets a Go == v1 == v2 byte-equality
// gate over its FULL golden fixture set with FallbackCounts asserted per-export.

func wireExportsB7() []wireExport {
	return []wireExport{
		{"overlays_v2", zigui.RenderOverlaysV2},
		{"overlays_appearance_v2", zigui.RenderOverlaysAppearanceV2},
		{"overlays_spout_v2", zigui.RenderOverlaysSpoutV2},
		{"overlays_status_v2", zigui.RenderOverlaysStatusV2},
		{"overlays_strip_v2", zigui.RenderOverlaysStripV2},
		{"twitch_v2", zigui.RenderTwitchV2},
		{"twitch_obs_v2", zigui.RenderTwitchObsV2},
		{"twitch_presets_v2", zigui.RenderTwitchPresetsV2},
		{"twitch_feed_v2", zigui.RenderTwitchFeedV2},
		{"midictl_v2", zigui.RenderMIDICtlV2},
		{"midictl_active_v2", zigui.RenderMIDIActiveV2},
		{"midictl_stat_v2", zigui.RenderMIDICtlStatV2},
		{"midimon_rows_v2", zigui.RenderMIDIMonRowsV2},
		{"pc_viewer_v2", zigui.RenderPCViewerV2},
		{"pc_gpu_v2", zigui.RenderPCGpuV2},
		{"vrchat_v2", zigui.RenderVRChatV2},
		{"vrchat_status_v2", zigui.RenderVRChatStatusV2},
		{"vrchat_editor_v2", zigui.RenderVRChatEditorV2},
		{"vrchat_campaths_v2", zigui.RenderVRChatCampathsV2},
		{"vrchat_photos_v2", zigui.RenderVRChatPhotosV2},
		{"vrcgroups_v2", zigui.RenderVRCGroupsV2},
		{"vg_rolebody_v2", zigui.RenderVgRoleBodyV2},
		{"vg_invitelist_v2", zigui.RenderVgInviteListV2},
		{"vg_rolesmodal_v2", zigui.RenderVgRolesModalV2},
		{"vg_invitemodal_v2", zigui.RenderVgInviteModalV2},
		{"vg_memberconfirm_v2", zigui.RenderVgMemberConfirmV2},
		{"vg_postconfirm_v2", zigui.RenderVgPostConfirmV2},
		{"worlds_v2", zigui.RenderWorldsV2},
		{"worlds_linkhint_v2", zigui.RenderWorldsLinkHintV2},
		{"worlds_github_v2", zigui.RenderWorldsGitHubV2},
		{"worlds_status_v2", zigui.RenderWorldsStatusV2},
		{"worlds_unityrows_v2", zigui.RenderWorldsUnityRowsV2},
		{"ws_listeditor_v2", zigui.RenderWsListEditorV2},
		{"ws_postereditor_v2", zigui.RenderWsPosterEditorV2},
		{"ws_friendpicker_v2", zigui.RenderWsFriendPickerV2},
		{"ws_friendlist_v2", zigui.RenderWsFriendListV2},
		{"ws_grouppicker_v2", zigui.RenderWsGroupPickerV2},
		{"ws_grouplist_v2", zigui.RenderWsGroupListV2},
		{"ws_rolepicker_v2", zigui.RenderWsRolePickerV2},
		{"ws_rolelist_v2", zigui.RenderWsRoleListV2},
		{"ws_device_v2", zigui.RenderWsDeviceV2},
		{"libmirror_v2", zigui.RenderLibMirrorV2},
		{"libmirror_banner_v2", zigui.RenderLibMirrorBannerV2},
		{"rce_info_v2", zigui.RenderRCEInfoV2},
		{"rce_body_v2", zigui.RenderRCEBodyV2},
		{"rce_save_v2", zigui.RenderRCESaveV2},
		{"editor_preview_v2", zigui.RenderEditorPreviewV2},
		{"editor_v2", zigui.RenderEditorV2},
		{"cueedit_topbar_v2", zigui.RenderCueEditTopbarV2},
		{"cueedit_wave_v2", zigui.RenderCueEditWaveV2},
		{"cueedit_rail_v2", zigui.RenderCueEditRailV2},
		{"libfix_gflive_v2", zigui.RenderLibFixGFLiveV2},
		{"lib_smartmodal_v2", zigui.RenderLibSmartModalV2},
		{"lib_relocmodal_v2", zigui.RenderLibRelocModalV2},
		{"libremote_v2", zigui.RenderLibRemoteV2},
		{"dlg_choice_v2", zigui.RenderDlgChoiceV2},
		{"dlg_txtexport_v2", zigui.RenderDlgTxtExportV2},
		{"dlg_exportprev_v2", zigui.RenderDlgExportPrevV2},
		{"dlg_rename_v2", zigui.RenderDlgRenameV2},
		{"dlg_fix_v2", zigui.RenderDlgFixV2},
		{"dlg_preset_v2", zigui.RenderDlgPresetV2},
		{"dlg_patmgr_v2", zigui.RenderDlgPatMgrV2},
		{"auto_editor_v2", zigui.RenderAutoEditorV2},
		{"auto_runnow_v2", zigui.RenderAutoRunNowV2},
		{"auto_schedule_v2", zigui.RenderAutoScheduleV2},
		{"publish_remote_v2", zigui.RenderPublishRemoteV2},
		{"settings_updflow_v2", zigui.RenderSettingsUpdFlowV2},
	}
}

func wireBasesB7() []wireBase {
	var out []wireBase
	for n, st := range ovlFixtures() {
		out = append(out,
			wireBase{"ovl/" + n, wireOvlState(st)},
			wireBase{"ovl/" + n + "/appearance", wireOvlAppr(st.Appearance)},
			wireBase{"ovl/" + n + "/spout", wireOvlSpout(st.VS.SpoutCtl)},
			wireBase{"ovl/" + n + "/status", wireUiStatus(st.Web.Card.Status)},
			wireBase{"ovl/" + n + "/strip", wireOvlStrip(st.Strip)})
	}
	for n, st := range twFixtures() {
		out = append(out,
			wireBase{"tw/" + n, wireTwState(st)},
			wireBase{"tw/" + n + "/obs", wireTwObs(st.Obs)},
			wireBase{"tw/" + n + "/presets", wireTwPresets(st.Presets)},
			wireBase{"tw/" + n + "/feed", wireTwFeed(st.Feed)})
	}
	for n, st := range midiCtlFixtures() {
		out = append(out,
			wireBase{"midi/" + n, wireMidiCtl(st)},
			wireBase{"midi/" + n + "/active", wireMidiActive(st.Port.Active)},
			wireBase{"midi/" + n + "/mon", wireMidiMonLines(st.Mon.Lines)})
		for i, bl := range st.Ctls.Blocks {
			out = append(out, wireBase{fmt.Sprintf("midi/%s/stat%d", n, i), wireMidiPortStat(bl.Stat)})
		}
	}
	for n, st := range moPCViewFixtures() {
		out = append(out, wireBase{"pcv/" + n, wirePCView(st)})
	}
	for n, st := range moPCGpuFixtures() {
		out = append(out, wireBase{"pcg/" + n, wirePCGpu(st)})
	}
	for n, st := range vrcFixtures() {
		out = append(out,
			wireBase{"vrc/" + n, wireVrcTab(st)},
			wireBase{"vrc/" + n + "/status", wireVrcStatus(st.Status)},
			wireBase{"vrc/" + n + "/editor", wireVrcEditor(st.Editor)},
			wireBase{"vrc/" + n + "/campaths", wireVrcCampaths(st.CamPaths)},
			wireBase{"vrc/" + n + "/photos", wireVrcPhotos(st.Photos)})
	}
	for n, st := range vrcgFixtures() {
		out = append(out, wireBase{"vrcg/" + n, wireVrcg(st)})
	}
	for n, st := range vgRoleBodyFixtures() {
		out = append(out,
			wireBase{"vgrb/" + n, wireVgRoleBody(st)},
			wireBase{"vgrm/" + n, wireVgRolesModal(vgRolesModalSt{Title: "Roles", Body: st})})
	}
	for n, st := range vgInviteListFixtures() {
		out = append(out,
			wireBase{"vgil/" + n, wireVgInviteList(st)},
			wireBase{"vgim/" + n, wireVgInviteModal(vgInviteModalSt{Title: "Invite", SearchPh: "f", IDPh: "id", IDBtn: "Go", List: st})})
	}
	for n, st := range vgMemberConfirmFixtures() {
		out = append(out, wireBase{"vgmc/" + n, wireVgMemberConfirm(st)})
	}
	for n, st := range vgPostConfirmFixtures() {
		out = append(out, wireBase{"vgpc/" + n, wireVgPostConfirm(st)})
	}
	for n, st := range worldsFixtures() {
		out = append(out,
			wireBase{"ws/" + n, wireWorlds(st)},
			wireBase{"ws/" + n + "/hint", wireWsHint(st.LinkHint)},
			wireBase{"ws/" + n + "/gh", wireWsGitHub(st.GH)},
			wireBase{"ws/" + n + "/status", wireWsStatus(st.Posters.Status)},
			wireBase{"ws/" + n + "/unity", wireWsUnity(st.Unity)})
	}
	for n, st := range wsListEditorFixtures() {
		out = append(out, wireBase{"wsle/" + n, wireWsListEditor(st)})
	}
	for n, st := range wsPosterEditorFixtures() {
		out = append(out, wireBase{"wspe/" + n, wireWsPosterEditor(st)})
	}
	for n, st := range wsFriendListFixtures() {
		out = append(out,
			wireBase{"wsfl/" + n, wireWsFriendList(st)},
			wireBase{"wsfp/" + n, wireWsFriendPicker(wsFriendPickerSt{Title: "Add friend", SearchPh: "filter friends…", BackLbl: "Back to list", BackAct: "world-list-edit:list-1", List: st})})
	}
	for n, st := range wsGroupListFixtures() {
		out = append(out,
			wireBase{"wsgl/" + n, wireWsGroupList(st)},
			wireBase{"wsgp/" + n, wireWsGroupPicker(wsGroupPickerSt{Title: "Add group role", SearchPh: "search all groups…", SearchBtn: "Search", Help: "h", BackLbl: "Back to list", BackAct: "world-list-edit:list-1", List: st})})
	}
	for n, st := range wsRoleListFixtures() {
		out = append(out,
			wireBase{"wsrl/" + n, wireWsRoleList(st)},
			wireBase{"wsrp/" + n, wireWsRolePicker(wsRolePickerSt{Title: "Roles of X", BackLbl: "Back to groups", BackAct: "world-groups:list-1", List: st})})
	}
	for n, st := range wsDeviceFixtures() {
		out = append(out, wireBase{"wsdev/" + n, wireWsDevice(st)})
	}
	for n, st := range libMirrorFixtures() {
		out = append(out,
			wireBase{"mir/" + n, wireLibMirror(st)},
			wireBase{"mir/" + n + "/ban", wireLibMirrorBan(st.Banner)})
	}
	for n, st := range rceBodyFixtures() {
		out = append(out, wireBase{"rceb/" + n, wireRceBody(st)})
	}
	for n, st := range rceInfoFixtures() {
		out = append(out, wireBase{"rcei/" + n, wireRceInfo(st)})
	}
	for n, st := range rceSaveFixtures() {
		out = append(out, wireBase{"rces/" + n, wireRceSave(st)})
	}
	for n, st := range edFixtures() {
		out = append(out,
			wireBase{"ed/" + n, wireEdView(st)},
			wireBase{"ed/" + n + "/prev", wireEdPreview(st.Preview)})
	}
	for n, tb := range ceTopbarFixtures() {
		out = append(out,
			wireBase{"cetb/" + n, wireCeTopbar(tb)},
			wireBase{"cew/" + n, wireCeWave(ceWaveSt{Topbar: tb, Player: ceWireBenchPlayer})})
	}
	for n, st := range ceRailFixtures() {
		out = append(out, wireBase{"cer/" + n, wireCeRail(st)})
	}
	for n, st := range gfLiveWireFixtures() {
		out = append(out, wireBase{"gfl/" + n, wireLibGFLive(st)})
	}
	for n, st := range libSmartModalFixtures() {
		out = append(out, wireBase{"srm/" + n, wireLibSmartModal(st)})
	}
	for n, st := range libRelocModalFixtures() {
		out = append(out, wireBase{"rlm/" + n, wireLibRelocModal(st)})
	}
	for n, st := range libRemoteFixtures() {
		out = append(out, wireBase{"lrm/" + n, wireLibRemote(st)})
	}
	for n, st := range dlgChoiceFixtures() {
		out = append(out, wireBase{"dch/" + n, wireDlgChoice(st)})
	}
	for i, st := range dlgTxtFx() {
		out = append(out, wireBase{fmt.Sprintf("dtx/%d", i), wireDlgTxtExport(st)})
	}
	for n, st := range dlgExportFixtures() {
		out = append(out, wireBase{"dxp/" + n, wireDlgExportPrev(st)})
	}
	for n, st := range dlgRenameFixtures() {
		out = append(out, wireBase{"drn/" + n, wireDlgRename(st)})
	}
	for n, st := range dlgFixFx() {
		out = append(out, wireBase{"dfx/" + n, wireDlgFix(st)})
	}
	for n, st := range dlgPresetFx() {
		out = append(out, wireBase{"dps/" + n, wireDlgPreset(st)})
	}
	for n, st := range dlgPatFx() {
		out = append(out, wireBase{"dpm/" + n, wireDlgPatMgr(st)})
	}
	// aeModalFixtures needs testing.T (setup can Fatal) - seed the corpus with two hand states;
	// the full fixture set still crosses in TestZigWireThreeWayAutomations.
	for n, st := range map[string]aeModalSt{
		"empty": {},
		"mini": {Title: "Edit", SecMatch: "Match", SecActions: "Actions", NoSteps: true,
			NoStepsMsg: "none", Save: "Save", Cancel: "Cancel",
			Ident: []aeBlockSt{{Kind: aeBlkField, Field: newDlgField("Label", "act", "v", "text", "", "")}}},
	} {
		out = append(out, wireBase{"aem/" + n, wireAutoEditor(st)})
	}
	for n, st := range arModalFixtures() {
		out = append(out, wireBase{"arm/" + n, wireAutoRunNow(st)})
	}
	for n, st := range asModalFixtures() {
		out = append(out, wireBase{"asm/" + n, wireAutoSchedule(st)})
	}
	for n, st := range pubRemFixtures() {
		out = append(out, wireBase{"prm/" + n, wirePublishRemote(st)})
	}
	for n, st := range updFlowFixtures() {
		out = append(out, wireBase{"upf/" + n, wireUpdFlow(st)})
	}
	return out
}

// TestZigWireThreeWayTwitch: full tab + the three fragments over the whole twitch golden
// fixture set. The feed is patched on every chat/alert event - the hot path.
func TestZigWireThreeWayTwitch(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	before := zigui.FallbackCounts()
	fx := twFixtures()
	var wireB, jsonB int
	for name, st := range fx {
		t.Run(name, func(t *testing.T) {
			doc, js := wireTwState(st), stateJSON(st)
			if len(doc) == 0 {
				t.Fatal("wire encode failed")
			}
			wireB += len(doc)
			jsonB += len(js)

			v1, ok := zigui.RenderTwitch(js)
			if !ok {
				t.Fatal("v1 full render failed")
			}
			v2, ok := zigui.RenderTwitchV2(doc)
			if !ok {
				t.Fatal("v2 full render failed")
			}
			assertBytesEqual(t, "full go==v1", twitchHTML(st), v1)
			assertBytesEqual(t, "full v1==v2", v1, v2)

			threeWayFrag(t, "feed", twFeedHTML(st.Feed), stateJSON(st.Feed),
				wireTwFeed(st.Feed), zigui.RenderTwitchFeed, zigui.RenderTwitchFeedV2)
			if st.ShowObs {
				threeWayFrag(t, "obs", twObsHTML(st.Obs), stateJSON(st.Obs),
					wireTwObs(st.Obs), zigui.RenderTwitchObs, zigui.RenderTwitchObsV2)
			}
			if st.ShowPresets {
				threeWayFrag(t, "presets", twPresetsHTML(st.Presets), stateJSON(st.Presets),
					wireTwPresets(st.Presets), zigui.RenderTwitchPresets, zigui.RenderTwitchPresetsV2)
			}
		})
	}
	t.Logf("%d fixtures: wire %d B vs json %d B (%.1f%%)", len(fx), wireB, jsonB, 100*float64(wireB)/float64(jsonB))
	assertNoNewFallbacksIn(t, before,
		"RenderTwitch", "RenderTwitchV2", "RenderTwitchObs", "RenderTwitchObsV2",
		"RenderTwitchPresets", "RenderTwitchPresetsV2", "RenderTwitchFeed", "RenderTwitchFeedV2")
}

// TestZigWireThreeWayOverlays: full tab + the four live-patched fragments over the whole
// overlays golden fixture set.
func TestZigWireThreeWayOverlays(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	before := zigui.FallbackCounts()
	fx := ovlFixtures()
	var wireB, jsonB int
	for name, st := range fx {
		t.Run(name, func(t *testing.T) {
			doc, js := wireOvlState(st), stateJSON(st)
			if len(doc) == 0 {
				t.Fatal("wire encode failed")
			}
			wireB += len(doc)
			jsonB += len(js)

			v1, ok := zigui.RenderOverlays(js)
			if !ok {
				t.Fatal("v1 full render failed")
			}
			v2, ok := zigui.RenderOverlaysV2(doc)
			if !ok {
				t.Fatal("v2 full render failed")
			}
			assertBytesEqual(t, "full go==v1", overlaysHTML(st), v1)
			assertBytesEqual(t, "full v1==v2", v1, v2)

			if !st.Available {
				return // fragments only exist on the available view (a zero uiStatus renders "" - the exports decline empty output)
			}
			threeWayFrag(t, "appearance", ovlApprHTML(st.Appearance), stateJSON(st.Appearance),
				wireOvlAppr(st.Appearance), zigui.RenderOverlaysAppearance, zigui.RenderOverlaysAppearanceV2)
			threeWayFrag(t, "spout", ovlSpoutHTML(st.VS.SpoutCtl), stateJSON(st.VS.SpoutCtl),
				wireOvlSpout(st.VS.SpoutCtl), zigui.RenderOverlaysSpout, zigui.RenderOverlaysSpoutV2)
			threeWayFrag(t, "status", st.Web.Card.Status.html(), stateJSON(st.Web.Card.Status),
				wireUiStatus(st.Web.Card.Status), zigui.RenderOverlaysStatus, zigui.RenderOverlaysStatusV2)
			threeWayFrag(t, "strip", ovlStripHTMLOf(st.Strip), stateJSON(st.Strip),
				wireOvlStrip(st.Strip), zigui.RenderOverlaysStrip, zigui.RenderOverlaysStripV2)
		})
	}
	t.Logf("%d fixtures: wire %d B vs json %d B (%.1f%%)", len(fx), wireB, jsonB, 100*float64(wireB)/float64(jsonB))
	assertNoNewFallbacksIn(t, before,
		"RenderOverlays", "RenderOverlaysV2", "RenderOverlaysAppearance", "RenderOverlaysAppearanceV2",
		"RenderOverlaysSpout", "RenderOverlaysSpoutV2", "RenderOverlaysStatus", "RenderOverlaysStatusV2",
		"RenderOverlaysStrip", "RenderOverlaysStripV2")
}

// ── bench: whole dispatch (serialize + Zig render), v1 JSON vs v2 wire ──

func BenchmarkWireBenchOverlays(b *testing.B) {
	if !zigui.Available() {
		b.Skip("zigui lib unavailable")
	}
	st := ovlFixtures()["populated"]
	benchPair(b,
		func() (string, bool) { return zigui.RenderOverlays(stateJSON(st)) },
		func() (string, bool) { return zigui.RenderOverlaysV2(wireOvlState(st)) })
}

func BenchmarkWireBenchOverlaysStatus(b *testing.B) {
	if !zigui.Available() {
		b.Skip("zigui lib unavailable")
	}
	st := ovlFixtures()["populated"].Web.Card.Status
	benchPair(b,
		func() (string, bool) { return zigui.RenderOverlaysStatus(stateJSON(st)) },
		func() (string, bool) { return zigui.RenderOverlaysStatusV2(wireUiStatus(st)) })
}

func BenchmarkWireBenchOverlaysStrip(b *testing.B) {
	if !zigui.Available() {
		b.Skip("zigui lib unavailable")
	}
	st := ovlFixtures()["populated"].Strip
	benchPair(b,
		func() (string, bool) { return zigui.RenderOverlaysStrip(stateJSON(st)) },
		func() (string, bool) { return zigui.RenderOverlaysStripV2(wireOvlStrip(st)) })
}

// TestZigWireThreeWayMIDICtl: the full MIDI Mixer tab + its three ~1 Hz patch targets
// (#midi-active, #midi-monitor rows, #midi-ctlstat-<i>) over the whole golden fixture set.
// An all-zero stat renders "" and the exports decline empty output (both v1 and v2 return
// NULL) - mirrored from the golden suite's empty-fragment arm.
func TestZigWireThreeWayMIDICtl(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	before := zigui.FallbackCounts()
	fx := midiCtlFixtures()
	var wireB, jsonB int
	for name, st := range fx {
		t.Run(name, func(t *testing.T) {
			doc, js := wireMidiCtl(st), stateJSON(st)
			if len(doc) == 0 {
				t.Fatal("wire encode failed")
			}
			wireB += len(doc)
			jsonB += len(js)

			v1, ok := zigui.RenderMIDICtl(js)
			if !ok {
				t.Fatal("v1 full render failed")
			}
			v2, ok := zigui.RenderMIDICtlV2(doc)
			if !ok {
				t.Fatal("v2 full render failed")
			}
			assertBytesEqual(t, "full go==v1", midiCtlHTML(st), v1)
			assertBytesEqual(t, "full v1==v2", v1, v2)

			threeWayFrag(t, "active", midiActiveRowHTML(st.Port.Active), stateJSON(st.Port.Active),
				wireMidiActive(st.Port.Active), zigui.RenderMIDIActive, zigui.RenderMIDIActiveV2)
			threeWayFrag(t, "mon", midiMonRowsHTML(st.Mon.Lines), stateJSON(st.Mon.Lines),
				wireMidiMonLines(st.Mon.Lines), zigui.RenderMIDIMonRows, zigui.RenderMIDIMonRowsV2)
			for i, bl := range st.Ctls.Blocks {
				want := midiPortStatHTML(bl.Stat)
				if want == "" {
					// empty fragment: both exports must decline identically
					if _, ok := zigui.RenderMIDICtlStatV2(wireMidiPortStat(bl.Stat)); ok {
						t.Fatalf("stat %d: v2 rendered an empty fragment", i)
					}
					continue
				}
				threeWayFrag(t, fmt.Sprintf("stat%d", i), want, stateJSON(bl.Stat),
					wireMidiPortStat(bl.Stat), zigui.RenderMIDICtlStat, zigui.RenderMIDICtlStatV2)
			}
		})
	}
	t.Logf("%d fixtures: wire %d B vs json %d B (%.1f%%)", len(fx), wireB, jsonB, 100*float64(wireB)/float64(jsonB))
	assertNoNewFallbacksIn(t, before,
		"RenderMIDICtl", "RenderMIDICtlV2", "RenderMIDIActive", "RenderMIDIActiveV2",
		"RenderMIDICtlStat", "RenderMIDICtlStatV2", "RenderMIDIMonRows", "RenderMIDIMonRowsV2")
}

// TestZigWireThreeWayPCV: the two point-cloud modals (dialogs_b renderers).
func TestZigWireThreeWayPCV(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	before := zigui.FallbackCounts()
	for name, st := range moPCViewFixtures() {
		t.Run("viewer/"+name, func(t *testing.T) {
			threeWayFrag(t, "viewer", moPCViewerHTMLOf(st), stateJSON(st),
				wirePCView(st), zigui.RenderPCViewer, zigui.RenderPCViewerV2)
		})
	}
	for name, st := range moPCGpuFixtures() {
		t.Run("gpu/"+name, func(t *testing.T) {
			threeWayFrag(t, "gpu", moPCGpuHTMLOf(st), stateJSON(st),
				wirePCGpu(st), zigui.RenderPCGpu, zigui.RenderPCGpuV2)
		})
	}
	assertNoNewFallbacksIn(t, before,
		"RenderPCViewer", "RenderPCViewerV2", "RenderPCGpu", "RenderPCGpuV2")
}

// threeWayOrEmpty is threeWayFrag with the golden suite's empty-render arm: a fragment whose Go
// reference renders "" makes BOTH Zig exports decline (NULL), and that must match too. Each
// decline COUNTS as a fallback, so the caller passes its expected-delta map (rec) + the export
// names and asserts exact deltas at the end - "no fallbacks" would be the wrong assertion here
// (the player suite's lesson).
func threeWayOrEmpty(t *testing.T, what, want string, js, doc []byte,
	v1 func([]byte) (string, bool), v2 func([]byte) (string, bool),
	rec map[string]int, v1n, v2n string) {
	t.Helper()
	if want == "" {
		if _, ok := v1(js); ok {
			t.Fatalf("%s: v1 rendered an empty fragment", what)
		}
		if _, ok := v2(doc); ok {
			t.Fatalf("%s: v2 rendered an empty fragment", what)
		}
		rec[v1n]++
		rec[v2n]++
		return
	}
	threeWayFrag(t, what, want, js, doc, v1, v2)
}

// assertExactFallbacksIn: every named export's fallback delta must equal want[name] (0 default).
func assertExactFallbacksIn(t *testing.T, before map[string]int, want map[string]int, keys ...string) {
	t.Helper()
	now := zigui.FallbackCounts()
	for _, k := range keys {
		if d, w := now[k]-before[k], want[k]; d != w {
			t.Errorf("fallbacks for %s = %d, want %d", k, d, w)
		}
	}
}

// TestZigWireThreeWayVRChat: the full VRChat tab + its four fragments + the Groups sub-tab
// root, over both golden fixture sets.
func TestZigWireThreeWayVRChat(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	before := zigui.FallbackCounts()
	rec := map[string]int{}
	fx := vrcFixtures()
	var wireB, jsonB int
	for name, st := range fx {
		t.Run(name, func(t *testing.T) {
			doc, js := wireVrcTab(st), stateJSON(st)
			if len(doc) == 0 {
				t.Fatal("wire encode failed")
			}
			wireB += len(doc)
			jsonB += len(js)

			v1, ok := zigui.RenderVRChat(js)
			if !ok {
				t.Fatal("v1 full render failed")
			}
			v2, ok := zigui.RenderVRChatV2(doc)
			if !ok {
				t.Fatal("v2 full render failed")
			}
			assertBytesEqual(t, "full go==v1", vrchatHTML(st), v1)
			assertBytesEqual(t, "full v1==v2", v1, v2)

			threeWayOrEmpty(t, "status", vrcStatusHTML(st.Status), stateJSON(st.Status),
				wireVrcStatus(st.Status), zigui.RenderVRChatStatus, zigui.RenderVRChatStatusV2,
				rec, "RenderVRChatStatus", "RenderVRChatStatusV2")
			threeWayOrEmpty(t, "editor", vrcEditorRenderHTML(st.Editor), stateJSON(st.Editor),
				wireVrcEditor(st.Editor), zigui.RenderVRChatEditor, zigui.RenderVRChatEditorV2,
				rec, "RenderVRChatEditor", "RenderVRChatEditorV2")
			threeWayOrEmpty(t, "campaths", vrcCampathsHTML(st.CamPaths), stateJSON(st.CamPaths),
				wireVrcCampaths(st.CamPaths), zigui.RenderVRChatCampaths, zigui.RenderVRChatCampathsV2,
				rec, "RenderVRChatCampaths", "RenderVRChatCampathsV2")
			threeWayOrEmpty(t, "photos", vrcPhotosHTML(st.Photos), stateJSON(st.Photos),
				wireVrcPhotos(st.Photos), zigui.RenderVRChatPhotos, zigui.RenderVRChatPhotosV2,
				rec, "RenderVRChatPhotos", "RenderVRChatPhotosV2")
			threeWayOrEmpty(t, "groups", vrcgBodyHTML(st.Groups), stateJSON(st.Groups),
				wireVrcg(st.Groups), zigui.RenderVRCGroups, zigui.RenderVRCGroupsV2,
				rec, "RenderVRCGroups", "RenderVRCGroupsV2")
		})
	}
	for name, st := range vrcgFixtures() {
		t.Run("vrcg/"+name, func(t *testing.T) {
			threeWayOrEmpty(t, "body", vrcgBodyHTML(st), stateJSON(st),
				wireVrcg(st), zigui.RenderVRCGroups, zigui.RenderVRCGroupsV2,
				rec, "RenderVRCGroups", "RenderVRCGroupsV2")
		})
	}
	t.Logf("%d fixtures: wire %d B vs json %d B (%.1f%%)", len(fx), wireB, jsonB, 100*float64(wireB)/float64(jsonB))
	assertExactFallbacksIn(t, before, rec,
		"RenderVRChat", "RenderVRChatV2", "RenderVRChatStatus", "RenderVRChatStatusV2",
		"RenderVRChatEditor", "RenderVRChatEditorV2", "RenderVRChatCampaths", "RenderVRChatCampathsV2",
		"RenderVRChatPhotos", "RenderVRChatPhotosV2", "RenderVRCGroups", "RenderVRCGroupsV2")
}

// TestZigWireThreeWayVgModals: the six group modals (dialogs_b renderers). Shell modals are
// composed from the body fixtures like the JSON goldens compose them.
func TestZigWireThreeWayVgModals(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	before := zigui.FallbackCounts()
	rec := map[string]int{}
	for name, st := range vgRoleBodyFixtures() {
		t.Run("rolebody/"+name, func(t *testing.T) {
			threeWayOrEmpty(t, "rolebody", vgRoleBodyHTMLOf(st), stateJSON(st),
				wireVgRoleBody(st), zigui.RenderVgRoleBody, zigui.RenderVgRoleBodyV2,
				rec, "RenderVgRoleBody", "RenderVgRoleBodyV2")
			m := vgRolesModalSt{Title: "Manage roles", Body: st}
			threeWayOrEmpty(t, "rolesmodal", vgRolesModalHTMLOf(m), stateJSON(m),
				wireVgRolesModal(m), zigui.RenderVgRolesModal, zigui.RenderVgRolesModalV2,
				rec, "RenderVgRolesModal", "RenderVgRolesModalV2")
		})
	}
	for name, st := range vgInviteListFixtures() {
		t.Run("invitelist/"+name, func(t *testing.T) {
			threeWayOrEmpty(t, "invitelist", vgInviteListHTMLOf(st), stateJSON(st),
				wireVgInviteList(st), zigui.RenderVgInviteList, zigui.RenderVgInviteListV2,
				rec, "RenderVgInviteList", "RenderVgInviteListV2")
			m := vgInviteModalSt{Title: "Invite", SearchPh: "Filter friends… (Enter)",
				IDPh: "usr_… (invite by user ID)", IDBtn: "Invite ID", List: st}
			threeWayOrEmpty(t, "invitemodal", vgInviteModalHTMLOf(m), stateJSON(m),
				wireVgInviteModal(m), zigui.RenderVgInviteModal, zigui.RenderVgInviteModalV2,
				rec, "RenderVgInviteModal", "RenderVgInviteModalV2")
		})
	}
	for name, st := range vgMemberConfirmFixtures() {
		t.Run("memberconfirm/"+name, func(t *testing.T) {
			threeWayOrEmpty(t, "memberconfirm", vgMemberConfirmHTMLOf(st), stateJSON(st),
				wireVgMemberConfirm(st), zigui.RenderVgMemberConfirm, zigui.RenderVgMemberConfirmV2,
				rec, "RenderVgMemberConfirm", "RenderVgMemberConfirmV2")
		})
	}
	for name, st := range vgPostConfirmFixtures() {
		t.Run("postconfirm/"+name, func(t *testing.T) {
			threeWayOrEmpty(t, "postconfirm", vgPostConfirmHTMLOf(st), stateJSON(st),
				wireVgPostConfirm(st), zigui.RenderVgPostConfirm, zigui.RenderVgPostConfirmV2,
				rec, "RenderVgPostConfirm", "RenderVgPostConfirmV2")
		})
	}
	assertExactFallbacksIn(t, before, rec,
		"RenderVgRoleBody", "RenderVgRoleBodyV2", "RenderVgInviteList", "RenderVgInviteListV2",
		"RenderVgRolesModal", "RenderVgRolesModalV2", "RenderVgInviteModal", "RenderVgInviteModalV2",
		"RenderVgMemberConfirm", "RenderVgMemberConfirmV2", "RenderVgPostConfirm", "RenderVgPostConfirmV2")
}

func BenchmarkWireBenchVRChat(b *testing.B) {
	if !zigui.Available() {
		b.Skip("zigui lib unavailable")
	}
	st := vrcFixtures()["populated"]
	benchPair(b,
		func() (string, bool) { return zigui.RenderVRChat(stateJSON(st)) },
		func() (string, bool) { return zigui.RenderVRChatV2(wireVrcTab(st)) })
}

func BenchmarkWireBenchVRCGroups(b *testing.B) {
	if !zigui.Available() {
		b.Skip("zigui lib unavailable")
	}
	st := vrcFixtures()["groupsView"].Groups
	benchPair(b,
		func() (string, bool) { return zigui.RenderVRCGroups(stateJSON(st)) },
		func() (string, bool) { return zigui.RenderVRCGroupsV2(wireVrcg(st)) })
}

// TestZigWireThreeWayDialogsA: the seven dialogs_a modals over their full golden fixture sets.
func TestZigWireThreeWayDialogsA(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	before := zigui.FallbackCounts()
	for name, st := range dlgChoiceFixtures() {
		t.Run("choice/"+name, func(t *testing.T) {
			threeWayFrag(t, "choice", dlgChoiceHTMLOf(st), stateJSON(st), wireDlgChoice(st),
				zigui.RenderDlgChoice, zigui.RenderDlgChoiceV2)
		})
	}
	for i, st := range dlgTxtFx() {
		t.Run(fmt.Sprintf("txtExport/%d", i), func(t *testing.T) {
			threeWayFrag(t, "txtExport", pubTxtDlgHTMLOf(st), stateJSON(st), wireDlgTxtExport(st),
				zigui.RenderDlgTxtExport, zigui.RenderDlgTxtExportV2)
		})
	}
	for name, st := range dlgExportFixtures() {
		t.Run("exportPrev/"+name, func(t *testing.T) {
			threeWayFrag(t, "exportPrev", pubExpDlgHTMLOf(st), stateJSON(st), wireDlgExportPrev(st),
				zigui.RenderDlgExportPrev, zigui.RenderDlgExportPrevV2)
		})
	}
	for name, st := range dlgRenameFixtures() {
		t.Run("rename/"+name, func(t *testing.T) {
			threeWayFrag(t, "rename", pubRenameDlgHTMLOf(st), stateJSON(st), wireDlgRename(st),
				zigui.RenderDlgRename, zigui.RenderDlgRenameV2)
		})
	}
	for name, st := range dlgFixFx() {
		t.Run("fix/"+name, func(t *testing.T) {
			threeWayFrag(t, "fix", pubFixDlgHTMLOf(st), stateJSON(st), wireDlgFix(st),
				zigui.RenderDlgFix, zigui.RenderDlgFixV2)
		})
	}
	for name, st := range dlgPresetFx() {
		t.Run("preset/"+name, func(t *testing.T) {
			threeWayFrag(t, "preset", mpPresetDlgHTMLOf(st), stateJSON(st), wireDlgPreset(st),
				zigui.RenderDlgPreset, zigui.RenderDlgPresetV2)
		})
	}
	for name, st := range dlgPatFx() {
		t.Run("patMgr/"+name, func(t *testing.T) {
			threeWayFrag(t, "patMgr", cePatMgrHTMLOf(st), stateJSON(st), wireDlgPatMgr(st),
				zigui.RenderDlgPatMgr, zigui.RenderDlgPatMgrV2)
		})
	}
	assertNoNewFallbacksIn(t, before,
		"RenderDlgChoice", "RenderDlgChoiceV2", "RenderDlgTxtExport", "RenderDlgTxtExportV2",
		"RenderDlgExportPrev", "RenderDlgExportPrevV2", "RenderDlgRename", "RenderDlgRenameV2",
		"RenderDlgFix", "RenderDlgFixV2", "RenderDlgPreset", "RenderDlgPresetV2",
		"RenderDlgPatMgr", "RenderDlgPatMgrV2")
}

// TestZigWireThreeWayAutoDialogs: editor + run-now + schedule dialogs, full fixture sets.
// (TestZigWireThreeWayAutomations in zigui_wire_test.go covers the automations LIST view.)
func TestZigWireThreeWayAutoDialogs(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	before := zigui.FallbackCounts()
	var wireB, jsonB int
	for name, st := range aeModalFixtures(t) {
		t.Run("ae/"+name, func(t *testing.T) {
			doc, js := wireAutoEditor(st), stateJSON(st)
			wireB += len(doc)
			jsonB += len(js)
			threeWayFrag(t, "aeModal", aeModalHTMLOf(st), js, doc,
				zigui.RenderAutoEditor, zigui.RenderAutoEditorV2)
		})
	}
	for name, st := range arModalFixtures() {
		t.Run("ar/"+name, func(t *testing.T) {
			threeWayFrag(t, "arModal", arModalHTMLOf(st), stateJSON(st), wireAutoRunNow(st),
				zigui.RenderAutoRunNow, zigui.RenderAutoRunNowV2)
		})
	}
	for name, st := range asModalFixtures() {
		t.Run("as/"+name, func(t *testing.T) {
			threeWayFrag(t, "asModal", asModalHTMLOf(st), stateJSON(st), wireAutoSchedule(st),
				zigui.RenderAutoSchedule, zigui.RenderAutoScheduleV2)
		})
	}
	t.Logf("aeModal: wire %d B vs json %d B (%.1f%%)", wireB, jsonB, 100*float64(wireB)/float64(jsonB))
	assertNoNewFallbacksIn(t, before,
		"RenderAutoEditor", "RenderAutoEditorV2", "RenderAutoRunNow", "RenderAutoRunNowV2",
		"RenderAutoSchedule", "RenderAutoScheduleV2")
}

// TestZigWireThreeWayPubRemoteUpd: remote Publish view + #inst-update. UpdFlow's hidden states
// render "" - both exports must decline in lockstep (exact-delta asserted).
func TestZigWireThreeWayPubRemoteUpd(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	before := zigui.FallbackCounts()
	declines := map[string]int{}
	for name, st := range pubRemFixtures() {
		t.Run("rem/"+name, func(t *testing.T) {
			threeWayFrag(t, "pubRemote", pubRemoteHTML(st), stateJSON(st), wirePublishRemote(st),
				zigui.RenderPublishRemote, zigui.RenderPublishRemoteV2)
		})
	}
	for name, st := range updFlowFixtures() {
		t.Run("upd/"+name, func(t *testing.T) {
			threeWayOrEmpty(t, "updflow", updFlowHTMLOf(st), stateJSON(st), wireUpdFlow(st),
				zigui.RenderSettingsUpdFlow, zigui.RenderSettingsUpdFlowV2,
				declines, "RenderSettingsUpdFlow", "RenderSettingsUpdFlowV2")
		})
	}
	assertExactFallbacksIn(t, before, declines,
		"RenderPublishRemote", "RenderPublishRemoteV2",
		"RenderSettingsUpdFlow", "RenderSettingsUpdFlowV2")
}

// BenchmarkWireBenchAutoSchedule: aeModalFixtures needs a live *testing.T (headless UI +
// Cleanup), so the automations bench uses the schedule editor - same AeBlock kit, no t.
func BenchmarkWireBenchAutoSchedule(b *testing.B) {
	if !zigui.Available() {
		b.Skip("zigui lib unavailable")
	}
	st := asModalFixtures()["daily"]
	benchPair(b,
		func() (string, bool) { return zigui.RenderAutoSchedule(stateJSON(st)) },
		func() (string, bool) { return zigui.RenderAutoScheduleV2(wireAutoSchedule(st)) })
}

func BenchmarkWireBenchDlgPreset(b *testing.B) {
	if !zigui.Available() {
		b.Skip("zigui lib unavailable")
	}
	st := dlgPresetFx()["video"]
	benchPair(b,
		func() (string, bool) { return zigui.RenderDlgPreset(stateJSON(st)) },
		func() (string, bool) { return zigui.RenderDlgPresetV2(wireDlgPreset(st)) })
}

func BenchmarkWireBenchMIDICtl(b *testing.B) {
	if !zigui.Available() {
		b.Skip("zigui lib unavailable")
	}
	st := midiCtlFixtures()["populated"]
	benchPair(b,
		func() (string, bool) { return zigui.RenderMIDICtl(stateJSON(st)) },
		func() (string, bool) { return zigui.RenderMIDICtlV2(wireMidiCtl(st)) })
}

func BenchmarkWireBenchMIDIMonRows(b *testing.B) {
	if !zigui.Available() {
		b.Skip("zigui lib unavailable")
	}
	st := midiCtlFixtures()["populated"].Mon.Lines
	benchPair(b,
		func() (string, bool) { return zigui.RenderMIDIMonRows(stateJSON(st)) },
		func() (string, bool) { return zigui.RenderMIDIMonRowsV2(wireMidiMonLines(st)) })
}

func BenchmarkWireBenchTwitch(b *testing.B) {
	if !zigui.Available() {
		b.Skip("zigui lib unavailable")
	}
	st := twFixtures()["populated"]
	benchPair(b,
		func() (string, bool) { return zigui.RenderTwitch(stateJSON(st)) },
		func() (string, bool) { return zigui.RenderTwitchV2(wireTwState(st)) })
}

func BenchmarkWireBenchTwitchFeed(b *testing.B) {
	if !zigui.Available() {
		b.Skip("zigui lib unavailable")
	}
	st := twFixtures()["populated"].Feed
	benchPair(b,
		func() (string, bool) { return zigui.RenderTwitchFeed(stateJSON(st)) },
		func() (string, bool) { return zigui.RenderTwitchFeedV2(wireTwFeed(st)) })
}

// TestZigWireThreeWayWorlds: the full Worlds tab + its four live patch targets (#world-linkhint,
// #world-gh, #world-st-<key>, #world-unity-rows) over the whole golden fixture set. Every status
// site (posters/events/np + each list row) goes through the status export like the golden suite.
func TestZigWireThreeWayWorlds(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	before := zigui.FallbackCounts()
	fx := worldsFixtures()
	var wireB, jsonB int
	for name, st := range fx {
		t.Run(name, func(t *testing.T) {
			doc, js := wireWorlds(st), stateJSON(st)
			if len(doc) == 0 {
				t.Fatal("wire encode failed")
			}
			wireB += len(doc)
			jsonB += len(js)

			v1, ok := zigui.RenderWorlds(js)
			if !ok {
				t.Fatal("v1 full render failed")
			}
			v2, ok := zigui.RenderWorldsV2(doc)
			if !ok {
				t.Fatal("v2 full render failed")
			}
			assertBytesEqual(t, "full go==v1", worldsHTML(st), v1)
			assertBytesEqual(t, "full v1==v2", v1, v2)

			threeWayFrag(t, "linkhint", wsHintHTML(st.LinkHint), stateJSON(st.LinkHint),
				wireWsHint(st.LinkHint), zigui.RenderWorldsLinkHint, zigui.RenderWorldsLinkHintV2)
			threeWayFrag(t, "github", wsGitHubHTML(st.GH), stateJSON(st.GH),
				wireWsGitHub(st.GH), zigui.RenderWorldsGitHub, zigui.RenderWorldsGitHubV2)
			threeWayFrag(t, "unityrows", wsUnityRowsHTML(st.Unity), stateJSON(st.Unity),
				wireWsUnity(st.Unity), zigui.RenderWorldsUnityRows, zigui.RenderWorldsUnityRowsV2)
			for i, s := range []wsStatusSt{st.Posters.Status, st.Events.Status, st.NP.Status} {
				threeWayFrag(t, fmt.Sprintf("status%d", i), wsStatusHTML(s), stateJSON(s),
					wireWsStatus(s), zigui.RenderWorldsStatus, zigui.RenderWorldsStatusV2)
			}
			for i, l := range st.Lists.Rows {
				threeWayFrag(t, fmt.Sprintf("status:list%d", i), wsStatusHTML(l.Status), stateJSON(l.Status),
					wireWsStatus(l.Status), zigui.RenderWorldsStatus, zigui.RenderWorldsStatusV2)
			}
		})
	}
	t.Logf("%d fixtures: wire %d B vs json %d B (%.1f%%)", len(fx), wireB, jsonB, 100*float64(wireB)/float64(jsonB))
	assertNoNewFallbacksIn(t, before,
		"RenderWorlds", "RenderWorldsV2", "RenderWorldsLinkHint", "RenderWorldsLinkHintV2",
		"RenderWorldsGitHub", "RenderWorldsGitHubV2", "RenderWorldsStatus", "RenderWorldsStatusV2",
		"RenderWorldsUnityRows", "RenderWorldsUnityRowsV2")
}

// TestZigWireThreeWayWsDialogs: the nine worlds modals. The friend/group list "empty" fixtures
// render "" (no loading, no rows, no empty flag) - both exports must decline identically and
// the declines are asserted exactly (i4's exact-delta pattern). Picker shells are composed from
// the list fixtures like the golden suite.
func TestZigWireThreeWayWsDialogs(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	before := zigui.FallbackCounts()
	rec := map[string]int{}
	for name, st := range wsListEditorFixtures() {
		t.Run("listEditor/"+name, func(t *testing.T) {
			threeWayFrag(t, "listEditor", wsListEditorHTMLOf(st), stateJSON(st),
				wireWsListEditor(st), zigui.RenderWsListEditor, zigui.RenderWsListEditorV2)
		})
	}
	for name, st := range wsPosterEditorFixtures() {
		t.Run("posterEditor/"+name, func(t *testing.T) {
			threeWayFrag(t, "posterEditor", wsPosterEditorHTMLOf(st), stateJSON(st),
				wireWsPosterEditor(st), zigui.RenderWsPosterEditor, zigui.RenderWsPosterEditorV2)
		})
	}
	for name, st := range wsFriendListFixtures() {
		t.Run("friendList/"+name, func(t *testing.T) {
			threeWayOrEmpty(t, "friendList", wsFriendListHTMLOf(st), stateJSON(st),
				wireWsFriendList(st), zigui.RenderWsFriendList, zigui.RenderWsFriendListV2,
				rec, "RenderWsFriendList", "RenderWsFriendListV2")
			p := wsFriendPickerSt{Title: "Add friend", SearchPh: "filter friends…",
				BackLbl: "Back to list", BackAct: "world-list-edit:list-1", List: st}
			threeWayFrag(t, "friendPicker", wsFriendPickerHTMLOf(p), stateJSON(p),
				wireWsFriendPicker(p), zigui.RenderWsFriendPicker, zigui.RenderWsFriendPickerV2)
		})
	}
	for name, st := range wsGroupListFixtures() {
		t.Run("groupList/"+name, func(t *testing.T) {
			threeWayOrEmpty(t, "groupList", wsGroupListHTMLOf(st), stateJSON(st),
				wireWsGroupList(st), zigui.RenderWsGroupList, zigui.RenderWsGroupListV2,
				rec, "RenderWsGroupList", "RenderWsGroupListV2")
			p := wsGroupPickerSt{Title: "Add group role", SearchPh: "search all groups…", SearchBtn: "Search",
				Help:    "Grant a whole group or a role. Member expansion only works where the member list is visible (public groups); private groups keep their last good expansion.",
				BackLbl: "Back to list", BackAct: "world-list-edit:list-1", List: st}
			threeWayFrag(t, "groupPicker", wsGroupPickerHTMLOf(p), stateJSON(p),
				wireWsGroupPicker(p), zigui.RenderWsGroupPicker, zigui.RenderWsGroupPickerV2)
		})
	}
	for name, st := range wsRoleListFixtures() {
		t.Run("roleList/"+name, func(t *testing.T) {
			threeWayFrag(t, "roleList", wsRoleListHTMLOf(st), stateJSON(st),
				wireWsRoleList(st), zigui.RenderWsRoleList, zigui.RenderWsRoleListV2)
			p := wsRolePickerSt{Title: `Roles of Crew & "B"`, BackLbl: "Back to groups",
				BackAct: "world-groups:list-1", List: st}
			threeWayFrag(t, "rolePicker", wsRolePickerHTMLOf(p), stateJSON(p),
				wireWsRolePicker(p), zigui.RenderWsRolePicker, zigui.RenderWsRolePickerV2)
		})
	}
	for name, st := range wsDeviceFixtures() {
		t.Run("device/"+name, func(t *testing.T) {
			threeWayFrag(t, "device", wsDeviceHTMLOf(st), stateJSON(st),
				wireWsDevice(st), zigui.RenderWsDevice, zigui.RenderWsDeviceV2)
		})
	}
	assertExactFallbacksIn(t, before, rec,
		"RenderWsListEditor", "RenderWsListEditorV2", "RenderWsPosterEditor", "RenderWsPosterEditorV2",
		"RenderWsFriendPicker", "RenderWsFriendPickerV2", "RenderWsFriendList", "RenderWsFriendListV2",
		"RenderWsGroupPicker", "RenderWsGroupPickerV2", "RenderWsGroupList", "RenderWsGroupListV2",
		"RenderWsRolePicker", "RenderWsRolePickerV2", "RenderWsRoleList", "RenderWsRoleListV2",
		"RenderWsDevice", "RenderWsDeviceV2")
}

func BenchmarkWireBenchWorlds(b *testing.B) {
	if !zigui.Available() {
		b.Skip("zigui lib unavailable")
	}
	st := worldsFixtures()["populated"]
	benchPair(b,
		func() (string, bool) { return zigui.RenderWorlds(stateJSON(st)) },
		func() (string, bool) { return zigui.RenderWorldsV2(wireWorlds(st)) })
}

func BenchmarkWireBenchWorldsStatus(b *testing.B) {
	if !zigui.Available() {
		b.Skip("zigui lib unavailable")
	}
	st := worldsFixtures()["populated"].Posters.Status
	benchPair(b,
		func() (string, bool) { return zigui.RenderWorldsStatus(stateJSON(st)) },
		func() (string, bool) { return zigui.RenderWorldsStatusV2(wireWsStatus(st)) })
}

// ceWireBenchPlayer is the raw player markup spliced into ceWaveSt (mirrors the golden suite).
const ceWireBenchPlayer = `<div id=mp-library><div id=mp-library-ph style="left:12.50%"></div>` +
	`<svg viewBox="0 0 1000 120"><path d="M0.00,60.00 L1.25,58.75"/></svg></div>`

// gfLiveWireFixtures mirrors TestZigLibFixGFLiveGolden's inline fixture map.
func gfLiveWireFixtures() map[string]libGFLiveSt {
	return map[string]libGFLiveSt{
		"zero":      {Pct: progressPct(0), Caption: "0 / 0  "},
		"batch":     {Tiles: libGFTilesFixture(), Pct: progressPct(0.42), Caption: "230 / 548  ~4m12s left", Current: `C:\m\a.flac`},
		"calibrate": {Pct: progressPct(0.5), Caption: "30 / 60", Current: "kick.wav"},
		"clamped":   {Tiles: libGFTilesFixture(), Pct: progressPct(1.7), Caption: "", Current: ""},
	}
}

// TestZigWireThreeWayLibViews: mirror body + banner, the three rce panes, the two library
// modals and the target switcher. show=false panes render "" - both exports must decline
// identically (exact-delta assertion, i4 pattern).
func TestZigWireThreeWayLibViews(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	before := zigui.FallbackCounts()
	rec := map[string]int{}
	for name, st := range libMirrorFixtures() {
		t.Run("mir/"+name, func(t *testing.T) {
			threeWayFrag(t, "mirror", libMirrorBodyHTML(st), stateJSON(st),
				wireLibMirror(st), zigui.RenderLibMirror, zigui.RenderLibMirrorV2)
			threeWayFrag(t, "banner", mirrorBannerHTMLOf(st.Banner), stateJSON(st.Banner),
				wireLibMirrorBan(st.Banner), zigui.RenderLibMirrorBanner, zigui.RenderLibMirrorBannerV2)
		})
	}
	for name, st := range rceBodyFixtures() {
		t.Run("rceb/"+name, func(t *testing.T) {
			threeWayFrag(t, "body", rceBodyHTML(st), stateJSON(st),
				wireRceBody(st), zigui.RenderRCEBody, zigui.RenderRCEBodyV2)
		})
	}
	for name, st := range rceInfoFixtures() {
		t.Run("rcei/"+name, func(t *testing.T) {
			threeWayOrEmpty(t, "info", rceInfoHTMLOf(st), stateJSON(st), wireRceInfo(st),
				zigui.RenderRCEInfo, zigui.RenderRCEInfoV2, rec, "RenderRCEInfo", "RenderRCEInfoV2")
		})
	}
	for name, st := range rceSaveFixtures() {
		t.Run("rces/"+name, func(t *testing.T) {
			threeWayOrEmpty(t, "save", rceSaveHTMLOf(st), stateJSON(st), wireRceSave(st),
				zigui.RenderRCESave, zigui.RenderRCESaveV2, rec, "RenderRCESave", "RenderRCESaveV2")
		})
	}
	for name, st := range libSmartModalFixtures() {
		t.Run("srm/"+name, func(t *testing.T) {
			threeWayFrag(t, "smart", libSmartModalHTMLOf(st), stateJSON(st),
				wireLibSmartModal(st), zigui.RenderLibSmartModal, zigui.RenderLibSmartModalV2)
		})
	}
	for name, st := range libRelocModalFixtures() {
		t.Run("rlm/"+name, func(t *testing.T) {
			threeWayFrag(t, "reloc", libRelocModalHTMLOf(st), stateJSON(st),
				wireLibRelocModal(st), zigui.RenderLibRelocModal, zigui.RenderLibRelocModalV2)
		})
	}
	for name, st := range libRemoteFixtures() {
		t.Run("lrm/"+name, func(t *testing.T) {
			threeWayOrEmpty(t, "switcher", libRemoteHTML(st), stateJSON(st), wireLibRemote(st),
				zigui.RenderLibRemote, zigui.RenderLibRemoteV2, rec, "RenderLibRemote", "RenderLibRemoteV2")
		})
	}
	assertExactFallbacksIn(t, before, rec,
		"RenderLibMirror", "RenderLibMirrorV2", "RenderLibMirrorBanner", "RenderLibMirrorBannerV2",
		"RenderRCEBody", "RenderRCEBodyV2", "RenderRCEInfo", "RenderRCEInfoV2",
		"RenderRCESave", "RenderRCESaveV2", "RenderLibSmartModal", "RenderLibSmartModalV2",
		"RenderLibRelocModal", "RenderLibRelocModalV2", "RenderLibRemote", "RenderLibRemoteV2")
}

// TestZigWireThreeWayEditor: full Editor view + #ed-preview (recursive layer trees).
func TestZigWireThreeWayEditor(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	before := zigui.FallbackCounts()
	fx := edFixtures()
	var wireB, jsonB int
	for name, st := range fx {
		t.Run(name, func(t *testing.T) {
			doc, js := wireEdView(st), stateJSON(st)
			wireB += len(doc)
			jsonB += len(js)
			threeWayFrag(t, "full", editorHTML(st), js, doc, zigui.RenderEditor, zigui.RenderEditorV2)
			threeWayFrag(t, "preview", edPreviewHTMLOf(st.Preview), stateJSON(st.Preview),
				wireEdPreview(st.Preview), zigui.RenderEditorPreview, zigui.RenderEditorPreviewV2)
		})
	}
	t.Logf("%d fixtures: wire %d B vs json %d B (%.1f%%)", len(fx), wireB, jsonB, 100*float64(wireB)/float64(jsonB))
	assertNoNewFallbacksIn(t, before,
		"RenderEditor", "RenderEditorV2", "RenderEditorPreview", "RenderEditorPreviewV2")
}

// TestZigWireThreeWayCueEdit: topbar / wave strip / rail + the #gf-live fixer fragment.
// Topbar and rail have show=false arms that render "" - exact-delta declines.
func TestZigWireThreeWayCueEdit(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	before := zigui.FallbackCounts()
	rec := map[string]int{}
	for name, tb := range ceTopbarFixtures() {
		t.Run("tb/"+name, func(t *testing.T) {
			threeWayOrEmpty(t, "topbar", ceTopbarHTMLOf(tb), stateJSON(tb), wireCeTopbar(tb),
				zigui.RenderCueEditTopbar, zigui.RenderCueEditTopbarV2, rec, "RenderCueEditTopbar", "RenderCueEditTopbarV2")
			st := ceWaveSt{Topbar: tb, Player: ceWireBenchPlayer}
			threeWayFrag(t, "wave", ceWaveHTMLOf(st), stateJSON(st),
				wireCeWave(st), zigui.RenderCueEditWave, zigui.RenderCueEditWaveV2)
		})
	}
	for name, st := range ceRailFixtures() {
		t.Run("rail/"+name, func(t *testing.T) {
			threeWayOrEmpty(t, "rail", ceRailHTMLOf(st), stateJSON(st), wireCeRail(st),
				zigui.RenderCueEditRail, zigui.RenderCueEditRailV2, rec, "RenderCueEditRail", "RenderCueEditRailV2")
		})
	}
	for name, st := range gfLiveWireFixtures() {
		t.Run("gfl/"+name, func(t *testing.T) {
			threeWayFrag(t, "gflive", libGFLiveHTML(st), stateJSON(st),
				wireLibGFLive(st), zigui.RenderLibFixGFLive, zigui.RenderLibFixGFLiveV2)
		})
	}
	assertExactFallbacksIn(t, before, rec,
		"RenderCueEditTopbar", "RenderCueEditTopbarV2", "RenderCueEditWave", "RenderCueEditWaveV2",
		"RenderCueEditRail", "RenderCueEditRailV2", "RenderLibFixGFLive", "RenderLibFixGFLiveV2")
}

func BenchmarkWireBenchCueEditTopbar(b *testing.B) {
	if !zigui.Available() {
		b.Skip("zigui lib unavailable")
	}
	tb := ceTopbarFixtures()["verified"]
	benchPair(b,
		func() (string, bool) { return zigui.RenderCueEditTopbar(stateJSON(tb)) },
		func() (string, bool) { return zigui.RenderCueEditTopbarV2(wireCeTopbar(tb)) })
}

func BenchmarkWireBenchEditor(b *testing.B) {
	if !zigui.Available() {
		b.Skip("zigui lib unavailable")
	}
	st := edFixtures()["populated"]
	benchPair(b,
		func() (string, bool) { return zigui.RenderEditor(stateJSON(st)) },
		func() (string, bool) { return zigui.RenderEditorV2(wireEdView(st)) })
}

// threeWayFrag asserts one fragment renderer three ways: Go == v1(JSON) == v2(RZW1).
func threeWayFrag(t *testing.T, what, want string, js, doc []byte,
	v1 func([]byte) (string, bool), v2 func([]byte) (string, bool)) {
	t.Helper()
	if js == nil {
		t.Fatalf("%s: state marshal failed", what)
	}
	if len(doc) == 0 {
		t.Fatalf("%s: wire encode failed", what)
	}
	h1, ok := v1(js)
	if !ok {
		t.Fatalf("%s: v1 render failed", what)
	}
	h2, ok := v2(doc)
	if !ok {
		t.Fatalf("%s: v2 render failed", what)
	}
	assertBytesEqual(t, what+" go==v1", want, h1)
	assertBytesEqual(t, what+" v1==v2", h1, h2)
}

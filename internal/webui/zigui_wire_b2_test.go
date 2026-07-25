//go:build zigui

package webui

import "rave.page/mate/internal/zigui"

// Registry for the wave B-2 fan-out: every _v2 export and every base document the mutation fuzz
// (zigui_wire_fuzz_test.go) must cover. One block per tab, so adding a tab is one edit here and
// the cross-feed matrix (every mutated document against every export) grows automatically.
//
// Order is FIXED and the caller sorts the bases by name: the fuzz seed is fixed, and map
// iteration order would otherwise stop a failure from reproducing.

type wireBase struct {
	name string
	doc  []byte
}

// liveFragExport adapts one kind of the kind-dispatched live fragment export.
func liveFragExport(kind string) wireExport {
	return wireExport{"live_frag_v2/" + kind, func(b []byte) (string, bool) {
		return zigui.RenderLiveFragV2(kind, b)
	}}
}

func wireExportsB2() []wireExport {
	out := []wireExport{{"live_v2", zigui.RenderLiveV2}}
	for _, k := range []string{"transport", "np", "status", "decks", "signals", "cockpit", "link", "graph", "perf", "strip"} {
		out = append(out, liveFragExport(k))
	}
	out = append(out,
		wireExport{"motion_v2", zigui.RenderMotionV2},
		wireExport{"motion_body_v2", zigui.RenderMotionBodyV2},
		wireExport{"publish_v2", zigui.RenderPublishV2},
		wireExport{"publish_hero_v2", zigui.RenderPublishHeroV2},
		wireExport{"settings_v2", zigui.RenderSettingsV2},
		wireExport{"settings_content_v2", zigui.RenderSettingsContentV2},
		wireExport{"settings_status_v2", zigui.RenderSettingsStatusV2},
		wireExport{"library_v2", zigui.RenderLibraryV2},
		wireExport{"library_body_v2", zigui.RenderLibraryBodyV2},
		wireExport{"library_detail_v2", zigui.RenderLibraryDetailV2},
		wireExport{"library_queue_v2", zigui.RenderLibraryQueueV2},
		wireExport{"library_cuecell_v2", zigui.RenderLibraryCueCellV2},
		wireExport{"player_v2", zigui.RenderPlayerV2},
		wireExport{"player_root_v2", zigui.RenderPlayerRootV2},
		wireExport{"player_vid_v2", zigui.RenderPlayerVidV2},
		wireExport{"player_wave_v2", zigui.RenderPlayerWaveV2},
		wireExport{"player_tp_v2", zigui.RenderPlayerTpV2},
		wireExport{"player_edit_v2", zigui.RenderPlayerEditV2},
		wireExport{"player_export_v2", zigui.RenderPlayerExportV2},
		wireExport{"player_ro_v2", zigui.RenderPlayerROV2},
		wireExport{"player_hov_v2", zigui.RenderPlayerHovV2},
		wireExport{"automations_v2", zigui.RenderAutomationsV2},
		wireExport{"automations_body_v2", zigui.RenderAutomationsBodyV2},
		wireExport{"peers_v2", zigui.RenderPeersV2},
		wireExport{"peers_body_v2", zigui.RenderPeersBodyV2})
	return out
}

func wireBasesB2() []wireBase {
	var out []wireBase
	for n, st := range liveFixtures() {
		out = append(out,
			wireBase{"live/" + n, wireLiveState(st)},
			wireBase{"live/" + n + "/transport", wireLiveTransport(st.Transport)},
			wireBase{"live/" + n + "/np", wireLiveNP(st.NP)},
			wireBase{"live/" + n + "/status", wireLiveStatus(st.Status)},
			wireBase{"live/" + n + "/decks", wireLiveDecks(st.Decks)},
			wireBase{"live/" + n + "/signals", wireLiveSignals(st.Signals)},
			wireBase{"live/" + n + "/cockpit", wireLiveCockpit(st.Cockpit)},
			wireBase{"live/" + n + "/link", wireLiveLink(st.Link)},
			wireBase{"live/" + n + "/graph", wireLiveGraph(st.Net)},
			wireBase{"live/" + n + "/perf", wireLivePerf(st.Perf)},
			wireBase{"live/" + n + "/strip", wireLiveStrip(st.Strip)},
		)
	}
	for n, st := range moFixtures() {
		out = append(out, wireBase{"motion/" + n, wireMoState(st)})
	}
	for n, st := range peersFixtures() {
		out = append(out,
			wireBase{"peers/" + n, wirePeers(st)},
			wireBase{"peers/" + n + "/body", wirePeersBody(st.Body)})
	}
	for n, st := range autoFixtures() {
		out = append(out,
			wireBase{"auto/" + n, wireAutoState(st)},
			wireBase{"auto/" + n + "/body", wireAutoBodyState(st.Body)})
	}
	for n, fx := range mpFixtures() {
		u := &UI{}
		*u.mp(fx.host) = fx
		inner := u.mpInnerState(u.mpSnap(fx.host))
		out = append(out,
			wireBase{"mp/" + n, wireMpFull(mpFullSt{Host: fx.host, Inner: inner})},
			wireBase{"mp/" + n + "/inner", wireMpInner(inner)},
			wireBase{"mp/" + n + "/tp", wireMpTp(inner.Tp)},
			wireBase{"mp/" + n + "/edit", wireMpEdit(inner.EditBox)},
			wireBase{"mp/" + n + "/export", wireMpExport(inner.EditBox.Export)})
		releaseUIState(u)
	}
	for n, st := range libFixtures() {
		out = append(out,
			wireBase{"lib/" + n, wireLibState(st)},
			wireBase{"lib/" + n + "/body", wireLibBody(st.Body)},
			wireBase{"lib/" + n + "/detail", wireLibDetail(st.Body.Detail)})
	}
	out = append(out,
		wireBase{"lib/queue", wireLibQueue(libQueueFixture())},
		wireBase{"lib/cuecell", wireLibCueCell(libCueCellSt{Drops: 2, DropsTitle: "2 drops", Cues: 4, CuesTitle: "4 cues"})})
	for n, f := range setFixtures() {
		f.u.setMu.Lock()
		f.u.setSec, f.u.setQuery = f.sec, f.q
		f.u.setMu.Unlock()
		st := f.u.settingsState()
		out = append(out,
			wireBase{"set/" + n, wireSetState(st)},
			wireBase{"set/" + n + "/content", wireSetContent(st.Content)})
	}
	for _, s := range []stv{stOk("ok"), stWarn("warn"), stLive("live")} {
		out = append(out, wireBase{"set/status/" + s.v, wireSetStatus(setStatusSt{V: s.v, T: s.t})})
	}
	for n, st := range pubFixtures() {
		out = append(out,
			wireBase{"pub/" + n, wirePub(st)},
			wireBase{"pub/" + n + "/hero", wirePubHero(st.Body.Hero)})
	}
	return out
}

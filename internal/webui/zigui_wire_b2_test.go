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
		wireExport{"publish_hero_v2", zigui.RenderPublishHeroV2})
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
	for n, st := range pubFixtures() {
		out = append(out,
			wireBase{"pub/" + n, wirePub(st)},
			wireBase{"pub/" + n + "/hero", wirePubHero(st.Body.Hero)})
	}
	return out
}

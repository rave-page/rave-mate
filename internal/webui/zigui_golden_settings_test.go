//go:build zigui

package webui

import (
	"strings"
	"testing"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/version"
	"rave.page/mate/internal/zigui"
)

// Settings golden gate: the Zig renderer must be BYTE-IDENTICAL to the Go renderers for
// representative states - full view + the #set-content pane + #stset-<id> status fragments.
// Run: make zig && GOWORK=off go test -tags zigui ./internal/webui -run TestZig
//
// The fixtures drive the REAL state builders (settingsState/settingsContentState/cardBlocks) off
// a synthetic UI, so every card in every section is exercised: 40+ cards, all block kinds
// (note/hint/empty/field/toggle/gated toggle/select/selectTip/amenu/fpair/btn-row/path-row/
// item-row/kv/install/form/region/raw). The fixture builders themselves are UNTAGGED
// (render_settings_fixtures_test.go) so the stub build shares the corpus.

func TestZigSettingsGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	for name, fx := range setFixtures() {
		t.Run(name, func(t *testing.T) {
			fx.u.setMu.Lock()
			fx.u.setSec, fx.u.setQuery = fx.sec, fx.q
			fx.u.setMu.Unlock()
			if name == "updatesFeed" {
				old := version.FeedURL
				version.FeedURL = "https://feed.rave.page/mate.json"
				defer func() { version.FeedURL = old }()
			}
			if name == "selectOpen" {
				ssOpen("set-ml-codec", "h") // registered by an earlier render of the same UI
				defer func() { ssOpen("set-ml-codec", ""); ssMu.Lock(); ssSts["set-ml-codec"].open = false; ssMu.Unlock() }()
			}

			st := fx.u.settingsState()
			js := stateJSON(st)
			if js == nil {
				t.Fatal("state marshal failed")
			}
			zig, ok := zigui.RenderSettings(js)
			if !ok {
				t.Fatal("zig full render failed")
			}
			assertBytesEqual(t, "full", settingsHTML(st), zig)

			if !st.Available {
				return // the content pane only exists on the available view
			}
			zigFrag(t, "content", setContentHTML(st.Content), stateJSON(st.Content), zigui.RenderSettingsContent)
		})
	}
}

// TestZigSettingsStatusGolden pins the #stset-<id> tick fragment (dot + escaped state line).
func TestZigSettingsStatusGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	for _, s := range []stv{
		stOff(""),
		stOk("https://development.api.rave.page"),
		stOk(`a&b <"c"> 'd'`),
		stWarn("not listening"),
		stLive("recording 3 tracks"),
		stOk("信号 ✓ больш"),
		stWarn(strings.Repeat("e", 500)),
	} {
		st := setStatusSt{V: s.v, T: s.t}
		zigFrag(t, "status", setStatusHTML(st), stateJSON(st), zigui.RenderSettingsStatus)
	}
}

// TestZigSettingsTipGolden pins the tooltip seam on the settings card grid (phase B1b): a
// hand-built #set-content pane covering every place a settings tooltip can sit - card head, field,
// toggle, an fpair kid - in the four shapes the contract names (present / absent / multi-link /
// keybind grid, which no real settings topic carries) plus the raw-bridge fallback a not-yet
// migrated builder would still ship.
func TestZigSettingsTipGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	t.Cleanup(func() { i18n.SetLocale("en") })
	i18n.SetLocale("en")

	fld := func(label, act string) *uiField {
		f := newField(label, act, "v", "text")
		return &f
	}
	tgl := func(label, act string) *uiToggle {
		x := newToggle(label, act, true)
		return &x
	}
	multi := tipTopicSt("account-bridge") // 3 authoritative links
	grid := tipTopicSt("cue-edit")        // the only full keybind grid in the registry
	waveGrid := tipTopicSt("wave-nav")    // grid with no section headers
	esc := tipState(`c&"d"`, `T & <"x">`, "para &<>\n\npara two", nil, []ttLink{{`L & "x"`, `https://x/?a&b="c"`}})

	content := setContentSt{
		Nav: []setNavSt{{ID: "tips", Title: "Tips", Agg: "ok", Active: true}},
		Secs: []setSecSt{{ID: "tips", Title: "Tooltips", Desc: "every tooltip shape", Cards: []setCardSt{
			// card head: structured tip with a keybind grid + a feature switch
			{ID: "grid", Title: "Card with a grid", TipS: grid, St: setStatusSt{V: "ok", T: "on"},
				Tgl: &setSwitchSt{Label: "Enable", On: true},
				Blocks: []setBlock{
					{K: "field", Fld: fld("Field with a grid tip", "set:a"), TipS: waveGrid},
					{K: "toggle", Tgl: tgl("Toggle with 3 links", "set:b"), TipS: multi},
				}},
			// card head: multi-link tip, body mixes tipped + untipped controls
			{ID: "multi", Title: "Card with 3 links", TipS: multi, Desc: "desc", St: setStatusSt{V: "warn", T: "check"},
				Blocks: []setBlock{
					{K: "field", Fld: fld("No tip", "set:c")},
					{K: "toggle", Tgl: tgl("No tip either", "set:d")},
					{K: "fpair", Kids: []setKid{
						{K: "field", Fld: fld("Kid with tip", "set:e"), TipS: tipTopicSt("obssync-fps")},
						{K: "field", Fld: fld("Kid without", "set:f")},
					}},
					// gated toggle: the gate hint wins over any tooltip, exactly like Go
					{K: "toggle", Tgl: tgl("Gated", ""), Gate: "install ffmpeg", TipS: multi},
				}},
			// no tooltip anywhere + the escaping-heavy ad-hoc tip
			{ID: "plain", Title: "Plain card", St: setStatusSt{V: "off", T: ""},
				Blocks: []setBlock{
					{K: "field", Fld: fld(`Esc & <"tip">`, "set:g"), TipS: &esc},
				}},
			// dual-field bridge: an un-migrated builder still ships pre-rendered markup
			{ID: "bridge", Title: "Raw bridge", Tip: tipTopic("icecast"), St: setStatusSt{V: "ok", T: "raw"},
				Blocks: []setBlock{
					{K: "field", Fld: fld("Raw field tip", "set:h"), Tip: tipTopic("fingerprinting")},
					{K: "toggle", Tgl: tgl("Raw toggle tip", "set:i"), Tip: tipTopic("led-feedback")},
					{K: "fpair", Kids: []setKid{{K: "field", Fld: fld("Raw kid tip", "set:j"), Tip: tipTopic("tc-ltc")}}},
				}},
		}}},
	}
	zigFrag(t, "tipContent", setContentHTML(content), stateJSON(content), zigui.RenderSettingsContent)

	// ...and the same pane in every locale: the resolved prose changes, parity must not.
	for _, loc := range i18n.Available() {
		i18n.SetLocale(loc.Code)
		c2 := content
		c2.Secs[0].Cards[0].TipS = tipTopicSt("cue-edit")
		c2.Secs[0].Cards[1].TipS = tipTopicSt("account-bridge")
		t.Run("locale/"+loc.Code, func(t *testing.T) {
			zigFrag(t, "tipContent", setContentHTML(c2), stateJSON(c2), zigui.RenderSettingsContent)
		})
	}
}

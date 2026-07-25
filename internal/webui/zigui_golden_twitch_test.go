//go:build zigui

package webui

import (
	"strings"
	"testing"

	"rave.page/mate/internal/zigui"
)

// Twitch golden gate: Zig renderer must be BYTE-IDENTICAL to the Go renderer for
// representative states, full view + the three live-patched fragments.
// Run: make zig && GOWORK=off go test -tags zigui ./internal/webui -run TestZig

// Row helpers mirror the production constructors: Tags is never nil (JSON null fails the
// Zig slice parse).
func twDay(d string) twRow { return twRow{Kind: "day", Date: d, Tags: []twTag{}} }

func twAlert(variant, text string) twRow {
	return twRow{Kind: "alert", Variant: variant, Text: text, Tags: []twTag{}}
}

func twChat(name, style, text string, tags []twTag, modVal, modTitle string) twRow {
	if tags == nil {
		tags = []twTag{}
	}
	return twRow{Kind: "chat", Name: name, NameStyle: style, Tags: tags, Text: text,
		Mod: modVal != "", ModVal: modVal, ModTitle: modTitle}
}

// twFixtures: unavailable, empty, populated (chat log history w/ day separators + alerts),
// escaping edge, long values, unicode.
func twFixtures() map[string]twState {
	base := func() twState {
		return twState{
			Title: "Twitch", Sub: "Chat, alerts and stream control", Available: true,
			Unavailable: "Twitch unavailable",
			ShowObs:     true, ObsTitle: "Streaming cockpit",
			Obs: twObsState{
				Viewers: twViewerState{Cls: "tw-vc tw-vc--off", Text: "Viewers unknown"},
				Cockpit: `<div class=cockpit><button class="rp-btn rp-btn--go" data-act="obs-stream">Go live</button></div>`,
			},
			ShowPresets: true, PresetsTitle: "Stream title",
			Presets: twPresetsState{Chips: []uiBtn{}, Empty: "No presets yet",
				Manage: "Manage presets", Add: "Add preset"},
			Feed:     twFeedState{Empty: "No messages yet", Rows: []twRow{}},
			ShowSend: true, SendPH: "Send a message…", SendLbl: "Send",
		}
	}

	unavailable := twState{Title: "Twitch", Sub: "Chat, alerts and stream control",
		Unavailable: "Twitch unavailable",
		Presets:     twPresetsState{Chips: []uiBtn{}},
		Feed:        twFeedState{Empty: "No messages yet", Rows: []twRow{}}}

	empty := base()

	// no OBS control + no config → both sections gone, feed only
	minimal := base()
	minimal.ShowObs, minimal.ShowPresets, minimal.ShowSend = false, false, false
	minimal.Obs = twObsState{}

	populated := base()
	populated.Obs.Viewers = twViewerState{Cls: "tw-vc tw-vc--live", Text: "1,234 viewers"}
	populated.Presets.Chips = []uiBtn{
		{Label: "Techno set", Variant: "outline", Act: "tw-apply:0"},
		{Label: "Drum & bass", Variant: "outline", Act: "tw-apply:1"},
	}
	populated.Feed.Rows = []twRow{
		twDay("2026-07-24"),
		twChat("dymattic", "color:#08F79B", "opening set in 5",
			[]twTag{{Text: "HOST", Variant: "error"}}, "m1|u1|dymattic", "Moderate"),
		twAlert("follow", "raverX followed"),
		twDay("2026-07-25"),
		twChat("modbot", "color:var(--rp-base,#F70864)", "welcome everyone",
			[]twTag{{Text: "MOD", Variant: "success"}, {Text: "SUB", Variant: "secondary"}}, "", ""),
		twChat("cheerer", "color:#FFB547", "500 bits: lets go",
			[]twTag{{Text: "VIP", Variant: "info"}, {Text: "CHEER", Variant: "warning"}}, "m2|u2|cheerer", "Moderate"),
		twAlert("sub", "someone subscribed Tier 2"),
		twAlert("cheer", "anonymous cheered 100 bits"),
	}

	escaping := base()
	escaping.Title = `Twi&tch <"live">`
	escaping.Sub = `a&b<c>"d"'e'`
	escaping.ObsTitle = `C&ockpit"<>'`
	escaping.Obs.Viewers = twViewerState{Cls: "tw-vc tw-vc--live", Text: `1,2&34 "viewers"'<>`}
	escaping.Obs.Cockpit = `<div class="raw & kept"><b>'unescaped'</b></div>`
	escaping.PresetsTitle = `P&resets"<>'`
	escaping.Presets.Chips = []uiBtn{{Label: `D&B <"jungle">'`, Variant: "outline", Act: `tw-apply:0&"`}}
	escaping.Presets.Manage = `M&anage"<>'`
	escaping.Presets.Add = `A&dd"<>'`
	escaping.SendPH = `S&end "here"'<>`
	escaping.SendLbl = `S&end"<>'`
	escaping.Feed.Rows = []twRow{
		twDay(`2026&07"25`),
		twChat(`d&j"<>'`, "color:#08F79B", `msg &<>"' end`,
			[]twTag{{Text: `H&OST"<>'`, Variant: "error"}}, `m&1"|u<1>|d'j`, `M&od"<>'`),
		twAlert("sub", `a&b "subscribed" <t2>'`),
	}

	long := base()
	longS := strings.Repeat("very-long-", 120)
	long.Sub = longS
	long.Obs.Viewers = twViewerState{Cls: "tw-vc tw-vc--live", Text: longS}
	long.Obs.Cockpit = "<div>" + strings.Repeat("c", 2000) + "</div>"
	long.Presets.Chips = []uiBtn{{Label: longS, Variant: "outline", Act: "tw-apply:0"}}
	long.Feed.Rows = []twRow{
		twChat(strings.Repeat("n", 400), "color:#08F79B", longS,
			[]twTag{{Text: strings.Repeat("t", 120), Variant: "info"}}, strings.Repeat("m", 300), longS),
		twAlert("cheer", longS),
	}
	long.SendPH = longS

	unicode := base()
	unicode.Title = "ツイッチ 🎧"
	unicode.Sub = "größer Твич"
	unicode.ObsTitle = "Кабина трансляции"
	unicode.Obs.Viewers = twViewerState{Cls: "tw-vc tw-vc--live", Text: "1 234 глядачі"}
	unicode.PresetsTitle = "タイトル プリセット"
	unicode.Presets.Chips = []uiBtn{{Label: "Техно сет 🎛️", Variant: "outline", Act: "tw-apply:0"}}
	unicode.Presets.Manage = "Керувати"
	unicode.Presets.Add = "追加"
	unicode.SendPH = "Напишите сообщение…"
	unicode.SendLbl = "送信"
	unicode.Feed.Rows = []twRow{
		twDay("2026-07-25"),
		twChat("участник☂", "color:#08F79B", "中文 emoji 🎛️ ラヴ",
			[]twTag{{Text: "ホスト", Variant: "error"}}, "", ""),
		twAlert("sub", "хтось підписався 🎉"),
	}

	return map[string]twState{
		"unavailable": unavailable,
		"empty":       empty,
		"minimal":     minimal,
		"populated":   populated,
		"escaping":    escaping,
		"long":        long,
		"unicode":     unicode,
	}
}

func TestZigTwitchGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	for name, st := range twFixtures() {
		t.Run(name, func(t *testing.T) {
			js := stateJSON(st)
			if js == nil {
				t.Fatal("state marshal failed")
			}
			zig, ok := zigui.RenderTwitch(js)
			if !ok {
				t.Fatal("zig full render failed")
			}
			assertBytesEqual(t, "full", twitchHTML(st), zig)

			zigFrag(t, "feed", twFeedHTML(st.Feed), stateJSON(st.Feed), zigui.RenderTwitchFeed)
			if st.ShowObs {
				zigFrag(t, "obs", twObsHTML(st.Obs), stateJSON(st.Obs), zigui.RenderTwitchObs)
			}
			if st.ShowPresets {
				zigFrag(t, "presets", twPresetsHTML(st.Presets), stateJSON(st.Presets), zigui.RenderTwitchPresets)
			}
		})
	}
}

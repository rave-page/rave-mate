//go:build zigui

package webui

import (
	"strings"
	"testing"

	"rave.page/mate/internal/zigui"
)

// Live golden gate: Zig renderer must be BYTE-IDENTICAL to the Go renderer for
// representative states - the full cockpit AND every ~1 Hz tick-patched fragment
// (live_ticks.go patches them individually, so each funnel needs its own assertion).
// Run: make zig && GOWORK=off go test -tags zigui ./internal/webui -run TestZig

// liveFixtures: unavailable (no optional services), empty, populated, escaping edge,
// long values, unicode.
func liveFixtures() map[string]liveState {
	base := func() liveState {
		return liveState{
			Title: "Live", Sub: "Mid-set cockpit",
			Transport: liveTransportSt{
				StreamHint: "Metadata only — never audio/video", StreamLabel: "STREAM",
				DotVar: "muted", State: "Idle", MetaOnly: "now-playing metadata only",
				PauseLabel: "Pause live signal", PauseHint: "Private stream: stop publishing",
			},
			NP:          liveNPSt{Line1: "No track", Line2: ""},
			StatusTitle: "Status",
			Status: liveStatusSt{Rows: []liveKV{
				liveRow("API", "https://development.api.rave.page"),
				liveRow("Account", "Not signed in"),
				liveRow("Traktor", "-"),
				liveRow("Stream", "Idle"),
			}},
			DecksTitle: "Decks",
			Decks: liveDecksSt{Decks: []liveDeck{
				{Cls: "deckbig", Name: "DECK A", Title: "–", Meta: "-"},
				{Cls: "deckbig", Name: "DECK B", Title: "–", Meta: "-"},
				{Cls: "deckbig", Name: "DECK C", Title: "–", Meta: "-"},
				{Cls: "deckbig", Name: "DECK D", Title: "–", Meta: "-"},
			}},
			Signals: liveSignalsSt{Rows: []liveKV{}},
			Cockpit: liveCockpitSt{Rows: []liveCockpitRow{}},
			Link:    liveLinkSt{Sources: []liveSRow{}},
			Strip:   liveStripSt{},
		}
	}

	// no optional service wired: only transport/np/status/decks/strip render
	unavailable := base()

	// every section present but idle/empty
	empty := base()
	empty.HasSignals, empty.SignalsTitle, empty.SignalsTip = true, "Signal sources", `<label class=tt data-label="tt-signal-sources">i</label>`
	empty.Signals = liveSignalsSt{Rows: []liveKV{
		liveRow("Channel 1", "EQ lo — · EQ mid — · EQ hi — · Filter — · Fader —"),
	}}
	empty.HasCockpit, empty.CockpitTitle = true, "Streaming cockpit"
	empty.Cockpit = liveCockpitSt{Empty: "No OBS instances", Rows: []liveCockpitRow{}}
	empty.HasLink, empty.LinkTitle = true, "Ableton Link"
	empty.Link = liveLinkSt{Backend: liveSR("warning", "Backend", "Link unavailable (build without abletonlink)"), Sources: []liveSRow{}}
	empty.HasNet, empty.NetTitle, empty.TimTitle = true, "Network", "Timing"
	empty.NetTip, empty.TimTip = `<label class=tt data-label="tt-network-graph">i</label>`, `<label class=tt data-label="tt-timing-graph">i</label>`
	empty.Net = liveGraphSt{Tooltip: "Peer + API throughput", Legend: `<span style="color:#08F79B">peer ↓ 0 B/s</span>`, Graph: `<svg viewBox="0 0 600 56" class="spark"></svg>`}
	empty.Tim = liveGraphSt{Tooltip: "Peer RTT", Legend: `<span>No peers</span>`, Graph: `<svg viewBox="0 0 600 56" class="spark"></svg>`}
	empty.HasPerf, empty.PerfTitle, empty.PerfTip = true, "System performance", `<label class=tt data-label="tt-perf-graph">i</label>`
	empty.Perf = livePerfSt{Tooltip: "App vs system", CPULeg: `<span>App -</span><span>Sys -</span>`,
		CPUGraph: `<svg class="spark"></svg>`, RAMLeg: `<span>App -</span><span>Sys -</span>`,
		RAMGraph: `<svg class="spark"></svg>`, Head: "Headroom -", HeadColor: sparkMint}

	// mid-set: live stream, recording, timecode, decks playing, peers, link enabled
	populated := empty
	populated.Transport = liveTransportSt{
		StreamHint: "Metadata only", StreamLabel: "STREAM", DotVar: "success", State: "Live",
		MetaOnly: "now-playing metadata only", PauseLabel: "Pause live signal", PauseHint: "hint", Paused: true,
		HasRec: true, RecHint: "Records the master bus", RecLabel: "REC", RecBtn: "Stop",
		RecState: "● manual · set-2026-07-25.flac",
		HasTC:    true, TCLabel: "TC", TC: "01:23:45:12", StartLbl: "Start", StopLbl: "STOP",
	}
	populated.NP = liveNPSt{Line1: "♪ ARTIST - TRACK TITLE", Line2: "Deck A  1:23 / 6:45    128.0 BPM    8A"}
	populated.Decks = liveDecksSt{Note: "Mirrored from peer studio-pc", Decks: []liveDeck{
		{Cls: "deckbig deckbig--live deckbig--audible", Name: "DECK A", Title: "artist - track", Meta: "1:23 / 6:45 · 128.0 BPM · 8A", Via: "via traktor, midi"},
		{Cls: "deckbig deckbig--live", Name: "DECK B", Title: "other - tune", Meta: "0:12 / 5:00 · 126.0 BPM"},
		{Cls: "deckbig", Name: "DECK C", Title: "–", Meta: "-"},
		{Cls: "deckbig", Name: "DECK D", Title: "–", Meta: "-"},
		{Cls: "deckbig", Name: "DECK E", Title: "–", Meta: "-"},
	}}
	populated.Cockpit = liveCockpitSt{Empty: "No OBS instances", Caption: "Start/stop OBS here; recordings link to the tracklist.",
		Rows: []liveCockpitRow{
			{Variant: "error", Name: "studio-pc (this PC)", State: "Streaming 6000 kbps",
				StreamLbl: "Stop stream", StreamAct: "obs-stream:local", RecLbl: "Start recording", RecAct: "obs-record:local"},
			{Variant: "success", Name: "vj-box", State: "Ready",
				StreamLbl: "Start stream", StreamAct: "obs-stream:n2", RecLbl: "Stop recording", RecAct: "obs-record:n2"},
		}}
	populated.Link = liveLinkSt{Available: true, Fill: pbarPct(37.5), Cap: "Beat 7 / 16",
		Session: liveSR("success", "Session", "128.0 BPM · 2 peers · enabled"), ResyncLbl: "Resync",
		Sources: []liveSRow{
			liveSR("success", "media-source", "seek · err +12ms · 3 corr/min"),
			liveSR("error", "vj-loop", "error: source not found · err -0ms · 0 corr/min"),
		}}
	populated.Signals = liveSignalsSt{Rows: []liveKV{
		liveRow("Channel 1", "EQ lo traktor · EQ mid traktor · EQ hi traktor · Filter midi · Fader traktor"),
		liveRow("Channel 2", "EQ lo — · EQ mid — · EQ hi — · Filter — · Fader —"),
		liveRow("traktor", "receiving (2s ago)"),
		liveRow("midi", "idle"),
		liveRow("ravemidi in 1", "bound"),
		liveRow("LoopBe Internal MIDI", "port muted (anti-feedback)"),
	}}
	populated.Perf = livePerfSt{Tooltip: "App vs system",
		CPULeg:   `<span style="color:#08F79B">App 3%</span><span style="color:#FF3E8A">Sys 41%</span>`,
		CPUGraph: `<svg viewBox="0 0 600 56" class="spark"><polyline points="0.0,55.0 600.0,10.0" fill="none" stroke="#08F79B" stroke-width="1.5"/></svg>`,
		RAMLeg:   `<span style="color:#7C3AED">App 182 MB</span><span style="color:#FFB547">Sys 12.4/32.0 GB</span>`,
		RAMGraph: `<svg viewBox="0 0 600 56" class="spark"></svg>`,
		Head:     "19.6 GB + 59% CPU free", HeadColor: sparkMint}
	populated.Strip = liveStripSt{Left: "OBS recording 12m3s · capture 1.2 GB · audio rec",
		Center: "TC 01:23:45:12 · DMX active", Right: "twitch: dymattic · 59% CPU / 19.6 GB free"}

	escaping := empty
	escaping.Title = `Li&ve <"cockpit">`
	escaping.Sub = `a&b<c>"d"'e'`
	escaping.StatusTitle = `St&atus <"now">`
	escaping.DecksTitle = `De&cks'`
	escaping.SignalsTitle = `Si&gnals <"src">`
	escaping.CockpitTitle = `C&ockpit"`
	escaping.LinkTitle = `Li&nk<>`
	escaping.NetTitle, escaping.TimTitle, escaping.PerfTitle = `Ne&t"`, `Ti&ming<>`, `Pe&rf'`
	escaping.Transport = liveTransportSt{
		StreamHint: `hint & <"quoted"> 'x'`, StreamLabel: `ST&REAM<>`, DotVar: "warning", State: `Pa&used "x"`,
		MetaOnly: `meta & <only>`, PauseLabel: `Pa&use"`, PauseHint: `p<h>&"'`, Paused: true,
		HasRec: true, RecHint: `rec & <hint>"`, RecLabel: `RE&C<>`, RecBtn: `St&op"`, RecState: `● ma&nual · se"t<>.flac`,
		HasTC: true, TCLabel: `T&C<>`, TC: `01:23:45:12 ◼`, StartLbl: `St&art"`, StopLbl: `ST<OP>`,
	}
	escaping.NP = liveNPSt{Line1: `♪ A&B - <"TRACK">`, Line2: `Deck A  1:23 / 6:45 & "more"`}
	escaping.Status = liveStatusSt{Rows: []liveKV{
		liveRow(`AP&I<>`, `https://x/?a=1&b=2<>"'`),
		liveRow(`Ac&count"`, `sig&ned <in>`),
	}}
	escaping.Decks = liveDecksSt{Note: `no&te <"peer">`, Decks: []liveDeck{
		{Cls: "deckbig deckbig--live", Name: `DE&CK A<>`, Title: `ar&tist - "track" <x>`, Meta: `1:23 / 6:45 & 128.0`, Via: `via tra&ktor, "midi"`},
	}}
	escaping.Signals = liveSignalsSt{Rows: []liveKV{liveRow(`Ch&annel <1>`, `EQ lo tra&ktor · Filter "midi"`)}}
	escaping.Cockpit = liveCockpitSt{Empty: `n&one<>`, Caption: `ca&ption <"x">`, Rows: []liveCockpitRow{
		{Variant: "error", Name: `stu&dio "pc" <1>`, State: `Str&eaming <6000>`,
			StreamLbl: `Sto&p "stream"`, StreamAct: `obs-stream:n&"1'<>`, RecLbl: `St&art rec"`, RecAct: `obs-record:n&"1'<>`},
	}}
	escaping.Link = liveLinkSt{Available: true, Fill: pbarPct(100.126), Cap: `Be&at 7 / <16>`,
		Session: liveSR(`success`, `Se&ssion "x"`, `128.0 & <"enabled">`), ResyncLbl: `Re&sync<>`,
		Sources: []liveSRow{liveSR("error", `so&urce "1"`, `error: <not found> & "x" · err -3ms · 0 corr/min`)}}
	escaping.Net = liveGraphSt{Tooltip: `to&oltip <"x">`, Legend: `<span style="color:#08F79B">pe&er ↓</span>`, Graph: `<svg data-x="1&2"/>`}
	escaping.Perf = livePerfSt{Tooltip: `pe&rf <"tip">`, CPULeg: `<span>A&pp</span>`, CPUGraph: `<svg id="c&1"/>`,
		RAMLeg: `<span>R&AM</span>`, RAMGraph: `<svg id="r&1"/>`, Head: `he&ad <"room">`, HeadColor: sparkMint}
	escaping.Strip = liveStripSt{Left: `OBS & <rec>`, Center: `TC "01" & 02`, Right: `twitch: dy&mattic <'x'>`}

	long := empty
	longS := strings.Repeat("very-long-status-value-", 120)
	long.Status = liveStatusSt{Rows: []liveKV{liveRow("API", longS), liveRow(strings.Repeat("K", 300), longS)}}
	long.NP = liveNPSt{Line1: "♪ " + strings.ToUpper(longS), Line2: longS}
	long.Decks = liveDecksSt{Note: longS, Decks: []liveDeck{
		{Cls: "deckbig deckbig--live", Name: "DECK A", Title: longS, Meta: longS, Via: longS},
	}}
	long.Signals = liveSignalsSt{Rows: []liveKV{liveRow(strings.Repeat("s", 200), longS)}}
	long.Net = liveGraphSt{Tooltip: longS, Legend: strings.Repeat(`<span style="color:#08F79B">x</span>`, 40),
		Graph: `<svg>` + strings.Repeat(`<polyline points="0.0,1.0 2.0,3.0"/>`, 200) + `</svg>`}
	long.Strip = liveStripSt{Left: longS, Center: longS, Right: longS}

	unicode := empty
	unicode.Title = "Лайв 🎧"
	unicode.Sub = "größer ライブ"
	unicode.StatusTitle = "状態"
	unicode.DecksTitle = "Деки"
	unicode.SignalsTitle = "Сигналы"
	unicode.CockpitTitle = "コックピット"
	unicode.LinkTitle = "Ableton Link 同期"
	unicode.NetTitle, unicode.TimTitle, unicode.PerfTitle = "Сеть", "タイミング", "Производительность"
	unicode.Transport = liveTransportSt{StreamHint: "メタデータのみ", StreamLabel: "СТРИМ", DotVar: "success",
		State: "В эфире", MetaOnly: "メタデータのみ - 音声/映像なし", PauseLabel: "Пауза", PauseHint: "приватный стрим",
		HasRec: true, RecHint: "запись", RecLabel: "ЗАПИСЬ", RecBtn: "停止", RecState: "● авто · сет-2026.flac"}
	unicode.NP = liveNPSt{Line1: "♪ АРТИСТ - 中文 トラック 🎛️", Line2: "Дек A  1:23 / 6:45    128.0 BPM    8A"}
	unicode.Status = liveStatusSt{Rows: []liveKV{liveRow("API", "https://api.rave.page"), liveRow("Аккаунт", "Вошли как dymattic")}}
	unicode.Decks = liveDecksSt{Note: "Зеркалируется с пира студия-пк", Decks: []liveDeck{
		{Cls: "deckbig deckbig--live", Name: "ДЕК A", Title: "артист - 曲名 🎵", Meta: "1:23 / 6:45 · 128.0 BPM", Via: "через traktor, миди"},
	}}
	unicode.Signals = liveSignalsSt{Rows: []liveKV{liveRow("Канал 1", "EQ lo трактор · Фильтр миди")}}
	unicode.Cockpit = liveCockpitSt{Empty: "нет OBS", Caption: "Управление OBS 🎥", Rows: []liveCockpitRow{
		{Variant: "success", Name: "студия-пк (этот ПК)", State: "Готов", StreamLbl: "Начать стрим",
			StreamAct: "obs-stream:локальный", RecLbl: "Начать запись", RecAct: "obs-record:локальный"},
	}}
	unicode.Link = liveLinkSt{Available: true, Fill: pbarPct(6.25), Cap: "Такт 2 / 16",
		Session: liveSR("success", "Сессия", "128.0 BPM · 2 пира · включено"), ResyncLbl: "Пересинхронизировать",
		Sources: []liveSRow{liveSR("warning", "медиа-источник", "ожидание · err +0ms · 0 corr/min")}}
	unicode.Strip = liveStripSt{Left: "OBS пишет 12м · захват 1,2 ГБ", Center: "TC 01:23:45:12 · DMX активен",
		Right: "twitch: dymattic · 59% CPU / 19,6 ГБ свободно"}

	return map[string]liveState{
		"unavailable": unavailable,
		"empty":       empty,
		"populated":   populated,
		"escaping":    escaping,
		"long":        long,
		"unicode":     unicode,
	}
}

func TestZigLiveGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	for name, st := range liveFixtures() {
		t.Run(name, func(t *testing.T) {
			js := stateJSON(st)
			if js == nil {
				t.Fatal("state marshal failed")
			}
			zig, ok := zigui.RenderLive(js)
			if !ok {
				t.Fatal("zig full render failed")
			}
			assertBytesEqual(t, "full", liveHTML(st), zig)

			// every tick-patched fragment funnel (live_ticks.go patches each id on its own)
			assertFrag(t, "transport", st.Transport, liveTransHTML)
			assertFrag(t, "np", st.NP, liveNPHTML)
			assertFrag(t, "status", st.Status, liveStatusFragHTML)
			assertFrag(t, "decks", st.Decks, liveDecksFragHTML)
			assertFrag(t, "signals", st.Signals, liveSignalsFragHTML)
			assertFrag(t, "cockpit", st.Cockpit, liveCockpitFragHTML)
			assertFrag(t, "link", st.Link, liveLinkFragHTML)
			assertFrag(t, "graph", st.Net, liveGraphFragHTML)
			assertFrag(t, "graph", st.Tim, liveGraphFragHTML)
			assertFrag(t, "perf", st.Perf, livePerfFragHTML)
			assertFrag(t, "strip", st.Strip, liveStripFragHTML)
		})
	}
}

// assertFrag asserts one fragment kind renders byte-identically in Zig and Go.
func assertFrag[T any](t *testing.T, kind string, st T, goHTML func(T) string) {
	t.Helper()
	js := stateJSON(st)
	if js == nil {
		t.Fatalf("%s: state marshal failed", kind)
	}
	zig, ok := zigui.RenderLiveFrag(kind, js)
	if !ok {
		t.Fatalf("%s: zig render failed", kind)
	}
	assertBytesEqual(t, kind, goHTML(st), zig)
}

// TestZigLiveFragUnknownKind: an unknown fragment kind must report !ok so the bridge
// falls back to Go instead of emitting nothing.
func TestZigLiveFragUnknownKind(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable")
	}
	if _, ok := zigui.RenderLiveFrag("nope", []byte(`{}`)); ok {
		t.Fatal("unknown kind rendered")
	}
	if _, ok := zigui.RenderLiveFrag("", []byte(`{}`)); ok {
		t.Fatal("empty kind rendered")
	}
}

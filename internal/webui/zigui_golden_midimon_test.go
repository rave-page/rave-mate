//go:build zigui

package webui

import (
	"strings"
	"testing"

	"rave.page/mate/internal/zigui"
)

// MIDI monitor + wire-trace golden gate: Zig must be BYTE-IDENTICAL to the Go renderers.
// Run: make zig && GOWORK=off go test -tags zigui ./internal/webui -run TestZig

// midiMonFixtures: unavailable (no bus rows), empty, populated, escaping, long, unicode.
func midiMonFixtures() map[string]midiMonState {
	base := func() midiMonState {
		return midiMonState{
			Card: "Input monitor", Badge: "live", Sub: "Press a control to see which device it is",
			Lines: midiMonLines{Empty: "No MIDI input yet", Rows: []midiMonRow{}},
		}
	}
	unavailable := base()
	unavailable.Lines.Empty = "MIDI monitor not wired"

	empty := base()

	populated := base()
	populated.Lines.Rows = []midiMonRow{
		{Ago: "now", Src: "DDJ-400", Msg: "CC 20 ch1 = 127"},
		{Ago: "2s", Src: "DDJ-400", Msg: "Note On 36 ch1"},
		{Ago: "5m", Src: "Traktor Kontrol Z1", Msg: "CC 7 ch2 = 64"},
		{Ago: "1h", Src: "", Msg: "sysex"},
	}

	escaping := base()
	escaping.Card = `Mon&itor <"in">`
	escaping.Badge = `l'ive&`
	escaping.Sub = `a&b<c>"d"'e'`
	escaping.Lines.Empty = `no&thing <"yet">`
	escaping.Lines.Rows = []midiMonRow{
		{Ago: `1s&`, Src: `DDJ<"400">'x'`, Msg: `CC 20 & <ch1> "127"`},
	}

	long := base()
	longS := strings.Repeat("very-long-device-", 90)
	long.Lines.Rows = []midiMonRow{{Ago: "999h", Src: longS, Msg: strings.Repeat("m", 800)}}

	unicode := base()
	unicode.Card = "モニター 🎛️"
	unicode.Sub = "größer Монитор"
	unicode.Lines.Rows = []midiMonRow{{Ago: "сейчас", Src: "コントローラー", Msg: "нота 36 · 中文 🎧"}}

	return map[string]midiMonState{
		"unavailable": unavailable,
		"empty":       empty,
		"populated":   populated,
		"escaping":    escaping,
		"long":        long,
		"unicode":     unicode,
	}
}

// midiTraceFixtures: unavailable (ioctl error), empty ring, populated, escaping, long, unicode.
func midiTraceFixtures() map[string]midiTraceState {
	base := func() midiTraceState {
		return midiTraceState{
			Hdr: "Wire trace · port 3", Empty: "Trace ring empty",
			Rows: []midiTraceRow{}, Refresh: "Refresh", Close: "Close",
		}
	}
	unavailable := base()
	unavailable.HasErr, unavailable.Err = true, "IOCTL_RAVEMIDI_QUERY_TRACE failed: 0x1F"

	empty := base()

	populated := base()
	populated.Rows = []midiTraceRow{
		{Dir: "0", Label: "tap raw", Hex: "F0 7E 00 06 01 F7", Len: "6B"},
		{DT: "+3ms", Dir: "1", Label: "to app", Hex: "B0 14 7F", Len: "3B", Dec: "CC 20 = 127"},
		{DT: "+12ms", Dir: "2", Label: "read pop", Hex: "90 24 64", Len: "3B", Dec: "Note On 36"},
		{DT: "+1200ms", Dir: "4", Label: "feedback", Hex: "80 24 00", Len: "3B", Dec: "Note Off 36"},
	}

	escaping := base()
	escaping.Hdr = `Trace &"port" <3>`
	escaping.Empty = `emp&ty<">`
	escaping.Refresh = `Re&fresh"`
	escaping.Close = `C<lose>'`
	escaping.Rows = []midiTraceRow{
		{DT: `+1ms&`, Dir: "9", Label: `to&"app"<>`, Hex: `B0 & 14`, Len: `3B'`, Dec: `CC 20 & <"x">`},
	}

	longFx := base()
	longFx.Hdr = strings.Repeat("hdr-", 200)
	longFx.Rows = []midiTraceRow{{
		DT: "+999999ms", Dir: "6", Label: strings.Repeat("dir-", 100),
		Hex: strings.Repeat("FF ", 300), Len: "512B", Dec: strings.Repeat("d", 600),
	}}

	unicode := base()
	unicode.Hdr = "トレース · порт 3"
	unicode.Empty = "пусто"
	unicode.Refresh = "Обновить"
	unicode.Close = "閉じる"
	unicode.Rows = []midiTraceRow{{DT: "+7ms", Dir: "3", Label: "от приложения", Hex: "B0 14 7F", Len: "3B", Dec: "中文 🎹"}}

	return map[string]midiTraceState{
		"unavailable": unavailable,
		"empty":       empty,
		"populated":   populated,
		"escaping":    escaping,
		"long":        longFx,
		"unicode":     unicode,
	}
}

func TestZigMIDIMonGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	for name, st := range midiMonFixtures() {
		t.Run(name, func(t *testing.T) {
			js := stateJSON(st)
			if js == nil {
				t.Fatal("state marshal failed")
			}
			zig, ok := zigui.RenderMIDIMon(js)
			if !ok {
				t.Fatal("zig card render failed")
			}
			assertBytesEqual(t, "card", midiMonHTML(st), zig)

			ljs := stateJSON(st.Lines)
			if ljs == nil {
				t.Fatal("lines marshal failed")
			}
			zigRows, ok := zigui.RenderMIDIMonRows(ljs)
			if !ok {
				t.Fatal("zig rows render failed")
			}
			assertBytesEqual(t, "rows", midiMonRowsHTML(st.Lines), zigRows)
		})
	}
}

func TestZigMIDITraceGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	for name, st := range midiTraceFixtures() {
		t.Run(name, func(t *testing.T) {
			js := stateJSON(st)
			if js == nil {
				t.Fatal("state marshal failed")
			}
			zig, ok := zigui.RenderMIDITrace(js)
			if !ok {
				t.Fatal("zig trace render failed")
			}
			assertBytesEqual(t, "trace", midiTraceHTML(st), zig)
		})
	}
}

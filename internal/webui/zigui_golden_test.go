//go:build zigui

package webui

import (
	"strings"
	"testing"

	"rave.page/mate/internal/zigui"
)

// Per-tab regression gate for the Zig UI migration: for representative states the Zig
// renderer must be BYTE-IDENTICAL to the Go renderer (which stays the golden reference).
// Run: make zig && GOWORK=off go test -tags zigui ./internal/webui -run TestZig

// agFixtures: empty, unavailable, populated, escaping edge, long values, unicode.
func agFixtures() map[string]agState {
	long := strings.Repeat("Really-Long-Group-Name-", 60)
	return map[string]agState{
		"unavailable": {
			Title: "App Groups", Unavailable: "App groups unavailable",
			Empty: "none", Groups: []agGroup{},
		},
		"empty": {
			Title: "App Groups", Subtitle: "Relaunch named app sets after a crash",
			Available: true, Empty: "No app groups configured yet", Groups: []agGroup{},
		},
		"populated": {
			Title: "App Groups", Subtitle: "sub", Available: true,
			Admin: "admin", Launch: "Launch",
			Groups: []agGroup{
				{ID: "g1", Name: "Stream rig", Up: "2/3 up", Variant: "warning", Apps: []agApp{
					{Base: "obs64.exe"}, {Base: "traktor.exe", Elevated: true}, {Base: "resolume.exe"},
				}},
				{ID: "g2", Name: "Idle", Up: "0/1 up", Variant: "muted", Apps: []agApp{{Base: "vlc.exe"}}},
				{ID: "g3", Name: "All up", Up: "1/1 up", Variant: "success", Apps: []agApp{{Base: "a.exe", Elevated: true}}},
			},
		},
		"escaping": {
			Title: `R&B <"live"> 'set'`, Subtitle: `a&b<c>"d"'e'`, Available: true,
			Admin: `<admin&>`, Launch: `Launch & <go>"now"'!'`,
			Groups: []agGroup{
				{ID: `g:1"x'<&>`, Name: `A&B <"quoted'>`, Up: `1/2 & <up>`, Variant: "warning", Apps: []agApp{
					{Base: `we"ird&app'<>.exe`, Elevated: true},
				}},
			},
		},
		"long": {
			Title: "T", Subtitle: "S", Available: true, Admin: "admin", Launch: "Launch",
			Groups: []agGroup{
				{ID: strings.Repeat("id-", 200), Name: long, Up: "0/0 up", Variant: "muted",
					Apps: []agApp{{Base: strings.Repeat("x", 1000) + ".exe"}}},
			},
		},
		"unicode": {
			Title: "アプリ グループ", Subtitle: "größer 🎧", Available: true, Admin: "адмін", Launch: "Запустити",
			Groups: []agGroup{
				{ID: "g☂", Name: "Кириллица + 中文 + emoji 🎛️", Up: "1/1 上", Variant: "success",
					Apps: []agApp{{Base: "приложение.exe", Elevated: true}}},
			},
		},
	}
}

func TestZigAppGroupsGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	for name, st := range agFixtures() {
		t.Run(name, func(t *testing.T) {
			js := stateJSON(st)
			if js == nil {
				t.Fatal("state marshal failed")
			}
			zig, ok := zigui.RenderAppGroups(js)
			if !ok {
				t.Fatal("zig full render failed")
			}
			assertBytesEqual(t, "full", appGroupsHTML(st), zig)

			zigBody, ok := zigui.RenderAppGroupsBody(js)
			if !ok {
				t.Fatal("zig body render failed")
			}
			assertBytesEqual(t, "body", appGroupsBodyHTML(st), zigBody)
		})
	}
}

// assertBytesEqual fails with the first divergence offset + context windows.
func assertBytesEqual(t *testing.T, what, want, got string) {
	t.Helper()
	if want == got {
		return
	}
	i := 0
	for i < len(want) && i < len(got) && want[i] == got[i] {
		i++
	}
	lo := max(0, i-40)
	end := func(s string) string { return s[lo:min(len(s), i+40)] }
	t.Errorf("%s: diverges at byte %d (want len %d, got len %d)\nwant …%q…\ngot  …%q…",
		what, i, len(want), len(got), end(want), end(got))
}

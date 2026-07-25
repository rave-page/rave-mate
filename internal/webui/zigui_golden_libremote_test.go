//go:build zigui

package webui

import (
	"strings"
	"testing"

	"rave.page/mate/internal/zigui"
)

// library_remote golden gate: the "Controlling [This computer ▾]" switcher must render
// byte-identically in Zig. Run: make zig && GOWORK=off go test -tags zigui ./internal/webui -run TestZig

// libRemoteFixtures: hidden, closed, open, filtered-to-nothing, escaping, long, unicode.
func libRemoteFixtures() map[string]libRemoteSt {
	sel := func(cur string, open bool, filter string, rows ...selRow) selState {
		s := selState{ID: "libtarget", Label: "Controlling", CurLabel: cur, Open: open, Filter: filter, Rows: []selRow{}}
		s.Rows = append(s.Rows, rows...)
		return s
	}
	local := selRow{Val: "", Label: "This computer"}

	return map[string]libRemoteSt{
		// no peer connected / headless remote session → no row at all
		"hidden": {Sel: emptySel()},
		"closed": {Show: true, Sel: sel("This computer", false, "",
			local, selRow{Val: "node-1", Label: "▸ Studio PC"})},
		"open": {Show: true, Sel: sel("▸ Studio PC", true, "",
			local,
			selRow{Val: "node-1", Label: "▸ Studio PC", Cur: true},
			selRow{Val: "node-2", Label: "▸ Booth Mac"})},
		// filter matched nothing → ss-none
		"filteredEmpty": {Show: true, Sel: sel("This computer", true, "zzz")},
		"escaping": {Show: true, Sel: selState{
			ID: `lib&target"<>'`, Label: `C&ontrolling <"x">'`, CurLabel: `▸ A&B "peer'<>`,
			Open: true, Filter: `f&lt"<>'`,
			Rows: []selRow{
				{Val: "", Label: `T&his "computer"<>`},
				{Val: `n&"1'<>`, Label: `▸ A&B <"quoted'>`, Cur: true},
			},
		}},
		"long": {Show: true, Sel: selState{
			ID: strings.Repeat("libtarget-", 40), Label: strings.Repeat("Controlling-", 60),
			CurLabel: strings.Repeat("▸ peer-", 200), Open: true, Filter: strings.Repeat("f", 500),
			Rows: []selRow{
				{Val: strings.Repeat("n", 300), Label: strings.Repeat("▸ very-long-peer-", 80), Cur: true},
			},
		}},
		"unicode": {Show: true, Sel: selState{
			ID: "libtarget", Label: "Управление", CurLabel: "▸ Студія 中文 🎛️", Open: true, Filter: "студ",
			Rows: []selRow{
				{Val: "", Label: "Этот компьютер"},
				{Val: "узел☂", Label: "▸ Студія 中文 🎛️", Cur: true},
			},
		}},
	}
}

func TestZigLibRemoteGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	for name, st := range libRemoteFixtures() {
		t.Run(name, func(t *testing.T) {
			js := stateJSON(st)
			if js == nil {
				t.Fatal("state marshal failed")
			}
			want := libRemoteHTML(st)
			zig, ok := zigui.RenderLibRemote(js)
			if !ok {
				// A legitimately empty fragment returns NULL; the Go fallback must agree.
				if want != "" {
					t.Fatalf("zig render failed but Go rendered %d bytes", len(want))
				}
				return
			}
			assertBytesEqual(t, "switcher", want, zig)
		})
	}
}

//go:build zigui

package webui

import (
	"sort"
	"strings"
	"testing"

	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/zigui"
)

// Tooltip-primitive golden gate: components.zig renderTip must be BYTE-IDENTICAL to Go
// renderTipSt. Two layers:
//
//  1. the WHOLE helpTopics registry (every id, so every keybind grid + link list in the app)
//     rendered in EVERY installed locale - the branch that used to live inside renderTip
//     (i18n.T per title/body/group/action/mouse-gesture word) now runs Go-side, and this is
//     what proves the split lost nothing;
//  2. synthetic edge fixtures for the shapes the registry does not contain (empty title, blank
//     paragraph runs, escaping, unicode, very long verbose prose, chip-only rows).
//
// Run: make zig && GOWORK=off go test -count=1 -tags zigui ./internal/webui -run TestZigTip

// tipIDs lists the registry ids sorted, so failures name a stable subtest.
func tipIDs() []string {
	out := make([]string, 0, len(helpTopics))
	for id := range helpTopics {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// TestZigTipRegistryGolden renders every registry topic in every installed locale and compares.
func TestZigTipRegistryGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	t.Cleanup(func() { i18n.SetLocale("en") })
	ids := tipIDs()
	for _, loc := range i18n.Available() {
		if got := i18n.SetLocale(loc.Code); got != loc.Code {
			t.Fatalf("locale %q did not activate (got %q)", loc.Code, got)
		}
		for _, id := range ids {
			t.Run(loc.Code+"/"+id, func(t *testing.T) {
				st := tipTopicSt(id)
				if st == nil {
					t.Fatalf("topic %q vanished from the registry", id)
				}
				// the structured path must reproduce the legacy pre-rendered markup exactly
				assertBytesEqual(t, "go-bridge", tipTopic(id), renderTipSt(*st))
				zigFrag(t, "zig", renderTipSt(*st), stateJSON(st), zigui.RenderTip)
			})
		}
	}
}

// TestZigTipEdgeGolden pins the shapes the registry has no example of.
func TestZigTipEdgeGolden(t *testing.T) {
	if !zigui.Available() {
		t.Skip("zigui lib unavailable / ABI mismatch — run `make zig` first")
	}
	long := strings.Repeat("Long-form help is the product: the app teaches while it is used, so a topic "+
		"explains the term, the trade-off and the failure mode in full. ", 40)
	fixtures := map[string]tipSt{
		"empty":       {},
		"titleOnly":   {ID: "t", Title: "Just a title"},
		"noBody":      {ID: "t", Title: "T", Links: []tipLinkSt{{"Only a link", "https://x/"}}},
		"escaping":    tipState(`a"b&c<d>`, `T & <"x"> 'y'`, "p1 & <b>\n\np2 \"q\" 'r'", nil, []ttLink{{`L & <"x">`, `https://x/?a&b="c"&d='e'`}}),
		"unicode":     tipState("uni", "アイコン Б 🎧", "段落 один 🎛️\n\nвторой абзац ✓", nil, []ttLink{{"日本語のリンク ↗", "https://例え.jp/ドキュメント"}}),
		"longVerbose": tipState("verbose", "Verbose by design", long+"\n\n"+long, nil, nil),
		"blankParas":  tipState("blank", "Blank runs", "\n\n\n  \n\nreal paragraph\n\n \t \n\nsecond\n\n", nil, nil),
		"bodyOnlyWs":  tipState("ws", "Whitespace body", "   \n\n \t ", nil, nil),
		"crlfBody":    tipState("crlf", "CRLF body", "a\r\n\r\nb", nil, nil),
		"multiLink":   tipState("multi", "Three sources", "body", nil, virtualMIDILinks()),
		"kbNoGroups":  tipState("wave-nav", "Waveform gestures", "body", waveNavKeys, nil),
		"kbGroups":    tipState("cue-edit", "Cue editor", "body", cueEditKeys, nil),
		"kbRepeatGroup": tipState("kbrep", "Repeated + empty groups", "body", []kbRow{
			{"help.cue-edit.g.nav", []string{"A"}, "help.cue-edit.k.step"},
			{"help.cue-edit.g.nav", []string{"B"}, "help.cue-edit.k.jump"}, // same key → no second header
			{"", []string{"C"}, "help.cue-edit.k.listNav"},
			{"help.cue-edit.g.grid", []string{"D"}, "help.cue-edit.k.nudge"}, // new key → header
			{"help.cue-edit.g.nav", []string{"E"}, "help.cue-edit.k.step"},   // back → header again
		}, nil),
		"kbEdgeCombos": {ID: "kbedge", Title: "Chip edges", Keys: []tipKbSt{
			{Chips: nil, Verb: "NoChips"},
			{Chips: []tipChipSt{{Text: "+", Sep: true}}, Verb: ""},
			{Chips: []tipChipSt{{Text: `a&"b"`}}, Verb: `Do&<x>`, Rest: ` the "thing"`},
			{HasGroup: true, Group: "", Chips: []tipChipSt{{Text: "Z"}}, Verb: "Empty header row"},
		}, Paras: []string{"tail"}},
		"kbTabSplit": tipState("kbtab", "Tab-split verb", "body", []kbRow{
			{"", []string{"T"}, "help.cue-edit.k.addDrop"},
		}, nil),
		"adHoc": tipState("adhoc", "Ad-hoc tip", "one paragraph only", nil, nil),
	}
	names := make([]string, 0, len(fixtures))
	for n := range fixtures {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		st := fixtures[n]
		t.Run(n, func(t *testing.T) {
			zigFrag(t, n, renderTipSt(st), stateJSON(st), zigui.RenderTip)
		})
	}
}

// TestTipStateSplitIsLossless: tipState + renderTipSt must reproduce renderTip for the ad-hoc
// (non-registry) entry point too, and an unknown id must stay the empty-string / nil pair.
func TestTipStateSplitIsLossless(t *testing.T) {
	t.Cleanup(func() { i18n.SetLocale("en") })
	i18n.SetLocale("en")
	if got, want := tipOr(tipTopicSt("icecast"), "RAW"), tipTopic("icecast"); got != want {
		t.Fatalf("tipOr(structured) diverged from tipTopic:\n got %s\nwant %s", got, want)
	}
	if got := tipOr(nil, "RAW"); got != "RAW" {
		t.Fatalf("tipOr(nil) must pass the raw bridge value through, got %q", got)
	}
	if tipTopicSt("no-such-topic") != nil || tipTopic("no-such-topic") != "" {
		t.Fatal("an unknown topic id must resolve to nil / \"\"")
	}
	// the ad-hoc tip() helper shares renderTip, so its split must hold as well
	ad := tip("adhoc", "Title", "Body one\n\nBody two", ttLink{"L", "https://x/"})
	if want := renderTipSt(tipState("adhoc", "Title", "Body one\n\nBody two", nil, []ttLink{{"L", "https://x/"}})); ad != want {
		t.Fatalf("tip() diverged from the structured path:\n got %s\nwant %s", ad, want)
	}
}

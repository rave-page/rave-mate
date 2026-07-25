package main

import "hash/fnv"

// ONE schema, both sides. Every message here yields a Go encoder method
// (internal/webui/wire_gen.go) and a Zig decoder fn (native/zigui/src/wire_gen.zig), so the
// two representations cannot drift silently - a hand-mirrored decoder for 100+ state structs
// is memory corruption waiting to happen. Field numbers are the wire contract: append only,
// never renumber, never reuse. A changed schema changes schemaHash, and a document whose hash
// doesn't match the linked lib is rejected (→ v1 JSON path).

type kind int

const (
	kStr kind = iota
	kBool
	kUint
	kStruct
	kList
)

func (k kind) String() string {
	switch k {
	case kStr:
		return "str"
	case kBool:
		return "bool"
	case kUint:
		return "uint"
	case kStruct:
		return "struct"
	case kList:
		return "list"
	}
	return "?"
}

// field is one schema field. num is the wire field number (1..); goF/zigF are the field
// names on each side (they differ - Go exports, Zig lowerCamel).
type field struct {
	num  int
	goF  string
	zigF string
	kind kind
	ref  string // message name, for kStruct/kList
}

// msg is one state struct. id > 0 marks a ROOT message (gets a Go encode entry point + a
// msg-id const both sides; the id travels in the header so an export refuses a document
// meant for another message).
type msg struct {
	name string
	goT  string
	zigT string
	id   int
	doc  string
	fs   []field
}

func s(n int, g, z string) field { return field{num: n, goF: g, zigF: z, kind: kStr} }
func b(n int, g, z string) field { return field{num: n, goF: g, zigF: z, kind: kBool} }
func st(n int, g, z, r string) field {
	return field{num: n, goF: g, zigF: z, kind: kStruct, ref: r}
}
func li(n int, g, z, r string) field {
	return field{num: n, goF: g, zigF: z, kind: kList, ref: r}
}

// schema: wave B-1 pilots (appgroups + logs) and everything they nest.
var schema = []msg{
	{
		name: "AgApp", goT: "agApp", zigT: "appgroups.App",
		fs: []field{s(1, "Base", "base"), b(2, "Elevated", "elevated")},
	},
	{
		name: "AgGroup", goT: "agGroup", zigT: "appgroups.Group",
		fs: []field{s(1, "ID", "id"), s(2, "Name", "name"), s(3, "Up", "up"), s(4, "Variant", "variant"),
			li(5, "Apps", "apps", "AgApp")},
	},
	{
		name: "AgState", goT: "agState", zigT: "appgroups.State", id: 1,
		doc: "App Groups tab (full view + the #appgroups-body fragment share this state)",
		fs: []field{s(1, "Title", "title"), s(2, "Subtitle", "subtitle"), b(3, "Available", "available"),
			s(4, "Unavailable", "unavailable"), s(5, "Empty", "empty"), s(6, "Admin", "admin"),
			s(7, "Launch", "launch"), li(8, "Groups", "groups", "AgGroup")},
	},
	{
		name: "SelRow", goT: "selRow", zigT: "c.SelectRow",
		fs: []field{s(1, "Val", "val"), s(2, "Label", "label"), s(3, "Sub", "sub"), s(4, "Badge", "badge"),
			b(5, "Cur", "cur")},
	},
	{
		name: "SelState", goT: "selState", zigT: "c.Select",
		fs: []field{s(1, "ID", "id"), s(2, "Label", "label"), s(3, "CurLabel", "curLabel"),
			b(4, "Open", "open"), s(5, "Filter", "filter"), li(6, "Rows", "rows", "SelRow")},
	},
	{
		name: "LogsTab", goT: "logsTab", zigT: "c.Tab",
		fs: []field{s(1, "Val", "val"), s(2, "Label", "label")},
	},
	{
		name: "LogsEntry", goT: "logsEntry", zigT: "logs.Entry",
		fs: []field{s(1, "Time", "time"), s(2, "Lvl", "lvl"), s(3, "Cls", "cls"), s(4, "Src", "src"),
			s(5, "Msg", "msg"), s(6, "Fields", "fields")},
	},
	{
		name: "LogsLines", goT: "logsLines", zigT: "logs.Lines", id: 3,
		doc: "#log-view inner fragment (filter change + ~1 Hz tick)",
		fs: []field{b(1, "Wired", "wired"), s(2, "NoBus", "noBus"), s(3, "NoEntries", "noEntries"),
			li(4, "Entries", "entries", "LogsEntry")},
	},
	{
		name: "LogsState", goT: "logsState", zigT: "logs.State", id: 2,
		doc: "Logs tab (full view)",
		fs: []field{s(1, "Title", "title"), s(2, "Sub", "sub"), b(3, "ShowBus", "showBus"),
			s(4, "BusActive", "busActive"), li(5, "BusItems", "busItems", "LogsTab"),
			st(6, "Level", "level", "SelState"), st(7, "Source", "source", "SelState"),
			s(8, "SearchLabel", "searchLabel"), s(9, "SearchPH", "searchPh"), s(10, "SearchVal", "searchVal"),
			s(11, "AutoLabel", "autoLabel"), s(12, "AutoDL", "autoDl"), b(13, "AutoOn", "autoOn"),
			s(14, "Copy", "copy"), s(15, "Clear", "clear"), s(16, "Tailing", "tailing"),
			st(17, "Lines", "lines", "LogsLines")},
	},

	// ── phase B3 fragment scheduler (topic sched): the tick surfaces ──
	// Root ids 100-149. Tk* messages mirror the Live cockpit's fragment states; their Zig types
	// are re-exported by tick.zig, so this block needs no second import alias for live.zig.
	{
		name: "TkPrev", goT: "tickPrev", zigT: "tick.Prev",
		// kUint's first user: a dedup hash is not a rendered number, so rule 6 (Go formats every
		// number) does not apply - a 16-char hex string per fragment per tick would be pure waste.
		fs: []field{s(1, "ID", "id"), {num: 2, goF: "Hash", zigF: "hash", kind: kUint}},
	},
	{
		name: "TkKV", goT: "liveKV", zigT: "tick.KV",
		fs: []field{s(1, "K", "k"), s(2, "KL", "kl"), s(3, "V", "v")},
	},
	{
		name: "TkSRow", goT: "liveSRow", zigT: "tick.SRow",
		fs: []field{s(1, "Variant", "variant"), s(2, "Label", "label"), s(3, "DL", "dl"), s(4, "Line", "line")},
	},
	{
		name: "TkTransport", goT: "liveTransportSt", zigT: "tick.Transport",
		fs: []field{s(1, "StreamHint", "streamHint"), s(2, "StreamLabel", "streamLabel"),
			s(3, "DotVar", "dotVar"), s(4, "State", "state"), s(5, "MetaOnly", "metaOnly"),
			s(6, "PauseLabel", "pauseLabel"), s(7, "PauseHint", "pauseHint"), b(8, "Paused", "paused"),
			b(9, "HasRec", "hasRec"), s(10, "RecHint", "recHint"), s(11, "RecLabel", "recLabel"),
			s(12, "RecBtn", "recBtn"), s(13, "RecState", "recState"), b(14, "HasTC", "hasTc"),
			s(15, "TCLabel", "tcLabel"), s(16, "TC", "tc"), s(17, "StartLbl", "startLbl"),
			s(18, "StopLbl", "stopLbl")},
	},
	{
		name: "TkNP", goT: "liveNPSt", zigT: "tick.NP",
		fs: []field{s(1, "Line1", "line1"), s(2, "Line2", "line2")},
	},
	{
		name: "TkStatus", goT: "liveStatusSt", zigT: "tick.Status",
		fs: []field{li(1, "Rows", "rows", "TkKV")},
	},
	{
		name: "TkDeck", goT: "liveDeck", zigT: "tick.Deck",
		fs: []field{s(1, "Cls", "cls"), s(2, "Name", "name"), s(3, "Title", "title"),
			s(4, "Meta", "meta"), s(5, "Via", "via")},
	},
	{
		name: "TkDecks", goT: "liveDecksSt", zigT: "tick.Decks",
		fs: []field{s(1, "Note", "note"), li(2, "Decks", "decks", "TkDeck")},
	},
	{
		name: "TkSignals", goT: "liveSignalsSt", zigT: "tick.Signals",
		fs: []field{li(1, "Rows", "rows", "TkKV")},
	},
	{
		name: "TkCockpitRow", goT: "liveCockpitRow", zigT: "tick.CockpitRow",
		fs: []field{s(1, "Variant", "variant"), s(2, "Name", "name"), s(3, "State", "state"),
			s(4, "StreamLbl", "streamLbl"), s(5, "StreamAct", "streamAct"), s(6, "RecLbl", "recLbl"),
			s(7, "RecAct", "recAct")},
	},
	{
		name: "TkCockpit", goT: "liveCockpitSt", zigT: "tick.Cockpit",
		fs: []field{s(1, "Empty", "empty"), s(2, "Caption", "caption"),
			li(3, "Rows", "rows", "TkCockpitRow")},
	},
	{
		name: "TkLink", goT: "liveLinkSt", zigT: "tick.Link",
		fs: []field{b(1, "Available", "available"), st(2, "Backend", "backend", "TkSRow"),
			s(3, "Fill", "fill"), s(4, "Cap", "cap"), st(5, "Session", "session", "TkSRow"),
			s(6, "ResyncLbl", "resyncLbl"), li(7, "Sources", "sources", "TkSRow")},
	},
	{
		name: "TkGraph", goT: "liveGraphSt", zigT: "tick.Graph",
		fs: []field{s(1, "Tooltip", "tooltip"), s(2, "Legend", "legend"), s(3, "Graph", "graph")},
	},
	{
		name: "TkPerf", goT: "livePerfSt", zigT: "tick.Perf",
		fs: []field{s(1, "Tooltip", "tooltip"), s(2, "CPULeg", "cpuLeg"), s(3, "CPUGraph", "cpuGraph"),
			s(4, "RAMLeg", "ramLeg"), s(5, "RAMGraph", "ramGraph"), s(6, "Head", "head"),
			s(7, "HeadColor", "headColor")},
	},
	{
		name: "TkStrip", goT: "liveStripSt", zigT: "tick.Strip",
		fs: []field{s(1, "Left", "left"), s(2, "Center", "center"), s(3, "Right", "right")},
	},
	{
		// Full liveState mirror (field numbers follow the Go struct order): the tick leaves the
		// static chrome empty, and absent tags cost nothing on the wire.
		name: "TkLiveState", goT: "liveState", zigT: "tick.LiveState",
		fs: []field{s(1, "Title", "title"), s(2, "Sub", "sub"),
			st(3, "Transport", "transport", "TkTransport"), st(4, "NP", "np", "TkNP"),
			s(5, "StatusTitle", "statusTitle"), st(6, "Status", "status", "TkStatus"),
			s(7, "DecksTitle", "decksTitle"), st(8, "Decks", "decks", "TkDecks"),
			b(9, "HasSignals", "hasSignals"), s(10, "SignalsTitle", "signalsTitle"),
			s(11, "SignalsTip", "signalsTip"), st(12, "Signals", "signals", "TkSignals"),
			b(13, "HasCockpit", "hasCockpit"), s(14, "CockpitTitle", "cockpitTitle"),
			st(15, "Cockpit", "cockpit", "TkCockpit"), b(16, "HasLink", "hasLink"),
			s(17, "LinkTitle", "linkTitle"), st(18, "Link", "link", "TkLink"),
			b(19, "HasNet", "hasNet"), s(20, "NetTitle", "netTitle"), s(21, "NetTip", "netTip"),
			st(22, "Net", "net", "TkGraph"), s(23, "TimTitle", "timTitle"), s(24, "TimTip", "timTip"),
			st(25, "Tim", "tim", "TkGraph"), b(26, "HasPerf", "hasPerf"),
			s(27, "PerfTitle", "perfTitle"), s(28, "PerfTip", "perfTip"),
			st(29, "Perf", "perf", "TkPerf"), st(30, "Strip", "strip", "TkStrip")},
	},
	{
		name: "TkLive", goT: "liveTickSt", zigT: "tick.LiveBatch", id: 100,
		doc: "Live-tab tick surface (all ~1 Hz fragments in one call)",
		fs: []field{st(1, "Live", "live", "TkLiveState"), s(2, "TC", "tc"),
			li(3, "Prev", "prev", "TkPrev")},
	},
	{
		name: "TkLogs", goT: "logsTickSt", zigT: "tick.LogsBatch", id: 101,
		doc: "#log-view tick surface (one fragment, 400-line tail)",
		fs:  []field{st(1, "Lines", "lines", "LogsLines"), li(2, "Prev", "prev", "TkPrev")},
	},
}

// zigImports maps the import alias used in wire_gen.zig to its source file.
var zigImports = [][2]string{
	{"appgroups", "appgroups.zig"},
	{"logs", "logs.zig"},
	{"c", "components.zig"},
	{"tick", "tick.zig"},
}

// schemaHash is FNV-1a over the canonical schema text. Both sides embed it; a mismatch means
// the .a and the Go tree were generated from different schemas → the document is rejected.
func schemaHash() uint32 {
	h := fnv.New32a()
	for _, m := range schema {
		fmtWrite(h, m.name, m.goT, m.zigT, itoa(m.id))
		for _, f := range m.fs {
			fmtWrite(h, itoa(f.num), f.kind.String(), f.ref, f.goF, f.zigF)
		}
	}
	return h.Sum32()
}

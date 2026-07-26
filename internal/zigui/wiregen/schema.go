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
	// wave B-2 additions (append only - the iota values are part of nothing, but the
	// String() names feed schemaHash, so renaming one rewrites every hash).
	kStrAlways // string, tag emitted even when empty (Zig field has a non-zero default)
	kOptPtr    // Go *T ↔ Zig ?T: tag present iff non-nil (presence is meaningful)
	kOptVal    // Go T ↔ Zig ?T: tag ALWAYS present (JSON always sends the object)
	kStrList   // Go []string ↔ Zig []const []const u8
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
	case kStrAlways:
		return "stra"
	case kOptPtr:
		return "optp"
	case kOptVal:
		return "optv"
	case kStrList:
		return "strlist"
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
	// retain opts this ROOT into the retained-doc delta channel (B7 increment ii): wiregen also
	// emits merge/clone/hash walkers for it and everything it nests (retain.go). Stateless stays
	// the default and the fallback - flag a surface only when its bench row shows a win.
	retain bool
}

func s(n int, g, z string) field { return field{num: n, goF: g, zigF: z, kind: kStr} }
func b(n int, g, z string) field { return field{num: n, goF: g, zigF: z, kind: kBool} }
func st(n int, g, z, r string) field {
	return field{num: n, goF: g, zigF: z, kind: kStruct, ref: r}
}
func li(n int, g, z, r string) field {
	return field{num: n, goF: g, zigF: z, kind: kList, ref: r}
}
func u(n int, g, z string) field  { return field{num: n, goF: g, zigF: z, kind: kUint} }
func sa(n int, g, z string) field { return field{num: n, goF: g, zigF: z, kind: kStrAlways} }
func sl(n int, g, z string) field { return field{num: n, goF: g, zigF: z, kind: kStrList} }
func op(n int, g, z, r string) field {
	return field{num: n, goF: g, zigF: z, kind: kOptPtr, ref: r}
}
func ov(n int, g, z, r string) field {
	return field{num: n, goF: g, zigF: z, kind: kOptVal, ref: r}
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
	// --- phaseb-wire (B-2 fan-out) ---
	// One block per tab, in fan-out order. Rows are DERIVED from the Go state structs + their
	// Zig counterparts (json tag == Zig field name), so a field added on one side without the
	// other trips the three-way golden gate instead of silently dropping from v2.
	// live: full cockpit + the ten ~1 Hz tick fragments. Each fragment state crosses alone, so
	// each is its own root message and the header id still refuses a foreign document.
	{
		name: "LiveTransport", goT: "liveTransportSt", zigT: "live.Transport", id: 11,
		doc: "#live-transport fragment",
		fs:  []field{s(1, "StreamHint", "streamHint"), s(2, "StreamLabel", "streamLabel"), s(3, "DotVar", "dotVar"), s(4, "State", "state"), s(5, "MetaOnly", "metaOnly"), s(6, "PauseLabel", "pauseLabel"), s(7, "PauseHint", "pauseHint"), b(8, "Paused", "paused"), b(9, "HasRec", "hasRec"), s(10, "RecHint", "recHint"), s(11, "RecLabel", "recLabel"), s(12, "RecBtn", "recBtn"), s(13, "RecState", "recState"), b(14, "HasTC", "hasTc"), s(15, "TCLabel", "tcLabel"), s(16, "TC", "tc"), s(17, "StartLbl", "startLbl"), s(18, "StopLbl", "stopLbl")},
	},
	{
		name: "LiveNP", goT: "liveNPSt", zigT: "live.NP", id: 12,
		doc: "#live-np fragment",
		fs:  []field{s(1, "Line1", "line1"), s(2, "Line2", "line2")},
	},
	{
		name: "LiveKV", goT: "liveKV", zigT: "live.KV",
		fs: []field{s(1, "K", "k"), s(2, "KL", "kl"), s(3, "V", "v")},
	},
	{
		name: "LiveStatus", goT: "liveStatusSt", zigT: "live.Status", id: 13,
		doc: "#live-status fragment",
		fs:  []field{li(1, "Rows", "rows", "LiveKV")},
	},
	{
		name: "LiveDeck", goT: "liveDeck", zigT: "live.Deck",
		fs: []field{s(1, "Cls", "cls"), s(2, "Name", "name"), s(3, "Title", "title"), s(4, "Meta", "meta"), s(5, "Via", "via")},
	},
	{
		name: "LiveDecks", goT: "liveDecksSt", zigT: "live.Decks", id: 14,
		doc: "#live-decks fragment",
		fs:  []field{s(1, "Note", "note"), li(2, "Decks", "decks", "LiveDeck")},
	},
	{
		name: "LiveSignals", goT: "liveSignalsSt", zigT: "live.Signals", id: 15,
		doc: "#live-signals fragment",
		fs:  []field{li(1, "Rows", "rows", "LiveKV")},
	},
	{
		name: "LiveCockpitRow", goT: "liveCockpitRow", zigT: "live.CockpitRow",
		fs: []field{s(1, "Variant", "variant"), s(2, "Name", "name"), s(3, "State", "state"), s(4, "StreamLbl", "streamLbl"), s(5, "StreamAct", "streamAct"), s(6, "RecLbl", "recLbl"), s(7, "RecAct", "recAct")},
	},
	{
		name: "LiveCockpit", goT: "liveCockpitSt", zigT: "live.Cockpit", id: 16,
		doc: "#live-cockpit fragment",
		fs:  []field{s(1, "Empty", "empty"), s(2, "Caption", "caption"), li(3, "Rows", "rows", "LiveCockpitRow")},
	},
	{
		name: "LiveSRow", goT: "liveSRow", zigT: "live.SRow",
		fs: []field{s(1, "Variant", "variant"), s(2, "Label", "label"), s(3, "DL", "dl"), s(4, "Line", "line")},
	},
	{
		name: "LiveLink", goT: "liveLinkSt", zigT: "live.Link", id: 17,
		doc: "#live-ablelink fragment",
		fs:  []field{b(1, "Available", "available"), st(2, "Backend", "backend", "LiveSRow"), sa(3, "Fill", "fill"), s(4, "Cap", "cap"), st(5, "Session", "session", "LiveSRow"), s(6, "ResyncLbl", "resyncLbl"), li(7, "Sources", "sources", "LiveSRow")},
	},
	{
		name: "LiveGraph", goT: "liveGraphSt", zigT: "live.Graph", id: 18,
		doc: "#live-net + #live-tim fragments",
		fs:  []field{s(1, "Tooltip", "tooltip"), s(2, "Legend", "legend"), s(3, "Graph", "graph")},
	},
	{
		name: "LivePerf", goT: "livePerfSt", zigT: "live.Perf", id: 19,
		doc: "#live-perf2 fragment",
		fs:  []field{s(1, "Tooltip", "tooltip"), s(2, "CPULeg", "cpuLeg"), s(3, "CPUGraph", "cpuGraph"), s(4, "RAMLeg", "ramLeg"), s(5, "RAMGraph", "ramGraph"), s(6, "Head", "head"), s(7, "HeadColor", "headColor")},
	},
	{
		name: "LiveStrip", goT: "liveStripSt", zigT: "live.Strip", id: 20,
		doc: "#live-strip fragment",
		fs:  []field{s(1, "Left", "left"), s(2, "Center", "center"), s(3, "Right", "right")},
	},
	{
		name: "LiveState", goT: "liveState", zigT: "live.State", id: 10,
		doc: "Live tab - full cockpit",
		fs:  []field{s(1, "Title", "title"), s(2, "Sub", "sub"), st(3, "Transport", "transport", "LiveTransport"), st(4, "NP", "np", "LiveNP"), s(5, "StatusTitle", "statusTitle"), st(6, "Status", "status", "LiveStatus"), s(7, "DecksTitle", "decksTitle"), st(8, "Decks", "decks", "LiveDecks"), b(9, "HasSignals", "hasSignals"), s(10, "SignalsTitle", "signalsTitle"), s(11, "SignalsTip", "signalsTip"), st(12, "Signals", "signals", "LiveSignals"), b(13, "HasCockpit", "hasCockpit"), s(14, "CockpitTitle", "cockpitTitle"), st(15, "Cockpit", "cockpit", "LiveCockpit"), b(16, "HasLink", "hasLink"), s(17, "LinkTitle", "linkTitle"), st(18, "Link", "link", "LiveLink"), b(19, "HasNet", "hasNet"), s(20, "NetTitle", "netTitle"), s(21, "NetTip", "netTip"), st(22, "Net", "net", "LiveGraph"), s(23, "TimTitle", "timTitle"), s(24, "TimTip", "timTip"), st(25, "Tim", "tim", "LiveGraph"), b(26, "HasPerf", "hasPerf"), s(27, "PerfTitle", "perfTitle"), s(28, "PerfTip", "perfTip"), st(29, "Perf", "perf", "LivePerf"), st(30, "Strip", "strip", "LiveStrip"), op(31, "SignalsTipS", "signalsTipSt", "Tip"), op(32, "NetTipS", "netTipSt", "Tip"), op(33, "TimTipS", "timTipSt", "Tip"), op(34, "PerfTipS", "perfTipSt", "Tip")},
	},
	// motion: one root - the full view and the #mo-body fragment share moState. Cam/Studio are Go pointers (exactly one section is built per render), so they are optp: presence IS the section switch.
	{
		name: "MoCamRow", goT: "moCamRow", zigT: "motion.CamRow",
		fs: []field{s(1, "Group", "group"), b(2, "ShowGroup", "showGroup"), s(3, "Act", "act"), b(4, "Sel", "sel"), s(5, "Name", "name"), s(6, "Meta", "meta")},
	},
	{
		name: "MoCam", goT: "moCamSt", zigT: "motion.Cam",
		fs: []field{s(1, "Unavailable", "unavailable"), li(2, "Rows", "rows", "MoCamRow"), s(3, "Empty", "empty"), s(4, "ReloadLbl", "reloadLbl"), s(5, "OrganizeLbl", "organizeLbl"), s(6, "DJLbl", "djLbl"), s(7, "PreviewLbl", "previewLbl"), s(8, "Tip", "tip"), s(9, "View", "view"), s(10, "Hint", "hint"), s(11, "Info", "info"), s(12, "PlayBtn", "playBtn"), s(13, "LoadLbl", "loadLbl"), s(14, "CopyLbl", "copyLbl"), op(15, "TipS", "tipSt", "Tip")},
	},
	{
		name: "MoRecRow", goT: "moRecRow", zigT: "motion.RecRow",
		fs: []field{s(1, "Name", "name"), s(2, "Act", "act"), b(3, "Sel", "sel")},
	},
	{
		name: "MoAvatar", goT: "moAvatarSt", zigT: "motion.Avatar",
		fs: []field{s(1, "Label", "label"), st(2, "Sel", "sel", "SelState"), s(3, "ImportLbl", "importLbl"), s(4, "SyncLbl", "syncLbl"), s(5, "Info", "info")},
	},
	{
		name: "MoSlider", goT: "moSliderSt", zigT: "c.Slider",
		fs: []field{s(1, "Label", "label"), s(2, "DL", "dl"), s(3, "Act", "act"), s(4, "Unit", "unit"), sa(5, "UnitJS", "unitJs"), sa(6, "MinS", "minS"), sa(7, "MaxS", "maxS"), sa(8, "StepS", "stepS"), sa(9, "ValS", "valS")},
	},
	{
		name: "MoToggle", goT: "moToggleSt", zigT: "motion.Toggle",
		fs: []field{s(1, "Label", "label"), s(2, "DL", "dl"), s(3, "Act", "act"), b(4, "On", "on")},
	},
	{
		name: "MoStudio", goT: "moStudioSt", zigT: "motion.Studio",
		fs: []field{li(1, "Recs", "recs", "MoRecRow"), s(2, "Empty", "empty"), s(3, "RefreshLbl", "refreshLbl"), s(4, "ExportLbl", "exportLbl"), s(5, "RenderLbl", "renderLbl"), s(6, "PCViewLbl", "pcViewLbl"), s(7, "RenderProg", "renderProg"), st(8, "Avatar", "avatar", "MoAvatar"), s(9, "PreviewLbl", "previewLbl"), s(10, "Tip", "tip"), s(11, "View", "view"), s(12, "Hint", "hint"), s(13, "Time", "time"), st(14, "Scrub", "scrub", "MoSlider"), s(15, "PlayLbl", "playLbl"), s(16, "StopLbl", "stopLbl"), st(17, "Loop", "loop", "MoToggle"), st(18, "OSC", "osc", "MoToggle"), st(19, "VMC", "vmc", "MoToggle"), st(20, "Model", "model", "MoToggle"), b(21, "ModelOn", "modelOn"), b(22, "HasDyn", "hasDyn"), s(23, "PhysNote", "physNote"), st(24, "Phys", "phys", "MoToggle"), st(25, "Rest", "rest", "MoToggle"), st(26, "Marks", "marks", "MoToggle"), st(27, "PC", "pc", "MoToggle"), b(28, "PCOn", "pcOn"), st(29, "PCDensity", "pcDensity", "SelState"), st(30, "PCColor", "pcColor", "MoToggle"), s(31, "PCNote", "pcNote"), s(32, "PCExportLbl", "pcExportLbl"), s(33, "VMCHelp", "vmcHelp"), op(34, "TipS", "tipSt", "Tip")},
	},
	{
		name: "MoState", goT: "moState", zigT: "motion.State", id: 21,
		doc: "Motion tab (full view + the #mo-body fragment share this state)",
		fs:  []field{s(1, "Title", "title"), s(2, "Sub", "sub"), s(3, "Section", "section"), s(4, "TabCam", "tabCam"), s(5, "TabStudio", "tabStudio"), op(6, "Cam", "cam", "MoCam"), op(7, "Studio", "studio", "MoStudio")},
	},
	// publish: full tab + the #pub-hero tick fragment. PubTrack.Num is the FIRST kUint field on the wire (Zig i64): it is a 1-based row index (render_publish.go builds it as i+1), never negative - a signed value would encode as a huge uvarint. Everything else stays pre-formatted per rule 6.
	{
		name: "PubBadge", goT: "pubBadgeSt", zigT: "publish.Badge",
		fs: []field{s(1, "Key", "key"), s(2, "DL", "dl"), s(3, "Variant", "variant"), s(4, "Line", "line")},
	},
	{
		name: "PubBar", goT: "pubBarSt", zigT: "publish.Bar",
		fs: []field{b(1, "Show", "show"), s(2, "Pct", "pct"), s(3, "Cap", "cap")},
	},
	{
		name: "PubNp", goT: "pubNpSt", zigT: "publish.Np",
		fs: []field{s(1, "Label", "label"), s(2, "Title", "title"), s(3, "Meta", "meta"), s(4, "State", "state"), st(5, "Bar", "bar", "PubBar")},
	},
	{
		name: "PubPlayer", goT: "pubPlayerSt", zigT: "publish.Player",
		fs: []field{b(1, "Show", "show"), s(2, "Label", "label"), s(3, "Pos", "pos"), st(4, "Bar", "bar", "PubBar")},
	},
	{
		name: "PubHero", goT: "pubHeroSt", zigT: "publish.Hero", id: 23,
		doc: "#pub-hero fragment (~1 Hz tick)",
		fs:  []field{b(1, "Show", "show"), st(2, "Rec", "rec", "PubBadge"), st(3, "Cap", "cap", "PubBadge"), st(4, "Obs", "obs", "PubBadge"), s(5, "Finish", "finish"), st(6, "NP", "np", "PubNp"), st(7, "Player", "player", "PubPlayer")},
	},
	{
		name: "PubSetRow", goT: "pubSetRowSt", zigT: "publish.SetRow",
		fs: []field{s(1, "ID", "id"), s(2, "Title", "title"), s(3, "Sub", "sub"), b(4, "Sel", "sel"), s(5, "Rename", "rename")},
	},
	{
		name: "PubList", goT: "pubListSt", zigT: "publish.List",
		fs: []field{s(1, "Empty", "empty"), s(2, "Count", "count"), li(3, "Rows", "rows", "PubSetRow")},
	},
	{
		name: "UiBtn", goT: "uiBtn", zigT: "c.Btn",
		fs: []field{s(1, "Label", "label"), s(2, "Variant", "variant"), s(3, "Act", "act"), s(4, "Val", "val")},
	},
	{
		name: "PubCap", goT: "pubCapSt", zigT: "publish.Cap",
		fs: []field{s(1, "Caption", "caption"), li(2, "Btns", "btns", "UiBtn"), st(3, "Menu", "menu", "SelState")},
	},
	{
		name: "PubLoose", goT: "pubLooseSt", zigT: "publish.Loose",
		fs: []field{s(1, "Count", "count"), s(2, "Desc", "desc"), li(3, "Caps", "caps", "PubCap")},
	},
	{
		name: "PubCaptures", goT: "pubCapturesSt", zigT: "publish.Captures",
		fs: []field{s(1, "Player", "player"), s(2, "Empty", "empty"), li(3, "Caps", "caps", "PubCap")},
	},
	{
		name: "PubTrack", goT: "pubTrackSt", zigT: "publish.Track",
		fs: []field{u(1, "Num", "num"), s(2, "Label", "label"), s(3, "Off", "off"), s(4, "Lead", "lead"), s(5, "LeadTip", "leadTip"), b(6, "Checked", "checked"), s(7, "Path", "path"), s(8, "Ctx", "ctx"), s(9, "OffAct", "offAct"), s(10, "OffDL", "offDl")},
	},
	{
		name: "PubBatch", goT: "pubBatchSt", zigT: "publish.Batch",
		fs: []field{s(1, "Count", "count"), li(2, "Btns", "btns", "UiBtn")},
	},
	{
		name: "PubTracklist", goT: "pubTracklistSt", zigT: "publish.Tracklist",
		fs: []field{s(1, "Empty", "empty"), s(2, "Resolving", "resolving"), b(3, "Editable", "editable"), s(4, "OffTip", "offTip"), li(5, "Rows", "rows", "PubTrack"), b(6, "ShowFix", "showFix"), st(7, "Fix", "fix", "UiBtn"), s(8, "Help", "help"), s(9, "Unres", "unres"), st(10, "Batch", "batch", "PubBatch")},
	},
	{
		name: "PubDetail", goT: "pubDetailSt", zigT: "publish.Detail",
		fs: []field{s(1, "CardTitle", "cardTitle"), b(2, "Sel", "sel"), s(3, "Hint", "hint"), s(4, "Player", "player"), st(5, "Loose", "loose", "PubLoose"), s(6, "Name", "name"), s(7, "Meta", "meta"), li(8, "Actions", "actions", "UiBtn"), s(9, "Active", "active"), s(10, "CapsLbl", "capsLbl"), s(11, "TracksLbl", "tracksLbl"), st(12, "Captures", "captures", "PubCaptures"), st(13, "Tracklist", "tracklist", "PubTracklist")},
	},
	{
		name: "PubBody", goT: "pubBodySt", zigT: "publish.Body",
		fs: []field{st(1, "Hero", "hero", "PubHero"), st(2, "List", "list", "PubList"), st(3, "Detail", "detail", "PubDetail")},
	},
	{
		name: "Pub", goT: "pubSt", zigT: "publish.State", id: 22,
		doc: "Publish tab (full view)",
		fs:  []field{s(1, "Title", "title"), s(2, "Sub", "sub"), s(3, "Switcher", "switcher"), b(4, "Available", "available"), s(5, "Unavailable", "unavailable"), st(6, "Body", "body", "PubBody")},
	},
	// settings: full tab + the #set-content pane + one #stset-<id> status fragment. This block also pins the SHARED tooltip state (tipState/tipKb/tipLink, phase B-1b shard 1) - the first consumer to reach it owns the rows, later tabs reuse them. Sixteen kOptPtr fields: the settings card grid is almost entirely optional blocks.
	{
		name: "SetNav", goT: "setNavSt", zigT: "settings.Nav",
		fs: []field{s(1, "ID", "id"), s(2, "Title", "title"), s(3, "Agg", "agg"), b(4, "Active", "active")},
	},
	{
		name: "TipChip", goT: "tipChipSt", zigT: "c.TipChip",
		fs: []field{s(1, "Text", "text"), b(2, "Sep", "sep")},
	},
	{
		name: "TipKb", goT: "tipKbSt", zigT: "c.TipKb",
		fs: []field{b(1, "HasGroup", "hasGroup"), s(2, "Group", "group"), li(3, "Chips", "chips", "TipChip"), s(4, "Verb", "verb"), s(5, "Rest", "rest")},
	},
	{
		name: "TipLink", goT: "tipLinkSt", zigT: "c.TipLink",
		fs: []field{s(1, "Label", "label"), s(2, "URL", "url")},
	},
	{
		name: "Tip", goT: "tipSt", zigT: "c.Tip",
		fs: []field{s(1, "ID", "id"), s(2, "Title", "title"), li(3, "Keys", "keys", "TipKb"), sl(4, "Paras", "paras"), li(5, "Links", "links", "TipLink")},
	},
	{
		name: "SetStatus", goT: "setStatusSt", zigT: "settings.Status", id: 26,
		doc: "one #stset-<id> status fragment (settings tick)",
		fs:  []field{s(1, "V", "v"), s(2, "T", "t")},
	},
	{
		name: "SetSwitch", goT: "setSwitchSt", zigT: "settings.Switch",
		fs: []field{s(1, "Label", "label"), b(2, "On", "on"), s(3, "Gate", "gate")},
	},
	{
		name: "UiField", goT: "uiField", zigT: "c.Field",
		fs: []field{s(1, "Label", "label"), s(2, "DL", "dl"), s(3, "Act", "act"), s(4, "Value", "value"), s(5, "Type", "inputType"), s(6, "PH", "ph")},
	},
	{
		name: "UiToggle", goT: "uiToggle", zigT: "c.Toggle",
		fs: []field{s(1, "Label", "label"), s(2, "DL", "dl"), s(3, "Act", "act"), b(4, "On", "on")},
	},
	{
		name: "UiKV", goT: "uiKV", zigT: "c.KV",
		fs: []field{s(1, "Label", "label"), s(2, "DL", "dl"), s(3, "Value", "value")},
	},
	{
		name: "SetKid", goT: "setKid", zigT: "settings.Kid",
		fs: []field{s(1, "K", "k"), op(2, "Fld", "fld", "UiField"), s(3, "Tip", "tip"), op(4, "TipS", "tipSt", "Tip"), op(5, "Sel", "sel", "SelState"), s(6, "SelLbl", "selLbl"), op(7, "Btn", "btn", "UiBtn"), op(8, "SelLblS", "selLblSt", "SsLabel")},
	},
	{
		name: "SetInput", goT: "setInput", zigT: "settings.Input",
		fs: []field{s(1, "Type", "type"), s(2, "Name", "name"), s(3, "PH", "ph")},
	},
	{
		name: "GfBtn", goT: "gfBtn", zigT: "sub.GfBtn",
		fs: []field{s(1, "Label", "label"), s(2, "Variant", "variant"), s(3, "Act", "act"), s(4, "Gate", "gate")},
	},
	{
		name: "GfVar", goT: "gfVarSt", zigT: "sub.GfVar",
		fs: []field{s(1, "Key", "key"), s(2, "Tone", "tone"), s(3, "Line", "line"), li(4, "Btns", "btns", "GfBtn"), b(5, "HasNote", "hasNote"), s(6, "Note", "note")},
	},
	{
		name: "GfCard", goT: "gfCardSt", zigT: "sub.GfCard",
		fs: []field{s(1, "LeadKind", "leadKind"), s(2, "LeadTone", "leadTone"), s(3, "Lead", "lead"), li(4, "Vars", "vars", "GfVar"), st(5, "Recheck", "recheck", "UiBtn"), st(6, "Engine", "engine", "SelState"), st(7, "Python", "python", "UiField"), st(8, "Browse", "browse", "UiBtn"), st(9, "MinQ", "minq", "UiField"), st(10, "Thresh", "thresh", "UiField"), st(11, "Lock", "lock", "UiToggle"), b(12, "HasCal", "hasCal"), s(13, "Cal", "cal"), s(14, "CalNote", "calNote"), s(15, "Note", "note")},
	},
	{
		name: "GfModel", goT: "gfModelSt", zigT: "sub.GfModel",
		fs: []field{st(1, "Sel", "sel", "SelState"), s(2, "Dataset", "dataset"), b(3, "Running", "running"), s(4, "BarPct", "barPct"), s(5, "BarCap", "barCap"), st(6, "Cancel", "cancel", "UiBtn"), b(7, "HasVerdict", "hasVerdict"), s(8, "VerdictTone", "verdictTone"), s(9, "Verdict", "verdict"), s(10, "Err", "err"), b(11, "CanTrain", "canTrain"), st(12, "Train", "train", "UiBtn"), b(13, "Few", "few"), s(14, "FewHint", "fewHint"), s(15, "Note", "note")},
	},
	{
		name: "UiStatus", goT: "uiStatus", zigT: "c.Status", id: 48,
		doc: "one #ovl-st-<kind> status fragment (patched on every overlays action); nested everywhere else",
		fs:  []field{s(1, "Variant", "variant"), s(2, "Label", "label"), s(3, "DL", "dl"), s(4, "Line", "line")},
	},
	{
		name: "BridgeSess", goT: "bridgeSessSt", zigT: "sub.BridgeSess",
		fs: []field{s(1, "Title", "title"), s(2, "Sub", "sub"), st(3, "Revoke", "revoke", "UiBtn")},
	},
	{
		name: "BridgeGate", goT: "bridgeGateSt", zigT: "sub.BridgeGate",
		fs: []field{s(1, "Kind", "kind"), s(2, "Help", "help"), s(3, "Secret", "secret"), s(4, "URI", "uri"), s(5, "CodeLabel", "codeLabel"), s(6, "CodeDL", "codeDL"), s(7, "Confirm", "confirm"), st(8, "Cancel", "cancel", "UiBtn"), s(9, "Burn", "burn"), li(10, "Rows", "rows", "UiStatus"), s(11, "Note", "note"), st(12, "Btn", "btn", "UiBtn"), s(13, "SessionsTitle", "sessionsTitle"), s(14, "Empty", "empty"), li(15, "Sessions", "sessions", "BridgeSess"), st(16, "RevokeAll", "revokeAll", "UiBtn")},
	},
	{
		name: "Bridge", goT: "bridgeSt", zigT: "sub.Bridge",
		fs: []field{st(1, "St", "st", "UiStatus"), st(2, "Studio", "studio", "UiToggle"), s(3, "Tip", "tip"), b(4, "HasGate", "hasGate"), s(5, "GateTitle", "gateTitle"), st(6, "Gate", "gate", "BridgeGate"), op(7, "TipS", "tipSt", "Tip")},
	},
	{
		// i7 promoted UpdFlow to a root (id 113): #inst-update patches standalone. Field
		// numbers untouched, so embedded wire bytes are unchanged.
		name: "UpdFlow", goT: "updFlowSt", zigT: "sub.UpdFlow", id: 113,
		doc: "#inst-update region (self-update check/apply flow)",
		fs:  []field{s(1, "Kind", "kind"), s(2, "Tone", "tone"), s(3, "Text", "text"), b(4, "HasNotes", "hasNotes"), s(5, "Notes", "notes"), s(6, "Err", "err"), s(7, "Pct", "pct"), s(8, "Cap", "cap"), b(9, "HasBtn", "hasBtn"), st(10, "Btn", "btn", "UiBtn")},
	},
	{
		name: "SetBlock", goT: "setBlock", zigT: "settings.Block",
		fs: []field{s(1, "K", "k"), s(2, "Text", "text"), s(3, "HTML", "html"), s(4, "Tone", "tone"), s(5, "ID", "id"), s(6, "Title", "title"), s(7, "Sub", "sub"), op(8, "Fld", "fld", "UiField"), s(9, "Tip", "tip"), op(10, "TipS", "tipSt", "Tip"), op(11, "Tgl", "tgl", "UiToggle"), s(12, "Gate", "gate"), op(13, "KV", "kv", "UiKV"), op(14, "Sel", "sel", "SelState"), s(15, "SelLbl", "selLbl"), op(16, "Btn", "btn", "UiBtn"), li(17, "Kids", "kids", "SetKid"), li(18, "Inputs", "inputs", "SetInput"), s(19, "Submit", "submit"), s(20, "SubVar", "subVar"), op(21, "GF", "gf", "GfCard"), op(22, "GFM", "gfm", "GfModel"), op(23, "Brg", "brg", "Bridge"), op(24, "Upd", "upd", "UpdFlow"), op(25, "SelLblS", "selLblSt", "SsLabel")},
	},
	{
		name: "SetCard", goT: "setCardSt", zigT: "settings.Card",
		fs: []field{s(1, "ID", "id"), s(2, "Title", "title"), s(3, "Tip", "tip"), op(4, "TipS", "tipSt", "Tip"), s(5, "Desc", "desc"), st(6, "St", "st", "SetStatus"), op(7, "Tgl", "tgl", "SetSwitch"), li(8, "Blocks", "blocks", "SetBlock")},
	},
	{
		name: "SetSec", goT: "setSecSt", zigT: "settings.Sec",
		fs: []field{s(1, "ID", "id"), s(2, "Title", "title"), s(3, "Desc", "desc"), li(4, "Cards", "cards", "SetCard")},
	},
	{
		name: "SetContent", goT: "setContentSt", zigT: "settings.Content", id: 25,
		doc: "#set-content pane (sub-tab switch + search)",
		fs:  []field{b(1, "Searching", "searching"), s(2, "NoResults", "noResults"), li(3, "Nav", "nav", "SetNav"), li(4, "Secs", "secs", "SetSec")},
	},
	{
		name: "SetState", goT: "setState", zigT: "settings.State", id: 24,
		doc: "Settings tab (full view)",
		fs:  []field{s(1, "Title", "title"), s(2, "Sub", "sub"), b(3, "Available", "available"), s(4, "Unavailable", "unavailable"), s(5, "Query", "query"), s(6, "Placeholder", "placeholder"), st(7, "Content", "content", "SetContent")},
	},
	// library: the biggest state in the app (11 kB) - tab + #lib-body + #lib-detail + #lib-queue-body + one cue-census cell. LibCueCell.Drops/Cues are counts (kUint, zero = absent tag).
	{
		name: "LibTab", goT: "libTabSt", zigT: "c.Tab",
		fs: []field{s(1, "Val", "val"), s(2, "Label", "label")},
	},
	{
		name: "LibNavRow", goT: "libNavRowSt", zigT: "f.NavRow",
		fs: []field{b(1, "Hd", "hd"), s(2, "Label", "label"), s(3, "Act", "act"), s(4, "Icon", "icon"), s(5, "Count", "count"), b(6, "On", "on")},
	},
	{
		name: "LibNav", goT: "libNavSt", zigT: "f.Nav",
		fs: []field{li(1, "Rows", "rows", "LibNavRow")},
	},
	{
		name: "LibGFStat", goT: "libGFStatSt", zigT: "f.GFStat",
		fs: []field{s(1, "N", "n"), s(2, "Label", "label"), s(3, "Tone", "tone")},
	},
	{
		name: "LibGFTile", goT: "libGFTileSt", zigT: "f.GFTile",
		fs: []field{s(1, "N", "n"), s(2, "Label", "label"), s(3, "Tone", "tone")},
	},
	{
		name: "LibGFLive", goT: "libGFLiveSt", zigT: "f.GFLive", id: 96,
		doc: "#gf-live fixer progress fragment (~2 Hz)",
		fs:  []field{li(1, "Tiles", "tiles", "LibGFTile"), s(2, "Pct", "pct"), s(3, "Caption", "caption"), s(4, "Current", "current")},
	},
	{
		name: "LibHint", goT: "libHintSt", zigT: "k.Hint",
		fs: []field{s(1, "Tone", "tone"), s(2, "Text", "text")},
	},
	{
		name: "LibGF", goT: "libGFSt", zigT: "f.GF",
		fs: []field{s(1, "Kind", "kind"), s(2, "Eyebrow", "eyebrow"), s(3, "Title", "title"), li(4, "Stats", "stats", "LibGFStat"), s(5, "Note", "note"), b(6, "NoteAfter", "noteAfter"), li(7, "Btns", "btns", "UiBtn"), s(8, "ConfirmNote", "confirmNote"), st(9, "Force", "force", "UiToggle"), s(10, "ForceHint", "forceHint"), li(11, "Scopes", "scopes", "UiBtn"), st(12, "Live", "live", "LibGFLive"), s(13, "StopLbl", "stopLbl"), li(14, "Tiles", "tiles", "LibGFTile"), s(15, "CachedNote", "cachedNote"), li(16, "Hints", "hints", "LibHint"), li(17, "Acts", "acts", "UiBtn"), sl(18, "Notes", "notes"), s(19, "ApplyNote", "applyNote")},
	},
	{
		name: "LibSelTip", goT: "libSelTip", zigT: "k.SelTip",
		fs: []field{st(1, "Sel", "sel", "SelState"), s(2, "Label", "labelHtml"), op(3, "LabelS", "labelSt", "SsLabel")},
	},
	{
		name: "LibChip", goT: "libChipSt", zigT: "k.Chip",
		fs: []field{s(1, "Label", "label"), s(2, "Val", "val"), s(3, "Act", "act"), b(4, "Active", "active")},
	},
	{
		name: "LibPBField", goT: "libPBFieldSt", zigT: "c.PBField",
		fs: []field{s(1, "Label", "label"), s(2, "DL", "dl"), s(3, "Act", "act"), s(4, "Value", "value"), s(5, "Type", "inputType"), s(6, "PH", "ph"), s(7, "Hint", "hint")},
	},
	{
		name: "LibEncVideo", goT: "libEncVideoSt", zigT: "d.EncVideo",
		fs: []field{st(1, "VCodec", "vcodec", "LibSelTip"), st(2, "Accel", "accel", "SelState"), s(3, "QualityLbl", "qualityLbl"), li(4, "Profiles", "profiles", "LibChip"), s(5, "ProfileHint", "profileHint"), st(6, "RateMode", "rateMode", "LibSelTip"), st(7, "RateField", "rateField", "LibPBField"), st(8, "Res", "res", "SelState"), st(9, "FPS", "fps", "LibPBField")},
	},
	{
		name: "LoudChip", goT: "loudChipSt", zigT: "c.LoudChip",
		fs: []field{s(1, "Label", "label"), s(2, "Val", "val"), s(3, "Title", "title"), b(4, "Active", "active")},
	},
	{
		name: "Loud", goT: "loudSt", zigT: "c.Loud",
		fs: []field{b(1, "Compact", "compact"), st(2, "Toggle", "toggle", "UiToggle"), s(3, "Tip", "tip"), s(4, "ChipAct", "chipAct"), li(5, "Chips", "chips", "LoudChip"), st(6, "IField", "iField", "LibPBField"), st(7, "TPField", "tpField", "LibPBField"), st(8, "Raise", "raise", "UiToggle"), b(9, "HasWarn", "hasWarn"), s(10, "Warn", "warn"), s(11, "Extra", "extra"), op(12, "TipS", "tipSt", "Tip")},
	},
	{
		name: "LibEnc", goT: "libEncSt", zigT: "d.Enc",
		fs: []field{st(1, "Preset", "preset", "SelState"), s(2, "Desc", "desc"), li(3, "Hints", "hints", "LibHint"), b(4, "AudioOnly", "audioOnly"), st(5, "Container", "container", "LibSelTip"), st(6, "Video", "video", "LibEncVideo"), st(7, "AudioCodec", "audioCodec", "LibSelTip"), st(8, "AudioBitrate", "audioBitrate", "LibPBField"), st(9, "Channels", "channels", "SelState"), st(10, "SampleRate", "sampleRate", "SelState"), st(11, "Loud", "loud", "Loud"), st(12, "TrimStart", "trimStart", "LibPBField"), st(13, "TrimEnd", "trimEnd", "LibPBField"), s(14, "OutputNote", "outputNote"), s(15, "StartLbl", "startLbl"), s(16, "SaveLbl", "saveLbl"), s(17, "SaveAsLbl", "saveAsLbl")},
	},
	{
		name: "LibHarm", goT: "libHarmSt", zigT: "d.Harm",
		fs: []field{s(1, "Desc", "desc"), s(2, "Wheel", "wheel"), s(3, "SameLbl", "sameLbl"), s(4, "RelLbl", "relLbl"), s(5, "ShowLbl", "showLbl"), s(6, "ShowAct", "showAct"), s(7, "ClearLbl", "clearLbl")},
	},
	{
		name: "LibTagEd", goT: "libTagEdSt", zigT: "f.TagEdit",
		fs: []field{b(1, "Open", "open"), s(2, "OpenLbl", "openLbl"), s(3, "Desc", "desc"), li(4, "Fields", "fields", "LibPBField"), s(5, "SaveLbl", "saveLbl"), s(6, "CancelLbl", "cancelLbl")},
	},
	{
		name: "LibTrackPls", goT: "libTrackPlsSt", zigT: "d.TrackPls",
		fs: []field{b(1, "Unavailable", "unavailable"), li(2, "Chips", "chips", "LibChip"), s(3, "EmptyText", "emptyText"), s(4, "AddLbl", "addLbl"), s(5, "AddAct", "addAct")},
	},
	{
		name: "LibCompatRow", goT: "libCompatRowSt", zigT: "f.CompatRow",
		fs: []field{s(1, "Title", "title"), s(2, "Sub", "sub"), s(3, "Act", "act")},
	},
	{
		name: "LibCompatSec", goT: "libCompatSecSt", zigT: "f.Compat",
		fs: []field{b(1, "IsEmpty", "isEmpty"), s(2, "Empty", "empty"), li(3, "Rows", "rows", "LibCompatRow"), s(4, "OpenLbl", "openLbl"), s(5, "FindLbl", "findLbl"), s(6, "FindAct", "findAct")},
	},
	{
		name: "LibDetail", goT: "libDetailSt", zigT: "d.Detail", id: 29,
		doc: "#lib-detail inspector",
		fs:  []field{s(1, "Kind", "kind"), s(2, "Raw", "raw"), s(3, "Msg", "msg"), st(4, "GF", "gf", "LibGF"), s(5, "Eyebrow", "eyebrow"), s(6, "Title", "title"), s(7, "Sub", "sub"), s(8, "ActionsTitle", "actionsTitle"), s(9, "Missing", "missing"), li(10, "ActBtns", "actBtns", "UiBtn"), b(11, "HasPlayer", "hasPlayer"), s(12, "PlayerTitle", "playerTitle"), s(13, "Player", "player"), b(14, "HasEnc", "hasEnc"), s(15, "EncTitle", "encTitle"), b(16, "EncDemoted", "encDemoted"), s(17, "DemotedNote", "demotedNote"), s(18, "ShowLbl", "showLbl"), st(19, "Enc", "enc", "LibEnc"), b(20, "HasHarm", "hasHarm"), s(21, "HarmTitle", "harmTitle"), st(22, "Harm", "harm", "LibHarm"), b(23, "HasTags", "hasTags"), s(24, "TagsTitle", "tagsTitle"), s(25, "TagsDesc", "tagsDesc"), s(26, "WriteLbl", "writeLbl"), s(27, "WriteAct", "writeAct"), s(28, "RevertLbl", "revertLbl"), s(29, "RevertAct", "revertAct"), st(30, "TagEditor", "tagEditor", "LibTagEd"), b(31, "HasPls", "hasPls"), s(32, "PlsTitle", "plsTitle"), st(33, "Pls", "pls", "LibTrackPls"), b(34, "HasCompat", "hasCompat"), s(35, "CompatTitle", "compatTitle"), st(36, "Compat", "compat", "LibCompatSec"), s(37, "DetailsTitle", "detailsTitle"), li(38, "Meta", "meta", "UiKV")},
	},
	{
		name: "LibSeg", goT: "libSegSt", zigT: "s.Seg",
		fs: []field{s(1, "Label", "label"), s(2, "Path", "path")},
	},
	{
		name: "LibPlAct", goT: "libPlActSt", zigT: "k.PlAct",
		fs: []field{li(1, "Btns", "btns", "UiBtn"), st(2, "Menu", "menu", "SelState")},
	},
	{
		name: "LibKeyPill", goT: "libKeyPillSt", zigT: "k.KeyPill",
		fs: []field{s(1, "Text", "text"), s(2, "Cls", "cls"), b(3, "Ok", "ok")},
	},
	{
		name: "LibFe", goT: "libFeSt", zigT: "s.Fe",
		fs: []field{s(1, "Name", "name"), s(2, "Path", "path"), b(3, "IsDir", "isDir"), s(4, "Glyph", "glyph"), s(5, "GridSub", "gridSub"), s(6, "Sub", "sub"), st(7, "Key", "key", "LibKeyPill"), b(8, "Checked", "checked"), b(9, "Sel", "sel")},
	},
	{
		name: "LibBatch", goT: "libBatchSt", zigT: "k.Batch",
		fs: []field{b(1, "On", "on"), s(2, "Count", "count"), li(3, "Btns", "btns", "UiBtn")},
	},
	{
		name: "LibBrowse", goT: "libBrowseSt", zigT: "s.Browse",
		fs: []field{s(1, "Msg", "msg"), li(2, "Crumbs", "crumbs", "LibSeg"), s(3, "Up", "up"), s(4, "UpPath", "upPath"), s(5, "Goto", "gotoLbl"), s(6, "Filter", "filter"), s(7, "FilterPH", "filterPh"), s(8, "KindLbl", "kindLbl"), st(9, "Kind", "kind", "SelState"), s(10, "SortLbl", "sortLbl"), st(11, "Sort", "sort", "SelState"), s(12, "ListLbl", "listLbl"), s(13, "GridLbl", "gridLbl"), b(14, "Grid", "grid"), st(15, "KeyChip", "keyChip", "LibChip"), st(16, "Folder", "folder", "SelState"), b(17, "SelAll", "selAll"), b(18, "SelAllOn", "selAllOn"), s(19, "SelAllTitle", "selAllTitle"), s(20, "Count", "count"), s(21, "BoundNote", "boundNote"), b(22, "HasBound", "hasBound"), st(23, "BoundActs", "boundActs", "LibPlAct"), li(24, "Entries", "entries", "LibFe"), s(25, "More", "more"), st(26, "Batch", "batch", "LibBatch")},
	},
	{
		name: "LibGFResRow", goT: "libGFResRowSt", zigT: "f.GFResRow",
		fs: []field{s(1, "Path", "path"), s(2, "St", "st"), s(3, "StLow", "stLow"), s(4, "Title", "title"), s(5, "Detail", "detail"), s(6, "Delta", "delta")},
	},
	{
		name: "LibGFRes", goT: "libGFResSt", zigT: "f.GFRes",
		fs: []field{li(1, "Chips", "chips", "LibChip"), li(2, "Rows", "rows", "LibGFResRow"), b(3, "IsEmpty", "isEmpty"), s(4, "Empty", "empty")},
	},
	{
		name: "LibTFRow", goT: "libTFRowSt", zigT: "f.TFRow",
		fs: []field{s(1, "Idx", "idx"), b(2, "Checked", "checked"), s(3, "Path", "path"), s(4, "Base", "base"), s(5, "Field", "field"), s(6, "Cur", "cur"), s(7, "Proposed", "proposed")},
	},
	{
		name: "LibTFGrp", goT: "libTFGrpSt", zigT: "f.TFGrp",
		fs: []field{s(1, "Title", "title"), s(2, "Badge", "badge"), s(3, "AllLbl", "allLbl"), s(4, "AllAct", "allAct"), s(5, "NoneLbl", "noneLbl"), s(6, "NoneAct", "noneAct"), s(7, "Desc", "desc"), li(8, "Rows", "rows", "LibTFRow"), s(9, "More", "more")},
	},
	{
		name: "LibTFRes", goT: "libTFResSt", zigT: "f.TFRes",
		fs: []field{s(1, "Eyebrow", "eyebrow"), s(2, "Title", "title"), s(3, "Desc", "desc"), b(4, "Scanning", "scanning"), s(5, "Pct", "pct"), s(6, "ScanCap", "scanCap"), s(7, "CloseLbl", "closeLbl"), s(8, "ApplyLbl", "applyLbl"), s(9, "RescanLbl", "rescanLbl"), li(10, "Hints", "hints", "LibHint"), s(11, "Skipped", "skipped"), b(12, "IsEmpty", "isEmpty"), s(13, "Empty", "empty"), li(14, "Groups", "groups", "LibTFGrp")},
	},
	{
		name: "LibFixRes", goT: "libFixResSt", zigT: "f.Results",
		fs: []field{s(1, "Kind", "kind"), st(2, "GF", "gf", "LibGFRes"), st(3, "TF", "tf", "LibTFRes")},
	},
	{
		name: "LibCollHdr", goT: "libCollHdrSt", zigT: "s.CollHdr",
		fs: []field{s(1, "Cls", "cls"), s(2, "Key", "key"), s(3, "Label", "label"), s(4, "Arrow", "arrow")},
	},
	{
		name: "LibCollHead", goT: "libCollHeadSt", zigT: "s.CollHead",
		fs: []field{s(1, "SelAllTitle", "selAllTitle"), b(2, "SelAllOn", "selAllOn"), st(3, "Main", "main", "LibCollHdr"), s(4, "CueLbl", "cueLbl"), st(5, "BPM", "bpm", "LibCollHdr"), s(6, "TimeLbl", "timeLbl"), st(7, "Key", "key", "LibCollHdr")},
	},
	{
		name: "LibCueCell", goT: "libCueCellSt", zigT: "s.CueCell", id: 31,
		doc: "one cue-census cell (per-row patch)",
		fs:  []field{u(1, "Drops", "drops"), s(2, "DropsTitle", "dropsTitle"), s(3, "NoDropsTitle", "noDropsTitle"), u(4, "Cues", "cues"), s(5, "CuesTitle", "cuesTitle"), s(6, "NoCuesTitle", "noCuesTitle")},
	},
	{
		name: "LibCollRow", goT: "libCollRowSt", zigT: "s.CollRow",
		fs: []field{s(1, "Path", "path"), b(2, "Checked", "checked"), b(3, "Warn", "warn"), s(4, "SelCls", "selCls"), s(5, "Title", "title"), s(6, "Sub", "sub"), b(7, "Verified", "verified"), s(8, "CellID", "cellId"), st(9, "Cue", "cue", "LibCueCell"), s(10, "BPM", "bpm"), s(11, "Dur", "dur"), st(12, "Key", "key", "LibKeyPill")},
	},
	{
		name: "LibColl", goT: "libCollSt", zigT: "s.Coll",
		fs: []field{s(1, "Msg", "msg"), s(2, "ImportLbl", "importLbl"), s(3, "DJSyncLbl", "djsyncLbl"), b(4, "GridFix", "gridFix"), s(5, "GridFixLbl", "gridFixLbl"), s(6, "MoreLbl", "moreLbl"), b(7, "MoreOpen", "moreOpen"), li(8, "MoreItems", "moreItems", "LibTab"), s(9, "Search", "search"), s(10, "SearchPH", "searchPh"), st(11, "Genre", "genre", "SelState"), st(12, "Label", "label", "SelState"), b(13, "HasPlFacet", "hasPlFacet"), st(14, "PlFacet", "plFacet", "SelState"), st(15, "KeyChip", "keyChip", "LibChip"), s(16, "NoDropsLbl", "noDropsLbl"), b(17, "NoDrops", "noDrops"), b(18, "Clear", "clear"), s(19, "ClearLbl", "clearLbl"), st(20, "Prep", "prep", "SelState"), li(21, "Chips", "chips", "LibChip"), b(22, "HasInline", "hasInline"), st(23, "Inline", "inlineActs", "LibPlAct"), b(24, "HasResults", "hasResults"), st(25, "Results", "results", "LibFixRes"), st(26, "Head", "head", "LibCollHead"), li(27, "Rows", "rows", "LibCollRow"), s(28, "VerifiedTitle", "verifiedTitle"), s(29, "Empty", "empty"), b(30, "IsEmpty", "isEmpty"), s(31, "More", "more"), st(32, "Batch", "batch", "LibBatch")},
	},
	{
		name: "LibFavRow", goT: "libFavRowSt", zigT: "s.FavRow",
		fs: []field{s(1, "Label", "label"), s(2, "Path", "path")},
	},
	{
		name: "LibFav", goT: "libFavSt", zigT: "s.Fav",
		fs: []field{s(1, "Desc", "desc"), s(2, "Empty", "empty"), s(3, "OpenLbl", "openLbl"), s(4, "UnpinLbl", "unpinLbl"), li(5, "Rows", "rows", "LibFavRow")},
	},
	{
		name: "LibPlRow", goT: "libPlRowSt", zigT: "s.PlRow",
		fs: []field{s(1, "ID", "id"), s(2, "Icon", "icon"), s(3, "Name", "name"), s(4, "Sub", "sub"), b(5, "Sel", "sel")},
	},
	{
		name: "LibPlItem", goT: "libPlItemSt", zigT: "s.PlItem",
		fs: []field{s(1, "Pos", "pos"), s(2, "Idx", "idx"), s(3, "Path", "path"), s(4, "Title", "title"), st(5, "Key", "key", "LibKeyPill"), b(6, "Manual", "manual")},
	},
	{
		name: "LibPlOpen", goT: "libPlOpenSt", zigT: "s.PlOpen",
		fs: []field{s(1, "Title", "title"), s(2, "SmartNote", "smartNote"), st(3, "Acts", "acts", "LibPlAct"), li(4, "Items", "items", "LibPlItem"), s(5, "Empty", "empty")},
	},
	{
		name: "LibPls", goT: "libPlsSt", zigT: "s.Pls",
		fs: []field{s(1, "Msg", "msg"), s(2, "NewLbl", "newLbl"), s(3, "NewSmartLbl", "newSmartLbl"), b(4, "HasCloud", "hasCloud"), st(5, "Cloud", "cloud", "SelState"), li(6, "Rows", "rows", "LibPlRow"), s(7, "Empty", "empty"), b(8, "HasOpen", "hasOpen"), st(9, "Open", "open", "LibPlOpen")},
	},
	{
		name: "LibSess", goT: "libSessSt", zigT: "s.Sess",
		fs: []field{s(1, "Idx", "idx"), s(2, "Date", "date"), s(3, "Sub", "sub"), b(4, "Sel", "sel")},
	},
	{
		name: "LibPlayed", goT: "libPlayedSt", zigT: "s.Played",
		fs: []field{s(1, "Path", "path"), b(2, "Warn", "warn"), s(3, "Title", "title"), s(4, "Meta", "meta"), st(5, "Key", "key", "LibKeyPill")},
	},
	{
		name: "LibHist", goT: "libHistSt", zigT: "s.Hist",
		fs: []field{s(1, "LoadLbl", "loadLbl"), st(2, "Src", "src", "SelState"), s(3, "Desc", "desc"), s(4, "Empty", "empty"), b(5, "IsEmpty", "isEmpty"), li(6, "Sessions", "sessions", "LibSess"), b(7, "HasPlayed", "hasPlayed"), s(8, "PlayedLbl", "playedLbl"), s(9, "SortLbl", "sortLbl"), st(10, "Sort", "sort", "SelState"), s(11, "DirLbl", "dirLbl"), li(12, "Played", "played", "LibPlayed")},
	},
	{
		name: "LibIDMRow", goT: "libIDMRowSt", zigT: "s.IDMRow",
		fs: []field{s(1, "Path", "path"), b(2, "Artist", "artist"), s(3, "ArtistAct", "artistAct"), b(4, "Label", "label"), s(5, "LabelAct", "labelAct"), s(6, "DelAct", "delAct")},
	},
	{
		name: "LibIDM", goT: "libIDMSt", zigT: "s.IDM",
		fs: []field{s(1, "Msg", "msg"), s(2, "MarkFileLbl", "markFileLbl"), s(3, "MarkFolderLbl", "markFolderLbl"), s(4, "TypePathLbl", "typePathLbl"), s(5, "Desc", "desc"), s(6, "Empty", "empty"), s(7, "ArtistLbl", "artistLbl"), s(8, "ArtistDL", "artistDl"), s(9, "LabelLbl", "labelLbl"), s(10, "LabelDL", "labelDl"), s(11, "RemoveLbl", "removeLbl"), li(12, "Rows", "rows", "LibIDMRow")},
	},
	{
		name: "LibJob", goT: "libJobSt", zigT: "s.Job",
		fs: []field{s(1, "Label", "label"), b(2, "Cancel", "cancel"), s(3, "CancelLbl", "cancelLbl"), s(4, "CancelAct", "cancelAct"), s(5, "Status", "status"), s(6, "StatusVar", "statusVar"), s(7, "Width", "width"), s(8, "Caption", "caption"), s(9, "Msg", "msg")},
	},
	{
		name: "LibQueue", goT: "libQueueSt", zigT: "s.Queue", id: 30,
		doc: "#lib-queue-body (job progress patch)",
		fs:  []field{s(1, "Desc", "desc"), s(2, "Empty", "empty"), li(3, "Jobs", "jobs", "LibJob")},
	},
	{
		name: "LibPreset", goT: "libPresetSt", zigT: "s.Preset",
		fs: []field{s(1, "ID", "id"), s(2, "Label", "label"), s(3, "Desc", "desc")},
	},
	{
		name: "LibPresets", goT: "libPresetsSt", zigT: "s.Presets",
		fs: []field{s(1, "NewLbl", "newLbl"), s(2, "YoursTitle", "yoursTitle"), s(3, "EmptyCustom", "emptyCustom"), s(4, "BuiltinsTitle", "builtinsTitle"), s(5, "CustomBadge", "customBadge"), s(6, "BuiltinBadge", "builtinBadge"), s(7, "EditLbl", "editLbl"), s(8, "DupLbl", "dupLbl"), s(9, "DelLbl", "delLbl"), s(10, "DupEditLbl", "dupEditLbl"), li(11, "Custom", "custom", "LibPreset"), li(12, "Builtins", "builtins", "LibPreset")},
	},
	{
		name: "LibBody", goT: "libBodySt", zigT: "library.Body", id: 28,
		doc: "#lib-body (active section)",
		fs:  []field{s(1, "Kind", "kind"), s(2, "Raw", "raw"), s(3, "Msg", "msg"), st(4, "NavRail", "navRail", "LibNav"), b(5, "CEFull", "ceFull"), s(6, "CEWave", "ceWave"), st(7, "Detail", "detail", "LibDetail"), st(8, "Browse", "browse", "LibBrowse"), st(9, "Coll", "coll", "LibColl"), st(10, "Fav", "fav", "LibFav"), st(11, "Pls", "pls", "LibPls"), st(12, "Hist", "hist", "LibHist"), st(13, "IDM", "idm", "LibIDM"), st(14, "Queue", "queue", "LibQueue"), st(15, "Presets", "presets", "LibPresets")},
	},
	{
		name: "LibState", goT: "libState", zigT: "library.State", id: 27,
		doc: "Library tab (full view)",
		fs:  []field{s(1, "Title", "title"), s(2, "NavTitle", "navTitle"), s(3, "Switcher", "switcher"), b(4, "Embedded", "embedded"), s(5, "Section", "section"), li(6, "Tabs", "tabs", "LibTab"), st(7, "Body", "body", "LibBody")},
	},
	// player: nine patch targets, one message each. The full view carries the 29 kB raw waveform SVG (mpWaveSVG stays Go by design) - one huge string, so the wire's win there is smaller than on structure-heavy states. uiSlider's minS/maxS/stepS/valS/unitJs need kStrAlways (Zig defaults '0'/'1'/'\\"\\"').
	{
		name: "MpVid", goT: "mpVidSt", zigT: "player.Vid", id: 34,
		doc: "#mp-vid",
		fs:  []field{s(1, "Host", "host"), s(2, "Kind", "kind"), s(3, "ErrText", "errText"), st(4, "OpenExt", "openExt", "UiBtn"), s(5, "NoStream", "noStream"), s(6, "URL", "url"), s(7, "MSE", "mse"), b(8, "Muted", "muted"), s(9, "Ev", "ev"), s(10, "OnMeta", "onmeta"), s(11, "OnErr", "onerr")},
	},
	{
		name: "MpKVRow", goT: "mpKVRow", zigT: "player.KVRow",
		fs: []field{s(1, "K", "k"), s(2, "V", "v")},
	},
	{
		name: "MpLink", goT: "mpLinkSt", zigT: "player.Link",
		fs: []field{s(1, "URL", "url"), s(2, "Label", "label")},
	},
	{
		name: "MpChip", goT: "mpChipSt", zigT: "player.Chip",
		fs: []field{s(1, "Kind", "kind"), b(2, "Loud", "loud"), s(3, "Dim", "dim"), s(4, "Text", "text"), li(5, "Rows", "rows", "MpKVRow"), s(6, "Note", "note"), li(7, "Links", "links", "MpLink")},
	},
	{
		name: "MpWave", goT: "mpWaveSt", zigT: "player.Wave", id: 35,
		doc: "#mp-wave",
		fs:  []field{s(1, "SVG", "svg"), b(2, "HasChips", "hasChips"), st(3, "Enc", "enc", "MpChip"), st(4, "Loud", "loud", "MpChip"), s(5, "SeekTab", "seekTab"), sl(6, "Captions", "captions")},
	},
	{
		name: "MpHov", goT: "mpHovSt", zigT: "player.Hov", id: 40,
		doc: "#mp-hov hover readout",
		fs:  []field{s(1, "Text", "text"), b(2, "Raw", "raw")},
	},
	{
		name: "MpTab", goT: "mpTabSt", zigT: "c.Tab",
		fs: []field{s(1, "Val", "val"), s(2, "Label", "label")},
	},
	{
		name: "UiSlider", goT: "uiSlider", zigT: "c.Slider",
		fs: []field{s(1, "Label", "label"), s(2, "DL", "dl"), s(3, "Act", "act"), s(4, "Unit", "unit"), sa(5, "UnitJS", "unitJs"), sa(6, "MinS", "minS"), sa(7, "MaxS", "maxS"), sa(8, "StepS", "stepS"), sa(9, "ValS", "valS")},
	},
	{
		name: "MpTp", goT: "mpTpSt", zigT: "player.Tp", id: 36,
		doc: "#mp-tp transport",
		fs:  []field{s(1, "Host", "host"), b(2, "Show", "show"), b(3, "HasTabs", "hasTabs"), s(4, "TabPrefix", "tabPrefix"), s(5, "TabActive", "tabActive"), li(6, "Tabs", "tabs", "MpTab"), st(7, "Play", "play", "UiBtn"), st(8, "Stop", "stop", "UiBtn"), b(9, "HasPreview", "hasPreview"), st(10, "Preview", "preview", "UiBtn"), b(11, "HasTracks", "hasTracks"), st(12, "Prev", "prev", "UiBtn"), st(13, "TrackSel", "trackSel", "SelState"), st(14, "Next", "next", "UiBtn"), b(15, "Demoted", "demoted"), st(16, "MoreSel", "moreSel", "SelState"), st(17, "EditBtn", "editBtn", "UiBtn"), b(18, "IsVideo", "isVideo"), st(19, "OpenExt", "openExt", "UiBtn"), s(20, "TipVideo", "tipVideo"), op(21, "TipVideoS", "tipVideoSt", "Tip"), s(22, "TimeTx", "timeTx"), st(23, "Seek", "seek", "UiSlider"), st(24, "Vol", "vol", "UiSlider")},
	},
	{
		name: "MpRO", goT: "mpROSt", zigT: "player.RO", id: 39,
		doc: "#mp-ro read-only strip",
		fs:  []field{s(1, "Value", "value"), s(2, "DurLbl", "durLbl"), s(3, "Dur", "dur"), s(4, "InLbl", "inLbl"), s(5, "In", "in"), s(6, "OutLbl", "outLbl"), s(7, "Out", "out"), s(8, "KeepsLbl", "keepsLbl"), s(9, "Keeps", "keeps")},
	},
	{
		name: "MpAlignSt2", goT: "mpAlignSt2", zigT: "player.Align",
		fs: []field{b(1, "Bar", "bar"), s(2, "BarPct", "barPct"), s(3, "BarCap", "barCap"), b(4, "Err", "err"), s(5, "ErrText", "errText"), s(6, "Line", "line"), s(7, "LineVal", "lineVal"), st(8, "AlignBtn", "alignBtn", "UiBtn"), li(9, "Nudges", "nudges", "UiBtn"), st(10, "OffField", "offField", "UiField"), s(11, "TipAlign", "tipAlign"), op(12, "TipAlignS", "tipAlignSt", "Tip"), sl(13, "Warns", "warns")},
	},
	{
		name: "MpSum", goT: "mpSumSt", zigT: "player.Sum",
		fs: []field{s(1, "Tx", "tx"), s(2, "Act", "act"), s(3, "Title", "title")},
	},
	{
		name: "MpExMedia", goT: "mpExMediaSt", zigT: "player.ExMedia",
		fs: []field{st(1, "PresetSel", "presetSel", "SelState"), st(2, "Summary", "summary", "MpSum"), st(3, "OutField", "outField", "UiField"), st(4, "PickBtn", "pickBtn", "UiBtn"), st(5, "Loud", "loud", "Loud"), s(6, "LoudExtra", "loudExtra")},
	},
	{
		name: "MpExport", goT: "mpExportSt", zigT: "player.Export", id: 38,
		doc: "#mp-export",
		fs:  []field{li(1, "Medias", "medias", "MpExMedia"), b(2, "Exporting", "exporting"), s(3, "RunPct", "runPct"), s(4, "RunLabel", "runLabel"), st(5, "Cancel", "cancel", "UiBtn"), b(6, "Dual", "dual"), st(7, "ScopeSel", "scopeSel", "SelState"), st(8, "ExportBtn", "exportBtn", "UiBtn"), s(9, "Est", "est"), s(10, "LoudTx", "loudTx"), s(11, "Msg", "msg")},
	},
	{
		name: "MpEdit", goT: "mpEditSt", zigT: "player.Edit", id: 37,
		doc: "#mp-edit",
		fs:  []field{s(1, "Host", "host"), b(2, "Show", "show"), st(3, "InField", "inField", "UiField"), st(4, "OutField", "outField", "UiField"), st(5, "SetIn", "setIn", "UiBtn"), st(6, "SetOut", "setOut", "UiBtn"), st(7, "AutoSel", "autoSel", "SelState"), s(8, "TipTrim", "tipTrim"), op(9, "TipTrimS", "tipTrimSt", "Tip"), st(10, "RO", "ro", "MpRO"), b(11, "Dual", "dual"), st(12, "Align", "alignRow", "MpAlignSt2"), st(13, "Export", "exportPane", "MpExport")},
	},
	{
		name: "MpInner", goT: "mpInnerSt", zigT: "player.Inner", id: 33,
		doc: "#mp-root inner",
		fs:  []field{s(1, "Host", "host"), s(2, "Title", "title"), st(3, "Vid", "vid", "MpVid"), b(4, "Dual", "dual"), b(5, "Edit", "edit"), st(6, "Wave", "wave", "MpWave"), s(7, "LaneIn", "laneIn"), s(8, "LaneMid", "laneMid"), s(9, "LaneOut", "laneOut"), s(10, "LaneFull", "laneFull"), st(11, "ZIn", "zin", "UiBtn"), st(12, "ZOut", "zout", "UiBtn"), st(13, "FitBtn", "fit", "UiBtn"), s(14, "ZInfo", "zinfo"), st(15, "Hov", "hov", "MpHov"), s(16, "TipWave", "tipWave"), op(17, "TipWaveS", "tipWaveSt", "Tip"), st(18, "Tp", "tp", "MpTp"), st(19, "EditBox", "editBox", "MpEdit")},
	},
	{
		name: "MpFull", goT: "mpFullSt", zigT: "player.State", id: 32,
		doc: "Player (full view; the 29 kB raw waveform SVG lives here)",
		fs:  []field{s(1, "Host", "host"), st(2, "Inner", "inner", "MpInner")},
	},
	// automations: full tab + the version-gated #auto-body tick fragment.
	{
		name: "AutoLabels", goT: "autoLabels", zigT: "automations.Labels",
		fs: []field{s(1, "Enabled", "enabled"), s(2, "EnabledDL", "enabledDl"), s(3, "Run", "run"), s(4, "SchAdd", "schAdd"), s(5, "Edit", "edit"), s(6, "Delete", "delete")},
	},
	{
		name: "AutoCard", goT: "autoCard", zigT: "automations.Card",
		fs: []field{s(1, "ID", "id"), s(2, "Label", "label"), s(3, "WatchDir", "watchDir"), s(4, "Status", "status"), s(5, "StatusVar", "statusVar"), s(6, "Chain", "chain"), b(7, "Enabled", "enabled")},
	},
	{
		name: "AutoListState", goT: "autoListState", zigT: "automations.ListState",
		fs: []field{s(1, "New", "new"), s(2, "Empty", "empty"), li(3, "Cards", "cards", "AutoCard")},
	},
	{
		name: "AutoSchedCard", goT: "autoSchedCard", zigT: "automations.SchedCard",
		fs: []field{s(1, "ID", "id"), s(2, "Label", "label"), s(3, "Target", "target"), s(4, "StateText", "stateText"), s(5, "StateVar", "stateVar"), s(6, "Trigger", "trigger"), s(7, "Gates", "gates"), s(8, "LastFired", "lastFired"), s(9, "WarnTone", "warnTone"), s(10, "WarnText", "warnText"), b(11, "Enabled", "enabled")},
	},
	{
		name: "AutoSchedsState", goT: "autoSchedsState", zigT: "automations.SchedsState",
		fs: []field{s(1, "New", "new"), b(2, "Gated", "gated"), s(3, "GateWhy", "gateWhy"), s(4, "Empty", "empty"), li(5, "Cards", "cards", "AutoSchedCard")},
	},
	{
		name: "AutoRunRow", goT: "autoRunRow", zigT: "automations.RunRow",
		fs: []field{s(1, "Name", "name"), s(2, "Trigger", "trigger"), s(3, "Status", "status"), s(4, "Variant", "variant")},
	},
	{
		name: "AutoRunsState", goT: "autoRunsState", zigT: "automations.RunsState",
		fs: []field{s(1, "Empty", "empty"), li(2, "Rows", "rows", "AutoRunRow")},
	},
	{
		name: "AutoBodyState", goT: "autoBodyState", zigT: "automations.Body", id: 42,
		doc: "#auto-body (version-gated ~1 Hz tick)",
		fs:  []field{s(1, "ListTitle", "listTitle"), s(2, "SchedTitle", "schedTitle"), s(3, "RunsTitle", "runsTitle"), st(4, "Labels", "labels", "AutoLabels"), st(5, "List", "list", "AutoListState"), st(6, "Scheds", "scheds", "AutoSchedsState"), st(7, "Runs", "runs", "AutoRunsState")},
	},
	{
		name: "AutoState", goT: "autoState", zigT: "automations.State", id: 41,
		doc: "Automations tab (full view)",
		fs:  []field{s(1, "Title", "title"), s(2, "Sub", "sub"), b(3, "Available", "available"), s(4, "Unavailable", "unavailable"), st(5, "Body", "body", "AutoBodyState")},
	},
	// peers: full tab + the ~1 Hz #peers-body live tick. Carries the only []string on the wire (PeerMedia.SyncLines -> kStrList) and CamProp's slider-shaped strings need kStrAlways (Zig defaults '0'/'1').
	{
		name: "PeerBanner", goT: "peerBannerSt", zigT: "peers.Banner",
		fs: []field{b(1, "Show", "show"), s(2, "Text", "text"), st(3, "Btn", "btn", "UiBtn")},
	},
	{
		name: "PeerDeck", goT: "peerDeckSt", zigT: "peers.Deck",
		fs: []field{b(1, "Audible", "audible"), s(2, "Line", "line")},
	},
	{
		name: "PeerRow", goT: "peerRowSt", zigT: "peers.Row",
		fs: []field{s(1, "Dot", "dot"), s(2, "Name", "name"), s(3, "Sub", "sub"), li(4, "Btns", "btns", "UiBtn"), li(5, "Decks", "decks", "PeerDeck")},
	},
	{
		name: "PeerList", goT: "peerListSt", zigT: "peers.List",
		fs: []field{s(1, "Empty", "empty"), li(2, "Rows", "rows", "PeerRow")},
	},
	{
		name: "PeerRoute", goT: "peerRouteSt", zigT: "peers.Route",
		fs: []field{s(1, "Title", "title"), s(2, "Detail", "detail"), s(3, "Pipe", "pipe")},
	},
	{
		name: "PeerRecvRow", goT: "peerRecvRowSt", zigT: "peers.RecvRow",
		fs: []field{s(1, "Mark", "mark"), s(2, "Line", "line"), st(3, "Btn", "btn", "UiBtn")},
	},
	{
		name: "PeerRecv", goT: "peerRecvSt", zigT: "peers.Recv",
		fs: []field{b(1, "Show", "show"), s(2, "Head", "head"), li(3, "Rows", "rows", "PeerRecvRow")},
	},
	{
		name: "PeerMedia", goT: "peerMediaSt", zigT: "peers.Media",
		fs: []field{b(1, "Show", "show"), s(2, "ClockLine", "clockLine"), sl(3, "SyncLines", "syncLines"), b(4, "HasTC", "hasTc"), s(5, "TCLine", "tcLine"), s(6, "NoRoutes", "noRoutes"), s(7, "RoutesHdr", "routesHdr"), li(8, "Routes", "routes", "PeerRoute"), st(9, "Recv", "recv", "PeerRecv")},
	},
	{
		name: "CamProp", goT: "camPropSt", zigT: "peers.CamProp",
		fs: []field{s(1, "Label", "label"), sa(2, "MinS", "minS"), sa(3, "MaxS", "maxS"), sa(4, "StepS", "stepS"), sa(5, "ValS", "valS"), s(6, "Act", "act"), b(7, "Disabled", "disabled"), b(8, "CanAuto", "canAuto"), b(9, "Auto", "auto"), s(10, "AutoAct", "autoAct"), s(11, "AutoLbl", "autoLbl")},
	},
	{
		name: "CamNode", goT: "camNodeSt", zigT: "peers.CamNode",
		fs: []field{s(1, "Name", "name"), s(2, "RefreshAct", "refreshAct"), s(3, "Status", "status"), st(4, "Dev", "dev", "SelState"), st(5, "Mode", "mode", "SelState"), st(6, "Start", "start", "UiBtn"), s(7, "Sender", "sender"), s(8, "SenderLine", "senderLine"), s(9, "PropsHdr", "propsHdr"), li(10, "Props", "props", "CamProp")},
	},
	{
		name: "PeerCam", goT: "peerCamSt", zigT: "peers.Cam",
		fs: []field{b(1, "Show", "show"), b(2, "Gated", "gated"), s(3, "GateHint", "gateHint"), s(4, "Empty", "empty"), li(5, "Nodes", "nodes", "CamNode")},
	},
	{
		name: "XferSet", goT: "xferSetSt", zigT: "peers.XferSet",
		fs: []field{b(1, "Show", "show"), st(2, "Enabled", "enabled", "UiToggle"), s(3, "AcceptLbl", "acceptLbl"), s(4, "Mode", "mode"), s(5, "AskLbl", "askLbl"), s(6, "AutoLbl", "autoLbl"), st(7, "Dir", "dir", "UiField"), s(8, "DefaultDir", "defaultDir")},
	},
	{
		name: "XferPend", goT: "xferPendSt", zigT: "peers.XferPend",
		fs: []field{s(1, "Line", "line"), li(2, "Btns", "btns", "UiBtn")},
	},
	{
		name: "XferProg", goT: "xferProgSt", zigT: "peers.XferProg",
		fs: []field{s(1, "Title", "title"), b(2, "IsBadge", "isBadge"), st(3, "Btn", "btn", "UiBtn"), s(4, "Badge", "badge"), s(5, "BadgeVar", "badgeVar"), b(6, "Bar", "bar"), s(7, "BarPct", "barPct"), s(8, "BarCap", "barCap"), s(9, "SubText", "subText")},
	},
	{
		name: "PeerXfer", goT: "peerXferSt", zigT: "peers.Xfer",
		fs: []field{b(1, "Show", "show"), st(2, "Settings", "settings", "XferSet"), b(3, "None", "none"), s(4, "NoneHint", "noneHint"), li(5, "Pend", "pend", "XferPend"), li(6, "Rows", "rows", "XferProg")},
	},
	{
		name: "PeersBody", goT: "peersBodySt", zigT: "peers.Body", id: 44,
		doc: "#peers-body (~1 Hz live tick)",
		fs:  []field{s(1, "Strip", "strip"), st(2, "Banner", "banner", "PeerBanner"), s(3, "ConnsTitle", "connsTitle"), st(4, "Conns", "conns", "PeerList"), s(5, "MediaTitle", "mediaTitle"), st(6, "Media", "media", "PeerMedia"), s(7, "CamTitle", "camTitle"), st(8, "Cam", "cam", "PeerCam"), s(9, "XferTitle", "xferTitle"), st(10, "Xfer", "xfer", "PeerXfer"), s(11, "NetTitle", "netTitle"), st(12, "Discovered", "discovered", "PeerList"), s(13, "RememberedTitle", "rememberedTitle"), st(14, "Remembered", "remembered", "PeerList")},
	},
	{
		name: "Peers", goT: "peersSt", zigT: "peers.State", id: 43,
		doc: "Peers tab (full view)",
		fs:  []field{s(1, "Title", "title"), s(2, "Sub", "sub"), b(3, "Available", "available"), s(4, "Unavailable", "unavailable"), st(5, "Body", "body", "PeersBody")},
	},
	// ── phase B7 fan-out: root ids 45-99 (extends wave B-2's 10-44 partition; 100-149 stay
	// the fragment scheduler's). Remaining JSON-bridged tabs move to the wire by adding rows
	// here - same recipe as B-2, no new codec kinds unless a Zig default demands one. ──
	// overlays: full tab + the four live-patched fragments (#ovl-appearance, #ovl-spout,
	// #ovl-st-<kind>, #ovl-strip). UiStatus doubles as the status fragment's ROOT message
	// (id 48) - a message that is nested elsewhere can also be a root (LogsLines precedent).
	{
		name: "OvlCard", goT: "ovlCardState", zigT: "overlays.Card",
		fs: []field{s(1, "Title", "title"), s(2, "StatusID", "statusId"), st(3, "Status", "status", "UiStatus"), st(4, "En", "en", "UiToggle")},
	},
	{
		name: "OvlAppr", goT: "ovlApprState", zigT: "overlays.Appearance", id: 46,
		doc: "#ovl-appearance fragment (re-patched by the fader-flag cache)",
		fs:  []field{st(1, "Card", "card", "OvlCard"), s(2, "Note1", "note1"), li(3, "Btns", "btns", "UiBtn"), st(4, "Fader", "fader", "UiToggle"), s(5, "Note2", "note2")},
	},
	{
		name: "OvlWeb", goT: "ovlWebState", zigT: "overlays.Web",
		fs: []field{st(1, "Card", "card", "OvlCard"), st(2, "Port", "port", "UiField"), li(3, "Btns", "btns", "UiBtn"), st(4, "URL", "url", "UiKV"), s(5, "Note1", "note1"), st(6, "AutoAdd", "autoAdd", "UiToggle"), st(7, "Scene", "scene", "UiField"), st(8, "Nest", "nest", "UiToggle"), s(9, "Note2", "note2")},
	},
	{
		name: "OvlWave", goT: "ovlWaveState", zigT: "overlays.Wave",
		fs: []field{st(1, "Card", "card", "OvlCard"), s(2, "Note1", "note1"), st(3, "Zoom", "zoom", "SelState"), st(4, "Playhead", "playhead", "SelState"), st(5, "WaveColor", "waveColor", "UiField"), st(6, "WaveOpac", "waveOpac", "UiSlider"), st(7, "BgColor", "bgColor", "UiField"), st(8, "BgOpac", "bgOpac", "UiSlider"), s(9, "Note2", "note2")},
	},
	{
		name: "OvlDir", goT: "ovlDirState", zigT: "overlays.Dir",
		fs: []field{st(1, "Card", "card", "OvlCard"), st(2, "Dir", "dir", "UiField"), st(3, "Open", "open", "UiBtn"), s(4, "Note", "note")},
	},
	{
		name: "OvlNote", goT: "ovlNoteState", zigT: "overlays.Note",
		fs: []field{st(1, "Card", "card", "OvlCard"), s(2, "Note", "note")},
	},
	{
		name: "OvlSpout", goT: "ovlSpoutState", zigT: "overlays.Spout", id: 47,
		doc: "#ovl-spout fragment (re-rendered on install completion)",
		fs:  []field{s(1, "Note", "note"), s(2, "StatusLine", "statusLine"), s(3, "InstallLbl", "installLbl"), b(4, "CanInstall", "canInstall"), s(5, "OpenSdk", "openSdk"), s(6, "SdkURL", "sdkUrl")},
	},
	{
		name: "OvlVS", goT: "ovlVSState", zigT: "overlays.VideoShare",
		fs: []field{st(1, "Card", "card", "OvlCard"), s(2, "Note", "note"), st(3, "Scale", "scale", "SelState"), s(4, "Note2", "note2"), b(5, "Spout", "spout"), st(6, "SpoutCtl", "spoutCtl", "OvlSpout")},
	},
	{
		name: "OvlStrip", goT: "ovlStripState", zigT: "overlays.Strip", id: 49,
		doc: "#ovl-strip fragment (outputs summary)",
		fs:  []field{s(1, "Parts", "parts"), s(2, "Hint", "hint"), s(3, "Right", "right")},
	},
	{
		name: "OvlState", goT: "ovlState", zigT: "overlays.State", id: 45,
		doc: "Overlays tab (full view)",
		fs:  []field{s(1, "Title", "title"), s(2, "Sub", "sub"), b(3, "Available", "available"), s(4, "Unavailable", "unavailable"), li(5, "TopBtns", "topBtns", "UiBtn"), st(6, "Appearance", "appearance", "OvlAppr"), st(7, "Web", "web", "OvlWeb"), st(8, "Wave", "wave", "OvlWave"), st(9, "Png", "png", "OvlDir"), st(10, "Obs", "obs", "OvlNote"), st(11, "VS", "vs", "OvlVS"), st(12, "NP", "np", "OvlDir"), st(13, "Strip", "strip", "OvlStrip")},
	},
	// twitch: full tab + #twitch-obs + #twitch-presets + #twitch-feed (the feed is patched on
	// EVERY chat/alert event - the hot path this tab moves to the wire for).
	{
		name: "TwTag", goT: "twTag", zigT: "twitch.Tag",
		fs: []field{s(1, "Text", "text"), s(2, "Variant", "variant")},
	},
	{
		name: "TwRow", goT: "twRow", zigT: "twitch.Row",
		fs: []field{s(1, "Kind", "kind"), s(2, "Date", "date"), s(3, "Name", "name"), s(4, "NameStyle", "nameStyle"), li(5, "Tags", "tags", "TwTag"), b(6, "Mod", "mod"), s(7, "ModVal", "modVal"), s(8, "ModTitle", "modTitle"), s(9, "Text", "text"), s(10, "Variant", "variant")},
	},
	{
		name: "TwViewer", goT: "twViewerState", zigT: "twitch.Viewers",
		fs: []field{s(1, "Cls", "cls"), s(2, "Text", "text")},
	},
	{
		name: "TwObs", goT: "twObsState", zigT: "twitch.Obs", id: 51,
		doc: "#twitch-obs fragment (viewer count + cockpit)",
		fs:  []field{st(1, "Viewers", "viewers", "TwViewer"), s(2, "Cockpit", "cockpit")},
	},
	{
		name: "TwPresets", goT: "twPresetsState", zigT: "twitch.Presets", id: 52,
		doc: "#twitch-presets fragment (title-preset chip strip)",
		fs:  []field{li(1, "Chips", "chips", "UiBtn"), s(2, "Empty", "empty"), s(3, "Manage", "manage"), s(4, "Add", "add")},
	},
	{
		name: "TwFeed", goT: "twFeedState", zigT: "twitch.Feed", id: 53, retain: true,
		doc: "#twitch-feed inner fragment (patched on every chat/alert event)",
		fs:  []field{s(1, "Empty", "empty"), li(2, "Rows", "rows", "TwRow")},
	},
	{
		name: "TwState", goT: "twState", zigT: "twitch.State", id: 50,
		doc: "Twitch tab (full view)",
		fs:  []field{s(1, "Title", "title"), s(2, "Sub", "sub"), b(3, "Available", "available"), s(4, "Unavailable", "unavailable"), b(5, "ShowObs", "showObs"), s(6, "ObsTitle", "obsTitle"), st(7, "Obs", "obs", "TwObs"), b(8, "ShowPresets", "showPresets"), s(9, "PresetsTitle", "presetsTitle"), st(10, "Presets", "presets", "TwPresets"), st(11, "Feed", "feed", "TwFeed"), b(12, "ShowSend", "showSend"), s(13, "SendPH", "sendPh"), s(14, "SendLbl", "sendLbl")},
	},
	// midi mixer (i3): full tab (root 57) + the three live patch targets - #midi-active (54,
	// ~1 Hz), #midi-monitor rows (55, ~1 Hz), #midi-ctlstat-<i> (56, ~1 Hz) - plus the two pcv
	// modals (58/59, dialogs_b renderers). Tooltip/ss-label dual fields ride as kOptPtr like
	// tip2's composition; LearnGrid.ChHdrs is the second []string on the wire.
	{
		name: "MidiActive", goT: "midiActiveState", zigT: "midictl.Active", id: 54,
		doc: "#midi-active status line (~1 Hz patch target)",
		fs:  []field{s(1, "Variant", "variant"), s(2, "Label", "label"), s(3, "LabelDL", "labelDl"), s(4, "Line", "line")},
	},
	{
		name: "MidiMonRow", goT: "midiMonRow", zigT: "midimon.Row",
		fs: []field{s(1, "Ago", "ago"), s(2, "Src", "src"), s(3, "Msg", "msg")},
	},
	{
		name: "MidiMonLines", goT: "midiMonLines", zigT: "midimon.Lines", id: 55, retain: true,
		doc: "#midi-monitor inner rows (~1 Hz patch target)",
		fs:  []field{s(1, "Empty", "empty"), li(2, "Rows", "rows", "MidiMonRow")},
	},
	{
		name: "MidiMonState", goT: "midiMonState", zigT: "midimon.State",
		fs: []field{s(1, "Card", "card"), s(2, "Badge", "badge"), s(3, "Sub", "sub"), st(4, "Lines", "lines", "MidiMonLines")},
	},
	{
		name: "MidiTraceRow", goT: "midiTraceRow", zigT: "midimon.TraceRow",
		fs: []field{s(1, "DT", "dt"), s(2, "Dir", "dir"), s(3, "Label", "label"), s(4, "Hex", "hex"), s(5, "Len", "len"), s(6, "Dec", "dec")},
	},
	{
		name: "MidiTrace", goT: "midiTraceState", zigT: "midimon.Trace",
		fs: []field{s(1, "Hdr", "hdr"), b(2, "HasErr", "hasErr"), s(3, "Err", "err"), s(4, "Empty", "empty"), li(5, "Rows", "rows", "MidiTraceRow"), s(6, "Refresh", "refresh"), s(7, "Close", "close")},
	},
	{
		name: "MidiLink", goT: "midiLinkState", zigT: "ctls.Link",
		fs: []field{s(1, "Label", "label"), s(2, "URL", "url")},
	},
	{
		name: "MidiPortStat", goT: "midiPortStat", zigT: "ctls.PortStat", id: 56, retain: true,
		doc: "#midi-ctlstat-<i> inner status (~1 Hz patch target)",
		fs:  []field{b(1, "HasRow", "hasRow"), s(2, "Variant", "variant"), s(3, "Label", "label"), s(4, "LabelDL", "labelDl"), s(5, "Line", "line"), s(6, "Hint", "hint"), b(7, "HasAct", "hasAct"), s(8, "Act", "act"), s(9, "ActMsg", "actMsg")},
	},
	{
		name: "MidiChip", goT: "midiChipState", zigT: "ctls.Chip",
		fs: []field{s(1, "Label", "label"), s(2, "Act", "act"), b(3, "Active", "active")},
	},
	{
		name: "MidiDrvThru", goT: "midiDrvThru", zigT: "ctls.DrvThru",
		fs: []field{b(1, "Show", "show"), s(2, "UseInDJ", "useInDj"), s(3, "Port", "port"), s(4, "CloneLbl", "cloneLbl"), s(5, "CloneDL", "cloneDl"), s(6, "CloneAct", "cloneAct"), b(7, "CloneOn", "cloneOn"), s(8, "CloneNote", "cloneNote"), s(9, "DrvNote", "drvNote"), b(10, "HasState", "hasState"), s(11, "StVariant", "stVariant"), s(12, "StLabel", "stLabel"), s(13, "StLabelDL", "stLabelDl"), s(14, "StLine", "stLine"), s(15, "FilterLbl", "filterLbl"), s(16, "FilterTip", "filterTip"), op(17, "FilterTipS", "filterTipSt", "Tip"), li(18, "Chips", "chips", "MidiChip")},
	},
	{
		name: "MidiWarn", goT: "midiWarnState", zigT: "ctls.Warn",
		fs: []field{b(1, "Show", "show"), s(2, "Label", "label"), s(3, "LabelDL", "labelDl"), s(4, "Line", "line"), s(5, "Hint", "hint")},
	},
	{
		name: "MidiLearnCell", goT: "midiLearnCell", zigT: "ctls.LearnCell",
		fs: []field{s(1, "Act", "act"), s(2, "ClearAct", "clearAct"), s(3, "Tid", "tid"), b(4, "Set", "set"), s(5, "Readout", "readout")},
	},
	{
		name: "MidiLearnRow", goT: "midiLearnRow", zigT: "ctls.LearnRow",
		fs: []field{s(1, "Label", "label"), li(2, "Cells", "cells", "MidiLearnCell")},
	},
	{
		name: "MidiLearnGrid", goT: "midiLearnGridState", zigT: "ctls.LearnGrid",
		fs: []field{s(1, "Hdr", "hdr"), s(2, "HdrTip", "hdrTip"), op(3, "HdrTipS", "hdrTipSt", "Tip"), s(4, "Cols", "cols"), sl(5, "ChHdrs", "chHdrs"), li(6, "Rows", "rows", "MidiLearnRow"), s(7, "Learn", "learn"), s(8, "Relearn", "relearn"), s(9, "Clear", "clear")},
	},
	{
		name: "MidiCtlBlock", goT: "midiCtlBlock", zigT: "ctls.Block",
		fs: []field{s(1, "Tid", "tid"), s(2, "Title", "title"), s(3, "StatID", "statId"), st(4, "Port", "port", "SelState"), s(5, "PortLbl", "portLbl"), op(6, "PortLblS", "portLblSt", "SsLabel"), st(7, "Stat", "stat", "MidiPortStat"), s(8, "EnableLbl", "enableLbl"), s(9, "EnableDL", "enableDl"), s(10, "EnableAct", "enableAct"), b(11, "EnableOn", "enableOn"), st(12, "Thru", "thru", "SelState"), s(13, "ThruLbl", "thruLbl"), op(14, "ThruLblS", "thruLblSt", "SsLabel"), st(15, "DrvThru", "drvThru", "MidiDrvThru"), st(16, "Warn", "warn", "MidiWarn"), s(17, "Remove", "remove"), s(18, "RemoveAct", "removeAct"), st(19, "Grid", "grid", "MidiLearnGrid")},
	},
	{
		name: "MidiCtls", goT: "midiCtlsState", zigT: "ctls.State",
		fs: []field{b(1, "Show", "show"), s(2, "Card", "card"), s(3, "Badge", "badge"), s(4, "Intro", "intro"), s(5, "IntroTip", "introTip"), op(6, "IntroTipS", "introTipSt", "Tip"), s(7, "LinksLbl", "linksLbl"), li(8, "Links", "links", "MidiLink"), s(9, "Empty", "empty"), li(10, "Blocks", "blocks", "MidiCtlBlock"), s(11, "Add", "add")},
	},
	{
		name: "MidiBridge", goT: "midiBridgeState", zigT: "ctls.Bridge",
		fs: []field{b(1, "Show", "show"), s(2, "Card", "card"), s(3, "Badge", "badge"), s(4, "Intro", "intro"), s(5, "IntroTip", "introTip"), op(6, "IntroTipS", "introTipSt", "Tip"), s(7, "EnableLbl", "enableLbl"), s(8, "EnableDL", "enableDl"), s(9, "EnableAct", "enableAct"), b(10, "EnableOn", "enableOn"), s(11, "EnableTip", "enableTip"), op(12, "EnableTipS", "enableTipSt", "Tip"), st(13, "ToDJ", "toDj", "SelState"), s(14, "ToDJLbl", "toDjLbl"), op(15, "ToDJLblS", "toDjLblSt", "SsLabel"), st(16, "FromDJ", "fromDj", "SelState"), s(17, "FromDJLbl", "fromDjLbl"), op(18, "FromDJLblS", "fromDjLblSt", "SsLabel")},
	},
	{
		name: "UmTrail", goT: "umTrail", zigT: "uimap.Trail",
		fs: []field{s(1, "Kind", "kind"), st(2, "Sel", "sel", "SelState"), s(3, "Label", "label"), s(4, "Var", "@\"var\""), s(5, "Act", "act")},
	},
	{
		name: "UmRow", goT: "umRow", zigT: "uimap.Row",
		fs: []field{s(1, "Title", "title"), s(2, "Sub", "sub"), li(3, "Trail", "trail", "UmTrail")},
	},
	{
		name: "UmProfileRow", goT: "umProfileRow", zigT: "uimap.Profile",
		fs: []field{st(1, "Row", "row", "UmRow"), b(2, "HasBinds", "hasBinds"), s(3, "Empty", "empty"), li(4, "Binds", "binds", "UmRow")},
	},
	{
		name: "UmState", goT: "umState", zigT: "uimap.State",
		fs: []field{b(1, "Show", "show"), s(2, "Title", "title"), s(3, "TitleTip", "titleTip"), op(4, "TitleTipS", "titleTipSt", "Tip"), s(5, "Sub", "sub"), s(6, "EnableLbl", "enableLbl"), s(7, "EnableDL", "enableDl"), s(8, "EnableAct", "enableAct"), b(9, "EnableOn", "enableOn"), s(10, "EnableTip", "enableTip"), op(11, "EnableTipS", "enableTipSt", "Tip"), st(12, "Add", "add", "UmRow"), li(13, "Profiles", "profiles", "UmProfileRow"), s(14, "Note", "note")},
	},
	{
		name: "MidiPortCard", goT: "midiPortCard", zigT: "midictl.PortCard",
		fs: []field{s(1, "Card", "card"), s(2, "Sub", "sub"), st(3, "Port", "port", "SelState"), st(4, "Active", "active", "MidiActive"), s(5, "Panic", "panic")},
	},
	{
		name: "MidiDrvInput", goT: "midiDrvInput", zigT: "midictl.DrvInput",
		fs: []field{s(1, "Variant", "variant"), s(2, "Name", "name"), s(3, "NameDL", "nameDl"), s(4, "Line", "line"), s(5, "FbHint", "fbHint"), b(6, "HasBtns", "hasBtns"), s(7, "TraceLbl", "traceLbl"), s(8, "TraceAct", "traceAct"), b(9, "FbTest", "fbTest"), s(10, "FbTestLbl", "fbTestLbl"), s(11, "FbTestAct", "fbTestAct"), s(12, "FbTip", "fbTip"), op(13, "FbTipS", "fbTipSt", "Tip"), b(14, "FbRes", "fbRes"), s(15, "FbResVar", "fbResVar"), s(16, "FbResLbl", "fbResLbl"), s(17, "FbResDL", "fbResDl"), s(18, "FbResLine", "fbResLine")},
	},
	{
		name: "MidiDrvManaged", goT: "midiDrvManaged", zigT: "midictl.DrvManaged",
		fs: []field{s(1, "Hdr", "hdr"), s(2, "Sub", "sub"), s(3, "SyncErr", "syncErr"), b(4, "HasQueryErr", "hasQueryErr"), s(5, "QueryErr", "queryErr"), s(6, "NoneManaged", "noneManaged"), li(7, "Inputs", "inputs", "MidiDrvInput"), b(8, "ShowTrace", "showTrace"), st(9, "Trace", "trace", "MidiTrace"), s(10, "Reapply", "reapply"), s(11, "Reload", "reload")},
	},
	{
		name: "MidiDrvCard", goT: "midiDrvCard", zigT: "midictl.DrvCard",
		fs: []field{b(1, "Show", "show"), s(2, "Card", "card"), s(3, "Badge", "badge"), s(4, "BadgeVar", "badgeVar"), s(5, "Why", "why"), s(6, "StVariant", "stVariant"), s(7, "StLabel", "stLabel"), s(8, "StLabelDL", "stLabelDl"), s(9, "StLine", "stLine"), b(10, "Installed", "installed"), s(11, "TestSign", "testSign"), s(12, "Steps", "steps"), s(13, "Cmds", "cmds"), s(14, "SmartScreen", "smartScreen"), st(15, "Managed", "managed", "MidiDrvManaged"), s(16, "Docs", "docs"), s(17, "DocsURL", "docsUrl")},
	},
	{
		name: "MidiKnob", goT: "midiKnobState", zigT: "midictl.Knob",
		fs: []field{s(1, "DL", "dl"), s(2, "V", "v"), s(3, "Rot", "rot"), s(4, "Val", "val"), s(5, "Act", "act"), s(6, "Tid", "tid"), s(7, "Aria", "aria"), s(8, "Label", "label"), s(9, "CC", "cc"), s(10, "SweepAct", "sweepAct"), s(11, "SweepTitle", "sweepTitle"), s(12, "SweepAria", "sweepAria"), s(13, "SweepGlyph", "sweepGlyph")},
	},
	{
		name: "MidiMom", goT: "midiMomState", zigT: "midictl.Mom",
		fs: []field{s(1, "Cls", "cls"), s(2, "Act", "act"), s(3, "Tid", "tid"), s(4, "DL", "dl"), s(5, "Aria", "aria"), s(6, "Label", "label"), s(7, "CC", "cc")},
	},
	{
		name: "MidiStrip", goT: "midiStripState", zigT: "midictl.Strip",
		fs: []field{s(1, "Head", "head"), li(2, "Knobs", "knobs", "MidiKnob"), li(3, "Faders", "faders", "MidiKnob"), li(4, "Btns", "btns", "MidiMom")},
	},
	{
		name: "MidiRack", goT: "midiRackState", zigT: "midictl.Rack",
		fs: []field{s(1, "Card", "card"), s(2, "StepLbl", "stepLbl"), s(3, "N", "n"), s(4, "Dec", "dec"), s(5, "Inc", "inc"), b(6, "MinusOff", "minusOff"), b(7, "PlusOff", "plusOff"), s(8, "Sub", "sub"), li(9, "Strips", "strips", "MidiStrip")},
	},
	{
		name: "MidiSwRow", goT: "midiSwRow", zigT: "midictl.SwRow",
		fs: []field{s(1, "Name", "name"), s(2, "Badge", "badge"), s(3, "BadgeVar", "badgeVar"), s(4, "Note", "note")},
	},
	{
		name: "MidiHelp", goT: "midiHelpState", zigT: "midictl.Help",
		fs: []field{s(1, "Card", "card"), s(2, "Badge", "badge"), s(3, "Step1", "step1"), s(4, "Step2", "step2"), s(5, "Step3", "step3"), s(6, "Feedback", "feedback"), s(7, "Caveat", "caveat"), s(8, "Link", "link"), s(9, "SwHdr", "swHdr"), li(10, "Rows", "rows", "MidiSwRow")},
	},
	{
		name: "MidiCtl", goT: "midiCtlState", zigT: "midictl.State", id: 57,
		doc: "MIDI Mixer tab (full view)",
		fs:  []field{s(1, "Title", "title"), s(2, "Sub", "sub"), st(3, "Ctls", "ctls", "MidiCtls"), st(4, "UIMap", "uimap", "UmState"), b(5, "ShowMon", "showMon"), st(6, "Mon", "mon", "MidiMonState"), st(7, "Port", "port", "MidiPortCard"), st(8, "Driver", "driver", "MidiDrvCard"), st(9, "Rack", "rack", "MidiRack"), st(10, "Bridge", "bridge", "MidiBridge"), st(11, "Help", "help", "MidiHelp")},
	},
	{
		name: "PCView", goT: "moPCViewSt", zigT: "dialogs_b.PCViewer", id: 58,
		doc: "point-cloud viewer modal shell",
		fs:  []field{s(1, "Title", "title"), s(2, "PlayLabel", "playLabel"), s(3, "MaxFrame", "maxFrame"), s(4, "Hint", "hint"), s(5, "Close", "close")},
	},
	{
		name: "PCGpu", goT: "moPCGpuSt", zigT: "dialogs_b.PCGpu", id: 59,
		doc: "point-cloud GPU prompt modal",
		fs:  []field{s(1, "Title", "title"), s(2, "Msg", "msg"), b(3, "Enabled", "enabled"), s(4, "EnableLabel", "enableLabel"), s(5, "Close", "close")},
	},
	// vrchat family (i4): full tab (64) + #vrc-status (60) + #vrc-editor (61) + #vrc-campaths
	// (62) + #vrc-photos-body (63) + the Groups sub-tab root #vrcg-body (65) + the six group
	// modals (66-71, dialogs_b renderers). Go int fields ride kUint (Zig i64, all non-negative);
	// Campaths.SVG/PlayBtn + PhotoCell.TitleQ + InviteList.MoreMsg + MemberConfirm.Verb are
	// pre-rendered/trusted - plain kStr, raw semantics live in the renderer. VgTab duplicates
	// LogsTab's zig type (c.Tab) for the vgTabSt Go type - one message per Go type.
	{
		name: "VrcStatus", goT: "vrcStatusSt", zigT: "vrchat.Status", id: 60,
		doc: "#vrc-status account status region",
		fs:  []field{b(1, "Present", "present"), s(2, "Variant", "variant"), s(3, "Label", "label"), s(4, "DL", "dl"), s(5, "Line", "line")},
	},
	{
		name: "VrcOpt", goT: "vrcOptSt", zigT: "vrchat.Opt",
		fs: []field{s(1, "Val", "val"), s(2, "Label", "label"), b(3, "Sel", "sel")},
	},
	{
		name: "VrcPresetSel", goT: "vrcPresetSelSt", zigT: "vrchat.PresetSel",
		fs: []field{s(1, "Act", "act"), s(2, "Placeholder", "placeholder"), sl(3, "Names", "names")},
	},
	{
		name: "VrcEditor", goT: "vrcEditorSt", zigT: "vrchat.Editor", id: 61,
		doc: "#vrc-editor status & bio editor",
		fs:  []field{s(1, "StatusTitle", "statusTitle"), s(2, "StatusTip", "statusTip"), op(3, "StatusTipS", "statusTipSt", "Tip"), s(4, "PresenceLabel", "presenceLabel"), li(5, "Presence", "presence", "VrcOpt"), s(6, "StatusMsgLabel", "statusMsgLabel"), s(7, "DescCls", "descCls"), s(8, "DescCount", "descCount"), s(9, "DescVal", "descVal"), u(10, "MaxDesc", "maxDesc"), s(11, "SaveStatus", "saveStatus"), st(12, "StatusPreset", "statusPreset", "VrcPresetSel"), s(13, "PresetsLabel", "presetsLabel"), s(14, "BioTitle", "bioTitle"), s(15, "BioCls", "bioCls"), s(16, "BioCount", "bioCount"), s(17, "BioVal", "bioVal"), u(18, "MaxBio", "maxBio"), s(19, "SaveBio", "saveBio"), s(20, "BioHint", "bioHint"), s(21, "PreviewLabel", "previewLabel"), s(22, "Preview", "preview"), b(23, "HasPreview", "hasPreview"), st(24, "BioPreset", "bioPreset", "VrcPresetSel"), s(25, "VarsLabel", "varsLabel"), s(26, "RefreshLabel", "refreshLabel")},
	},
	{
		name: "VrcFrameOpt", goT: "vrcFrameOptSt", zigT: "vrchat.FrameOpt",
		fs: []field{u(1, "Frames", "frames"), u(2, "Grid", "grid"), u(3, "Res", "res"), b(4, "Sel", "sel")},
	},
	{
		name: "VrcEmotes", goT: "vrcEmotesSt", zigT: "vrchat.Emotes",
		fs: []field{s(1, "Hint", "hint"), s(2, "SourceLabel", "sourceLabel"), s(3, "NameLabel", "nameLabel"), s(4, "FramesLabel", "framesLabel"), s(5, "FPSLabel", "fpsLabel"), s(6, "TrimStart", "trimStart"), s(7, "TrimEnd", "trimEnd"), s(8, "OutDirLabel", "outDirLabel"), li(9, "FrameOpts", "frameOpts", "VrcFrameOpt"), s(10, "OutDir", "outDir"), s(11, "PingPong", "pingpong"), s(12, "Crop", "crop"), s(13, "Generate", "generate"), s(14, "OpenFolder", "openFolder"), s(15, "OpenUpload", "openUpload"), s(16, "UploadURL", "uploadUrl")},
	},
	{
		name: "VrcPathItem", goT: "vrcPathItemSt", zigT: "vrchat.PathItem",
		fs: []field{u(1, "Idx", "idx"), s(2, "Label", "label"), b(3, "Active", "active")},
	},
	{
		name: "VrcCampaths", goT: "vrcCampathsSt", zigT: "vrchat.Campaths", id: 62,
		doc: "#vrc-campaths camera-paths master/detail",
		fs:  []field{s(1, "State", "state"), s(2, "Msg", "msg"), li(3, "Items", "items", "VrcPathItem"), s(4, "SVG", "svg"), s(5, "PlayBtn", "playBtn"), s(6, "Name", "name"), s(7, "Info", "info"), s(8, "Load", "load"), s(9, "Copy", "copy"), s(10, "CopyPath", "copyPath"), s(11, "Organize", "organize"), s(12, "Hint", "hint")},
	},
	{
		name: "VrcPhotoGrp", goT: "vrcPhotoGrpSt", zigT: "vrchat.PhotoGrp",
		fs: []field{s(1, "Label", "label"), u(2, "Count", "count"), b(3, "Active", "active")},
	},
	{
		name: "VrcPhotoCell", goT: "vrcPhotoCellSt", zigT: "vrchat.PhotoCell",
		fs: []field{s(1, "File", "file"), s(2, "TitleQ", "titleQ"), s(3, "Label", "label"), s(4, "Src", "src")},
	},
	{
		name: "VrcPhotos", goT: "vrcPhotosSt", zigT: "vrchat.Photos", id: 63,
		doc: "#vrc-photos-body screenshots browser",
		fs:  []field{s(1, "State", "state"), s(2, "Msg", "msg"), li(3, "Groups", "groups", "VrcPhotoGrp"), li(4, "Cells", "cells", "VrcPhotoCell"), s(5, "Note", "note"), s(6, "OpenFolder", "openFolder"), s(7, "PhotosDir", "photosDir")},
	},
	{
		name: "VgTab", goT: "vgTabSt", zigT: "c.Tab",
		fs: []field{s(1, "Val", "val"), s(2, "Label", "label")},
	},
	{
		name: "VgBadge", goT: "vgBadgeSt", zigT: "vrcgroups.Badge",
		fs: []field{s(1, "Text", "text"), s(2, "Variant", "variant")},
	},
	{
		name: "VgBtn", goT: "vgBtnSt", zigT: "vrcgroups.Btn",
		fs: []field{s(1, "Label", "label"), s(2, "Variant", "variant"), s(3, "Act", "act")},
	},
	{
		name: "VgKV", goT: "vgKVSt", zigT: "vrcgroups.KV",
		fs: []field{s(1, "Label", "label"), s(2, "DL", "dl"), s(3, "Value", "value")},
	},
	{
		name: "VgPager", goT: "vgPagerSt", zigT: "vrcgroups.Pager",
		fs: []field{s(1, "Mode", "mode"), s(2, "Msg", "msg"), s(3, "Label", "label"), s(4, "Act", "act")},
	},
	{
		name: "VgPickerRow", goT: "vgPickerRowSt", zigT: "vrcgroups.PickerRow",
		fs: []field{u(1, "Idx", "idx"), s(2, "Name", "name"), s(3, "Meta", "meta")},
	},
	{
		name: "VgPicker", goT: "vgPickerSt", zigT: "vrcgroups.Picker",
		fs: []field{s(1, "Title", "title"), s(2, "Refresh", "refresh"), s(3, "Filter", "filter"), s(4, "State", "state"), s(5, "Msg", "msg"), li(6, "Rows", "rows", "VgPickerRow")},
	},
	{
		name: "VgRole", goT: "vgRoleSt", zigT: "vrcgroups.Role",
		fs: []field{s(1, "Name", "name"), li(2, "Tags", "tags", "VgBadge"), s(3, "Order", "order"), s(4, "Desc", "desc"), s(5, "PermSum", "permSum"), sl(6, "Perms", "perms")},
	},
	{
		name: "VgOverview", goT: "vgOverviewSt", zigT: "vrcgroups.Overview",
		fs: []field{s(1, "CardTitle", "cardTitle"), b(2, "Loading", "loading"), s(3, "LoadingMsg", "loadingMsg"), b(4, "Missing", "missing"), s(5, "MissingMsg", "missingMsg"), s(6, "AboutTitle", "aboutTitle"), s(7, "Desc", "desc"), li(8, "KVs", "kvs", "VgKV"), s(9, "RulesTitle", "rulesTitle"), s(10, "Rules", "rules"), s(11, "PermsTitle", "permsTitle"), s(12, "PermsMode", "permsMode"), s(13, "PermsMsg", "permsMsg"), li(14, "PermBadges", "permBadges", "VgBadge"), s(15, "RolesTitle", "rolesTitle"), s(16, "RolesEmpty", "rolesEmpty"), li(17, "Roles", "roles", "VgRole")},
	},
	{
		name: "VgMemberRow", goT: "vgMemberRowSt", zigT: "vrcgroups.MemberRow",
		fs: []field{s(1, "Name", "name"), li(2, "Tags", "tags", "VgBadge"), s(3, "Meta", "meta"), li(4, "Acts", "acts", "VgBtn")},
	},
	{
		name: "VgMembers", goT: "vgMembersSt", zigT: "vrcgroups.Members",
		fs: []field{s(1, "CardTitle", "cardTitle"), s(2, "State", "state"), s(3, "Msg", "msg"), li(4, "Rows", "rows", "VgMemberRow"), st(5, "Pager", "pager", "VgPager")},
	},
	{
		name: "VgUserRow", goT: "vgUserRowSt", zigT: "vrcgroups.UserRow",
		fs: []field{s(1, "Name", "name"), s(2, "Sub", "sub"), li(3, "Acts", "acts", "VgBtn")},
	},
	{
		name: "VgUsers", goT: "vgUsersSt", zigT: "vrcgroups.Users",
		fs: []field{s(1, "CardTitle", "cardTitle"), li(2, "Head", "head", "VgBtn"), s(3, "State", "state"), s(4, "Msg", "msg"), s(5, "Empty", "empty"), li(6, "Rows", "rows", "VgUserRow"), st(7, "Pager", "pager", "VgPager")},
	},
	{
		name: "VgPostRow", goT: "vgPostRowSt", zigT: "vrcgroups.PostRow",
		fs: []field{s(1, "Title", "title"), s(2, "Meta", "meta"), s(3, "Text", "text"), li(4, "Del", "del", "VgBtn")},
	},
	{
		name: "VgPosts", goT: "vgPostsSt", zigT: "vrcgroups.Posts",
		fs: []field{s(1, "AnnTitle", "annTitle"), s(2, "AnnTip", "annTip"), op(3, "AnnTipS", "annTipSt", "Tip"), b(4, "HasAnn", "hasAnn"), s(5, "AnnHead", "annHead"), s(6, "AnnWhen", "annWhen"), s(7, "AnnText", "annText"), b(8, "AnnEmpty", "annEmpty"), s(9, "AnnEmptyMsg", "annEmptyMsg"), b(10, "CanAnn", "canAnn"), s(11, "NewAnnTitle", "newAnnTitle"), s(12, "NewPostTitle", "newPostTitle"), s(13, "FTitle", "fTitle"), s(14, "FText", "fText"), s(15, "FImage", "fImage"), s(16, "FNotify", "fNotify"), s(17, "AnnSubmit", "annSubmit"), s(18, "AnnHint", "annHint"), s(19, "PostSubmit", "postSubmit"), s(20, "PostHint", "postHint"), s(21, "CardTitle", "cardTitle"), s(22, "State", "state"), s(23, "Msg", "msg"), s(24, "Empty", "empty"), li(25, "Rows", "rows", "VgPostRow"), st(26, "Pager", "pager", "VgPager")},
	},
	{
		name: "VgAuditRow", goT: "vgAuditRowSt", zigT: "vrcgroups.AuditRow",
		fs: []field{s(1, "When", "when"), s(2, "Event", "event"), s(3, "Actor", "actor"), s(4, "Desc", "desc"), s(5, "Raw", "raw")},
	},
	{
		name: "VgAudit", goT: "vgAuditSt", zigT: "vrcgroups.Audit",
		fs: []field{s(1, "CardTitle", "cardTitle"), b(2, "NoPerm", "noPerm"), s(3, "NoPermMsg", "noPermMsg"), s(4, "State", "state"), s(5, "Msg", "msg"), s(6, "Empty", "empty"), s(7, "RawSummary", "rawSummary"), li(8, "Rows", "rows", "VgAuditRow"), st(9, "Pager", "pager", "VgPager")},
	},
	{
		name: "VgWorkspace", goT: "vgWorkspaceSt", zigT: "vrcgroups.Workspace",
		fs: []field{s(1, "Title", "title"), s(2, "Refresh", "refresh"), s(3, "Back", "back"), li(4, "Badges", "badges", "VgBadge"), s(5, "View", "view"), li(6, "Tabs", "tabs", "VgTab"), st(7, "Overview", "overview", "VgOverview"), st(8, "Members", "members", "VgMembers"), st(9, "Users", "users", "VgUsers"), st(10, "Posts", "posts", "VgPosts"), st(11, "Audit", "audit", "VgAudit")},
	},
	{
		name: "Vrcg", goT: "vrcgState", zigT: "vrcgroups.State", id: 65,
		doc: "#vrcg-body Groups sub-tab root",
		fs:  []field{b(1, "Available", "available"), s(2, "Unavailable", "unavailable"), b(3, "SignedIn", "signedIn"), s(4, "SignInTitle", "signInTitle"), s(5, "SignInHint", "signInHint"), s(6, "Mode", "mode"), st(7, "Picker", "picker", "VgPicker"), st(8, "WS", "ws", "VgWorkspace")},
	},
	{
		name: "VrcTab", goT: "vrcTabSt", zigT: "vrchat.State", id: 64,
		doc: "VRChat tab (full view)",
		fs:  []field{b(1, "Available", "available"), s(2, "Title", "title"), s(3, "Sub", "sub"), s(4, "Unavailable", "unavailable"), st(5, "Status", "status", "VrcStatus"), s(6, "SubActive", "subActive"), li(7, "SubTabs", "subTabs", "VgTab"), st(8, "Groups", "groups", "Vrcg"), b(9, "LoggedIn", "loggedIn"), s(10, "SecStatusBio", "secStatusBio"), s(11, "SignInHint", "signInHint"), st(12, "Editor", "editor", "VrcEditor"), s(13, "SecEmotes", "secEmotes"), st(14, "Emotes", "emotes", "VrcEmotes"), b(15, "HasTools", "hasTools"), s(16, "SecCamPaths", "secCamPaths"), st(17, "CamPaths", "camPaths", "VrcCampaths"), s(18, "SecPhotos", "secPhotos"), st(19, "Photos", "photos", "VrcPhotos")},
	},
	{
		name: "VgRoleRow", goT: "vgRoleRowSt", zigT: "dialogs_b.RoleRow",
		fs: []field{s(1, "Label", "label"), s(2, "Desc", "desc"), s(3, "BtnLabel", "btnLabel"), s(4, "BtnVar", "btnVar"), s(5, "Act", "act")},
	},
	{
		name: "VgRoleBody", goT: "vgRoleBodySt", zigT: "dialogs_b.RoleBody", id: 66,
		doc: "#vrcg-role-body add/remove-role list",
		fs:  []field{b(1, "HasHint", "hasHint"), s(2, "HintTone", "hintTone"), s(3, "HintText", "hintText"), li(4, "Rows", "rows", "VgRoleRow")},
	},
	{
		name: "VgInviteRow", goT: "vgInviteRowSt", zigT: "dialogs_b.InviteRow",
		fs: []field{s(1, "Name", "name"), s(2, "Status", "status"), s(3, "Act", "act")},
	},
	{
		name: "VgInviteList", goT: "vgInviteListSt", zigT: "dialogs_b.InviteList", id: 67,
		doc: "#vrcg-inv-list filtered friends list",
		fs:  []field{b(1, "Loading", "loading"), s(2, "LoadingMsg", "loadingMsg"), b(3, "Empty", "empty"), s(4, "EmptyMsg", "emptyMsg"), li(5, "Rows", "rows", "VgInviteRow"), b(6, "HasMore", "hasMore"), s(7, "MoreMsg", "moreMsg")},
	},
	{
		name: "VgRolesModal", goT: "vgRolesModalSt", zigT: "dialogs_b.RolesModal", id: 68,
		doc: "roles dialog shell (embeds #vrcg-role-body)",
		fs:  []field{s(1, "Title", "title"), st(2, "Body", "body", "VgRoleBody")},
	},
	{
		name: "VgInviteModal", goT: "vgInviteModalSt", zigT: "dialogs_b.InviteModal", id: 69,
		doc: "invite dialog shell (embeds #vrcg-inv-list)",
		fs:  []field{s(1, "Title", "title"), s(2, "SearchPh", "searchPh"), s(3, "IDPh", "idPh"), s(4, "IDBtn", "idBtn"), st(5, "List", "list", "VgInviteList")},
	},
	{
		name: "VgMemberConfirm", goT: "vgMemberConfirmSt", zigT: "dialogs_b.MemberConfirm", id: 70,
		doc: "kick/ban confirm dialog",
		fs:  []field{s(1, "Title", "title"), s(2, "Verb", "verb"), s(3, "Name", "name"), s(4, "Group", "group"), s(5, "Note", "note"), s(6, "Act", "act"), s(7, "Cancel", "cancel")},
	},
	{
		name: "VgPostConfirm", goT: "vgPostConfirmSt", zigT: "dialogs_b.PostConfirm", id: 71,
		doc: "delete-post confirm dialog",
		fs:  []field{s(1, "Title", "title"), s(2, "Post", "post"), s(3, "Group", "group"), s(4, "Confirm", "confirm"), s(5, "Cancel", "cancel")},
	},
	// worlds family (i5): full tab (76) + the four live patch targets - #world-linkhint (72),
	// #world-gh (73), #world-st-<key> (74), #world-unity-rows (75) - plus the nine ws modals
	// (77-85; #world-fr-list / #world-grp-list / #world-role-list are async-patched inners).
	// Prose fields are trusted Go-source literals rendered raw on BOTH sides; plain kStr here,
	// raw semantics live in the renderers.
	{
		name: "WsHint", goT: "wsHintSt", zigT: "worlds.Hint", id: 72,
		doc: "#world-linkhint chip",
		fs:  []field{s(1, "Tone", "tone"), s(2, "Text", "text")},
	},
	{
		name: "WsGitHub", goT: "wsGitHubSt", zigT: "worlds.GitHub", id: 73,
		doc: "#world-gh link control",
		fs:  []field{s(1, "Mode", "mode"), s(2, "Msg", "msg"), s(3, "LinkedLabel", "linkedLabel"), s(4, "LinkedDL", "linkedDl"), s(5, "Login", "login"), s(6, "LinkedHelp", "linkedHelp"), s(7, "UnlinkLabel", "unlinkLabel"), s(8, "UnlinkedHelp", "unlinkedHelp"), s(9, "DeviceLabel", "deviceLabel"), s(10, "PatLabel", "patLabel")},
	},
	{
		name: "WsStatus", goT: "wsStatusSt", zigT: "worlds.Status", id: 74,
		doc: "one #world-st-<key> publish status",
		fs:  []field{s(1, "Tone", "tone"), s(2, "Line", "line"), s(3, "URL", "url"), s(4, "CopyLabel", "copyLabel"), s(5, "OpenLabel", "openLabel"), s(6, "HTMLURL", "htmlUrl")},
	},
	{
		name: "WsListRow", goT: "wsListRowSt", zigT: "worlds.ListRow",
		fs: []field{s(1, "Key", "key"), s(2, "Name", "name"), s(3, "Entries", "entries"), s(4, "EditAct", "editAct"), s(5, "PubAct", "pubAct"), s(6, "DelAct", "delAct"), st(7, "Status", "status", "WsStatus")},
	},
	{
		name: "WsLists", goT: "wsListsSt", zigT: "worlds.Lists",
		fs: []field{s(1, "Help", "help"), s(2, "Empty", "empty"), li(3, "Rows", "rows", "WsListRow"), s(4, "EditLabel", "editLabel"), s(5, "PubLabel", "pubLabel"), s(6, "DelLabel", "delLabel"), s(7, "AddPlaceholder", "addPlaceholder"), s(8, "AddLabel", "addLabel")},
	},
	{
		name: "WsPosterRow", goT: "wsPosterRowSt", zigT: "worlds.PosterRow",
		fs: []field{s(1, "Title", "title"), s(2, "Sub", "sub"), s(3, "EditAct", "editAct"), s(4, "DelAct", "delAct")},
	},
	{
		name: "WsPosters", goT: "wsPostersSt", zigT: "worlds.Posters",
		fs: []field{s(1, "CardTitle", "cardTitle"), s(2, "AddLabel", "addLabel"), s(3, "PubLabel", "pubLabel"), s(4, "ToggleLabel", "toggleLabel"), s(5, "ToggleDL", "toggleDl"), b(6, "ToggleOn", "toggleOn"), s(7, "Help", "help"), s(8, "Empty", "empty"), li(9, "Rows", "rows", "WsPosterRow"), s(10, "EditLabel", "editLabel"), s(11, "DelLabel", "delLabel"), st(12, "Status", "status", "WsStatus")},
	},
	{
		name: "WsEvents", goT: "wsEventsSt", zigT: "worlds.Events",
		fs: []field{s(1, "CardTitle", "cardTitle"), s(2, "PubLabel", "pubLabel"), s(3, "ToggleLabel", "toggleLabel"), s(4, "ToggleDL", "toggleDl"), b(5, "ToggleOn", "toggleOn"), s(6, "Help", "help"), st(7, "Status", "status", "WsStatus")},
	},
	{
		name: "WsNowPlaying", goT: "wsNowPlayingSt", zigT: "worlds.NowPlaying",
		fs: []field{s(1, "CardTitle", "cardTitle"), s(2, "PubLabel", "pubLabel"), s(3, "ToggleLabel", "toggleLabel"), s(4, "ToggleDL", "toggleDl"), b(5, "ToggleOn", "toggleOn"), s(6, "LinkLabel", "linkLabel"), s(7, "LinkDL", "linkDl"), s(8, "Link", "link"), s(9, "ImgLabel", "imgLabel"), s(10, "ImgDL", "imgDl"), s(11, "Img", "img"), s(12, "ImgWarn", "imgWarn"), s(13, "Help", "help"), st(14, "Status", "status", "WsStatus")},
	},
	{
		name: "WsUnityRow", goT: "wsUnityRowSt", zigT: "worlds.UnityRow",
		fs: []field{s(1, "Name", "name"), s(2, "Dir", "dir"), s(3, "Act", "act")},
	},
	{
		name: "WsUnity", goT: "wsUnitySt", zigT: "worlds.Unity", id: 75,
		doc: "#world-unity-rows hand-off list",
		fs:  []field{s(1, "Mode", "mode"), s(2, "Msg", "msg"), s(3, "WriteLabel", "writeLabel"), li(4, "Rows", "rows", "WsUnityRow")},
	},
	{
		name: "Worlds", goT: "worldsState", zigT: "worlds.State", id: 76,
		doc: "Worlds tab (full view)",
		fs:  []field{b(1, "Available", "available"), s(2, "Title", "title"), s(3, "Sub", "sub"), s(4, "Unavailable", "unavailable"), st(5, "LinkHint", "linkHint", "WsHint"), s(6, "SecGitHub", "secGitHub"), st(7, "GH", "gh", "WsGitHub"), s(8, "SecLists", "secLists"), st(9, "Lists", "lists", "WsLists"), s(10, "SecPosters", "secPosters"), st(11, "Posters", "posters", "WsPosters"), s(12, "SecEvents", "secEvents"), st(13, "Events", "events", "WsEvents"), s(14, "SecNP", "secNp"), st(15, "NP", "np", "WsNowPlaying"), s(16, "SecUnity", "secUnity"), s(17, "UnityHelp", "unityHelp"), st(18, "Unity", "unity", "WsUnity")},
	},
	{
		name: "WsEntryRow", goT: "wsEntryRowSt", zigT: "dialogs_b.WsEntryRow",
		fs: []field{s(1, "Label", "label"), s(2, "Act", "act")},
	},
	{
		name: "WsListEditor", goT: "wsListEditorSt", zigT: "dialogs_b.WsListEditor", id: 77,
		doc: "permission-list entry editor dialog",
		fs:  []field{s(1, "Title", "title"), s(2, "Help", "help"), b(3, "Empty", "empty"), s(4, "EmptyMsg", "emptyMsg"), li(5, "Entries", "entries", "WsEntryRow"), s(6, "DelLabel", "delLabel"), s(7, "AddPh", "addPh"), s(8, "AddBtn", "addBtn"), s(9, "FriendBtn", "friendBtn"), s(10, "FriendAct", "friendAct"), s(11, "GroupBtn", "groupBtn"), s(12, "GroupAct", "groupAct")},
	},
	{
		name: "WsPosterEditor", goT: "wsPosterEditorSt", zigT: "dialogs_b.WsPosterEditor", id: 78,
		doc: "poster-slot editor form",
		fs:  []field{s(1, "Title", "title"), s(2, "Idx", "idx"), s(3, "ImgLbl", "imgLbl"), s(4, "Img", "img"), s(5, "ImgPh", "imgPh"), s(6, "CapLbl", "capLbl"), s(7, "Caption", "caption"), s(8, "CapPh", "capPh"), s(9, "LinkLbl", "linkLbl"), s(10, "Link", "link"), s(11, "LinkPh", "linkPh"), b(12, "HasWarn", "hasWarn"), s(13, "Warn", "warn"), s(14, "Save", "save")},
	},
	{
		name: "WsPickRow", goT: "wsPickRowSt", zigT: "dialogs_b.WsPickRow",
		fs: []field{s(1, "Label", "label"), s(2, "Act", "act")},
	},
	{
		name: "WsFriendList", goT: "wsFriendListSt", zigT: "dialogs_b.WsFriendList", id: 79,
		doc: "#world-fr-list inner (async friends load / filter)",
		fs:  []field{b(1, "Loading", "loading"), s(2, "LoadingMsg", "loadingMsg"), li(3, "Rows", "rows", "WsPickRow"), s(4, "AddLabel", "addLabel"), b(5, "HasMore", "hasMore"), s(6, "MoreMsg", "moreMsg"), b(7, "Empty", "empty"), s(8, "EmptyMsg", "emptyMsg")},
	},
	{
		name: "WsFriendPicker", goT: "wsFriendPickerSt", zigT: "dialogs_b.WsFriendPicker", id: 80,
		doc: "friend-picker dialog shell",
		fs:  []field{s(1, "Title", "title"), s(2, "SearchPh", "searchPh"), s(3, "BackLbl", "backLbl"), s(4, "BackAct", "backAct"), st(5, "List", "list", "WsFriendList")},
	},
	{
		name: "WsGroupRow", goT: "wsGroupRowSt", zigT: "dialogs_b.WsGroupRow",
		fs: []field{s(1, "Label", "label"), s(2, "FavLabel", "favLabel"), s(3, "FavAct", "favAct"), s(4, "RolesAct", "rolesAct")},
	},
	{
		name: "WsGroupSec", goT: "wsGroupSecSt", zigT: "dialogs_b.WsGroupSec",
		fs: []field{s(1, "Caption", "caption"), li(2, "Rows", "rows", "WsGroupRow")},
	},
	{
		name: "WsGroupList", goT: "wsGroupListSt", zigT: "dialogs_b.WsGroupList", id: 81,
		doc: "#world-grp-list inner (own-groups load + group search)",
		fs:  []field{b(1, "Loading", "loading"), s(2, "LoadingMsg", "loadingMsg"), li(3, "Sections", "sections", "WsGroupSec"), s(4, "RolesLabel", "rolesLabel"), b(5, "Empty", "empty"), s(6, "EmptyMsg", "emptyMsg")},
	},
	{
		name: "WsGroupPicker", goT: "wsGroupPickerSt", zigT: "dialogs_b.WsGroupPicker", id: 82,
		doc: "group-picker dialog shell",
		fs:  []field{s(1, "Title", "title"), s(2, "SearchPh", "searchPh"), s(3, "SearchBtn", "searchBtn"), s(4, "Help", "help"), s(5, "BackLbl", "backLbl"), s(6, "BackAct", "backAct"), st(7, "List", "list", "WsGroupList")},
	},
	{
		name: "WsRoleList", goT: "wsRoleListSt", zigT: "dialogs_b.WsRoleList", id: 83,
		doc: "#world-role-list inner (async roles load)",
		fs:  []field{b(1, "Loading", "loading"), s(2, "LoadingMsg", "loadingMsg"), s(3, "AllLabel", "allLabel"), s(4, "GrantLabel", "grantLabel"), li(5, "Rows", "rows", "WsPickRow")},
	},
	{
		name: "WsRolePicker", goT: "wsRolePickerSt", zigT: "dialogs_b.WsRolePicker", id: 84,
		doc: "role-grant dialog shell",
		fs:  []field{s(1, "Title", "title"), s(2, "BackLbl", "backLbl"), s(3, "BackAct", "backAct"), st(4, "List", "list", "WsRoleList")},
	},
	{
		name: "WsDevice", goT: "wsDeviceSt", zigT: "dialogs_b.WsDevice", id: 85,
		doc: "GitHub device-code dialog",
		fs:  []field{s(1, "Title", "title"), s(2, "Help", "help"), s(3, "Code", "code"), s(4, "CopyLbl", "copyLbl"), s(5, "OpenLbl", "openLbl"), s(6, "URI", "uri")},
	},
	// editor/cueedit/mirror/rce/library-modals/remote (i6): roots 86-99. 86/87 = remote-library
	// mirror body + #rmirror-banner (patched per session-state move); 88-90 = remote cue-edit
	// panes (#rce-info, #lib-body, save rail); 91/92 = #ed-preview + full Editor view; 93-95 =
	// cue-editor #ce-topbar / wave strip / rail (re-rendered during drag - the hot path); 96 =
	// #gf-live (existing message, promoted to root; ~2 Hz from the fixer run goroutine); 97/98 =
	// smart-rules + relocate modals; 99 = the "Controlling [peer]" switcher. EdLayer is the
	// schema's first self-recursive message (children) - decode depth is bounded by the
	// document's byte length (every nesting level consumes a tag), fuzz leans on that.
	{
		name: "LibMirrorBan", goT: "libMirrorBanSt", zigT: "libviews.MirrorBanner", id: 87,
		doc: "#rmirror-banner status strip (patched on session-state moves)",
		fs:  []field{s(1, "Status", "status"), s(2, "Title", "title"), s(3, "Tip", "tip"), op(4, "TipS", "tipSt", "Tip"), b(5, "HasNote", "hasNote"), s(6, "Note", "note"), b(7, "IsErr", "isErr"), s(8, "Err", "err"), s(9, "Reconnect", "reconnect")},
	},
	{
		name: "LibMirror", goT: "libMirrorSt", zigT: "libviews.Mirror", id: 86,
		doc: "remote-library mirror body (#lib-body while a peer is targeted)",
		fs:  []field{b(1, "NoLink", "noLink"), s(2, "NoLinkMsg", "noLinkMsg"), st(3, "Banner", "banner", "LibMirrorBan")},
	},
	{
		name: "RceNav", goT: "rceNavSt", zigT: "libviews.RceNav",
		fs: []field{s(1, "Label", "label"), s(2, "Act", "act"), b(3, "Gated", "gated"), s(4, "Why", "why")},
	},
	{
		name: "RceInfo", goT: "rceInfoSt", zigT: "libviews.RceInfo", id: 88,
		doc: "#rce-info left pane (remote cue-edit)",
		fs:  []field{b(1, "Show", "show"), s(2, "Eyebrow", "eyebrow"), s(3, "Title", "title"), s(4, "Path", "path"), b(5, "HasSet", "hasSet"), s(6, "SetLine", "setLine"), st(7, "Prev", "prev", "RceNav"), st(8, "Next", "next", "RceNav"), s(9, "LocalNote", "localNote"), li(10, "Hints", "hints", "LibHint"), s(11, "Back", "back")},
	},
	{
		name: "RceBody", goT: "rceBodySt", zigT: "libviews.RceBody", id: 89,
		doc: "#lib-body while remote-editing (wave strip + info + detail)",
		fs:  []field{s(1, "Wave", "wave"), st(2, "Info", "info", "RceInfo"), st(3, "Detail", "detail", "LibDetail")},
	},
	{
		name: "RceWrite", goT: "rceWriteSt", zigT: "libviews.RceWrite",
		fs: []field{b(1, "Done", "done"), s(2, "Text", "text"), s(3, "Act", "act"), b(4, "Gated", "gated"), s(5, "Why", "why")},
	},
	{
		name: "RceSave", goT: "rceSaveSt", zigT: "libviews.RceSave", id: 90,
		doc: "remote cue-edit save/write-back rail section",
		fs:  []field{b(1, "Show", "show"), s(2, "Header", "header"), b(3, "Moved", "moved"), s(4, "MovedText", "movedText"), s(5, "ReloadLbl", "reloadLbl"), b(6, "HasErr", "hasErr"), s(7, "ErrText", "errText"), s(8, "Status", "status"), s(9, "StatusText", "statusText"), s(10, "UnsavedText", "unsavedText"), s(11, "SaveLbl", "saveLbl"), b(12, "HasWrites", "hasWrites"), s(13, "WriteHeader", "writeHeader"), li(14, "Writes", "writes", "RceWrite")},
	},
	{
		name: "EdGradStop", goT: "edGradStop", zigT: "editor.GradStop",
		fs: []field{s(1, "RGBA", "rgba"), s(2, "Pos", "pos")},
	},
	{
		name: "EdPaint", goT: "edPaint", zigT: "editor.Paint",
		fs: []field{s(1, "Kind", "kind"), s(2, "RGBA", "rgba"), s(3, "Angle", "angle"), li(4, "Stops", "stops", "EdGradStop"), s(5, "URLQ", "urlq"), s(6, "Size", "size")},
	},
	{
		name: "EdText", goT: "edText", zigT: "editor.Text",
		fs: []field{s(1, "Content", "content"), s(2, "FamQ", "famq"), s(3, "Size", "size"), s(4, "LH", "lh"), s(5, "Align", "alignment"), s(6, "RGBA", "rgba"), s(7, "LS", "ls")},
	},
	{
		name: "EdInner", goT: "edInner", zigT: "editor.Inner",
		fs: []field{s(1, "Kind", "kind"), st(2, "Text", "text", "EdText"), s(3, "Placeholder", "placeholder")},
	},
	{
		name: "EdLayer", goT: "edLayer", zigT: "editor.Layer",
		fs: []field{b(1, "Group", "group"), s(2, "ID", "id"), b(3, "Sel", "sel"), s(4, "Blend", "blend"), s(5, "Opacity", "opacity"), b(6, "Xform", "xform"), s(7, "Tx", "tx"), s(8, "Ty", "ty"), s(9, "Sx", "sx"), s(10, "Sy", "sy"), s(11, "Rot", "rot"), s(12, "Left", "left"), s(13, "Top", "top"), s(14, "W", "w"), s(15, "H", "h"), st(16, "Paint", "paint", "EdPaint"), st(17, "Inner", "inner", "EdInner"), li(18, "Children", "children", "EdLayer")},
	},
	{
		name: "EdPreview", goT: "edPreviewState", zigT: "editor.Preview", id: 91,
		doc: "#ed-preview live composite",
		fs:  []field{s(1, "AW", "aw"), s(2, "AH", "ah"), li(3, "Layers", "layers", "EdLayer"), s(4, "Cap", "cap"), s(5, "Hint", "hint")},
	},
	{
		name: "EdRow", goT: "edRow", zigT: "editor.Row",
		fs: []field{s(1, "ID", "id"), s(2, "Name", "name"), u(3, "Depth", "depth"), b(4, "Group", "group"), b(5, "Sel", "sel"), b(6, "Visible", "visible"), b(7, "Locked", "locked")},
	},
	{
		name: "EdActions", goT: "edActionsState", zigT: "editor.Actions",
		fs: []field{s(1, "Up", "up"), s(2, "Down", "down"), s(3, "Group", "group"), s(4, "Ungroup", "ungroup"), s(5, "Delete", "delete"), b(6, "HasSel", "hasSel"), s(7, "NoSel", "noSel"), st(8, "Opacity", "opacity", "UiSlider"), st(9, "Blend", "blend", "SelState")},
	},
	{
		name: "EdLayers", goT: "edLayersState", zigT: "editor.Layers",
		fs: []field{li(1, "Rows", "rows", "EdRow"), s(2, "Empty", "empty"), st(3, "Actions", "actions", "EdActions")},
	},
	{
		name: "EdColorRow", goT: "edColorRowState", zigT: "editor.ColorRow",
		fs: []field{s(1, "RGBA", "rgba"), st(2, "Field", "field", "UiField")},
	},
	{
		name: "EdInspText", goT: "edInspTextState", zigT: "editor.InspText",
		fs: []field{s(1, "Label", "label"), s(2, "Content", "content"), s(3, "Hint", "hint"), st(4, "Font", "font", "SelState"), st(5, "Size", "size", "UiField"), st(6, "LS", "ls", "UiField"), st(7, "LH", "lh", "UiField"), st(8, "Align", "alignment", "SelState"), st(9, "Color", "color", "EdColorRow")},
	},
	{
		name: "EdInsp", goT: "edInspState", zigT: "editor.Insp",
		fs: []field{b(1, "HasSel", "hasSel"), s(2, "Empty", "empty"), st(3, "Name", "name", "UiField"), st(4, "X", "x", "UiField"), st(5, "Y", "y", "UiField"), b(6, "ShowWH", "showWh"), st(7, "W", "w", "UiField"), st(8, "H", "h", "UiField"), st(9, "SX", "sx", "UiField"), st(10, "SY", "sy", "UiField"), st(11, "Rot", "rot", "UiField"), s(12, "Kind", "kind"), st(13, "Text", "text", "EdInspText"), st(14, "Fill", "fill", "EdColorRow"), st(15, "Angle", "angle", "UiField"), st(16, "Start", "start", "EdColorRow"), st(17, "End", "end", "EdColorRow"), st(18, "Path", "path", "UiField"), st(19, "Fit", "fit", "SelState")},
	},
	{
		name: "EdView", goT: "edViewState", zigT: "editor.State", id: 92,
		doc: "Editor tab (full view)",
		fs:  []field{s(1, "Title", "title"), s(2, "Sub", "sub"), b(3, "Disabled", "disabled"), s(4, "DisabledSub", "disabledSub"), s(5, "DisabledHint", "disabledHint"), s(6, "SecPreview", "secPreview"), s(7, "SecLayers", "secLayers"), s(8, "SecInspector", "secInspector"), li(9, "Row1", "row1", "UiBtn"), li(10, "Row2", "row2", "UiBtn"), st(11, "Preview", "preview", "EdPreview"), st(12, "Layers", "layers", "EdLayers"), st(13, "Insp", "insp", "EdInsp")},
	},
	{
		name: "CeTbDrop", goT: "ceTbDropSt", zigT: "cueedit.TbDrop",
		fs: []field{s(1, "Act", "act"), s(2, "Lbl", "lbl"), s(3, "When", "when")},
	},
	{
		name: "CeTopbar", goT: "ceTopbarSt", zigT: "cueedit.Topbar", id: 93, retain: true,
		doc: "#ce-topbar readout strip (re-rendered during drag)",
		fs:  []field{b(1, "Show", "show"), s(2, "Eyebrow", "eyebrow"), s(3, "Title", "title"), b(4, "HasRce", "hasRce"), s(5, "RceMeta", "rceMeta"), b(6, "Dirty", "dirty"), s(7, "DirtyTip", "dirtyTip"), s(8, "Meta", "meta"), s(9, "Cursor", "cursor"), s(10, "BarLbl", "barLbl"), s(11, "BarBeat", "barBeat"), s(12, "Jump", "jump"), li(13, "Drops", "drops", "CeTbDrop"), s(14, "Census", "census"), b(15, "NoTag", "noTag"), s(16, "NoTagTip", "noTagTip"), b(17, "Verified", "verified"), b(18, "Verifiable", "verifiable"), s(19, "VerifyAct", "verifyAct"), s(20, "VerifiedTip", "verifiedTip"), s(21, "VerifiedLbl", "verifiedLbl"), s(22, "VerifyTip", "verifyTip"), s(23, "VerifyLbl", "verifyLbl"), s(24, "Tip", "tip"), op(25, "TipS", "tipSt", "Tip"), st(26, "Close", "close", "UiBtn")},
	},
	{
		name: "CeWave", goT: "ceWaveSt", zigT: "cueedit.Wave", id: 94,
		doc: "cue-edit full-width player strip",
		fs:  []field{st(1, "Topbar", "topbar", "CeTopbar"), s(2, "Player", "player")},
	},
	{
		name: "CeDefaults", goT: "ceDefaultsSt", zigT: "cueedit.Defaults",
		fs: []field{s(1, "Arrow", "arrow"), s(2, "Title", "title"), b(3, "Open", "open"), st(4, "Pads", "pads", "SelState"), st(5, "Ow", "ow", "UiToggle"), st(6, "Split", "split", "UiToggle"), b(7, "HasPromote", "hasPromote"), st(8, "Promote", "promote", "UiToggle"), b(9, "HasGrid", "hasGrid"), st(10, "Grid", "grid", "UiToggle"), s(11, "Note", "note")},
	},
	{
		name: "CeARow", goT: "ceARowSt", zigT: "cueedit.ARow",
		fs: []field{b(1, "Placed", "placed"), s(2, "Tag", "tag"), s(3, "Act", "act"), s(4, "When", "when"), s(5, "UnplacedTip", "unplacedTip"), s(6, "UnplacedLbl", "unplacedLbl"), b(7, "HasSel", "hasSel"), st(8, "Sel", "sel", "SelState")},
	},
	{
		name: "CeAssign", goT: "ceAssignSt", zigT: "cueedit.Assign",
		fs: []field{s(1, "Title", "title"), li(2, "Rows", "rows", "CeARow"), b(3, "ShowNoDrops", "showNoDrops"), s(4, "NoDropsHint", "noDropsHint")},
	},
	{
		name: "CeBatch", goT: "ceBatchSt", zigT: "cueedit.Batch",
		fs: []field{b(1, "Show", "show"), s(2, "Header", "header"), st(3, "ApplyHot", "applyHot", "UiBtn"), st(4, "ApplyMem", "applyMem", "UiBtn"), st(5, "PromoteSel", "promoteSel", "UiBtn"), st(6, "ConvertSel", "convertSel", "UiBtn"), st(7, "ClearSel", "clearSel", "UiBtn"), s(8, "Note", "note")},
	},
	{
		name: "CeRail", goT: "ceRailSt", zigT: "cueedit.Rail", id: 95,
		doc: "cue-editor rail (#lib-detail inner in cue-edit mode)",
		fs:  []field{b(1, "Show", "show"), s(2, "Eyebrow", "eyebrow"), s(3, "Title", "title"), st(4, "Mode", "mode", "SelState"), st(5, "Defaults", "defaults", "CeDefaults"), s(6, "PrepSel", "prepSel"), s(7, "PrepHint", "prepHint"), st(8, "Assign", "assign", "CeAssign"), st(9, "AddDrop", "addDrop", "UiBtn"), st(10, "DelDrop", "delDrop", "UiBtn"), b(11, "HasSel", "hasSel"), s(12, "SelLbl", "selLbl"), s(13, "PatNamePH", "patNamePh"), st(14, "SavePat", "savePat", "UiBtn"), b(15, "HasDSel", "hasDsel"), s(16, "DSelLbl", "dselLbl"), b(17, "ShowDelHint", "showDelHint"), s(18, "DelHint", "delHint"), b(19, "HasPats", "hasPats"), st(20, "Manage", "manage", "UiBtn"), b(21, "HasDrops", "hasDrops"), st(22, "ApplyHot", "applyHot", "UiBtn"), st(23, "ApplyMem", "applyMem", "UiBtn"), b(24, "ShowOwNote", "showOwNote"), s(25, "OwNote", "owNote"), st(26, "PromoteAll", "promoteAll", "UiBtn"), st(27, "ConvertAll", "convertAll", "UiBtn"), st(28, "ClearOne", "clearOne", "UiBtn"), li(29, "Hints", "hints", "LibHint"), st(30, "Batch", "batch", "CeBatch"), s(31, "WriteBack", "writeBack"), st(32, "Close", "close", "UiBtn")},
	},
	{
		name: "LibSmartModal", goT: "libSmartModalSt", zigT: "libviews.SmartModal", id: 97,
		doc: "smart-rules editor modal",
		fs:  []field{s(1, "Title", "title"), s(2, "Desc", "desc"), st(3, "Name", "name", "LibPBField"), s(4, "GenresLbl", "genresLbl"), li(5, "Genres", "genres", "LibChip"), st(6, "Feel", "feel", "SelState"), st(7, "BPMMin", "bpmMin", "LibPBField"), st(8, "BPMMax", "bpmMax", "LibPBField"), st(9, "KeyField", "keyField", "LibPBField"), st(10, "Rating", "rating", "SelState"), st(11, "Plays", "plays", "LibPBField"), st(12, "Search", "search", "LibPBField"), s(13, "CompatLbl", "compatLbl"), st(14, "Compat", "compat", "SelState"), b(15, "HasDepth", "hasDepth"), li(16, "Depth", "depth", "LibChip"), s(17, "CompatHint", "compatHint"), s(18, "Count", "count"), s(19, "Confirm", "confirm"), s(20, "Cancel", "cancel")},
	},
	{
		name: "LibRelocRow", goT: "libRelocRowSt", zigT: "libviews.RelocRow",
		fs: []field{s(1, "Act", "act"), b(2, "Checked", "checked"), s(3, "Old", "old"), s(4, "New", "newPath"), s(5, "Conf", "conf"), s(6, "ConfVar", "confVar")},
	},
	{
		name: "LibRelocModal", goT: "libRelocModalSt", zigT: "libviews.RelocModal", id: 98,
		doc: "relocate-missing modal",
		fs:  []field{s(1, "Title", "title"), s(2, "Desc", "desc"), s(3, "Missing", "missing"), s(4, "Root", "root"), s(5, "RootPH", "rootPh"), s(6, "BrowseLbl", "browseLbl"), s(7, "FindLbl", "findLbl"), b(8, "HasMsg", "hasMsg"), s(9, "Msg", "msg"), b(10, "HasRows", "hasRows"), li(11, "Rows", "rows", "LibRelocRow"), b(12, "HasMore", "hasMore"), s(13, "More", "more"), s(14, "ApplyLbl", "applyLbl")},
	},
	{
		name: "LibRemote", goT: "libRemoteSt", zigT: "libremote.State", id: 99,
		doc: "'Controlling [peer]' target switcher row",
		fs:  []field{b(1, "Show", "show"), st(2, "Sel", "sel", "SelState")},
	},
	// dialogs_a + automations dialogs + publish-remote + update-flow (i7): the LAST JSON
	// bridges. Roots 102-108 dialogs_a (7 modals), 109-111 automations editor/run-now/schedule,
	// 112 remote Publish view, 113 #inst-update region. AeBlock is the discriminated form-block
	// kit (kind names which fields are read - all cross the wire, zero-cost for absent ones);
	// DlgField/ArFoot/AeStep ride under it. Shared messages (UiBtn/UiKV/UiField/UiToggle/
	// LibPBField/LibSelTip/LibChip/LibHint/Loud/SelState/Tip/SsLabel) are reused, not re-keyed.
	{
		name: "DlgChoice", goT: "dlgChoiceSt", zigT: "c.Choice", id: 102,
		doc: "generic choice dialog",
		fs:  []field{s(1, "Title", "title"), s(2, "Msg", "msg"), b(3, "MsgRaw", "msgRaw"), b(4, "HasMsg", "hasMsg"), li(5, "Btns", "btns", "UiBtn"), b(6, "InBody", "inBody")},
	},
	{
		name: "DlgTxtExport", goT: "pubTxtDlgSt", zigT: "dialogs_a.TxtExport", id: 103,
		doc: "tracklist text-export dialog",
		fs:  []field{s(1, "Title", "title"), st(2, "Sel", "sel", "SelState"), st(3, "Tmpl", "tmpl", "UiField"), st(4, "Header", "header", "UiToggle"), s(5, "Place", "place"), s(6, "Content", "content"), s(7, "CopyLbl", "copyLbl"), s(8, "CloseLbl", "closeLbl")},
	},
	{
		name: "DlgExportPrev", goT: "pubExpDlgSt", zigT: "dialogs_a.ExportPrev", id: 104,
		doc: "tracklist-export preview (CSV/JSON; also the remote arm)",
		fs:  []field{s(1, "Title", "title"), s(2, "Note", "note"), s(3, "Content", "content"), s(4, "CopyLbl", "copyLbl"), s(5, "CloseLbl", "closeLbl")},
	},
	{
		name: "DlgRename", goT: "pubRenameDlgSt", zigT: "dialogs_a.Rename", id: 105,
		doc: "rename-set form dialog",
		fs:  []field{s(1, "Title", "title"), s(2, "ID", "id"), s(3, "NameLbl", "nameLbl"), s(4, "NameDL", "nameDL"), s(5, "Cur", "cur"), s(6, "Submit", "submit")},
	},
	{
		name: "PubFixRow", goT: "pubFixRowSt", zigT: "dialogs_a.FixRow",
		fs: []field{s(1, "Num", "num"), s(2, "Off", "off"), s(3, "NewOff", "newOff"), b(4, "Removed", "removed"), s(5, "Label", "label")},
	},
	{
		name: "DlgFix", goT: "pubFixDlgSt", zigT: "dialogs_a.Fix", id: 106,
		doc: "capture-aligned time-fix preview",
		fs:  []field{s(1, "Title", "title"), s(2, "Desc", "desc"), b(3, "HasOpener", "hasOpener"), st(4, "Opener", "opener", "SelState"), s(5, "SetStartLbl", "setStartLbl"), s(6, "StartT", "startT"), s(7, "NewT", "newT"), li(8, "Rows", "rows", "PubFixRow"), s(9, "RemovedTx", "removedTx"), s(10, "ApplyLbl", "applyLbl"), s(11, "ApplyAct", "applyAct"), s(12, "CancelLbl", "cancelLbl")},
	},
	{
		name: "DlgPreset", goT: "mpPresetDlgSt", zigT: "dialogs_a.Preset", id: 107,
		doc: "export preset editor",
		fs:  []field{s(1, "Title", "title"), st(2, "IDField", "idField", "LibPBField"), st(3, "LabelField", "labelField", "LibPBField"), b(4, "HasSrc", "hasSrc"), s(5, "SrcHint", "srcHint"), st(6, "Container", "container", "LibSelTip"), b(7, "HasVideo", "hasVideo"), st(8, "VCodec", "vcodec", "LibSelTip"), b(9, "HasVEnc", "hasVEnc"), st(10, "Accel", "accel", "SelState"), st(11, "RateMode", "rateMode", "LibSelTip"), st(12, "RateField", "rateField", "LibPBField"), st(13, "Res", "res", "SelState"), st(14, "FPS", "fps", "LibPBField"), st(15, "ACodec", "acodec", "LibSelTip"), b(16, "HasLadder", "hasLadder"), b(17, "HasVBRTgl", "hasVbrTgl"), st(18, "VBR", "vbr", "UiToggle"), b(19, "HasVBRQ", "hasVbrq"), st(20, "VBRQ", "vbrq", "SelState"), b(21, "HasChips", "hasChips"), s(22, "BitrateLbl", "bitrateLbl"), li(23, "Chips", "chips", "LibChip"), s(24, "MaxHint", "maxHint"), b(25, "HasLossles", "hasLossless"), s(26, "LosslessTx", "losslessTx"), st(27, "Channels", "channels", "SelState"), st(28, "SampleRate", "samplerate", "SelState"), st(29, "Loud", "loud", "Loud"), li(30, "Warns", "warns", "LibHint"), li(31, "Foot", "foot", "UiBtn")},
	},
	{
		name: "CePatRow", goT: "cePatRowSt", zigT: "dialogs_a.PatRow",
		fs: []field{s(1, "ID", "id"), s(2, "Name", "name"), s(3, "Meta", "meta"), b(4, "OwGated", "owGated"), s(5, "OwLbl", "owLbl"), s(6, "OwWhy", "owWhy"), s(7, "DelLbl", "delLbl")},
	},
	{
		name: "DlgPatMgr", goT: "cePatMgrSt", zigT: "dialogs_a.PatMgr", id: 108,
		doc: "manage-patterns dialog",
		fs:  []field{s(1, "Title", "title"), b(2, "Gone", "gone"), s(3, "GoneTx", "goneTx"), b(4, "HasEmpty", "hasEmpty"), s(5, "EmptyTx", "emptyTx"), li(6, "Pats", "pats", "CePatRow"), s(7, "RenameLbl", "renameLbl"), s(8, "Note", "note")},
	},
	{
		name: "DlgField", goT: "dlgFieldSt", zigT: "dialogs_b.DlgField",
		fs: []field{s(1, "Label", "label"), s(2, "DL", "dl"), s(3, "Act", "act"), s(4, "Value", "value"), s(5, "Type", "inputType"), s(6, "PH", "ph"), s(7, "Tip", "tip"), op(8, "TipS", "tipSt", "Tip")},
	},
	{
		name: "AeBlock", goT: "aeBlockSt", zigT: "dialogs_b.AeBlock",
		fs: []field{s(1, "Kind", "kind"), st(2, "Field", "field", "DlgField"), st(3, "Field2", "field2", "DlgField"), st(4, "Btn", "btn", "UiBtn"), st(5, "Toggle", "toggle", "UiToggle"), st(6, "Sel", "sel", "SelState"), st(7, "Sel2", "sel2", "SelState"), s(8, "LabelHTML", "labelHtml"), op(9, "Label", "labelSt", "SsLabel"), s(10, "Tone", "tone"), s(11, "Text", "text"), s(12, "Tip", "tip"), op(13, "TipS", "tipSt", "Tip"), st(14, "Loud", "loud", "Loud")},
	},
	{
		name: "AeStep", goT: "aeStepSt", zigT: "dialogs_b.AeStep",
		fs: []field{s(1, "Title", "title"), li(2, "Trail", "trail", "UiBtn"), s(3, "Desc", "desc"), li(4, "Blocks", "blocks", "AeBlock")},
	},
	{
		name: "AutoEditor", goT: "aeModalSt", zigT: "dialogs_b.AeModal", id: 109,
		doc: "automation-editor dialog",
		fs:  []field{s(1, "Title", "title"), b(2, "HasErr", "hasErr"), s(3, "Err", "err"), li(4, "Ident", "ident", "AeBlock"), s(5, "SecMatch", "secMatch"), li(6, "Match", "match", "AeBlock"), s(7, "SecActions", "secActions"), b(8, "NoSteps", "noSteps"), s(9, "NoStepsMsg", "noStepsMsg"), li(10, "Steps", "steps", "AeStep"), li(11, "Add", "add", "UiBtn"), b(12, "HasVerdict", "hasVerdict"), s(13, "Verdict", "verdict"), s(14, "Save", "save"), s(15, "Cancel", "cancel")},
	},
	{
		name: "ArFoot", goT: "arFootSt", zigT: "dialogs_b.ArFoot",
		fs: []field{b(1, "Gated", "gated"), s(2, "Label", "label"), s(3, "Why", "why"), s(4, "Variant", "variant"), s(5, "Cancel", "cancel")},
	},
	{
		name: "AutoRunNow", goT: "arModalSt", zigT: "dialogs_b.ArModal", id: 110,
		doc: "automation run-now dialog",
		fs:  []field{s(1, "Title", "title"), b(2, "HasErr", "hasErr"), s(3, "Err", "err"), st(4, "Auto", "auto", "UiKV"), st(5, "Watch", "watch", "UiKV"), st(6, "Chain", "chain", "UiKV"), s(7, "IgnoresMatch", "ignoresMatch"), st(8, "File", "file", "DlgField"), st(9, "Browse", "browse", "UiBtn"), b(10, "Erases", "erases"), s(11, "DeleteWarn", "deleteWarn"), s(12, "DeleteScope", "deleteScope"), s(13, "DeleteTip", "deleteTip"), op(14, "DeleteTipS", "deleteTipSt", "Tip"), st(15, "Ack", "ack", "UiToggle"), st(16, "Foot", "foot", "ArFoot")},
	},
	{
		name: "AutoSchedule", goT: "asModalSt", zigT: "dialogs_b.AsModal", id: 111,
		doc: "schedule-editor dialog",
		fs:  []field{s(1, "Title", "title"), b(2, "HasErr", "hasErr"), s(3, "Err", "err"), li(4, "Head", "head", "AeBlock"), s(5, "SecTrigger", "secTrigger"), li(6, "Trigger", "trigger", "AeBlock"), s(7, "SecGates", "secGates"), li(8, "Gates", "gates", "AeBlock"), s(9, "Save", "save"), s(10, "Cancel", "cancel")},
	},
	{
		name: "PubRemRow", goT: "pubRemRowSt", zigT: "publish.RemRow",
		fs: []field{s(1, "ID", "id"), s(2, "Title", "title"), s(3, "Sub", "sub"), b(4, "Sel", "sel")},
	},
	{
		name: "PubRemList", goT: "pubRemListSt", zigT: "publish.RemList",
		fs: []field{s(1, "Empty", "empty"), s(2, "Count", "count"), s(3, "Note", "note"), li(4, "Rows", "rows", "PubRemRow")},
	},
	{
		name: "PubRemTrack", goT: "pubRemTrackSt", zigT: "publish.RemTrack",
		fs: []field{u(1, "Num", "num"), s(2, "Off", "off"), s(3, "Label", "label")},
	},
	{
		name: "PubRemTl", goT: "pubRemTlSt", zigT: "publish.RemTl",
		fs: []field{s(1, "Empty", "empty"), s(2, "Hint", "hint"), s(3, "Note", "note"), li(4, "Rows", "rows", "PubRemTrack")},
	},
	{
		name: "PubRemCaps", goT: "pubRemCapsSt", zigT: "publish.RemCaps",
		fs: []field{s(1, "Hint", "hint"), s(2, "Note", "note"), sl(3, "Caps", "caps")},
	},
	{
		name: "PubRemDetail", goT: "pubRemDetailSt", zigT: "publish.RemDetail",
		fs: []field{s(1, "CardTitle", "cardTitle"), b(2, "Sel", "sel"), s(3, "Hint", "hint"), s(4, "Name", "name"), s(5, "Meta", "meta"), li(6, "Actions", "actions", "UiBtn"), s(7, "Active", "active"), s(8, "CapsLbl", "capsLbl"), s(9, "TracksLbl", "tracksLbl"), st(10, "Tl", "tl", "PubRemTl"), st(11, "Caps", "caps", "PubRemCaps")},
	},
	{
		name: "PublishRemote", goT: "pubRemSt", zigT: "publish.Remote", id: 112,
		doc: "remote Publish view (peer sets + tracklist)",
		fs:  []field{s(1, "Title", "title"), s(2, "Sub", "sub"), s(3, "Switcher", "switcher"), s(4, "Hint", "hint"), st(5, "List", "list", "PubRemList"), st(6, "Detail", "detail", "PubRemDetail")},
	},
	// --- merge composition: tip2 (B-1b shard 2) structured tooltip/label fields ---
	// tip2 flipped the last tipTopic call sites, which added `*tipSt` / `*ssLabelSt` fields to
	// states this block already froze. They are kOptPtr: nil means "no tooltip", and OptStruct
	// keeps a present-but-empty one from decoding as null. Field numbers are appended INSIDE the
	// existing messages (never renumbered), so old documents stay readable.
	{
		name: "SsLabel", goT: "ssLabelSt", zigT: "c.SsLabel",
		fs: []field{s(1, "Text", "text"), op(2, "Tip", "tip", "Tip")},
	},
	// ── phase B3 fragment scheduler (topic sched): the tick surfaces ──
	// Root ids 100-149. The fragment states themselves are the B-2 wire's messages (LiveState /
	// LogsLines) - this block adds only the tick ENVELOPE: the surface state + the hash of what Go
	// last pushed per fragment id. (Pre-merge this block carried its own Tk* mirrors of the live
	// states; wave B-2 defined the same structs as LiveState & co, so the duplicates are gone and
	// the tick documents ride the one canonical set - tooltips fields included.)
	{
		name: "TkPrev", goT: "tickPrev", zigT: "tick.Prev",
		// kUint's user: a dedup hash is not a rendered number, so rule 6 (Go formats every number)
		// does not apply - a 16-char hex string per fragment per tick would be pure waste. (Pre-merge
		// this was an inline field literal; wave B-2 added the u() helper, which the schema-drift
		// scanner can actually see.)
		fs: []field{s(1, "ID", "id"), u(2, "Hash", "hash")},
	},
	{
		name: "TkLive", goT: "liveTickSt", zigT: "tick.LiveBatch", id: 100, retain: true,
		doc: "Live-tab tick surface (all ~1 Hz fragments in one call)",
		fs: []field{st(1, "Live", "live", "LiveState"), s(2, "TC", "tc"),
			li(3, "Prev", "prev", "TkPrev")},
	},
	{
		name: "TkLogs", goT: "logsTickSt", zigT: "tick.LogsBatch", id: 101, retain: true,
		doc: "#log-view tick surface (one fragment, 400-line tail)",
		fs:  []field{st(1, "Lines", "lines", "LogsLines"), li(2, "Prev", "prev", "TkPrev")},
	},
}

// zigImports maps the import alias used in wire_gen.zig to its source file.
var zigImports = [][2]string{
	{"appgroups", "appgroups.zig"},
	{"logs", "logs.zig"},
	{"c", "components.zig"},
	{"peers", "peers.zig"},
	{"automations", "automations.zig"},
	{"player", "player.zig"},
	{"f", "libfixers.zig"},
	{"d", "library_detail.zig"},
	{"s", "library_sections.zig"},
	{"k", "library_kit.zig"},
	{"library", "library.zig"},
	{"sub", "settings_sub.zig"},
	{"settings", "settings.zig"},
	{"publish", "publish.zig"},
	{"motion", "motion.zig"},
	// --- phaseb-wire (B-2 fan-out) ---
	{"live", "live.zig"},
	// --- phaseb-sched ---
	{"tick", "tick.zig"},
	// --- phase B7 fan-out ---
	{"overlays", "overlays.zig"},
	{"twitch", "twitch.zig"},
	{"midictl", "midictl.zig"},
	{"ctls", "midictl_ctls.zig"},
	{"uimap", "midictl_uimap.zig"},
	{"midimon", "midimon.zig"},
	{"dialogs_b", "dialogs_b.zig"},
	{"dialogs_a", "dialogs_a.zig"},
	{"vrchat", "vrchat.zig"},
	{"vrcgroups", "vrcgroups.zig"},
	{"worlds", "worlds.zig"},
	{"editor", "editor.zig"},
	{"cueedit", "cueedit.zig"},
	{"libviews", "libviews.zig"},
	{"libremote", "libremote.zig"},
}

// schemaHash is FNV-1a over the canonical schema text. Both sides embed it; a mismatch means
// the .a and the Go tree were generated from different schemas → the document is rejected.
func schemaHash() uint32 {
	h := fnv.New32a()
	for _, m := range schema {
		fmtWrite(h, m.name, m.goT, m.zigT, itoa(m.id), retainTag(m.retain))
		for _, f := range m.fs {
			fmtWrite(h, itoa(f.num), f.kind.String(), f.ref, f.goF, f.zigF)
		}
	}
	return h.Sum32()
}

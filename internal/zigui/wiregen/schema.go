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
		fs:  []field{s(1, "Title", "title"), s(2, "Sub", "sub"), st(3, "Transport", "transport", "LiveTransport"), st(4, "NP", "np", "LiveNP"), s(5, "StatusTitle", "statusTitle"), st(6, "Status", "status", "LiveStatus"), s(7, "DecksTitle", "decksTitle"), st(8, "Decks", "decks", "LiveDecks"), b(9, "HasSignals", "hasSignals"), s(10, "SignalsTitle", "signalsTitle"), s(11, "SignalsTip", "signalsTip"), st(12, "Signals", "signals", "LiveSignals"), b(13, "HasCockpit", "hasCockpit"), s(14, "CockpitTitle", "cockpitTitle"), st(15, "Cockpit", "cockpit", "LiveCockpit"), b(16, "HasLink", "hasLink"), s(17, "LinkTitle", "linkTitle"), st(18, "Link", "link", "LiveLink"), b(19, "HasNet", "hasNet"), s(20, "NetTitle", "netTitle"), s(21, "NetTip", "netTip"), st(22, "Net", "net", "LiveGraph"), s(23, "TimTitle", "timTitle"), s(24, "TimTip", "timTip"), st(25, "Tim", "tim", "LiveGraph"), b(26, "HasPerf", "hasPerf"), s(27, "PerfTitle", "perfTitle"), s(28, "PerfTip", "perfTip"), st(29, "Perf", "perf", "LivePerf"), st(30, "Strip", "strip", "LiveStrip")},
	},
	// motion: one root - the full view and the #mo-body fragment share moState. Cam/Studio are Go pointers (exactly one section is built per render), so they are optp: presence IS the section switch.
	{
		name: "MoCamRow", goT: "moCamRow", zigT: "motion.CamRow",
		fs: []field{s(1, "Group", "group"), b(2, "ShowGroup", "showGroup"), s(3, "Act", "act"), b(4, "Sel", "sel"), s(5, "Name", "name"), s(6, "Meta", "meta")},
	},
	{
		name: "MoCam", goT: "moCamSt", zigT: "motion.Cam",
		fs: []field{s(1, "Unavailable", "unavailable"), li(2, "Rows", "rows", "MoCamRow"), s(3, "Empty", "empty"), s(4, "ReloadLbl", "reloadLbl"), s(5, "OrganizeLbl", "organizeLbl"), s(6, "DJLbl", "djLbl"), s(7, "PreviewLbl", "previewLbl"), s(8, "Tip", "tip"), s(9, "View", "view"), s(10, "Hint", "hint"), s(11, "Info", "info"), s(12, "PlayBtn", "playBtn"), s(13, "LoadLbl", "loadLbl"), s(14, "CopyLbl", "copyLbl")},
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
		fs: []field{li(1, "Recs", "recs", "MoRecRow"), s(2, "Empty", "empty"), s(3, "RefreshLbl", "refreshLbl"), s(4, "ExportLbl", "exportLbl"), s(5, "RenderLbl", "renderLbl"), s(6, "PCViewLbl", "pcViewLbl"), s(7, "RenderProg", "renderProg"), st(8, "Avatar", "avatar", "MoAvatar"), s(9, "PreviewLbl", "previewLbl"), s(10, "Tip", "tip"), s(11, "View", "view"), s(12, "Hint", "hint"), s(13, "Time", "time"), st(14, "Scrub", "scrub", "MoSlider"), s(15, "PlayLbl", "playLbl"), s(16, "StopLbl", "stopLbl"), st(17, "Loop", "loop", "MoToggle"), st(18, "OSC", "osc", "MoToggle"), st(19, "VMC", "vmc", "MoToggle"), st(20, "Model", "model", "MoToggle"), b(21, "ModelOn", "modelOn"), b(22, "HasDyn", "hasDyn"), s(23, "PhysNote", "physNote"), st(24, "Phys", "phys", "MoToggle"), st(25, "Rest", "rest", "MoToggle"), st(26, "Marks", "marks", "MoToggle"), st(27, "PC", "pc", "MoToggle"), b(28, "PCOn", "pcOn"), st(29, "PCDensity", "pcDensity", "SelState"), st(30, "PCColor", "pcColor", "MoToggle"), s(31, "PCNote", "pcNote"), s(32, "PCExportLbl", "pcExportLbl"), s(33, "VMCHelp", "vmcHelp")},
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
		fs: []field{s(1, "K", "k"), op(2, "Fld", "fld", "UiField"), s(3, "Tip", "tip"), op(4, "TipS", "tipSt", "Tip"), op(5, "Sel", "sel", "SelState"), s(6, "SelLbl", "selLbl"), op(7, "Btn", "btn", "UiBtn")},
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
		name: "UiStatus", goT: "uiStatus", zigT: "c.Status",
		fs: []field{s(1, "Variant", "variant"), s(2, "Label", "label"), s(3, "DL", "dl"), s(4, "Line", "line")},
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
		fs: []field{st(1, "St", "st", "UiStatus"), st(2, "Studio", "studio", "UiToggle"), s(3, "Tip", "tip"), b(4, "HasGate", "hasGate"), s(5, "GateTitle", "gateTitle"), st(6, "Gate", "gate", "BridgeGate")},
	},
	{
		name: "UpdFlow", goT: "updFlowSt", zigT: "sub.UpdFlow",
		fs: []field{s(1, "Kind", "kind"), s(2, "Tone", "tone"), s(3, "Text", "text"), b(4, "HasNotes", "hasNotes"), s(5, "Notes", "notes"), s(6, "Err", "err"), s(7, "Pct", "pct"), s(8, "Cap", "cap"), b(9, "HasBtn", "hasBtn"), st(10, "Btn", "btn", "UiBtn")},
	},
	{
		name: "SetBlock", goT: "setBlock", zigT: "settings.Block",
		fs: []field{s(1, "K", "k"), s(2, "Text", "text"), s(3, "HTML", "html"), s(4, "Tone", "tone"), s(5, "ID", "id"), s(6, "Title", "title"), s(7, "Sub", "sub"), op(8, "Fld", "fld", "UiField"), s(9, "Tip", "tip"), op(10, "TipS", "tipSt", "Tip"), op(11, "Tgl", "tgl", "UiToggle"), s(12, "Gate", "gate"), op(13, "KV", "kv", "UiKV"), op(14, "Sel", "sel", "SelState"), s(15, "SelLbl", "selLbl"), op(16, "Btn", "btn", "UiBtn"), li(17, "Kids", "kids", "SetKid"), li(18, "Inputs", "inputs", "SetInput"), s(19, "Submit", "submit"), s(20, "SubVar", "subVar"), op(21, "GF", "gf", "GfCard"), op(22, "GFM", "gfm", "GfModel"), op(23, "Brg", "brg", "Bridge"), op(24, "Upd", "upd", "UpdFlow")},
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
		name: "LibGFLive", goT: "libGFLiveSt", zigT: "f.GFLive",
		fs: []field{li(1, "Tiles", "tiles", "LibGFTile"), s(2, "Pct", "pct"), s(3, "Caption", "caption"), s(4, "Current", "current")},
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
		fs: []field{st(1, "Sel", "sel", "SelState"), s(2, "Label", "labelHtml")},
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
		fs: []field{b(1, "Compact", "compact"), st(2, "Toggle", "toggle", "UiToggle"), s(3, "Tip", "tip"), s(4, "ChipAct", "chipAct"), li(5, "Chips", "chips", "LoudChip"), st(6, "IField", "iField", "LibPBField"), st(7, "TPField", "tpField", "LibPBField"), st(8, "Raise", "raise", "UiToggle"), b(9, "HasWarn", "hasWarn"), s(10, "Warn", "warn"), s(11, "Extra", "extra")},
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
}

// zigImports maps the import alias used in wire_gen.zig to its source file.
var zigImports = [][2]string{
	{"appgroups", "appgroups.zig"},
	{"logs", "logs.zig"},
	{"c", "components.zig"},
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

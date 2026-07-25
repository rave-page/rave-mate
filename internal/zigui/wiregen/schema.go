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
}

// zigImports maps the import alias used in wire_gen.zig to its source file.
var zigImports = [][2]string{
	{"appgroups", "appgroups.zig"},
	{"logs", "logs.zig"},
	{"c", "components.zig"},
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

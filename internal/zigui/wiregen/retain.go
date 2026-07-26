package main

import (
	"fmt"
	"sort"
	"strings"
)

// Retained-doc delta channel (B7 increment ii) code generation. The stateless RZW1 codec above is
// untouched; this adds, for the messages reachable from a `retain: true` ROOT only:
//
//	Go   hashWire  - state fingerprint (both sides must agree bit for bit)
//	Go   wireEq    - value equality, the delta's change detector
//	Go   deltaWire - emit only changed field trees (absent = KEEP, Clear = back to zero)
//	Zig  merge<X>  - merge a delta into the retained state (strings duped, lists wholesale)
//	Zig  clone<X>  - deep copy into the scratch arena (patch-then-swap needs a private target)
//	Zig  hash<X>   - the mirror of hashWire
//
// Scoped to the closure ON PURPOSE: generating six more walkers for all 347 messages would
// triple the codec for surfaces that never retain anything. Opting a surface in is one `retain:`
// flag plus an export - the closure grows on its own.

// retainTag feeds the retain flag into schemaHash: flipping a surface's opt-in changes the
// generated codec on BOTH sides, so it must change the hash a stale lib is refused by.
func retainTag(v bool) string {
	if v {
		return "retain"
	}
	return "-"
}

// retainRoots are the retain-flagged root messages, id-ordered.
func retainRoots() []msg {
	var out []msg
	for _, m := range schema {
		if m.retain {
			if m.id == 0 {
				fail(fmt.Errorf("%s: retain needs a root id", m.name))
			}
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

// retainClosure is every message reachable from a retain root, in schema order (so the emitted
// code is stable and the generator's output is diffable).
func retainClosure() []msg {
	byName := map[string]msg{}
	for _, m := range schema {
		byName[m.name] = m
	}
	in := map[string]bool{}
	var walk func(m msg)
	walk = func(m msg) {
		if in[m.name] {
			return
		}
		in[m.name] = true
		for _, f := range m.fs {
			switch f.kind {
			case kStruct, kList, kOptPtr, kOptVal:
				walk(byName[f.ref])
			}
		}
	}
	for _, m := range retainRoots() {
		walk(m)
	}
	var out []msg
	for _, m := range schema {
		if in[m.name] {
			out = append(out, m)
		}
	}
	return out
}

// ── Go emitter ──

func emitGoRetain(p func(string, ...any)) {
	cl := retainClosure()
	if len(cl) == 0 {
		return
	}
	p("")
	p("// ── retained-doc delta channel (B7 increment ii; internal/zigui/wiregen/retain.go) ──")
	p("//")
	p("// hashWire/wireEq/deltaWire exist only for the messages reachable from a retain-flagged")
	p("// root. deltaWire's contract: a field it does NOT write keeps the retained value, and a")
	p("// field falling back to its zero value is written as an explicit Clear - which is why")
	p("// these documents (RZD1) can never be read by a stateless export (RZW1 magic).")
	p("")
	p("// wireEqStrs compares two []string fields (kStrList).")
	p("func wireEqStrs(a, b []string) bool {")
	p("\tif len(a) != len(b) {")
	p("\t\treturn false")
	p("\t}")
	p("\tfor i := range a {")
	p("\t\tif a[i] != b[i] {")
	p("\t\t\treturn false")
	p("\t\t}")
	p("\t}")
	p("\treturn true")
	p("}")

	for _, m := range cl {
		// hashWire
		p("")
		p("func (v %s) hashWire(h *zigui.WireHasher) {", m.goT)
		if len(m.fs) == 0 {
			p("\t_, _ = v, h")
		}
		for _, f := range m.fs {
			switch f.kind {
			case kStr, kStrAlways:
				p("\th.Str(%d, v.%s)", f.num, f.goF)
			case kBool:
				p("\th.Bool(%d, v.%s)", f.num, f.goF)
			case kUint:
				p("\th.Uint(%d, uint64(v.%s))", f.num, f.goF)
			case kStruct:
				p("\th.Sub(%d)", f.num)
				p("\tv.%s.hashWire(h)", f.goF)
			case kOptPtr:
				p("\th.Opt(%d, v.%s != nil)", f.num, f.goF)
				p("\tif v.%s != nil {", f.goF)
				p("\t\tv.%s.hashWire(h)", f.goF)
				p("\t}")
			case kOptVal:
				p("\th.Opt(%d, true)", f.num)
				p("\tv.%s.hashWire(h)", f.goF)
			case kList:
				p("\th.List(%d, len(v.%s))", f.num, f.goF)
				p("\tfor i := range v.%s {", f.goF)
				p("\t\tv.%s[i].hashWire(h)", f.goF)
				p("\t}")
			case kStrList:
				p("\th.StrList(%d, v.%s)", f.num, f.goF)
			}
		}
		p("}")

		// wireEq
		p("")
		p("func (v %s) wireEq(o *%s) bool {", m.goT, m.goT)
		if len(m.fs) == 0 {
			p("\t_, _ = v, o")
		}
		for i, f := range m.fs {
			switch f.kind {
			case kStr, kStrAlways, kBool, kUint:
				p("\tif v.%s != o.%s {", f.goF, f.goF)
				p("\t\treturn false")
				p("\t}")
			case kStruct, kOptVal:
				p("\tif !v.%s.wireEq(&o.%s) {", f.goF, f.goF)
				p("\t\treturn false")
				p("\t}")
			case kOptPtr:
				p("\tif (v.%s == nil) != (o.%s == nil) {", f.goF, f.goF)
				p("\t\treturn false")
				p("\t}")
				p("\tif v.%s != nil && !v.%s.wireEq(o.%s) {", f.goF, f.goF, f.goF)
				p("\t\treturn false")
				p("\t}")
			case kList:
				p("\tif len(v.%s) != len(o.%s) {", f.goF, f.goF)
				p("\t\treturn false")
				p("\t}")
				p("\tfor i%d := range v.%s {", i, f.goF)
				p("\t\tif !v.%s[i%d].wireEq(&o.%s[i%d]) {", f.goF, i, f.goF, i)
				p("\t\t\treturn false")
				p("\t\t}")
				p("\t}")
			case kStrList:
				p("\tif !wireEqStrs(v.%s, o.%s) {", f.goF, f.goF)
				p("\t\treturn false")
				p("\t}")
			}
		}
		p("\treturn true")
		p("}")

		// deltaWire
		p("")
		p("func (v %s) deltaWire(w *zigui.WireWriter, prev *%s) {", m.goT, m.goT)
		if len(m.fs) == 0 {
			p("\t_, _, _ = v, w, prev")
		}
		for i, f := range m.fs {
			switch f.kind {
			case kStr:
				p("\tif v.%s != prev.%s {", f.goF, f.goF)
				p("\t\tif v.%s == \"\" {", f.goF)
				p("\t\t\tw.Clear(%d)", f.num)
				p("\t\t} else {")
				p("\t\t\tw.Str(%d, v.%s)", f.num, f.goF)
				p("\t\t}")
				p("\t}")
			case kStrAlways:
				// Never Clear: absence on this field restores the Zig DEFAULT, not "".
				p("\tif v.%s != prev.%s {", f.goF, f.goF)
				p("\t\tw.StrAlways(%d, v.%s)", f.num, f.goF)
				p("\t}")
			case kBool:
				p("\tif v.%s != prev.%s {", f.goF, f.goF)
				p("\t\tif !v.%s {", f.goF)
				p("\t\t\tw.Clear(%d)", f.num)
				p("\t\t} else {")
				p("\t\t\tw.Bool(%d, v.%s)", f.num, f.goF)
				p("\t\t}")
				p("\t}")
			case kUint:
				p("\tif v.%s != prev.%s {", f.goF, f.goF)
				p("\t\tif v.%s == 0 {", f.goF)
				p("\t\t\tw.Clear(%d)", f.num)
				p("\t\t} else {")
				p("\t\t\tw.Uint(%d, uint64(v.%s))", f.num, f.goF)
				p("\t\t}")
				p("\t}")
			case kStruct:
				// Struct drops an empty body, and an empty body is exactly "nothing changed".
				p("\tw.Struct(%d, func() { v.%s.deltaWire(w, &prev.%s) })", f.num, f.goF, f.goF)
			case kOptPtr:
				p("\tswitch {")
				p("\tcase v.%s == nil:", f.goF)
				p("\t\tif prev.%s != nil {", f.goF)
				p("\t\t\tw.Clear(%d)", f.num)
				p("\t\t}")
				p("\tcase prev.%s == nil:", f.goF)
				p("\t\tw.OptStruct(%d, func() { v.%s.encodeWire(w) })", f.num, f.goF)
				p("\tcase !v.%s.wireEq(prev.%s):", f.goF, f.goF)
				p("\t\tw.OptStruct(%d, func() { v.%s.deltaWire(w, prev.%s) })", f.num, f.goF, f.goF)
				p("\t}")
			case kOptVal:
				p("\tif !v.%s.wireEq(&prev.%s) {", f.goF, f.goF)
				p("\t\tw.OptStruct(%d, func() { v.%s.deltaWire(w, &prev.%s) })", f.num, f.goF, f.goF)
				p("\t}")
			case kList:
				// v1 of the channel replaces a changed list WHOLESALE (no per-element splicing):
				// elements ride the full encoder, which is what the Zig merge zero-inits for.
				p("\tchg%d := len(v.%s) != len(prev.%s)", i, f.goF, f.goF)
				p("\tfor i%d := 0; !chg%d && i%d < len(v.%s); i%d++ {", i, i, i, f.goF, i)
				p("\t\tchg%d = !v.%s[i%d].wireEq(&prev.%s[i%d])", i, f.goF, i, f.goF, i)
				p("\t}")
				p("\tif chg%d {", i)
				p("\t\tif len(v.%s) == 0 {", f.goF)
				p("\t\t\tw.Clear(%d)", f.num)
				p("\t\t} else {")
				p("\t\t\tw.List(%d, len(v.%s), func(i int) { v.%s[i].encodeWire(w) })", f.num, f.goF, f.goF)
				p("\t\t}")
				p("\t}")
			case kStrList:
				p("\tif !wireEqStrs(v.%s, prev.%s) {", f.goF, f.goF)
				p("\t\tif len(v.%s) == 0 {", f.goF)
				p("\t\t\tw.Clear(%d)", f.num)
				p("\t\t} else {")
				p("\t\t\tw.StrList(%d, v.%s)", f.num, f.goF)
				p("\t\t}")
				p("\t}")
			}
		}
		p("}")
	}

	for _, m := range retainRoots() {
		p("")
		p("// hash%s fingerprints %s for the slot guard (== the Zig hash%s walk).", m.name, m.goT, m.name)
		p("func hash%s(v %s) uint64 {", m.name, m.goT)
		p("\th := zigui.NewWireHasher()")
		p("\tv.hashWire(h)")
		p("\ts, _ := h.Sum()")
		p("\treturn s")
		p("}")
		p("")
		p("// seed%s encodes %s as an RZD1 SEED document (full state; re-seeds the slot).", m.name, m.goT)
		p("func seed%s(v %s, handle uint64, loc uint32) []byte {", m.name, m.goT)
		p("\tw := zigui.NewDeltaWriter(wireMsg%s, wireSchemaHash, zigui.DeltaKindSeed, handle, 0, hash%s(v), loc)", m.name, m.name)
		p("\tv.encodeWire(w)")
		p("\treturn w.FinishDelta()")
		p("}")
		p("")
		p("// delta%s encodes what changed between prev and v as an RZD1 DELTA document. nil = the", m.name)
		p("// encoder refused (over-size); an EMPTY body means nothing changed and the caller skips")
		p("// the ABI call entirely.")
		p("func delta%s(v, prev %s, handle, base uint64, loc uint32) ([]byte, bool) {", m.name, m.goT)
		p("\tw := zigui.NewDeltaWriter(wireMsg%s, wireSchemaHash, zigui.DeltaKindDelta, handle, base, hash%s(v), loc)", m.name, m.name)
		p("\tv.deltaWire(w, &prev)")
		p("\tif w.Empty() {")
		p("\t\treturn nil, false")
		p("\t}")
		p("\treturn w.FinishDelta(), true")
		p("}")
	}
}

// ── Zig emitter ──

func emitZigRetain(p func(string, ...any)) {
	cl := retainClosure()
	if len(cl) == 0 {
		return
	}
	p("")
	p("// ── retained-doc delta channel (B7 increment ii) ──")
	p("// merge/clone/hash for the messages reachable from a retain-flagged root. merge treats an")
	p("// absent field as KEEP and wire.clear_field as \"back to the zero value\"; strings are DUPED")
	p("// into the reader's allocator (the slot's scratch arena) because a retained state outlives")
	p("// the document that produced it.")

	for _, m := range cl {
		// merge
		p("")
		p("pub fn merge%s(r: *wire.Reader, out: *%s) wire.Error!void {", m.name, m.zigT)
		if len(m.fs) == 0 {
			p("    _ = out;")
			p("    while (try r.next()) |t| try r.skip(t);")
			p("}")
		} else {
			p("    while (try r.next()) |t| switch (t.field) {")
			p("        wire.clear_field => switch (try r.uint(t)) {")
			for _, f := range m.fs {
				switch f.kind {
				case kStr:
					p("            %d => out.%s = \"\",", f.num, f.zigF)
				case kBool:
					p("            %d => out.%s = false,", f.num, f.zigF)
				case kUint:
					p("            %d => out.%s = 0,", f.num, f.zigF)
				case kStruct:
					p("            %d => out.%s = .{},", f.num, f.zigF)
				case kList, kStrList:
					p("            %d => out.%s = &.{},", f.num, f.zigF)
				case kOptPtr, kOptVal:
					p("            %d => out.%s = null,", f.num, f.zigF)
				case kStrAlways:
					// no clear arm: absence restores the Zig default, so Go never clears it
				}
			}
			p("            else => {},")
			p("        },")
			for _, f := range m.fs {
				switch f.kind {
				case kStr, kStrAlways:
					p("        %d => out.%s = try wire.strDup(r, t),", f.num, f.zigF)
				case kBool:
					p("        %d => out.%s = try r.boolean(t),", f.num, f.zigF)
				case kUint:
					p("        %d => out.%s = @intCast(try r.uint(t)),", f.num, f.zigF)
				case kStruct:
					p("        %d => try wire.mergeSub(r, %s, merge%s, t, &out.%s),", f.num, refZig(f.ref), f.ref, f.zigF)
				case kList:
					p("        %d => out.%s = try r.list(%s, merge%s, t),", f.num, f.zigF, refZig(f.ref), f.ref)
				case kStrList:
					p("        %d => out.%s = try wire.strListDup(r, t),", f.num, f.zigF)
				case kOptPtr, kOptVal:
					p("        %d => {", f.num)
					p("            if (out.%s == null) out.%s = .{};", f.zigF, f.zigF)
					p("            try wire.mergeSub(r, %s, merge%s, t, &out.%s.?);", refZig(f.ref), f.ref, f.zigF)
					p("        },")
				}
			}
			p("        else => try r.skip(t),")
			p("    };")
			p("}")
		}

		// clone
		var cln []string
		for _, f := range m.fs {
			switch f.kind {
			case kStr, kStrAlways:
				cln = append(cln, fmt.Sprintf("    out.%s = try a.dupe(u8, v.%s);", f.zigF, f.zigF))
			case kStruct:
				cln = append(cln, fmt.Sprintf("    out.%s = try clone%s(a, v.%s);", f.zigF, f.ref, f.zigF))
			case kList:
				cln = append(cln, fmt.Sprintf("    out.%s = try wire.cloneList(%s, clone%s, a, v.%s);", f.zigF, refZig(f.ref), f.ref, f.zigF))
			case kStrList:
				cln = append(cln, fmt.Sprintf("    out.%s = try wire.cloneStrList(a, v.%s);", f.zigF, f.zigF))
			case kOptPtr, kOptVal:
				cln = append(cln, fmt.Sprintf("    if (v.%s) |x| { out.%s = try clone%s(a, x); }", f.zigF, f.zigF, f.ref))
			}
		}
		p("")
		p("pub fn clone%s(a: std.mem.Allocator, v: %s) wire.Error!%s {", m.name, m.zigT, m.zigT)
		if len(cln) == 0 {
			p("    _ = a;")
			p("    return v;")
		} else {
			p("    var out = v;")
			for _, l := range cln {
				p("%s", l)
			}
			p("    return out;")
		}
		p("}")

		// hash
		p("")
		p("pub fn hash%s(h: *wire.Hasher, v: %s) void {", m.name, m.zigT)
		if len(m.fs) == 0 {
			p("    _ = h;")
			p("    _ = v;")
		}
		for _, f := range m.fs {
			switch f.kind {
			case kStr, kStrAlways:
				p("    h.str(%d, v.%s);", f.num, f.zigF)
			case kBool:
				p("    h.boolean(%d, v.%s);", f.num, f.zigF)
			case kUint:
				p("    h.uint(%d, wire.u64of(v.%s));", f.num, f.zigF)
			case kStruct:
				p("    h.sub(%d);", f.num)
				p("    hash%s(h, v.%s);", f.ref, f.zigF)
			case kOptPtr, kOptVal:
				p("    h.opt(%d, v.%s != null);", f.num, f.zigF)
				p("    if (v.%s) |x| hash%s(h, x);", f.zigF, f.ref)
			case kList:
				p("    h.list(%d, v.%s.len);", f.num, f.zigF)
				p("    for (v.%s) |e| hash%s(h, e);", f.zigF, f.ref)
			case kStrList:
				p("    h.strList(%d, v.%s);", f.num, f.zigF)
			}
		}
		p("}")
	}
}

// retainNames lists the retain roots as "name id" for the generator's stdout line.
func retainNames() string {
	var out []string
	for _, m := range retainRoots() {
		out = append(out, fmt.Sprintf("%s(%d)", m.name, m.id))
	}
	return strings.Join(out, " ")
}

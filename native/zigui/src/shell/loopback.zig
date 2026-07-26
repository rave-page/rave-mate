//! Loopback page model - `procInit.virtual` (test transport fixture, never production). Byte-for-
//! byte behavioral port of internal/webui/shell_proc_loopback.go: no window, no WebView2, scripted
//! ctl answers from a `<!--LBFIX {json}-->` fixture in the loaded document, applied through a
//! bounded async queue that models the window's UI thread. It string-scans the daemon's own
//! emitters (__patch / window.rave / __rave_evalResult) and NEVER executes JS.

const std = @import("std");
const wire = @import("wire.zig");
const sync = @import("sync.zig");

/// Apply-queue cap (Go lbApplyQueueCap): past it the caller BLOCKS - a child that cannot keep up
/// must stop consuming stdin, never accumulate without bound.
pub const apply_queue_cap = 4096;

pub const Mode = enum { normal, deaf, slow };

const Item = struct {
    doc: bool,
    s: []u8, // gpa-owned; freed by the applier
};

pub const Loopback = struct {
    gpa: std.mem.Allocator,
    em: *wire.Emitter,
    mode: Mode,

    mu: sync.Lock = .{},
    cond: sync.Cond = .{},
    queue: std.ArrayList(Item) = .empty, // FIFO; bounded apply_queue_cap
    done: bool = false,

    fix_snapshot: []u8 = &.{},
    fix_reads: std.StringHashMapUnmanaged([]u8) = .empty, // lowercased key -> value
    fix_clicks: std.ArrayList([]u8) = .empty,
    frags: std.StringHashMapUnmanaged([]u8) = .empty, // fragment id -> last __patch html

    thread: ?std.Thread = null,

    pub fn init(gpa: std.mem.Allocator, em: *wire.Emitter, mode: Mode) Loopback {
        return .{ .gpa = gpa, .em = em, .mode = mode };
    }

    /// run applies the initial document synchronously (like SetHtml before the loop), then starts
    /// the applier thread (the model of the UI thread).
    pub fn run(lb: *Loopback, initial_html: []const u8) !void {
        lb.applyDoc(initial_html);
        lb.thread = try std.Thread.spawn(.{}, applier, .{lb});
    }

    pub fn terminate(lb: *Loopback) void {
        lb.mu.lock();
        lb.done = true;
        lb.cond.broadcast();
        lb.mu.unlock();
        if (lb.thread) |t| {
            t.join();
            lb.thread = null;
        }
    }

    /// setHTML / eval enqueue one item; deaf mode blocks the CALLER (the child's stdin reader) -
    /// that is a child that stopped consuming its stdin.
    pub fn setHTML(lb: *Loopback, html: []const u8) void {
        lb.enqueue(true, html);
    }

    pub fn eval(lb: *Loopback, js: []const u8) void {
        lb.enqueue(false, js);
    }

    /// post replays a Go-originated act inline (Go loopbackWindow.post -> onAction).
    pub fn post(lb: *Loopback, payload: []const u8) void {
        lb.em.event("action", .{ .payload = payload });
    }

    fn enqueue(lb: *Loopback, doc: bool, s: []const u8) void {
        if (lb.mode == .deaf) {
            while (true) sync.sleepMs(3_600_000); // wedged consumer
        }
        const copy = lb.gpa.dupe(u8, s) catch return;
        lb.mu.lock();
        defer lb.mu.unlock();
        while (lb.queue.items.len >= apply_queue_cap and !lb.done) lb.cond.wait(&lb.mu);
        if (lb.done) {
            lb.gpa.free(copy);
            return;
        }
        lb.queue.append(lb.gpa, .{ .doc = doc, .s = copy }) catch {
            lb.gpa.free(copy);
            return;
        };
        lb.cond.broadcast();
    }

    /// applier drains items one at a time, in arrival order.
    fn applier(lb: *Loopback) void {
        while (true) {
            lb.mu.lock();
            while (lb.queue.items.len == 0 and !lb.done) lb.cond.wait(&lb.mu);
            if (lb.done and lb.queue.items.len == 0) {
                lb.mu.unlock();
                return;
            }
            const it = lb.queue.orderedRemove(0);
            lb.cond.broadcast();
            lb.mu.unlock();
            if (lb.mode == .slow) sync.sleepMs(2); // busy UI thread
            if (it.doc) lb.applyDoc(it.s) else lb.applyEval(it.s);
            lb.gpa.free(it.s);
        }
    }

    fn applyDoc(lb: *Loopback, html: []const u8) void {
        lb.mu.lock();
        var it = lb.frags.iterator();
        while (it.next()) |e| {
            lb.gpa.free(e.key_ptr.*);
            lb.gpa.free(e.value_ptr.*);
        }
        lb.frags.clearRetainingCapacity();
        lb.mu.unlock();
        lb.parseFixture(html);
    }

    /// applyEval "runs" one script: applies every __patch, replays every window.rave() act, and
    /// answers every __rave_evalResult call.
    fn applyEval(lb: *Loopback, js: []const u8) void {
        var i: usize = 0;
        while (findFrom(js, i, "window.__patch(")) |k| {
            i = k + "window.__patch(".len;
            const id = lbString(js[i..]) orelse continue;
            const rest = js[i + id.consumed ..];
            if (!std.mem.startsWith(u8, rest, ",")) continue;
            const html = lbString(rest[1..]) orelse continue;
            lb.storeFrag(id.value.slice(js[i..]), html.value.slice(rest[1..]));
            id.value.free(lb.gpa);
            html.value.free(lb.gpa);
            i += id.consumed + 1 + html.consumed;
        }
        i = 0;
        while (findFrom(js, i, "window.rave(")) |k| {
            i = k + "window.rave(".len;
            const v = lbString(js[i..]) orelse continue;
            defer v.value.free(lb.gpa);
            lb.em.event("action", .{ .payload = v.value.slice(js[i..]) });
            i += v.consumed;
        }
        i = 0;
        while (findFrom(js, i, "__rave_evalResult(")) |k| {
            i = k + "__rave_evalResult(".len;
            const id = lbString(js[i..]) orelse continue;
            const id_text = id.value.slice(js[i..]);
            i += id.consumed;
            if (!std.mem.startsWith(u8, js[i..], ",")) {
                id.value.free(lb.gpa);
                continue;
            }
            if (lbString(js[i + 1 ..])) |lit| {
                // literal second arg = the ordered-lane ack; forwarded verbatim
                lb.em.event("evalres", .{ .id = id_text, .result = lit.value.slice(js[i + 1 ..]) });
                lit.value.free(lb.gpa);
                i += 1 + lit.consumed;
            } else {
                const inner = lbInner(js);
                lb.answer(id_text, inner);
            }
            id.value.free(lb.gpa);
        }
    }

    fn storeFrag(lb: *Loopback, id: []const u8, html: []const u8) void {
        lb.mu.lock();
        defer lb.mu.unlock();
        if (lb.frags.getEntry(id)) |e| {
            lb.gpa.free(e.value_ptr.*);
            e.value_ptr.* = lb.gpa.dupe(u8, html) catch return;
            return;
        }
        const k = lb.gpa.dupe(u8, id) catch return;
        const v = lb.gpa.dupe(u8, html) catch {
            lb.gpa.free(k);
            return;
        };
        lb.frags.put(lb.gpa, k, v) catch {
            lb.gpa.free(k);
            lb.gpa.free(v);
        };
    }

    /// answer produces the JSON the page would have returned for one ctl primitive and emits it.
    fn answer(lb: *Loopback, id: []const u8, inner: []const u8) void {
        var buf: std.Io.Writer.Allocating = .init(lb.gpa);
        defer buf.deinit();
        lb.mu.lock();
        if (std.mem.indexOf(u8, inner, "window.__snapshot") != null) {
            writeJSONString(&buf.writer, lb.fix_snapshot);
        } else if (std.mem.indexOf(u8, inner, "window.__click(") != null) {
            const q = lbFirstArgLower(lb.gpa, inner, "window.__click(");
            defer if (q) |s| lb.gpa.free(s);
            var hit = false;
            if (q != null and q.?.len > 0) {
                for (lb.fix_clicks.items) |c| {
                    if (containsLower(lb.gpa, c, q.?)) {
                        hit = true;
                        break;
                    }
                }
            }
            buf.writer.writeAll(if (hit) "true" else "false") catch {};
        } else if (std.mem.indexOf(u8, inner, "window.__read(") != null) {
            const q = lbFirstArgLower(lb.gpa, inner, "window.__read(");
            defer if (q) |s| lb.gpa.free(s);
            if (q != null) {
                if (lb.fix_reads.get(q.?)) |v| {
                    writeJSONString(&buf.writer, v);
                } else buf.writer.writeAll("null") catch {};
            } else buf.writer.writeAll("null") catch {};
        } else if (std.mem.indexOf(u8, inner, "window.__set(") != null) {
            const q = lbFirstArgLower(lb.gpa, inner, "window.__set(");
            defer if (q) |s| lb.gpa.free(s);
            const ok = q != null and lb.fix_reads.get(q.?) != null;
            buf.writer.writeAll(if (ok) "true" else "false") catch {};
        } else if (std.mem.indexOf(u8, inner, "window.__type(") != null or
            std.mem.indexOf(u8, inner, "window.__tap(") != null or
            std.mem.indexOf(u8, inner, "window.__ctx(") != null)
        {
            buf.writer.writeAll("true") catch {};
        } else {
            buf.writer.writeAll("null") catch {};
        }
        lb.mu.unlock();
        lb.em.event("evalres", .{ .id = id, .result = buf.writer.buffered() });
    }

    /// parseFixture reads the `<!--LBFIX {json}-->` comment (absent = keep the previous fixture).
    fn parseFixture(lb: *Loopback, html: []const u8) void {
        const mark = "<!--LBFIX ";
        const k = std.mem.indexOf(u8, html, mark) orelse return;
        const rest = html[k + mark.len ..];
        const e = std.mem.indexOf(u8, rest, "-->") orelse return;
        const Fx = struct {
            snapshot: []const u8 = "",
            reads: std.json.ArrayHashMap([]const u8) = .{},
            clicks: []const []const u8 = &.{},
        };
        var arena_state: std.heap.ArenaAllocator = .init(lb.gpa);
        defer arena_state.deinit();
        const fx = std.json.parseFromSliceLeaky(Fx, arena_state.allocator(), rest[0..e], .{
            .ignore_unknown_fields = true,
        }) catch return;

        lb.mu.lock();
        defer lb.mu.unlock();
        lb.gpa.free(lb.fix_snapshot);
        lb.fix_snapshot = lb.gpa.dupe(u8, fx.snapshot) catch &.{};
        var it = lb.fix_reads.iterator();
        while (it.next()) |en| {
            lb.gpa.free(en.key_ptr.*);
            lb.gpa.free(en.value_ptr.*);
        }
        lb.fix_reads.clearRetainingCapacity();
        var rit = fx.reads.map.iterator();
        while (rit.next()) |en| {
            const key = std.ascii.allocLowerString(lb.gpa, en.key_ptr.*) catch continue;
            const val = lb.gpa.dupe(u8, en.value_ptr.*) catch {
                lb.gpa.free(key);
                continue;
            };
            lb.fix_reads.put(lb.gpa, key, val) catch {
                lb.gpa.free(key);
                lb.gpa.free(val);
            };
        }
        for (lb.fix_clicks.items) |c| lb.gpa.free(c);
        lb.fix_clicks.clearRetainingCapacity();
        for (fx.clicks) |c| {
            const copy = lb.gpa.dupe(u8, c) catch continue;
            lb.fix_clicks.append(lb.gpa, copy) catch lb.gpa.free(copy);
        }
    }
};

fn findFrom(hay: []const u8, from: usize, needle: []const u8) ?usize {
    if (from >= hay.len) return null;
    const k = std.mem.indexOf(u8, hay[from..], needle) orelse return null;
    return from + k;
}

/// Decoded leading string literal: value + bytes consumed. The value either borrows the input
/// (single-quoted, no escapes needed after replace - we always allocate for simplicity) or owns
/// an allocation; DecodedString tracks which.
const DecodedString = struct {
    owned: ?[]u8, // null = borrow range [1..consumed-1] of the input
    start: usize,
    end: usize,

    fn slice(v: DecodedString, input: []const u8) []const u8 {
        return v.owned orelse input[v.start..v.end];
    }

    fn free(v: DecodedString, gpa: std.mem.Allocator) void {
        if (v.owned) |o| gpa.free(o);
    }
};

const Decoded = struct {
    value: DecodedString,
    consumed: usize,
};

var decode_gpa: ?std.mem.Allocator = null;

/// setAllocator wires the allocator lbString uses for escaped literals (process-wide, set once).
pub fn setAllocator(gpa: std.mem.Allocator) void {
    decode_gpa = gpa;
}

/// lbString decodes a leading JS/JSON string literal (jsQuote output, or dispatchEvals' ack's
/// single-quoted literal). Port of Go lbString. Returns null when s does not start with one.
fn lbString(s: []const u8) ?Decoded {
    if (s.len == 0) return null;
    const gpa = decode_gpa orelse return null;
    switch (s[0]) {
        '"' => {
            var i: usize = 1;
            while (i < s.len) : (i += 1) {
                if (s[i] == '\\') {
                    i += 1;
                    continue;
                }
                if (s[i] == '"') {
                    var arena_state: std.heap.ArenaAllocator = .init(gpa);
                    defer arena_state.deinit();
                    const v = std.json.parseFromSliceLeaky([]const u8, arena_state.allocator(), s[0 .. i + 1], .{}) catch return null;
                    const owned = gpa.dupe(u8, v) catch return null;
                    return .{ .value = .{ .owned = owned, .start = 0, .end = 0 }, .consumed = i + 1 };
                }
            }
        },
        '\'' => {
            var i: usize = 1;
            while (i < s.len) : (i += 1) {
                if (s[i] == '\\') {
                    i += 1;
                    continue;
                }
                if (s[i] == '\'') {
                    // ReplaceAll(`\'`, `'`)
                    const raw = s[1..i];
                    if (std.mem.indexOf(u8, raw, "\\'") == null) {
                        return .{ .value = .{ .owned = null, .start = 1, .end = i }, .consumed = i + 1 };
                    }
                    const size = std.mem.replacementSize(u8, raw, "\\'", "'");
                    const out = gpa.alloc(u8, size) catch return null;
                    _ = std.mem.replace(u8, raw, "\\'", "'", out);
                    return .{ .value = .{ .owned = out, .start = 0, .end = 0 }, .consumed = i + 1 };
                }
            }
        },
        else => {},
    }
    return null;
}

/// lbInner pulls the ctl script out of control.go's async wrapper.
fn lbInner(js: []const u8) []const u8 {
    const open = "var r=await (async()=>{";
    const a = std.mem.indexOf(u8, js, open) orelse return "";
    const rest = js[a + open.len ..];
    const b = std.mem.indexOf(u8, rest, "})();window.__rave_evalResult(") orelse return "";
    return rest[0..b];
}

/// lbFirstArgLower decodes the first string argument of the call at marker, lowercased (allocated).
fn lbFirstArgLower(gpa: std.mem.Allocator, js: []const u8, marker: []const u8) ?[]u8 {
    const k = std.mem.indexOf(u8, js, marker) orelse return null;
    const d = lbString(js[k + marker.len ..]) orelse return null;
    defer d.value.free(gpa);
    return std.ascii.allocLowerString(gpa, d.value.slice(js[k + marker.len ..])) catch null;
}

/// containsLower reports needle_lower occurs in lower(hay).
fn containsLower(gpa: std.mem.Allocator, hay: []const u8, needle_lower: []const u8) bool {
    const low = std.ascii.allocLowerString(gpa, hay) catch return false;
    defer gpa.free(low);
    return std.mem.indexOf(u8, low, needle_lower) != null;
}

/// writeJSONString writes s as a JSON string literal (Go lbJSON/jsQuote parity).
fn writeJSONString(w: *std.Io.Writer, s: []const u8) void {
    var st: std.json.Stringify = .{ .writer = w };
    st.write(s) catch {};
}

// ── tests ────────────────────────────────────────────────────────────────────

test "lbString decodes json and single-quoted literals" {
    setAllocator(std.testing.allocator);
    {
        const d = lbString("\"abc\\\"x\",rest").?;
        defer d.value.free(std.testing.allocator);
        try std.testing.expectEqualStrings("abc\"x", d.value.slice("\"abc\\\"x\",rest"));
        try std.testing.expectEqual(@as(usize, 9), d.consumed);
    }
    {
        const in = "'1');tail";
        const d = lbString(in).?;
        defer d.value.free(std.testing.allocator);
        try std.testing.expectEqualStrings("1", d.value.slice(in));
        try std.testing.expectEqual(@as(usize, 3), d.consumed);
    }
    try std.testing.expect(lbString("nope") == null);
}

test "lbInner extracts the wrapped ctl script" {
    const js = "(async()=>{try{var r=await (async()=>{return window.__snapshot();})();window.__rave_evalResult(\"e1\",JSON.stringify(r));}catch(e){}})();";
    try std.testing.expectEqualStrings("return window.__snapshot();", lbInner(js));
}

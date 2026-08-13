//! rave-shell - Zig-owned PSH1 window child (ZIG_MIGRATION phase B6). Drop-in replacement for the
//! Go `rave-mate feature webview` child behind the byte-identical PSH1 contract (ZIG_UI_GUIDE.md
//! "Phase B - B5 procShell protocol"): featurehost newline-JSON stdio, init/stop handshake,
//! doc/eval/xeval/act/resize/show/quit/streaming/screenshot events in, ready/evalres/action/win/
//! gone/shotres/heartbeat/log events out. Pure VIEW + INPUT TRANSPORT: reads no config, opens no
//! database, holds no identity - everything arrives in init, everything learned leaves as an event.
//! Opt-in from the daemon via features.ui.shellImpl="zig" (shell_zig.go); the Go child stays the
//! default until parity soaks.
//!
//! Test rig parity (shell_proc_test.go): procInit.virtual selects the loopback page model, and
//! RAVE_MATE_WEBVIEW_TEST_MODE ∈ {deaf,crash,slow} reproduces the wedged/crashed/busy children the
//! B5 shutdown gates run against.
//!
//! Bounded buffers: frame line ≤ 64 MiB (oversize = fatal, exit 1 - the daemon's doc frames are
//! MBs at most); loopback apply queue 4096 (blocks = stops consuming stdin); act queue 64
//! (drop-on-full, cgoShell parity).

const std = @import("std");
const builtin = @import("builtin");
const wire = @import("wire.zig");
const sync = @import("sync.zig");
const loopback = @import("loopback.zig");
const winshell = @import("winshell.zig");

const max_frame_line: usize = 64 << 20;
const default_quit_grace_ms: u32 = 1500; // childForceExitGrace

extern "kernel32" fn ExitProcess(u32) callconv(.winapi) noreturn;
extern "kernel32" fn SetPriorityClass(?*anyopaque, u32) callconv(.winapi) i32;
extern "kernel32" fn GetCurrentProcess() callconv(.winapi) ?*anyopaque;

const Frame = struct {
    id: []const u8 = "",
    method: []const u8 = "",
    params: std.json.Value = .null,
    event: []const u8 = "",
    data: std.json.Value = .null,
};

const ProcInit = struct {
    title: []const u8 = "rave-mate",
    w: i64 = 1280,
    h: i64 = 820,
    startHidden: bool = false,
    allowGpu: bool = false,
    shellHosting: []const u8 = "", // ""/"windowed" | "visual" (DirectComposition visual hosting)
    dataDir: []const u8 = "",
    runtimeJs: []const u8 = "",
    initialHtml: []const u8 = "",
    mediaOrigin: []const u8 = "",
    mediaSession: []const u8 = "",
    streaming: bool = false,
    virtual: bool = false,
};

/// Binding shim injected BEFORE the daemon's runtimeJS: window.rave / window.__rave_evalResult
/// post {"m":..} through the WebView2 web-message channel (the cgo child's webview bindings,
/// re-homed). The page's scripts are the daemon's own; the shim only transports.
const binding_shim =
    "(function(){var pm=function(o){try{window.chrome.webview.postMessage(JSON.stringify(o))}catch(e){}};" ++
    "window.rave=function(p){pm({m:'a',p:''+p})};" ++
    "window.__rave_evalResult=function(i,r){pm({m:'r',i:''+i,r:(r===void 0||r===null)?'':''+r})};})();\n";

const Child = struct {
    gpa: std.mem.Allocator,
    em: *wire.Emitter,
    mode: loopback.Mode,
    inited: bool = false,
    virtual: bool = false,
    lb: ?*loopback.Loopback = null,
    sh: ?*winshell.Shell = null,
    stream_prio: u8 = 255, // virtual-mode dedup for SetPriorityClass
};

pub fn main(init: std.process.Init) !void {
    if (builtin.os.tag != .windows) {
        @compileError("rave-shell is Windows-only (WebView2)");
    }
    const gpa = init.gpa;
    // Before any window: claim the app's taskbar identity, else Windows derives one from this
    // exe's (hash-stamped) path and the window lands in its own taskbar button instead of the
    // pinned rave-mate one.
    winshell.setAppIdentity();
    var em = wire.Emitter.init(gpa);
    loopback.setAllocator(gpa);

    var mode: loopback.Mode = .normal;
    if (init.environ_map.get("RAVE_MATE_WEBVIEW_TEST_MODE")) |m| {
        if (std.mem.eql(u8, m, "deaf")) mode = .deaf;
        if (std.mem.eql(u8, m, "slow")) mode = .slow;
        if (std.mem.eql(u8, m, "crash")) {
            _ = try std.Thread.spawn(.{}, crashSoon, .{});
        }
    }
    var child: Child = .{ .gpa = gpa, .em = &em, .mode = mode };

    var in_buf: [64 * 1024]u8 = undefined;
    var stdin_reader = std.Io.File.stdin().readerStreaming(init.io, &in_buf);
    const in = &stdin_reader.interface;
    var line: std.Io.Writer.Allocating = .init(gpa);
    defer line.deinit();

    while (true) {
        line.writer.end = 0;
        _ = in.streamDelimiter(&line.writer, '\n') catch |err| switch (err) {
            error.EndOfStream => break, // daemon gone -> wind down quietly (Go die(0) parity)
            else => break,
        };
        _ = in.takeByte() catch break; // consume the delimiter
        if (line.writer.end > max_frame_line) {
            wire.errLine("rave-shell: frame too large");
            ExitProcess(1);
        }
        const trimmed = std.mem.trim(u8, line.writer.buffered(), " \t\r");
        if (trimmed.len == 0) continue;
        serveFrame(&child, trimmed);
    }
    shutdown(&child, null);
}

fn crashSoon() void {
    sync.sleepMs(1200); // procCrashAfter: past init+ready, before the gates
    ExitProcess(3);
}

fn serveFrame(c: *Child, linev: []const u8) void {
    var arena_state: std.heap.ArenaAllocator = .init(c.gpa);
    defer arena_state.deinit();
    const arena = arena_state.allocator();
    const fr = std.json.parseFromSliceLeaky(Frame, arena, linev, .{ .ignore_unknown_fields = true }) catch {
        wire.errLine("rave-shell: malformed frame");
        return;
    };
    if (fr.event.len > 0) {
        if (c.inited) handleEvent(c, arena, fr.event, fr.data);
        return;
    }
    if (std.mem.eql(u8, fr.method, "init")) {
        if (c.inited) {
            c.em.respond(fr.id, "already initialized");
            return;
        }
        handleInit(c, arena, fr) catch |err| {
            c.em.respond(fr.id, @errorName(err));
            ExitProcess(1);
        };
        c.inited = true;
        c.em.respond(fr.id, null); // init OK = ready (window bring-up is async, like Start)
        return;
    }
    if (std.mem.eql(u8, fr.method, "stop")) {
        c.em.respond(fr.id, null);
        shutdown(c, null);
        return;
    }
    if (fr.method.len > 0) {
        c.em.respond(fr.id, "unknown method"); // PSH1 is event-only past init/stop
    }
}

fn handleInit(c: *Child, arena: std.mem.Allocator, fr: Frame) !void {
    const ini = try std.json.parseFromValueLeaky(ProcInit, arena, fr.params, .{ .ignore_unknown_fields = true });
    if (ini.runtimeJs.len == 0 and !ini.virtual) {
        // The wire bytes ARE the document-start runtime (byte-contracted with the daemon's
        // renderers); this child has no compiled-in copy at all.
        return error.NoRuntimeJS;
    }
    c.virtual = ini.virtual;
    if (ini.virtual) {
        const lb = try c.gpa.create(loopback.Loopback);
        lb.* = loopback.Loopback.init(c.gpa, c.em, c.mode);
        try lb.run(ini.initialHtml);
        c.lb = lb;
        applyVirtualPriority(c, ini.streaming);
        c.em.event("ready", .{ .hwnd = 0, .virtual = true });
        return;
    }
    const sh = try c.gpa.create(winshell.Shell);
    sh.* = .{
        .gpa = c.gpa,
        .em = c.em,
        .title = try c.gpa.dupe(u8, ini.title),
        .init_w = @intCast(@max(1, ini.w)),
        .init_h = @intCast(@max(1, ini.h)),
        .start_hidden = ini.startHidden,
        .allow_gpu = ini.allowGpu,
        .visual_hosting = std.mem.eql(u8, ini.shellHosting, "visual"),
        .data_dir = try c.gpa.dupe(u8, ini.dataDir),
        .boot_js = try std.mem.concat(c.gpa, u8, &.{ binding_shim, ini.runtimeJs }),
        .initial_html = try c.gpa.dupe(u8, ini.initialHtml),
    };
    sh.streaming.store(ini.streaming, .seq_cst);
    try winshell.start(sh);
    c.sh = sh;
}

fn handleEvent(c: *Child, arena: std.mem.Allocator, event: []const u8, data: std.json.Value) void {
    if (std.mem.eql(u8, event, "doc")) {
        const D = struct { seq: u64 = 0, html: []const u8 = "" };
        const d = parse(D, arena, data) orelse return;
        if (c.lb) |lb| {
            lb.setHTML(d.html);
        } else if (c.sh) |sh| {
            const copy = c.gpa.dupe(u8, d.html) catch return;
            winshell.postUi(sh, .{ .doc = copy });
        }
    } else if (std.mem.eql(u8, event, "eval") or std.mem.eql(u8, event, "xeval")) {
        const D = struct { seq: u64 = 0, js: []const u8 = "" };
        const d = parse(D, arena, data) orelse return;
        if (c.lb) |lb| {
            lb.eval(d.js);
        } else if (c.sh) |sh| {
            const copy = c.gpa.dupe(u8, d.js) catch return;
            winshell.postUi(sh, .{ .eval_js = copy });
        }
    } else if (std.mem.eql(u8, event, "act")) {
        const D = struct { payload: []const u8 = "" };
        const d = parse(D, arena, data) orelse return;
        if (c.lb) |lb| {
            lb.post(d.payload); // inline replay (loopbackWindow.post parity)
        } else if (c.sh) |sh| {
            winshell.postAct(sh, d.payload); // serial act worker (cgoShell.post parity)
        }
    } else if (std.mem.eql(u8, event, "resize")) {
        const D = struct { w: i64 = 0, h: i64 = 0 };
        const d = parse(D, arena, data) orelse return;
        if (c.sh) |sh| winshell.postUi(sh, .{ .resize = .{ .w = @intCast(d.w), .h = @intCast(d.h) } });
    } else if (std.mem.eql(u8, event, "show")) {
        if (c.sh) |sh| winshell.postUi(sh, .show);
    } else if (std.mem.eql(u8, event, "streaming")) {
        const D = struct { on: bool = false };
        const d = parse(D, arena, data) orelse return;
        if (c.sh) |sh| winshell.setStreaming(sh, d.on) else applyVirtualPriority(c, d.on);
    } else if (std.mem.eql(u8, event, "screenshot")) {
        const D = struct { rid: []const u8 = "", path: []const u8 = "", x: i64 = 0, y: i64 = 0, w: i64 = 0, h: i64 = 0 };
        const d = parse(D, arena, data) orelse return;
        if (c.sh) |sh| {
            winshell.capture(sh, d.rid, d.path, @intCast(d.x), @intCast(d.y), @intCast(d.w), @intCast(d.h));
        } else {
            c.em.event("shotres", .{ .rid = d.rid, .err = "no window handle" });
        }
    } else if (std.mem.eql(u8, event, "quit")) {
        const D = struct { graceMs: i64 = 0 };
        const d = parse(D, arena, data) orelse return;
        var grace: u32 = default_quit_grace_ms;
        if (d.graceMs > 0) grace = @intCast(d.graceMs);
        shutdown(c, grace);
    }
}

fn parse(comptime T: type, arena: std.mem.Allocator, data: std.json.Value) ?T {
    return std.json.parseFromValueLeaky(T, arena, data, .{ .ignore_unknown_fields = true }) catch null;
}

/// applyVirtualPriority: the loopback child has no window signals; the governor verdict reduces
/// to "below normal while a stream is live" (window fields keep their focused defaults).
fn applyVirtualPriority(c: *Child, streaming: bool) void {
    const want: u8 = if (streaming) 1 else 0;
    if (c.stream_prio == want) return;
    c.stream_prio = want;
    _ = SetPriorityClass(GetCurrentProcess(), if (streaming) 0x4000 else 0x20);
}

/// shutdown winds the child down: grace!=null = a `quit` (window close + force-exit backstop);
/// null = stop request / stdin EOF (same teardown, default grace).
fn shutdown(c: *Child, grace: ?u32) void {
    if (c.sh) |sh| {
        winshell.terminate(sh, grace orelse default_quit_grace_ms);
        return; // the window thread emits gone + exits when the loop unwinds (or the backstop fires)
    }
    if (c.lb) |lb| lb.terminate();
    c.em.event("gone", wire.Empty{});
    ExitProcess(0);
}

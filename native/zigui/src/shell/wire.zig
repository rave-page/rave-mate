//! PSH1 child-side wire: featurehost's newline-JSON frames over stdio (ZIG_UI_GUIDE.md
//! "Phase B - B5 procShell protocol" §1). ONE mutex-serialized writer on the raw stdout
//! handle (Win32 WriteFile; no std.Io so any thread - wndproc, act worker, capture - can
//! emit), one full frame per write, unbuffered = flushed. stderr = diagnostics the daemon
//! scans into its log.

const std = @import("std");
const sync = @import("sync.zig");

extern "kernel32" fn GetStdHandle(nStdHandle: u32) callconv(.winapi) ?*anyopaque;
extern "kernel32" fn WriteFile(
    hFile: ?*anyopaque,
    lpBuffer: [*]const u8,
    nNumberOfBytesToWrite: u32,
    lpNumberOfBytesWritten: ?*u32,
    lpOverlapped: ?*anyopaque,
) callconv(.winapi) i32;

const std_output_handle: u32 = @bitCast(@as(i32, -11));
const std_error_handle: u32 = @bitCast(@as(i32, -12));

var mu: sync.Lock = .{};

/// Empty stringifies as {} (a tuple literal would emit []).
pub const Empty = struct {};

fn writeAll(h: ?*anyopaque, bytes: []const u8) void {
    if (h == null) return;
    var off: usize = 0;
    while (off < bytes.len) {
        var written: u32 = 0;
        const chunk: u32 = @intCast(@min(bytes.len - off, 1 << 20));
        if (WriteFile(h, bytes.ptr + off, chunk, &written, null) == 0 or written == 0) return;
        off += written;
    }
}

/// writeLine sends one complete frame line (caller appends no newline).
pub fn writeLine(bytes: []const u8) void {
    mu.lock();
    defer mu.unlock();
    const h = GetStdHandle(std_output_handle);
    writeAll(h, bytes);
    writeAll(h, "\n");
}

/// errLine writes one diagnostic line to stderr.
pub fn errLine(bytes: []const u8) void {
    mu.lock();
    defer mu.unlock();
    const h = GetStdHandle(std_error_handle);
    writeAll(h, bytes);
    writeAll(h, "\n");
}

/// Emitter builds one frame into a growing buffer, then writes it atomically. One process-wide
/// instance shared by every thread: its own lock keeps a frame's build+send atomic.
pub const Emitter = struct {
    aw: std.Io.Writer.Allocating,
    emit_mu: sync.Lock = .{},

    pub fn init(gpa: std.mem.Allocator) Emitter {
        return .{ .aw = .init(gpa) };
    }

    pub fn deinit(e: *Emitter) void {
        e.aw.deinit();
    }

    fn send(e: *Emitter) void {
        writeLine(e.aw.writer.buffered());
        e.aw.writer.end = 0;
    }

    /// event emits {"event":name,"data":<data>} where data is any stringifiable value.
    pub fn event(e: *Emitter, name: []const u8, data: anytype) void {
        e.emit_mu.lock();
        defer e.emit_mu.unlock();
        e.aw.writer.end = 0;
        var s: std.json.Stringify = .{ .writer = &e.aw.writer };
        s.beginObject() catch return;
        s.objectField("event") catch return;
        s.write(name) catch return;
        s.objectField("data") catch return;
        s.write(data) catch return;
        s.endObject() catch return;
        e.send();
    }

    /// respond answers a request: ok (err_msg==null) or error.
    pub fn respond(e: *Emitter, id: []const u8, err_msg: ?[]const u8) void {
        e.emit_mu.lock();
        defer e.emit_mu.unlock();
        e.aw.writer.end = 0;
        var s: std.json.Stringify = .{ .writer = &e.aw.writer };
        s.beginObject() catch return;
        s.objectField("id") catch return;
        s.write(id) catch return;
        if (err_msg) |m| {
            s.objectField("error") catch return;
            s.write(m) catch return;
        } else {
            s.objectField("ok") catch return;
            s.write(true) catch return;
        }
        s.endObject() catch return;
        e.send();
    }

    /// log forwards one child log entry into the daemon's bus. Levels: 0=debug 1=info
    /// 2=warn 3=error (internal/shared/logbus).
    pub fn log(e: *Emitter, level: u8, msg: []const u8) void {
        e.event("log", .{ .level = level, .source = "webui", .msg = msg });
    }
};

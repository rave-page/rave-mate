//! rave-mate-vfx - open-standard video effects child (Zig, no cgo): frei0r plugins
//! + ISF shaders (single- and multi-pass). Spawned by the Go `vfx` worker; a
//! plugin crash kills only THIS process.
//!
//! Modes:
//!   --list <dir>[;<dir>...]            plugin discovery -> {"plugins":[...]} on stdout
//!   --frame <chain.json> <in.raw> <out.raw> [tSec]   one RGBA frame through the chain
//!   --pipe <chain.json>                rawvideo RGBA stdin -> chain -> stdout
//!                                      (one frame in flight - bounded by design)
//! Frames are w*h*4 RGBA bytes, dims from the chain spec. Exit 0 ok, 2 usage, 1 error
//! (message on stderr).
const std = @import("std");
const frei0r = @import("frei0r.zig");
const isf = @import("isf.zig");
const chain = @import("chain.zig");

const usage =
    \\usage: rave-mate-vfx --list <dir>[;<dir>...]
    \\       rave-mate-vfx --frame <chain.json> <in.raw> <out.raw> [tSec]
    \\       rave-mate-vfx --pipe <chain.json>
    \\
;

fn fail(msg: []const u8) u8 {
    std.debug.print("rave-mate-vfx: {s}\n", .{msg});
    return 1;
}

pub fn main(init: std.process.Init) !u8 {
    const gpa = init.gpa;
    const io = init.io;

    var args = try std.process.Args.Iterator.initAllocator(init.minimal.args, gpa);
    defer args.deinit();
    _ = args.next(); // exe
    const mode = args.next() orelse {
        std.debug.print("{s}", .{usage});
        return 2;
    };

    if (std.mem.eql(u8, mode, "--list")) {
        const dirs = args.next() orelse {
            std.debug.print("{s}", .{usage});
            return 2;
        };
        return list(gpa, io, dirs);
    }
    if (std.mem.eql(u8, mode, "--frame")) {
        const spec_path = args.next() orelse return 2;
        const in_path = args.next() orelse return 2;
        const out_path = args.next() orelse return 2;
        var t: f64 = 0;
        if (args.next()) |a| t = std.fmt.parseFloat(f64, a) catch 0;
        return frame(gpa, io, spec_path, in_path, out_path, t);
    }
    if (std.mem.eql(u8, mode, "--pipe")) {
        const spec_path = args.next() orelse return 2;
        return pipe(gpa, io, spec_path);
    }
    std.debug.print("{s}", .{usage});
    return 2;
}

// ── discovery ──

const ListParam = struct {
    name: []const u8,
    type: []const u8,
    def: [3]f64,
};
const ListEntry = struct {
    kind: []const u8 = "frei0r",
    ref: []const u8,
    name: []const u8,
    author: []const u8,
    desc: []const u8,
    params: []ListParam,
};

fn isfTypeName(k: isf.ParamKind) []const u8 {
    return switch (k) {
        .float => "double", // UI renders doubles as 0..1 sliders - same as frei0r
        .boolean => "bool",
        .color => "color",
        .point2d => "position",
    };
}

fn isIsfExt(name: []const u8) bool {
    return std.ascii.endsWithIgnoreCase(name, ".fs") or std.ascii.endsWithIgnoreCase(name, ".isf");
}

fn paramTypeName(t: frei0r.ParamType) []const u8 {
    return switch (t) {
        .boolean => "bool",
        .double => "double",
        .color => "color",
        .position => "position",
        .string, _ => "string",
    };
}

fn pluginExt() []const u8 {
    return switch (@import("builtin").os.tag) {
        .windows => ".dll",
        .macos => ".dylib",
        else => ".so",
    };
}

fn list(gpa: std.mem.Allocator, io: std.Io, dirs: []const u8) u8 {
    var arena_state: std.heap.ArenaAllocator = .init(gpa);
    defer arena_state.deinit();
    const arena = arena_state.allocator();

    var entries: std.ArrayList(ListEntry) = .empty;
    var it = std.mem.splitScalar(u8, dirs, ';');
    while (it.next()) |dirpath| {
        if (dirpath.len == 0) continue;
        var dir = std.Io.Dir.cwd().openDir(io, dirpath, .{ .iterate = true }) catch continue;
        defer dir.close(io);
        var dit = dir.iterate();
        while (dit.next(io) catch null) |ent| {
            if (ent.kind != .file) continue;
            const full = std.fs.path.join(arena, &.{ dirpath, ent.name }) catch continue;
            if (std.ascii.endsWithIgnoreCase(ent.name, pluginExt())) {
                var p = frei0r.open(gpa, full) catch |err| {
                    std.debug.print("rave-mate-vfx: skip {s}: {s}\n", .{ full, @errorName(err) });
                    continue;
                };
                defer p.close(gpa);
                var params = arena.alloc(ListParam, p.params.len) catch continue;
                for (p.params, 0..) |pr, i| {
                    params[i] = .{
                        .name = arena.dupe(u8, pr.name) catch continue,
                        .type = paramTypeName(pr.typ),
                        .def = pr.def,
                    };
                }
                entries.append(arena, .{
                    .ref = full,
                    .name = arena.dupe(u8, p.name) catch continue,
                    .author = arena.dupe(u8, p.author) catch continue,
                    .desc = arena.dupe(u8, p.desc) catch continue,
                    .params = params,
                }) catch continue;
            } else if (isIsfExt(ent.name)) {
                const src = std.Io.Dir.cwd().readFileAlloc(io, full, arena, .limited(chain.max_shader_bytes)) catch continue;
                var doc = isf.parse(gpa, src) catch |err| {
                    std.debug.print("rave-mate-vfx: skip {s}: {s}\n", .{ full, @errorName(err) });
                    continue;
                };
                defer doc.deinit(gpa);
                var params = arena.alloc(ListParam, doc.params.len) catch continue;
                for (doc.params, 0..) |pr, i| {
                    params[i] = .{
                        .name = arena.dupe(u8, pr.name) catch continue,
                        .type = isfTypeName(pr.kind),
                        .def = .{ pr.def[0], pr.def[1], pr.def[2] },
                    };
                }
                const stem = ent.name[0 .. ent.name.len - (std.fs.path.extension(ent.name)).len];
                entries.append(arena, .{
                    .kind = "isf",
                    .ref = full,
                    .name = arena.dupe(u8, stem) catch continue,
                    .author = arena.dupe(u8, doc.credit) catch continue,
                    .desc = arena.dupe(u8, doc.desc) catch continue,
                    .params = params,
                }) catch continue;
            }
        }
    }

    var out_buf: [16 * 1024]u8 = undefined;
    var stdout_writer = std.Io.File.stdout().writerStreaming(io, &out_buf);
    const w = &stdout_writer.interface;
    var s: std.json.Stringify = .{ .writer = w };
    s.write(struct { plugins: []ListEntry }{ .plugins = entries.items }) catch return 1;
    w.writeByte('\n') catch return 1;
    w.flush() catch return 1;
    return 0;
}

// ── single frame ──

fn frame(gpa: std.mem.Allocator, io: std.Io, spec_path: []const u8, in_path: []const u8, out_path: []const u8, t: f64) u8 {
    var arena_state: std.heap.ArenaAllocator = .init(gpa);
    defer arena_state.deinit();
    const arena = arena_state.allocator();

    var c = loadChain(gpa, arena, io, spec_path) catch |err| return fail(@errorName(err));
    defer c.deinit();

    const raw = std.Io.Dir.cwd().readFileAlloc(io, in_path, arena, .limited(c.frameBytes() + 1)) catch |err|
        return fail(@errorName(err));
    if (raw.len != c.frameBytes()) return fail("input size != w*h*4");
    const px = @as([*]u32, @ptrCast(@alignCast(raw.ptr)))[0 .. raw.len / 4];
    c.apply(@intFromFloat(@max(0, t) * c.fps), px);
    std.Io.Dir.cwd().writeFile(io, .{ .sub_path = out_path, .data = raw }) catch |err|
        return fail(@errorName(err));
    return 0;
}

// ── streaming pipe ──

fn pipe(gpa: std.mem.Allocator, io: std.Io, spec_path: []const u8) u8 {
    var arena_state: std.heap.ArenaAllocator = .init(gpa);
    defer arena_state.deinit();

    var c = loadChain(gpa, arena_state.allocator(), io, spec_path) catch |err| return fail(@errorName(err));
    defer c.deinit();

    const buf = gpa.alignedAlloc(u8, .of(u32), c.frameBytes()) catch return fail("oom");
    defer gpa.free(buf);
    const px = @as([*]u32, @ptrCast(buf.ptr))[0 .. buf.len / 4];

    var in_buf: [64 * 1024]u8 = undefined;
    var stdin_reader = std.Io.File.stdin().readerStreaming(io, &in_buf);
    const in = &stdin_reader.interface;
    var out_buf: [64 * 1024]u8 = undefined;
    var stdout_writer = std.Io.File.stdout().writerStreaming(io, &out_buf);
    const out = &stdout_writer.interface;

    var idx: u64 = 0;
    while (true) : (idx += 1) {
        in.readSliceAll(buf) catch |err| switch (err) {
            error.EndOfStream => break,
            else => return fail("stdin read failed"),
        };
        c.apply(idx, px);
        out.writeAll(buf) catch return fail("stdout write failed");
        out.flush() catch return fail("stdout flush failed");
    }
    return 0;
}

fn loadChain(gpa: std.mem.Allocator, arena: std.mem.Allocator, io: std.Io, spec_path: []const u8) !chain.Chain {
    const bytes = try std.Io.Dir.cwd().readFileAlloc(io, spec_path, arena, .limited(chain.max_spec_bytes));
    return chain.Chain.load(gpa, arena, io, bytes);
}

test {
    _ = frei0r;
    _ = isf;
    _ = chain;
}

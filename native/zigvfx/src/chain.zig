//! Effect chain: JSON spec -> loaded plugin instances -> per-frame ping-pong apply.
//! Spec (written by the Go vfx worker):
//!   {"w":1080,"h":1920,"fps":30,
//!    "fx":[{"kind":"frei0r","ref":"C:/.../glow.dll","params":{"blur":0.5}}]}
//! kind "isf" loads a shader file through the WGL host (isf.zig).
const std = @import("std");
const frei0r = @import("frei0r.zig");
const isf = @import("isf.zig");

pub const FxSpec = struct {
    kind: []const u8 = "",
    ref: []const u8 = "",
    params: std.json.ArrayHashMap(f64) = .{},
};

pub const Spec = struct {
    w: u32 = 0,
    h: u32 = 0,
    fps: f64 = 30,
    fx: []FxSpec = &.{},
};

pub const max_dim = 8192;
pub const max_spec_bytes = 1 << 20;

pub const SpecError = error{ BadSpec, OutOfMemory };

// parseSpec validates dims + fps; arena-owned result.
pub fn parseSpec(arena: std.mem.Allocator, bytes: []const u8) SpecError!Spec {
    const s = std.json.parseFromSliceLeaky(Spec, arena, bytes, .{
        .ignore_unknown_fields = true,
    }) catch return error.BadSpec;
    if (s.w == 0 or s.h == 0 or s.w > max_dim or s.h > max_dim) return error.BadSpec;
    if (!(s.fps > 0) or s.fps > 1000) return error.BadSpec;
    return s;
}

pub const Stage = union(enum) {
    f0r: struct { plugin: *frei0r.Plugin, inst: frei0r.Instance },
    shader: isf.Instance,
};

pub const max_shader_bytes = 1 << 20;

pub const Chain = struct {
    gpa: std.mem.Allocator,
    w: u32,
    h: u32,
    fps: f64,
    stages: []Stage,
    scratch: []u32, // ping-pong partner of the caller's frame buffer
    gl: ?*isf.Host, // lazy - created for the first shader stage

    pub const LoadError = error{ BadSpec, IsfNotSupported, UnknownKind, PluginFailed, OutOfMemory };

    // load opens every plugin/shader in the spec and applies static params.
    pub fn load(gpa: std.mem.Allocator, arena: std.mem.Allocator, io: std.Io, bytes: []const u8) LoadError!Chain {
        const spec = try parseSpec(arena, bytes);
        var stages: std.ArrayList(Stage) = .empty;
        var gl: ?*isf.Host = null;
        errdefer {
            for (stages.items) |*st| closeStage(gpa, st);
            stages.deinit(gpa);
            closeGl(gpa, gl);
        }
        var keybuf: [256]u8 = undefined;
        for (spec.fx) |fx| {
            if (std.mem.eql(u8, fx.kind, "frei0r")) {
                const p = try gpa.create(frei0r.Plugin);
                errdefer gpa.destroy(p);
                p.* = frei0r.open(gpa, fx.ref) catch return error.PluginFailed;
                errdefer p.close(gpa);
                var inst = frei0r.Instance.create(gpa, p, spec.w, spec.h) catch return error.PluginFailed;
                inst.setParams(&fx.params, &keybuf);
                try stages.append(gpa, .{ .f0r = .{ .plugin = p, .inst = inst } });
                continue;
            }
            if (!std.mem.eql(u8, fx.kind, "isf")) return error.UnknownKind;
            if (gl == null) {
                const h = try gpa.create(isf.Host);
                errdefer gpa.destroy(h);
                h.* = isf.Host.init() catch return error.IsfNotSupported;
                gl = h;
            }
            const src = std.Io.Dir.cwd().readFileAlloc(io, fx.ref, arena, .limited(max_shader_bytes)) catch
                return error.PluginFailed;
            var doc = isf.parse(gpa, src) catch return error.PluginFailed;
            defer doc.deinit(gpa); // compile-time only; the instance owns its copies
            var inst = isf.Instance.create(gpa, gl.?, &doc, spec.w, spec.h) catch return error.PluginFailed;
            inst.setParams(&fx.params, &keybuf);
            try stages.append(gpa, .{ .shader = inst });
        }
        const scratch = try gpa.alloc(u32, @as(usize, spec.w) * spec.h);
        errdefer gpa.free(scratch);
        return .{
            .gpa = gpa,
            .w = spec.w,
            .h = spec.h,
            .fps = spec.fps,
            .stages = try stages.toOwnedSlice(gpa),
            .scratch = scratch,
            .gl = gl,
        };
    }

    fn closeStage(gpa: std.mem.Allocator, st: *Stage) void {
        switch (st.*) {
            .f0r => |*f| {
                f.inst.destroy(gpa);
                f.plugin.close(gpa);
                gpa.destroy(f.plugin);
            },
            .shader => |*s| s.destroy(gpa),
        }
    }

    fn closeGl(gpa: std.mem.Allocator, gl: ?*isf.Host) void {
        if (gl) |h| {
            h.deinit();
            gpa.destroy(h);
        }
    }

    pub fn deinit(c: *Chain) void {
        for (c.stages) |*st| closeStage(c.gpa, st);
        c.gpa.free(c.stages);
        c.gpa.free(c.scratch);
        closeGl(c.gpa, c.gl);
    }

    pub fn frameBytes(c: *const Chain) usize {
        return @as(usize, c.w) * c.h * 4;
    }

    // apply runs all stages over frame (in place; scratch ping-pongs internally).
    pub fn apply(c: *Chain, frame_idx: u64, frame: []u32) void {
        const t = @as(f64, @floatFromInt(frame_idx)) / c.fps;
        var cur = frame;
        var alt = c.scratch;
        for (c.stages) |*st| {
            switch (st.*) {
                .f0r => |*f| f.inst.apply(t, cur, alt),
                .shader => |*s| s.apply(t, frame_idx, cur, alt),
            }
            const tmp = cur;
            cur = alt;
            alt = tmp;
        }
        if (cur.ptr != frame.ptr) @memcpy(frame, cur);
    }
};

test "parseSpec validates" {
    var arena_state: std.heap.ArenaAllocator = .init(std.testing.allocator);
    defer arena_state.deinit();
    const a = arena_state.allocator();

    const ok = try parseSpec(a, "{\"w\":1080,\"h\":1920,\"fps\":29.97,\"fx\":[]}");
    try std.testing.expectEqual(@as(u32, 1080), ok.w);
    try std.testing.expectEqual(@as(usize, 0), ok.fx.len);

    const fx = try parseSpec(a,
        \\{"w":8,"h":8,"fx":[{"kind":"frei0r","ref":"x.dll","params":{"amount":0.25,"col.r":1}}]}
    );
    try std.testing.expectEqual(@as(f64, 30), fx.fps); // default
    try std.testing.expectEqual(@as(usize, 1), fx.fx.len);
    try std.testing.expectEqual(@as(f64, 0.25), fx.fx[0].params.map.get("amount").?);
    try std.testing.expectEqual(@as(f64, 1), fx.fx[0].params.map.get("col.r").?);

    try std.testing.expectError(error.BadSpec, parseSpec(a, "{\"w\":0,\"h\":9}"));
    try std.testing.expectError(error.BadSpec, parseSpec(a, "{\"w\":9000,\"h\":9,\"fps\":30}"));
    try std.testing.expectError(error.BadSpec, parseSpec(a, "{\"w\":8,\"h\":8,\"fps\":0}"));
    try std.testing.expectError(error.BadSpec, parseSpec(a, "not json"));
}

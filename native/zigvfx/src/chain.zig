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
    blend: []const u8 = "", // composite this stage OVER its input ("" = replace)
    mix: f64 = 1, // stage opacity 0..1
};

/// Blend composites a stage's output (top) back over its input (base). Generators
/// and heavy filters are usable this way instead of replacing the picture.
pub const Blend = struct {
    mode: Mode = .normal,
    mix: f32 = 1,

    pub const Mode = enum { normal, screen, add, multiply, overlay, lighten, darken, difference };

    pub fn parse(name: []const u8, mix: f64) Blend {
        var b: Blend = .{ .mix = @floatCast(std.math.clamp(mix, 0, 1)) };
        inline for (@typeInfo(Mode).@"enum".fields) |f| {
            if (std.mem.eql(u8, name, f.name)) b.mode = @enumFromInt(f.value);
        }
        return b;
    }

    /// passthrough = the stage output IS the result (no compositing work needed).
    pub fn passthrough(b: Blend) bool {
        return b.mode == .normal and b.mix >= 1;
    }

    fn chan(mode: Mode, base: u32, top: u32) u32 {
        return switch (mode) {
            .normal => top,
            .screen => 255 - (255 - base) * (255 - top) / 255,
            .add => @min(255, base + top),
            .multiply => base * top / 255,
            .overlay => if (base < 128) 2 * base * top / 255 else 255 - 2 * (255 - base) * (255 - top) / 255,
            .lighten => @max(base, top),
            .darken => @min(base, top),
            .difference => if (base > top) base - top else top - base,
        };
    }

    /// apply writes base⊕top into top (RGBA8 packed LE; base alpha kept).
    pub fn apply(b: Blend, base: []const u32, top: []u32) void {
        const m: u32 = @intFromFloat(@round(b.mix * 255));
        for (top, 0..) |px, i| {
            const bp = base[i];
            var out: u32 = bp & 0xFF000000; // opaque video: alpha comes from the base
            inline for (.{ 0, 8, 16 }) |shift| {
                const sh: u5 = shift;
                const bc = (bp >> sh) & 0xFF;
                const tc = (px >> sh) & 0xFF;
                const bl = chan(b.mode, bc, tc);
                const v = (bc * (255 - m) + bl * m) / 255; // mix toward the blended value
                out |= @as(u32, @min(v, 255)) << sh; // @min narrows to u8 - widen before shifting
            }
            top[i] = out;
        }
    }
};

pub const Spec = struct {
    w: u32 = 0,
    h: u32 = 0,
    fps: f64 = 30,
    fx: ?[]FxSpec = null, // optional: a Go nil slice marshals "fx":null - treat as empty
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
    blends: []Blend, // per stage, parallel to stages
    scratch: []u32, // ping-pong partner of the caller's frame buffer
    orig: []u32, // untouched input frame = mixer base layer ([] when no mixer stage)
    gl: ?*isf.Host, // lazy - created for the first shader stage

    pub const LoadError = error{ BadSpec, IsfNotSupported, UnknownKind, PluginFailed, OutOfMemory };

    // load opens every plugin/shader in the spec and applies static params.
    pub fn load(gpa: std.mem.Allocator, arena: std.mem.Allocator, io: std.Io, bytes: []const u8) LoadError!Chain {
        const spec = try parseSpec(arena, bytes);
        const fx_list: []FxSpec = spec.fx orelse &.{};
        var stages: std.ArrayList(Stage) = .empty;
        var blends: std.ArrayList(Blend) = .empty;
        var gl: ?*isf.Host = null;
        errdefer {
            for (stages.items) |*st| closeStage(gpa, st);
            stages.deinit(gpa);
            blends.deinit(gpa);
            closeGl(gpa, gl);
        }
        var keybuf: [256]u8 = undefined;
        for (fx_list) |fx| {
            if (std.mem.eql(u8, fx.kind, "frei0r")) {
                const p = try gpa.create(frei0r.Plugin);
                errdefer gpa.destroy(p);
                p.* = frei0r.open(gpa, fx.ref) catch return error.PluginFailed;
                errdefer p.close(gpa);
                var inst = frei0r.Instance.create(gpa, p, spec.w, spec.h) catch return error.PluginFailed;
                inst.setParams(&fx.params, &keybuf);
                try stages.append(gpa, .{ .f0r = .{ .plugin = p, .inst = inst } });
                try blends.append(gpa, Blend.parse(fx.blend, fx.mix));
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
            try blends.append(gpa, Blend.parse(fx.blend, fx.mix));
        }
        const scratch = try gpa.alloc(u32, @as(usize, spec.w) * spec.h);
        errdefer gpa.free(scratch);
        var need_orig = false;
        for (stages.items) |*st| {
            if (st.* == .f0r and st.f0r.plugin.plug_type >= 2) need_orig = true;
        }
        const orig: []u32 = if (need_orig) try gpa.alloc(u32, @as(usize, spec.w) * spec.h) else &.{};
        errdefer if (need_orig) gpa.free(orig);
        return .{
            .gpa = gpa,
            .w = spec.w,
            .h = spec.h,
            .fps = spec.fps,
            .stages = try stages.toOwnedSlice(gpa),
            .blends = try blends.toOwnedSlice(gpa),
            .scratch = scratch,
            .orig = orig,
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
        c.gpa.free(c.blends);
        c.gpa.free(c.scratch);
        if (c.orig.len != 0) c.gpa.free(c.orig);
        closeGl(c.gpa, c.gl);
    }

    pub fn frameBytes(c: *const Chain) usize {
        return @as(usize, c.w) * c.h * 4;
    }

    // apply runs all stages over frame (in place; scratch ping-pongs internally).
    // Sources replace the frame; mixers blend orig (base) with the chain so far (top).
    pub fn apply(c: *Chain, frame_idx: u64, frame: []u32) void {
        const t = @as(f64, @floatFromInt(frame_idx)) / c.fps;
        if (c.orig.len != 0) @memcpy(c.orig, frame);
        var cur = frame;
        var alt = c.scratch;
        for (c.stages, c.blends) |*st, bl| {
            switch (st.*) {
                .f0r => |*f| switch (f.plugin.plug_type) {
                    1 => f.inst.applySource(t, alt),
                    2, 3 => f.inst.applyMix(t, c.orig, cur, alt),
                    else => f.inst.apply(t, cur, alt),
                },
                .shader => |*s| s.apply(t, frame_idx, cur, alt),
            }
            if (!bl.passthrough()) bl.apply(cur, alt); // composite the stage over its own input
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
    try std.testing.expectEqual(@as(usize, 0), ok.fx.?.len);

    // Go nil slice → "fx":null (and an absent fx) both mean an empty chain
    const nul = try parseSpec(a, "{\"w\":8,\"h\":8,\"fps\":30,\"fx\":null}");
    try std.testing.expectEqual(@as(?[]FxSpec, null), nul.fx);
    const abs = try parseSpec(a, "{\"w\":8,\"h\":8,\"fps\":30}");
    try std.testing.expectEqual(@as(?[]FxSpec, null), abs.fx);

    const fx = try parseSpec(a,
        \\{"w":8,"h":8,"fx":[{"kind":"frei0r","ref":"x.dll","params":{"amount":0.25,"col.r":1}}]}
    );
    try std.testing.expectEqual(@as(f64, 30), fx.fps); // default
    try std.testing.expectEqual(@as(usize, 1), fx.fx.?.len);
    try std.testing.expectEqual(@as(f64, 0.25), fx.fx.?[0].params.map.get("amount").?);
    try std.testing.expectEqual(@as(f64, 1), fx.fx.?[0].params.map.get("col.r").?);

    const bl = try parseSpec(a,
        \\{"w":8,"h":8,"fx":[{"kind":"isf","ref":"g.fs","blend":"screen","mix":0.5}]}
    );
    try std.testing.expectEqualStrings("screen", bl.fx.?[0].blend);
    try std.testing.expectEqual(@as(f64, 0.5), bl.fx.?[0].mix);

    try std.testing.expectError(error.BadSpec, parseSpec(a, "{\"w\":0,\"h\":9}"));
    try std.testing.expectError(error.BadSpec, parseSpec(a, "{\"w\":9000,\"h\":9,\"fps\":30}"));
    try std.testing.expectError(error.BadSpec, parseSpec(a, "{\"w\":8,\"h\":8,\"fps\":0}"));
    try std.testing.expectError(error.BadSpec, parseSpec(a, "not json"));
}

test "Blend composites over the stage input" {
    // unknown name falls back to normal; mix defaults full → passthrough
    try std.testing.expect(Blend.parse("nope", 1).passthrough());
    try std.testing.expect(!Blend.parse("screen", 1).passthrough());
    try std.testing.expect(!Blend.parse("normal", 0.5).passthrough());

    const base = [_]u32{0xFF000000}; // opaque black
    var top = [_]u32{0xFF0000FF}; // opaque red
    Blend.parse("screen", 1).apply(&base, &top);
    try std.testing.expectEqual(@as(u32, 0xFF0000FF), top[0]); // screen over black = top

    top = [_]u32{0xFF0000FF};
    Blend.parse("normal", 0.5).apply(&base, &top);
    try std.testing.expectEqual(@as(u32, 0xFF000080), top[0]); // half-mixed red (0.5 → m=128)

    top = [_]u32{0xFF0000FF};
    Blend.parse("multiply", 1).apply(&base, &top);
    try std.testing.expectEqual(@as(u32, 0xFF000000), top[0]); // multiply with black = black
}

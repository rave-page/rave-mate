//! frei0r plugin host (std.DynLib, no cgo). Spec: https://frei0r.dyne.org C ABI v1.x.
//! Filters only (sources/mixers rejected); color models RGBA8888/PACKED32 direct,
//! BGRA8888 via R/B byte swap. frei0r wants mult-of-8 frame dims - Instance pads
//! (edge-replicate) and crops so callers keep exact sizes.
const std = @import("std");
const builtin = @import("builtin");

// std.DynLib lost its Windows arm in 0.16 - tiny LoadLibrary shim, std.DynLib elsewhere.
extern "kernel32" fn LoadLibraryW([*:0]const u16) callconv(.winapi) ?*anyopaque;
extern "kernel32" fn GetProcAddress(*anyopaque, [*:0]const u8) callconv(.winapi) ?*anyopaque;
extern "kernel32" fn FreeLibrary(*anyopaque) callconv(.winapi) i32;

pub const Lib = if (builtin.os.tag == .windows) struct {
    handle: *anyopaque,

    pub fn open(path: []const u8) !Lib {
        var buf: [4096:0]u16 = undefined;
        const n = std.unicode.wtf8ToWtf16Le(buf[0..4095], path) catch return error.BadPath;
        buf[n] = 0;
        return .{ .handle = LoadLibraryW(&buf) orelse return error.OpenFailed };
    }
    pub fn lookup(l: *Lib, comptime T: type, name: [:0]const u8) ?T {
        return @ptrCast(GetProcAddress(l.handle, name) orelse return null);
    }
    pub fn close(l: *Lib) void {
        _ = FreeLibrary(l.handle);
    }
} else struct {
    inner: std.DynLib,

    pub fn open(path: []const u8) !Lib {
        return .{ .inner = try std.DynLib.open(path) };
    }
    pub fn lookup(l: *Lib, comptime T: type, name: [:0]const u8) ?T {
        return l.inner.lookup(T, name);
    }
    pub fn close(l: *Lib) void {
        l.inner.close();
    }
};

pub const ParamType = enum(c_int) { boolean = 0, double = 1, color = 2, position = 3, string = 4, _ };

pub const RawInfo = extern struct {
    name: ?[*:0]const u8,
    author: ?[*:0]const u8,
    plugin_type: c_int, // 0 filter, 1 source, 2 mixer2, 3 mixer3
    color_model: c_int, // 0 bgra8888, 1 rgba8888, 2 packed32
    frei0r_version: c_int,
    major_version: c_int,
    minor_version: c_int,
    num_params: c_int,
    explanation: ?[*:0]const u8,
};
pub const RawParamInfo = extern struct {
    name: ?[*:0]const u8,
    type: c_int,
    explanation: ?[*:0]const u8,
};
pub const Color = extern struct { r: f32, g: f32, b: f32 };
pub const Position = extern struct { x: f64, y: f64 };

pub const Param = struct {
    name: []const u8,
    typ: ParamType,
    def: [3]f64, // double/bool [0]; position x,y; color r,g,b
};

const ConstructFn = *const fn (c_uint, c_uint) callconv(.c) ?*anyopaque;
const DestructFn = *const fn (?*anyopaque) callconv(.c) void;
const ParamFn = *const fn (?*anyopaque, ?*anyopaque, c_int) callconv(.c) void;
const UpdateFn = *const fn (?*anyopaque, f64, [*]const u32, [*]u32) callconv(.c) void;

pub const OpenError = error{ NotFrei0r, NotFilter, BadColorModel, InitFailed, OutOfMemory };

pub const Plugin = struct {
    lib: Lib,
    name: []const u8,
    author: []const u8,
    desc: []const u8,
    color_model: c_int,
    params: []Param,
    construct: ConstructFn,
    destruct: DestructFn,
    set_param: ParamFn,
    get_param: ParamFn,
    update: UpdateFn,
    deinit_fn: ?*const fn () callconv(.c) void,

    pub fn close(p: *Plugin, gpa: std.mem.Allocator) void {
        if (p.deinit_fn) |f| f();
        for (p.params) |pr| gpa.free(pr.name);
        gpa.free(p.params);
        gpa.free(p.name);
        gpa.free(p.author);
        gpa.free(p.desc);
        p.lib.close();
    }
};

fn dupZ(gpa: std.mem.Allocator, s: ?[*:0]const u8) ![]const u8 {
    return gpa.dupe(u8, if (s) |z| std.mem.span(z) else "");
}

// open loads + f0r_init()s a filter plugin and reads its param table (defaults via a
// throwaway 16x16 instance). Caller owns the Plugin; close() releases everything.
pub fn open(gpa: std.mem.Allocator, path: []const u8) OpenError!Plugin {
    var lib = Lib.open(path) catch return error.NotFrei0r;
    errdefer lib.close();

    const init_fn = lib.lookup(*const fn () callconv(.c) c_int, "f0r_init") orelse return error.NotFrei0r;
    const get_info = lib.lookup(*const fn (*RawInfo) callconv(.c) void, "f0r_get_plugin_info") orelse return error.NotFrei0r;
    const get_pinfo = lib.lookup(*const fn (*RawParamInfo, c_int) callconv(.c) void, "f0r_get_param_info") orelse return error.NotFrei0r;
    const construct = lib.lookup(ConstructFn, "f0r_construct") orelse return error.NotFrei0r;
    const destruct = lib.lookup(DestructFn, "f0r_destruct") orelse return error.NotFrei0r;
    const set_param = lib.lookup(ParamFn, "f0r_set_param_value") orelse return error.NotFrei0r;
    const get_param = lib.lookup(ParamFn, "f0r_get_param_value") orelse return error.NotFrei0r;
    const update = lib.lookup(UpdateFn, "f0r_update") orelse return error.NotFrei0r;
    const deinit_fn = lib.lookup(*const fn () callconv(.c) void, "f0r_deinit");

    if (init_fn() < 0) return error.InitFailed; // ffmpeg convention: negative = failure

    var info: RawInfo = std.mem.zeroes(RawInfo);
    get_info(&info);
    if (info.plugin_type != 0) return error.NotFilter;
    if (info.color_model < 0 or info.color_model > 2) return error.BadColorModel;

    const n: usize = if (info.num_params > 0) @intCast(info.num_params) else 0;
    var params = try gpa.alloc(Param, n);
    errdefer gpa.free(params);
    var got: usize = 0;
    errdefer for (params[0..got]) |pr| gpa.free(pr.name);

    // defaults live inside a constructed instance
    const probe = construct(16, 16);
    defer if (probe) |pi| destruct(pi);

    for (0..n) |i| {
        var pi: RawParamInfo = std.mem.zeroes(RawParamInfo);
        get_pinfo(&pi, @intCast(i));
        const typ: ParamType = if (pi.type >= 0 and pi.type <= 4) @enumFromInt(pi.type) else .double;
        var def: [3]f64 = .{ 0, 0, 0 };
        if (probe) |inst| switch (typ) {
            .boolean, .double => {
                var v: f64 = 0;
                get_param(inst, &v, @intCast(i));
                def[0] = v;
            },
            .position => {
                var v: Position = .{ .x = 0, .y = 0 };
                get_param(inst, &v, @intCast(i));
                def = .{ v.x, v.y, 0 };
            },
            .color => {
                var v: Color = .{ .r = 0, .g = 0, .b = 0 };
                get_param(inst, &v, @intCast(i));
                def = .{ v.r, v.g, v.b };
            },
            .string, _ => {},
        };
        params[i] = .{ .name = try dupZ(gpa, pi.name), .typ = typ, .def = def };
        got = i + 1;
    }

    return .{
        .lib = lib,
        .name = try dupZ(gpa, info.name),
        .author = try dupZ(gpa, info.author),
        .desc = try dupZ(gpa, info.explanation),
        .color_model = info.color_model,
        .params = params,
        .construct = construct,
        .destruct = destruct,
        .set_param = set_param,
        .get_param = get_param,
        .update = update,
        .deinit_fn = deinit_fn,
    };
}

// pad8 rounds up to the next multiple of 8 (frei0r frame-dim requirement).
pub fn pad8(v: u32) u32 {
    return (v + 7) & ~@as(u32, 7);
}

pub const Instance = struct {
    plugin: *Plugin,
    inst: *anyopaque,
    w: u32,
    h: u32,
    pw: u32,
    ph: u32,
    staged: bool, // pad or byte-swap forces staging buffers
    swap_rb: bool, // BGRA8888 plugin fed RGBA frames
    pin: []u32,
    pout: []u32,

    pub fn create(gpa: std.mem.Allocator, p: *Plugin, w: u32, h: u32) !Instance {
        const pw = pad8(w);
        const ph = pad8(h);
        const swap = p.color_model == 0;
        const staged = swap or pw != w or ph != h;
        const inst = p.construct(pw, ph) orelse return error.ConstructFailed;
        errdefer p.destruct(inst);
        const pin: []u32 = if (staged) try gpa.alloc(u32, pw * ph) else &.{};
        errdefer if (staged) gpa.free(pin);
        const pout: []u32 = if (staged) try gpa.alloc(u32, pw * ph) else &.{};
        return .{ .plugin = p, .inst = inst, .w = w, .h = h, .pw = pw, .ph = ph, .staged = staged, .swap_rb = swap, .pin = pin, .pout = pout };
    }

    pub fn destroy(i: *Instance, gpa: std.mem.Allocator) void {
        i.plugin.destruct(i.inst);
        if (i.staged) {
            gpa.free(i.pin);
            gpa.free(i.pout);
        }
    }

    // setParams pushes spec values by param name; dotted sub-keys address components
    // ("col.r"/"col.g"/"col.b", "pos.x"/"pos.y"). Missing keys keep plugin defaults.
    pub fn setParams(i: *Instance, spec: *const std.json.ArrayHashMap(f64), buf: []u8) void {
        for (i.plugin.params, 0..) |pr, idx| {
            switch (pr.typ) {
                .boolean, .double => {
                    if (spec.map.get(pr.name)) |v| {
                        var s: f64 = v;
                        i.plugin.set_param(i.inst, &s, @intCast(idx));
                    }
                },
                .color => {
                    var c: Color = .{ .r = @floatCast(pr.def[0]), .g = @floatCast(pr.def[1]), .b = @floatCast(pr.def[2]) };
                    var any = false;
                    if (sub(spec, buf, pr.name, "r")) |v| {
                        c.r = @floatCast(v);
                        any = true;
                    }
                    if (sub(spec, buf, pr.name, "g")) |v| {
                        c.g = @floatCast(v);
                        any = true;
                    }
                    if (sub(spec, buf, pr.name, "b")) |v| {
                        c.b = @floatCast(v);
                        any = true;
                    }
                    if (any) i.plugin.set_param(i.inst, &c, @intCast(idx));
                },
                .position => {
                    var pos: Position = .{ .x = pr.def[0], .y = pr.def[1] };
                    var any = false;
                    if (sub(spec, buf, pr.name, "x")) |v| {
                        pos.x = v;
                        any = true;
                    }
                    if (sub(spec, buf, pr.name, "y")) |v| {
                        pos.y = v;
                        any = true;
                    }
                    if (any) i.plugin.set_param(i.inst, &pos, @intCast(idx));
                },
                .string, _ => {},
            }
        }
    }

    fn sub(spec: *const std.json.ArrayHashMap(f64), buf: []u8, name: []const u8, comp: []const u8) ?f64 {
        const key = std.fmt.bufPrint(buf, "{s}.{s}", .{ name, comp }) catch return null;
        return spec.map.get(key);
    }

    // apply runs one frame; src/dst are w*h RGBA and must not alias.
    pub fn apply(i: *Instance, time: f64, src: []const u32, dst: []u32) void {
        if (!i.staged) {
            i.plugin.update(i.inst, time, src.ptr, dst.ptr);
            return;
        }
        stageIn(i.pin, src, i.w, i.h, i.pw, i.ph, i.swap_rb);
        i.plugin.update(i.inst, time, i.pin.ptr, i.pout.ptr);
        stageOut(i.pout, dst, i.w, i.h, i.pw, i.swap_rb);
    }
};

// stageIn copies w*h into a pw*ph padded buffer, edge-replicating pad columns/rows,
// optionally swapping R/B bytes (RGBA<->BGRA).
pub fn stageIn(pin: []u32, src: []const u32, w: u32, h: u32, pw: u32, ph: u32, swap: bool) void {
    var y: u32 = 0;
    while (y < h) : (y += 1) {
        const srow = src[y * w ..][0..w];
        const drow = pin[y * pw ..][0..pw];
        if (swap) {
            for (srow, 0..) |px, x| drow[x] = swapRB(px);
        } else {
            @memcpy(drow[0..w], srow);
        }
        const edge = drow[w - 1];
        for (drow[w..]) |*px| px.* = edge;
    }
    while (y < ph) : (y += 1) {
        @memcpy(pin[y * pw ..][0..pw], pin[(h - 1) * pw ..][0..pw]);
    }
}

// stageOut crops pw-stride back to w*h, optionally swapping R/B.
pub fn stageOut(pout: []const u32, dst: []u32, w: u32, h: u32, pw: u32, swap: bool) void {
    var y: u32 = 0;
    while (y < h) : (y += 1) {
        const srow = pout[y * pw ..][0..w];
        const drow = dst[y * w ..][0..w];
        if (swap) {
            for (srow, 0..) |px, x| drow[x] = swapRB(px);
        } else {
            @memcpy(drow, srow);
        }
    }
}

pub inline fn swapRB(px: u32) u32 {
    // bytes in memory R,G,B,A (little-endian u32 = A<<24|B<<16|G<<8|R) <-> B,G,R,A
    return (px & 0xFF00FF00) | ((px & 0x00FF0000) >> 16) | ((px & 0x000000FF) << 16);
}

test "pad8" {
    try std.testing.expectEqual(@as(u32, 8), pad8(1));
    try std.testing.expectEqual(@as(u32, 8), pad8(8));
    try std.testing.expectEqual(@as(u32, 16), pad8(9));
    try std.testing.expectEqual(@as(u32, 1080), pad8(1080));
    try std.testing.expectEqual(@as(u32, 1352), pad8(1350));
}

test "swapRB roundtrip" {
    const px: u32 = 0xAABBCCDD;
    try std.testing.expectEqual(px, swapRB(swapRB(px)));
    // R=0x11 G=0x22 B=0x33 A=0x44 (memory RGBA, LE) -> u32 0x44332211 -> swapped 0x44112233
    try std.testing.expectEqual(@as(u32, 0x44112233), swapRB(0x44332211));
}

test "stage pad replicates edges" {
    // 2x2 -> 8x8 padded
    const src = [_]u32{ 1, 2, 3, 4 };
    var pin = [_]u32{0} ** 64;
    stageIn(&pin, &src, 2, 2, 8, 8, false);
    try std.testing.expectEqual(@as(u32, 2), pin[7]); // row 0 pad = right edge
    try std.testing.expectEqual(@as(u32, 3), pin[7 * 8]); // bottom pad rows = last row
    try std.testing.expectEqual(@as(u32, 4), pin[7 * 8 + 7]);
    var dst = [_]u32{0} ** 4;
    stageOut(&pin, &dst, 2, 2, 8, false);
    try std.testing.expectEqualSlices(u32, &src, &dst);
}

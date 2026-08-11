//! Test frei0r plugin (invert). Built as f0r_test_invert.{dll,so} purely so the host
//! has a real dynamic plugin to load in integration tests - never shipped.
//! Params: amount (double, 1.0 = full invert), bypass (bool).
const std = @import("std");

const PluginInfo = extern struct {
    name: ?[*:0]const u8,
    author: ?[*:0]const u8,
    plugin_type: c_int,
    color_model: c_int,
    frei0r_version: c_int,
    major_version: c_int,
    minor_version: c_int,
    num_params: c_int,
    explanation: ?[*:0]const u8,
};
const ParamInfo = extern struct {
    name: ?[*:0]const u8,
    type: c_int,
    explanation: ?[*:0]const u8,
};

const Inst = struct {
    w: u32,
    h: u32,
    amount: f64 = 1.0,
    bypass: f64 = 0,
};

export fn f0r_init() c_int {
    return 1;
}
export fn f0r_deinit() void {}

export fn f0r_get_plugin_info(info: *PluginInfo) void {
    info.* = .{
        .name = "Invert Test",
        .author = "rave-mate",
        .plugin_type = 0, // filter
        .color_model = 1, // RGBA8888
        .frei0r_version = 1,
        .major_version = 0,
        .minor_version = 1,
        .num_params = 2,
        .explanation = "test-only RGB invert",
    };
}

export fn f0r_get_param_info(info: *ParamInfo, idx: c_int) void {
    switch (idx) {
        0 => info.* = .{ .name = "amount", .type = 1, .explanation = "invert mix" },
        1 => info.* = .{ .name = "bypass", .type = 0, .explanation = "pass through" },
        else => info.* = .{ .name = "", .type = 1, .explanation = "" },
    }
}

export fn f0r_construct(w: c_uint, h: c_uint) ?*anyopaque {
    const inst = std.heap.page_allocator.create(Inst) catch return null;
    inst.* = .{ .w = w, .h = h };
    return inst;
}

export fn f0r_destruct(p: ?*anyopaque) void {
    const inst: *Inst = @ptrCast(@alignCast(p orelse return));
    std.heap.page_allocator.destroy(inst);
}

export fn f0r_set_param_value(p: ?*anyopaque, param: ?*anyopaque, idx: c_int) void {
    const inst: *Inst = @ptrCast(@alignCast(p orelse return));
    const v: *const f64 = @ptrCast(@alignCast(param orelse return));
    switch (idx) {
        0 => inst.amount = v.*,
        1 => inst.bypass = v.*,
        else => {},
    }
}

export fn f0r_get_param_value(p: ?*anyopaque, param: ?*anyopaque, idx: c_int) void {
    const inst: *Inst = @ptrCast(@alignCast(p orelse return));
    const v: *f64 = @ptrCast(@alignCast(param orelse return));
    switch (idx) {
        0 => v.* = inst.amount,
        1 => v.* = inst.bypass,
        else => v.* = 0,
    }
}

export fn f0r_update(p: ?*anyopaque, time: f64, inframe: [*]const u32, outframe: [*]u32) void {
    _ = time;
    const inst: *Inst = @ptrCast(@alignCast(p orelse return));
    const n: usize = @as(usize, inst.w) * inst.h;
    if (inst.bypass >= 0.5) {
        @memcpy(outframe[0..n], inframe[0..n]);
        return;
    }
    const amt = std.math.clamp(inst.amount, 0.0, 1.0);
    for (inframe[0..n], outframe[0..n]) |src, *dst| {
        const b: [4]u8 = @bitCast(src);
        var o: [4]u8 = b;
        inline for (0..3) |i| {
            const inv: f64 = 255.0 - @as(f64, @floatFromInt(b[i]));
            const mixed = @as(f64, @floatFromInt(b[i])) * (1 - amt) + inv * amt;
            o[i] = @intFromFloat(std.math.clamp(mixed, 0, 255));
        }
        dst.* = @bitCast(o);
    }
}

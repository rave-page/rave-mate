//! ISF (Interactive Shader Format, https://isf.video) host - single-pass subset.
//! Header parsing is cross-platform (powers --list); rendering is a hidden-window
//! WGL context + FBO + glReadPixels (Windows only for now). GLSL 1.20 prelude
//! covers the common IMG_* functions; INPUTS float/bool/color/point2D + inputImage.
//! Multi-pass (PASSES), IMPORTED, audio and event inputs are out of scope.
const std = @import("std");
const builtin = @import("builtin");

// ── header parsing (cross-platform) ──

pub const ParamKind = enum { float, boolean, color, point2d };

pub const Param = struct {
    name: []const u8,
    kind: ParamKind,
    def: [4]f64, // float/bool [0]; point2D x,y; color r,g,b,a
};

pub const Doc = struct {
    desc: []const u8,
    credit: []const u8,
    params: []Param,
    body: []const u8, // GLSL after the header comment

    pub fn deinit(d: *Doc, gpa: std.mem.Allocator) void {
        for (d.params) |p| gpa.free(p.name);
        gpa.free(d.params);
        gpa.free(d.desc);
        gpa.free(d.credit);
        gpa.free(d.body);
    }
};

pub const ParseError = error{ NoHeader, BadHeader, MultiPass, Unsupported, OutOfMemory };

const HeaderInput = struct {
    NAME: []const u8 = "",
    TYPE: []const u8 = "",
    DEFAULT: std.json.Value = .null,
    // MIN/MAX intentionally ignored: the starter set is 0..1-normalized
};
const Header = struct {
    DESCRIPTION: []const u8 = "",
    CREDIT: []const u8 = "",
    ISFVSN: []const u8 = "",
    CATEGORIES: []const []const u8 = &.{},
    INPUTS: []const HeaderInput = &.{},
    PASSES: []const std.json.Value = &.{},
    IMPORTED: std.json.Value = .null,
};

fn defScalar(v: std.json.Value, fallback: f64) f64 {
    return switch (v) {
        .integer => |i| @floatFromInt(i),
        .float => |f| f,
        .bool => |b| if (b) 1 else 0,
        else => fallback,
    };
}

fn defVec(v: std.json.Value, comptime n: usize, fallback: [4]f64) [4]f64 {
    var out = fallback;
    switch (v) {
        .array => |arr| {
            for (arr.items, 0..) |item, i| {
                if (i >= n) break;
                out[i] = defScalar(item, out[i]);
            }
        },
        else => {},
    }
    return out;
}

// parse splits `/*{json}*/ glsl…` into a Doc. Caller owns the Doc (deinit).
pub fn parse(gpa: std.mem.Allocator, src: []const u8) ParseError!Doc {
    const open = std.mem.indexOf(u8, src, "/*") orelse return error.NoHeader;
    const close = std.mem.indexOf(u8, src[open..], "*/") orelse return error.NoHeader;
    const raw = std.mem.trim(u8, src[open + 2 .. open + close], " \t\r\n");
    if (raw.len == 0 or raw[0] != '{') return error.NoHeader;

    var arena_state: std.heap.ArenaAllocator = .init(gpa);
    defer arena_state.deinit();
    const hdr = std.json.parseFromSliceLeaky(Header, arena_state.allocator(), raw, .{
        .ignore_unknown_fields = true,
    }) catch return error.BadHeader;
    if (hdr.PASSES.len > 1) return error.MultiPass;
    if (hdr.IMPORTED != .null) return error.Unsupported;

    var params: std.ArrayList(Param) = .empty;
    errdefer {
        for (params.items) |p| gpa.free(p.name);
        params.deinit(gpa);
    }
    for (hdr.INPUTS) |in| {
        if (in.NAME.len == 0) continue;
        if (std.mem.eql(u8, in.TYPE, "image")) continue; // inputImage - implicit
        const kind: ParamKind = if (std.mem.eql(u8, in.TYPE, "float"))
            .float
        else if (std.mem.eql(u8, in.TYPE, "bool"))
            .boolean
        else if (std.mem.eql(u8, in.TYPE, "color"))
            .color
        else if (std.mem.eql(u8, in.TYPE, "point2D"))
            .point2d
        else
            continue; // long/event/audio inputs unsupported - shader defaults apply
        var def: [4]f64 = switch (kind) {
            .color => .{ 0, 0, 0, 1 },
            else => .{ 0, 0, 0, 0 },
        };
        switch (kind) {
            .float, .boolean => def[0] = defScalar(in.DEFAULT, 0),
            .point2d => def = defVec(in.DEFAULT, 2, def),
            .color => def = defVec(in.DEFAULT, 4, def),
        }
        try params.append(gpa, .{ .name = try gpa.dupe(u8, in.NAME), .kind = kind, .def = def });
    }

    return .{
        .desc = try gpa.dupe(u8, hdr.DESCRIPTION),
        .credit = try gpa.dupe(u8, hdr.CREDIT),
        .params = try params.toOwnedSlice(gpa),
        .body = try gpa.dupe(u8, src[open + close + 2 ..]),
    };
}

// ── GL host (Windows / WGL) ──

const is_windows = builtin.os.tag == .windows;

extern "user32" fn RegisterClassExW(*const WNDCLASSEXW) callconv(.winapi) u16;
extern "user32" fn CreateWindowExW(u32, [*:0]const u16, [*:0]const u16, u32, i32, i32, i32, i32, ?*anyopaque, ?*anyopaque, ?*anyopaque, ?*anyopaque) callconv(.winapi) ?*anyopaque;
extern "user32" fn DestroyWindow(*anyopaque) callconv(.winapi) i32;
extern "user32" fn GetDC(*anyopaque) callconv(.winapi) ?*anyopaque;
extern "user32" fn ReleaseDC(*anyopaque, *anyopaque) callconv(.winapi) i32;
extern "user32" fn DefWindowProcW(*anyopaque, u32, usize, isize) callconv(.winapi) isize;
extern "gdi32" fn ChoosePixelFormat(*anyopaque, *const PIXELFORMATDESCRIPTOR) callconv(.winapi) i32;
extern "gdi32" fn SetPixelFormat(*anyopaque, i32, *const PIXELFORMATDESCRIPTOR) callconv(.winapi) i32;
extern "opengl32" fn wglCreateContext(*anyopaque) callconv(.winapi) ?*anyopaque;
extern "opengl32" fn wglDeleteContext(*anyopaque) callconv(.winapi) i32;
extern "opengl32" fn wglMakeCurrent(?*anyopaque, ?*anyopaque) callconv(.winapi) i32;
extern "opengl32" fn wglGetProcAddress([*:0]const u8) callconv(.winapi) ?*anyopaque;

extern "opengl32" fn glViewport(i32, i32, i32, i32) callconv(.winapi) void;
extern "opengl32" fn glGenTextures(i32, [*]u32) callconv(.winapi) void;
extern "opengl32" fn glDeleteTextures(i32, [*]const u32) callconv(.winapi) void;
extern "opengl32" fn glBindTexture(u32, u32) callconv(.winapi) void;
extern "opengl32" fn glTexImage2D(u32, i32, i32, i32, i32, i32, u32, u32, ?*const anyopaque) callconv(.winapi) void;
extern "opengl32" fn glTexParameteri(u32, u32, i32) callconv(.winapi) void;
extern "opengl32" fn glReadPixels(i32, i32, i32, i32, u32, u32, [*]u8) callconv(.winapi) void;
extern "opengl32" fn glPixelStorei(u32, i32) callconv(.winapi) void;
extern "opengl32" fn glBegin(u32) callconv(.winapi) void;
extern "opengl32" fn glEnd() callconv(.winapi) void;
extern "opengl32" fn glVertex2f(f32, f32) callconv(.winapi) void;
extern "opengl32" fn glGetError() callconv(.winapi) u32;

const WNDCLASSEXW = extern struct {
    cbSize: u32,
    style: u32 = 0,
    lpfnWndProc: *const fn (*anyopaque, u32, usize, isize) callconv(.winapi) isize,
    cbClsExtra: i32 = 0,
    cbWndExtra: i32 = 0,
    hInstance: ?*anyopaque = null,
    hIcon: ?*anyopaque = null,
    hCursor: ?*anyopaque = null,
    hbrBackground: ?*anyopaque = null,
    lpszMenuName: ?[*:0]const u16 = null,
    lpszClassName: [*:0]const u16,
    hIconSm: ?*anyopaque = null,
};

const PIXELFORMATDESCRIPTOR = extern struct {
    nSize: u16 = @sizeOf(PIXELFORMATDESCRIPTOR),
    nVersion: u16 = 1,
    dwFlags: u32 = 0,
    iPixelType: u8 = 0,
    cColorBits: u8 = 0,
    cRedBits: u8 = 0,
    cRedShift: u8 = 0,
    cGreenBits: u8 = 0,
    cGreenShift: u8 = 0,
    cBlueBits: u8 = 0,
    cBlueShift: u8 = 0,
    cAlphaBits: u8 = 0,
    cAlphaShift: u8 = 0,
    cAccumBits: u8 = 0,
    cAccumRedBits: u8 = 0,
    cAccumGreenBits: u8 = 0,
    cAccumBlueBits: u8 = 0,
    cAccumAlphaBits: u8 = 0,
    cDepthBits: u8 = 0,
    cStencilBits: u8 = 0,
    cAuxBuffers: u8 = 0,
    iLayerType: u8 = 0,
    bReserved: u8 = 0,
    dwLayerMask: u32 = 0,
    dwVisibleMask: u32 = 0,
    dwDamageMask: u32 = 0,
};

// GL constants (only what we touch)
const GL_TEXTURE_2D: u32 = 0x0DE1;
const GL_RGBA: u32 = 0x1908;
const GL_RGBA8: i32 = 0x8058;
const GL_UNSIGNED_BYTE: u32 = 0x1401;
const GL_TEXTURE_MIN_FILTER: u32 = 0x2801;
const GL_TEXTURE_MAG_FILTER: u32 = 0x2800;
const GL_TEXTURE_WRAP_S: u32 = 0x2802;
const GL_TEXTURE_WRAP_T: u32 = 0x2803;
const GL_LINEAR: i32 = 0x2601;
const GL_CLAMP_TO_EDGE: i32 = 0x812F;
const GL_QUADS: u32 = 0x0007;
const GL_PACK_ALIGNMENT: u32 = 0x0D05;
const GL_UNPACK_ALIGNMENT: u32 = 0x0CF5;
const GL_FRAGMENT_SHADER: u32 = 0x8B30;
const GL_VERTEX_SHADER: u32 = 0x8B31;
const GL_COMPILE_STATUS: u32 = 0x8B81;
const GL_LINK_STATUS: u32 = 0x8B82;
const GL_FRAMEBUFFER: u32 = 0x8D40;
const GL_COLOR_ATTACHMENT0: u32 = 0x8CE0;
const GL_FRAMEBUFFER_COMPLETE: u32 = 0x8CD5;
const GL_TEXTURE0: u32 = 0x84C0;

const GlExt = struct {
    createShader: *const fn (u32) callconv(.winapi) u32,
    shaderSource: *const fn (u32, i32, [*]const [*:0]const u8, ?[*]const i32) callconv(.winapi) void,
    compileShader: *const fn (u32) callconv(.winapi) void,
    getShaderiv: *const fn (u32, u32, *i32) callconv(.winapi) void,
    getShaderInfoLog: *const fn (u32, i32, ?*i32, [*]u8) callconv(.winapi) void,
    createProgram: *const fn () callconv(.winapi) u32,
    attachShader: *const fn (u32, u32) callconv(.winapi) void,
    linkProgram: *const fn (u32) callconv(.winapi) void,
    getProgramiv: *const fn (u32, u32, *i32) callconv(.winapi) void,
    useProgram: *const fn (u32) callconv(.winapi) void,
    deleteShader: *const fn (u32) callconv(.winapi) void,
    deleteProgram: *const fn (u32) callconv(.winapi) void,
    getUniformLocation: *const fn (u32, [*:0]const u8) callconv(.winapi) i32,
    uniform1f: *const fn (i32, f32) callconv(.winapi) void,
    uniform1i: *const fn (i32, i32) callconv(.winapi) void,
    uniform2f: *const fn (i32, f32, f32) callconv(.winapi) void,
    uniform4f: *const fn (i32, f32, f32, f32, f32) callconv(.winapi) void,
    genFramebuffers: *const fn (i32, [*]u32) callconv(.winapi) void,
    deleteFramebuffers: *const fn (i32, [*]const u32) callconv(.winapi) void,
    bindFramebuffer: *const fn (u32, u32) callconv(.winapi) void,
    framebufferTexture2D: *const fn (u32, u32, u32, u32, i32) callconv(.winapi) void,
    checkFramebufferStatus: *const fn (u32) callconv(.winapi) u32,
    activeTexture: *const fn (u32) callconv(.winapi) void,

    fn load() ?GlExt {
        var out: GlExt = undefined;
        inline for (@typeInfo(GlExt).@"struct".fields) |f| {
            const glname = "gl" ++ [1]u8{std.ascii.toUpper(f.name[0])} ++ f.name[1..];
            const p = wglGetProcAddress(glname ++ "") orelse return null;
            @field(out, f.name) = @ptrCast(p);
        }
        return out;
    }
};

fn wndProc(hwnd: *anyopaque, msg: u32, wp: usize, lp: isize) callconv(.winapi) isize {
    return DefWindowProcW(hwnd, msg, wp, lp);
}

pub const HostError = error{ Unsupported, GlInit, Compile, OutOfMemory };

// Host is the process-wide hidden-window GL context (create once, reuse).
pub const Host = if (is_windows) struct {
    hwnd: *anyopaque,
    hdc: *anyopaque,
    ctx: *anyopaque,
    ext: GlExt,

    var registered = false;

    pub fn init() HostError!Host {
        const cls = std.unicode.utf8ToUtf16LeStringLiteral("rvfx-gl");
        if (!registered) {
            var wc: WNDCLASSEXW = .{
                .cbSize = @sizeOf(WNDCLASSEXW),
                .lpfnWndProc = wndProc,
                .lpszClassName = cls,
            };
            _ = RegisterClassExW(&wc); // idempotent enough: duplicate reg fails, window creation decides
            registered = true;
        }
        const hwnd = CreateWindowExW(0, cls, cls, 0, 0, 0, 4, 4, null, null, null, null) orelse return error.GlInit;
        errdefer _ = DestroyWindow(hwnd);
        const hdc = GetDC(hwnd) orelse return error.GlInit;
        errdefer _ = ReleaseDC(hwnd, hdc);

        const pfd: PIXELFORMATDESCRIPTOR = .{
            .dwFlags = 0x00000024, // PFD_DRAW_TO_WINDOW | PFD_SUPPORT_OPENGL
            .iPixelType = 0, // PFD_TYPE_RGBA
            .cColorBits = 32,
            .cAlphaBits = 8,
        };
        const pf = ChoosePixelFormat(hdc, &pfd);
        if (pf == 0 or SetPixelFormat(hdc, pf, &pfd) == 0) return error.GlInit;
        const ctx = wglCreateContext(hdc) orelse return error.GlInit;
        errdefer _ = wglDeleteContext(ctx);
        if (wglMakeCurrent(hdc, ctx) == 0) return error.GlInit;
        const ext = GlExt.load() orelse return error.GlInit;
        return .{ .hwnd = hwnd, .hdc = hdc, .ctx = ctx, .ext = ext };
    }

    pub fn deinit(hst: *Host) void {
        _ = wglMakeCurrent(null, null);
        _ = wglDeleteContext(hst.ctx);
        _ = ReleaseDC(hst.hwnd, hst.hdc);
        _ = DestroyWindow(hst.hwnd);
    }
} else struct {
    pub fn init() HostError!Host {
        return error.Unsupported;
    }
    pub fn deinit(_: *Host) void {}
};

const prelude =
    \\#version 120
    \\uniform sampler2D inputImage;
    \\uniform vec2 RENDERSIZE;
    \\uniform float TIME;
    \\varying vec2 isf_FragNormCoord;
    \\#define IMG_PIXEL(i,p) texture2D((i),(p)/RENDERSIZE)
    \\#define IMG_NORM_PIXEL(i,p) texture2D((i),(p))
    \\#define IMG_THIS_PIXEL(i) texture2D((i),isf_FragNormCoord)
    \\#define IMG_THIS_NORM_PIXEL(i) texture2D((i),isf_FragNormCoord)
    \\#define IMG_SIZE(i) RENDERSIZE
    \\
;

const vertex_src: [*:0]const u8 =
    \\#version 120
    \\varying vec2 isf_FragNormCoord;
    \\void main(){ isf_FragNormCoord = gl_Vertex.xy*0.5+0.5; gl_Position = vec4(gl_Vertex.xy,0.,1.); }
;

// Instance is one compiled shader bound to a frame size.
pub const Instance = if (is_windows) struct {
    host: *Host,
    prog: u32,
    fbo: u32,
    tex_in: u32,
    tex_out: u32,
    w: u32,
    h: u32,
    loc_time: i32,
    flip: []u32, // row-flip staging (GL is bottom-up, frames are top-down)
    uniforms: []Uni,

    const Uni = struct {
        name: []const u8, // duped; freed in destroy
        loc: i32,
        kind: ParamKind,
        val: [4]f64,
    };

    pub fn create(gpa: std.mem.Allocator, host: *Host, doc: *const Doc, w: u32, h: u32) (HostError || error{OutOfMemory})!Instance {
        const e = &host.ext;
        // fragment = prelude + per-param uniforms + body
        var frag: std.ArrayList(u8) = .empty;
        defer frag.deinit(gpa);
        try frag.appendSlice(gpa, prelude);
        for (doc.params) |p| {
            const decl = switch (p.kind) {
                .float => "uniform float ",
                .boolean => "uniform bool ",
                .color => "uniform vec4 ",
                .point2d => "uniform vec2 ",
            };
            try frag.appendSlice(gpa, decl);
            try frag.appendSlice(gpa, p.name);
            try frag.appendSlice(gpa, ";\n");
        }
        try frag.appendSlice(gpa, doc.body);
        try frag.append(gpa, 0);

        const vs = try compile(e, GL_VERTEX_SHADER, vertex_src);
        defer e.deleteShader(vs);
        const fs = try compile(e, GL_FRAGMENT_SHADER, @ptrCast(frag.items.ptr));
        defer e.deleteShader(fs);
        const prog = e.createProgram();
        e.attachShader(prog, vs);
        e.attachShader(prog, fs);
        e.linkProgram(prog);
        var ok: i32 = 0;
        e.getProgramiv(prog, GL_LINK_STATUS, &ok);
        if (ok == 0) {
            e.deleteProgram(prog);
            return error.Compile;
        }

        var texs: [2]u32 = .{ 0, 0 };
        glGenTextures(2, &texs);
        for (texs) |t| {
            glBindTexture(GL_TEXTURE_2D, t);
            glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MIN_FILTER, GL_LINEAR);
            glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MAG_FILTER, GL_LINEAR);
            glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_WRAP_S, GL_CLAMP_TO_EDGE);
            glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_WRAP_T, GL_CLAMP_TO_EDGE);
            glTexImage2D(GL_TEXTURE_2D, 0, GL_RGBA8, @intCast(w), @intCast(h), 0, GL_RGBA, GL_UNSIGNED_BYTE, null);
        }
        var fbo: u32 = 0;
        e.genFramebuffers(1, @ptrCast(&fbo));
        e.bindFramebuffer(GL_FRAMEBUFFER, fbo);
        e.framebufferTexture2D(GL_FRAMEBUFFER, GL_COLOR_ATTACHMENT0, GL_TEXTURE_2D, texs[1], 0);
        if (e.checkFramebufferStatus(GL_FRAMEBUFFER) != GL_FRAMEBUFFER_COMPLETE) {
            e.deleteFramebuffers(1, @ptrCast(&fbo));
            glDeleteTextures(2, &texs);
            e.deleteProgram(prog);
            return error.GlInit;
        }

        var uniforms = try gpa.alloc(Uni, doc.params.len);
        errdefer gpa.free(uniforms);
        var namebuf: [256]u8 = undefined;
        var got: usize = 0;
        errdefer for (uniforms[0..got]) |u| gpa.free(u.name);
        for (doc.params, 0..) |p, i| {
            const nz = std.fmt.bufPrintZ(&namebuf, "{s}", .{p.name}) catch return error.Compile;
            uniforms[i] = .{ .name = try gpa.dupe(u8, p.name), .loc = e.getUniformLocation(prog, nz), .kind = p.kind, .val = p.def };
            got = i + 1;
        }

        const flip = try gpa.alloc(u32, @as(usize, w) * h);
        return .{
            .host = host,
            .prog = prog,
            .fbo = fbo,
            .tex_in = texs[0],
            .tex_out = texs[1],
            .w = w,
            .h = h,
            .loc_time = e.getUniformLocation(prog, "TIME"),
            .flip = flip,
            .uniforms = uniforms,
        };
    }

    fn compile(e: *const GlExt, kind: u32, src: [*:0]const u8) HostError!u32 {
        const sh = e.createShader(kind);
        var one = [1][*:0]const u8{src};
        e.shaderSource(sh, 1, &one, null);
        e.compileShader(sh);
        var ok: i32 = 0;
        e.getShaderiv(sh, GL_COMPILE_STATUS, &ok);
        if (ok == 0) {
            var log: [1024]u8 = undefined;
            var n: i32 = 0;
            e.getShaderInfoLog(sh, log.len, &n, &log);
            if (n > 0) std.debug.print("rave-mate-vfx: shader: {s}\n", .{log[0..@intCast(n)]});
            e.deleteShader(sh);
            return error.Compile;
        }
        return sh;
    }

    pub fn destroy(inst: *Instance, gpa: std.mem.Allocator) void {
        const e = &inst.host.ext;
        e.deleteFramebuffers(1, @ptrCast(&inst.fbo));
        var texs: [2]u32 = .{ inst.tex_in, inst.tex_out };
        glDeleteTextures(2, &texs);
        e.deleteProgram(inst.prog);
        gpa.free(inst.flip);
        for (inst.uniforms) |u| gpa.free(u.name);
        gpa.free(inst.uniforms);
    }

    // setParams applies chain-spec values by uniform name; dotted sub-keys
    // address components ("tint.r", "center.x"). Missing keys keep defaults.
    pub fn setParams(inst: *Instance, spec: *const std.json.ArrayHashMap(f64), buf: []u8) void {
        for (inst.uniforms) |*u| {
            switch (u.kind) {
                .float, .boolean => {
                    if (spec.map.get(u.name)) |v| u.val[0] = v;
                },
                .color => {
                    inline for (.{ "r", "g", "b", "a" }, 0..) |comp, ci| {
                        if (subKey(spec, buf, u.name, comp)) |v| u.val[ci] = v;
                    }
                },
                .point2d => {
                    inline for (.{ "x", "y" }, 0..) |comp, ci| {
                        if (subKey(spec, buf, u.name, comp)) |v| u.val[ci] = v;
                    }
                },
            }
        }
    }

    fn subKey(spec: *const std.json.ArrayHashMap(f64), buf: []u8, name: []const u8, comp: []const u8) ?f64 {
        const key = std.fmt.bufPrint(buf, "{s}.{s}", .{ name, comp }) catch return null;
        return spec.map.get(key);
    }

    // apply runs one frame through the shader; src/dst are w*h RGBA, top-down.
    pub fn apply(inst: *Instance, time: f64, src: []const u32, dst: []u32) void {
        const e = &inst.host.ext;
        const w = inst.w;
        const h = inst.h;

        // top-down → GL bottom-up
        flipRows(inst.flip, src, w, h);
        glBindTexture(GL_TEXTURE_2D, inst.tex_in);
        glPixelStorei(GL_UNPACK_ALIGNMENT, 1);
        glTexImage2D(GL_TEXTURE_2D, 0, GL_RGBA8, @intCast(w), @intCast(h), 0, GL_RGBA, GL_UNSIGNED_BYTE, inst.flip.ptr);

        e.bindFramebuffer(GL_FRAMEBUFFER, inst.fbo);
        glViewport(0, 0, @intCast(w), @intCast(h));
        e.useProgram(inst.prog);
        e.uniform1i(e.getUniformLocation(inst.prog, "inputImage"), 0);
        e.uniform2f(e.getUniformLocation(inst.prog, "RENDERSIZE"), @floatFromInt(w), @floatFromInt(h));
        if (inst.loc_time >= 0) e.uniform1f(inst.loc_time, @floatCast(time));
        for (inst.uniforms) |u| {
            if (u.loc < 0) continue;
            switch (u.kind) {
                .float => e.uniform1f(u.loc, @floatCast(u.val[0])),
                .boolean => e.uniform1i(u.loc, if (u.val[0] >= 0.5) 1 else 0),
                .point2d => e.uniform2f(u.loc, @floatCast(u.val[0]), @floatCast(u.val[1])),
                .color => e.uniform4f(u.loc, @floatCast(u.val[0]), @floatCast(u.val[1]), @floatCast(u.val[2]), @floatCast(u.val[3])),
            }
        }
        e.activeTexture(GL_TEXTURE0);
        glBindTexture(GL_TEXTURE_2D, inst.tex_in);

        glBegin(GL_QUADS);
        glVertex2f(-1, -1);
        glVertex2f(1, -1);
        glVertex2f(1, 1);
        glVertex2f(-1, 1);
        glEnd();

        glPixelStorei(GL_PACK_ALIGNMENT, 1);
        glReadPixels(0, 0, @intCast(w), @intCast(h), GL_RGBA, GL_UNSIGNED_BYTE, @ptrCast(inst.flip.ptr));
        e.bindFramebuffer(GL_FRAMEBUFFER, 0);
        flipRows(dst, inst.flip, w, h);
    }
} else struct {
    pub fn create(_: std.mem.Allocator, _: *Host, _: *const Doc, _: u32, _: u32) (HostError || error{OutOfMemory})!Instance {
        return error.Unsupported;
    }
    pub fn destroy(_: *Instance, _: std.mem.Allocator) void {}
    pub fn setParams(_: *Instance, _: *const std.json.ArrayHashMap(f64), _: []u8) void {}
    pub fn apply(_: *Instance, _: f64, _: []const u32, _: []u32) void {}
};

fn flipRows(dst: []u32, src: []const u32, w: u32, h: u32) void {
    var y: u32 = 0;
    while (y < h) : (y += 1) {
        @memcpy(dst[y * w ..][0..w], src[(h - 1 - y) * w ..][0..w]);
    }
}

test "parse header + params" {
    const src =
        \\/*{
        \\  "DESCRIPTION": "test shader",
        \\  "CREDIT": "rave-mate",
        \\  "INPUTS": [
        \\    {"NAME":"inputImage","TYPE":"image"},
        \\    {"NAME":"amount","TYPE":"float","DEFAULT":0.5},
        \\    {"NAME":"on","TYPE":"bool","DEFAULT":true},
        \\    {"NAME":"tint","TYPE":"color","DEFAULT":[1,0.5,0,1]},
        \\    {"NAME":"center","TYPE":"point2D","DEFAULT":[0.5,0.5]}
        \\  ]
        \\}*/
        \\void main(){ gl_FragColor = IMG_THIS_PIXEL(inputImage); }
    ;
    var doc = try parse(std.testing.allocator, src);
    defer doc.deinit(std.testing.allocator);
    try std.testing.expectEqualStrings("test shader", doc.desc);
    try std.testing.expectEqual(@as(usize, 4), doc.params.len);
    try std.testing.expectEqualStrings("amount", doc.params[0].name);
    try std.testing.expectEqual(ParamKind.float, doc.params[0].kind);
    try std.testing.expectEqual(@as(f64, 0.5), doc.params[0].def[0]);
    try std.testing.expectEqual(@as(f64, 1), doc.params[1].def[0]); // bool true
    try std.testing.expectEqual(ParamKind.color, doc.params[2].kind);
    try std.testing.expectEqual(@as(f64, 0.5), doc.params[2].def[1]);
    try std.testing.expect(std.mem.indexOf(u8, doc.body, "gl_FragColor") != null);
}

test "parse rejects headerless + multipass" {
    try std.testing.expectError(error.NoHeader, parse(std.testing.allocator, "void main(){}"));
    try std.testing.expectError(error.MultiPass, parse(std.testing.allocator,
        \\/*{"PASSES":[{"TARGET":"a"},{}]}*/
        \\void main(){}
    ));
}

test "flipRows roundtrip" {
    const src = [_]u32{ 1, 2, 3, 4, 5, 6 }; // 2x3
    var mid = [_]u32{0} ** 6;
    var out = [_]u32{0} ** 6;
    flipRows(&mid, &src, 2, 3);
    try std.testing.expectEqual(@as(u32, 5), mid[0]);
    flipRows(&out, &mid, 2, 3);
    try std.testing.expectEqualSlices(u32, &src, &out);
}

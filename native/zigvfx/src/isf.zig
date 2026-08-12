//! ISF (Interactive Shader Format, https://isf.video) host.
//! Header parsing is cross-platform (powers --list); rendering is a hidden-window
//! WGL context + FBO + glReadPixels (Windows only for now). GLSL 1.20 prelude
//! covers the common IMG_* functions; INPUTS float/bool/color/point2D + inputImage.
//! Multi-pass PASSES supported: named TARGETs (double-buffered, PERSISTENT keeps
//! contents across frames = feedback), WIDTH/HEIGHT $WIDTH/$HEIGHT expressions,
//! FLOAT buffers (RGBA32F, RGBA8 fallback), PASSINDEX/FRAMEINDEX uniforms.
//! IMPORTED, audio and event inputs are out of scope. IMG_PIXEL/IMG_SIZE use the
//! current pass RENDERSIZE (subset: no per-image size) - normalized sampling
//! (IMG_NORM_PIXEL / isf_FragNormCoord) is exact across differently-sized targets.
const std = @import("std");
const builtin = @import("builtin");

// ── header parsing (cross-platform) ──

pub const ParamKind = enum { float, boolean, color, point2d };

pub const Param = struct {
    name: []const u8,
    kind: ParamKind,
    def: [4]f64, // float/bool [0]; point2D x,y; color r,g,b,a
};

// Pass is one PASSES entry; empty target = renders to the composition output.
pub const Pass = struct {
    target: []const u8,
    persistent: bool,
    float: bool,
    w_expr: []const u8, // "" = full size; else $WIDTH/$HEIGHT arithmetic
    h_expr: []const u8,
};

pub const max_passes = 16;
pub const max_targets = 8; // sampler units 1..8 (0 = inputImage)

pub const Doc = struct {
    desc: []const u8,
    credit: []const u8,
    categories: []const u8, // CATEGORIES joined ", " ("" = none)
    has_input: bool, // inputImage declared - filter; else generator
    params: []Param,
    passes: []Pass, // empty = implicit single output pass
    body: []const u8, // GLSL after the header comment

    pub fn deinit(d: *Doc, gpa: std.mem.Allocator) void {
        for (d.params) |p| gpa.free(p.name);
        gpa.free(d.params);
        for (d.passes) |p| {
            gpa.free(p.target);
            gpa.free(p.w_expr);
            gpa.free(p.h_expr);
        }
        gpa.free(d.passes);
        gpa.free(d.desc);
        gpa.free(d.credit);
        gpa.free(d.categories);
        gpa.free(d.body);
    }
};

pub const ParseError = error{ NoHeader, BadHeader, Unsupported, OutOfMemory };

const HeaderInput = struct {
    NAME: []const u8 = "",
    TYPE: []const u8 = "",
    DEFAULT: std.json.Value = .null,
    // MIN/MAX intentionally ignored: the starter set is 0..1-normalized
};
const HeaderPass = struct {
    TARGET: []const u8 = "",
    PERSISTENT: std.json.Value = .null,
    FLOAT: std.json.Value = .null,
    WIDTH: std.json.Value = .null,
    HEIGHT: std.json.Value = .null,
};
const Header = struct {
    DESCRIPTION: []const u8 = "",
    CREDIT: []const u8 = "",
    ISFVSN: []const u8 = "",
    CATEGORIES: []const []const u8 = &.{},
    INPUTS: []const HeaderInput = &.{},
    PASSES: []const HeaderPass = &.{},
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

fn truthy(v: std.json.Value) bool {
    return switch (v) {
        .bool => |b| b,
        .integer => |i| i != 0,
        .float => |f| f != 0,
        else => false,
    };
}

// dimExpr dupes a WIDTH/HEIGHT header value ("" = full size). Numbers become
// their decimal form so evalDim handles both spellings.
fn dimExpr(gpa: std.mem.Allocator, v: std.json.Value) error{OutOfMemory}![]const u8 {
    return switch (v) {
        .string, .number_string => |s| gpa.dupe(u8, s),
        .integer => |i| std.fmt.allocPrint(gpa, "{d}", .{i}),
        .float => |f| std.fmt.allocPrint(gpa, "{d}", .{f}),
        else => gpa.dupe(u8, ""),
    };
}

fn validIdent(s: []const u8) bool {
    if (s.len == 0 or s.len > 64) return false;
    if (!std.ascii.isAlphabetic(s[0]) and s[0] != '_') return false;
    for (s[1..]) |c| {
        if (!std.ascii.isAlphanumeric(c) and c != '_') return false;
    }
    return true;
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
    if (hdr.PASSES.len > max_passes) return error.BadHeader;
    if (hdr.IMPORTED != .null) return error.Unsupported;

    var passes: std.ArrayList(Pass) = .empty;
    errdefer {
        for (passes.items) |p| {
            gpa.free(p.target);
            gpa.free(p.w_expr);
            gpa.free(p.h_expr);
        }
        passes.deinit(gpa);
    }
    for (hdr.PASSES) |hp| {
        if (hp.TARGET.len > 0 and !validIdent(hp.TARGET)) return error.BadHeader;
        const target = try gpa.dupe(u8, hp.TARGET);
        errdefer gpa.free(target);
        const we = try dimExpr(gpa, hp.WIDTH);
        errdefer gpa.free(we);
        const he = try dimExpr(gpa, hp.HEIGHT);
        errdefer gpa.free(he);
        try passes.append(gpa, .{
            .target = target,
            .persistent = truthy(hp.PERSISTENT),
            .float = truthy(hp.FLOAT),
            .w_expr = we,
            .h_expr = he,
        });
    }

    var params: std.ArrayList(Param) = .empty;
    errdefer {
        for (params.items) |p| gpa.free(p.name);
        params.deinit(gpa);
    }
    var has_input = false;
    for (hdr.INPUTS) |in| {
        if (in.NAME.len == 0) continue;
        // audio-reactive shaders can never render here (no audio source) - reject at
        // parse so --list skips them instead of failing at chain load
        if (std.mem.eql(u8, in.TYPE, "audio") or std.mem.eql(u8, in.TYPE, "audioFFT")) return error.Unsupported;
        // a secondary image input never gets bound - the shader would sample junk
        if (std.mem.eql(u8, in.TYPE, "image") and !std.mem.eql(u8, in.NAME, "inputImage")) return error.Unsupported;
        if (std.mem.eql(u8, in.TYPE, "image")) {
            has_input = true;
            continue; // inputImage - implicit
        }
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

    var cats: std.ArrayList(u8) = .empty;
    errdefer cats.deinit(gpa);
    for (hdr.CATEGORIES, 0..) |cat, i| {
        if (cat.len == 0) continue;
        if (i > 0 and cats.items.len > 0) try cats.appendSlice(gpa, ", ");
        try cats.appendSlice(gpa, cat);
    }

    return .{
        .desc = try gpa.dupe(u8, hdr.DESCRIPTION),
        .credit = try gpa.dupe(u8, hdr.CREDIT),
        .categories = try cats.toOwnedSlice(gpa),
        .has_input = has_input,
        .params = try params.toOwnedSlice(gpa),
        .passes = try passes.toOwnedSlice(gpa),
        .body = try gpa.dupe(u8, src[open + close + 2 ..]),
    };
}

// ── WIDTH/HEIGHT expression evaluator ──

// evalDim evaluates a PASSES WIDTH/HEIGHT expression against the composition
// size: numbers, $WIDTH/$HEIGHT, + - * /, parentheses, floor/ceil/round/abs,
// min/max. Empty or unparseable input falls back to `full`; result rounds and
// clamps to [1, 8192].
pub fn evalDim(expr: []const u8, w: u32, h: u32, full: u32) u32 {
    const trimmed = std.mem.trim(u8, expr, " \t\r\n");
    if (trimmed.len == 0) return full;
    var p: ExprParser = .{ .src = trimmed, .w = @floatFromInt(w), .h = @floatFromInt(h) };
    const v = p.expr(0) catch return full;
    p.skipWs();
    if (p.pos != p.src.len) return full;
    if (!std.math.isFinite(v)) return full;
    return @intFromFloat(std.math.clamp(@round(v), 1, 8192));
}

const ExprParser = struct {
    src: []const u8,
    pos: usize = 0,
    w: f64,
    h: f64,

    const Error = error{Bad};

    fn skipWs(p: *ExprParser) void {
        while (p.pos < p.src.len and (p.src[p.pos] == ' ' or p.src[p.pos] == '\t')) p.pos += 1;
    }

    fn peek(p: *const ExprParser) u8 {
        return if (p.pos < p.src.len) p.src[p.pos] else 0;
    }

    fn expr(p: *ExprParser, depth: u32) Error!f64 {
        if (depth > 16) return error.Bad;
        var v = try p.term(depth);
        while (true) {
            p.skipWs();
            switch (p.peek()) {
                '+' => {
                    p.pos += 1;
                    v += try p.term(depth);
                },
                '-' => {
                    p.pos += 1;
                    v -= try p.term(depth);
                },
                else => return v,
            }
        }
    }

    fn term(p: *ExprParser, depth: u32) Error!f64 {
        var v = try p.unary(depth);
        while (true) {
            p.skipWs();
            switch (p.peek()) {
                '*' => {
                    p.pos += 1;
                    v *= try p.unary(depth);
                },
                '/' => {
                    p.pos += 1;
                    v /= try p.unary(depth);
                },
                else => return v,
            }
        }
    }

    fn unary(p: *ExprParser, depth: u32) Error!f64 {
        p.skipWs();
        if (p.peek() == '-') {
            p.pos += 1;
            return -(try p.unary(depth));
        }
        return p.primary(depth);
    }

    fn primary(p: *ExprParser, depth: u32) Error!f64 {
        p.skipWs();
        const c = p.peek();
        if (c == '(') {
            p.pos += 1;
            const v = try p.expr(depth + 1);
            p.skipWs();
            if (p.peek() != ')') return error.Bad;
            p.pos += 1;
            return v;
        }
        if (c == '$') {
            p.pos += 1;
            if (p.lit("WIDTH")) return p.w;
            if (p.lit("HEIGHT")) return p.h;
            return error.Bad;
        }
        if (std.ascii.isDigit(c) or c == '.') {
            const start = p.pos;
            while (p.pos < p.src.len and (std.ascii.isDigit(p.src[p.pos]) or p.src[p.pos] == '.')) p.pos += 1;
            return std.fmt.parseFloat(f64, p.src[start..p.pos]) catch error.Bad;
        }
        if (std.ascii.isAlphabetic(c)) return p.call(depth);
        return error.Bad;
    }

    fn call(p: *ExprParser, depth: u32) Error!f64 {
        const start = p.pos;
        while (p.pos < p.src.len and (std.ascii.isAlphanumeric(p.src[p.pos]) or p.src[p.pos] == '_')) p.pos += 1;
        const name = p.src[start..p.pos];
        p.skipWs();
        if (p.peek() != '(') return error.Bad;
        p.pos += 1;
        var args: [2]f64 = .{ 0, 0 };
        var n: usize = 0;
        while (true) {
            if (n >= args.len) return error.Bad;
            args[n] = try p.expr(depth + 1);
            n += 1;
            p.skipWs();
            if (p.peek() == ',') {
                p.pos += 1;
                continue;
            }
            break;
        }
        if (p.peek() != ')') return error.Bad;
        p.pos += 1;
        if (std.ascii.eqlIgnoreCase(name, "floor") and n == 1) return @floor(args[0]);
        if (std.ascii.eqlIgnoreCase(name, "ceil") and n == 1) return @ceil(args[0]);
        if (std.ascii.eqlIgnoreCase(name, "round") and n == 1) return @round(args[0]);
        if (std.ascii.eqlIgnoreCase(name, "abs") and n == 1) return @abs(args[0]);
        if (std.ascii.eqlIgnoreCase(name, "min") and n == 2) return @min(args[0], args[1]);
        if (std.ascii.eqlIgnoreCase(name, "max") and n == 2) return @max(args[0], args[1]);
        return error.Bad;
    }

    fn lit(p: *ExprParser, word: []const u8) bool {
        if (p.pos + word.len > p.src.len) return false;
        if (!std.mem.eql(u8, p.src[p.pos..][0..word.len], word)) return false;
        p.pos += word.len;
        return true;
    }
};

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
extern "opengl32" fn glClearColor(f32, f32, f32, f32) callconv(.winapi) void;
extern "opengl32" fn glClear(u32) callconv(.winapi) void;

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
const GL_RGBA32F: i32 = 0x8814;
const GL_COLOR_BUFFER_BIT: u32 = 0x4000;

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
    \\uniform int PASSINDEX;
    \\uniform int FRAMEINDEX;
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
    loc_pass: i32,
    loc_frame: i32,
    flip: []u32, // row-flip staging (GL is bottom-up, frames are top-down)
    uniforms: []Uni,
    targets: []Target,
    pass_targets: []i32, // per pass: targets index, -1 = composition output

    const Uni = struct {
        name: []const u8, // duped; freed in destroy
        loc: i32,
        kind: ParamKind,
        val: [4]f64,
    };

    const Target = struct {
        name: [:0]const u8, // duped; freed in destroy
        tex: [2]u32, // ping-pong: sample [front], render [front^1], swap after pass
        front: u1,
        w: u32,
        h: u32,
        loc: i32, // sampler uniform
    };

    const TargetDesc = struct {
        name: []const u8,
        float: bool,
        w: u32,
        h: u32,
    };

    pub fn create(gpa: std.mem.Allocator, host: *Host, doc: *const Doc, w: u32, h: u32) (HostError || error{OutOfMemory})!Instance {
        const e = &host.ext;

        // pass plan: dedupe named targets, resolve sizes; last pass always
        // renders the composition output at full size (its target, if any, is
        // forced to w*h so read-back stays exact)
        var tds: [max_targets]TargetDesc = undefined;
        var ntd: usize = 0;
        const npass = @max(doc.passes.len, 1);
        var plan: [max_passes]i32 = undefined;
        if (doc.passes.len == 0) {
            plan[0] = -1;
        } else for (doc.passes, 0..) |ps, i| {
            if (ps.target.len == 0) {
                plan[i] = -1;
                continue;
            }
            var ti: ?usize = null;
            for (tds[0..ntd], 0..) |td, k| {
                if (std.mem.eql(u8, td.name, ps.target)) {
                    ti = k;
                    break;
                }
            }
            if (ti == null) {
                if (ntd >= max_targets) return error.Compile;
                tds[ntd] = .{
                    .name = ps.target,
                    .float = ps.float,
                    .w = evalDim(ps.w_expr, w, h, w),
                    .h = evalDim(ps.h_expr, w, h, h),
                };
                ti = ntd;
                ntd += 1;
            }
            plan[i] = @intCast(ti.?);
        }
        if (plan[npass - 1] >= 0) {
            const k: usize = @intCast(plan[npass - 1]);
            tds[k].w = w;
            tds[k].h = h;
        }

        // fragment = prelude + per-target samplers + per-param uniforms + body
        var frag: std.ArrayList(u8) = .empty;
        defer frag.deinit(gpa);
        try frag.appendSlice(gpa, prelude);
        for (tds[0..ntd]) |td| {
            try frag.appendSlice(gpa, "uniform sampler2D ");
            try frag.appendSlice(gpa, td.name);
            try frag.appendSlice(gpa, ";\n");
        }
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
        errdefer e.deleteProgram(prog);
        e.attachShader(prog, vs);
        e.attachShader(prog, fs);
        e.linkProgram(prog);
        var ok: i32 = 0;
        e.getProgramiv(prog, GL_LINK_STATUS, &ok);
        if (ok == 0) return error.Compile;

        var texs: [2]u32 = .{ 0, 0 };
        glGenTextures(2, &texs);
        errdefer glDeleteTextures(2, &texs);
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
        errdefer e.deleteFramebuffers(1, @ptrCast(&fbo));
        e.bindFramebuffer(GL_FRAMEBUFFER, fbo);
        e.framebufferTexture2D(GL_FRAMEBUFFER, GL_COLOR_ATTACHMENT0, GL_TEXTURE_2D, texs[1], 0);
        if (e.checkFramebufferStatus(GL_FRAMEBUFFER) != GL_FRAMEBUFFER_COMPLETE) return error.GlInit;

        var targets = try gpa.alloc(Target, ntd);
        errdefer gpa.free(targets);
        var made: usize = 0;
        errdefer for (targets[0..made]) |*t| {
            glDeleteTextures(2, &t.tex);
            gpa.free(t.name);
        };
        for (tds[0..ntd], 0..) |td, i| {
            const name = try gpa.dupeZ(u8, td.name);
            errdefer gpa.free(name);
            var pair: [2]u32 = undefined;
            pair[0] = try makeTargetTex(e, td.w, td.h, td.float);
            errdefer glDeleteTextures(1, @ptrCast(&pair[0]));
            pair[1] = try makeTargetTex(e, td.w, td.h, td.float);
            targets[i] = .{
                .name = name,
                .tex = pair,
                .front = 0,
                .w = td.w,
                .h = td.h,
                .loc = e.getUniformLocation(prog, name),
            };
            made = i + 1;
        }

        const pass_targets = try gpa.dupe(i32, plan[0..npass]);
        errdefer gpa.free(pass_targets);

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
            .loc_pass = e.getUniformLocation(prog, "PASSINDEX"),
            .loc_frame = e.getUniformLocation(prog, "FRAMEINDEX"),
            .flip = flip,
            .uniforms = uniforms,
            .targets = targets,
            .pass_targets = pass_targets,
        };
    }

    // makeTargetTex allocates one pass-target texture on the bound FBO's
    // context, verifies FBO completeness and clears it to transparent black.
    // FLOAT buffers try RGBA32F and fall back to RGBA8.
    fn makeTargetTex(e: *const GlExt, w: u32, h: u32, float_fmt: bool) HostError!u32 {
        var t: u32 = 0;
        glGenTextures(1, @ptrCast(&t));
        glBindTexture(GL_TEXTURE_2D, t);
        glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MIN_FILTER, GL_LINEAR);
        glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MAG_FILTER, GL_LINEAR);
        glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_WRAP_S, GL_CLAMP_TO_EDGE);
        glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_WRAP_T, GL_CLAMP_TO_EDGE);
        _ = glGetError();
        glTexImage2D(GL_TEXTURE_2D, 0, if (float_fmt) GL_RGBA32F else GL_RGBA8, @intCast(w), @intCast(h), 0, GL_RGBA, GL_UNSIGNED_BYTE, null);
        var fell_back = false;
        if (float_fmt and glGetError() != 0) {
            glTexImage2D(GL_TEXTURE_2D, 0, GL_RGBA8, @intCast(w), @intCast(h), 0, GL_RGBA, GL_UNSIGNED_BYTE, null);
            fell_back = true;
        }
        e.framebufferTexture2D(GL_FRAMEBUFFER, GL_COLOR_ATTACHMENT0, GL_TEXTURE_2D, t, 0);
        if (e.checkFramebufferStatus(GL_FRAMEBUFFER) != GL_FRAMEBUFFER_COMPLETE) {
            if (float_fmt and !fell_back) {
                glBindTexture(GL_TEXTURE_2D, t);
                glTexImage2D(GL_TEXTURE_2D, 0, GL_RGBA8, @intCast(w), @intCast(h), 0, GL_RGBA, GL_UNSIGNED_BYTE, null);
            }
            if (e.checkFramebufferStatus(GL_FRAMEBUFFER) != GL_FRAMEBUFFER_COMPLETE) {
                glDeleteTextures(1, @ptrCast(&t));
                return error.GlInit;
            }
        }
        glViewport(0, 0, @intCast(w), @intCast(h));
        glClearColor(0, 0, 0, 0);
        glClear(GL_COLOR_BUFFER_BIT);
        return t;
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
        for (inst.targets) |*t| {
            glDeleteTextures(2, &t.tex);
            gpa.free(t.name);
        }
        gpa.free(inst.targets);
        gpa.free(inst.pass_targets);
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

    // apply runs one frame through all passes; src/dst are w*h RGBA, top-down.
    // Persistent targets are never cleared, so their contents feed back into
    // the next frame (--pipe keeps one Instance across the whole stream).
    pub fn apply(inst: *Instance, time: f64, fidx: u64, src: []const u32, dst: []u32) void {
        const e = &inst.host.ext;
        const w = inst.w;
        const h = inst.h;

        // top-down → GL bottom-up
        flipRows(inst.flip, src, w, h);
        glBindTexture(GL_TEXTURE_2D, inst.tex_in);
        glPixelStorei(GL_UNPACK_ALIGNMENT, 1);
        glTexImage2D(GL_TEXTURE_2D, 0, GL_RGBA8, @intCast(w), @intCast(h), 0, GL_RGBA, GL_UNSIGNED_BYTE, inst.flip.ptr);

        e.bindFramebuffer(GL_FRAMEBUFFER, inst.fbo);
        e.useProgram(inst.prog);
        e.uniform1i(e.getUniformLocation(inst.prog, "inputImage"), 0);
        for (inst.targets, 0..) |t, k| {
            if (t.loc >= 0) e.uniform1i(t.loc, @intCast(1 + k));
        }
        if (inst.loc_time >= 0) e.uniform1f(inst.loc_time, @floatCast(time));
        if (inst.loc_frame >= 0) e.uniform1i(inst.loc_frame, @intCast(fidx & 0x7fffffff));
        for (inst.uniforms) |u| {
            if (u.loc < 0) continue;
            switch (u.kind) {
                .float => e.uniform1f(u.loc, @floatCast(u.val[0])),
                .boolean => e.uniform1i(u.loc, if (u.val[0] >= 0.5) 1 else 0),
                .point2d => e.uniform2f(u.loc, @floatCast(u.val[0]), @floatCast(u.val[1])),
                .color => e.uniform4f(u.loc, @floatCast(u.val[0]), @floatCast(u.val[1]), @floatCast(u.val[2]), @floatCast(u.val[3])),
            }
        }

        for (inst.pass_targets, 0..) |ti, i| {
            const dw, const dh, const dest = if (ti >= 0) blk: {
                const t = &inst.targets[@intCast(ti)];
                break :blk .{ t.w, t.h, t.tex[t.front ^ 1] };
            } else .{ w, h, inst.tex_out };
            e.framebufferTexture2D(GL_FRAMEBUFFER, GL_COLOR_ATTACHMENT0, GL_TEXTURE_2D, dest, 0);
            glViewport(0, 0, @intCast(dw), @intCast(dh));
            e.uniform2f(e.getUniformLocation(inst.prog, "RENDERSIZE"), @floatFromInt(dw), @floatFromInt(dh));
            if (inst.loc_pass >= 0) e.uniform1i(inst.loc_pass, @intCast(i));
            for (inst.targets, 0..) |t, k| {
                e.activeTexture(GL_TEXTURE0 + @as(u32, @intCast(1 + k)));
                glBindTexture(GL_TEXTURE_2D, t.tex[t.front]);
            }
            e.activeTexture(GL_TEXTURE0);
            glBindTexture(GL_TEXTURE_2D, inst.tex_in);

            glBegin(GL_QUADS);
            glVertex2f(-1, -1);
            glVertex2f(1, -1);
            glVertex2f(1, 1);
            glVertex2f(-1, 1);
            glEnd();

            // the last pass's dest is always w*h (create forces a targeted
            // final pass to full size) - read before the ping-pong swap
            if (i == inst.pass_targets.len - 1) {
                glPixelStorei(GL_PACK_ALIGNMENT, 1);
                glReadPixels(0, 0, @intCast(w), @intCast(h), GL_RGBA, GL_UNSIGNED_BYTE, @ptrCast(inst.flip.ptr));
            }
            if (ti >= 0) inst.targets[@intCast(ti)].front ^= 1;
        }
        e.bindFramebuffer(GL_FRAMEBUFFER, 0);
        flipRows(dst, inst.flip, w, h);
    }
} else struct {
    pub fn create(_: std.mem.Allocator, _: *Host, _: *const Doc, _: u32, _: u32) (HostError || error{OutOfMemory})!Instance {
        return error.Unsupported;
    }
    pub fn destroy(_: *Instance, _: std.mem.Allocator) void {}
    pub fn setParams(_: *Instance, _: *const std.json.ArrayHashMap(f64), _: []u8) void {}
    pub fn apply(_: *Instance, _: f64, _: u64, _: []const u32, _: []u32) void {}
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
        \\  "CATEGORIES": ["Stylize", "Color Effect"],
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
    try std.testing.expectEqualStrings("Stylize, Color Effect", doc.categories);
    try std.testing.expect(doc.has_input);
    try std.testing.expectEqual(@as(usize, 4), doc.params.len);
    try std.testing.expectEqualStrings("amount", doc.params[0].name);
    try std.testing.expectEqual(ParamKind.float, doc.params[0].kind);
    try std.testing.expectEqual(@as(f64, 0.5), doc.params[0].def[0]);
    try std.testing.expectEqual(@as(f64, 1), doc.params[1].def[0]); // bool true
    try std.testing.expectEqual(ParamKind.color, doc.params[2].kind);
    try std.testing.expectEqual(@as(f64, 0.5), doc.params[2].def[1]);
    try std.testing.expect(std.mem.indexOf(u8, doc.body, "gl_FragColor") != null);
}

test "parse rejects headerless + bad target" {
    try std.testing.expectError(error.NoHeader, parse(std.testing.allocator, "void main(){}"));
    try std.testing.expectError(error.BadHeader, parse(std.testing.allocator,
        \\/*{"PASSES":[{"TARGET":"bad name"},{}]}*/
        \\void main(){}
    ));
}

test "parse multipass header" {
    const src =
        \\/*{
        \\  "PASSES": [
        \\    {"TARGET":"bufA","PERSISTENT":true,"FLOAT":1,"WIDTH":"$WIDTH/2.0","HEIGHT":"floor($HEIGHT/2.0)"},
        \\    {"TARGET":"bufB","WIDTH":320,"HEIGHT":240.0},
        \\    {}
        \\  ]
        \\}*/
        \\void main(){ gl_FragColor = vec4(float(PASSINDEX)); }
    ;
    var doc = try parse(std.testing.allocator, src);
    defer doc.deinit(std.testing.allocator);
    try std.testing.expectEqual(@as(usize, 3), doc.passes.len);
    try std.testing.expectEqualStrings("bufA", doc.passes[0].target);
    try std.testing.expect(doc.passes[0].persistent);
    try std.testing.expect(doc.passes[0].float);
    try std.testing.expectEqualStrings("$WIDTH/2.0", doc.passes[0].w_expr);
    try std.testing.expectEqualStrings("floor($HEIGHT/2.0)", doc.passes[0].h_expr);
    try std.testing.expect(!doc.passes[1].persistent);
    try std.testing.expectEqualStrings("320", doc.passes[1].w_expr);
    try std.testing.expectEqualStrings("240", doc.passes[1].h_expr);
    try std.testing.expectEqualStrings("", doc.passes[2].target);
    try std.testing.expectEqualStrings("", doc.passes[2].w_expr);
}

test "parse generator (no inputImage)" {
    var doc = try parse(std.testing.allocator,
        \\/*{"INPUTS":[{"NAME":"speed","TYPE":"float","DEFAULT":0.5}]}*/
        \\void main(){ gl_FragColor = vec4(speed); }
    );
    defer doc.deinit(std.testing.allocator);
    try std.testing.expect(!doc.has_input);
    try std.testing.expectEqualStrings("", doc.categories);
}

test "evalDim expressions" {
    try std.testing.expectEqual(@as(u32, 1920), evalDim("", 1920, 1080, 1920));
    try std.testing.expectEqual(@as(u32, 1920), evalDim("$WIDTH", 1920, 1080, 1));
    try std.testing.expectEqual(@as(u32, 960), evalDim("$WIDTH/2.0", 1920, 1080, 1));
    try std.testing.expectEqual(@as(u32, 360), evalDim("floor($HEIGHT/3.0)", 1920, 1080, 1));
    try std.testing.expectEqual(@as(u32, 640), evalDim("min($WIDTH, 640)", 1920, 1080, 1));
    try std.testing.expectEqual(@as(u32, 1080), evalDim("max($HEIGHT, 32)", 1920, 1080, 1));
    try std.testing.expectEqual(@as(u32, 320), evalDim("320", 1920, 1080, 1));
    try std.testing.expectEqual(@as(u32, 970), evalDim(" ( $WIDTH + 20 ) / 2 ", 1920, 1080, 1));
    // fallback on junk, trailing garbage, div-by-zero, unknown fn
    try std.testing.expectEqual(@as(u32, 77), evalDim("$WIDTH/", 1920, 1080, 77));
    try std.testing.expectEqual(@as(u32, 77), evalDim("$WIDTH 2", 1920, 1080, 77));
    try std.testing.expectEqual(@as(u32, 77), evalDim("$WIDTH/0", 1920, 1080, 77));
    try std.testing.expectEqual(@as(u32, 77), evalDim("pow($WIDTH,2)", 1920, 1080, 77));
    try std.testing.expectEqual(@as(u32, 77), evalDim("$SIZE", 1920, 1080, 77));
    // clamps
    try std.testing.expectEqual(@as(u32, 1), evalDim("0", 1920, 1080, 77));
    try std.testing.expectEqual(@as(u32, 8192), evalDim("99999", 1920, 1080, 77));
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

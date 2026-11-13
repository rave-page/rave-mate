//! rave-mate-keeper - long-lived, single-instance, pure-D3D11 Spout sender host.
//!
//! Why (.devnotes/VRAM_FULL_CARD_POLICY.md 1b): on a card another app keeps ~95 % full, every NEW
//! Spout sender makes Resolume register a new GL/DX interop - the one event that can fail there. The
//! receive destinations therefore live HERE, outside the daemon and the media child: a daemon restart,
//! a self-update or a media-child crash never produces a new sender; the app adopts the existing one
//! (same name, same shared handle) and the decode child keeps writing into it via OpenSharedResource.
//!
//! No GL, no interop, no SpoutLibrary: the sender is the SpoutDX protocol (Spout2 SDK SpoutDX +
//! spoutSenderNames, verified from source): a D3D11 MISC_SHARED B8G8R8A8 texture with a legacy share
//! handle, the "SpoutSenderNames" name list (256-byte slots, sorted set, mutex "SpoutSenderNames_mutex"),
//! the per-sender 280-byte SharedTextureInfo map "<name>" (mutex "<name>_mutex"), "ActiveSenderName",
//! and the access mutex "<name>_SpoutAccessMutex" the decode child already locks.
//!
//! Control: named pipe \\.\pipe\rave-mate-keeper-<session>, one client at a time, newline JSON:
//!   {"op":"hello","proto":1}              -> {"ok":true,"proto":1,"pid":N,"senders":K}
//!   {"op":"ensure","name":..,"w":..,"h":..} -> {"ok":true,"handle":H,"fmt":87,"w":..,"h":..,"created":B}
//!     (an existing sender is ADOPTED as is - nominal geometry is immortal; created=false)
//!   {"op":"list"}                         -> {"ok":true,"senders":[{"name","w","h","handle"}...]}
//!   {"op":"clear","name":..}              -> transparent frame (park), {"ok":true}
//!   {"op":"release","name":..}            -> unregister + free, {"ok":true}
//!   unknown op / malformed                -> {"ok":false,"err":".."} (version tolerant: unknown fields ignored)
//! Lifecycle: started on first need (detached, outside the app's job object); exits after idle_ms with
//! no senders and no client, or at logoff. Bounded: max_senders slots, 4 KiB line buffer.
const std = @import("std");
const d3d = @import("d3d11");

const HANDLE = *anyopaque;
const BOOL = i32;
const DWORD = u32;

extern "kernel32" fn CreateMutexA(?*anyopaque, BOOL, [*:0]const u8) callconv(.winapi) ?HANDLE;
extern "kernel32" fn GetLastError() callconv(.winapi) DWORD;
extern "kernel32" fn WaitForSingleObject(HANDLE, DWORD) callconv(.winapi) DWORD;
extern "kernel32" fn ReleaseMutex(HANDLE) callconv(.winapi) BOOL;
extern "kernel32" fn CloseHandle(HANDLE) callconv(.winapi) BOOL;
extern "kernel32" fn CreateNamedPipeA([*:0]const u8, DWORD, DWORD, DWORD, DWORD, DWORD, DWORD, ?*anyopaque) callconv(.winapi) HANDLE;
extern "kernel32" fn ConnectNamedPipe(HANDLE, ?*anyopaque) callconv(.winapi) BOOL;
extern "kernel32" fn DisconnectNamedPipe(HANDLE) callconv(.winapi) BOOL;
extern "kernel32" fn ReadFile(HANDLE, [*]u8, DWORD, *DWORD, ?*anyopaque) callconv(.winapi) BOOL;
extern "kernel32" fn WriteFile(HANDLE, [*]const u8, DWORD, *DWORD, ?*anyopaque) callconv(.winapi) BOOL;
extern "kernel32" fn CreateFileMappingA(HANDLE, ?*anyopaque, DWORD, DWORD, DWORD, [*:0]const u8) callconv(.winapi) ?HANDLE;
extern "kernel32" fn MapViewOfFile(HANDLE, DWORD, DWORD, DWORD, usize) callconv(.winapi) ?[*]u8;
extern "kernel32" fn UnmapViewOfFile(*const anyopaque) callconv(.winapi) BOOL;
extern "kernel32" fn GetModuleFileNameA(?*anyopaque, [*]u8, DWORD) callconv(.winapi) DWORD;
extern "kernel32" fn GetCurrentProcessId() callconv(.winapi) DWORD;
extern "kernel32" fn ProcessIdToSessionId(DWORD, *DWORD) callconv(.winapi) BOOL;
extern "kernel32" fn GetTickCount64() callconv(.winapi) u64;
extern "kernel32" fn Sleep(DWORD) callconv(.winapi) void;
extern "kernel32" fn ExitProcess(u32) callconv(.winapi) noreturn;
extern "d3d11" fn D3D11CreateDevice(?*anyopaque, u32, ?*anyopaque, u32, ?*const anyopaque, u32, u32, *?*d3d.ID3D11Device, ?*u32, *?*d3d.ID3D11DeviceContext) callconv(.winapi) d3d.HRESULT;

const ERROR_ALREADY_EXISTS: DWORD = 183;
const INVALID_HANDLE: HANDLE = @ptrFromInt(std.math.maxInt(usize));
const PAGE_READWRITE: DWORD = 4;
const FILE_MAP_ALL_ACCESS: DWORD = 0xF001F;
const PIPE_ACCESS_DUPLEX: DWORD = 3;
const PIPE_TYPE_BYTE_WAIT: DWORD = 0; // PIPE_TYPE_BYTE | PIPE_READMODE_BYTE | PIPE_WAIT
const WAIT_OBJECT_0: DWORD = 0;
const WAIT_ABANDONED: DWORD = 0x80;

pub const proto: u32 = 1;
const max_senders = 16; // our destinations (one per remembered receive; medialink caps routes at 8)
const spout_name_len = 256; // SpoutMaxSenderNameLen
const spout_max_senders = 64; // Spout default (registry MaxSenders); the map size is the creator's
const info_size = 280; // sizeof(SharedTextureInfo)
const idle_ms: u64 = 10 * 60 * 1000;
const fmt_bgra: u32 = 87;

// ── pure helpers (unit-tested) ──

/// nameListInsert adds name to a Spout sender-name list buffer (slots of 256 bytes, sorted like the
/// SDK's std::set, terminated by an empty slot when not full). false = full / too long. Idempotent.
pub fn nameListInsert(buf: []u8, name: []const u8) bool {
    if (name.len == 0 or name.len >= spout_name_len) return false;
    const slots = buf.len / spout_name_len;
    var n: usize = 0;
    while (n < slots and buf[n * spout_name_len] != 0) : (n += 1) {
        if (std.mem.eql(u8, slotName(buf, n), name)) return true;
    }
    if (n >= slots) return false;
    var pos: usize = 0;
    while (pos < n and std.mem.order(u8, slotName(buf, pos), name) == .lt) : (pos += 1) {}
    var i = n;
    while (i > pos) : (i -= 1) {
        @memcpy(buf[i * spout_name_len ..][0..spout_name_len], buf[(i - 1) * spout_name_len ..][0..spout_name_len]);
    }
    @memset(buf[pos * spout_name_len ..][0..spout_name_len], 0);
    @memcpy(buf[pos * spout_name_len ..][0..name.len], name);
    if (n + 1 < slots) buf[(n + 1) * spout_name_len] = 0;
    return true;
}

/// nameListRemove removes name (compacting, keeping order). false = absent.
pub fn nameListRemove(buf: []u8, name: []const u8) bool {
    const slots = buf.len / spout_name_len;
    var n: usize = 0;
    while (n < slots and buf[n * spout_name_len] != 0) : (n += 1) {}
    var at: ?usize = null;
    for (0..n) |i| {
        if (std.mem.eql(u8, slotName(buf, i), name)) at = i;
    }
    const p = at orelse return false;
    var i = p;
    while (i + 1 < n) : (i += 1) {
        @memcpy(buf[i * spout_name_len ..][0..spout_name_len], buf[(i + 1) * spout_name_len ..][0..spout_name_len]);
    }
    @memset(buf[(n - 1) * spout_name_len ..][0..spout_name_len], 0);
    return true;
}

fn slotName(buf: []const u8, i: usize) []const u8 {
    const s = buf[i * spout_name_len ..][0..spout_name_len];
    return s[0 .. std.mem.indexOfScalar(u8, s, 0) orelse spout_name_len];
}

/// encodeInfo writes a SharedTextureInfo (x64 layout: 5 x u32, 256-byte description, u32 partnerId).
pub fn encodeInfo(out: *[info_size]u8, share: u32, w: u32, h: u32, fmt: u32, desc: []const u8) void {
    @memset(out, 0);
    std.mem.writeInt(u32, out[0..4], share, .little);
    std.mem.writeInt(u32, out[4..8], w, .little);
    std.mem.writeInt(u32, out[8..12], h, .little);
    std.mem.writeInt(u32, out[12..16], fmt, .little);
    std.mem.writeInt(u32, out[16..20], 0, .little); // usage
    const n = @min(desc.len, 255);
    @memcpy(out[20..][0..n], desc[0..n]);
    std.mem.writeInt(u32, out[276..280], 0, .little); // partnerId: GPU sharing, no CPU mode bits
}

// ── sender table ──

const Sender = struct {
    used: bool = false,
    name: [spout_name_len]u8 = @splat(0),
    name_len: usize = 0,
    w: u32 = 0,
    h: u32 = 0,
    tex: ?*anyopaque = null,
    rtv: ?*anyopaque = null,
    share: u32 = 0,
    info_map: ?HANDLE = null,
    info_view: ?[*]u8 = null,
    access: ?HANDLE = null,

    fn nameSlice(s: *const Sender) []const u8 {
        return s.name[0..s.name_len];
    }
};

var senders: [max_senders]Sender = @splat(.{});
var dev: ?*d3d.ID3D11Device = null;
var ctx: ?*d3d.ID3D11DeviceContext = null;
var names_map: ?HANDLE = null;
var names_view: ?[*]u8 = null;
var names_mtx: ?HANDLE = null;
var active_map: ?HANDLE = null;
var last_activity = std.atomic.Value(u64).init(0);
var client_connected = std.atomic.Value(bool).init(false);

fn liveCount() usize {
    var n: usize = 0;
    for (senders) |s| {
        if (s.used) n += 1;
    }
    return n;
}

fn find(name: []const u8) ?*Sender {
    for (&senders) |*s| {
        if (s.used and std.mem.eql(u8, s.nameSlice(), name)) return s;
    }
    return null;
}

fn ensureDevice() bool {
    if (dev != null) return true;
    var d: ?*d3d.ID3D11Device = null;
    var c: ?*d3d.ID3D11DeviceContext = null;
    const hr = D3D11CreateDevice(null, d3d.D3D_DRIVER_TYPE_HARDWARE, null, d3d.D3D11_CREATE_DEVICE_BGRA_SUPPORT, null, 0, d3d.D3D11_SDK_VERSION, &d, null, &c);
    if (d3d.failed(hr) or d == null or c == null) return false;
    dev, ctx = .{ d, c };
    return true;
}

fn ensureNames() bool {
    if (names_view != null) return true;
    const m = CreateFileMappingA(INVALID_HANDLE, null, PAGE_READWRITE, 0, spout_max_senders * spout_name_len, "SpoutSenderNames") orelse return false;
    const v = MapViewOfFile(m, FILE_MAP_ALL_ACCESS, 0, 0, 0) orelse {
        _ = CloseHandle(m);
        return false;
    };
    names_map, names_view = .{ m, v };
    names_mtx = CreateMutexA(null, 0, "SpoutSenderNames_mutex");
    return true;
}

fn lockNames() bool {
    const m = names_mtx orelse return true;
    const r = WaitForSingleObject(m, 67); // the SDK's own bound
    return r == WAIT_OBJECT_0 or r == WAIT_ABANDONED;
}

fn unlockNames() void {
    if (names_mtx) |m| _ = ReleaseMutex(m);
}

// IDXGIResource: IUnknown(3) + IDXGIObject(4) + GetDevice → GetSharedHandle at 8.
const IID_IDXGIResource = d3d.g(0x035f3ab4, 0x482e, 0x4e50, .{ 0xb4, 0x1f, 0x8a, 0x7f, 0x8b, 0xd8, 0x96, 0x0b });
const IDXGIResource = extern struct {
    v: *const extern struct {
        _p: [8]d3d.VOP,
        GetSharedHandle: *const fn (*anyopaque, *?*anyopaque) callconv(.winapi) d3d.HRESULT,
    },
};

const EnsureErr = error{ NoDevice, Texture, Share, Registry, Full, BadName };

/// ensure returns the sender named name, creating it at w x h if absent (ADOPT otherwise).
fn ensure(name: []const u8, w: u32, h: u32, created: *bool) EnsureErr!*Sender {
    created.* = false;
    if (find(name)) |s| return s;
    if (name.len == 0 or name.len >= spout_name_len - 32) return error.BadName;
    if (w == 0 or h == 0 or w > 16384 or h > 16384) return error.BadName;
    var slot: ?*Sender = null;
    for (&senders) |*s| {
        if (!s.used) {
            slot = s;
            break;
        }
    }
    const s = slot orelse return error.Full;
    if (!ensureDevice()) return error.NoDevice;
    const td = d3d.D3D11_TEXTURE2D_DESC{ .Width = w, .Height = h, .MipLevels = 1, .ArraySize = 1, .Format = fmt_bgra, .SampleCount = 1, .SampleQuality = 0, .Usage = d3d.USAGE_DEFAULT, .BindFlags = d3d.D3D11_BIND_RENDER_TARGET | d3d.D3D11_BIND_SHADER_RESOURCE, .CPUAccessFlags = 0, .MiscFlags = d3d.MISC_SHARED };
    var tex: ?*anyopaque = null;
    if (d3d.failed(dev.?.v.CreateTexture2D(@ptrCast(dev.?), &td, null, &tex)) or tex == null) return error.Texture;
    errdefer d3d.release(tex.?);
    var rraw: ?*anyopaque = null;
    if (d3d.failed(d3d.qi(tex.?, &IID_IDXGIResource, &rraw)) or rraw == null) return error.Share;
    const res: *IDXGIResource = @ptrCast(@alignCast(rraw.?));
    var sh: ?*anyopaque = null;
    const hr = res.v.GetSharedHandle(@ptrCast(res), &sh);
    d3d.release(res);
    if (d3d.failed(hr) or sh == null) return error.Share;
    var rtv: ?*anyopaque = null;
    _ = dev.?.v.CreateRenderTargetView(@ptrCast(dev.?), tex.?, null, &rtv);

    s.* = .{ .used = true, .w = w, .h = h, .tex = tex, .rtv = rtv, .share = @truncate(@intFromPtr(sh.?)) };
    @memcpy(s.name[0..name.len], name);
    s.name_len = name.len;
    clearSender(s); // a new sender starts transparent, never garbage
    if (!register(s)) {
        unregister(s);
        return error.Registry;
    }
    created.* = true;
    return s;
}

fn zname(buf: []u8, a: []const u8, b: []const u8) [*:0]const u8 {
    const z = std.fmt.bufPrintZ(buf, "{s}{s}", .{ a, b }) catch return "";
    return z.ptr;
}

/// register publishes s in the Spout registry (info map first, then the name, so a scanner that sees
/// the name always finds complete info).
fn register(s: *Sender) bool {
    var nb: [spout_name_len + 32]u8 = undefined;
    const name = s.nameSlice();
    const im = CreateFileMappingA(INVALID_HANDLE, null, PAGE_READWRITE, 0, info_size, zname(&nb, name, "")) orelse return false;
    s.info_map = im;
    const iv = MapViewOfFile(im, FILE_MAP_ALL_ACCESS, 0, 0, 0) orelse return false;
    s.info_view = iv;
    var exe: [260]u8 = @splat(0);
    const el = GetModuleFileNameA(null, &exe, exe.len);
    var info: [info_size]u8 = undefined;
    encodeInfo(&info, s.share, s.w, s.h, fmt_bgra, exe[0..el]);
    var mb: [spout_name_len + 32]u8 = undefined;
    const imx = CreateMutexA(null, 0, zname(&mb, name, "_mutex"));
    if (imx) |m| _ = WaitForSingleObject(m, 67);
    @memcpy(iv[0..info_size], &info);
    if (imx) |m| {
        _ = ReleaseMutex(m);
        _ = CloseHandle(m);
    }
    var ab: [spout_name_len + 32]u8 = undefined;
    s.access = CreateMutexA(null, 0, zname(&ab, name, "_SpoutAccessMutex"));
    if (!ensureNames()) return false;
    if (!lockNames()) return false;
    defer unlockNames();
    if (!nameListInsert(names_view.?[0 .. spout_max_senders * spout_name_len], name)) return false;
    setActive(name);
    return true;
}

fn setActive(name: []const u8) void {
    if (active_map == null) active_map = CreateFileMappingA(INVALID_HANDLE, null, PAGE_READWRITE, 0, spout_name_len, "ActiveSenderName");
    const m = active_map orelse return;
    const v = MapViewOfFile(m, FILE_MAP_ALL_ACCESS, 0, 0, 0) orelse return;
    @memset(v[0..spout_name_len], 0);
    @memcpy(v[0..name.len], name);
    _ = UnmapViewOfFile(v);
}

fn unregister(s: *Sender) void {
    if (names_view != null and lockNames()) {
        _ = nameListRemove(names_view.?[0 .. spout_max_senders * spout_name_len], s.nameSlice());
        unlockNames();
    }
    if (s.info_view) |v| _ = UnmapViewOfFile(v);
    if (s.info_map) |m| _ = CloseHandle(m);
    if (s.access) |m| _ = CloseHandle(m);
    if (s.rtv) |r| d3d.release(r);
    if (s.tex) |t| d3d.release(t);
    if (ctx) |c| c.v.Flush(@ptrCast(c));
    s.* = .{};
}

/// clearSender writes one fully transparent frame under the access mutex (bounded wait; a receiver
/// holding the texture just means this park frame is skipped).
fn clearSender(s: *Sender) void {
    const rtv = s.rtv orelse return;
    const c = ctx orelse return;
    var got = true;
    if (s.access) |m| {
        const r = WaitForSingleObject(m, 10);
        got = r == WAIT_OBJECT_0 or r == WAIT_ABANDONED;
    }
    if (!got) return;
    const zero = [4]f32{ 0, 0, 0, 0 };
    c.v.ClearRenderTargetView(@ptrCast(c), rtv, &zero);
    c.v.Flush(@ptrCast(c));
    if (s.access) |m| _ = ReleaseMutex(m);
}

// ── protocol ──

const Cmd = struct {
    op: []const u8 = "",
    proto: u32 = 0,
    name: []const u8 = "",
    w: u32 = 0,
    h: u32 = 0,
};

fn handleLine(line: []const u8, out: []u8) []const u8 {
    var arena = std.heap.ArenaAllocator.init(std.heap.page_allocator);
    defer arena.deinit();
    const cmd = std.json.parseFromSliceLeaky(Cmd, arena.allocator(), line, .{ .ignore_unknown_fields = true }) catch
        return std.fmt.bufPrint(out, "{{\"ok\":false,\"err\":\"malformed\"}}\n", .{}) catch "";
    last_activity.store(GetTickCount64(), .monotonic);
    if (std.mem.eql(u8, cmd.op, "hello")) {
        return std.fmt.bufPrint(out, "{{\"ok\":true,\"proto\":{d},\"pid\":{d},\"senders\":{d}}}\n", .{ proto, GetCurrentProcessId(), liveCount() }) catch "";
    }
    if (std.mem.eql(u8, cmd.op, "ensure")) {
        var created = false;
        const s = ensure(cmd.name, cmd.w, cmd.h, &created) catch |e|
            return std.fmt.bufPrint(out, "{{\"ok\":false,\"err\":\"{s}\"}}\n", .{@errorName(e)}) catch "";
        return std.fmt.bufPrint(out, "{{\"ok\":true,\"handle\":{d},\"fmt\":{d},\"w\":{d},\"h\":{d},\"created\":{}}}\n", .{ s.share, fmt_bgra, s.w, s.h, created }) catch "";
    }
    if (std.mem.eql(u8, cmd.op, "list")) {
        var fbs: usize = 0;
        const head = "{\"ok\":true,\"senders\":[";
        @memcpy(out[0..head.len], head);
        fbs = head.len;
        var first = true;
        for (senders) |s| {
            if (!s.used) continue;
            const item = std.fmt.bufPrint(out[fbs..], "{s}{{\"name\":\"{s}\",\"w\":{d},\"h\":{d},\"handle\":{d}}}", .{ if (first) "" else ",", s.nameSlice(), s.w, s.h, s.share }) catch break;
            fbs += item.len;
            first = false;
        }
        const tail = "]}\n";
        if (fbs + tail.len > out.len) return "";
        @memcpy(out[fbs..][0..tail.len], tail);
        return out[0 .. fbs + tail.len];
    }
    if (std.mem.eql(u8, cmd.op, "clear") or std.mem.eql(u8, cmd.op, "release")) {
        const s = find(cmd.name) orelse return std.fmt.bufPrint(out, "{{\"ok\":false,\"err\":\"unknown sender\"}}\n", .{}) catch "";
        if (cmd.op[0] == 'c') clearSender(s) else unregister(s);
        return std.fmt.bufPrint(out, "{{\"ok\":true}}\n", .{}) catch "";
    }
    return std.fmt.bufPrint(out, "{{\"ok\":false,\"err\":\"unknown op\"}}\n", .{}) catch "";
}

fn serve(pipe: HANDLE) void {
    var buf: [4096]u8 = undefined;
    var fill: usize = 0;
    var out: [8192]u8 = undefined;
    while (true) {
        var got: DWORD = 0;
        if (fill >= buf.len) fill = 0; // over-long line: drop it (bounded buffer)
        if (ReadFile(pipe, buf[fill..].ptr, @intCast(buf.len - fill), &got, null) == 0 or got == 0) return;
        fill += got;
        while (std.mem.indexOfScalar(u8, buf[0..fill], '\n')) |nl| {
            const line = std.mem.trim(u8, buf[0..nl], " \r\t");
            if (line.len > 0) {
                const resp = handleLine(line, &out);
                var wr: DWORD = 0;
                if (resp.len > 0 and WriteFile(pipe, resp.ptr, @intCast(resp.len), &wr, null) == 0) return;
            }
            std.mem.copyForwards(u8, buf[0 .. fill - nl - 1], buf[nl + 1 .. fill]);
            fill -= nl + 1;
        }
    }
}

fn idleWatch() void {
    while (true) {
        Sleep(10_000);
        const quiet = GetTickCount64() -% last_activity.load(.monotonic);
        if (!client_connected.load(.monotonic) and liveCount() == 0 and quiet > idle_ms) ExitProcess(0);
    }
}

pub fn main() void {
    var sid: DWORD = 0;
    _ = ProcessIdToSessionId(GetCurrentProcessId(), &sid);
    var mb: [64]u8 = undefined;
    const mname = std.fmt.bufPrintZ(&mb, "Local\\rave-mate-keeper", .{}) catch return;
    const single = CreateMutexA(null, 0, mname.ptr) orelse return;
    if (GetLastError() == ERROR_ALREADY_EXISTS) return; // single instance per session
    _ = single; // held for the process lifetime
    last_activity.store(GetTickCount64(), .monotonic);
    const t = std.Thread.spawn(.{}, idleWatch, .{}) catch return;
    t.detach();
    var pb: [96]u8 = undefined;
    const pname = std.fmt.bufPrintZ(&pb, "\\\\.\\pipe\\rave-mate-keeper-{d}", .{sid}) catch return;
    while (true) {
        const p = CreateNamedPipeA(pname.ptr, PIPE_ACCESS_DUPLEX, PIPE_TYPE_BYTE_WAIT, 1, 16 * 1024, 16 * 1024, 0, null);
        if (p == INVALID_HANDLE) {
            Sleep(1000);
            continue;
        }
        if (ConnectNamedPipe(p, null) != 0 or GetLastError() == 535) { // 535 = ERROR_PIPE_CONNECTED
            client_connected.store(true, .monotonic);
            serve(p);
            client_connected.store(false, .monotonic);
            last_activity.store(GetTickCount64(), .monotonic);
        }
        _ = DisconnectNamedPipe(p);
        _ = CloseHandle(p);
    }
}

test "name list: sorted insert, idempotent, remove compacts, full + too long refused" {
    var buf: [4 * spout_name_len]u8 = @splat(0);
    try std.testing.expect(nameListInsert(&buf, "rave-mate link B"));
    try std.testing.expect(nameListInsert(&buf, "Arena Out"));
    try std.testing.expect(nameListInsert(&buf, "rave-mate link B")); // idempotent
    try std.testing.expectEqualStrings("Arena Out", slotName(&buf, 0));
    try std.testing.expectEqualStrings("rave-mate link B", slotName(&buf, 1));
    try std.testing.expectEqual(@as(u8, 0), buf[2 * spout_name_len]);
    try std.testing.expect(nameListInsert(&buf, "c"));
    try std.testing.expect(nameListInsert(&buf, "d"));
    try std.testing.expect(!nameListInsert(&buf, "e")); // full
    try std.testing.expect(nameListRemove(&buf, "Arena Out"));
    try std.testing.expectEqualStrings("c", slotName(&buf, 0)); // byte order like std::set<std::string>
    try std.testing.expectEqualStrings("d", slotName(&buf, 1));
    try std.testing.expectEqualStrings("rave-mate link B", slotName(&buf, 2));
    try std.testing.expectEqual(@as(u8, 0), buf[3 * spout_name_len]);
    try std.testing.expect(!nameListRemove(&buf, "nope"));
    var long: [300]u8 = @splat('x');
    try std.testing.expect(!nameListInsert(&buf, &long));
}

test "SharedTextureInfo layout matches the 280-byte SDK struct" {
    var b: [info_size]u8 = undefined;
    encodeInfo(&b, 0x40002A42, 3840, 2160, 87, "C:\\x\\rave-mate-keeper.exe");
    try std.testing.expectEqual(@as(u32, 0x40002A42), std.mem.readInt(u32, b[0..4], .little));
    try std.testing.expectEqual(@as(u32, 3840), std.mem.readInt(u32, b[4..8], .little));
    try std.testing.expectEqual(@as(u32, 2160), std.mem.readInt(u32, b[8..12], .little));
    try std.testing.expectEqual(@as(u32, 87), std.mem.readInt(u32, b[12..16], .little));
    try std.testing.expectEqual(@as(u8, 'C'), b[20]);
    try std.testing.expectEqual(@as(u32, 0), std.mem.readInt(u32, b[276..280], .little));
}

test "protocol: unknown op and malformed lines answer an error, never crash" {
    var out: [512]u8 = undefined;
    try std.testing.expect(std.mem.indexOf(u8, handleLine("{\"op\":\"frobnicate\"}", &out), "unknown op") != null);
    try std.testing.expect(std.mem.indexOf(u8, handleLine("not json", &out), "malformed") != null);
    try std.testing.expect(std.mem.indexOf(u8, handleLine("{\"op\":\"hello\",\"proto\":9,\"extra\":1}", &out), "\"proto\":1") != null);
    try std.testing.expect(std.mem.indexOf(u8, handleLine("{\"op\":\"release\",\"name\":\"none\"}", &out), "unknown sender") != null);
}

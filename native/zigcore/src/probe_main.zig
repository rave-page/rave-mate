//! rave-probe — Zig replacement for the `rave-mate worker probe` child (ZIG_MIGRATION P4).
//! Speaks the same newline-JSON stdio protocol as internal/worker (Request{id,method,params}
//! → Response{id,ok,result|error}), serves every probe.* method, and produces byte-identical
//! peaks/bands payloads (bands.zig kernels, parity-tested vs Go). Opt-in via the daemon
//! config features.workers.probeExe; the Go worker stays authoritative until this soaks.
//!
//! Deviations from the Go worker (documented, daemon-tolerated):
//! - malformed request JSON → error Response (id "") instead of exit 1; sturdier, same wire.
//! - probe.tags / probe.artwork read metadata via ffprobe/ffmpeg instead of the dhowden/tag
//!   Go library — field values may differ on exotic containers; format section fields match.
//!
//! Bounded buffers (cap + policy):
//! - request line: 64 KiB, oversize → drain + error response (drop request, keep serving).
//! - peaks PCM slurp: 1 GiB (~9h @16k mono s16; Go slurps unbounded) → fail "peaks decode".
//! - envelope: streamed in 64 KiB chunks; env accumulator capped 8M f32 (~11h @200Hz) → fail.
//! - ffprobe/artwork/waveform outputs: 64 MiB → fail. No queue grows with input size.

const std = @import("std");
const builtin = @import("builtin");
const bands = @import("bands.zig");

const peaks_rate = 16000; // worker/probe.go peaksRate
const peaks_max_buckets = 60000; // worker/probe.go peaksMaxBuckets
const env_decode_rate = 4000; // worker/probe.go envDecodeRate

const max_request_line = 64 * 1024;
const max_pcm_bytes: usize = 1 << 30;
const max_env_samples = 8_000_000;
const max_tool_out: usize = 64 << 20;

const Ctx = struct {
    gpa: std.mem.Allocator,
    io: std.Io,
    environ: *std.process.Environ.Map,
};

/// Handler outcome: result JSON bytes, or an error message for the Response error field.
const Outcome = union(enum) {
    ok: []const u8,
    err: []const u8,
};

pub fn main(init: std.process.Init) !void {
    const ctx: Ctx = .{ .gpa = init.gpa, .io = init.io, .environ = init.environ_map };

    var in_buf: [max_request_line]u8 = undefined;
    var stdin_reader = std.Io.File.stdin().readerStreaming(ctx.io, &in_buf);
    const in = &stdin_reader.interface;

    var out_buf: [64 * 1024]u8 = undefined;
    var stdout_writer = std.Io.File.stdout().writerStreaming(ctx.io, &out_buf);
    const out = &stdout_writer.interface;

    var arena_state: std.heap.ArenaAllocator = .init(ctx.gpa);
    defer arena_state.deinit();

    while (true) {
        const line = in.takeDelimiter('\n') catch |err| switch (err) {
            error.StreamTooLong => {
                // Oversize request: drain to newline, reply with a protocol error, keep serving.
                _ = in.discardDelimiterInclusive('\n') catch return;
                try respondErr(out, "", "request too large");
                continue;
            },
            error.ReadFailed => return,
        } orelse return; // EOF → clean exit 0 (daemon closed the pipe)
        const trimmed = std.mem.trim(u8, line, " \t\r");
        if (trimmed.len == 0) continue;

        _ = arena_state.reset(.retain_capacity);
        const arena = arena_state.allocator();
        try serveOne(ctx, arena, trimmed, out);
    }
}

const Request = struct {
    id: []const u8 = "",
    method: []const u8 = "",
    params: std.json.Value = .null,
};

fn serveOne(ctx: Ctx, arena: std.mem.Allocator, line: []const u8, out: *std.Io.Writer) !void {
    const parsed = std.json.parseFromSliceLeaky(Request, arena, line, .{
        .ignore_unknown_fields = true,
    }) catch {
        try respondErr(out, "", "malformed request");
        return;
    };
    const outcome = dispatch(ctx, arena, parsed.method, parsed.params) catch |err| switch (err) {
        error.OutOfMemory => Outcome{ .err = "out of memory" },
        else => Outcome{ .err = @errorName(err) },
    };
    switch (outcome) {
        .ok => |result| try respondOk(out, parsed.id, result),
        .err => |msg| try respondErr(out, parsed.id, msg),
    }
}

fn dispatch(ctx: Ctx, arena: std.mem.Allocator, method: []const u8, params: std.json.Value) !Outcome {
    const map = std.static_string_map.StaticStringMap(*const fn (Ctx, std.mem.Allocator, std.json.Value) anyerror!Outcome).initComptime(.{
        .{ "ping", &pingHandler },
        .{ "probe.duration", &durationHandler },
        .{ "probe.streams", &streamsHandler },
        .{ "probe.tags", &tagsHandler },
        .{ "probe.artwork", &artworkHandler },
        .{ "probe.waveform", &waveformHandler },
        .{ "probe.peaks", &peaksHandler },
        .{ "probe.envelope", &envelopeHandler },
    });
    const h = map.get(method) orelse
        return .{ .err = try std.fmt.allocPrint(arena, "unknown method {s}", .{method}) };
    return h(ctx, arena, params);
}

// ── response framing ─────────────────────────────────────────────────────────

fn respondOk(out: *std.Io.Writer, id: []const u8, result_raw: []const u8) !void {
    var s: std.json.Stringify = .{ .writer = out };
    try s.beginObject();
    try s.objectField("id");
    try s.write(id);
    try s.objectField("ok");
    try s.write(true);
    try s.objectField("result");
    try s.beginWriteRaw();
    try out.writeAll(result_raw);
    s.endWriteRaw();
    try s.endObject();
    try out.writeByte('\n');
    try out.flush();
}

fn respondErr(out: *std.Io.Writer, id: []const u8, msg: []const u8) !void {
    var s: std.json.Stringify = .{ .writer = out };
    try s.beginObject();
    try s.objectField("id");
    try s.write(id);
    try s.objectField("ok");
    try s.write(false);
    try s.objectField("error");
    try s.write(msg);
    try s.endObject();
    try out.writeByte('\n');
    try out.flush();
}

// ── param helpers ────────────────────────────────────────────────────────────

fn paramString(params: std.json.Value, key: []const u8) ?[]const u8 {
    if (params != .object) return null;
    const v = params.object.get(key) orelse return null;
    return switch (v) {
        .string => |s| s,
        else => null,
    };
}

fn paramFloat(params: std.json.Value, key: []const u8) f64 {
    if (params != .object) return 0;
    const v = params.object.get(key) orelse return 0;
    return switch (v) {
        .integer => |n| @floatFromInt(n),
        .float => |f| f,
        else => 0,
    };
}

fn paramInt(params: std.json.Value, key: []const u8) i64 {
    if (params != .object) return 0;
    const v = params.object.get(key) orelse return 0;
    return switch (v) {
        .integer => |n| n,
        .float => |f| @intFromFloat(f),
        else => 0,
    };
}

fn requirePath(params: std.json.Value) ?[]const u8 {
    const p = paramString(params, "path") orelse return null;
    if (p.len == 0) return null;
    return p;
}

// ── tool resolution + execution ──────────────────────────────────────────────

/// Mirrors internal/mediatools.Resolve: app-managed <configDir>/bin/<base>[.exe] wins,
/// else the bare name (std.process spawn resolves via parent PATH).
fn resolveTool(ctx: Ctx, arena: std.mem.Allocator, comptime base: []const u8) ![]const u8 {
    const exe_name = if (builtin.os.tag == .windows) base ++ ".exe" else base;
    if (configDir(ctx, arena)) |dir| {
        const managed = try std.fs.path.join(arena, &.{ dir, "bin", exe_name });
        if (std.Io.Dir.cwd().access(ctx.io, managed, .{})) |_| {
            return managed;
        } else |_| {}
    }
    return base;
}

/// Mirrors internal/config.Dir resolution order (without mkdir): RAVE_MATE_CONFIG_DIR
/// override, else the OS user-config dir + "rave-mate".
fn configDir(ctx: Ctx, arena: std.mem.Allocator) ?[]const u8 {
    if (ctx.environ.get("RAVE_MATE_CONFIG_DIR")) |v| {
        if (v.len > 0) return v;
    }
    const base: ?[]const u8 = switch (builtin.os.tag) {
        .windows => ctx.environ.get("APPDATA"),
        .macos => if (ctx.environ.get("HOME")) |h|
            std.fs.path.join(arena, &.{ h, "Library", "Application Support" }) catch null
        else
            null,
        else => if (ctx.environ.get("XDG_CONFIG_HOME")) |x|
            x
        else if (ctx.environ.get("HOME")) |h|
            std.fs.path.join(arena, &.{ h, ".config" }) catch null
        else
            null,
    };
    const b = base orelse return null;
    return std.fs.path.join(arena, &.{ b, "rave-mate" }) catch null;
}

const RunOut = union(enum) {
    ok: []u8, // stdout
    err: []const u8,
};

/// Runs a tool collecting stdout (capped), with a timeout. Non-zero exit → err with the
/// first stderr line. Missing binary → the same "install FFmpeg" hint the Go worker gives.
fn runTool(ctx: Ctx, arena: std.mem.Allocator, comptime tool: []const u8, argv: []const []const u8, timeout_s: i64, stdout_cap: usize) !RunOut {
    const res = std.process.run(arena, ctx.io, .{
        .argv = argv,
        .stdout_limit = .limited(stdout_cap),
        .stderr_limit = .limited(64 * 1024),
        .timeout = .{ .duration = .{ .raw = .fromSeconds(timeout_s), .clock = .awake } },
    }) catch |err| switch (err) {
        error.FileNotFound => return .{ .err = tool ++ " not found (install FFmpeg from Settings → Transcode, or add it to PATH)" },
        error.StreamTooLong => return .{ .err = tool ++ ": output too large" },
        error.Timeout => return .{ .err = tool ++ ": timed out" },
        else => return .{ .err = try std.fmt.allocPrint(arena, tool ++ ": {s}", .{@errorName(err)}) },
    };
    switch (res.term) {
        .exited => |code| if (code == 0) return .{ .ok = res.stdout },
        else => {},
    }
    const line = firstLine(res.stderr);
    if (line.len > 0) return .{ .err = try std.fmt.allocPrint(arena, tool ++ ": {s}", .{line}) };
    return .{ .err = tool ++ " failed" };
}

fn firstLine(s: []const u8) []const u8 {
    const end = std.mem.indexOfScalar(u8, s, '\n') orelse s.len;
    return std.mem.trim(u8, s[0..end], " \t\r");
}

/// trimLine mirrors worker/probe.go: strip trailing newline/CR/space.
fn trimLine(s: []const u8) []const u8 {
    return std.mem.trimEnd(u8, s, "\n\r ");
}

fn b64Alloc(arena: std.mem.Allocator, data: []const u8) ![]const u8 {
    const enc = std.base64.standard.Encoder;
    const buf = try arena.alloc(u8, enc.calcSize(data.len));
    return enc.encode(buf, data);
}

/// Builds a result JSON via a Stringify over an allocating writer.
const ResultBuilder = struct {
    aw: std.Io.Writer.Allocating,
    s: std.json.Stringify,

    fn init(rb: *ResultBuilder, arena: std.mem.Allocator) void {
        rb.aw = .init(arena);
        rb.s = .{ .writer = &rb.aw.writer };
    }

    fn bytes(rb: *ResultBuilder) []const u8 {
        return rb.aw.written();
    }
};

// ── handlers ─────────────────────────────────────────────────────────────────

fn pingHandler(ctx: Ctx, arena: std.mem.Allocator, params: std.json.Value) !Outcome {
    _ = ctx;
    _ = params;
    var rb: ResultBuilder = undefined;
    rb.init(arena);
    try rb.s.beginObject();
    try rb.s.objectField("pong");
    try rb.s.write(true);
    try rb.s.objectField("pid");
    try rb.s.write(currentPid());
    try rb.s.endObject();
    return .{ .ok = rb.bytes() };
}

fn currentPid() u32 {
    return switch (builtin.os.tag) {
        .windows => std.os.windows.GetCurrentProcessId(),
        .linux => @intCast(std.os.linux.getpid()),
        else => 0, // informational only (ping health check)
    };
}

fn durationHandler(ctx: Ctx, arena: std.mem.Allocator, params: std.json.Value) !Outcome {
    const path = requirePath(params) orelse return .{ .err = "missing path" };
    const bin = try resolveTool(ctx, arena, "ffprobe");
    const run = try runTool(ctx, arena, "ffprobe", &.{
        bin, "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", path,
    }, 30, max_tool_out);
    const out = switch (run) {
        .ok => |o| o,
        .err => |e| return .{ .err = e },
    };
    var rb: ResultBuilder = undefined;
    rb.init(arena);
    try rb.s.beginObject();
    try rb.s.objectField("durationSeconds");
    if (std.fmt.parseFloat(f64, trimLine(out))) |secs| {
        try rb.s.write(secs);
    } else |_| {
        try rb.s.write(null);
    }
    try rb.s.endObject();
    return .{ .ok = rb.bytes() };
}

fn streamsHandler(ctx: Ctx, arena: std.mem.Allocator, params: std.json.Value) !Outcome {
    const path = requirePath(params) orelse return .{ .err = "missing path" };
    const bin = try resolveTool(ctx, arena, "ffprobe");
    const run = try runTool(ctx, arena, "ffprobe", &.{
        bin, "-v", "error", "-show_streams", "-show_format", "-of", "json", path,
    }, 30, max_tool_out);
    const out = switch (run) {
        .ok => |o| o,
        .err => |e| return .{ .err = e },
    };
    // Validate, then pass ffprobe's JSON through as the result (already structured).
    _ = std.json.parseFromSliceLeaky(std.json.Value, arena, out, .{}) catch
        return .{ .err = "ffprobe returned non-JSON" };
    return .{ .ok = std.mem.trim(u8, out, " \t\r\n") };
}

/// Case-insensitive lookup in an ffprobe tags object; first non-empty value wins.
fn tagValue(tags: ?std.json.Value, keys: []const []const u8) ?[]const u8 {
    const t = tags orelse return null;
    if (t != .object) return null;
    for (keys) |want| {
        var it = t.object.iterator();
        while (it.next()) |kv| {
            if (!std.ascii.eqlIgnoreCase(kv.key_ptr.*, want)) continue;
            if (kv.value_ptr.* != .string) continue;
            const s = std.mem.trim(u8, kv.value_ptr.string, " \t\r\n");
            if (s.len > 0) return s;
        }
    }
    return null;
}

fn tagFloat(tags: ?std.json.Value, keys: []const []const u8) f64 {
    const s = tagValue(tags, keys) orelse return 0;
    // First whitespace-delimited field, like worker/probe_tags.go rawFloat.
    var it = std.mem.tokenizeAny(u8, s, " \t");
    const head = it.next() orelse return 0;
    return std.fmt.parseFloat(f64, head) catch 0;
}

/// probe.tags via ffprobe (the Go worker uses dhowden/tag + ffprobe; format-section
/// fields match, embedded-tag fields are ffprobe's normalized view).
fn tagsHandler(ctx: Ctx, arena: std.mem.Allocator, params: std.json.Value) !Outcome {
    const path = requirePath(params) orelse return .{ .err = "missing path" };

    var title: []const u8 = "";
    var artist: []const u8 = "";
    var album: []const u8 = "";
    var genre: []const u8 = "";
    var comment: []const u8 = "";
    var key: []const u8 = "";
    var bpm: f64 = 0;
    var release_date: []const u8 = "";
    var duration_sec: f64 = 0;
    var bitrate_bps: i64 = 0;
    var file_size_kb: i64 = 0;

    // Best-effort like the Go handler: ffprobe failure still returns a Track with Path.
    const bin = try resolveTool(ctx, arena, "ffprobe");
    const run = try runTool(ctx, arena, "ffprobe", &.{
        bin, "-v", "error", "-show_format", "-of", "json", path,
    }, 30, max_tool_out);
    if (run == .ok) blk: {
        const doc = std.json.parseFromSliceLeaky(std.json.Value, arena, run.ok, .{}) catch break :blk;
        if (doc != .object) break :blk;
        const format = doc.object.get("format") orelse break :blk;
        if (format != .object) break :blk;
        if (format.object.get("duration")) |d| if (d == .string) {
            duration_sec = std.fmt.parseFloat(f64, d.string) catch 0;
        };
        if (format.object.get("bit_rate")) |v| if (v == .string) {
            bitrate_bps = std.fmt.parseInt(i64, v.string, 10) catch 0;
        };
        if (format.object.get("size")) |v| if (v == .string) {
            file_size_kb = @divTrunc(std.fmt.parseInt(i64, v.string, 10) catch 0, 1024);
        };
        const tags = format.object.get("tags");
        title = tagValue(tags, &.{"title"}) orelse "";
        artist = tagValue(tags, &.{"artist"}) orelse "";
        album = tagValue(tags, &.{"album"}) orelse "";
        genre = tagValue(tags, &.{"genre"}) orelse "";
        comment = tagValue(tags, &.{"comment"}) orelse "";
        key = tagValue(tags, &.{ "TKEY", "key", "initialkey" }) orelse "";
        bpm = tagFloat(tags, &.{ "TBPM", "tmpo", "bpm" });
        if (tagValue(tags, &.{ "date", "year" })) |d| {
            // Year only, like tag.Year(): leading 4 digits.
            var digits: usize = 0;
            while (digits < d.len and std.ascii.isDigit(d[digits])) digits += 1;
            if (digits >= 4) release_date = d[0..4];
        }
    }

    // musiclib.Track JSON shape (field names + order per internal/musiclib/model.go).
    var rb: ResultBuilder = undefined;
    rb.init(arena);
    try rb.s.beginObject();
    try writeStrField(&rb.s, "path", path);
    try writeStrField(&rb.s, "title", title);
    try writeStrField(&rb.s, "artist", artist);
    try writeStrField(&rb.s, "album", album);
    try writeStrField(&rb.s, "genre", genre);
    try writeStrField(&rb.s, "label", "");
    try writeStrField(&rb.s, "comment", comment);
    try writeStrField(&rb.s, "key", key);
    try rb.s.objectField("bpm");
    try rb.s.write(bpm);
    try rb.s.objectField("durationSec");
    try rb.s.write(duration_sec);
    try rb.s.objectField("bitrateBps");
    try rb.s.write(bitrate_bps);
    try rb.s.objectField("fileSizeKB");
    try rb.s.write(file_size_kb);
    try rb.s.objectField("playCount");
    try rb.s.write(0);
    try rb.s.objectField("rating");
    try rb.s.write(0);
    try writeStrField(&rb.s, "importDate", "");
    try writeStrField(&rb.s, "releaseDate", release_date);
    try writeStrField(&rb.s, "lastPlayed", "");
    try rb.s.endObject();
    return .{ .ok = rb.bytes() };
}

fn writeStrField(s: *std.json.Stringify, name: []const u8, value: []const u8) !void {
    try s.objectField(name);
    try s.write(value);
}

fn artworkHandler(ctx: Ctx, arena: std.mem.Allocator, params: std.json.Value) !Outcome {
    const path = requirePath(params) orelse return .{ .err = "missing path" };
    const ffprobe_bin = try resolveTool(ctx, arena, "ffprobe");
    const probe_run = try runTool(ctx, arena, "ffprobe", &.{
        ffprobe_bin, "-v", "error", "-show_streams", "-of", "json", path,
    }, 30, max_tool_out);
    const probe_out = switch (probe_run) {
        .ok => |o| o,
        .err => |e| return .{ .err = e },
    };

    var pic_index: ?i64 = null;
    var mime: []const u8 = "";
    blk: {
        const doc = std.json.parseFromSliceLeaky(std.json.Value, arena, probe_out, .{}) catch break :blk;
        if (doc != .object) break :blk;
        const streams = doc.object.get("streams") orelse break :blk;
        if (streams != .array) break :blk;
        for (streams.array.items) |st| {
            if (st != .object) continue;
            const disp = st.object.get("disposition") orelse continue;
            if (disp != .object) continue;
            const ap = disp.object.get("attached_pic") orelse continue;
            if (ap != .integer or ap.integer != 1) continue;
            const idx = st.object.get("index") orelse continue;
            if (idx != .integer) continue;
            pic_index = idx.integer;
            if (st.object.get("codec_name")) |cn| if (cn == .string) {
                mime = codecMime(arena, cn.string) catch "";
            };
            break;
        }
    }

    const idx = pic_index orelse return noArtwork(arena);
    const ffmpeg_bin = try resolveTool(ctx, arena, "ffmpeg");
    const map_arg = try std.fmt.allocPrint(arena, "0:{d}", .{idx});
    const art_run = try runTool(ctx, arena, "ffmpeg", &.{
        ffmpeg_bin, "-hide_banner", "-loglevel", "error", "-i", path, "-map", map_arg, "-c", "copy", "-f", "image2pipe", "-",
    }, 30, max_tool_out);
    const data = switch (art_run) {
        .ok => |o| o,
        .err => return noArtwork(arena), // unextractable = no art, like tag read failures in Go
    };
    if (data.len == 0) return noArtwork(arena);

    var rb: ResultBuilder = undefined;
    rb.init(arena);
    try rb.s.beginObject();
    try rb.s.objectField("mime");
    try rb.s.write(mime);
    try rb.s.objectField("data");
    try rb.s.write(try b64Alloc(arena, data));
    try rb.s.endObject();
    return .{ .ok = rb.bytes() };
}

fn noArtwork(arena: std.mem.Allocator) !Outcome {
    _ = arena;
    return .{ .ok = "{\"mime\":\"\",\"data\":null}" };
}

fn codecMime(arena: std.mem.Allocator, codec: []const u8) ![]const u8 {
    const map = std.static_string_map.StaticStringMap([]const u8).initComptime(.{
        .{ "mjpeg", "image/jpeg" },
        .{ "jpeg", "image/jpeg" },
        .{ "png", "image/png" },
        .{ "bmp", "image/bmp" },
        .{ "gif", "image/gif" },
        .{ "webp", "image/webp" },
    });
    if (map.get(codec)) |m| return m;
    return std.fmt.allocPrint(arena, "image/{s}", .{codec});
}

fn waveformHandler(ctx: Ctx, arena: std.mem.Allocator, params: std.json.Value) !Outcome {
    const path = requirePath(params) orelse return .{ .err = "missing path" };
    var width = paramInt(params, "width");
    var height = paramInt(params, "height");
    if (width <= 0) width = 800;
    if (height <= 0) height = 120;
    var color = paramString(params, "color") orelse "";
    if (color.len == 0) color = "#F70864";

    const bin = try resolveTool(ctx, arena, "ffmpeg");
    // pid + wall-clock ns, like the Go worker's tmp naming (unique per job).
    const now_ns = std.Io.Clock.now(.real, ctx.io).nanoseconds;
    const tmp = try std.fs.path.join(arena, &.{ tempDir(ctx), try std.fmt.allocPrint(
        arena,
        "ravemate-wave-{d}-{d}.png",
        .{ currentPid(), now_ns },
    ) });
    defer std.Io.Dir.cwd().deleteFile(ctx.io, tmp) catch {};

    const filter = try std.fmt.allocPrint(
        arena,
        "aformat=channel_layouts=mono,showwavespic=s={d}x{d}:colors={s}",
        .{ width, height, color },
    );
    const run = try runTool(ctx, arena, "ffmpeg", &.{
        bin, "-hide_banner", "-loglevel", "error", "-y", "-i", path, "-filter_complex", filter, "-frames:v", "1", tmp,
    }, 60, max_tool_out);
    switch (run) {
        .ok => {},
        .err => |e| return .{ .err = try std.fmt.allocPrint(arena, "waveform: {s}", .{e}) },
    }
    const png = std.Io.Dir.cwd().readFileAlloc(ctx.io, tmp, arena, .limited(max_tool_out)) catch |err|
        return .{ .err = try std.fmt.allocPrint(arena, "waveform: {s}", .{@errorName(err)}) };

    var rb: ResultBuilder = undefined;
    rb.init(arena);
    try rb.s.beginObject();
    try rb.s.objectField("png");
    try rb.s.write(try b64Alloc(arena, png));
    try rb.s.endObject();
    return .{ .ok = rb.bytes() };
}

fn tempDir(ctx: Ctx) []const u8 {
    if (ctx.environ.get("TMP")) |v| if (v.len > 0) return v;
    if (ctx.environ.get("TEMP")) |v| if (v.len > 0) return v;
    if (ctx.environ.get("TMPDIR")) |v| if (v.len > 0) return v;
    return if (builtin.os.tag == .windows) "." else "/tmp";
}

/// Resolve bucket count exactly like worker/probe.go peaksHandler.
fn resolveBuckets(samples: usize, req_buckets: i64, bin_rate_hz: f64) usize {
    var buckets: i64 = req_buckets;
    if (bin_rate_hz > 0) {
        const f: f64 = @round(@as(f64, @floatFromInt(samples)) / peaks_rate * bin_rate_hz);
        buckets = @intFromFloat(f);
    }
    if (buckets <= 0) buckets = 8192;
    if (buckets > peaks_max_buckets) buckets = peaks_max_buckets;
    return @intCast(buckets);
}

/// worker/mediatools CodecLeadSkipMs port.
fn codecLeadSkipMs(codec: []const u8) f64 {
    const map = std.static_string_map.StaticStringMap(f64).initComptime(.{
        .{ "aac", 45 }, // ~2112-sample AAC-LC priming @ 44.1-48k
        .{ "mp3", 25 }, // ~1104-sample LAME encoder+decoder delay @ 44.1k
        .{ "opus", 6 }, // ~312-sample Opus pre-skip @ 48k
    });
    return map.get(codec) orelse 0;
}

fn peaksHandler(ctx: Ctx, arena: std.mem.Allocator, params: std.json.Value) !Outcome {
    const path = requirePath(params) orelse return .{ .err = "missing path" };
    const bin = try resolveTool(ctx, arena, "ffmpeg");
    const rate_str = comptime std.fmt.comptimePrint("{d}", .{peaks_rate});
    const run = try runTool(ctx, arena, "ffmpeg", &.{
        bin, "-hide_banner", "-loglevel", "error", "-i", path, "-map", "a:0", "-ac", "1", "-ar", rate_str, "-f", "s16le", "-",
    }, 120, max_pcm_bytes);
    const pcm = switch (run) {
        .ok => |o| o,
        .err => |e| return .{ .err = try std.fmt.allocPrint(arena, "peaks decode: {s}", .{e}) },
    };
    const samples = pcm.len / 2;
    if (samples == 0) return .{ .err = "no audio decoded" };

    const n = resolveBuckets(samples, paramInt(params, "buckets"), paramFloat(params, "binRateHz"));
    const peaks_buf = try arena.alloc(u8, n);
    const nw = bands.bucketPeaks(pcm, n, peaks_buf);
    const bands_buf = try arena.alloc(u8, 3 * n);
    const nb = bands.bucketBands(pcm, n, peaks_rate, bands_buf);

    // Codec for leadSkipMs (best-effort, like the Go worker).
    var codec_lower: []const u8 = "";
    const ffprobe_bin = try resolveTool(ctx, arena, "ffprobe");
    const codec_run = try runTool(ctx, arena, "ffprobe", &.{
        ffprobe_bin, "-v", "error", "-select_streams", "a:0", "-show_entries", "stream=codec_name", "-of", "default=noprint_wrappers=1:nokey=1", path,
    }, 30, max_tool_out);
    if (codec_run == .ok) {
        codec_lower = try std.ascii.allocLowerString(arena, trimLine(codec_run.ok));
    }

    var rb: ResultBuilder = undefined;
    rb.init(arena);
    try rb.s.beginObject();
    try rb.s.objectField("peaks");
    try rb.s.write(try b64Alloc(arena, peaks_buf[0..nw]));
    try rb.s.objectField("bands");
    try rb.s.write(try b64Alloc(arena, bands_buf[0 .. 3 * nb]));
    try rb.s.objectField("durationSeconds");
    try rb.s.write(@as(f64, @floatFromInt(samples)) / peaks_rate);
    try rb.s.objectField("rate");
    try rb.s.write(peaks_rate);
    try rb.s.objectField("samples");
    try rb.s.write(samples);
    try rb.s.objectField("leadSkipMs");
    try rb.s.write(codecLeadSkipMs(codec_lower));
    try rb.s.endObject();
    return .{ .ok = rb.bytes() };
}

/// Streaming RMS envelope state — float64 math ordered exactly like the Go handler so the
/// f32 output is byte-identical for identical PCM.
const EnvAccum = struct {
    bucket_n: usize,
    sumsq: f64 = 0,
    in_bkt: usize = 0,
    samples: u64 = 0,
    env: std.ArrayList(f32) = .empty,

    fn feedSample(a: *EnvAccum, arena: std.mem.Allocator, raw: i16) !void {
        const v = @as(f64, @floatFromInt(raw)) / 32768.0;
        a.sumsq += v * v;
        a.in_bkt += 1;
        a.samples += 1;
        if (a.in_bkt == a.bucket_n) {
            if (a.env.items.len >= max_env_samples) return error.EnvelopeTooLong;
            try a.env.append(arena, @floatCast(@sqrt(a.sumsq / @as(f64, @floatFromInt(a.bucket_n)))));
            a.sumsq = 0;
            a.in_bkt = 0;
        }
    }
};

fn envelopeHandler(ctx: Ctx, arena: std.mem.Allocator, params: std.json.Value) !Outcome {
    const path = requirePath(params) orelse return .{ .err = "missing path" };
    var rate_hz = paramFloat(params, "rateHz");
    if (rate_hz <= 0) rate_hz = 50;
    if (rate_hz > 200) rate_hz = 200;
    var bucket_n: usize = @intFromFloat(@as(f64, env_decode_rate) / rate_hz); // Go int() truncation
    if (bucket_n < 1) bucket_n = 1;

    const bin = try resolveTool(ctx, arena, "ffmpeg");
    const rate_str = comptime std.fmt.comptimePrint("{d}", .{env_decode_rate});
    var child = std.process.spawn(ctx.io, .{
        .argv = &.{ bin, "-hide_banner", "-loglevel", "error", "-i", path, "-map", "a:0", "-ac", "1", "-ar", rate_str, "-f", "s16le", "-" },
        .stdin = .ignore,
        .stdout = .pipe,
        .stderr = .ignore,
        .create_no_window = true,
    }) catch |err| switch (err) {
        error.FileNotFound => return .{ .err = "ffmpeg not found (install it from Settings → Transcode, or add it to PATH)" },
        else => return .{ .err = try std.fmt.allocPrint(arena, "envelope decode: {s}", .{@errorName(err)}) },
    };
    defer child.kill(ctx.io);

    var acc: EnvAccum = .{ .bucket_n = bucket_n };
    var read_buf: [64 * 1024]u8 = undefined; // bounded stream chunk; env accumulator capped
    var file_reader = child.stdout.?.readerStreaming(ctx.io, &read_buf);
    const r = &file_reader.interface;
    var carry: u8 = 0;
    var has_carry = false;
    var chunk_buf: [64 * 1024]u8 = undefined;
    while (true) {
        const got = r.readSliceShort(&chunk_buf) catch |err| switch (err) {
            error.ReadFailed => break,
        };
        var chunk: []const u8 = chunk_buf[0..got];
        if (has_carry and chunk.len > 0) { // re-join a sample split across reads
            const lo: u16 = carry;
            const hi: u16 = chunk[0];
            acc.feedSample(arena, @bitCast(lo | (hi << 8))) catch
                return .{ .err = "envelope too long" };
            chunk = chunk[1..];
            has_carry = false;
        }
        while (chunk.len >= 2) {
            const lo: u16 = chunk[0];
            const hi: u16 = chunk[1];
            acc.feedSample(arena, @bitCast(lo | (hi << 8))) catch
                return .{ .err = "envelope too long" };
            chunk = chunk[2..];
        }
        if (chunk.len == 1) {
            carry = chunk[0];
            has_carry = true;
        }
        if (got < chunk_buf.len) break; // EOF
    }
    const term = child.wait(ctx.io) catch std.process.Child.Term{ .unknown = 0 };
    if (acc.samples == 0) {
        switch (term) {
            .exited => |code| if (code != 0)
                return .{ .err = try std.fmt.allocPrint(arena, "envelope decode: exit status {d}", .{code}) },
            else => return .{ .err = "envelope decode: ffmpeg terminated" },
        }
        return .{ .err = "no audio decoded" };
    }

    const raw = try arena.alloc(u8, 4 * acc.env.items.len);
    for (acc.env.items, 0..) |v, i| {
        std.mem.writeInt(u32, raw[4 * i ..][0..4], @bitCast(v), .little);
    }
    var rb: ResultBuilder = undefined;
    rb.init(arena);
    try rb.s.beginObject();
    try rb.s.objectField("env");
    try rb.s.write(try b64Alloc(arena, raw));
    try rb.s.objectField("rateHz");
    try rb.s.write(@as(f64, env_decode_rate) / @as(f64, @floatFromInt(bucket_n)));
    try rb.s.objectField("durationSeconds");
    try rb.s.write(@as(f64, @floatFromInt(acc.samples)) / env_decode_rate);
    try rb.s.endObject();
    return .{ .ok = rb.bytes() };
}

// ── tests (pure logic only; process/protocol covered by the Go cross-test) ───

const testing = std.testing;

test "resolveBuckets mirrors Go peaksHandler" {
    // binRateHz sizes to duration: 32000 samples @16k = 2 s → 200 buckets @100 Hz.
    try testing.expectEqual(@as(usize, 200), resolveBuckets(32000, 0, 100));
    // Explicit bucket count wins when no binRateHz.
    try testing.expectEqual(@as(usize, 512), resolveBuckets(32000, 512, 0));
    // Defaults + clamp.
    try testing.expectEqual(@as(usize, 8192), resolveBuckets(32000, 0, 0));
    try testing.expectEqual(@as(usize, peaks_max_buckets), resolveBuckets(100_000_000, 0, 100));
}

test "codecLeadSkipMs table" {
    try testing.expectEqual(@as(f64, 45), codecLeadSkipMs("aac"));
    try testing.expectEqual(@as(f64, 25), codecLeadSkipMs("mp3"));
    try testing.expectEqual(@as(f64, 6), codecLeadSkipMs("opus"));
    try testing.expectEqual(@as(f64, 0), codecLeadSkipMs("flac"));
    try testing.expectEqual(@as(f64, 0), codecLeadSkipMs(""));
}

test "request parse tolerates unknown fields + missing params" {
    const parsed = try std.json.parseFromSliceLeaky(Request, testing.allocator, // arena-free: no nested allocs retained
        "{\"id\":\"7\",\"method\":\"ping\",\"extra\":1}", .{ .ignore_unknown_fields = true });
    try testing.expectEqualStrings("7", parsed.id);
    try testing.expectEqualStrings("ping", parsed.method);
    try testing.expect(parsed.params == .null);
}

test "b64 standard alphabet" {
    var arena_state: std.heap.ArenaAllocator = .init(testing.allocator);
    defer arena_state.deinit();
    const got = try b64Alloc(arena_state.allocator(), &.{ 0xfb, 0xff, 0x00 });
    try testing.expectEqualStrings("+/8A", got); // standard (not url-safe) alphabet, padded
}

test "envelope accumulator matches Go bucket math" {
    var arena_state: std.heap.ArenaAllocator = .init(testing.allocator);
    defer arena_state.deinit();
    var acc: EnvAccum = .{ .bucket_n = 2 };
    // Samples 16384 (0.5) ×2 → RMS 0.5; partial tail dropped like Go.
    try acc.feedSample(arena_state.allocator(), 16384);
    try acc.feedSample(arena_state.allocator(), 16384);
    try acc.feedSample(arena_state.allocator(), 32000);
    try testing.expectEqual(@as(usize, 1), acc.env.items.len);
    try testing.expectApproxEqAbs(@as(f32, 0.5), acc.env.items[0], 1e-7);
    try testing.expectEqual(@as(u64, 3), acc.samples);
}

test "trimLine + firstLine" {
    try testing.expectEqualStrings("1.23", trimLine("1.23\r\n"));
    try testing.expectEqualStrings("err text", firstLine("err text\r\nmore"));
}

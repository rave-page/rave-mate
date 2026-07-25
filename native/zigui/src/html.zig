//! HTML string builder for view renderers. Escaping is byte-exact with Go
//! html.EscapeString (the webui golden contract): & ' < > " → &amp; &#39; &lt; &gt; &#34;.
//! Everything else (incl. non-ASCII UTF-8) passes through untouched.

const std = @import("std");

pub const Html = struct {
    a: std.mem.Allocator,
    b: std.ArrayList(u8),

    pub fn init(a: std.mem.Allocator) Html {
        return .{ .a = a, .b = .empty };
    }

    pub fn deinit(h: *Html) void {
        h.b.deinit(h.a);
    }

    /// raw appends markup verbatim (trusted literals only — never user data).
    pub fn raw(h: *Html, s: []const u8) !void {
        try h.b.appendSlice(h.a, s);
    }

    /// esc appends s with Go html.EscapeString-identical escaping.
    pub fn esc(h: *Html, s: []const u8) !void {
        for (s) |c| switch (c) {
            '&' => try h.b.appendSlice(h.a, "&amp;"),
            '\'' => try h.b.appendSlice(h.a, "&#39;"),
            '<' => try h.b.appendSlice(h.a, "&lt;"),
            '>' => try h.b.appendSlice(h.a, "&gt;"),
            '"' => try h.b.appendSlice(h.a, "&#34;"),
            else => try h.b.append(h.a, c),
        };
    }

    /// attrQ appends `"` + escaped s + `"` (components.go attrQ contract).
    pub fn attrQ(h: *Html, s: []const u8) !void {
        try h.b.append(h.a, '"');
        try h.esc(s);
        try h.b.append(h.a, '"');
    }

    /// toOwnedSlice hands the buffer to the caller; the Html resets to empty.
    pub fn toOwnedSlice(h: *Html) ![]u8 {
        return h.b.toOwnedSlice(h.a);
    }
};

test "esc matches Go html.EscapeString semantics" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try h.esc("a&b<c>d\"e'f");
    try std.testing.expectEqualStrings("a&amp;b&lt;c&gt;d&#34;e&#39;f", h.b.items);
}

test "esc passes UTF-8 through untouched" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try h.esc("größer 🎧 ラヴ");
    try std.testing.expectEqualStrings("größer 🎧 ラヴ", h.b.items);
}

test "esc does not double-escape entity output" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try h.esc("&amp;");
    try std.testing.expectEqualStrings("&amp;amp;", h.b.items);
}

test "attrQ quotes and escapes" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try h.attrQ("x\"y");
    try std.testing.expectEqualStrings("\"x&#34;y\"", h.b.items);
}

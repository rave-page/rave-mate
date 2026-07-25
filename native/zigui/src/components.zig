//! Ports of internal/webui/components.go helpers used by migrated tabs.
//! Byte-exact markup contract: extend by porting the Go helper verbatim — never
//! restyle here. Variant/class strings are trusted literals (raw), dynamic text
//! is always escaped, matching the Go originals.

const std = @import("std");
const Html = @import("html.zig").Html;

/// panel: titled page header (page-title + optional subtitle).
pub fn panel(h: *Html, title: []const u8, sub: []const u8) !void {
    try h.raw("<h1 class=page-title>");
    try h.esc(title);
    try h.raw("</h1>");
    if (sub.len != 0) {
        try h.raw("<p class=page-sub>");
        try h.esc(sub);
        try h.raw("</p>");
    }
}

/// emptyState: the rp-empty placeholder.
pub fn emptyState(h: *Html, msg: []const u8) !void {
    try h.raw("<div class=\"rp-empty\"><div class=\"rp-empty__title\">");
    try h.esc(msg);
    try h.raw("</div></div>");
}

/// badge: rp-badge. Empty variant defaults to "secondary" (Go badge parity).
pub fn badge(h: *Html, text: []const u8, variant: []const u8) !void {
    const v = if (variant.len == 0) "secondary" else variant;
    try h.raw("<span class=\"rp-badge rp-badge--");
    try h.raw(v);
    try h.raw("\">");
    try h.esc(text);
    try h.raw("</span>");
}

/// dot: small status dot (color via variant → CSS var).
pub fn dot(h: *Html, variant: []const u8) !void {
    try h.raw("<span class=\"dot dot--");
    try h.raw(variant);
    try h.raw("\"></span>");
}

/// badgeDot: status dot + badge pair (render_appgroups.go badgeDot).
pub fn badgeDot(h: *Html, text: []const u8, variant: []const u8) !void {
    try dot(h, variant);
    try h.raw(" ");
    try badge(h, text, variant);
}

/// btn: rp-btn. Empty variant defaults to "outline"; act/val become escaped
/// data-act/data-val; empty act = plain (non-action) button (Go btn parity).
pub fn btn(h: *Html, label: []const u8, variant: []const u8, act: []const u8, val: []const u8) !void {
    const v = if (variant.len == 0) "outline" else variant;
    try h.raw("<button class=\"rp-btn rp-btn--");
    try h.raw(v);
    try h.raw("\"");
    if (act.len != 0) {
        try h.raw(" data-act=\"");
        try h.esc(act);
        try h.raw("\"");
        if (val.len != 0) {
            try h.raw(" data-val=\"");
            try h.esc(val);
            try h.raw("\"");
        }
    }
    try h.raw(">");
    try h.esc(label);
    try h.raw("</button>");
}

/// btnRowOpen/btnRowClose bracket buttons horizontally (Go btnRow, streaming form).
pub fn btnRowOpen(h: *Html) !void {
    try h.raw("<div class=btn-row>");
}

pub fn btnRowClose(h: *Html) !void {
    try h.raw("</div>");
}

test "panel with and without sub" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try panel(&h, "T<x>", "");
    try std.testing.expectEqualStrings("<h1 class=page-title>T&lt;x&gt;</h1>", h.b.items);
    h.b.clearRetainingCapacity();
    try panel(&h, "T", "S&s");
    try std.testing.expectEqualStrings("<h1 class=page-title>T</h1><p class=page-sub>S&amp;s</p>", h.b.items);
}

test "btn escapes act, omits empty val, defaults variant" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try btn(&h, "Go", "", "a\"b", "");
    try std.testing.expectEqualStrings("<button class=\"rp-btn rp-btn--outline\" data-act=\"a&#34;b\">Go</button>", h.b.items);
    h.b.clearRetainingCapacity();
    try btn(&h, "L", "go", "act", "v'1");
    try std.testing.expectEqualStrings("<button class=\"rp-btn rp-btn--go\" data-act=\"act\" data-val=\"v&#39;1\">L</button>", h.b.items);
}

test "badge default variant" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try badge(&h, "x", "");
    try std.testing.expectEqualStrings("<span class=\"rp-badge rp-badge--secondary\">x</span>", h.b.items);
}

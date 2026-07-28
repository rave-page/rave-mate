//! Real window host: Win32 top-level window + WebView2 via COM, PSH1 child side (mirrors
//! internal/webui/shell_cgo.go + shell_proc_child.go + sizemove_windows.go observable behavior).
//! Loader = the same two-step webview_go ships: WebView2Loader.dll if present, else the Evergreen
//! runtime's EmbeddedBrowserWebView.dll internal entry (registry ClientState\{stable} EBWebView).
//!
//! CRITICAL (B5 black-screenshot lesson): the daemon spawns this child with sysexec.Hide, so the
//! process's first top-level window inherits SW_HIDE. The window is therefore created HIDDEN on
//! purpose and revealed with SW_SHOWNOACTIVATE on first ready (unless startHidden) - reveal is
//! what makes the UI visible AND PrintWindow captures non-black.

const std = @import("std");
const wire = @import("wire.zig");
const sync = @import("sync.zig");
const png = @import("png.zig");

// ── Win32 ────────────────────────────────────────────────────────────────────

const HWND = usize;

const RECT = extern struct { left: i32, top: i32, right: i32, bottom: i32 };
const POINT = extern struct { x: i32, y: i32 };
const MSG = extern struct {
    hwnd: HWND,
    message: u32,
    wParam: usize,
    lParam: isize,
    time: u32,
    pt: POINT,
};

const WNDCLASSEXW = extern struct {
    cbSize: u32,
    style: u32,
    lpfnWndProc: *const fn (HWND, u32, usize, isize) callconv(.winapi) isize,
    cbClsExtra: i32,
    cbWndExtra: i32,
    hInstance: ?*anyopaque,
    hIcon: ?*anyopaque,
    hCursor: ?*anyopaque,
    hbrBackground: ?*anyopaque,
    lpszMenuName: ?[*:0]const u16,
    lpszClassName: [*:0]const u16,
    hIconSm: ?*anyopaque,
};

extern "user32" fn RegisterClassExW(*const WNDCLASSEXW) callconv(.winapi) u16;
extern "user32" fn CreateWindowExW(u32, [*:0]const u16, [*:0]const u16, u32, i32, i32, i32, i32, HWND, ?*anyopaque, ?*anyopaque, ?*anyopaque) callconv(.winapi) HWND;
extern "user32" fn DefWindowProcW(HWND, u32, usize, isize) callconv(.winapi) isize;
extern "user32" fn DestroyWindow(HWND) callconv(.winapi) i32;
extern "user32" fn ShowWindow(HWND, i32) callconv(.winapi) i32;
extern "user32" fn SetForegroundWindow(HWND) callconv(.winapi) i32;
extern "user32" fn PostMessageW(HWND, u32, usize, isize) callconv(.winapi) i32;
extern "user32" fn PostQuitMessage(i32) callconv(.winapi) void;
extern "user32" fn GetMessageW(*MSG, HWND, u32, u32) callconv(.winapi) i32;
extern "user32" fn TranslateMessage(*const MSG) callconv(.winapi) i32;
extern "user32" fn DispatchMessageW(*const MSG) callconv(.winapi) isize;
extern "user32" fn SetWindowPos(HWND, HWND, i32, i32, i32, i32, u32) callconv(.winapi) i32;
extern "user32" fn GetWindowRect(HWND, *RECT) callconv(.winapi) i32;
extern "user32" fn GetClientRect(HWND, *RECT) callconv(.winapi) i32;
extern "user32" fn GetDpiForWindow(HWND) callconv(.winapi) u32;
extern "user32" fn AdjustWindowRectExForDpi(*RECT, u32, i32, u32, u32) callconv(.winapi) i32;
extern "user32" fn SetProcessDpiAwarenessContext(isize) callconv(.winapi) i32;
extern "user32" fn SetTimer(HWND, usize, u32, ?*anyopaque) callconv(.winapi) usize;
extern "user32" fn LoadCursorW(?*anyopaque, usize) callconv(.winapi) ?*anyopaque;
extern "user32" fn GetDC(HWND) callconv(.winapi) ?*anyopaque;
extern "user32" fn ReleaseDC(HWND, ?*anyopaque) callconv(.winapi) i32;
extern "user32" fn PrintWindow(HWND, ?*anyopaque, u32) callconv(.winapi) i32;
extern "user32" fn MoveWindow(HWND, i32, i32, i32, i32, i32) callconv(.winapi) i32;
extern "user32" fn UpdateWindow(HWND) callconv(.winapi) i32;
extern "user32" fn LoadImageW(?*anyopaque, usize, u32, i32, i32, u32) callconv(.winapi) ?*anyopaque;
extern "user32" fn GetSystemMetrics(i32) callconv(.winapi) i32;
extern "user32" fn SendMessageW(HWND, u32, usize, isize) callconv(.winapi) isize;

extern "gdi32" fn CreateCompatibleDC(?*anyopaque) callconv(.winapi) ?*anyopaque;
extern "gdi32" fn CreateCompatibleBitmap(?*anyopaque, i32, i32) callconv(.winapi) ?*anyopaque;
extern "gdi32" fn SelectObject(?*anyopaque, ?*anyopaque) callconv(.winapi) ?*anyopaque;
extern "gdi32" fn DeleteDC(?*anyopaque) callconv(.winapi) i32;
extern "gdi32" fn DeleteObject(?*anyopaque) callconv(.winapi) i32;
extern "gdi32" fn GetDIBits(?*anyopaque, ?*anyopaque, u32, u32, ?*anyopaque, *BitmapInfo, u32) callconv(.winapi) i32;

extern "kernel32" fn GetModuleHandleW(?[*:0]const u16) callconv(.winapi) ?*anyopaque;
extern "kernel32" fn LoadLibraryW([*:0]const u16) callconv(.winapi) ?*anyopaque;
extern "kernel32" fn GetProcAddress(?*anyopaque, [*:0]const u8) callconv(.winapi) ?*anyopaque;
extern "kernel32" fn SetEnvironmentVariableW([*:0]const u16, ?[*:0]const u16) callconv(.winapi) i32;
extern "kernel32" fn GetEnvironmentVariableW([*:0]const u16, ?[*]u16, u32) callconv(.winapi) u32;
extern "kernel32" fn SetPriorityClass(?*anyopaque, u32) callconv(.winapi) i32;
extern "kernel32" fn GetCurrentProcess() callconv(.winapi) ?*anyopaque;
extern "kernel32" fn ExitProcess(u32) callconv(.winapi) noreturn;
extern "kernel32" fn CreateDirectoryW([*:0]const u16, ?*anyopaque) callconv(.winapi) i32;
extern "kernel32" fn CreateFileW([*:0]const u16, u32, u32, ?*anyopaque, u32, u32, ?*anyopaque) callconv(.winapi) ?*anyopaque;
extern "kernel32" fn WriteFile(?*anyopaque, [*]const u8, u32, ?*u32, ?*anyopaque) callconv(.winapi) i32;
extern "kernel32" fn CloseHandle(?*anyopaque) callconv(.winapi) i32;
extern "kernel32" fn GetTickCount64() callconv(.winapi) u64;

extern "advapi32" fn RegOpenKeyExW(usize, [*:0]const u16, u32, u32, *usize) callconv(.winapi) i32;
extern "advapi32" fn RegQueryValueExW(usize, [*:0]const u16, ?*u32, ?*u32, ?[*]u8, *u32) callconv(.winapi) i32;
extern "advapi32" fn RegCloseKey(usize) callconv(.winapi) i32;

extern "ole32" fn CoInitializeEx(?*anyopaque, u32) callconv(.winapi) i32;
extern "ole32" fn CoTaskMemFree(?*anyopaque) callconv(.winapi) void;

extern "shell32" fn SetCurrentProcessExplicitAppUserModelID([*:0]const u16) callconv(.winapi) i32;
extern "shell32" fn SHGetPropertyStoreForWindow(HWND, *const GUID, *?*IPropertyStore) callconv(.winapi) i32;

const GUID = extern struct { d1: u32, d2: u16, d3: u16, d4: [8]u8 };
const PROPERTYKEY = extern struct { fmtid: GUID, pid: u32 };
/// x64 PROPVARIANT: 8-byte header + 16-byte union. Only VT_LPWSTR is used, in the first word.
const PROPVARIANT = extern struct { vt: u16, r1: u16 = 0, r2: u16 = 0, r3: u16 = 0, val: usize, pad: usize = 0 };

const IPropertyStoreVtbl = extern struct {
    QueryInterface: *const fn (*anyopaque, *const GUID, *?*anyopaque) callconv(.winapi) i32,
    AddRef: *const fn (*anyopaque) callconv(.winapi) u32,
    Release: *const fn (*anyopaque) callconv(.winapi) u32,
    GetCount: *const fn (*anyopaque, *u32) callconv(.winapi) i32,
    GetAt: *const fn (*anyopaque, u32, *PROPERTYKEY) callconv(.winapi) i32,
    GetValue: *const fn (*anyopaque, *const PROPERTYKEY, *PROPVARIANT) callconv(.winapi) i32,
    SetValue: *const fn (*anyopaque, *const PROPERTYKEY, *const PROPVARIANT) callconv(.winapi) i32,
    Commit: *const fn (*anyopaque) callconv(.winapi) i32,
};
const IPropertyStore = extern struct { vtbl: *const IPropertyStoreVtbl };

const iid_property_store: GUID = .{ .d1 = 0x886D8EEB, .d2 = 0x8CF2, .d3 = 0x4446, .d4 = .{ 0x8D, 0x02, 0xCD, 0xBA, 0x1D, 0xBD, 0xCF, 0x99 } };
const pkey_app_user_model_id: PROPERTYKEY = .{ // System.AppUserModel.ID
    .fmtid = .{ .d1 = 0x9F4C2855, .d2 = 0x9F79, .d3 = 0x4B39, .d4 = .{ 0xA8, 0xD0, 0xE1, 0xD4, 0x2D, 0xE1, 0xD5, 0xF3 } },
    .pid = 5,
};
const vt_lpwstr: u16 = 31;

const BitmapInfoHeader = extern struct {
    biSize: u32,
    biWidth: i32,
    biHeight: i32,
    biPlanes: u16,
    biBitCount: u16,
    biCompression: u32,
    biSizeImage: u32,
    biXPelsPerMeter: i32,
    biYPelsPerMeter: i32,
    biClrUsed: u32,
    biClrImportant: u32,
};
const BitmapInfo = extern struct {
    header: BitmapInfoHeader,
    colors: [1]u32,
};

const ws_overlappedwindow: u32 = 0x00CF0000;
const sw_hide = 0;
const sw_shownoactivate = 4;
const sw_show = 5;
const wm_app_frame: u32 = 0x8000 + 1; // WM_APP+1: one boxed UiMsg in lParam
const wm_timer: u32 = 0x0113;
const beat_timer_id: usize = 1;
const below_normal_priority: u32 = 0x4000;
const normal_priority: u32 = 0x20;

// Brand icon (rave-shell.rc id 1 == internal/webui appIconResID).
const app_icon_res_id: usize = 1;
const image_icon: u32 = 1; // IMAGE_ICON
const lr_defaultsize: u32 = 0x40; // LR_DEFAULTSIZE
const sm_cxicon = 11;
const sm_cyicon = 12;
const sm_cxsmicon = 49;
const sm_cysmicon = 50;
const wm_seticon: u32 = 0x0080;
const icon_small: usize = 0; // title bar
const icon_big: usize = 1; // Alt-Tab / taskbar

/// app_user_model_id is the taskbar + notification identity of the WHOLE app. It MUST match
/// rave-mate.exe's (internal/app/appid_windows.go) and the AppID stamped on the Start Menu
/// shortcut, or Windows counts this window as a different app than the pinned launcher and adds a
/// SECOND taskbar button. Absent an explicit id Windows derives one from the exe path - and this
/// exe is staged as rave-shell-<embed-hash>.exe, so the derived identity would change on EVERY
/// update and no pin could ever match it.
const app_user_model_id = std.unicode.utf8ToUtf16LeStringLiteral("RavePage.RaveMate");

/// setAppIdentity pins this process's AppUserModelID. Must run before the first window exists.
pub fn setAppIdentity() void {
    _ = SetCurrentProcessExplicitAppUserModelID(app_user_model_id);
}

/// setWindowAppIdentity stamps the id on the WINDOW too. Belt and braces with real value: the
/// window property beats the process default in the taskbar's lookup order, and unlike the
/// process default it is READABLE from outside (SHGetPropertyStoreForWindow), so the grouping
/// invariant can be verified instead of assumed. Needs COM on this thread - windowThread's
/// CoInitializeEx has already run.
fn setWindowAppIdentity(hwnd: HWND) void {
    var store: ?*IPropertyStore = null;
    if (SHGetPropertyStoreForWindow(hwnd, &iid_property_store, &store) < 0) return;
    const s = store orelse return;
    defer _ = s.vtbl.Release(@ptrCast(s));
    const pv: PROPVARIANT = .{ .vt = vt_lpwstr, .val = @intFromPtr(app_user_model_id) };
    if (s.vtbl.SetValue(@ptrCast(s), &pkey_app_user_model_id, &pv) >= 0) {
        _ = s.vtbl.Commit(@ptrCast(s));
    }
}

/// loadAppIcon loads the exe's embedded brand icon at the given SM_* metric size (windowicon_
/// windows.go parity). Shared module resource - never destroyed, freed at process exit.
fn loadAppIcon(hinst: ?*anyopaque, cx_metric: i32, cy_metric: i32) ?*anyopaque {
    const cx = GetSystemMetrics(cx_metric);
    const cy = GetSystemMetrics(cy_metric);
    if (LoadImageW(hinst, app_icon_res_id, image_icon, cx, cy, 0)) |h| return h;
    // Metric lookup failed - let the system pick the size.
    return LoadImageW(hinst, app_icon_res_id, image_icon, 0, 0, lr_defaultsize);
}

// ── WebView2 COM surface (slot orders copied from the shipped mswebview2 WebView2.h) ──

const HResult = i32;
const EventRegistrationToken = extern struct { value: i64 };

fn Vfn(comptime T: type) type {
    return *const T;
}

const EnvironmentVtbl = extern struct {
    QueryInterface: Vfn(fn (*anyopaque, *const anyopaque, *?*anyopaque) callconv(.winapi) HResult),
    AddRef: Vfn(fn (*anyopaque) callconv(.winapi) u32),
    Release: Vfn(fn (*anyopaque) callconv(.winapi) u32),
    CreateCoreWebView2Controller: Vfn(fn (*anyopaque, HWND, *anyopaque) callconv(.winapi) HResult),
    CreateWebResourceResponse: Vfn(fn () callconv(.winapi) HResult),
    get_BrowserVersionString: Vfn(fn () callconv(.winapi) HResult),
    add_NewBrowserVersionAvailable: Vfn(fn () callconv(.winapi) HResult),
    remove_NewBrowserVersionAvailable: Vfn(fn () callconv(.winapi) HResult),
};
const Environment = extern struct { vtbl: *const EnvironmentVtbl };

const ControllerVtbl = extern struct {
    QueryInterface: Vfn(fn (*anyopaque, *const anyopaque, *?*anyopaque) callconv(.winapi) HResult),
    AddRef: Vfn(fn (*anyopaque) callconv(.winapi) u32),
    Release: Vfn(fn (*anyopaque) callconv(.winapi) u32),
    get_IsVisible: Vfn(fn (*anyopaque, *i32) callconv(.winapi) HResult),
    put_IsVisible: Vfn(fn (*anyopaque, i32) callconv(.winapi) HResult),
    get_Bounds: Vfn(fn (*anyopaque, *RECT) callconv(.winapi) HResult),
    put_Bounds: Vfn(fn (*anyopaque, RECT) callconv(.winapi) HResult),
    get_ZoomFactor: Vfn(fn () callconv(.winapi) HResult),
    put_ZoomFactor: Vfn(fn () callconv(.winapi) HResult),
    add_ZoomFactorChanged: Vfn(fn () callconv(.winapi) HResult),
    remove_ZoomFactorChanged: Vfn(fn () callconv(.winapi) HResult),
    SetBoundsAndZoomFactor: Vfn(fn () callconv(.winapi) HResult),
    MoveFocus: Vfn(fn (*anyopaque, i32) callconv(.winapi) HResult),
    add_MoveFocusRequested: Vfn(fn () callconv(.winapi) HResult),
    remove_MoveFocusRequested: Vfn(fn () callconv(.winapi) HResult),
    add_GotFocus: Vfn(fn () callconv(.winapi) HResult),
    remove_GotFocus: Vfn(fn () callconv(.winapi) HResult),
    add_LostFocus: Vfn(fn () callconv(.winapi) HResult),
    remove_LostFocus: Vfn(fn () callconv(.winapi) HResult),
    add_AcceleratorKeyPressed: Vfn(fn () callconv(.winapi) HResult),
    remove_AcceleratorKeyPressed: Vfn(fn () callconv(.winapi) HResult),
    get_ParentWindow: Vfn(fn () callconv(.winapi) HResult),
    put_ParentWindow: Vfn(fn () callconv(.winapi) HResult),
    NotifyParentWindowPositionChanged: Vfn(fn (*anyopaque) callconv(.winapi) HResult),
    Close: Vfn(fn (*anyopaque) callconv(.winapi) HResult),
    get_CoreWebView2: Vfn(fn (*anyopaque, *?*WebView) callconv(.winapi) HResult),
};
const Controller = extern struct { vtbl: *const ControllerVtbl };

const WebViewVtbl = extern struct {
    QueryInterface: Vfn(fn (*anyopaque, *const anyopaque, *?*anyopaque) callconv(.winapi) HResult),
    AddRef: Vfn(fn (*anyopaque) callconv(.winapi) u32),
    Release: Vfn(fn (*anyopaque) callconv(.winapi) u32),
    get_Settings: Vfn(fn (*anyopaque, *?*Settings) callconv(.winapi) HResult),
    get_Source: Vfn(fn () callconv(.winapi) HResult),
    Navigate: Vfn(fn (*anyopaque, [*:0]const u16) callconv(.winapi) HResult),
    NavigateToString: Vfn(fn (*anyopaque, [*:0]const u16) callconv(.winapi) HResult),
    add_NavigationStarting: Vfn(fn () callconv(.winapi) HResult),
    remove_NavigationStarting: Vfn(fn () callconv(.winapi) HResult),
    add_ContentLoading: Vfn(fn () callconv(.winapi) HResult),
    remove_ContentLoading: Vfn(fn () callconv(.winapi) HResult),
    add_SourceChanged: Vfn(fn () callconv(.winapi) HResult),
    remove_SourceChanged: Vfn(fn () callconv(.winapi) HResult),
    add_HistoryChanged: Vfn(fn () callconv(.winapi) HResult),
    remove_HistoryChanged: Vfn(fn () callconv(.winapi) HResult),
    add_NavigationCompleted: Vfn(fn () callconv(.winapi) HResult),
    remove_NavigationCompleted: Vfn(fn () callconv(.winapi) HResult),
    add_FrameNavigationStarting: Vfn(fn () callconv(.winapi) HResult),
    remove_FrameNavigationStarting: Vfn(fn () callconv(.winapi) HResult),
    add_FrameNavigationCompleted: Vfn(fn () callconv(.winapi) HResult),
    remove_FrameNavigationCompleted: Vfn(fn () callconv(.winapi) HResult),
    add_ScriptDialogOpening: Vfn(fn () callconv(.winapi) HResult),
    remove_ScriptDialogOpening: Vfn(fn () callconv(.winapi) HResult),
    add_PermissionRequested: Vfn(fn () callconv(.winapi) HResult),
    remove_PermissionRequested: Vfn(fn () callconv(.winapi) HResult),
    add_ProcessFailed: Vfn(fn () callconv(.winapi) HResult),
    remove_ProcessFailed: Vfn(fn () callconv(.winapi) HResult),
    AddScriptToExecuteOnDocumentCreated: Vfn(fn (*anyopaque, [*:0]const u16, ?*anyopaque) callconv(.winapi) HResult),
    RemoveScriptToExecuteOnDocumentCreated: Vfn(fn () callconv(.winapi) HResult),
    ExecuteScript: Vfn(fn (*anyopaque, [*:0]const u16, ?*anyopaque) callconv(.winapi) HResult),
    CapturePreview: Vfn(fn () callconv(.winapi) HResult),
    Reload: Vfn(fn () callconv(.winapi) HResult),
    PostWebMessageAsJson: Vfn(fn () callconv(.winapi) HResult),
    PostWebMessageAsString: Vfn(fn () callconv(.winapi) HResult),
    add_WebMessageReceived: Vfn(fn (*anyopaque, *anyopaque, *EventRegistrationToken) callconv(.winapi) HResult),
    remove_WebMessageReceived: Vfn(fn () callconv(.winapi) HResult),
};
const WebView = extern struct { vtbl: *const WebViewVtbl };

const SettingsVtbl = extern struct {
    QueryInterface: Vfn(fn (*anyopaque, *const anyopaque, *?*anyopaque) callconv(.winapi) HResult),
    AddRef: Vfn(fn (*anyopaque) callconv(.winapi) u32),
    Release: Vfn(fn (*anyopaque) callconv(.winapi) u32),
    get_IsScriptEnabled: Vfn(fn () callconv(.winapi) HResult),
    put_IsScriptEnabled: Vfn(fn (*anyopaque, i32) callconv(.winapi) HResult),
    get_IsWebMessageEnabled: Vfn(fn () callconv(.winapi) HResult),
    put_IsWebMessageEnabled: Vfn(fn (*anyopaque, i32) callconv(.winapi) HResult),
    get_AreDefaultScriptDialogsEnabled: Vfn(fn () callconv(.winapi) HResult),
    put_AreDefaultScriptDialogsEnabled: Vfn(fn (*anyopaque, i32) callconv(.winapi) HResult),
    get_IsStatusBarEnabled: Vfn(fn () callconv(.winapi) HResult),
    put_IsStatusBarEnabled: Vfn(fn (*anyopaque, i32) callconv(.winapi) HResult),
    get_AreDevToolsEnabled: Vfn(fn () callconv(.winapi) HResult),
    put_AreDevToolsEnabled: Vfn(fn (*anyopaque, i32) callconv(.winapi) HResult),
    get_AreDefaultContextMenusEnabled: Vfn(fn () callconv(.winapi) HResult),
    put_AreDefaultContextMenusEnabled: Vfn(fn (*anyopaque, i32) callconv(.winapi) HResult),
};
const Settings = extern struct { vtbl: *const SettingsVtbl };

const MsgArgsVtbl = extern struct {
    QueryInterface: Vfn(fn (*anyopaque, *const anyopaque, *?*anyopaque) callconv(.winapi) HResult),
    AddRef: Vfn(fn (*anyopaque) callconv(.winapi) u32),
    Release: Vfn(fn (*anyopaque) callconv(.winapi) u32),
    get_Source: Vfn(fn () callconv(.winapi) HResult),
    get_WebMessageAsJson: Vfn(fn () callconv(.winapi) HResult),
    TryGetWebMessageAsString: Vfn(fn (*anyopaque, *?[*:0]u16) callconv(.winapi) HResult),
};
const MsgArgs = extern struct { vtbl: *const MsgArgsVtbl };

// ── handler objects we implement (static lifetime; QI hands back self) ──

const HandlerVtbl = extern struct {
    QueryInterface: *const fn (*anyopaque, *const anyopaque, *?*anyopaque) callconv(.winapi) HResult,
    AddRef: *const fn (*anyopaque) callconv(.winapi) u32,
    Release: *const fn (*anyopaque) callconv(.winapi) u32,
    Invoke: *const fn (*anyopaque, usize, usize) callconv(.winapi) HResult,
};
const Handler = extern struct { vtbl: *const HandlerVtbl };

fn hQI(self: *anyopaque, _: *const anyopaque, out: *?*anyopaque) callconv(.winapi) HResult {
    out.* = self; // static object: any requested interface is answered with self (webview.h pattern)
    return 0;
}
fn hAddRef(_: *anyopaque) callconv(.winapi) u32 {
    return 1;
}
fn hRelease(_: *anyopaque) callconv(.winapi) u32 {
    return 1;
}

// ── the window host ──────────────────────────────────────────────────────────

pub const Shell = struct {
    gpa: std.mem.Allocator,
    em: *wire.Emitter,

    title: []u8,
    init_w: i32,
    init_h: i32,
    start_hidden: bool,
    allow_gpu: bool,
    data_dir: []u8,
    boot_js: []u8, // shim + runtimeJS (document-start injection)
    initial_html: []u8,

    hwnd: std.atomic.Value(usize) = .init(0),
    widget: std.atomic.Value(usize) = .init(0), // WS_CHILD host the WebView2 controller parents to
    controller: ?*Controller = null,
    webview: ?*WebView = null,
    web_ready: std.atomic.Value(bool) = .init(false),

    focused: bool = false,
    minimized: bool = false,
    size_move: bool = false,
    drag_seen_ms: i64 = 0,
    streaming: std.atomic.Value(bool) = .init(false),
    prio_below: std.atomic.Value(u8) = .init(255), // 255 = unset

    acts_mu: sync.Lock = .{},
    acts_cond: sync.Cond = .{},
    acts: std.ArrayList([]u8) = .empty, // cap 64, drop-on-full (cgoShell parity)
    acts_done: bool = false,

    gone: std.atomic.Value(bool) = .init(false),
    thread: ?std.Thread = null,
};

/// The wndproc has no context argument, and there is exactly one window per child process:
/// package-global instance (same shape as the Go child's package-level latches).
var g: ?*Shell = null;

const acts_cap = 64;

pub fn start(sh: *Shell) !void {
    g = sh;
    sh.thread = try std.Thread.spawn(.{}, windowThread, .{sh});
    _ = try std.Thread.spawn(.{}, actWorker, .{sh});
}

// ── UI-thread mailbox ──

const UiMsg = union(enum) {
    doc: []u8,
    eval_js: []u8,
    resize: struct { w: i32, h: i32 },
    show,
    quit,
};

/// postUi hands one message to the window thread (FIFO via the thread's message queue).
/// Pre-window frames are DROPPED - cgoShell.dispatch parity; the daemon re-pushes on ready.
pub fn postUi(sh: *Shell, m: UiMsg) void {
    const h = sh.hwnd.load(.acquire);
    if (h == 0) {
        freeUiMsg(sh, m);
        return;
    }
    const boxed = sh.gpa.create(UiMsg) catch {
        freeUiMsg(sh, m);
        return;
    };
    boxed.* = m;
    if (PostMessageW(h, wm_app_frame, 0, @bitCast(@intFromPtr(boxed))) == 0) {
        freeUiMsg(sh, boxed.*);
        sh.gpa.destroy(boxed);
    }
}

fn freeUiMsg(sh: *Shell, m: UiMsg) void {
    switch (m) {
        .doc, .eval_js => |s| sh.gpa.free(s),
        else => {},
    }
}

/// postAct enqueues a payload on the serial act worker (page + Go-originated input share it).
/// Queue full = drop, never block the caller (cgoShell parity).
pub fn postAct(sh: *Shell, payload: []const u8) void {
    const copy = sh.gpa.dupe(u8, payload) catch return;
    sh.acts_mu.lock();
    defer sh.acts_mu.unlock();
    if (sh.acts_done or sh.acts.items.len >= acts_cap) {
        sh.gpa.free(copy);
        return;
    }
    sh.acts.append(sh.gpa, copy) catch {
        sh.gpa.free(copy);
        return;
    };
    sh.acts_cond.signal();
}

fn actWorker(sh: *Shell) void {
    while (true) {
        sh.acts_mu.lock();
        while (sh.acts.items.len == 0 and !sh.acts_done) sh.acts_cond.wait(&sh.acts_mu);
        if (sh.acts_done and sh.acts.items.len == 0) {
            sh.acts_mu.unlock();
            return;
        }
        const p = sh.acts.orderedRemove(0);
        sh.acts_mu.unlock();
        sh.em.event("action", .{ .payload = p });
        sh.gpa.free(p);
    }
}

/// setStreaming applies the governor's downstream signal (below-normal parity with the Go child).
pub fn setStreaming(sh: *Shell, on: bool) void {
    sh.streaming.store(on, .seq_cst);
    applyPriority(sh);
}

fn applyPriority(sh: *Shell) void {
    // internal/governor rule: below-normal whenever a stream is live, the window isn't the
    // user's focus, or the user is mid drag/resize.
    const below = sh.streaming.load(.seq_cst) or sh.minimized or !sh.focused or sh.size_move;
    const want: u8 = if (below) 1 else 0;
    if (sh.prio_below.swap(want, .seq_cst) == want) return;
    _ = SetPriorityClass(GetCurrentProcess(), if (below) below_normal_priority else normal_priority);
}

/// terminate asks the window to close; force-exits after grace_ms if the loop has not unwound
/// (childForceExitGrace parity).
pub fn terminate(sh: *Shell, grace_ms: u32) void {
    const h = sh.hwnd.load(.acquire);
    if (h == 0) {
        // No window yet (quit raced creation): nothing to unwind - the daemon is quitting.
        sh.em.event("gone", wire.Empty{});
        ExitProcess(0);
    }
    const boxed = sh.gpa.create(UiMsg) catch null;
    if (boxed) |b| {
        b.* = .quit;
        if (PostMessageW(h, wm_app_frame, 0, @bitCast(@intFromPtr(b))) == 0) sh.gpa.destroy(b);
    }
    _ = std.Thread.spawn(.{}, forceExit, .{ sh, grace_ms }) catch {};
}

fn forceExit(sh: *Shell, grace_ms: u32) void {
    var waited: u32 = 0;
    while (waited < grace_ms) : (waited += 25) {
        if (sh.gone.load(.seq_cst)) return;
        sync.sleepMs(25);
    }
    wire.errLine("rave-shell: window did not unwind in grace - hard exit");
    ExitProcess(0);
}

// ── window thread ──

fn utf16z(gpa: std.mem.Allocator, s: []const u8) ?[:0]u16 {
    return std.unicode.utf8ToUtf16LeAllocZ(gpa, s) catch null;
}

fn windowThread(sh: *Shell) void {
    _ = CoInitializeEx(null, 2); // COINIT_APARTMENTTHREADED
    _ = SetProcessDpiAwarenessContext(-4); // PER_MONITOR_AWARE_V2

    const cls_name = std.unicode.utf8ToUtf16LeStringLiteral("rave_shell_window");
    const hinst = GetModuleHandleW(null);
    const icon_big_h = loadAppIcon(hinst, sm_cxicon, sm_cyicon);
    const icon_small_h = loadAppIcon(hinst, sm_cxsmicon, sm_cysmicon);
    var wc: WNDCLASSEXW = .{
        .cbSize = @sizeOf(WNDCLASSEXW),
        .style = 0,
        .lpfnWndProc = wndProc,
        .cbClsExtra = 0,
        .cbWndExtra = 0,
        .hInstance = hinst,
        .hIcon = icon_big_h,
        .hCursor = LoadCursorW(null, 32512), // IDC_ARROW
        .hbrBackground = null,
        .lpszMenuName = null,
        .lpszClassName = cls_name,
        .hIconSm = icon_small_h,
    };
    _ = RegisterClassExW(&wc);
    const title16 = utf16z(sh.gpa, sh.title) orelse return;
    defer sh.gpa.free(title16);
    // Created WITHOUT WS_VISIBLE: this process was spawned with SW_HIDE (sysexec.Hide) and the
    // first top-level window inherits it anyway. Reveal happens explicitly on ready.
    const cw_usedefault: i32 = @bitCast(@as(u32, 0x80000000));
    const hwnd = CreateWindowExW(0, cls_name, title16, ws_overlappedwindow, cw_usedefault, cw_usedefault, 640, 480, 0, null, wc.hInstance, null);
    if (hwnd == 0) {
        wire.errLine("rave-shell: CreateWindowExW failed");
        sh.em.event("gone", wire.Empty{});
        ExitProcess(1);
    }
    _ = ShowWindow(hwnd, sw_hide); // burn any STARTUPINFO first-show override deterministically
    // Per-window icon as well as the class icon: the taskbar/Alt-Tab ask the WINDOW first
    // (WM_GETICON), and a class icon alone leaves WM_GETICON answering 0 - which is exactly the
    // "no rave-mate logo in the taskbar" this fixes. Matches shell_cgo.go's setWindowIcon call.
    if (icon_small_h) |h| _ = SendMessageW(hwnd, wm_seticon, icon_small, @bitCast(@intFromPtr(h)));
    if (icon_big_h) |h| _ = SendMessageW(hwnd, wm_seticon, icon_big, @bitCast(@intFromPtr(h)));
    setWindowAppIdentity(hwnd); // group with the pinned rave-mate button, not a second one
    sh.hwnd.store(hwnd, .release);
    applyOuterSize(sh, hwnd, sh.init_w, sh.init_h);

    // WebView2 hosts on a CHILD "widget" window filling the client area (webview.h structure).
    // Not cosmetic: with GPU compositing OFF, PrintWindow of a top-level hosting the controller
    // DIRECTLY captures white while the on-screen window is fine - parenting the controller to a
    // widget child is the arrangement the Go child's captures work under (proven by probe).
    const widget_cls = std.unicode.utf8ToUtf16LeStringLiteral("rave_shell_widget");
    var wcw: WNDCLASSEXW = wc;
    wcw.lpfnWndProc = widgetProc;
    wcw.lpszClassName = widget_cls;
    _ = RegisterClassExW(&wcw);
    var cr: RECT = undefined;
    _ = GetClientRect(hwnd, &cr);
    const ws_child: u32 = 0x40000000;
    const ws_ex_controlparent: u32 = 0x00010000;
    const widget = CreateWindowExW(ws_ex_controlparent, widget_cls, std.unicode.utf8ToUtf16LeStringLiteral(""), ws_child, 0, 0, cr.right, cr.bottom, hwnd, null, wc.hInstance, null);
    if (widget == 0) {
        wire.errLine("rave-shell: widget CreateWindowExW failed");
        sh.em.event("gone", wire.Empty{});
        ExitProcess(1);
    }
    _ = ShowWindow(widget, sw_show);
    sh.widget.store(widget, .release);

    // GPU compositing off by default (good-neighbour; shell_cgo.go parity) - the loader reads
    // this env var when options are default.
    if (!sh.allow_gpu) {
        const name = std.unicode.utf8ToUtf16LeStringLiteral("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS");
        if (GetEnvironmentVariableW(name, null, 0) == 0) {
            const args = std.unicode.utf8ToUtf16LeStringLiteral("--disable-gpu --disable-gpu-compositing --disable-software-rasterizer");
            _ = SetEnvironmentVariableW(name, args);
        }
    }
    mkdirAll(sh.gpa, sh.data_dir);

    if (!createEnvironment(sh)) {
        wire.errLine("rave-shell: WebView2 environment creation failed - runtime missing?");
        sh.em.event("gone", wire.Empty{});
        ExitProcess(1);
    }

    var msg: MSG = undefined;
    while (GetMessageW(&msg, 0, 0, 0) > 0) {
        _ = TranslateMessage(&msg);
        _ = DispatchMessageW(&msg);
    }
    sh.gone.store(true, .seq_cst);
    sh.em.event("gone", wire.Empty{});
    ExitProcess(0); // message loop over = the child is done (Go child: Start returns, exit 0)
}

/// applyOuterSize resizes the window so the CLIENT area is w x h scaled by the monitor DPI
/// (webview_go SetSize/HintNone behavior, close enough for ctl resize parity).
fn applyOuterSize(sh: *Shell, hwnd: HWND, w: i32, h: i32) void {
    _ = sh;
    const dpi = GetDpiForWindow(hwnd);
    const sw = @divTrunc(w * @as(i32, @intCast(dpi)), 96);
    const s_h = @divTrunc(h * @as(i32, @intCast(dpi)), 96);
    var r: RECT = .{ .left = 0, .top = 0, .right = sw, .bottom = s_h };
    _ = AdjustWindowRectExForDpi(&r, ws_overlappedwindow, 0, 0, dpi);
    _ = SetWindowPos(hwnd, 0, 0, 0, r.right - r.left, r.bottom - r.top, 0x0002 | 0x0004 | 0x0010); // NOMOVE|NOZORDER|NOACTIVATE
}

fn mkdirAll(gpa: std.mem.Allocator, path: []const u8) void {
    if (path.len == 0) return;
    var i: usize = 0;
    while (i <= path.len) : (i += 1) {
        if (i == path.len or path[i] == '\\' or path[i] == '/') {
            if (i > 2) { // skip drive root
                const part16 = utf16z(gpa, path[0..i]) orelse return;
                defer gpa.free(part16);
                _ = CreateDirectoryW(part16, null);
            }
        }
    }
}

// ── WebView2 bring-up (loader → environment → controller → webview → ready) ──

var env_handler_vtbl: HandlerVtbl = .{ .QueryInterface = hQI, .AddRef = hAddRef, .Release = hRelease, .Invoke = envInvoke };
var env_handler: Handler = .{ .vtbl = &env_handler_vtbl };
var ctrl_handler_vtbl: HandlerVtbl = .{ .QueryInterface = hQI, .AddRef = hAddRef, .Release = hRelease, .Invoke = ctrlInvoke };
var ctrl_handler: Handler = .{ .vtbl = &ctrl_handler_vtbl };
var script_handler_vtbl: HandlerVtbl = .{ .QueryInterface = hQI, .AddRef = hAddRef, .Release = hRelease, .Invoke = scriptInvoke };
var script_handler: Handler = .{ .vtbl = &script_handler_vtbl };
var msg_handler_vtbl: HandlerVtbl = .{ .QueryInterface = hQI, .AddRef = hAddRef, .Release = hRelease, .Invoke = msgInvoke };
var msg_handler: Handler = .{ .vtbl = &msg_handler_vtbl };

const CreateEnvFn = *const fn (?[*:0]const u16, ?[*:0]const u16, ?*anyopaque, *Handler) callconv(.winapi) HResult;
const CreateEnvInternalFn = *const fn (bool, i32, ?[*:0]const u16, ?*anyopaque, *Handler) callconv(.winapi) HResult;

fn createEnvironment(sh: *Shell) bool {
    const data16 = utf16z(sh.gpa, sh.data_dir);
    const data_ptr: ?[*:0]const u16 = if (data16) |d| d.ptr else null;
    // 1) official loader beside the exe, if shipped
    if (LoadLibraryW(std.unicode.utf8ToUtf16LeStringLiteral("WebView2Loader.dll"))) |lib| {
        if (GetProcAddress(lib, "CreateCoreWebView2EnvironmentWithOptions")) |p| {
            const create: CreateEnvFn = @ptrCast(@alignCast(p));
            return create(null, data_ptr, null, &env_handler) >= 0;
        }
    }
    // 2) built-in fallback (what webview_go actually uses): Evergreen runtime client dll via
    //    registry ClientState\{stable-channel} EBWebView, KEY_WOW64_32KEY.
    const dll_path = findRuntimeClientDll(sh.gpa) orelse return false;
    defer sh.gpa.free(dll_path);
    const dll16 = utf16z(sh.gpa, dll_path) orelse return false;
    defer sh.gpa.free(dll16);
    const lib = LoadLibraryW(dll16) orelse return false;
    const p = GetProcAddress(lib, "CreateWebViewEnvironmentWithOptionsInternal") orelse return false;
    const create: CreateEnvInternalFn = @ptrCast(@alignCast(p));
    return create(true, 0, data_ptr, null, &env_handler) >= 0;
}

/// findRuntimeClientDll resolves <EBWebView>\EBWebView\x64\EmbeddedBrowserWebView.dll for the
/// stable Evergreen channel (webview.h find_installed_client port; min api version 1150).
fn findRuntimeClientDll(gpa: std.mem.Allocator) ?[]u8 {
    const sub = std.unicode.utf8ToUtf16LeStringLiteral("SOFTWARE\\Microsoft\\EdgeUpdate\\ClientState\\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}");
    const value = std.unicode.utf8ToUtf16LeStringLiteral("EBWebView");
    const roots = [_]usize{ 0x80000002, 0x80000001 }; // HKLM then HKCU
    for (roots) |root| {
        var key: usize = 0;
        if (RegOpenKeyExW(root, sub, 0, 0x20019 | 0x0200, &key) != 0) continue; // KEY_READ|WOW64_32
        defer _ = RegCloseKey(key);
        var size: u32 = 0;
        if (RegQueryValueExW(key, value, null, null, null, &size) != 0 or size == 0) continue;
        const buf = gpa.alloc(u16, size / 2 + 1) catch continue;
        defer gpa.free(buf);
        if (RegQueryValueExW(key, value, null, null, @ptrCast(buf.ptr), &size) != 0) continue;
        const trimmed = std.mem.sliceTo(buf[0 .. size / 2], 0);
        const dir = std.unicode.utf16LeToUtf8Alloc(gpa, trimmed) catch continue;
        defer gpa.free(dir);
        if (!versionOK(dir)) continue;
        const sep: []const u8 = if (dir.len > 0 and (dir[dir.len - 1] == '\\' or dir[dir.len - 1] == '/')) "" else "\\";
        return std.fmt.allocPrint(gpa, "{s}{s}EBWebView\\x64\\EmbeddedBrowserWebView.dll", .{ dir, sep }) catch null;
    }
    return null;
}

/// versionOK checks the runtime version dir's third component >= 1150 (webview.h api_version).
fn versionOK(dir: []const u8) bool {
    var last: []const u8 = dir;
    if (std.mem.lastIndexOfScalar(u8, dir, '\\')) |k| last = dir[k + 1 ..];
    var it = std.mem.splitScalar(u8, last, '.');
    _ = it.next() orelse return false;
    _ = it.next() orelse return false;
    const third = it.next() orelse return false;
    const v = std.fmt.parseInt(u32, third, 10) catch return false;
    return v >= 1150;
}

fn envInvoke(_: *anyopaque, hr_or_env: usize, env_ptr: usize) callconv(.winapi) HResult {
    const sh = g orelse return 0;
    const hr: HResult = @bitCast(@as(u32, @truncate(hr_or_env)));
    if (hr < 0 or env_ptr == 0) {
        wire.errLine("rave-shell: environment completed with failure");
        sh.em.event("gone", wire.Empty{});
        ExitProcess(1);
    }
    const env: *Environment = @ptrFromInt(env_ptr);
    _ = env.vtbl.CreateCoreWebView2Controller(@ptrCast(env), sh.widget.load(.acquire), @ptrCast(&ctrl_handler));
    return 0;
}

fn ctrlInvoke(_: *anyopaque, hr_or: usize, ctrl_ptr: usize) callconv(.winapi) HResult {
    const sh = g orelse return 0;
    const hr: HResult = @bitCast(@as(u32, @truncate(hr_or)));
    if (hr < 0 or ctrl_ptr == 0) {
        wire.errLine("rave-shell: controller creation failed");
        sh.em.event("gone", wire.Empty{});
        ExitProcess(1);
    }
    const ctrl: *Controller = @ptrFromInt(ctrl_ptr);
    _ = ctrl.vtbl.AddRef(@ptrCast(ctrl));
    sh.controller = ctrl;
    var wv: ?*WebView = null;
    _ = ctrl.vtbl.get_CoreWebView2(@ptrCast(ctrl), &wv);
    const webview = wv orelse {
        wire.errLine("rave-shell: get_CoreWebView2 returned null");
        sh.em.event("gone", wire.Empty{});
        ExitProcess(1);
    };
    _ = webview.vtbl.AddRef(@ptrCast(webview));
    sh.webview = webview;

    var settings: ?*Settings = null;
    if (webview.vtbl.get_Settings(@ptrCast(webview), &settings) >= 0) {
        if (settings) |s| {
            _ = s.vtbl.put_AreDevToolsEnabled(@ptrCast(s), 0);
            _ = s.vtbl.put_AreDefaultContextMenusEnabled(@ptrCast(s), 0);
            _ = s.vtbl.put_IsStatusBarEnabled(@ptrCast(s), 0);
        }
    }
    var tok: EventRegistrationToken = .{ .value = 0 };
    _ = webview.vtbl.add_WebMessageReceived(@ptrCast(webview), @ptrCast(&msg_handler), &tok);
    boundsToClient(sh);
    // Document-start injection: binding shim + the DAEMON's runtimeJS bytes (never a local copy).
    const js16 = utf16z(sh.gpa, sh.boot_js) orelse return 0;
    defer sh.gpa.free(js16);
    _ = webview.vtbl.AddScriptToExecuteOnDocumentCreated(@ptrCast(webview), js16, @ptrCast(&script_handler));
    return 0;
}

/// scriptInvoke: the boot script is registered - load the initial document, then READY: reveal
/// (the B5 lesson) + emit ready + start the beat.
fn scriptInvoke(_: *anyopaque, _: usize, _: usize) callconv(.winapi) HResult {
    const sh = g orelse return 0;
    if (sh.web_ready.swap(true, .seq_cst)) return 0;
    navigateTo(sh, sh.initial_html);
    const hwnd = sh.hwnd.load(.acquire);
    resizeWidget(sh, hwnd);
    const widget = sh.widget.load(.acquire);
    if (widget != 0) {
        _ = ShowWindow(widget, sw_show);
        _ = UpdateWindow(widget);
    }
    if (!sh.start_hidden) {
        _ = ShowWindow(hwnd, sw_shownoactivate); // reveal WITHOUT stealing focus (revealWindow parity)
        _ = UpdateWindow(hwnd);
    }
    sh.em.event("ready", .{ .hwnd = hwnd, .virtual = false });
    _ = SetTimer(hwnd, beat_timer_id, 2000, null); // procBeatInterval
    return 0;
}

fn navigateTo(sh: *Shell, html: []const u8) void {
    const wv = sh.webview orelse return;
    const h16 = utf16z(sh.gpa, html) orelse return;
    defer sh.gpa.free(h16);
    _ = wv.vtbl.NavigateToString(@ptrCast(wv), h16);
}

fn execScript(sh: *Shell, js: []const u8) void {
    const wv = sh.webview orelse return;
    const j16 = utf16z(sh.gpa, js) orelse return;
    defer sh.gpa.free(j16);
    _ = wv.vtbl.ExecuteScript(@ptrCast(wv), j16, null);
}

/// resizeWidget keeps the widget child covering the main window's client area (webview.h
/// resize_widget), then re-fits the controller bounds.
fn resizeWidget(sh: *Shell, main: HWND) void {
    const widget = sh.widget.load(.acquire);
    if (widget != 0) {
        var r: RECT = undefined;
        if (GetClientRect(main, &r) != 0) {
            _ = MoveWindow(widget, 0, 0, r.right - r.left, r.bottom - r.top, 1);
        }
    }
    boundsToClient(sh);
}

fn boundsToClient(sh: *Shell) void {
    const ctrl = sh.controller orelse return;
    const widget = sh.widget.load(.acquire);
    if (widget == 0) return;
    var r: RECT = undefined;
    if (GetClientRect(widget, &r) == 0) return;
    _ = ctrl.vtbl.put_Bounds(@ptrCast(ctrl), r);
    _ = ctrl.vtbl.put_IsVisible(@ptrCast(ctrl), 1);
}

// ── page → child messages (the binding shim posts {"m":..}) ──

fn msgInvoke(_: *anyopaque, _: usize, args_ptr: usize) callconv(.winapi) HResult {
    const sh = g orelse return 0;
    if (args_ptr == 0) return 0;
    const args: *MsgArgs = @ptrFromInt(args_ptr);
    var raw: ?[*:0]u16 = null;
    if (args.vtbl.TryGetWebMessageAsString(@ptrCast(args), &raw) < 0) return 0;
    const wide = raw orelse return 0;
    defer CoTaskMemFree(wide);
    const text = std.unicode.utf16LeToUtf8Alloc(sh.gpa, std.mem.sliceTo(wide, 0)) catch return 0;
    defer sh.gpa.free(text);

    const Bind = struct {
        m: []const u8 = "",
        p: []const u8 = "",
        i: []const u8 = "",
        r: []const u8 = "",
    };
    var arena_state: std.heap.ArenaAllocator = .init(sh.gpa);
    defer arena_state.deinit();
    const b = std.json.parseFromSliceLeaky(Bind, arena_state.allocator(), text, .{ .ignore_unknown_fields = true }) catch return 0;
    if (std.mem.eql(u8, b.m, "a")) {
        // page act: enqueue only - handling on the UI thread froze the message pump (cgo lesson)
        postAct(sh, b.p);
    } else if (std.mem.eql(u8, b.m, "r")) {
        if (std.mem.eql(u8, b.i, "__beat")) {
            sh.em.event("heartbeat", wire.Empty{}); // the beat originates on the window's UI thread = liveness proof
        } else {
            sh.em.event("evalres", .{ .id = b.i, .result = b.r });
        }
    }
    return 0;
}

// ── wndproc (sizemove_windows.go behavior parity) ──

const wm_size = 0x0005;
const wm_activate = 0x0006;
const wm_close = 0x0010;
const wm_showwindow = 0x0018;
const wm_entersizemove = 0x0231;
const wm_exitsizemove = 0x0232;
const wm_sizing = 0x0214;
const wm_moving = 0x0216;
const wm_capturechanged = 0x0215;
const wm_destroy = 0x0002;
const size_minimized = 1;

fn emitWin(sh: *Shell) void {
    sh.em.event("win", .{ .focused = sh.focused, .minimized = sh.minimized, .sizeMove = sh.size_move, .hidden = false });
    applyPriority(sh);
}

fn widgetProc(hwnd: HWND, msg: u32, wp: usize, lp: isize) callconv(.winapi) isize {
    return DefWindowProcW(hwnd, msg, wp, lp);
}

fn wndProc(hwnd: HWND, msg: u32, wp: usize, lp: isize) callconv(.winapi) isize {
    const sh = g orelse return DefWindowProcW(hwnd, msg, wp, lp);
    switch (msg) {
        wm_app_frame => {
            const boxed: *UiMsg = @ptrFromInt(@as(usize, @bitCast(lp)));
            defer sh.gpa.destroy(boxed);
            switch (boxed.*) {
                .doc => |htmlv| {
                    navigateTo(sh, htmlv);
                    sh.gpa.free(htmlv);
                },
                .eval_js => |js| {
                    execScript(sh, js);
                    sh.gpa.free(js);
                },
                .resize => |r| applyOuterSize(sh, hwnd, r.w, r.h),
                .show => {
                    _ = ShowWindow(hwnd, sw_show);
                    _ = SetForegroundWindow(hwnd);
                },
                .quit => _ = DestroyWindow(hwnd),
            }
            return 0;
        },
        wm_timer => {
            if (wp == beat_timer_id) {
                // Dispatched on the UI thread; the page routes it back via the binding shim, so a
                // wedged webview stops beating and the daemon Host restarts the child.
                execScript(sh, "window.__rave_evalResult(\"__beat\",'1');");
                // Size-move stale self-heal (inSizeMove parity): a swallowed EXITSIZEMOVE must not
                // wedge the daemon's eval flusher forever.
                if (sh.size_move and @as(i64, @intCast(GetTickCount64())) - sh.drag_seen_ms > 1500) {
                    sh.size_move = false;
                    emitWin(sh);
                }
            }
            return 0;
        },
        wm_entersizemove => {
            sh.drag_seen_ms = @as(i64, @intCast(GetTickCount64()));
            sh.size_move = true;
            emitWin(sh);
        },
        wm_sizing, wm_moving => {
            if (sh.size_move) sh.drag_seen_ms = @as(i64, @intCast(GetTickCount64()));
        },
        wm_exitsizemove => {
            sh.size_move = false;
            emitWin(sh);
        },
        wm_capturechanged => {
            if (sh.size_move) { // capture loss ends any drag - don't wait for a maybe-missing EXIT
                sh.size_move = false;
                emitWin(sh);
            }
        },
        wm_activate => {
            sh.focused = (wp & 0xffff) != 0;
            emitWin(sh);
            if (sh.focused) {
                if (sh.controller) |c| _ = c.vtbl.MoveFocus(@ptrCast(c), 0); // PROGRAMMATIC (webview.h parity)
            }
        },
        wm_size => {
            switch (wp) {
                size_minimized => {
                    sh.minimized = true;
                    emitWin(sh);
                },
                0, 2 => { // restored / maximized
                    sh.minimized = false;
                    emitWin(sh);
                },
                else => {},
            }
            resizeWidget(sh, hwnd);
            if (sh.controller) |c| _ = c.vtbl.NotifyParentWindowPositionChanged(@ptrCast(c));
            return 0;
        },
        wm_showwindow => {
            sh.minimized = wp == 0; // hidden-to-tray = not being looked at (Go parity)
            emitWin(sh);
        },
        wm_close => {
            // Tray app, not quit-on-close: X/Alt+F4 hides; only quit (daemon `quit` event) exits.
            _ = ShowWindow(hwnd, sw_hide);
            sh.em.event("win", .{ .focused = false, .minimized = false, .sizeMove = false, .hidden = true });
            return 0;
        },
        wm_destroy => {
            PostQuitMessage(0);
            return 0;
        },
        else => {},
    }
    return DefWindowProcW(hwnd, msg, wp, lp);
}

// ── child-side screenshot (PSH1 §5: path + rect cross the pipe, never pixels) ──

pub fn capture(sh: *Shell, rid: []const u8, path: []const u8, x: i32, y: i32, w: i32, h: i32) void {
    const Ctx = struct {
        sh: *Shell,
        rid: []u8,
        path: []u8,
        x: i32,
        y: i32,
        w: i32,
        h: i32,
    };
    const ctx = sh.gpa.create(Ctx) catch return;
    ctx.* = .{
        .sh = sh,
        .rid = sh.gpa.dupe(u8, rid) catch return,
        .path = sh.gpa.dupe(u8, path) catch return,
        .x = x,
        .y = y,
        .w = w,
        .h = h,
    };
    // Own thread: PrintWindow + PNG encode is tens of ms and must not stall the stdin reader.
    _ = std.Thread.spawn(.{}, struct {
        fn run(c: *Ctx) void {
            const err = captureHWND(c.sh, c.path, c.x, c.y, c.w, c.h);
            if (err) |msgv| {
                c.sh.em.event("shotres", .{ .rid = c.rid, .err = msgv });
            } else {
                c.sh.em.event("shotres", .{ .rid = c.rid });
            }
            c.sh.gpa.free(c.rid);
            c.sh.gpa.free(c.path);
            c.sh.gpa.destroy(c);
        }
    }.run, .{ctx}) catch {
        sh.gpa.free(ctx.rid);
        sh.gpa.free(ctx.path);
        sh.gpa.destroy(ctx);
    };
}

/// captureHWND: PrintWindow(PW_RENDERFULLCONTENT) → GetDIBits → (crop) → PNG. Port of
/// screenshot_windows.go captureHWND. Returns an error message or null on success.
fn captureHWND(sh: *Shell, path: []const u8, x: i32, y: i32, w: i32, h: i32) ?[]const u8 {
    const hwnd = sh.hwnd.load(.acquire);
    if (hwnd == 0) return "no window handle";
    var r: RECT = undefined;
    _ = GetWindowRect(hwnd, &r);
    const full_w: i32 = r.right - r.left;
    const full_h: i32 = r.bottom - r.top;
    if (full_w <= 0 or full_h <= 0) return "bad window rect";

    const hdc_win = GetDC(hwnd) orelse return "GetDC failed";
    defer _ = ReleaseDC(hwnd, hdc_win);
    const hdc_mem = CreateCompatibleDC(hdc_win);
    defer _ = DeleteDC(hdc_mem);
    const hbmp = CreateCompatibleBitmap(hdc_win, full_w, full_h);
    defer _ = DeleteObject(hbmp);
    const old = SelectObject(hdc_mem, hbmp);
    _ = PrintWindow(hwnd, hdc_mem, 2); // PW_RENDERFULLCONTENT (GPU/WebView2 surfaces)

    var bi: BitmapInfo = std.mem.zeroes(BitmapInfo);
    bi.header.biSize = @sizeOf(BitmapInfoHeader);
    bi.header.biWidth = full_w;
    bi.header.biHeight = -full_h; // negative = top-down rows
    bi.header.biPlanes = 1;
    bi.header.biBitCount = 32;
    const n: usize = @intCast(full_w * full_h);
    const buf = sh.gpa.alloc(u8, n * 4) catch return "out of memory";
    defer sh.gpa.free(buf);
    _ = GetDIBits(hdc_mem, hbmp, 0, @intCast(full_h), buf.ptr, &bi, 0);
    _ = SelectObject(hdc_mem, old);

    // Crop rect (device px; w<=0||h<=0 = whole window), then BGRA → RGB.
    var cx: i32 = 0;
    var cy: i32 = 0;
    var cw: i32 = full_w;
    var ch: i32 = full_h;
    if (w > 0 and h > 0) {
        cx = @max(0, @min(x, full_w));
        cy = @max(0, @min(y, full_h));
        cw = @max(0, @min(x + w, full_w) - cx);
        ch = @max(0, @min(y + h, full_h) - cy);
        if (cw == 0 or ch == 0) return "empty capture region";
    }
    const out_px = sh.gpa.alloc(u8, @as(usize, @intCast(cw * ch)) * 3) catch return "out of memory";
    defer sh.gpa.free(out_px);
    var row: i32 = 0;
    while (row < ch) : (row += 1) {
        var col: i32 = 0;
        while (col < cw) : (col += 1) {
            const src: usize = (@as(usize, @intCast(cy + row)) * @as(usize, @intCast(full_w)) + @as(usize, @intCast(cx + col))) * 4;
            const dst: usize = (@as(usize, @intCast(row)) * @as(usize, @intCast(cw)) + @as(usize, @intCast(col))) * 3;
            out_px[dst] = buf[src + 2]; // R (from BGRA)
            out_px[dst + 1] = buf[src + 1];
            out_px[dst + 2] = buf[src];
        }
    }
    const encoded = png.encodeRGB(sh.gpa, out_px, @intCast(cw), @intCast(ch)) catch return "png encode failed";
    defer sh.gpa.free(encoded);
    return writeFileBytes(sh.gpa, path, encoded);
}

fn writeFileBytes(gpa: std.mem.Allocator, path: []const u8, bytes: []const u8) ?[]const u8 {
    const p16 = utf16z(gpa, path) orelse return "bad path";
    defer gpa.free(p16);
    const generic_write: u32 = 0x40000000;
    const create_always: u32 = 2;
    const hfile = CreateFileW(p16, generic_write, 0, null, create_always, 0x80, null) orelse return "create file failed";
    if (@intFromPtr(hfile) == std.math.maxInt(usize)) return "create file failed"; // INVALID_HANDLE_VALUE
    defer _ = CloseHandle(hfile);
    var off: usize = 0;
    while (off < bytes.len) {
        var written: u32 = 0;
        const chunk: u32 = @intCast(@min(bytes.len - off, 1 << 20));
        if (WriteFile(hfile, bytes.ptr + off, chunk, &written, null) == 0 or written == 0) return "write failed";
        off += written;
    }
    return null;
}

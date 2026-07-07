//go:build windows

package webui

import "syscall"

var (
	kernel32             = syscall.NewLazyDLL("kernel32.dll")
	procGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")

	procSendMessageW     = user32.NewProc("SendMessageW")
	procLoadImageW       = user32.NewProc("LoadImageW")
	procGetSystemMetrics = user32.NewProc("GetSystemMetrics")
)

const (
	wmSetIcon = 0x0080
	iconSmall = 0 // ICON_SMALL: title bar
	iconBig   = 1 // ICON_BIG: Alt-Tab / taskbar

	imageIcon     = 1    // IMAGE_ICON
	lrDefaultSize = 0x40 // LR_DEFAULTSIZE

	smCxSmIcon = 49 // SM_CXSMICON
	smCySmIcon = 50 // SM_CYSMICON
	smCxIcon   = 11 // SM_CXICON
	smCyIcon   = 12 // SM_CYICON

	// RT_GROUP_ICON id written by tools/winicon into cmd/rave-mate/rsrc_windows_amd64.syso.
	appIconResID = 1
)

// setWindowIcon points the window's title-bar + Alt-Tab icons at the exe's embedded brand icon
// (the committed .syso, same source as the tray's icon). Webview_go never sends WM_SETICON, so
// without this the webview window shows the stock application icon. No-op if the resource is
// absent (a build without the .syso keeps the stock icon).
func setWindowIcon(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	hInst, _, _ := procGetModuleHandleW.Call(0)
	load := func(cxMetric, cyMetric uintptr) uintptr {
		cx, _, _ := procGetSystemMetrics.Call(cxMetric)
		cy, _, _ := procGetSystemMetrics.Call(cyMetric)
		h, _, _ := procLoadImageW.Call(hInst, appIconResID, imageIcon, cx, cy, 0)
		if h == 0 { // metric lookup failed - let the system pick the size
			h, _, _ = procLoadImageW.Call(hInst, appIconResID, imageIcon, 0, 0, lrDefaultSize)
		}
		return h
	}
	// Shared icons from the module resource - never destroyed, freed at process exit.
	if small := load(smCxSmIcon, smCySmIcon); small != 0 {
		_, _, _ = procSendMessageW.Call(hwnd, wmSetIcon, iconSmall, small)
	}
	if big := load(smCxIcon, smCyIcon); big != 0 {
		_, _, _ = procSendMessageW.Call(hwnd, wmSetIcon, iconBig, big)
	}
}

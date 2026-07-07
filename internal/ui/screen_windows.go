//go:build windows

package ui

import "syscall"

var (
	scrUser32            = syscall.NewLazyDLL("user32.dll")
	procGetSystemMetrics = scrUser32.NewProc("GetSystemMetrics")
	procGetDpiForSystem  = scrUser32.NewProc("GetDpiForSystem")
)

const (
	smCXScreen = 0
	smCYScreen = 1
)

// screenSizeDIP returns the primary monitor size in Fyne (DPI-independent) units. GetSystemMetrics
// reports physical pixels for a DPI-aware process (glfw makes us one); Fyne sizes in DIPs, so we
// divide by the system DPI scale. ok=false if the metrics can't be read.
func screenSizeDIP() (w, h float32, ok bool) {
	cx, _, _ := procGetSystemMetrics.Call(smCXScreen)
	cy, _, _ := procGetSystemMetrics.Call(smCYScreen)
	if cx == 0 || cy == 0 {
		return 0, 0, false
	}
	dpi := uintptr(96)
	if procGetDpiForSystem.Find() == nil { // Win10 1607+
		if d, _, _ := procGetDpiForSystem.Call(); d >= 48 {
			dpi = d
		}
	}
	scale := float32(dpi) / 96
	if scale <= 0 {
		scale = 1
	}
	return float32(cx) / scale, float32(cy) / scale, true
}

//go:build windows

package i18n

import (
	"syscall"
	"unsafe"
)

// osLocale returns the Windows user UI locale (e.g. "de-DE"). Honors a LANG override first,
// else calls kernel32!GetUserDefaultLocaleName (stdlib syscall - no new dependency).
func osLocale() string {
	if l := envLocale(); l != "" {
		return l
	}
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GetUserDefaultLocaleName")
	buf := make([]uint16, 85) // LOCALE_NAME_MAX_LENGTH
	r, _, _ := proc.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if r == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf)
}

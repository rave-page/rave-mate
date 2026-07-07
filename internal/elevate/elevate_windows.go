//go:build windows

package elevate

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// IsElevated reports whether the current process token is elevated (admin).
func IsElevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

var (
	modshell32          = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteExW = modshell32.NewProc("ShellExecuteExW")
)

// SHELLEXECUTEINFOW (the trailing union is the icon/monitor handle).
type shellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hwnd         uintptr
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     uintptr
	lpIDList     uintptr
	lpClass      *uint16
	hkeyClass    uintptr
	dwHotKey     uint32
	hIconMonitor uintptr
	hProcess     windows.Handle
}

const (
	seeMaskNoCloseProcess = 0x00000040
	swHide                = 0
	swShowNormal          = 1
	errorCancelled        = 1223 // ERROR_CANCELLED - UAC dismissed
)

// StartElevated launches exe elevated (UAC "runas") detached - no wait, so the child outlives
// this process - with args + working dir. Used to relaunch a DJ-rig app that needs admin as
// part of an application group. ErrDeclined if the user dismisses the UAC prompt.
func StartElevated(exe string, args []string, workDir string) error {
	if exe == "" {
		return fmt.Errorf("elevate: empty exe")
	}
	verb, _ := syscall.UTF16PtrFromString("runas")
	file, _ := syscall.UTF16PtrFromString(exe)
	params, _ := syscall.UTF16PtrFromString(buildCmdline(args))
	var dir *uint16
	if workDir != "" {
		dir, _ = syscall.UTF16PtrFromString(workDir)
	}
	info := shellExecuteInfo{
		lpVerb:       verb,
		lpFile:       file,
		lpParameters: params,
		lpDirectory:  dir,
		nShow:        swShowNormal,
	}
	info.cbSize = uint32(unsafe.Sizeof(info))
	r1, _, callErr := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&info)))
	if r1 == 0 {
		if errno, ok := callErr.(syscall.Errno); ok && uintptr(errno) == errorCancelled {
			return ErrDeclined
		}
		return fmt.Errorf("ShellExecuteEx runas failed: %w", callErr)
	}
	return nil
}

// RunSelfElevated relaunches the current exe elevated with args (UAC prompt), waits for it to
// finish, and returns its exit code. ErrDeclined if the user cancels the prompt.
func RunSelfElevated(args []string) (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return -1, err
	}
	verb, _ := syscall.UTF16PtrFromString("runas")
	file, _ := syscall.UTF16PtrFromString(exe)
	params, _ := syscall.UTF16PtrFromString(buildCmdline(args))

	info := shellExecuteInfo{
		fMask:        seeMaskNoCloseProcess,
		lpVerb:       verb,
		lpFile:       file,
		lpParameters: params,
		nShow:        swHide,
	}
	info.cbSize = uint32(unsafe.Sizeof(info))

	r1, _, callErr := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&info)))
	if r1 == 0 {
		if errno, ok := callErr.(syscall.Errno); ok && uintptr(errno) == errorCancelled {
			return -1, ErrDeclined
		}
		return -1, fmt.Errorf("ShellExecuteEx runas failed: %w", callErr)
	}
	if info.hProcess == 0 {
		return 0, nil // launched, no handle to wait on (shouldn't happen with NOCLOSEPROCESS)
	}
	defer func() { _ = windows.CloseHandle(info.hProcess) }()
	if _, err := windows.WaitForSingleObject(info.hProcess, windows.INFINITE); err != nil {
		return -1, err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(info.hProcess, &code); err != nil {
		return -1, err
	}
	return int(code), nil
}

// buildCmdline quotes args into a single Windows command-line string (CommandLineToArgvW rules:
// wrap in quotes when empty or containing space/tab/quote, escape backslashes before quotes).
func buildCmdline(args []string) string {
	var b strings.Builder
	for i, a := range args {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(quoteArg(a))
	}
	return b.String()
}

func quoteArg(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\"") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	bs := 0
	for _, r := range s {
		switch r {
		case '\\':
			bs++
		case '"':
			for ; bs >= 0; bs-- { // double the run of backslashes, then escape the quote
				b.WriteByte('\\')
			}
			bs = 0
			b.WriteByte('"')
		default:
			for ; bs > 0; bs-- {
				b.WriteByte('\\')
			}
			b.WriteRune(r)
		}
	}
	for ; bs > 0; bs-- {
		b.WriteByte('\\')
	}
	b.WriteByte('"')
	return b.String()
}

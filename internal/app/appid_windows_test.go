//go:build windows

package app

// Executed proof for the .lnk stamping path. COM can't be faked here, so the test works on a COPY
// of the real installed shortcut: the risk being guarded is "did writing the property store
// corrupt the shortcut", which only a real .lnk can answer. Skips when rave-mate isn't installed.

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"unsafe"
)

var iidShellLinkW = syscall.GUID{Data1: 0x000214F9, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}

// IShellLinkW (ShObjIdl_core.h) - only GetPath is needed, but the preceding slots must be declared
// so the offset is right.
type iShellLinkWVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
	GetPath        uintptr
	GetIDList      uintptr
	SetIDList      uintptr
	GetDescription uintptr
}

type iShellLinkW struct{ vtbl *iShellLinkWVtbl }

// shortcutTarget returns a .lnk's target path via IShellLinkW::GetPath.
func shortcutTarget(t *testing.T, lnk string) string {
	t.Helper()
	r, _, _ := procCoInitializeEx.Call(0, coinitApartmentThreaded)
	if uint32(r) == 0 || uint32(r) == sFalse {
		defer procCoUninitialize.Call()
	}
	var link *iShellLinkW
	r, _, _ = procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidShellLink)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidShellLinkW)), uintptr(unsafe.Pointer(&link)))
	if int32(r) < 0 {
		t.Fatalf("CoCreateInstance(IShellLinkW): 0x%08x", uint32(r))
	}
	defer syscall.SyscallN(link.vtbl.Release, uintptr(unsafe.Pointer(link)))

	var persist *iPersistFile
	r, _, _ = syscall.SyscallN(link.vtbl.QueryInterface, uintptr(unsafe.Pointer(link)),
		uintptr(unsafe.Pointer(&iidPersistFile)), uintptr(unsafe.Pointer(&persist)))
	if int32(r) < 0 {
		t.Fatalf("QueryInterface(IPersistFile): 0x%08x", uint32(r))
	}
	defer persist.release()

	lnk16, err := syscall.UTF16PtrFromString(lnk)
	if err != nil {
		t.Fatal(err)
	}
	if r, _, _ = syscall.SyscallN(persist.vtbl.Load, uintptr(unsafe.Pointer(persist)),
		uintptr(unsafe.Pointer(lnk16)), 0); int32(r) < 0 {
		t.Fatalf("IPersistFile::Load(%s): 0x%08x", lnk, uint32(r))
	}
	var buf [520]uint16
	if r, _, _ = syscall.SyscallN(link.vtbl.GetPath, uintptr(unsafe.Pointer(link)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0, 0); int32(r) < 0 {
		t.Fatalf("IShellLinkW::GetPath: 0x%08x", uint32(r))
	}
	return syscall.UTF16ToString(buf[:])
}

func TestStampShortcutAppID(t *testing.T) {
	src := startMenuShortcut()
	if src == "" {
		t.Skip("rave-mate not installed - no start-menu shortcut to copy")
	}
	b, err := os.ReadFile(src)
	if err != nil {
		t.Skipf("read %s: %v", src, err)
	}
	dst := filepath.Join(t.TempDir(), "rave-mate.lnk")
	if err := os.WriteFile(dst, b, 0o644); err != nil {
		t.Fatal(err)
	}
	wantTarget := shortcutTarget(t, dst)
	if wantTarget == "" {
		t.Fatal("copied shortcut has no target - cannot prove it survives stamping")
	}

	changed, err := stampShortcutAppID(dst)
	if err != nil {
		t.Fatalf("stamp: %v", err)
	}
	if !changed {
		// Only legitimate if the installed copy already carries the id - then there is nothing
		// for this test to prove about writing.
		t.Skip("shortcut already carried the app id")
	}
	// Idempotent: the second run must read the id back and report no change, or every boot
	// would rewrite the user's shortcut.
	changed, err = stampShortcutAppID(dst)
	if err != nil {
		t.Fatalf("re-stamp: %v", err)
	}
	if changed {
		t.Error("second stamp reported a change - the read-back path is broken")
	}
	// The shortcut must still point where it did: a corrupted .lnk would be a broken launcher.
	if got := shortcutTarget(t, dst); got != wantTarget {
		t.Errorf("target changed by stamping: got %q, want %q", got, wantTarget)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Errorf("stat after stamp: %v", err)
	}
}

func TestSourceShortcutUntouched(t *testing.T) {
	src := startMenuShortcut()
	if src == "" {
		t.Skip("rave-mate not installed")
	}
	// The stamping test must operate on its copy only.
	before, err := os.ReadFile(src)
	if err != nil {
		t.Skipf("read %s: %v", src, err)
	}
	dst := filepath.Join(t.TempDir(), "copy.lnk")
	if err := os.WriteFile(dst, before, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := stampShortcutAppID(dst); err != nil {
		t.Fatalf("stamp copy: %v", err)
	}
	after, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Errorf("source shortcut changed size: %d -> %d", len(before), len(after))
	}
}

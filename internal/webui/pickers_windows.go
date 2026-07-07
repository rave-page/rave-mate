//go:build windows

package webui

// Native Windows file/folder dialogs (studio.Picker) - stdlib syscall only, no new deps.
// Files: comdlg32 GetOpenFileNameW/GetSaveFileNameW. Folder: IFileOpenDialog (FOS_PICKFOLDERS)
// with SHBrowseForFolderW as legacy fallback. Safe from any goroutine: each dialog locks its OS
// thread and runs its own modal message pump (owner hwnd 0). User cancel → ("", nil).

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"rave.page/mate/internal/i18n"
)

var (
	comdlg32               = syscall.NewLazyDLL("comdlg32.dll")
	procGetOpenFileNameW   = comdlg32.NewProc("GetOpenFileNameW")
	procGetSaveFileNameW   = comdlg32.NewProc("GetSaveFileNameW")
	procCommDlgExtendedErr = comdlg32.NewProc("CommDlgExtendedError")

	ole32                = syscall.NewLazyDLL("ole32.dll")
	procCoInitializeEx   = ole32.NewProc("CoInitializeEx")
	procCoUninitialize   = ole32.NewProc("CoUninitialize")
	procCoCreateInstance = ole32.NewProc("CoCreateInstance")
	procCoTaskMemFree    = ole32.NewProc("CoTaskMemFree")

	shell32                  = syscall.NewLazyDLL("shell32.dll")
	procSHBrowseForFolderW   = shell32.NewProc("SHBrowseForFolderW")
	procSHGetPathFromIDListW = shell32.NewProc("SHGetPathFromIDListW")
)

const (
	ofnOverwritePrompt = 0x00000002
	ofnHideReadOnly    = 0x00000004
	ofnNoChangeDir     = 0x00000008
	ofnPathMustExist   = 0x00000800
	ofnFileMustExist   = 0x00001000
	ofnExplorer        = 0x00080000

	coinitApartmentThreaded = 0x2
	sFalse                  = 0x1
	clsctxInprocServer      = 0x1
	hrCancelled             = 0x800704C7 // HRESULT_FROM_WIN32(ERROR_CANCELLED)
	rpcEChangedMode         = 0x80010106 // thread already CoInitialized with a different model

	fosPickFolders     = 0x20
	fosForceFileSystem = 0x40
	sigdnFilesysPath   = 0x80058000

	bifReturnOnlyFSDirs = 0x1
	bifNewDialogStyle   = 0x40

	// COM vtable slots (IUnknown → IModalWindow → IFileDialog → IFileOpenDialog / IShellItem)
	comRelease       = 2
	imwShow          = 3
	fdSetOptions     = 9
	fdGetOptions     = 10
	fdGetResult      = 20
	siGetDisplayName = 5
)

var (
	clsidFileOpenDialog = guid{0xDC1C5A9C, 0xE88A, 0x4DDE, [8]byte{0xA5, 0xA1, 0x60, 0xF8, 0x2A, 0x20, 0xAE, 0xF7}}
	iidIFileOpenDialog  = guid{0xD57C7288, 0xD4AD, 0x4768, [8]byte{0xBE, 0x02, 0x9D, 0x96, 0x95, 0x32, 0xD9, 0x60}}
)

type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

// openFileNameW mirrors OPENFILENAMEW (commdlg.h); Go's natural alignment matches MSVC's here.
type openFileNameW struct {
	lStructSize       uint32
	hwndOwner         uintptr
	hInstance         uintptr
	lpstrFilter       *uint16
	lpstrCustomFilter *uint16
	nMaxCustFilter    uint32
	nFilterIndex      uint32
	lpstrFile         *uint16
	nMaxFile          uint32
	lpstrFileTitle    *uint16
	nMaxFileTitle     uint32
	lpstrInitialDir   *uint16
	lpstrTitle        *uint16
	flags             uint32
	nFileOffset       uint16
	nFileExtension    uint16
	lpstrDefExt       *uint16
	lCustData         uintptr
	lpfnHook          uintptr
	lpTemplateName    *uint16
	pvReserved        uintptr
	dwReserved        uint32
	flagsEx           uint32
}

// browseInfoW mirrors BROWSEINFOW (shlobj.h) for the legacy folder-picker fallback.
type browseInfoW struct {
	hwndOwner      uintptr
	pidlRoot       uintptr
	pszDisplayName *uint16
	lpszTitle      *uint16
	ulFlags        uint32
	lpfn           uintptr
	lParam         uintptr
	iImage         int32
}

// comObj is a raw COM interface pointer; call invokes vtable slot i (first arg = this).
type comObj struct{ vtbl *[32]uintptr }

func (o *comObj) call(slot int, args ...uintptr) uintptr {
	all := append([]uintptr{uintptr(unsafe.Pointer(o))}, args...)
	r, _, _ := syscall.SyscallN(o.vtbl[slot], all...)
	return r
}

// ── studio.Picker (ctx-bridged: the dialog blocks a locked OS thread; ctx cancel just
// unblocks the caller - a modal dialog can't be force-closed, its late result is dropped) ──

type pickRes struct {
	path string
	err  error
}

func awaitPick(ctx context.Context, run func() (string, error)) (string, error) {
	ch := make(chan pickRes, 1)
	go func() {
		p, err := run()
		ch <- pickRes{p, err}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case r := <-ch:
		return r.path, r.err
	}
}

// PickDirectory implements studio.Picker (native folder dialog).
func (u *UI) PickDirectory(ctx context.Context) (string, error) {
	return awaitPick(ctx, pickFolderDialog)
}

// PickFile implements studio.Picker (native open-file dialog).
func (u *UI) PickFile(ctx context.Context) (string, error) {
	return awaitPick(ctx, func() (string, error) { return commonFileDialog(false, "", "") })
}

// ChooseSavePath implements studio.Picker (native save dialog; container = default extension).
func (u *UI) ChooseSavePath(ctx context.Context, defaultPath, container string) (string, error) {
	return awaitPick(ctx, func() (string, error) {
		return commonFileDialog(true, defaultSaveName(defaultPath, container), container)
	})
}

// defaultSaveName suggests "<source-stem>.<container>" (mirrors the Fyne picker).
func defaultSaveName(defaultPath, container string) string {
	if defaultPath == "" {
		return ""
	}
	base := filepath.Base(defaultPath)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	if stem == "" {
		return ""
	}
	if container != "" {
		return stem + "." + strings.TrimPrefix(container, ".")
	}
	return base
}

// ── comdlg32 open/save ──

// commonFileDialog shows GetOpen/GetSaveFileNameW. Cancel (FALSE + CommDlgExtendedError 0) → ("", nil).
func commonFileDialog(save bool, defName, container string) (string, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	buf := make([]uint16, 32768)
	if defName != "" {
		if n, err := syscall.UTF16FromString(defName); err == nil && len(n) < len(buf) {
			copy(buf, n)
		}
	}
	ofn := openFileNameW{
		lpstrFile: &buf[0],
		nMaxFile:  uint32(len(buf)),
		flags:     ofnExplorer | ofnNoChangeDir | ofnHideReadOnly,
	}
	ofn.lStructSize = uint32(unsafe.Sizeof(ofn))

	var filter []uint16
	proc := procGetOpenFileNameW
	if save {
		proc = procGetSaveFileNameW
		ofn.flags |= ofnPathMustExist | ofnOverwritePrompt
		if ext := strings.TrimPrefix(container, "."); ext != "" {
			filter = filterSpec(strings.ToUpper(ext)+" (*."+ext+")", "*."+ext, "All files (*.*)", "*.*")
			if p, err := syscall.UTF16PtrFromString(ext); err == nil {
				ofn.lpstrDefExt = p
			}
		}
	} else {
		ofn.flags |= ofnFileMustExist
	}
	if len(filter) > 0 {
		ofn.lpstrFilter = &filter[0]
	}

	ok, _, _ := proc.Call(uintptr(unsafe.Pointer(&ofn)))
	runtime.KeepAlive(buf)
	runtime.KeepAlive(filter)
	if ok == 0 {
		code, _, _ := procCommDlgExtendedErr.Call()
		if code == 0 {
			return "", nil // user cancel
		}
		return "", fmt.Errorf("file dialog error 0x%04X", code)
	}
	return syscall.UTF16ToString(buf), nil
}

// filterSpec builds a comdlg filter: "label\0pattern\0…\0\0" (embedded NULs, so not UTF16FromString-able whole).
func filterSpec(parts ...string) []uint16 {
	var out []uint16
	for _, s := range parts {
		u, err := syscall.UTF16FromString(s) // includes trailing NUL
		if err != nil {
			return nil
		}
		out = append(out, u...)
	}
	return append(out, 0) // double-NUL terminator
}

// ── folder picker (IFileOpenDialog, SHBrowseForFolderW fallback) ──

// pickFolderDialog shows the modern folder picker. IFileOpenDialog needs an STA thread, but Go
// may schedule us onto a thread another component already CoInitialized as MTA (WASAPI audio,
// media IPC) - there Show() returns "cancelled" without ever displaying. On RPC_E_CHANGED_MODE
// the poisoned thread is destroyed (a locked goroutine exiting kills its thread) and the dialog
// retries on a fresh one.
func pickFolderDialog() (string, error) {
	type res struct {
		path      string
		err       error
		staDenied bool
	}
	for attempt := 0; attempt < 8; attempt++ {
		ch := make(chan res, 1)
		go func() {
			runtime.LockOSThread() // no Unlock on the staDenied path: goroutine exit destroys the thread
			hr, _, _ := procCoInitializeEx.Call(0, coinitApartmentThreaded)
			if uint32(hr) == rpcEChangedMode {
				ch <- res{staDenied: true}
				return
			}
			defer runtime.UnlockOSThread()
			if hr == 0 || hr == sFalse {
				defer func() { _, _, _ = procCoUninitialize.Call() }()
			}
			p, cancelled, err := comFolderDialog()
			if err == nil {
				if cancelled {
					p = ""
				}
				ch <- res{path: p}
				return
			}
			p2, err2 := shBrowseFolder() // COM path unavailable - legacy dialog
			ch <- res{path: p2, err: err2}
		}()
		if r := <-ch; !r.staDenied {
			return r.path, r.err
		}
	}
	return "", fmt.Errorf("folder dialog: no STA-capable thread after 8 attempts")
}

func comFolderDialog() (path string, cancelled bool, err error) {
	var dlg *comObj
	hr, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidFileOpenDialog)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidIFileOpenDialog)), uintptr(unsafe.Pointer(&dlg)))
	if hr != 0 || dlg == nil {
		return "", false, hrErr("CoCreateInstance(FileOpenDialog)", hr)
	}
	defer dlg.call(comRelease)

	var opts uint32
	if hr := dlg.call(fdGetOptions, uintptr(unsafe.Pointer(&opts))); hr != 0 {
		return "", false, hrErr("GetOptions", hr)
	}
	if hr := dlg.call(fdSetOptions, uintptr(opts|fosPickFolders|fosForceFileSystem)); hr != 0 {
		return "", false, hrErr("SetOptions", hr)
	}
	switch hr := dlg.call(imwShow, 0); uint32(hr) { // uint32: HRESULTs may come back sign-extended
	case 0:
	case hrCancelled:
		return "", true, nil
	default:
		return "", false, hrErr("Show", hr)
	}
	var item *comObj
	if hr := dlg.call(fdGetResult, uintptr(unsafe.Pointer(&item))); hr != 0 || item == nil {
		return "", false, hrErr("GetResult", hr)
	}
	defer item.call(comRelease)
	var pw *uint16
	if hr := item.call(siGetDisplayName, sigdnFilesysPath, uintptr(unsafe.Pointer(&pw))); hr != 0 || pw == nil {
		return "", false, hrErr("GetDisplayName", hr)
	}
	defer func() { _, _, _ = procCoTaskMemFree.Call(uintptr(unsafe.Pointer(pw))) }()
	return utf16PtrToString(pw), false, nil
}

func shBrowseFolder() (string, error) {
	disp := make([]uint16, syscall.MAX_PATH)
	title, _ := syscall.UTF16PtrFromString(i18n.T("picker.selectFolder"))
	bi := browseInfoW{
		pszDisplayName: &disp[0],
		lpszTitle:      title,
		ulFlags:        bifReturnOnlyFSDirs | bifNewDialogStyle,
	}
	pidl, _, _ := procSHBrowseForFolderW.Call(uintptr(unsafe.Pointer(&bi)))
	runtime.KeepAlive(disp)
	if pidl == 0 {
		return "", nil // user cancel
	}
	defer func() { _, _, _ = procCoTaskMemFree.Call(pidl) }()
	buf := make([]uint16, 32768)
	ok, _, _ := procSHGetPathFromIDListW.Call(pidl, uintptr(unsafe.Pointer(&buf[0])))
	if ok == 0 {
		return "", fmt.Errorf("SHGetPathFromIDListW failed")
	}
	return syscall.UTF16ToString(buf), nil
}

func hrErr(what string, hr uintptr) error { return fmt.Errorf("%s: HRESULT 0x%08X", what, hr) }

// utf16PtrToString copies a NUL-terminated UTF-16 string (e.g. a CoTaskMem PWSTR).
func utf16PtrToString(p *uint16) string {
	if p == nil {
		return ""
	}
	n := 0
	for ptr := unsafe.Pointer(p); *(*uint16)(ptr) != 0; ptr = unsafe.Add(ptr, 2) {
		n++
	}
	return syscall.UTF16ToString(unsafe.Slice(p, n))
}

//go:build windows && manualpick

package webui

import (
	"runtime"
	"testing"
)

// Manual smoke test for the native folder dialog (opens a real dialog - excluded from normal
// runs via the manualpick tag). Close/cancel the dialog to finish.
func TestPickFolderManual(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hr, _, _ := procCoInitializeEx.Call(0, coinitApartmentThreaded)
	t.Logf("CoInitializeEx hr=0x%X", hr)
	p, cancelled, err := comFolderDialog()
	t.Logf("comFolderDialog: path=%q cancelled=%v err=%v", p, cancelled, err)
	if err != nil {
		p2, err2 := shBrowseFolder()
		t.Logf("shBrowseFolder: path=%q err=%v", p2, err2)
	}
}

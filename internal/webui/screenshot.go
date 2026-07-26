package webui

// Window capture, routed to the process that OWNS the window.
//
// Why: under RAVE_MATE_SHELL=proc, `ctl screenshot`/`screenshot-all` produced SOLID BLACK PNGs for
// every tab while the DOM, snapshot and click paths all worked. Root cause found by execution, and it
// is NOT the process boundary: featurehost spawns children with sysexec.Hide (STARTF_USESHOWWINDOW +
// SW_HIDE) so no console window appears, and Windows applies that show state to the process's FIRST
// top-level window - so the child's WebView2 window was created HIDDEN. A window that was never shown
// has no rendering for PrintWindow to return, hence black; the UI was invisible for the same reason.
// Proof: capturing the same window before/after a ShowWindow went 0.00% -> 97.89% non-black pixels.
// The fix for THAT is the child revealing its window on ready (shell_proc_child.go).
//
// The capture still moved into the child, deliberately: same-process PrintWindow is the proven-good
// path (it is what the in-proc shell has always used), and routing the request instead of the handle
// takes cross-process GDI/DWM behaviour out of the verification path for good. Measured 33-37 ms per
// capture, against ScreenshotAll's 300 ms per-tab settle. Only a path and a rect cross the pipe - no
// pixels, no base64, no framing budget to blow.

// captureRegion writes a PNG of the UI's window. x/y/w/h in device px; w<=0||h<=0 = full window.
func (u *UI) captureRegion(path string, x, y, w, h int) error {
	if ps, ok := u.shell.(*procShell); ok {
		return ps.captureRegion(path, x, y, w, h)
	}
	return u.captureRegionLocal(path, x, y, w, h)
}

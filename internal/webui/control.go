package webui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// This file implements the ctl control plane against the Go-rendered DOM, so `rave-mate ctl
// snapshot/click/tap/type/read/set/screenshot*` keep working under the webview UI (the
// verify-rave-mate-ui skill depends on them). It drives the page via the webview eval binding -
// the JS run is Go-generated or a local-operator ctl command (trusted, loopback-only), never
// remote/untrusted input.

const evalTimeout = 3 * time.Second

// evalValue runs js on the page and returns the JSON-decoded result (via the __rave_evalResult
// round-trip). ok=false on no-shell or timeout.
func (u *UI) evalValue(js string) (any, bool) {
	if u.shell == nil {
		return nil, false
	}
	id := nextEvalID()
	ch := make(chan string, 1)
	evalWaiters.Store(id, ch)
	defer evalWaiters.Delete(id)
	// Wrap so a returned value (or thrown error) is marshaled back to Go by id.
	wrapped := "(async()=>{try{var r=await (async()=>{" + js + "})();window.__rave_evalResult(" +
		jsQuote(id) + ",JSON.stringify(r===undefined?null:r));}catch(e){window.__rave_evalResult(" +
		jsQuote(id) + ",JSON.stringify('ERR:'+String(e)));}})()"
	u.shell.eval(wrapped)
	select {
	case raw := <-ch:
		var v any
		if json.Unmarshal([]byte(raw), &v) == nil {
			return v, true
		}
		return raw, true
	case <-time.After(evalTimeout):
		return nil, false
	}
}

func (u *UI) evalString(js string) (string, bool) {
	v, ok := u.evalValue(js)
	if !ok || v == nil {
		return "", false
	}
	if s, isStr := v.(string); isStr {
		return s, true
	}
	return fmt.Sprint(v), true
}

func (u *UI) evalBool(js string) bool {
	v, ok := u.evalValue(js)
	if !ok {
		return false
	}
	b, _ := v.(bool)
	return b
}

// Snapshot renders the visible DOM to a text+role outline (with ⚠OVERFLOW flags) - the webview
// equivalent of the Fyne widget-tree snapshot.
func (u *UI) Snapshot() string {
	if u.shell == nil {
		return "(no UI - running in service mode)\n"
	}
	s, ok := u.evalString("return window.__snapshot ? window.__snapshot() : ''")
	if !ok {
		return "(webview snapshot unavailable)\n"
	}
	return s + "\n"
}

func (u *UI) Click(query string) bool {
	if u.shell == nil || query == "" {
		return false
	}
	return u.evalBool("return window.__click(" + jsQuote(query) + ")")
}

func (u *UI) Tap(x, y float32) bool {
	if u.shell == nil {
		return false
	}
	return u.evalBool(fmt.Sprintf("return window.__tap(%g,%g)", x, y))
}

// TapSecondary right-clicks at (x,y): fires the context menu for the [data-ctx] element there.
func (u *UI) TapSecondary(x, y float32) bool {
	if u.shell == nil {
		return false
	}
	return u.evalBool(fmt.Sprintf("return window.__ctx(%g,%g)", x, y))
}

// Act posts an action through the page transport (window.rave), exactly as a page
// event would - lets ctl drive act-level surfaces (keyboard scopes, pointer lanes)
// that have no clickable DOM element. Local-operator verification use.
func (u *UI) Act(act, val string) bool {
	if u.shell == nil || act == "" {
		return false
	}
	payload := `{"act":` + jsQuote(act) + `,"val":` + jsQuote(val) + `}`
	u.eval("window.rave&&window.rave(" + jsQuote(payload) + ")")
	return true
}

func (u *UI) Type(text string) bool {
	if u.shell == nil {
		return false
	}
	return u.evalBool("return window.__type(" + jsQuote(text) + ")")
}

func (u *UI) Read(query string) (string, bool) {
	if u.shell == nil {
		return "", false
	}
	v, ok := u.evalValue("return window.__read(" + jsQuote(query) + ")")
	if !ok || v == nil {
		return "", false
	}
	if s, isStr := v.(string); isStr {
		return s, true
	}
	return fmt.Sprint(v), true
}

func (u *UI) Set(query, value string) bool {
	if u.shell == nil {
		return false
	}
	return u.evalBool("return window.__set(" + jsQuote(query) + "," + jsQuote(value) + ")")
}

func (u *UI) Resize(w, h float32) {
	if u.shell != nil && w >= 1 && h >= 1 {
		u.shell.resize(int(w), int(h))
	}
}

// Screenshot captures the whole window to a PNG (OS window capture off the native HWND).
func (u *UI) Screenshot(path string) error { return u.captureRegion(path, 0, 0, 0, 0) }

// ScreenshotRegion captures a sub-rect (device px).
func (u *UI) ScreenshotRegion(path string, x, y, w, h float32) error {
	return u.captureRegion(path, int(x), int(y), int(w), int(h))
}

// ── ctl tab intents ──
// ctl tab switches post through the page act pipeline (eval → window.rave → acts chan →
// actWorker) so the render runs serialized on the act worker, not on the ctl connection
// goroutine (was one of the concurrent render families). Reply chan = settle barrier.

var ctlTabWaiters sync.Map // string id -> chan struct{}, closed once setTab returned

func init() {
	onExact("__ctl-tab", func(u *UI, m actMsg) {
		u.setTab(m.Val)
		if ch, ok := ctlTabWaiters.LoadAndDelete(m.ID); ok {
			close(ch.(chan struct{}))
		}
	})
}

// setTabViaActs posts a tab-select intent and waits until the act worker finished the switch.
// Timeout (binding not up yet / a wedged handler) degrades to the old direct setTab so ctl
// keeps working during startup.
func (u *UI) setTabViaActs(id string) {
	waitID := nextEvalID()
	ch := make(chan struct{})
	ctlTabWaiters.Store(waitID, ch)
	defer ctlTabWaiters.Delete(waitID)
	payload := `{"act":"__ctl-tab","val":` + jsQuote(id) + `,"id":` + jsQuote(waitID) + `}`
	u.eval("window.rave&&window.rave(" + jsQuote(payload) + ")")
	select {
	case <-ch:
	case <-time.After(evalTimeout):
		u.setTab(id)
	}
}

// ScreenshotAll sweeps every enabled tab to a PNG + writes report.txt (the whole-UI verification
// pass), then restores the user's prior tab. First line = totals (appControl prefixes "ok ").
func (u *UI) ScreenshotAll(dir string) (string, error) {
	if u.shell == nil {
		return "", fmt.Errorf("no UI (service mode)")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	prev := u.activeTab()
	var rep strings.Builder
	n, errs := 0, 0
	for _, t := range u.tabs() {
		if !t.enabled {
			continue
		}
		u.setTabViaActs(t.id)
		time.Sleep(300 * time.Millisecond) // let the fragment render + fonts settle
		p := filepath.Join(dir, "tab-"+t.id+".png")
		status := "ok"
		if err := u.Screenshot(p); err != nil {
			status = "ERR " + err.Error()
			errs++
		}
		if snap, ok := u.evalString("return window.__snapshot ? window.__snapshot() : ''"); ok && strings.Contains(snap, "⚠OVERFLOW") {
			status += " ⚠OVERFLOW"
		}
		fmt.Fprintf(&rep, "%-12s %s  %s\n", t.id, p, status)
		n++
	}
	u.setTabViaActs(prev)
	head := fmt.Sprintf("%d tabs, %d errors\n", n, errs)
	_ = os.WriteFile(filepath.Join(dir, "report.txt"), []byte(head+rep.String()), 0o644)
	return head, nil
}

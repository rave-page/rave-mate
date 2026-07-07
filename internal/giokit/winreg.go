package giokit

import (
	"fmt"
	"strings"
	"sync"
)

// Package-level Gio window registry: NewWindow registers each window's *Registry under a
// unique ctl ID so `rave-mate ctl gio-snapshot/gio-tap` can enumerate + drive any live Gio
// surface - the Gio counterpart of the Fyne ctl snapshot/tap machinery.
var (
	winMu    sync.Mutex
	winRegs  = map[string]*winEntry{}
	winOrder []string // creation order for stable listings
)

type winEntry struct {
	title string
	reg   *Registry
}

// RegisterWindow exposes reg under a unique ID derived from name (slug; "#2" suffix on
// collision) and returns it. UnregisterWindow with the same ID on destroy.
func RegisterWindow(name, title string, reg *Registry) string {
	base := slug(name)
	if base == "" {
		base = "window"
	}
	winMu.Lock()
	defer winMu.Unlock()
	id := base
	for n := 2; ; n++ {
		if _, taken := winRegs[id]; !taken {
			break
		}
		id = fmt.Sprintf("%s#%d", base, n)
	}
	winRegs[id] = &winEntry{title: title, reg: reg}
	winOrder = append(winOrder, id)
	return id
}

// UnregisterWindow removes a window from the ctl registry. Unknown ID is a no-op.
func UnregisterWindow(id string) {
	winMu.Lock()
	defer winMu.Unlock()
	if _, ok := winRegs[id]; !ok {
		return
	}
	delete(winRegs, id)
	for i, v := range winOrder {
		if v == id {
			winOrder = append(winOrder[:i], winOrder[i+1:]...)
			break
		}
	}
}

// WindowIDs lists registered Gio windows in creation order as "id\ttitle" lines.
func WindowIDs() []string {
	winMu.Lock()
	defer winMu.Unlock()
	out := make([]string, 0, len(winOrder))
	for _, id := range winOrder {
		out = append(out, id+"\t"+winRegs[id].title)
	}
	return out
}

// SnapshotText returns window id's labeled-control tree from the last completed frame
// (one `kind "label" (x0,y0)-(x1,y1)` line per node), or ok=false for an unknown ID.
func SnapshotText(id string) (string, bool) {
	winMu.Lock()
	e := winRegs[id]
	winMu.Unlock()
	if e == nil {
		return "", false
	}
	var b strings.Builder
	fmt.Fprintf(&b, "gio window %q - %s\n", id, e.title)
	nodes := e.reg.Snapshot()
	if len(nodes) == 0 {
		b.WriteString("  (no labeled controls in the last frame)\n")
	}
	for _, n := range nodes {
		fmt.Fprintf(&b, "  %s %q (%d,%d)-(%d,%d)\n", n.Kind, n.Label,
			n.Bounds.Min.X, n.Bounds.Min.Y, n.Bounds.Max.X, n.Bounds.Max.Y)
	}
	return b.String(), true
}

// TapWindow queues an activation of control in window id (runs at the window's next
// frame on its UI goroutine).
func TapWindow(id, control string) error {
	winMu.Lock()
	e := winRegs[id]
	winMu.Unlock()
	if e == nil {
		return fmt.Errorf("unknown gio window %q", id)
	}
	if !e.reg.Activate(control) {
		return fmt.Errorf("no activatable control %q in gio window %q", control, id)
	}
	return nil
}

// slug lowercases and dashes a window name into a ctl-safe ID.
func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.':
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	return strings.TrimRight(b.String(), "-")
}

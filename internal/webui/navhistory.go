package webui

import "sync"

// Browser-style back/forward navigation for the webview. The shell forwards the mouse X1/X2 back/
// forward buttons (and Alt+←/→) as nav-back/nav-fwd acts; this restores an earlier view. A view is
// the active tab + the sub-nav that makes it meaningful (Library section/cwd, Settings section).

const navHistCap = 100 // bounded: drop-oldest past this many back entries

// navSnap is a restorable navigation position.
type navSnap struct {
	tab, libSection, libDir, setSec string
}

// navHist holds the back/forward stacks (guarded by mu).
type navHist struct {
	mu   sync.Mutex
	back []navSnap
	fwd  []navSnap
}

// navSnapshotNow captures the current position (locks u.mu + u.setMu, never u.nav.mu).
func (u *UI) navSnapshotNow() navSnap {
	u.mu.Lock()
	s := navSnap{tab: u.active, libSection: u.libSection, libDir: u.libDir}
	u.mu.Unlock()
	u.setMu.Lock()
	s.setSec = u.setSec
	u.setMu.Unlock()
	return s
}

// navRecord pushes the CURRENT position onto back + clears forward. Call BEFORE mutating state for
// a user-initiated navigation. Deduped so repeated events / no-op re-selects don't stack entries.
func (u *UI) navRecord() {
	cur := u.navSnapshotNow()
	u.nav.mu.Lock()
	defer u.nav.mu.Unlock()
	if n := len(u.nav.back); n > 0 && u.nav.back[n-1] == cur {
		return
	}
	u.nav.back = append(u.nav.back, cur)
	if len(u.nav.back) > navHistCap {
		u.nav.back = u.nav.back[len(u.nav.back)-navHistCap:]
	}
	u.nav.fwd = nil
}

// navBack restores the previous position (mouse-back / Alt+←). No-op at the start of history.
func (u *UI) navBack() {
	cur := u.navSnapshotNow()
	u.nav.mu.Lock()
	if len(u.nav.back) == 0 {
		u.nav.mu.Unlock()
		return
	}
	prev := u.nav.back[len(u.nav.back)-1]
	u.nav.back = u.nav.back[:len(u.nav.back)-1]
	u.nav.fwd = append(u.nav.fwd, cur)
	u.nav.mu.Unlock()
	u.navApply(prev)
}

// navFwd re-applies a position undone by navBack (mouse-forward / Alt+→). No-op at the tip.
func (u *UI) navFwd() {
	cur := u.navSnapshotNow()
	u.nav.mu.Lock()
	if len(u.nav.fwd) == 0 {
		u.nav.mu.Unlock()
		return
	}
	next := u.nav.fwd[len(u.nav.fwd)-1]
	u.nav.fwd = u.nav.fwd[:len(u.nav.fwd)-1]
	u.nav.back = append(u.nav.back, cur)
	u.nav.mu.Unlock()
	u.navApply(next)
}

// navApply restores a snapshot's state and re-renders WITHOUT recording history.
func (u *UI) navApply(s navSnap) {
	u.mu.Lock()
	u.active, u.libSection, u.libDir = s.tab, s.libSection, s.libDir
	u.mu.Unlock()
	u.setMu.Lock()
	u.setSec = s.setSec
	u.setMu.Unlock()
	u.patchMain()
	u.eval("window.__patch('nav-list'," + jsQuote(u.navListHTML()) + ")")
}

package vroverlay

import (
	"fmt"
	"strings"
	"time"
)

// Interaction-event ring: records what the in-VR input actually did (summon press/hold, menu click +
// which action, grab start/end, pointer hit/click) so a paired desk instance can SEE via ctl
// remote-vrinput why a control "does nothing" - snapshots don't reveal dynamic interaction.

const evtRingMax = 48

// evt records one timestamped interaction event (thread-safe; called from the 90Hz editor goroutine).
func (e *editor) evt(format string, a ...any) {
	line := time.Now().Format("15:04:05.000") + " " + fmt.Sprintf(format, a...)
	e.evtMu.Lock()
	e.evtRing = append(e.evtRing, line)
	if len(e.evtRing) > evtRingMax {
		e.evtRing = e.evtRing[len(e.evtRing)-evtRingMax:]
	}
	e.evtMu.Unlock()
}

// recentEvents dumps the interaction ring newest-last (empty string when nothing recorded).
func (e *editor) recentEvents() string {
	e.evtMu.Lock()
	defer e.evtMu.Unlock()
	if len(e.evtRing) == 0 {
		return ""
	}
	return strings.Join(e.evtRing, "\n")
}

// ptrDebug is a per-frame snapshot of the pointer/editor state (menu, hands, hit geometry) so a
// remote can SEE what the pointer is doing - active hand, where it's aiming vs which row it maps to,
// which hand holds the menu - without the user narrating it. Captured on the editor goroutine.
type ptrDebug struct {
	on, editMode        bool
	active              Hand
	menuHand, exclHand  Hand
	lTracked, rTracked  bool
	lHeld, rHeld        bool
	aimActive           bool // the active hand's aim (/pose/tip) pose is live (else the ray uses the offset raw device pose). Truthful - from AimPose, not GetActionOrigins.
	hitOK               bool
	hitKey              string
	hitU, hitV, hitDist float32
	hitRow              int // menu row the hit maps to (-1 = title, -99 = not the menu)
}

func (e *editor) setDbg(d ptrDebug) {
	e.evtMu.Lock()
	e.dbg = d
	e.evtMu.Unlock()
}

func handName(h Hand) string {
	switch h {
	case HandLeft:
		return "L"
	case HandRight:
		return "R"
	case HandHead:
		return "head"
	default:
		return "none"
	}
}

// debugState formats the last captured pointer/editor snapshot for the input diagnostic.
func (e *editor) debugState() string {
	e.evtMu.Lock()
	d := e.dbg
	menus := e.menuDiagStr
	e.evtMu.Unlock()
	var b strings.Builder
	fmt.Fprintf(&b, "menu: open=%v editMode=%v menuHand=%s exclHand=%s aimActive=%v\n",
		d.on, d.editMode, handName(d.menuHand), handName(d.exclHand), d.aimActive)
	fmt.Fprintf(&b, "hands: active=%s  L(track=%v trig=%v) R(track=%v trig=%v)\n",
		handName(d.active), d.lTracked, d.lHeld, d.rTracked, d.rHeld)
	if d.hitOK {
		fmt.Fprintf(&b, "pointer: hit=%s u=%.3f v=%.3f dist=%.2fm row=%d\n",
			strings.TrimPrefix(d.hitKey, "page.rave.mate."), d.hitU, d.hitV, d.hitDist, d.hitRow)
	} else {
		b.WriteString("pointer: hit=(none)\n")
	}
	if menus != "" {
		b.WriteString("menu textures (shown snapshot vs GPU):\n" + menus)
	}
	return b.String()
}

// updateMenuDiag records, per menu overlay, the alignment triple a remote diag needs to PROVE the
// displayed texture matches what hover/clicks map against: shown-snapshot rows, last-uploaded
// texture WxH, and the GPU-side size + bounds SteamVR reports (GetOverlayTextureSize). MISMATCH =
// the compositor displays something other than the click map - the exact hover/dot offset bug.
// Runs on the editor goroutine (tick); reads via debugState under evtMu.
func (e *editor) updateMenuDiag() {
	var b strings.Builder
	for _, key := range [3]string{menuKey, posKey, dashKey} {
		up, has := e.menuTexWH[key]
		if !has {
			continue
		}
		shown := e.shownMenu(key)
		fmt.Fprintf(&b, "  %s: shownRows=%d uploaded=%dx%d", strings.TrimPrefix(key, "page.rave.mate."), shown.rows, up[0], up[1])
		if e.ed != nil {
			if gw, gh, bounds, ok := e.ed.TextureInfo(key); ok {
				match := "OK"
				if gw != up[0] || gh != up[1] {
					match = "MISMATCH"
				}
				fmt.Fprintf(&b, " gpu=%dx%d bounds=[%.2f %.2f %.2f %.2f] %s", gw, gh, bounds[0], bounds[1], bounds[2], bounds[3], match)
			} else {
				b.WriteString(" gpu=?")
			}
		}
		b.WriteByte('\n')
	}
	s := b.String()
	e.evtMu.Lock()
	e.menuDiagStr = s
	e.evtMu.Unlock()
}

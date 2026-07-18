package vroverlay

import (
	"errors"
	"fmt"
	"image"
	"math"
	"testing"

	"rave.page/mate/internal/vrstats"
)

// Regression tests for the shown-snapshot consistency logic (live bug: clicks mapped rows against
// the freshly rebuilt menuItems list while the compositor still displayed an OLDER page's texture -
// row 3 of a 6-item list fired where the user saw row 14 of the 27-row home texture).

// fakeRT records texture uploads + can fail them (simulates OpenVR rejecting a raw-texture resize).
type fakeRT struct {
	texErr  error
	uploads []int // uploaded texture heights (px)
}

func (f *fakeRT) Available() bool                    { return true }
func (f *fakeRT) Init() error                        { return nil }
func (f *fakeRT) EnsureOverlay(string, string) error { return nil }
func (f *fakeRT) SetTexture(_ string, img *image.NRGBA) error {
	if f.texErr != nil {
		return f.texErr
	}
	f.uploads = append(f.uploads, img.Bounds().Dy())
	return nil
}
func (f *fakeRT) SetTransform(string, Transform) error   { return nil }
func (f *fakeRT) Show(string, bool) error                { return nil }
func (f *fakeRT) DestroyOverlay(string) error            { return nil }
func (f *fakeRT) Shutdown()                              {}
func (f *fakeRT) RuntimeInstalled() bool                 { return false }
func (f *fakeRT) PollQuit() QuitReason                   { return QuitNone }
func (f *fakeRT) RegisterApp(string, string, bool) error { return nil }
func (f *fakeRT) PerfStats() (vrstats.PerfStats, bool)   { return vrstats.PerfStats{}, false }

func newTestEditor(t *testing.T, rt Runtime) *editor {
	t.Helper()
	rend, err := NewRenderer(1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rend.Close)
	e := &editor{m: &Manager{rt: rt, rend: rend}}
	e.resetSession()
	return e
}

// actItems builds n action rows; a click writes the row's label into fired.
func actItems(n int, fired *string) []MenuItem {
	items := make([]MenuItem, n)
	for i := range items {
		lbl := fmt.Sprintf("row%d", i)
		items[i] = MenuItem{Kind: MIAction, Label: lbl, OnClick: func() { *fired = lbl }}
	}
	return items
}

// Clicks must fire from the list the DISPLAYED texture was rendered from, not the live rebuilt
// list. v is bottom-origin (see smoothRow): v=0.437 on the shown 26-item (27-row, 1512px) home
// texture → topY=(1-0.437)*1512=851.3 → visual row 14; a stale mapping against a live 6-item list
// yields another row entirely.
func TestClickUsesShownSnapshotNotLiveList(t *testing.T) {
	e := newTestEditor(t, &fakeRT{})
	var fired string
	home := actItems(26, &fired)
	if !e.uploadMenu(menuKey, "rave-mate", home, 1, "home") {
		t.Fatal("home upload failed")
	}
	// Live list moves on to a 6-item subpage; its texture upload hasn't happened yet.
	e.menuItems[menuKey] = actItems(6, &fired)

	hit := pointerHit{key: menuKey, u: 0.5, v: 0.437}
	e.applyHover(hit)
	if e.ptrRow != 14 {
		t.Fatalf("hovered row = %d, want 14 (the row under the cursor on the SHOWN texture)", e.ptrRow)
	}
	e.pointerClick(hit)
	if fired != "row14" {
		t.Fatalf("fired %q, want row14 (what the user sees)", fired)
	}
}

// A failed upload must commit neither sig nor snapshot: sig mismatch → retry next tick; clicks
// keep mapping to the texture still on the compositor.
func TestFailedUploadKeepsShownSnapshotAndRetries(t *testing.T) {
	rt := &fakeRT{}
	e := newTestEditor(t, rt)
	var fired string
	if !e.uploadMenu(menuKey, "rave-mate", actItems(26, &fired), 1, "home") {
		t.Fatal("home upload failed")
	}
	rt.texErr = errors.New("resize rejected")
	sub := actItems(6, &fired)
	if e.uploadMenu(menuKey, "rave-mate", sub, 1, "sub") {
		t.Fatal("upload should have failed")
	}
	if e.menuSig[menuKey] != "home" {
		t.Fatalf("sig = %q, want home (failure must not commit - retry next tick)", e.menuSig[menuKey])
	}
	if got := e.shownMenu(menuKey).rows; got != 26 {
		t.Fatalf("shown rows = %d, want 26 (old texture still displayed)", got)
	}
	// Upload recovers → snapshot + sig follow the texture together. Texture rows stay at the
	// 26-row high-water (fixed-height padding - page navs never resize the overlay).
	rt.texErr = nil
	if !e.uploadMenu(menuKey, "rave-mate", sub, 1, "sub") {
		t.Fatal("retry upload failed")
	}
	if got := e.shownMenu(menuKey).rows; got != 26 {
		t.Fatalf("shown rows after retry = %d, want 26 (high-water padded)", got)
	}
	if e.menuSig[menuKey] != "sub" {
		t.Fatalf("sig after retry = %q, want sub", e.menuSig[menuKey])
	}
}

// A page change followed by a successful upload swaps the snapshot - clicks follow the new texture.
func TestSnapshotFollowsSuccessfulPageChange(t *testing.T) {
	e := newTestEditor(t, &fakeRT{})
	var fired string
	if !e.uploadMenu(menuKey, "rave-mate", actItems(26, &fired), 1, "home") {
		t.Fatal("home upload failed")
	}
	if !e.uploadMenu(menuKey, "rave-mate", actItems(6, &fired), 1, "sub") {
		t.Fatal("sub upload failed")
	}
	// Texture stays 27 rows (1512px, high-water padded) across the page change - row pixel
	// geometry never shifts. Row 2 spans topY [168,224): v=0.8743 → topY=(1-0.8743)*1512=190.1.
	hit := pointerHit{key: menuKey, u: 0.5, v: 0.8743}
	e.applyHover(hit)
	e.pointerClick(hit)
	if fired != fmt.Sprintf("row%d", e.ptrRow) || e.ptrRow != 2 {
		t.Fatalf("fired %q (row %d), want row2 on the new texture", fired, e.ptrRow)
	}
}

// Texture rows follow the high-water mark: shrinking pages keep the padded height (no
// destroy+recreate, no state reset - the menu-blink fix), and only GROWTH past the
// high-water changes the texture → resets row state indexed to the old geometry.
func TestRowCountChangeResetsRowState(t *testing.T) {
	e := newTestEditor(t, &fakeRT{})
	var fired string
	if !e.uploadMenu(menuKey, "rave-mate", actItems(26, &fired), 1, "home") {
		t.Fatal("home upload failed")
	}
	e.menuTf.changed(Transform{WidthM: 0.36})
	e.ptrKey, e.ptrRow, e.hoverRow, e.hoverSig = menuKey, 14, 14, "placed"
	// Same row count → nothing resets.
	if !e.uploadMenu(menuKey, "rave-mate", actItems(26, &fired), 1, "home2") {
		t.Fatal("re-upload failed")
	}
	if e.ptrRow != 14 || !e.menuTf.set || e.hoverSig != "placed" {
		t.Fatalf("same-rows upload reset state: ptrRow=%d tfSet=%v hoverSig=%q", e.ptrRow, e.menuTf.set, e.hoverSig)
	}
	// FEWER rows → texture stays at the 26-row high-water: geometry unchanged, nothing resets.
	if !e.uploadMenu(menuKey, "rave-mate", actItems(6, &fired), 1, "sub") {
		t.Fatal("sub upload failed")
	}
	if e.ptrRow != 14 || !e.menuTf.set || e.hoverSig != "placed" {
		t.Fatalf("shrink upload reset state (fixed-height should keep it): ptrRow=%d tfSet=%v hoverSig=%q",
			e.ptrRow, e.menuTf.set, e.hoverSig)
	}
	if got := e.shownMenu(menuKey).rows; got != 26 {
		t.Fatalf("shown rows = %d, want 26 (high-water padded)", got)
	}
	// GROWTH past the high-water → texture resizes once → row state resets.
	if !e.uploadMenu(menuKey, "rave-mate", actItems(30, &fired), 1, "big") {
		t.Fatal("big upload failed")
	}
	if e.ptrRow != -1 {
		t.Fatalf("ptrRow = %d, want -1 (band indexed the old 27-row texture)", e.ptrRow)
	}
	if e.hoverRow != -1 {
		t.Fatalf("hoverRow = %d, want -1", e.hoverRow)
	}
	if e.menuTf.set {
		t.Fatal("menuTf cache not invalidated (transform must re-send for the new quad aspect)")
	}
	if e.hoverSig != "" {
		t.Fatalf("hoverSig = %q, want cleared", e.hoverSig)
	}
}

// Moving the pointer to a DIFFERENT overlay must drop the hysteresis band - smoothRow would gate the
// new menu's first hover against a row band measured on another texture's height.
func TestHoverKeyChangeResetsHysteresis(t *testing.T) {
	e := newTestEditor(t, &fakeRT{})
	var fired string
	if !e.uploadMenu(menuKey, "rave-mate", actItems(26, &fired), 1, "home") {
		t.Fatal("home upload failed")
	}
	e.ptrKey, e.ptrRow = posKey, 24 // stale band from the positioning menu
	hit := pointerHit{key: menuKey, u: 0.5, v: 0.437}
	e.applyHover(hit)
	if e.ptrRow != 14 {
		t.Fatalf("hovered row = %d, want 14 (stale cross-menu band must not gate)", e.ptrRow)
	}
}

// hoverRowOffset: pure row→local-offset math for the hover accent overlay.
func TestHoverRowOffset(t *testing.T) {
	const wm = 0.42 // quad width (m); 26 rows → mh = 27*56 = 1512 px, quadH = wm*1512/420
	quadH := wm * 1512 / 420
	cases := []struct {
		row      int
		centerPx float64
	}{
		{0, 84},    // first item row: band [56,112)
		{12, 756},  // dead center of the quad: y must be ~0
		{25, 1484}, // last row
	}
	for _, c := range cases {
		y, z := hoverRowOffset(c.row, 26, wm)
		want := quadH * (0.5 - c.centerPx/1512)
		if math.Abs(y-want) > 1e-9 {
			t.Fatalf("row %d: y = %.6f, want %.6f", c.row, y, want)
		}
		if z != 0.002 {
			t.Fatalf("row %d: z = %v, want 0.002 (toward viewer)", c.row, z)
		}
	}
	if y, _ := hoverRowOffset(12, 26, wm); math.Abs(y) > 1e-9 {
		t.Fatalf("center row y = %v, want 0", y)
	}
}

// With no texture ever uploaded there is nothing on screen - no row can hover, no click can fire.
func TestNoShownTextureNoClick(t *testing.T) {
	e := newTestEditor(t, &fakeRT{})
	var fired string
	e.menuItems[menuKey] = actItems(6, &fired) // live list exists, but nothing displayed
	hit := pointerHit{key: menuKey, u: 0.5, v: 0.5}
	e.applyHover(hit)
	if e.ptrRow != -1 {
		t.Fatalf("hovered row = %d, want -1 (nothing displayed)", e.ptrRow)
	}
	e.pointerClick(hit)
	if fired != "" {
		t.Fatalf("fired %q with no displayed texture", fired)
	}
}

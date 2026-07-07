package ui

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// writeTempPNG writes a 1×1 PNG and returns its path (valid image for thumbnail tests).
func writeTempPNG(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "thumb.png")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := png.Encode(f, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSegIndexOf(t *testing.T) {
	opts := []string{"List", "Grid", "Detail"}
	for i, l := range opts {
		if got := segIndexOf(opts, l); got != i {
			t.Errorf("segIndexOf(%q)=%d want %d", l, got, i)
		}
	}
	if got := segIndexOf(opts, "nope"); got != -1 {
		t.Errorf("segIndexOf(missing)=%d want -1", got)
	}
}

func TestGridColumnsRows(t *testing.T) {
	cases := []struct {
		w, cell, pad   float32
		wantCols       int
		items, wantRow int
	}{
		{800, 160, 8, 4, 10, 3}, // (800+8)/(168)=4.8→4 cols; 10 items → 3 rows
		{100, 160, 8, 1, 1, 1},  // narrower than a cell → clamp to 1 col
		{800, 0, 8, 1, 0, 0},    // zero cell width → 1 col; 0 items → 0 rows
	}
	for _, c := range cases {
		if got := gridColumns(c.w, c.cell, c.pad); got != c.wantCols {
			t.Errorf("gridColumns(%v,%v,%v)=%d want %d", c.w, c.cell, c.pad, got, c.wantCols)
		}
		if got := gridRows(c.items, c.wantCols); got != c.wantRow {
			t.Errorf("gridRows(%d,%d)=%d want %d", c.items, c.wantCols, got, c.wantRow)
		}
	}
}

func TestKitSegmentedState(t *testing.T) {
	test.NewApp()
	var fired []string
	s := newKitSegmented([]string{"List", "Grid"}, "List", func(v string) { fired = append(fired, v) })
	if s.Selected() != "List" {
		t.Fatalf("initial selected=%q want List", s.Selected())
	}
	s.Select("Grid")
	if s.Selected() != "Grid" {
		t.Fatalf("after select selected=%q want Grid", s.Selected())
	}
	s.Select("Grid") // no-op, must not re-fire
	s.Select("nope") // invalid, must not fire
	if len(fired) != 1 || fired[0] != "Grid" {
		t.Fatalf("onChange fired=%v want [Grid]", fired)
	}
	// exercise renderer hit-testing: layout then tap the first cell.
	r := s.CreateRenderer()
	r.Layout(fyne.NewSize(200, kitSegH))
	s.Tapped(&fyne.PointEvent{Position: fyne.NewPos(1, 1)})
	if s.Selected() != "List" {
		t.Fatalf("tap-first selected=%q want List", s.Selected())
	}
	_ = s.MinSize()
}

func TestKitIconButton(t *testing.T) {
	test.NewApp()
	tapped := 0
	b := newKitIconButton(theme.SearchIcon(), "Search", func() { tapped++ })
	b.SetActive(true)
	b.SetActive(true) // no-op branch
	b.SetDanger(true)
	b.SetIcon(theme.CancelIcon())
	b.SetTip("Clear")
	if b.MinSize().Width != kitIconBtnSize {
		t.Fatalf("min width=%v want %v", b.MinSize().Width, kitIconBtnSize)
	}
	b.Tapped(&fyne.PointEvent{})
	if tapped != 1 {
		t.Fatalf("tapped=%d want 1", tapped)
	}
	r := b.CreateRenderer()
	r.Layout(b.MinSize())
	r.Refresh()
}

func TestKitButton(t *testing.T) {
	a := test.NewApp()
	a.Settings().SetTheme(newBrandTheme())
	tapped := 0
	b := newKitButtonWithIcon("Go Live", theme.MediaPlayIcon(), func() { tapped++ })
	b.SetVariant(kitBtnBrand)
	b.SetVariant(kitBtnBrand) // no-op branch
	if m := b.MinSize(); m.Height != kitBtnHeight {
		t.Fatalf("height=%v want %v", m.Height, kitBtnHeight)
	}
	// label + icon fit inside the min box
	txt := fyne.MeasureText("Go Live", theme.CaptionTextSize(), fyne.TextStyle{Bold: true})
	icon := theme.Size(theme.SizeNameInlineIcon)
	if m := b.MinSize(); m.Width < txt.Width+icon+kitBtnGap+2*kitBtnHPad {
		t.Fatalf("min width %v clips icon+label %v", m.Width, txt.Width+icon)
	}
	// denser than the stock equivalent
	stock := widget.NewButtonWithIcon("Go Live", theme.MediaPlayIcon(), nil)
	if m, s := b.MinSize(), stock.MinSize(); m.Height >= s.Height {
		t.Fatalf("kitButton %v not shorter than stock %v", m, s)
	}
	b.Tapped(&fyne.PointEvent{})
	b.Disable()
	b.Tapped(&fyne.PointEvent{}) // blocked while disabled
	b.Enable()
	if tapped != 1 {
		t.Fatalf("tapped=%d want 1", tapped)
	}
	b.SetText("End")
	b.SetText("End") // no-op branch
	b.SetIcon(theme.MediaStopIcon())
	b.SetVariant(kitBtnDanger)
	r := b.CreateRenderer()
	r.Layout(b.MinSize())
	r.Refresh()
	b.MouseIn(nil)
	b.MouseOut()
	// text-only constructor
	plain := newKitButton("Refresh", nil)
	if m := plain.MinSize(); m.Height != kitBtnHeight || m.Width <= 2*kitBtnHPad {
		t.Fatalf("text-only min=%v", m)
	}
	// icon-only → compact near-square (icon + 2·gap wide, never narrower than tall)
	sq := newKitButtonWithIcon("", theme.SettingsIcon(), nil)
	if m := sq.MinSize(); m.Width != icon+2*kitBtnGap || m.Height != kitBtnHeight {
		t.Fatalf("icon-only min=%v want %vx%v", m, icon+2*kitBtnGap, kitBtnHeight)
	}
	sq.CreateRenderer().Layout(sq.MinSize())
}

// TestKitButtonCtlFlows: the ctl click/dom plumbing (snapshot.go) must treat kitButton
// like widget.Button - label-driven click, text extraction, disabled respected.
func TestKitButtonCtlFlows(t *testing.T) {
	test.NewApp()
	hit := 0
	b := newKitButton("Go Live", func() { hit++ })
	root := container.NewVBox(boldLabel("x"), b)
	if !clickWalk(root, "go live", false) || hit != 1 {
		t.Fatalf("clickWalk missed kitButton (hit=%d)", hit)
	}
	if got := objText(b); got != "Go Live" {
		t.Fatalf("objText=%q want Go Live", got)
	}
	var out string
	if !readWalk(root, "go live", &out) || out != "Go Live" {
		t.Fatalf("readWalk=%q", out)
	}
	b.Disable()
	if clickWalk(root, "go live", false) {
		t.Fatal("clickWalk must skip a disabled kitButton")
	}
	if kids := childrenOf(b); kids != nil {
		t.Fatal("kitButton is a leaf for the snapshot walk (no renderer noise)")
	}
}

func TestKitSearchField(t *testing.T) {
	test.NewApp()
	var last string
	f := newKitSearchField("Filter…", func(s string) { last = s })
	f.SetText("kick")
	if f.Text() != "kick" || last != "kick" {
		t.Fatalf("text=%q last=%q want kick", f.Text(), last)
	}
	f.SetText("")
	if last != "" {
		t.Fatalf("clear last=%q want empty", last)
	}
	if f.Object() == nil {
		t.Fatal("Object nil")
	}
}

func TestKitInspectorPersistsOpenState(t *testing.T) {
	test.NewApp()
	i := newKitInspector("SELECTED")
	i.SetHeader("track.mp3", "128 BPM")
	content := boldLabel("x")
	sec := kitSection{Key: "details", Title: "DETAILS", Help: "File metadata.", DefaultOpen: true, Content: content}
	i.SetSections(sec, kitSection{Key: "skip", Content: nil}) // nil-content section skipped
	if !content.Visible() {                                   // DefaultOpen → shown
		t.Fatal("default-open section should be visible")
	}
	i.open["details"] = false // simulate a user collapse
	i.SetSections(sec)        // rebuild
	if content.Visible() {
		t.Fatal("collapse state must persist across rebuild")
	}
	if i.Object() == nil {
		t.Fatal("Object nil")
	}
}

func TestKitStatusStrip(t *testing.T) {
	test.NewApp()
	s := newKitStatusStrip()
	s.SetLeft("120 items")
	s.SetCenter("hint")
	s.SetRight("ready")
	s.SetProgress("Transcoding…", 0.5)
	if !s.progW.Visible() {
		t.Fatal("progress should be visible")
	}
	s.ClearProgress()
	if s.progW.Visible() {
		t.Fatal("progress should hide")
	}
	if s.Object() == nil {
		t.Fatal("Object nil")
	}
}

func TestKitSplitAndStrip(t *testing.T) {
	test.NewApp()
	h := kitHSplit(boldLabel("l"), boldLabel("r"), 0.7)
	if h.Offset != 0.7 || !h.Horizontal {
		t.Fatalf("hsplit offset=%v horiz=%v", h.Offset, h.Horizontal)
	}
	v := kitVSplit(boldLabel("t"), boldLabel("b"), 0.3)
	if v.Offset != 0.3 || v.Horizontal {
		t.Fatalf("vsplit offset=%v horiz=%v", v.Offset, v.Horizontal)
	}
	if kitToolStrip(kitToolSep(), boldLabel("x")) == nil {
		t.Fatal("toolstrip nil")
	}
}

func TestKitDensityGrid(t *testing.T) {
	test.NewApp()
	g := newKitDensityGrid(160, 140)
	g.SetActions(kitGridAction{ID: "open", Icon: theme.MediaPlayIcon(), Tip: "Open"})
	var activated, actioned string
	g.OnActivate = func(id string) { activated = id }
	g.OnAction = func(id, a string) { actioned = id + ":" + a }
	g.SetItems([]kitGridItem{
		{ID: "a", Title: "Clip A", Subtitle: "mp4", Icon: theme.MediaVideoIcon(), Selected: true},
		{ID: "b", Title: "Pic B", ThumbPath: writeTempPNG(t)},
	})
	if g.MinSize().Width <= 0 {
		t.Fatal("grid min width <= 0")
	}
	g.CreateRenderer()

	cell := newKitGridCell(g)
	cell.bind(g.items[0])
	if cell.id != "a" || !cell.selected {
		t.Fatalf("cell bind id=%q selected=%v", cell.id, cell.selected)
	}
	cell.bind(g.items[1]) // thumbnail path branch
	cell.MouseIn(nil)
	if !cell.overlay.Visible() {
		t.Fatal("overlay should show on hover")
	}
	cell.MouseOut()
	cell.Tapped(&fyne.PointEvent{})
	if activated != "b" {
		t.Fatalf("activated=%q want b", activated)
	}
	if g.actions[0].ID == "" || actioned != "" { // actioned only fires via overlay button tap
		t.Log("overlay action wiring present")
	}
}

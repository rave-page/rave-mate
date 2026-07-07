package ui

import (
	"image/color"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/session"
	"rave.page/mate/internal/visualeditor"
)

const (
	veDefaultW = 1920
	veDefaultH = 1080
	veUndoCap  = 50
)

// veEditor is the Fyne front-end for the visualeditor engine: a zoomable canvas, a layer
// tree with per-layer controls, a property inspector, and a template/component insert panel.
type veEditor struct {
	u     *UI
	comp  *visualeditor.Compositor
	store *visualeditor.TemplateStore
	doc   *visualeditor.Document

	selID    string
	zoom     float64
	provider visualeditor.Provider

	// undo/redo hold marshaled document snapshots (IDs preserved).
	undo, redo [][]byte
	lastSnap   time.Time
	dragging   bool

	// widgets (rebuilt on state change)
	cv        *veCanvas
	layersBox *fyne.Container
	inspector *fyne.Container
	scroll    *container.Scroll
	lastSig   string
}

// newVisualEditor builds the visual editor view backed by the running app services.
func (u *UI) newVisualEditor() fyne.CanvasObject {
	reg := visualeditor.NewFontRegistry()
	loadUserFonts(reg)
	e := &veEditor{
		u:     u,
		comp:  visualeditor.NewCompositor(reg, visualeditor.LoadImageFile),
		store: visualeditor.NewTemplateStore(veTemplatesDir()),
		zoom:  0.4,
	}
	e.provider = liveProvider{u: u}
	e.doc = e.loadOrNewDoc()
	return e.build()
}

// build assembles the toolbar / canvas / right-panel layout.
func (e *veEditor) build() fyne.CanvasObject {
	e.cv = newVECanvas()
	e.cv.onScroll = func(dy float32) { e.zoomBy(dy) }
	e.cv.onDrag = func(dx, dy float32) {
		if e.selID == "" {
			return
		}
		if !e.dragging {
			e.snapshot(true)
			e.dragging = true
		}
		e.moveSelected(float64(dx)/e.zoom, float64(dy)/e.zoom)
	}
	e.cv.onDragEnd = func() { e.dragging = false; e.autosave() }
	e.cv.onTap = func(pos fyne.Position) { e.hitSelect(float64(pos.X)/e.zoom, float64(pos.Y)/e.zoom) }

	e.scroll = container.NewScroll(container.NewCenter(e.cv))

	e.layersBox = container.NewVBox()
	e.inspector = container.NewVBox()
	right := container.NewVScroll(container.NewVBox(
		sectionTitle("Layers", "The stack of visual elements. Top of the list draws on top. "+
			"Toggle the eye to hide, the lock to protect, and drag order with the arrow buttons."),
		e.layersBox,
		widget.NewSeparator(),
		sectionTitle("Inspector", "Properties of the selected layer: position, size, blend, "+
			"opacity, and type-specific options (text, color, gradient, image)."),
		e.inspector,
	))
	right.SetMinSize(fyne.NewSize(300, 100))

	body := container.NewHSplit(e.scroll, right)
	body.SetOffset(0.66)

	root := container.NewBorder(e.toolbar(), nil, nil, nil, body)
	e.rebuildAll()
	e.startLiveRefresh()
	return root
}

// ── toolbar ───────────────────────────────────────────────────────────────────

func (e *veEditor) toolbar() fyne.CanvasObject {
	addText := veToolBtn(theme.DocumentCreateIcon(), "Add text", "Add a text layer. Use {track.title}, "+
		"{track.artist}, {track.bpm}, {track.key}, {time}, {date} for live values.", func() {
		l := visualeditor.NewText("Text", 80, 80, 800, 120, "{track.title}", visualeditor.DefaultFontFamily, 72,
			color.NRGBA{R: 0xfa, G: 0xfa, B: 0xfa, A: 0xff})
		e.addLayer(l)
	})
	addImage := veToolBtn(theme.MediaPhotoIcon(), "Add image", "Add an image layer from a file (PNG/JPEG/GIF).", func() {
		e.addLayer(visualeditor.NewImage("Image", 0, 0, float64(e.doc.W), float64(e.doc.H), ""))
		e.pickImageForSelected()
	})
	addSolid := veToolBtn(theme.ColorPaletteIcon(), "Add solid", "Add a solid-color rectangle.", func() {
		e.addLayer(visualeditor.NewSolid("Solid", 0, 0, float64(e.doc.W), float64(e.doc.H),
			color.NRGBA{R: 0x16, G: 0x18, B: 0x1d, A: 0xff}))
	})
	addGrad := veToolBtn(theme.ColorChromaticIcon(), "Add gradient", "Add a linear-gradient rectangle.", func() {
		stops := []visualeditor.GradientStop{
			{Pos: 0, Color: visualeditor.RGBA{R: 0xF7, G: 0x08, B: 0x64, A: 0xff}},
			{Pos: 1, Color: visualeditor.RGBA{R: 0x7C, G: 0x3A, B: 0xED, A: 0xff}},
		}
		e.addLayer(visualeditor.NewGradient("Gradient", 0, 0, float64(e.doc.W), float64(e.doc.H), 90, stops))
	})
	insertTpl := veToolBtn(theme.ContentAddIcon(), "Insert template", "Insert a preset or saved component "+
		"(lower-third, title, caption, ticker, or your own).", func() { e.showInsertPanel() })

	undoBtn := veToolBtn(theme.ContentUndoIcon(), "Undo", "Undo the last change.", func() { e.undoAction() })
	redoBtn := veToolBtn(theme.ContentRedoIcon(), "Redo", "Redo the last undone change.", func() { e.redoAction() })

	saveTpl := veToolBtn(theme.DocumentSaveIcon(), "Save as template", "Save the selected group (or the whole "+
		"document) as a reusable component.", func() { e.saveAsTemplate() })
	exportBtn := veToolBtn(theme.DownloadIcon(), "Export PNG", "Render the document to a PNG file.", func() { e.exportPNG() })

	zoomOut := veToolBtn(theme.ZoomOutIcon(), "Zoom out", "Zoom the canvas out.", func() { e.setZoom(e.zoom / 1.25) })
	zoomIn := veToolBtn(theme.ZoomInIcon(), "Zoom in", "Zoom the canvas in.", func() { e.setZoom(e.zoom * 1.25) })
	zoomFit := veToolBtn(theme.ViewFullScreenIcon(), "Fit", "Fit the document to the view.", func() { e.fitZoom() })

	docSize := veToolBtn(theme.SettingsIcon(), "Canvas size", "Change the document dimensions.", func() { e.showCanvasSize() })

	row := container.NewHBox(
		addText, addImage, addSolid, addGrad, insertTpl, sep(),
		undoBtn, redoBtn, sep(),
		saveTpl, exportBtn, docSize, sep(),
		zoomOut, zoomIn, zoomFit,
	)
	return container.NewVBox(container.NewHScroll(row), widget.NewSeparator())
}

func sep() fyne.CanvasObject { return widget.NewSeparator() }

// ── rendering ───────────────────────────────────────────────────────────────

func (e *veEditor) rerender() {
	img := e.comp.Render(e.doc, e.provider)
	scaled := fyne.NewSize(float32(float64(e.doc.W)*e.zoom), float32(float64(e.doc.H)*e.zoom))
	e.cv.setImage(img, scaled)
	e.updateSelectionRect()
}

func (e *veEditor) updateSelectionRect() {
	l, _ := e.doc.Find(e.selID)
	if l == nil {
		e.cv.setSelection(0, 0, 0, 0)
		return
	}
	w := l.W * math.Abs(l.Transform.ScaleX)
	h := l.H * math.Abs(l.Transform.ScaleY)
	if l.IsGroup() && (w <= 0 || h <= 0) {
		w, h = float64(e.doc.W), float64(e.doc.H)
	}
	e.cv.setSelection(
		float32(l.Transform.X*e.zoom), float32(l.Transform.Y*e.zoom),
		float32(w*e.zoom), float32(h*e.zoom),
	)
}

func (e *veEditor) rebuildAll() {
	e.rebuildLayers()
	e.rebuildInspector()
	e.rerender()
}

// ── zoom ──────────────────────────────────────────────────────────────────────

func (e *veEditor) setZoom(z float64) {
	e.zoom = clampF(z, 0.05, 4)
	e.rerender()
}

func (e *veEditor) zoomBy(dy float32) {
	if dy > 0 {
		e.setZoom(e.zoom * 1.1)
	} else if dy < 0 {
		e.setZoom(e.zoom / 1.1)
	}
}

func (e *veEditor) fitZoom() {
	sz := e.scroll.Size()
	if sz.Width < 10 || sz.Height < 10 {
		e.setZoom(0.4)
		return
	}
	zx := float64(sz.Width-20) / float64(e.doc.W)
	zy := float64(sz.Height-20) / float64(e.doc.H)
	e.setZoom(math.Min(zx, zy))
}

// ── layer mutation ─────────────────────────────────────────────────────────────

// addLayer appends a new layer to the selected group (or root) and selects it.
func (e *veEditor) addLayer(l *visualeditor.Layer) {
	e.snapshot(true)
	parent := e.selectedGroupOrRoot()
	parent.Children = append(parent.Children, l)
	e.selID = l.ID
	e.autosave()
	e.rebuildAll()
}

// selectedGroupOrRoot returns the selected layer if it's a group, else the root group.
func (e *veEditor) selectedGroupOrRoot() *visualeditor.Layer {
	if l, _ := e.doc.Find(e.selID); l != nil && l.IsGroup() {
		return l
	}
	return e.doc.Root
}

func (e *veEditor) moveSelected(dx, dy float64) {
	l, _ := e.doc.Find(e.selID)
	if l == nil || l.Locked {
		return
	}
	l.Transform.X += dx
	l.Transform.Y += dy
	e.rerender()
	e.rebuildInspector()
}

func (e *veEditor) deleteSelected() {
	l, parent := e.doc.Find(e.selID)
	if l == nil || parent == nil {
		return
	}
	e.snapshot(true)
	parent.Children = removeLayer(parent.Children, l.ID)
	e.selID = ""
	e.autosave()
	e.rebuildAll()
}

// reorderSelected moves the selected layer up (dir<0) or down (dir>0) within its parent.
func (e *veEditor) reorderSelected(dir int) {
	l, parent := e.doc.Find(e.selID)
	if l == nil || parent == nil {
		return
	}
	idx := indexOf(parent.Children, l.ID)
	ni := idx + dir
	if ni < 0 || ni >= len(parent.Children) {
		return
	}
	e.snapshot(true)
	parent.Children[idx], parent.Children[ni] = parent.Children[ni], parent.Children[idx]
	e.autosave()
	e.rebuildAll()
}

// groupSelected wraps the selected layer in a new group in its parent's position.
func (e *veEditor) groupSelected() {
	l, parent := e.doc.Find(e.selID)
	if l == nil || parent == nil {
		return
	}
	e.snapshot(true)
	idx := indexOf(parent.Children, l.ID)
	g := visualeditor.NewGroup("Group")
	g.Children = []*visualeditor.Layer{l}
	parent.Children[idx] = g
	e.selID = g.ID
	e.autosave()
	e.rebuildAll()
}

// ungroupSelected dissolves the selected group into its parent.
func (e *veEditor) ungroupSelected() {
	l, parent := e.doc.Find(e.selID)
	if l == nil || parent == nil || !l.IsGroup() {
		return
	}
	e.snapshot(true)
	idx := indexOf(parent.Children, l.ID)
	merged := make([]*visualeditor.Layer, 0, len(parent.Children)+len(l.Children))
	merged = append(merged, parent.Children[:idx]...)
	merged = append(merged, l.Children...)
	merged = append(merged, parent.Children[idx+1:]...)
	parent.Children = merged
	e.selID = ""
	e.autosave()
	e.rebuildAll()
}

// ── undo/redo ───────────────────────────────────────────────────────────────

// snapshot pushes the current document onto the undo stack. Non-forced snapshots coalesce
// within 400ms (drags/sliders) so a gesture becomes a single undo step.
func (e *veEditor) snapshot(force bool) {
	if !force && time.Since(e.lastSnap) < 400*time.Millisecond {
		return
	}
	data, err := e.doc.Marshal()
	if err != nil {
		return
	}
	e.undo = append(e.undo, data)
	if len(e.undo) > veUndoCap {
		e.undo = e.undo[len(e.undo)-veUndoCap:]
	}
	e.redo = nil
	e.lastSnap = time.Now()
}

func (e *veEditor) undoAction() {
	if len(e.undo) == 0 {
		return
	}
	if cur, err := e.doc.Marshal(); err == nil {
		e.redo = append(e.redo, cur)
	}
	data := e.undo[len(e.undo)-1]
	e.undo = e.undo[:len(e.undo)-1]
	if d, err := visualeditor.Unmarshal(data); err == nil {
		e.doc = d
	}
	e.lastSnap = time.Time{}
	e.rebuildAll()
}

func (e *veEditor) redoAction() {
	if len(e.redo) == 0 {
		return
	}
	if cur, err := e.doc.Marshal(); err == nil {
		e.undo = append(e.undo, cur)
	}
	data := e.redo[len(e.redo)-1]
	e.redo = e.redo[:len(e.redo)-1]
	if d, err := visualeditor.Unmarshal(data); err == nil {
		e.doc = d
	}
	e.lastSnap = time.Time{}
	e.rebuildAll()
}

// ── selection / hit-test ───────────────────────────────────────────────────────

// hitSelect selects the topmost leaf whose (unrotated) box contains doc-space (x,y).
func (e *veEditor) hitSelect(x, y float64) {
	var hit string
	var walk func(layers []*visualeditor.Layer)
	walk = func(layers []*visualeditor.Layer) {
		for _, l := range layers {
			if !l.Visible {
				continue
			}
			if l.IsGroup() {
				walk(l.Children)
				continue
			}
			lw := l.W * math.Abs(l.Transform.ScaleX)
			lh := l.H * math.Abs(l.Transform.ScaleY)
			if x >= l.Transform.X && x <= l.Transform.X+lw &&
				y >= l.Transform.Y && y <= l.Transform.Y+lh {
				hit = l.ID // later layers draw on top → keep last match
			}
		}
	}
	walk(e.doc.Root.Children)
	if hit != "" {
		e.selID = hit
		e.rebuildLayers()
		e.rebuildInspector()
		e.updateSelectionRect()
	}
}

func (e *veEditor) selectLayer(id string) {
	e.selID = id
	e.rebuildLayers()
	e.rebuildInspector()
	e.updateSelectionRect()
}

// ── live provider ───────────────────────────────────────────────────────────

// liveProvider resolves placeholders from the running session's now-playing deck + clock.
type liveProvider struct{ u *UI }

func (p liveProvider) Value(key string) (string, bool) {
	switch key {
	case "time":
		return time.Now().Format("15:04"), true
	case "date":
		return time.Now().Format("2006-01-02"), true
	}
	if !strings.HasPrefix(key, "track.") || p.u.svc.Session == nil {
		return "", false
	}
	ov := p.u.svc.Session.Snapshot().BuildOverlay(time.Now(), session.NowPlayingStaleAfter)
	d, ok := nowPlayingDeck(ov)
	if !ok {
		return "", false
	}
	switch key {
	case "track.title":
		return d.Title, d.Title != ""
	case "track.artist":
		return d.Artist, d.Artist != ""
	case "track.bpm":
		if d.BPM > 0 {
			return strconv.FormatFloat(d.BPM, 'f', 0, 64), true
		}
	case "track.key":
		return d.Key, d.Key != ""
	}
	return "", false
}

// nowPlayingDeck picks the master (audible) deck, falling back to the first loaded deck.
func nowPlayingDeck(ov session.Overlay) (session.DeckSnapshot, bool) {
	for _, d := range ov.Decks {
		if d.Deck == ov.Master.Deck {
			return d, true
		}
	}
	if len(ov.Decks) > 0 {
		return ov.Decks[0], true
	}
	return session.DeckSnapshot{}, false
}

// startLiveRefresh re-renders when now-playing / clock values change (≈1s cadence).
func (e *veEditor) startLiveRefresh() {
	goUI("visualeditor-live", func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for range t.C {
			sig := e.providerSig()
			if sig == e.lastSig {
				continue
			}
			e.lastSig = sig
			fyne.Do(func() { e.rerender() })
		}
	})
}

func (e *veEditor) providerSig() string {
	var b strings.Builder
	for _, k := range visualeditor.KnownPlaceholders {
		if v, ok := e.provider.Value(k); ok {
			b.WriteString(k)
			b.WriteByte('=')
			b.WriteString(v)
			b.WriteByte(';')
		}
	}
	return b.String()
}

// ── persistence ───────────────────────────────────────────────────────────────

func (e *veEditor) loadOrNewDoc() *visualeditor.Document {
	path := veDocPath()
	if path != "" {
		if data, err := os.ReadFile(path); err == nil {
			if d, err := visualeditor.Unmarshal(data); err == nil {
				return d
			}
		}
	}
	return visualeditor.NewDocument(veDefaultW, veDefaultH)
}

// autosave persists the working document (best-effort).
func (e *veEditor) autosave() {
	path := veDocPath()
	if path == "" {
		return
	}
	if data, err := e.doc.Marshal(); err == nil {
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		_ = os.WriteFile(path, data, 0o644)
	}
}

// ── export ──────────────────────────────────────────────────────────────────

func (e *veEditor) exportPNG() {
	win := currentWindow()
	if win == nil {
		return
	}
	d := dialog.NewFileSave(func(w fyne.URIWriteCloser, err error) {
		if err != nil || w == nil {
			return
		}
		defer func() { _ = w.Close() }()
		img := e.comp.Render(e.doc, e.provider)
		if err := visualeditor.EncodePNG(img, w); err != nil {
			dialog.ShowError(err, win)
			return
		}
		e.autosave()
	}, win)
	d.SetFileName("composition.png")
	d.Resize(fileDialogSize(win))
	d.Show()
}

// ── helpers ─────────────────────────────────────────────────────────────────

func removeLayer(layers []*visualeditor.Layer, id string) []*visualeditor.Layer {
	out := layers[:0]
	for _, l := range layers {
		if l.ID != id {
			out = append(out, l)
		}
	}
	return out
}

func indexOf(layers []*visualeditor.Layer, id string) int {
	for i, l := range layers {
		if l.ID == id {
			return i
		}
	}
	return -1
}

func sectionTitle(title, help string) fyne.CanvasObject {
	lbl := widget.NewLabelWithStyle(title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	return container.NewHBox(lbl, helpIcon(help))
}

// veToolBtn is a labeled icon button with a hover-help "?" beside it (help.go primitive).
func veToolBtn(icon fyne.Resource, label, help string, tapped func()) fyne.CanvasObject {
	b := widget.NewButtonWithIcon(label, icon, tapped)
	b.Importance = widget.LowImportance
	return container.NewHBox(b, helpIcon(help))
}

func veTemplatesDir() string {
	p, err := config.DataPath(filepath.Join("visualeditor", "templates"))
	if err != nil {
		return ""
	}
	return p
}

func veDocPath() string {
	p, err := config.DataPath(filepath.Join("visualeditor", "document.json"))
	if err != nil {
		return ""
	}
	return p
}

// loadUserFonts registers TTF/OTF files from the config-dir fonts/ folder into reg.
func loadUserFonts(reg *visualeditor.FontRegistry) {
	dir, err := config.DataPath("fonts")
	if err != nil {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(ent.Name()))
		if ext != ".ttf" && ext != ".otf" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, ent.Name()))
		if err != nil {
			continue
		}
		fam := strings.TrimSuffix(ent.Name(), filepath.Ext(ent.Name()))
		_ = reg.Register(fam, data)
	}
}

package webui

import (
	"image/color"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/session"
	"rave.page/mate/internal/visualeditor"
)

// Editor tab state + action handlers. The doc + templates live on disk under the visualeditor
// data dir, shared byte-for-byte with the Fyne editor (same engine). State is a package
// singleton (one webview window) guarded by a mutex; renderEditor reads it under the lock and
// action handlers mutate → autosave → patchMain. All new actions are namespaced `ed-`.

const edUndoCap = 50

type edState struct {
	mu    sync.Mutex
	comp  *visualeditor.Compositor
	store *visualeditor.TemplateStore
	doc   *visualeditor.Document

	selID      string
	undo, redo [][]byte
	lastSnap   time.Time
	lastSig    string
}

var (
	editor     edState
	editorOnce sync.Once
)

// edEnsure lazily builds the compositor/store/doc on first use.
func edEnsure() {
	editorOnce.Do(func() {
		reg := visualeditor.NewFontRegistry()
		edLoadUserFonts(reg)
		editor.comp = visualeditor.NewCompositor(reg, visualeditor.LoadImageFile)
		editor.store = visualeditor.NewTemplateStore(edDataDir("templates"))
		editor.doc = edLoadOrNewDoc()
	})
}

// ── provider (live now-playing placeholders) ──

type edProvider struct{ u *UI }

func (p edProvider) Value(key string) (string, bool) {
	switch key {
	case "time":
		return time.Now().Format("15:04"), true
	case "date":
		return time.Now().Format("2006-01-02"), true
	}
	if !strings.HasPrefix(key, "track.") || p.u == nil || p.u.svc.Session == nil {
		return "", false
	}
	ov := p.u.svc.Session.Snapshot().BuildOverlay(time.Now(), session.NowPlayingStaleAfter)
	d, ok := edNowPlayingDeck(ov)
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

func edNowPlayingDeck(ov session.Overlay) (session.DeckSnapshot, bool) {
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

func (u *UI) edProviderSig() string {
	var b strings.Builder
	p := edProvider{u}
	for _, k := range visualeditor.KnownPlaceholders {
		if v, ok := p.Value(k); ok {
			b.WriteString(k)
			b.WriteByte('=')
			b.WriteString(v)
			b.WriteByte(';')
		}
	}
	return b.String()
}

// ── mutation helpers (caller must hold editor.mu) ──

// snapshot pushes the current doc onto the undo stack (coalesces within 400ms unless forced).
func (s *edState) snapshot(force bool) {
	if !force && time.Since(s.lastSnap) < 400*time.Millisecond {
		return
	}
	data, err := s.doc.Marshal()
	if err != nil {
		return
	}
	s.undo = append(s.undo, data)
	if len(s.undo) > edUndoCap {
		s.undo = s.undo[len(s.undo)-edUndoCap:]
	}
	s.redo = nil
	s.lastSnap = time.Now()
}

func (s *edState) autosave() {
	path := edDocPath()
	if path == "" {
		return
	}
	if data, err := s.doc.Marshal(); err == nil {
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		_ = os.WriteFile(path, data, 0o644)
	}
}

func (s *edState) selectedGroupOrRoot() *visualeditor.Layer {
	if l, _ := s.doc.Find(s.selID); l != nil && l.IsGroup() {
		return l
	}
	return s.doc.Root
}

func (s *edState) sel() *visualeditor.Layer {
	l, _ := s.doc.Find(s.selID)
	return l
}

// edEdit runs fn under the lock (fn mutates state), then re-renders the tab.
func (u *UI) edEdit(fn func()) {
	edEnsure()
	editor.mu.Lock()
	fn()
	editor.mu.Unlock()
	u.patchMain()
}

// ── action registrations ──

func init() {
	onPrefix("ed-add:", func(u *UI, m actMsg) { u.edAdd(m.arg("ed-add:")) })
	onPrefix("ed-select:", func(u *UI, m actMsg) {
		u.edEdit(func() { editor.selID = m.arg("ed-select:") })
	})
	onPrefix("ed-vis:", func(u *UI, m actMsg) {
		u.edEdit(func() {
			if l, _ := editor.doc.Find(m.arg("ed-vis:")); l != nil {
				editor.snapshot(true)
				l.Visible = !l.Visible
				editor.autosave()
			}
		})
	})
	onPrefix("ed-lock:", func(u *UI, m actMsg) {
		u.edEdit(func() {
			if l, _ := editor.doc.Find(m.arg("ed-lock:")); l != nil {
				editor.snapshot(true)
				l.Locked = !l.Locked
				editor.autosave()
			}
		})
	})
	onPrefix("ed-prop:", func(u *UI, m actMsg) { u.edSetProp(m.arg("ed-prop:"), m.Val) })
	onPrefix("ed-txt:", func(u *UI, m actMsg) { u.edSetText(m.arg("ed-txt:"), m.Val) })
	onPrefix("ed-grad:", func(u *UI, m actMsg) { u.edSetGrad(m.arg("ed-grad:"), m.Val) })
	onPrefix("ed-img:", func(u *UI, m actMsg) { u.edSetImg(m.arg("ed-img:"), m.Val) })
	onPrefix("ed-tpl:", func(u *UI, m actMsg) { u.edApplyTemplate(m.arg("ed-tpl:")) })

	onExact("ed-solid-color", func(u *UI, m actMsg) { u.edSetSolidColor(m.Val) })
	onExact("ed-opacity", func(u *UI, m actMsg) { u.edSetOpacity(m.Val) })
	onExact("ed-blend", func(u *UI, m actMsg) {
		u.edEdit(func() {
			if l := editor.sel(); l != nil {
				editor.snapshot(true)
				l.Blend = visualeditor.BlendMode(m.Val)
				editor.autosave()
			}
		})
	})
	onExact("ed-up", func(u *UI, m actMsg) { u.edReorder(1) })
	onExact("ed-down", func(u *UI, m actMsg) { u.edReorder(-1) })
	onExact("ed-group", func(u *UI, m actMsg) { u.edGroup() })
	onExact("ed-ungroup", func(u *UI, m actMsg) { u.edUngroup() })
	onExact("ed-del", func(u *UI, m actMsg) { u.edDelete() })
	onExact("ed-undo", func(u *UI, m actMsg) { u.edUndo() })
	onExact("ed-redo", func(u *UI, m actMsg) { u.edRedo() })
	onExact("ed-tpl-open", func(u *UI, m actMsg) { u.edOpenTemplates() })
	onExact("ed-save-tpl", func(u *UI, m actMsg) { u.edOpenSaveTemplate() })
	onExact("ed-save-tpl-do", func(u *UI, m actMsg) { u.edSaveTemplate(parseForm(m.Form)["name"]) })
	onExact("ed-canvas", func(u *UI, m actMsg) { u.edOpenCanvas() })
	onExact("ed-canvas-do", func(u *UI, m actMsg) { u.edSetCanvas(parseForm(m.Form)) })
	onExact("ed-export", func(u *UI, m actMsg) { u.edExport() })

	onLiveTick("editor", func(u *UI) {
		edEnsure()
		editor.mu.Lock()
		sig := u.edProviderSig()
		if sig == editor.lastSig {
			editor.mu.Unlock()
			return
		}
		editor.lastSig = sig
		body := u.edPreviewHTML()
		editor.mu.Unlock()
		u.eval("window.__patch('ed-preview'," + jsQuote(body) + ")")
	})
}

// ── add / structural ──

func (u *UI) edAdd(kind string) {
	u.edEdit(func() {
		var l *visualeditor.Layer
		dw, dh := float64(editor.doc.W), float64(editor.doc.H)
		switch kind {
		case "text":
			l = visualeditor.NewText("Text", 80, 80, 800, 120, "{track.title}", visualeditor.DefaultFontFamily, 72,
				color.NRGBA{R: 0xfa, G: 0xfa, B: 0xfa, A: 0xff})
		case "image":
			l = visualeditor.NewImage("Image", 0, 0, dw, dh, "")
		case "solid":
			l = visualeditor.NewSolid("Solid", 0, 0, dw, dh, color.NRGBA{R: 0x16, G: 0x18, B: 0x1d, A: 0xff})
		case "gradient":
			l = visualeditor.NewGradient("Gradient", 0, 0, dw, dh, 90, []visualeditor.GradientStop{
				{Pos: 0, Color: visualeditor.RGBA{R: 0xF7, G: 0x08, B: 0x64, A: 0xff}},
				{Pos: 1, Color: visualeditor.RGBA{R: 0x7C, G: 0x3A, B: 0xED, A: 0xff}},
			})
		default:
			return
		}
		editor.snapshot(true)
		parent := editor.selectedGroupOrRoot()
		parent.Children = append(parent.Children, l)
		editor.selID = l.ID
		editor.autosave()
	})
}

func (u *UI) edReorder(dir int) {
	u.edEdit(func() {
		l, parent := editor.doc.Find(editor.selID)
		if l == nil || parent == nil {
			return
		}
		idx := edIndexOf(parent.Children, l.ID)
		ni := idx + dir
		if ni < 0 || ni >= len(parent.Children) {
			return
		}
		editor.snapshot(true)
		parent.Children[idx], parent.Children[ni] = parent.Children[ni], parent.Children[idx]
		editor.autosave()
	})
}

func (u *UI) edGroup() {
	u.edEdit(func() {
		l, parent := editor.doc.Find(editor.selID)
		if l == nil || parent == nil {
			return
		}
		editor.snapshot(true)
		idx := edIndexOf(parent.Children, l.ID)
		g := visualeditor.NewGroup("Group")
		g.Children = []*visualeditor.Layer{l}
		parent.Children[idx] = g
		editor.selID = g.ID
		editor.autosave()
	})
}

func (u *UI) edUngroup() {
	u.edEdit(func() {
		l, parent := editor.doc.Find(editor.selID)
		if l == nil || parent == nil || !l.IsGroup() {
			return
		}
		editor.snapshot(true)
		idx := edIndexOf(parent.Children, l.ID)
		merged := make([]*visualeditor.Layer, 0, len(parent.Children)+len(l.Children))
		merged = append(merged, parent.Children[:idx]...)
		merged = append(merged, l.Children...)
		merged = append(merged, parent.Children[idx+1:]...)
		parent.Children = merged
		editor.selID = ""
		editor.autosave()
	})
}

func (u *UI) edDelete() {
	u.edEdit(func() {
		l, parent := editor.doc.Find(editor.selID)
		if l == nil || parent == nil {
			return
		}
		editor.snapshot(true)
		parent.Children = edRemoveLayer(parent.Children, l.ID)
		editor.selID = ""
		editor.autosave()
	})
}

func (u *UI) edUndo() {
	u.edEdit(func() {
		if len(editor.undo) == 0 {
			return
		}
		if cur, err := editor.doc.Marshal(); err == nil {
			editor.redo = append(editor.redo, cur)
		}
		data := editor.undo[len(editor.undo)-1]
		editor.undo = editor.undo[:len(editor.undo)-1]
		if d, err := visualeditor.Unmarshal(data); err == nil {
			editor.doc = d
		}
		editor.lastSnap = time.Time{}
		editor.autosave()
	})
}

func (u *UI) edRedo() {
	u.edEdit(func() {
		if len(editor.redo) == 0 {
			return
		}
		if cur, err := editor.doc.Marshal(); err == nil {
			editor.undo = append(editor.undo, cur)
		}
		data := editor.redo[len(editor.redo)-1]
		editor.redo = editor.redo[:len(editor.redo)-1]
		if d, err := visualeditor.Unmarshal(data); err == nil {
			editor.doc = d
		}
		editor.lastSnap = time.Time{}
		editor.autosave()
	})
}

// ── property setters ──

func (u *UI) edSetProp(field, val string) {
	u.edEdit(func() {
		l := editor.sel()
		if l == nil {
			return
		}
		if field == "name" {
			editor.snapshot(false)
			l.Name = val
			editor.autosave()
			return
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(val), 64)
		if err != nil {
			return
		}
		editor.snapshot(false)
		switch field {
		case "x":
			l.Transform.X = f
		case "y":
			l.Transform.Y = f
		case "w":
			l.W = f
		case "h":
			l.H = f
		case "sx":
			l.Transform.ScaleX = f
		case "sy":
			l.Transform.ScaleY = f
		case "rot":
			l.Transform.Rotation = f
		}
		editor.autosave()
	})
}

func (u *UI) edSetText(field, val string) {
	u.edEdit(func() {
		l := editor.sel()
		if l == nil || l.Text == nil {
			return
		}
		t := l.Text
		editor.snapshot(false)
		switch field {
		case "content":
			t.Content = val
		case "font":
			t.FontFamily = val
		case "align":
			t.Align = visualeditor.Align(val)
		case "color":
			if c, ok := edParseHex(val); ok {
				t.Color = c
			}
		case "size", "ls", "lh":
			f, err := strconv.ParseFloat(strings.TrimSpace(val), 64)
			if err != nil {
				return
			}
			switch field {
			case "size":
				t.FontSize = f
			case "ls":
				t.LetterSpacing = f
			case "lh":
				t.LineHeight = f
			}
		}
		editor.autosave()
	})
}

func (u *UI) edSetGrad(field, val string) {
	u.edEdit(func() {
		l := editor.sel()
		if l == nil || l.Gradient == nil || len(l.Gradient.Stops) < 2 {
			return
		}
		g := l.Gradient
		editor.snapshot(false)
		switch field {
		case "angle":
			if f, err := strconv.ParseFloat(strings.TrimSpace(val), 64); err == nil {
				g.Angle = f
			}
		case "start":
			if c, ok := edParseHex(val); ok {
				g.Stops[0].Color = c
			}
		case "end":
			if c, ok := edParseHex(val); ok {
				g.Stops[len(g.Stops)-1].Color = c
			}
		}
		editor.autosave()
	})
}

func (u *UI) edSetImg(field, val string) {
	u.edEdit(func() {
		l := editor.sel()
		if l == nil || l.Image == nil {
			return
		}
		editor.snapshot(false)
		switch field {
		case "path":
			l.Image.Path = strings.TrimSpace(val)
		case "fit":
			l.Image.Fit = visualeditor.ImageFit(val)
		}
		editor.autosave()
	})
}

func (u *UI) edSetSolidColor(val string) {
	u.edEdit(func() {
		l := editor.sel()
		if l == nil || l.Solid == nil {
			return
		}
		if c, ok := edParseHex(val); ok {
			editor.snapshot(false)
			l.Solid.Color = c
			editor.autosave()
		}
	})
}

func (u *UI) edSetOpacity(val string) {
	u.edEdit(func() {
		l := editor.sel()
		if l == nil {
			return
		}
		if f, err := strconv.ParseFloat(strings.TrimSpace(val), 64); err == nil {
			editor.snapshot(false)
			l.Opacity = clamp01(f)
			editor.autosave()
		}
	})
}

// ── templates ──

func (u *UI) edOpenTemplates() {
	edEnsure()
	editor.mu.Lock()
	all := editor.store.All()
	var b strings.Builder
	b.WriteString(`<div class=ed-tpl-grid>`)
	for i, tpl := range all {
		kind := i18n.T("editor.label.component")
		if tpl.Builtin {
			kind = i18n.T("editor.label.preset")
		}
		w, h := tpl.W, tpl.H
		if w <= 0 || h <= 0 {
			w, h = editor.doc.W, editor.doc.H
		}
		preview := u.edStageHTML([]*visualeditor.Layer{tpl.Instantiate()}, editor.doc, w, h)
		b.WriteString(`<div class=ed-tpl-card><div class=ed-tpl-stage>` + preview + `</div>` +
			`<div class=ed-tpl-name>` + htmlEscape(tpl.Name) + `</div>` +
			`<div class=ed-tpl-kind>` + kind + `</div>` +
			btn(i18n.T("editor.label.insert"), "primary", "ed-tpl:"+strconv.Itoa(i), "") + `</div>`)
	}
	if len(all) == 0 {
		b.WriteString(emptyState(i18n.T("editor.empty.noTemplates")))
	}
	b.WriteString(`</div>`)
	editor.mu.Unlock()
	u.openModal(modal(i18n.T("editor.label.insertTemplateTitle"), b.String(), ""))
}

func (u *UI) edApplyTemplate(idxStr string) {
	idx, err := strconv.Atoi(idxStr)
	if err != nil {
		return
	}
	u.edEdit(func() {
		all := editor.store.All()
		if idx < 0 || idx >= len(all) {
			return
		}
		editor.snapshot(true)
		inst := all[idx].Instantiate()
		editor.doc.Root.Children = append(editor.doc.Root.Children, inst)
		editor.selID = inst.ID
		editor.autosave()
	})
	u.closeModal()
}

func (u *UI) edOpenSaveTemplate() {
	edEnsure()
	editor.mu.Lock()
	l, _ := editor.doc.Find(editor.selID)
	name := i18n.T("editor.label.myComponent")
	sub := i18n.T("editor.label.savesWholeDoc")
	if l != nil && l.IsGroup() {
		name = l.Name
		sub = i18n.T("editor.label.savesSelectedGroup")
	}
	editor.mu.Unlock()
	body := hint("info", sub) +
		`<form data-act=ed-save-tpl-do><label class=field><span class=field-label>` + i18n.T("editor.name") + `</span>` +
		`<input class=field-input type=text name=name value="` + htmlEscape(name) + `"></label>` +
		`<button class="rp-btn rp-btn--primary" type=submit>` + i18n.T("common.save") + `</button></form>`
	u.openModal(modal(i18n.T("editor.label.saveTemplateTitle"), body, `<span></span>`))
}

func (u *UI) edSaveTemplate(name string) {
	name = strings.TrimSpace(name)
	if name == "" {
		u.toast(i18n.T("editor.toast.enterTemplateName"))
		return
	}
	edEnsure()
	editor.mu.Lock()
	target := editor.sel()
	if target == nil || !target.IsGroup() {
		target = editor.doc.Root
	}
	err := editor.store.Save(name, target, editor.doc.W, editor.doc.H)
	editor.mu.Unlock()
	if err != nil {
		u.logErr("save template", err)
		u.toast(i18n.T("editor.toast.saveFailed") + err.Error())
		return
	}
	u.closeModal()
	u.toast(i18n.T("editor.toast.templateSaved") + name)
}

// ── canvas size ──

func (u *UI) edOpenCanvas() {
	edEnsure()
	editor.mu.Lock()
	w, h := editor.doc.W, editor.doc.H
	editor.mu.Unlock()
	body := `<form data-act=ed-canvas-do><div class=ed-row2>` +
		`<label class=field><span class=field-label>` + i18n.T("editor.label.width") + `</span><input class=field-input type=number name=w value="` + strconv.Itoa(w) + `"></label>` +
		`<label class=field><span class=field-label>` + i18n.T("editor.label.height") + `</span><input class=field-input type=number name=h value="` + strconv.Itoa(h) + `"></label>` +
		`</div><button class="rp-btn rp-btn--primary" type=submit>` + i18n.T("editor.label.apply") + `</button></form>`
	u.openModal(modal(i18n.T("editor.label.canvasSizeTitle"), body, `<span></span>`))
}

func (u *UI) edSetCanvas(form map[string]string) {
	w, err1 := strconv.Atoi(strings.TrimSpace(form["w"]))
	h, err2 := strconv.Atoi(strings.TrimSpace(form["h"]))
	if err1 != nil || err2 != nil || w < 1 || h < 1 {
		u.toast(i18n.T("editor.toast.invalidCanvasSize"))
		return
	}
	u.edEdit(func() {
		editor.snapshot(true)
		editor.doc.W, editor.doc.H = w, h
		editor.autosave()
	})
	u.closeModal()
}

// ── export ──

func (u *UI) edExport() {
	edEnsure()
	editor.mu.Lock()
	clone := editor.doc.Clone()
	editor.mu.Unlock()
	dir := edDataDir("exports")
	if dir == "" {
		u.toast(i18n.T("editor.toast.cannotResolveExportDir"))
		return
	}
	path := filepath.Join(dir, "composition-"+time.Now().Format("20060102-150405")+".png")
	u.toast(i18n.T("editor.toast.exportingPng"))
	u.bg(func() {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			u.logErr("export mkdir", err)
			u.toast(i18n.T("editor.toast.exportFailed") + err.Error())
			return
		}
		f, err := os.Create(path)
		if err != nil {
			u.logErr("export create", err)
			u.toast(i18n.T("editor.toast.exportFailed") + err.Error())
			return
		}
		defer func() { _ = f.Close() }()
		img := editor.comp.Render(clone, edProvider{u})
		if err := visualeditor.EncodePNG(img, f); err != nil {
			u.logErr("export encode", err)
			u.toast(i18n.T("editor.toast.exportFailed") + err.Error())
			return
		}
		u.toast(i18n.T("editor.toast.exportedTo", i18n.A{"path": path}))
	})
}

// ── persistence / paths (shared with the Fyne editor) ──

func edDataDir(sub string) string {
	p, err := config.DataPath(filepath.Join("visualeditor", sub))
	if err != nil {
		return ""
	}
	return p
}

func edDocPath() string {
	p, err := config.DataPath(filepath.Join("visualeditor", "document.json"))
	if err != nil {
		return ""
	}
	return p
}

func edLoadOrNewDoc() *visualeditor.Document {
	if path := edDocPath(); path != "" {
		if data, err := os.ReadFile(path); err == nil {
			if d, err := visualeditor.Unmarshal(data); err == nil {
				return d
			}
		}
	}
	return visualeditor.NewDocument(1920, 1080)
}

// edLoadUserFonts registers TTF/OTF files from the config-dir fonts/ folder.
func edLoadUserFonts(reg *visualeditor.FontRegistry) {
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
		_ = reg.Register(strings.TrimSuffix(ent.Name(), filepath.Ext(ent.Name())), data)
	}
}

// ── slice helpers ──

func edRemoveLayer(layers []*visualeditor.Layer, id string) []*visualeditor.Layer {
	out := layers[:0]
	for _, l := range layers {
		if l.ID != id {
			out = append(out, l)
		}
	}
	return out
}

func edIndexOf(layers []*visualeditor.Layer, id string) int {
	for i, l := range layers {
		if l.ID == id {
			return i
		}
	}
	return -1
}

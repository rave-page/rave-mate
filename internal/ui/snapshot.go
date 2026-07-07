package ui

import (
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	fynetest "fyne.io/fyne/v2/test" // renderer access for walking opaque widgets (widget.List rows)
	"fyne.io/fyne/v2/widget"
)

// Screenshot captures the current window canvas to a PNG at path (control-plane SCREENSHOT) -
// the visual half of the headless driver, so the rendered UI can actually be eyeballed.
func (u *UI) Screenshot(path string) error {
	var img image.Image
	fyne.DoAndWait(func() { img = u.win.Canvas().Capture() })
	if img == nil {
		return fmt.Errorf("canvas capture returned nil")
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return png.Encode(f, img)
}

// Snapshot renders a Playwright-style text tree of the current window's widget hierarchy -
// type, text, visibility, position + size, and a ⚠OVERFLOW flag where a widget's MinSize
// exceeds its allocated Size (i.e. text/content is clipped or pushed out of bounds). It runs
// on the UI thread. Exposed over the control socket (`rave-mate ctl snapshot`) so the native
// UI can be inspected/automated headlessly - a desktop analogue to a Playwright DOM snapshot.
func (u *UI) Snapshot() string {
	var b strings.Builder
	fyne.DoAndWait(func() {
		// Walk every window - the master plus any secondary window (the detached now-playing
		// player, future pop-outs), so ctl can inspect/verify them too.
		for _, w := range fyne.CurrentApp().Driver().AllWindows() {
			sz := w.Canvas().Size()
			tab := ""
			if w == u.win && u.tabs != nil {
				if cur := u.tabs.Selected(); cur != nil {
					tab = cur.Text
				}
			}
			fmt.Fprintf(&b, "window %q  canvas=%.0fx%.0f  tab=%q\n", w.Title(), sz.Width, sz.Height, tab)
			walkSnapshot(&b, w.Content(), 0)
			for i, ov := range w.Canvas().Overlays().List() {
				fmt.Fprintf(&b, "overlay %d (dialog/popover)\n", i)
				walkSnapshot(&b, ov, 1)
			}
		}
	})
	return b.String()
}

// uiRoots returns the control-plane walk roots: open overlays (dialogs, popovers)
// topmost-first, then the window content - a modal's widgets win over the page behind
// it, matching what a user can actually interact with.
func (u *UI) uiRoots() []fyne.CanvasObject {
	ovs := u.win.Canvas().Overlays().List()
	roots := make([]fyne.CanvasObject, 0, len(ovs)+1)
	for i := len(ovs) - 1; i >= 0; i-- {
		roots = append(roots, ovs[i])
	}
	return append(roots, u.win.Content())
}

// walkSnapshot writes one line per object then recurses into its children.
func walkSnapshot(b *strings.Builder, obj fyne.CanvasObject, depth int) {
	if obj == nil {
		return
	}
	indent := strings.Repeat("  ", depth)

	// AppTabs: list every tab label, but only recurse the SELECTED tab's content - Fyne doesn't
	// mark hidden tabs' children invisible, so walking them all yields false overflow flags.
	if tabs, ok := obj.(*container.AppTabs); ok {
		fmt.Fprintf(b, "%sAppTabs%s\n", indent, geom(obj))
		sel := tabs.Selected()
		for _, it := range tabs.Items {
			if it == sel {
				fmt.Fprintf(b, "%s  Tab %q *selected\n", indent, it.Text)
				walkSnapshot(b, it.Content, depth+2)
			} else {
				fmt.Fprintf(b, "%s  Tab %q\n", indent, it.Text)
			}
		}
		return
	}

	line := indent + objType(obj)
	if t := objText(obj); t != "" {
		line += " " + strconv.Quote(trunc(t, 90))
	}
	if !obj.Visible() {
		line += " [hidden]"
	}
	line += geom(obj)
	b.WriteString(line + "\n")

	for _, ch := range childrenOf(obj) {
		walkSnapshot(b, ch, depth+1)
	}
}

// geom renders position + size, flagging overflow when MinSize exceeds the allocated Size.
// Overflow is only meaningful for a laid-out, visible object - a hidden/zero-size object
// (e.g. an unselected tab's content) hasn't been sized yet, so don't flag it.
func geom(obj fyne.CanvasObject) string {
	p, s, m := obj.Position(), obj.Size(), obj.MinSize()
	out := fmt.Sprintf("  @%.0f,%.0f %.0fx%.0f min=%.0fx%.0f", p.X, p.Y, s.Width, s.Height, m.Width, m.Height)
	if obj.Visible() && s.Width > 1 && s.Height > 1 && (m.Width > s.Width+0.5 || m.Height > s.Height+0.5) {
		out += "  ⚠OVERFLOW"
	}
	return out
}

// Resize sets the window size from any goroutine (control-plane RESIZE) - the viewport
// control for responsive testing alongside Snapshot.
func (u *UI) Resize(w, h float32) {
	fyne.Do(func() { u.win.Resize(fyne.NewSize(w, h)) })
}

// Click taps the first visible Button/Check/Hyperlink/Tab whose label contains query
// (case-insensitive) - the interaction half of the headless UI driver (ctl click). Returns
// whether something matched.
func (u *UI) Click(query string) bool {
	q := strings.ToLower(strings.TrimSpace(query))
	found := false
	fyne.DoAndWait(func() {
		roots := u.uiRoots()
		for _, r := range roots {
			if clickWalk(r, q, true) {
				found = true
				return
			}
		}
		if u.tabs != nil && selectTabByLabel(u.tabs, q, true) {
			found = true
			return
		}
		for _, r := range roots {
			if clickWalk(r, q, false) {
				found = true
				return
			}
		}
		found = u.tabs != nil && selectTabByLabel(u.tabs, q, false)
	})
	return found
}

func selectTabByLabel(tabs *container.AppTabs, q string, exact bool) bool {
	for _, it := range tabs.Items {
		if textMatches(it.Text, q, exact) {
			tabs.Select(it)
			return true
		}
	}
	return false
}

func clickWalk(obj fyne.CanvasObject, q string, exact bool) bool {
	if obj == nil || !obj.Visible() {
		return false
	}
	switch o := obj.(type) {
	case *widget.Button:
		if o.OnTapped != nil && textMatches(o.Text, q, exact) {
			o.OnTapped()
			return true
		}
	case *kitButton:
		if o.OnTapped != nil && !o.Disabled() && textMatches(o.text, q, exact) {
			o.OnTapped()
			return true
		}
	case *widget.Check:
		if textMatches(o.Text, q, exact) {
			o.SetChecked(!o.Checked)
			return true
		}
	case *widget.Hyperlink:
		if textMatches(o.Text, q, exact) && o.OnTapped != nil {
			o.OnTapped()
			return true
		}
	case *kitSegmented:
		// Same convention as RadioGroup below: match an OPTION label, select it.
		for _, opt := range o.options {
			if textMatches(opt, q, exact) {
				o.Select(opt)
				return true
			}
		}
	case *widget.RadioGroup:
		// A RadioGroup (Segmented: MODE/KIND/SORT) has no label of its own -
		// clicking it means selecting the OPTION whose text matches the query.
		// Fyne's SetSelected does NOT fire OnChanged programmatically, so we call
		// it ourselves to drive the control exactly like a real tap.
		for _, opt := range o.Options {
			if textMatches(opt, q, exact) {
				if o.Selected != opt {
					o.SetSelected(opt)
					if o.OnChanged != nil {
						o.OnChanged(opt)
					}
				}
				return true
			}
		}
	case *container.AppTabs:
		if sel := o.Selected(); sel != nil {
			return clickWalk(sel.Content, q, exact)
		}
		return false
	}
	for _, ch := range childrenOf(obj) {
		if clickWalk(ch, q, exact) {
			return true
		}
	}
	return false
}

func textMatches(text, q string, exact bool) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	if exact {
		return t == q
	}
	return strings.Contains(t, q)
}

// Tap hits-tests (x,y) in canvas coordinates and triggers the leaf widget's
// tap action - coordinate equivalent of Click. Returns false if no visible
// leaf contains the point. Mirrors Click's invocation of OnTapped / SetChecked
// for parity with label-driven clicks.
func (u *UI) Tap(x, y float32) bool {
	hit := false
	fyne.DoAndWait(func() {
		var path []fyne.CanvasObject
		for _, r := range u.uiRoots() {
			if path = hitPath(r, x, y); path != nil {
				break
			}
		}
		// Per-element absolute origins so the Tappable fallback can pass
		// widget-LOCAL coordinates - what a real tap delivers in e.Position
		// (position-sensitive widgets like the waveform depend on it).
		abs := make([]fyne.Position, len(path))
		offX, offY := float32(0), float32(0)
		for i, o := range path {
			p := o.Position()
			offX += p.X
			offY += p.Y
			abs[i] = fyne.NewPos(offX, offY)
		}
		// Deepest interactive object on the hit path wins - the leaf is often a
		// plain Label inside a tappable row (widget.List items).
		for i := len(path) - 1; i >= 0; i-- {
			switch o := path[i].(type) {
			case *widget.Button:
				if o.OnTapped != nil {
					o.OnTapped()
				}
			case *widget.Check:
				o.SetChecked(!o.Checked)
			case *widget.Hyperlink:
				if o.OnTapped != nil {
					o.OnTapped()
				}
			case *widget.Entry:
				o.FocusGained()
			case *widget.Select:
				// No public Tapped on Select at this Fyne version; focus is the
				// closest analogue (keyboard nav can then open the menu).
				o.FocusGained()
			default:
				// Generic Tappable fallback (covers custom widgets + list rows).
				if t, ok := path[i].(fyne.Tappable); ok {
					t.Tapped(&fyne.PointEvent{
						AbsolutePosition: fyne.NewPos(x, y),
						Position:         fyne.NewPos(x-abs[i].X, y-abs[i].Y),
					})
				} else {
					continue
				}
			}
			hit = true
			return
		}
	})
	return hit
}

// TapSecondary hit-tests (x,y) and fires the deepest SecondaryTappable - the ctl
// right-click (context menus). Same path/coordinate discipline as Tap.
func (u *UI) TapSecondary(x, y float32) bool {
	hit := false
	fyne.DoAndWait(func() {
		var path []fyne.CanvasObject
		for _, r := range u.uiRoots() {
			if path = hitPath(r, x, y); path != nil {
				break
			}
		}
		abs := make([]fyne.Position, len(path))
		offX, offY := float32(0), float32(0)
		for i, o := range path {
			p := o.Position()
			offX += p.X
			offY += p.Y
			abs[i] = fyne.NewPos(offX, offY)
		}
		for i := len(path) - 1; i >= 0; i-- {
			if t, ok := path[i].(fyne.SecondaryTappable); ok {
				t.TappedSecondary(&fyne.PointEvent{
					AbsolutePosition: fyne.NewPos(x, y),
					Position:         fyne.NewPos(x-abs[i].X, y-abs[i].Y),
				})
				hit = true
				return
			}
		}
	})
	return hit
}

// hitPath returns the root→leaf chain of visible objects under (x,y), or nil
// when the point misses. Positions are parent-relative, so coordinates are
// translated into each object's local space while descending.
func hitPath(obj fyne.CanvasObject, x, y float32) []fyne.CanvasObject {
	if obj == nil || !obj.Visible() {
		return nil
	}
	p, s := obj.Position(), obj.Size()
	if x < p.X || x >= p.X+s.Width || y < p.Y || y >= p.Y+s.Height {
		return nil
	}
	lx, ly := x-p.X, y-p.Y
	// Topmost child first: Stack containers draw later Objects on top (kitToolStrip is
	// Stack(bg, content) - forward order would stop at the bg rectangle).
	chs := childrenOf(obj)
	for i := len(chs) - 1; i >= 0; i-- {
		if sub := hitPath(chs[i], lx, ly); sub != nil {
			return append([]fyne.CanvasObject{obj}, sub...)
		}
	}
	// AppTabs: descend into the selected tab's content.
	if tabs, ok := obj.(*container.AppTabs); ok {
		if sel := tabs.Selected(); sel != nil {
			if sub := hitPath(sel.Content, lx, ly); sub != nil {
				return append([]fyne.CanvasObject{obj}, sub...)
			}
		}
	}
	return []fyne.CanvasObject{obj}
}

// Type appends text to the focused Entry (ctl TYPE). Fyne has no public
// synthetic-keypress API, so we go through Entry.SetText - preserves the
// field's validation/normalisation and handles Unicode. The focused
// widget is preferred; falls back to the only Entry in the tree.
func (u *UI) Type(text string) bool {
	ok := false
	fyne.DoAndWait(func() {
		var target *widget.Entry
		if f := u.win.Canvas().Focused(); f != nil {
			if e, isEntry := f.(*widget.Entry); isEntry {
				target = e
			}
		}
		if target == nil {
			for _, r := range u.uiRoots() {
				if target = findOnlyEntry(r); target != nil {
					break
				}
			}
		}
		if target == nil {
			return
		}
		target.SetText(target.Text + text)
		ok = true
	})
	return ok
}

// findOnlyEntry returns the lone *widget.Entry under root, or nil if
// there are zero or more than one (ambiguous - caller should tap to focus).
func findOnlyEntry(obj fyne.CanvasObject) *widget.Entry {
	count := 0
	var found *widget.Entry
	var walk func(o fyne.CanvasObject)
	walk = func(o fyne.CanvasObject) {
		if o == nil || !o.Visible() {
			return
		}
		if e, ok := o.(*widget.Entry); ok {
			count++
			found = e
			return
		}
		if tabs, ok := o.(*container.AppTabs); ok {
			if sel := tabs.Selected(); sel != nil {
				walk(sel.Content)
			}
			return
		}
		for _, ch := range childrenOf(o) {
			walk(ch)
		}
	}
	walk(obj)
	if count == 1 {
		return found
	}
	return nil
}

// Read returns the current text/value of the first leaf whose label contains
// query (case-insensitive). For Entry: o.Text; Select: o.Selected (or
// PlaceHolder); Check: "on"/"off"; Label/Button/Hyperlink: the label text.
func (u *UI) Read(query string) (string, bool) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return "", false
	}
	var out string
	found := false
	fyne.DoAndWait(func() {
		for _, r := range u.uiRoots() {
			if readWalk(r, q, &out) {
				found = true
				return
			}
		}
		if u.tabs != nil {
			readTabsWalk(u.tabs, q, &out)
			found = out != ""
		}
	})
	return out, found
}

func readWalk(obj fyne.CanvasObject, q string, out *string) bool {
	if obj == nil || !obj.Visible() || *out != "" {
		return false
	}
	// Check leaf types we can read.
	switch o := obj.(type) {
	case *widget.Entry:
		if textMatches(o.PlaceHolder, q, false) || textMatches(o.Text, q, false) {
			*out = o.Text
			return true
		}
	case *widget.Select:
		if textMatches(o.PlaceHolder, q, false) || textMatches(o.Selected, q, false) {
			if o.Selected != "" {
				*out = o.Selected
			} else {
				*out = "ph:" + o.PlaceHolder
			}
			return true
		}
	case *widget.Check:
		if textMatches(o.Text, q, false) {
			if o.Checked {
				*out = "on"
			} else {
				*out = "off"
			}
			return true
		}
	case *widget.Label, *widget.Button, *widget.Hyperlink, *kitButton:
		if t := objText(obj); t != "" && textMatches(t, q, false) {
			*out = t
			return true
		}
	}
	for _, ch := range childrenOf(obj) {
		if readWalk(ch, q, out) {
			return true
		}
	}
	return false
}

func readTabsWalk(tabs *container.AppTabs, q string, out *string) {
	for _, it := range tabs.Items {
		if textMatches(it.Text, q, false) {
			// Tab labels have no "value" - return the label.
			*out = it.Text
			return
		}
	}
}

// Set mutates the value of the first leaf whose label contains query.
// query is the first whitespace-delimited token; value is the rest of the
// line. Entry: SetText; Select: SetSelected; Check: SetChecked if value
// is on/true/1.
func (u *UI) Set(query, value string) bool {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return false
	}
	ok := false
	fyne.DoAndWait(func() {
		for _, r := range u.uiRoots() {
			if setWalk(r, q, value) {
				ok = true
				return
			}
		}
	})
	return ok
}

func setWalk(obj fyne.CanvasObject, q, value string) bool {
	if obj == nil || !obj.Visible() {
		return false
	}
	switch o := obj.(type) {
	case *widget.Entry:
		if textMatches(o.PlaceHolder, q, false) || textMatches(o.Text, q, false) {
			o.SetText(value)
			return true
		}
	case *widget.Select:
		if textMatches(o.PlaceHolder, q, false) || textMatches(o.Selected, q, false) {
			o.SetSelected(value)
			return true
		}
	case *widget.Check:
		if textMatches(o.Text, q, false) {
			o.SetChecked(value == "on" || value == "true" || value == "1")
			return true
		}
	case *widget.RadioGroup:
		// RadioGroup (Segmented: MODE/KIND/SORT) has no label - identify the group when q
		// matches any option, then select the option matching value (substring).
		group := false
		for _, opt := range o.Options {
			if textMatches(opt, q, false) {
				group = true
				break
			}
		}
		if group {
			val := strings.ToLower(strings.TrimSpace(value))
			for _, opt := range o.Options {
				if textMatches(opt, val, false) {
					if o.Selected != opt {
						o.SetSelected(opt)
						if o.OnChanged != nil {
							o.OnChanged(opt) // SetSelected doesn't fire OnChanged programmatically - drive it like a real tap
						}
					}
					return true
				}
			}
		}
	}
	for _, ch := range childrenOf(obj) {
		if setWalk(ch, q, value) {
			return true
		}
	}
	return false
}

// ScreenshotRegion captures only the (x,y,w,h) rectangle of the canvas
// (control-plane SCREENSHOT-REGION) - useful for zooming in on a clipped
// or overflowing widget without paying the full-canvas PNG cost.
func (u *UI) ScreenshotRegion(path string, x, y, w, h float32) error {
	if w < 1 || h < 1 {
		return fmt.Errorf("region too small")
	}
	var img image.Image
	fyne.DoAndWait(func() { img = u.win.Canvas().Capture() })
	if img == nil {
		return fmt.Errorf("canvas capture returned nil")
	}
	// Fyne's GL backend returns a *gl.captureImage (its own image.Image impl);
	// the software painter returns *image.NRGBA. Normalize to a fresh NRGBA so
	// we can SubImage a known concrete type and png.Encode a contiguous buffer.
	nrgba, ok := img.(*image.NRGBA)
	if !ok {
		src := image.NewNRGBA(img.Bounds())
		draw.Draw(src, src.Bounds(), img, img.Bounds().Min, draw.Src)
		nrgba = src
	}
	b := nrgba.Bounds()
	rx, ry := int(x), int(y)
	rw, rh := int(w), int(h)
	if rx < b.Min.X {
		rx = b.Min.X
	}
	if ry < b.Min.Y {
		ry = b.Min.Y
	}
	if rx+rw > b.Max.X {
		rw = b.Max.X - rx
	}
	if ry+rh > b.Max.Y {
		rh = b.Max.Y - ry
	}
	if rw < 1 || rh < 1 {
		return fmt.Errorf("region outside canvas")
	}
	sub := nrgba.SubImage(image.Rect(rx, ry, rx+rw, ry+rh))
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return png.Encode(f, sub)
}

// childrenOf returns a container/widget's child objects (explicit - no reflection; covers the
// container + content-bearing widget types this app uses).
func childrenOf(obj fyne.CanvasObject) []fyne.CanvasObject {
	switch o := obj.(type) {
	case *fyne.Container:
		return o.Objects
	case *container.Scroll:
		return []fyne.CanvasObject{o.Content}
	case *container.Split:
		return []fyne.CanvasObject{o.Leading, o.Trailing}
	case *widget.Card:
		if o.Content != nil {
			return []fyne.CanvasObject{o.Content}
		}
	case *widget.PopUp:
		// Dialogs/popovers (canvas overlays) - without this their content is unreachable.
		return []fyne.CanvasObject{o.Content}
	case *container.AppTabs:
		// Walk the selected tab's content so the active tab's widgets are visible
		// to Read/Set/Click/Tap - otherwise only tab labels are reachable.
		idx := o.SelectedIndex()
		if idx >= 0 && idx < len(o.Items) && o.Items[idx] != nil {
			return []fyne.CanvasObject{o.Items[idx].Content}
		}
	}
	// Fallback: walk any other widget's rendered tree - this is what makes
	// widget.List rows (unexported item widgets) reachable. Known leaf widgets are
	// excluded; their renderer internals (canvas primitives) would only add noise.
	switch obj.(type) {
	case *widget.Label, *widget.Button, *widget.Check, *widget.Hyperlink, *widget.Select,
		*widget.Entry, *widget.RichText, *widget.ProgressBar, *widget.Slider,
		*widget.RadioGroup, *widget.Separator, *widget.Icon, *kitButton:
		return nil
	}
	if w, ok := obj.(fyne.Widget); ok {
		if r := fynetest.WidgetRenderer(w); r != nil {
			return r.Objects()
		}
	}
	return nil
}

// objText pulls the human-visible text from a leaf widget ("" if none).
func objText(obj fyne.CanvasObject) string {
	switch o := obj.(type) {
	case *widget.Label:
		return o.Text
	case *widget.Button:
		return o.Text
	case *kitButton:
		return o.text
	case *widget.Hyperlink:
		return o.Text
	case *widget.Check:
		if o.Checked {
			return o.Text + " ✓"
		}
		return o.Text
	case *widget.Select:
		n := fmt.Sprintf(" [%d opts]", len(o.Options))
		if o.Selected != "" {
			return o.Selected + n
		}
		return o.PlaceHolder + n
	case *widget.Entry:
		if o.Text != "" {
			return o.Text
		}
		return "ph:" + o.PlaceHolder
	case *widget.RichText:
		return richTextString(o)
	case *widget.Card:
		return strings.TrimSpace(strings.TrimSuffix(o.Title+" / "+o.Subtitle, " / "))
	case *canvas.Text:
		return o.Text
	}
	return ""
}

func richTextString(rt *widget.RichText) string {
	var b strings.Builder
	for _, seg := range rt.Segments {
		if ts, ok := seg.(*widget.TextSegment); ok {
			b.WriteString(ts.Text)
		}
	}
	return b.String()
}

// objType is the short Go type name (no package), e.g. "*widget.Label" → "Label".
func objType(obj fyne.CanvasObject) string {
	name := fmt.Sprintf("%T", obj)
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	return name
}

func trunc(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", "⏎")
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

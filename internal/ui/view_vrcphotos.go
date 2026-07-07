package ui

import (
	"net/url"
	"path/filepath"
	"sort"
	"strconv"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/vrcphotos"
)

// allPhotosLabel is the left-list pseudo-group showing every photo regardless of label.
const allPhotosLabel = "All Photos"

// vrcPhotosDialog browses ALL VRChat screenshots - organized AND un-organizable (old/loose) ones -
// grouped by event/world label ("Unorganized" when unknown). Pick a group, see its photos as
// thumbnails (each captioned with its label), click one to view it large. Backed by
// vrcphotos.ScanAll, so photos taken when rave-mate wasn't running still appear.
func (u *UI) vrcPhotosDialog() {
	if u.svc.VRCTools == nil {
		dialog.ShowInformation("Photos", "VRChat tools are unavailable.", u.win)
		return
	}
	photos := u.svc.VRCTools.Photos()
	groups := photoGroups(photos)

	preview := canvas.NewImageFromResource(nil)
	preview.FillMode = canvas.ImageFillContain
	preview.SetMinSize(fyne.NewSize(520, 380))
	caption := widget.NewLabel("")
	caption.Wrapping = fyne.TextWrapWord

	thumbs := container.NewGridWrap(fyne.NewSize(160, 150))
	thumbScroll := container.NewVScroll(thumbs)

	showGroup := func(label string) {
		thumbs.Objects = nil
		for _, ph := range photos {
			if label != allPhotosLabel && ph.Label != label {
				continue
			}
			p := ph // capture for the closure
			img := canvas.NewImageFromFile(p.File)
			img.FillMode = canvas.ImageFillContain
			img.SetMinSize(fyne.NewSize(150, 96))
			cap := widget.NewLabel(p.Label)
			cap.Truncation = fyne.TextTruncateEllipsis
			cell := container.NewBorder(nil, cap, nil, nil, img)
			thumbs.Add(newTappable(cell, func() {
				preview.File = p.File
				preview.Refresh()
				caption.SetText(p.Name + "  ·  " + p.Label + "  ·  " + p.TakenAt.Format("2006-01-02 15:04"))
			}))
		}
		thumbs.Refresh()
	}

	groupList := widget.NewList(
		func() int { return len(groups) },
		func() fyne.CanvasObject { l := widget.NewLabel(""); l.Truncation = fyne.TextTruncateEllipsis; return l },
		func(i widget.ListItemID, o fyne.CanvasObject) { o.(*widget.Label).SetText(groups[i].text()) },
	)
	groupList.OnSelected = func(i widget.ListItemID) {
		if i >= 0 && i < len(groups) {
			showGroup(groups[i].label)
		}
	}

	openBtn := widget.NewButton("Open folder", func() { openInExplorer(u, u.svc.VRCTools.PhotosDir()) })
	left := container.NewBorder(widget.NewLabel("Events / worlds"), openBtn, nil, nil, groupList)
	right := container.NewBorder(caption, nil, nil, nil, container.NewBorder(nil, thumbScroll, nil, nil, preview))
	body := container.NewBorder(nil, nil, container.NewGridWrap(fyne.NewSize(220, 460), left), nil, right)

	if len(photos) == 0 {
		body = container.NewVBox(widget.NewLabel("No VRChat screenshots found."),
			mutedLabel("Take photos in VRChat (default Pictures\\VRChat) - they appear here, filed under the rave.page event or world when known."))
	}
	d := dialog.NewCustom("VRChat photos", "Done", body, u.win)
	d.Resize(fyne.NewSize(900, 600))
	if len(groups) > 0 {
		groupList.Select(0)
	}
	d.Show()
}

// photoGroup is a left-list row: a label + its photo count.
type photoGroup struct {
	label string
	count int
}

func (g photoGroup) text() string { return g.label + " (" + strconv.Itoa(g.count) + ")" }

// photoGroups builds the left-list rows: "All Photos" first, then labels A→Z, "Unorganized" last.
func photoGroups(photos []vrcphotos.Photo) []photoGroup {
	counts := map[string]int{}
	for _, p := range photos {
		counts[p.Label]++
	}
	if len(counts) == 0 {
		return nil
	}
	labels := make([]string, 0, len(counts))
	for l := range counts {
		if l != vrcphotos.Unorganized {
			labels = append(labels, l)
		}
	}
	sort.Strings(labels)
	out := make([]photoGroup, 0, len(labels)+2)
	out = append(out, photoGroup{allPhotosLabel, len(photos)})
	for _, l := range labels {
		out = append(out, photoGroup{l, counts[l]})
	}
	if n := counts[vrcphotos.Unorganized]; n > 0 {
		out = append(out, photoGroup{vrcphotos.Unorganized, n})
	}
	return out
}

// openInExplorer opens a folder in the OS file browser (best-effort).
func openInExplorer(u *UI, dir string) {
	if uri, err := url.Parse("file:///" + filepath.ToSlash(dir)); err == nil {
		_ = u.app.OpenURL(uri)
	}
}

// tappable wraps a CanvasObject (e.g. a thumbnail) with a tap handler.
type tappable struct {
	widget.BaseWidget
	content fyne.CanvasObject
	onTap   func()
}

func newTappable(content fyne.CanvasObject, onTap func()) *tappable {
	t := &tappable{content: content, onTap: onTap}
	t.ExtendBaseWidget(t)
	return t
}

func (t *tappable) Tapped(*fyne.PointEvent) {
	if t.onTap != nil {
		t.onTap()
	}
}
func (t *tappable) CreateRenderer() fyne.WidgetRenderer { return widget.NewSimpleRenderer(t.content) }

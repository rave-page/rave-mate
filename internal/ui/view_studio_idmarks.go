package ui

// Library → ID Marks: manage the unreleased-track redaction list (internal/idmark). Marks
// are files or folders (folder = recursive); a marked track shows title "ID" on EVERY
// output - overlays, live stream, now-playing file, recorder tracklist, Publish UI, VR
// overlays - with artist and label/album hidden unless the per-mark toggles allow them.

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/idmark"
)

const idMarksHelp = "Leak prevention for unreleased tracks (\"IDs\"). Mark a file or a folder " +
	"(folders match everything inside them) and every output - overlays, the live stream, the " +
	"now-playing file, the recorder tracklist, the Publish tab and VR overlays - shows the " +
	"track as \"ID\" while it plays. Artist and label/album stay hidden unless you allow them " +
	"per mark. A mark on a file overrides its folder's mark. Marks apply to this computer's " +
	"outputs; a paired instance keeps its own list."

// idMarksSection renders the mark list + add/remove + per-mark visibility toggles.
func (sv *studioView) idMarksSection() fyne.CanvasObject {
	st := sv.u.svc.IDMarks
	if st == nil {
		return container.NewCenter(mutedLabel("ID marks unavailable (no store)."))
	}
	marks := st.List()

	rebuild := func() { sv.showSection("ID Marks") } // re-enter: re-reads the store
	list := widget.NewList(
		func() int { return len(marks) },
		func() fyne.CanvasObject {
			path := widget.NewLabel("")
			path.Truncation = fyne.TextTruncateEllipsis
			artist := widget.NewCheck("Show artist", nil)
			label := widget.NewCheck("Show label", nil)
			rm := newKitIconButton(theme.DeleteIcon(), "Remove this mark (identity shows again)", nil)
			rm.SetDanger(true)
			return container.NewBorder(nil, nil, nil, container.NewHBox(artist, label, rm), path)
		},
		func(id widget.ListItemID, o fyne.CanvasObject) {
			if id < 0 || id >= len(marks) {
				return
			}
			m := marks[id]
			c := o.(*fyne.Container)
			c.Objects[0].(*widget.Label).SetText(m.Path)
			right := c.Objects[1].(*fyne.Container)
			artist := right.Objects[0].(*widget.Check)
			label := right.Objects[1].(*widget.Check)
			rm := right.Objects[2].(*kitIconButton)
			artist.OnChanged = nil
			artist.SetChecked(m.ShowArtist)
			artist.OnChanged = func(on bool) {
				st.Set(m.Path, idmark.Mark{ShowArtist: on, ShowLabel: m.ShowLabel})
				marks = st.List()
			}
			label.OnChanged = nil
			label.SetChecked(m.ShowLabel)
			label.OnChanged = func(on bool) {
				st.Set(m.Path, idmark.Mark{ShowArtist: m.ShowArtist, ShowLabel: on})
				marks = st.List()
			}
			rm.onTapped = func() {
				st.Remove(m.Path)
				rebuild()
			}
		},
	)

	addFile := newKitButtonWithIcon("Mark file…", theme.DocumentIcon(), func() {
		win := currentWindow()
		if win == nil {
			return
		}
		showFileOpen(win, func(rc fyne.URIReadCloser, _ error) {
			if rc == nil {
				return
			}
			p := rc.URI().Path()
			_ = rc.Close()
			st.Set(p, idmark.Mark{})
			rebuild()
		})
	})
	addFile.SetVariant(kitBtnBrand)
	addDir := newKitButtonWithIcon("Mark folder…", theme.FolderIcon(), func() {
		win := currentWindow()
		if win == nil {
			return
		}
		showFolderOpen(win, func(lu fyne.ListableURI, _ error) {
			if lu != nil {
				st.Set(lu.Path(), idmark.Mark{})
				rebuild()
			}
		})
	})

	head := container.NewVBox(
		container.NewHBox(boldLabel("ID Marks"), helpIcon(idMarksHelp)),
		mutedLabel("Marked files/folders play as \"ID\" on every output. Right-click any Library row → \"Mark as ID\" also adds here."),
		WrapActions(addFile, addDir),
		widget.NewSeparator(),
	)
	if len(marks) == 0 {
		return container.NewBorder(head, nil, nil, nil,
			container.NewCenter(mutedLabel("No marks yet. Mark a promo folder once - everything inside stays an \"ID\" until you unmark it.")))
	}
	return container.NewBorder(head, nil, nil, nil, list)
}

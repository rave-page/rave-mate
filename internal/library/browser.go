package library

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/debuglog"
)

// MetadataProbe resolves media duration out-of-process (e.g. via worker.Supervisor).
// Browser works with probe == nil - duration rows are omitted entirely.
type MetadataProbe interface {
	Duration(ctx context.Context, path string) (float64, error)
}

// Browser is a native file browser + metadata panel + directory favorites.
type Browser struct {
	probe MetadataProbe
	marks *Bookmarks

	cwd     string
	entries []entry
	showHid bool

	// widgets updated on navigation / selection
	breadcrumb *fyne.Container
	list       *widget.List
	panel      *metaPanel
	starBtn    *widget.Button
	marksBar   *fyne.Container
}

// New returns a Browser. probe may be nil. bookmarksFile backs directory favorites ("" =
// in-memory only).
func New(probe MetadataProbe, bookmarksFile string) *Browser {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = string(filepath.Separator)
	}
	return &Browser{probe: probe, marks: LoadBookmarks(bookmarksFile), cwd: home}
}

// View builds and returns the Fyne widget tree. Call once; reuse the result.
func (b *Browser) View() fyne.CanvasObject {
	if b.marks == nil {
		b.marks = LoadBookmarks("") // in-memory fallback (e.g. tests constructing Browser directly)
	}
	b.panel = newMetaPanel(b.probe)

	// ── Toolbar: Up button + breadcrumb ─────────────────────────────────────
	upBtn := widget.NewButtonWithIcon("", theme.NavigateBackIcon(), func() {
		parent := filepath.Dir(b.cwd)
		if parent != b.cwd {
			b.navigate(parent)
		}
	})
	upBtn.Importance = widget.LowImportance

	// ★ toggle: bookmark / unbookmark the current directory.
	b.starBtn = widget.NewButtonWithIcon("", theme.GridIcon(), func() {
		b.marks.Toggle(b.cwd, filepath.Base(b.cwd))
		b.refreshStar()
		b.rebuildMarksBar()
	})
	b.starBtn.Importance = widget.LowImportance

	b.breadcrumb = container.NewHBox()
	toolbar := container.NewBorder(nil, nil, container.NewHBox(upBtn, b.starBtn), nil, b.breadcrumb)

	// ── Show-hidden checkbox ─────────────────────────────────────────────────
	hidChk := widget.NewCheck("Show hidden", func(v bool) {
		b.showHid = v
		b.reload()
	})

	b.marksBar = container.NewHBox()
	b.rebuildMarksBar()
	head := container.NewVBox(toolbar, container.NewHScroll(b.marksBar), hidChk, widget.NewSeparator())

	// ── File list ────────────────────────────────────────────────────────────
	b.list = widget.NewList(
		func() int { return len(b.entries) },
		func() fyne.CanvasObject {
			icon := widget.NewIcon(theme.FileIcon())
			name := widget.NewLabel("placeholder")
			name.Truncation = fyne.TextTruncateEllipsis
			sizeL := mutedLbl("000.0 MB")
			return container.NewBorder(nil, nil, icon,
				container.NewHBox(layout.NewSpacer(), sizeL), name)
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			if id < 0 || id >= len(b.entries) {
				return
			}
			e := b.entries[id]
			// NewBorder stores children center-first: Objects = [center(name), left(icon), right(sizeBox)].
			border := obj.(*fyne.Container)
			name := border.Objects[0].(*widget.Label)
			name.SetText(e.name)
			icon := border.Objects[1].(*widget.Icon)
			icon.SetResource(kindIcon(e.kind))
			sizeBox := border.Objects[2].(*fyne.Container)
			sizeL := sizeBox.Objects[1].(*widget.Label)
			if e.isDirectory {
				sizeL.SetText("")
			} else {
				sizeL.SetText(humanSize(e.sizeBytes))
			}
		},
	)
	b.list.OnSelected = func(id widget.ListItemID) {
		if id < 0 || id >= len(b.entries) {
			return
		}
		e := b.entries[id]
		if e.isDirectory {
			b.list.UnselectAll()
			b.navigate(e.path)
		} else {
			b.panel.show(e)
		}
	}

	// initial load
	b.navigate(b.cwd)

	listView := container.NewBorder(head, nil, nil, nil, b.list)
	split := container.NewHSplit(listView, b.panel.view())
	split.SetOffset(0.55)
	return split
}

// navigate moves the browser to dir and refreshes the list.
func (b *Browser) navigate(dir string) {
	dir = filepath.Clean(dir)
	entries, err := readDir(dir, b.showHid)
	if err != nil {
		// Stay on current dir; show nothing - caller sees an empty list.
		return
	}
	b.cwd = dir
	b.entries = entries
	b.rebuildBreadcrumb()
	b.refreshStar()
	b.list.Refresh()
	b.panel.clear()
}

// refreshStar reflects whether the current dir is bookmarked.
func (b *Browser) refreshStar() {
	if b.starBtn == nil {
		return
	}
	if b.marks.Has(b.cwd) {
		b.starBtn.SetIcon(theme.GridIcon())
		b.starBtn.SetText("★ saved")
	} else {
		b.starBtn.SetIcon(theme.ContentAddIcon())
		b.starBtn.SetText("Bookmark")
	}
}

// rebuildMarksBar renders a chip per favorite; clicking navigates there.
func (b *Browser) rebuildMarksBar() {
	if b.marksBar == nil {
		return
	}
	b.marksBar.Objects = b.marksBar.Objects[:0]
	for _, bm := range b.marks.List() {
		chip := widget.NewButtonWithIcon(bm.Label, theme.FolderIcon(), func() { b.navigate(bm.Path) })
		chip.Importance = widget.LowImportance
		b.marksBar.Objects = append(b.marksBar.Objects, chip)
	}
	b.marksBar.Refresh()
}

// reload re-reads the current dir (e.g. after show-hidden toggle).
func (b *Browser) reload() {
	entries, err := readDir(b.cwd, b.showHid)
	if err != nil {
		return
	}
	b.entries = entries
	b.list.Refresh()
}

// rebuildBreadcrumb refreshes the clickable path segments.
func (b *Browser) rebuildBreadcrumb() {
	b.breadcrumb.Objects = b.breadcrumb.Objects[:0]
	segments := splitPath(b.cwd)
	for i, seg := range segments {
		btn := widget.NewButton(seg.label, func() { b.navigate(seg.path) })
		btn.Importance = widget.LowImportance
		b.breadcrumb.Objects = append(b.breadcrumb.Objects, btn)
		if i < len(segments)-1 {
			b.breadcrumb.Objects = append(b.breadcrumb.Objects, widget.NewLabel("/"))
		}
	}
	b.breadcrumb.Refresh()
}

// pathSegment is one breadcrumb segment.
type pathSegment struct {
	label string
	path  string
}

// splitPath decomposes an absolute path into clickable breadcrumb segments.
func splitPath(p string) []pathSegment {
	p = filepath.Clean(p)
	parts := strings.Split(p, string(filepath.Separator))
	segs := make([]pathSegment, 0, len(parts))
	acc := ""
	for _, part := range parts {
		if part == "" {
			// root separator on Unix or drive letter prefix on Windows
			if acc == "" {
				acc = string(filepath.Separator)
				segs = append(segs, pathSegment{label: string(filepath.Separator), path: acc})
			}
			continue
		}
		if acc == string(filepath.Separator) {
			acc = acc + part
		} else {
			acc = acc + string(filepath.Separator) + part
		}
		segs = append(segs, pathSegment{label: part, path: acc})
	}
	if len(segs) == 0 {
		segs = append(segs, pathSegment{label: p, path: p})
	}
	return segs
}

// kindIcon returns a theme icon for each media kind.
func kindIcon(k kind) fyne.Resource {
	switch k {
	case kindDirectory:
		return theme.FolderIcon()
	case kindAudio:
		return theme.MediaMusicIcon()
	case kindVideo:
		return theme.MediaVideoIcon()
	case kindImage:
		return theme.FileImageIcon()
	default:
		return theme.FileIcon()
	}
}

// ── metadata panel ───────────────────────────────────────────────────────────

// metaPanel shows file metadata + async duration for audio/video.
type metaPanel struct {
	probe MetadataProbe

	nameL     *widget.Label
	pathL     *widget.Label
	sizeL     *widget.Label
	modL      *widget.Label
	kindL     *widget.Label
	extL      *widget.Label
	durationL *widget.Label
	durationR *widget.RichText // row container visibility
	cancel    context.CancelFunc
}

func newMetaPanel(probe MetadataProbe) *metaPanel {
	mk := func(text string) *widget.Label {
		l := widget.NewLabel(text)
		l.Wrapping = fyne.TextWrapWord
		return l
	}
	return &metaPanel{
		probe:     probe,
		nameL:     mk(""),
		pathL:     mk(""),
		sizeL:     mk(""),
		modL:      mk(""),
		kindL:     mk(""),
		extL:      mk(""),
		durationL: mk(""),
		durationR: widget.NewRichText(),
	}
}

func (p *metaPanel) view() fyne.CanvasObject {
	row := func(label string, val *widget.Label) fyne.CanvasObject {
		return container.NewGridWithColumns(2, mutedLbl(label), val)
	}
	empty := widget.NewLabelWithStyle("Select a file to see details",
		fyne.TextAlignCenter, fyne.TextStyle{Italic: true})
	empty.Importance = widget.LowImportance

	detail := container.NewVBox(
		row("Name", p.nameL),
		row("Path", p.pathL),
		row("Size", p.sizeL),
		row("Modified", p.modL),
		row("Kind", p.kindL),
		row("Extension", p.extL),
		row("Duration", p.durationL),
	)
	return container.NewBorder(
		widget.NewLabelWithStyle("Details", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		nil, nil, nil,
		container.NewVScroll(container.NewVBox(empty, widget.NewSeparator(), detail)),
	)
}

func (p *metaPanel) show(e entry) {
	// cancel any in-flight probe
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
	p.nameL.SetText(e.name)
	p.pathL.SetText(e.path)
	p.sizeL.SetText(humanSize(e.sizeBytes))
	p.modL.SetText(e.modifiedAt.Local().Format("2006-01-02 15:04:05"))
	p.kindL.SetText(string(e.kind))
	if e.extension != "" {
		p.extL.SetText("." + e.extension)
	} else {
		p.extL.SetText("-")
	}

	if p.probe != nil && (e.kind == kindAudio || e.kind == kindVideo) {
		p.durationL.SetText("…")
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		p.cancel = cancel
		epath := e.path
		go func() {
			defer debuglog.Recover(nil, "library-probe", false)
			defer cancel()
			dur, err := p.probe.Duration(ctx, epath)
			fyne.Do(func() {
				if err != nil {
					p.durationL.SetText("n/a")
				} else {
					p.durationL.SetText(formatDuration(dur))
				}
			})
		}()
	} else {
		p.durationL.SetText("-")
	}
}

func (p *metaPanel) clear() {
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
	p.nameL.SetText("")
	p.pathL.SetText("")
	p.sizeL.SetText("")
	p.modL.SetText("")
	p.kindL.SetText("")
	p.extL.SetText("")
	p.durationL.SetText("")
}

// formatDuration converts seconds to mm:ss or h:mm:ss.
func formatDuration(secs float64) string {
	total := int(secs)
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

// mutedLbl is a package-local muted secondary label (ui.mutedLabel is in package ui).
func mutedLbl(text string) *widget.Label {
	l := widget.NewLabel(text)
	l.Importance = widget.LowImportance
	return l
}

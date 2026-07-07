package mediaeditor

import (
	"context"
	"image"
	"image/color"
	"os"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/debuglog"
)

// New returns the Media Editor Fyne view. src may be nil (disables "Load events" button).
func New(src APISource) fyne.CanvasObject {
	v := &editorView{src: src}
	return v.build()
}

type editorView struct {
	src APISource

	// form fields
	tplSel        *widget.Select
	titleEntry    *widget.Entry
	subtitleEntry *widget.Entry
	djEntry       *widget.Entry
	bgEntry       *widget.Entry
	logoEntry     *widget.Entry
	outEntry      *widget.Entry

	// live preview
	previewImg *canvas.Image

	// debounce
	debounceTimer *time.Timer

	// current poster (for export)
	poster Poster
}

func (v *editorView) build() fyne.CanvasObject {
	tpls := Templates()
	tplNames := make([]string, len(tpls))
	for i, t := range tpls {
		tplNames[i] = t.Name
	}

	v.tplSel = widget.NewSelect(tplNames, func(_ string) { v.scheduleRender() })
	v.tplSel.SetSelectedIndex(0)

	v.titleEntry = widget.NewEntry()
	v.titleEntry.SetPlaceHolder("Title (e.g. Friday Night Techno)")
	v.titleEntry.OnChanged = func(_ string) { v.scheduleRender() }

	v.subtitleEntry = widget.NewEntry()
	v.subtitleEntry.SetPlaceHolder("Subtitle (optional)")
	v.subtitleEntry.OnChanged = func(_ string) { v.scheduleRender() }

	v.djEntry = widget.NewMultiLineEntry()
	v.djEntry.SetPlaceHolder("DJ names - one per line")
	v.djEntry.Wrapping = fyne.TextWrapOff
	v.djEntry.OnChanged = func(_ string) { v.scheduleRender() }

	v.bgEntry = widget.NewEntry()
	v.bgEntry.SetPlaceHolder("Background image path (PNG/JPEG, optional)")
	v.bgEntry.OnChanged = func(_ string) { v.scheduleRender() }

	v.logoEntry = widget.NewEntry()
	v.logoEntry.SetPlaceHolder("Logo path (PNG/JPEG, optional)")
	v.logoEntry.OnChanged = func(_ string) { v.scheduleRender() }

	v.outEntry = widget.NewEntry()
	v.outEntry.SetPlaceHolder("Output path (e.g. poster.png)")
	v.outEntry.SetText("poster.png")

	// Initial preview placeholder (white 1x1 pixel).
	v.previewImg = canvas.NewImageFromImage(blankImage())
	v.previewImg.FillMode = canvas.ImageFillContain
	v.previewImg.SetMinSize(fyne.NewSize(360, 270))

	previewBtn := widget.NewButtonWithIcon("Preview", theme.MediaPlayIcon(), func() {
		v.renderPreview()
	})
	previewBtn.Importance = widget.MediumImportance

	exportBtn := widget.NewButtonWithIcon("Export PNG", theme.DocumentSaveIcon(), func() {
		v.exportPNG()
	})
	exportBtn.Importance = widget.HighImportance

	var loadBtn *widget.Button
	loadBtn = widget.NewButtonWithIcon("Load events", theme.DownloadIcon(), func() {
		loadBtn.Disable()
		go func() {
			defer debuglog.Recover(nil, "editor-load-events", false)
			events, err := v.src.UpcomingEvents(context.Background())
			fyne.Do(func() {
				loadBtn.Enable()
				if err != nil || len(events) == 0 {
					return
				}
				e := events[0]
				v.titleEntry.SetText(e.Title)
				v.djEntry.SetText(strings.Join(e.DJs, "\n"))
				if e.LogoPath != "" {
					v.logoEntry.SetText(e.LogoPath)
				}
				v.scheduleRender()
			})
		}()
	})
	if v.src == nil {
		loadBtn.Disable()
	}

	form := container.NewVBox(
		editorField("Template", v.tplSel),
		editorField("Title", v.titleEntry),
		editorField("Subtitle", v.subtitleEntry),
		editorField("DJs (one per line)", v.djEntry),
		editorField("Background image", v.bgEntry),
		editorField("Logo", v.logoEntry),
		editorField("Output file", v.outEntry),
		container.NewHBox(previewBtn, exportBtn, loadBtn),
	)

	previewPanel := container.NewVBox(
		widget.NewLabelWithStyle("Preview", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		v.previewImg,
	)

	body := container.NewHSplit(
		container.NewVScroll(form),
		container.NewVScroll(previewPanel),
	)
	body.SetOffset(0.55)

	head := container.NewVBox(
		widget.NewLabelWithStyle("Media Editor", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewSeparator(),
	)
	return container.NewBorder(head, nil, nil, nil, body)
}

func editorField(label string, w fyne.CanvasObject) fyne.CanvasObject {
	l := widget.NewLabel(label)
	l.Importance = widget.LowImportance
	return container.NewVBox(l, w)
}

// scheduleRender debounces rendering to 300 ms after the last change.
func (v *editorView) scheduleRender() {
	if v.debounceTimer != nil {
		v.debounceTimer.Stop()
	}
	v.debounceTimer = time.AfterFunc(300*time.Millisecond, func() {
		fyne.Do(v.renderPreview)
	})
}

// renderPreview runs the compositor and refreshes the preview image.
func (v *editorView) renderPreview() {
	p := v.buildPoster()
	v.poster = p
	img, err := p.Render()
	if err != nil {
		return // leave the previous preview
	}
	v.previewImg.Image = img
	v.previewImg.Refresh()
}

// exportPNG renders + writes PNG to the output path.
func (v *editorView) exportPNG() {
	outPath := strings.TrimSpace(v.outEntry.Text)
	if outPath == "" {
		outPath = "poster.png"
	}
	p := v.buildPoster()
	img, err := p.Render()
	if err != nil {
		showError(err)
		return
	}
	f, err := os.Create(outPath)
	if err != nil {
		showError(err)
		return
	}
	defer func() { _ = f.Close() }()
	if err := Encode(img, f); err != nil {
		showError(err)
		return
	}
	fyne.Do(func() {
		wins := fyne.CurrentApp().Driver().AllWindows()
		if len(wins) > 0 {
			dialog.ShowInformation("Exported", outPath, wins[0])
		}
	})
}

// buildPoster constructs a Poster from the current form state.
func (v *editorView) buildPoster() Poster {
	tpls := Templates()
	idx := v.tplSel.SelectedIndex()
	if idx < 0 || idx >= len(tpls) {
		idx = 0
	}
	t := tpls[idx]

	rawLines := strings.Split(v.djEntry.Text, "\n")
	lines := make([]string, 0, len(rawLines))
	for _, l := range rawLines {
		if s := strings.TrimSpace(l); s != "" {
			lines = append(lines, s)
		}
	}

	return Poster{
		Width:               t.Width,
		Height:              t.Height,
		Background:          t.DefaultBg,
		BackgroundImagePath: strings.TrimSpace(v.bgEntry.Text),
		Title:               strings.TrimSpace(v.titleEntry.Text),
		Subtitle:            strings.TrimSpace(v.subtitleEntry.Text),
		Lines:               lines,
		LogoPath:            strings.TrimSpace(v.logoEntry.Text),
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func showError(err error) {
	if err == nil {
		return
	}
	wins := fyne.CurrentApp().Driver().AllWindows()
	if len(wins) == 0 {
		return
	}
	fyne.Do(func() { dialog.ShowError(err, wins[0]) })
}

// blankImage returns a 4x4 dark placeholder image used before first render.
func blankImage() *solidImage {
	return &solidImage{c: color.NRGBA{R: 0x14, G: 0x14, B: 0x17, A: 0xff}, w: 4, h: 4}
}

// solidImage is a trivial image.Image of a single color.
type solidImage struct {
	c    color.Color
	w, h int
}

func (s *solidImage) ColorModel() color.Model { return color.NRGBAModel }
func (s *solidImage) Bounds() image.Rectangle { return image.Rect(0, 0, s.w, s.h) }
func (s *solidImage) At(_, _ int) color.Color { return s.c }

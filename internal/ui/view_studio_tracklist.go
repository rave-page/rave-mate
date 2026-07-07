package ui

import (
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/musiclib"
)

// Columnar collection-list metrics. Header + rows share these so columns can't drift.
const (
	trackRowH       float32 = 30  // cell height (GridWrap forces it identical per row)
	trackColLeftW   float32 = 56  // check + media icon gutter
	trackColBPMW    float32 = 52  // "128"
	trackColKeyW    float32 = 72  // harmonic key pill ("8A · Am")
	trackColTimeW   float32 = 48  // "m:ss"
	trackColGenreW  float32 = 120 // genre (truncates)
	trackColRatingW float32 = 64  // "★★★★☆"
	trackColPlaysW  float32 = 58  // "▶12"
	trackColBitW    float32 = 50  // "320k"
	trackColYearW   float32 = 50  // "2023"
)

// colCell wraps a widget in a fixed-width GridWrap so the column aligns across rows.
func colCell(w float32, o fyne.CanvasObject) *fyne.Container {
	return container.NewGridWrap(fyne.NewSize(w, trackRowH), o)
}

// newColLabel = dense, non-wrapping meta cell label (low importance).
func newColLabel(trunc bool) *widget.Label {
	l := widget.NewLabel("")
	l.Importance = widget.LowImportance
	l.Wrapping = fyne.TextWrapOff
	if trunc {
		l.Truncation = fyne.TextTruncateEllipsis
	}
	return l
}

// newTrackColsRow builds one columnar collection row: a flexing Artist-Title center
// plus fixed-width right cells (BPM, KEY, TIME, GENRE, RATING, PLAYS, KBPS, YEAR)
// that align with every other row + the header. Replaces newFileRow for the collList only.
func newTrackColsRow() fyne.CanvasObject {
	check := widget.NewCheck("", nil)
	icon := widget.NewIcon(theme.MediaMusicIcon())
	left := colCell(trackColLeftW, container.NewHBox(check, icon))

	title := widget.NewLabel("") // the ONLY flexible column
	title.Truncation = fyne.TextTruncateEllipsis

	kp := newPill("", colSecondary, colMuted, nil)
	kp.Hide()

	right := container.NewHBox(
		colCell(trackColBPMW, newColLabel(false)),
		colCell(trackColKeyW, container.NewCenter(kp)),
		colCell(trackColTimeW, newColLabel(false)),
		colCell(trackColGenreW, newColLabel(true)),
		colCell(trackColRatingW, newColLabel(false)),
		colCell(trackColPlaysW, newColLabel(false)),
		colCell(trackColBitW, newColLabel(false)),
		colCell(trackColYearW, newColLabel(false)),
	)
	return container.NewBorder(nil, nil, left, right, title)
}

// trackColsParts unpacks newTrackColsRow (mirrors fileRowParts) for fill + checkbox-rewire.
func trackColsParts(o fyne.CanvasObject) (
	check *widget.Check, icon *widget.Icon, title *widget.Label, kp *pill,
	bpm, tm, genre, rating, plays, bitrate, year *widget.Label,
) {
	c := o.(*fyne.Container)
	title = c.Objects[0].(*widget.Label)
	hb := c.Objects[1].(*fyne.Container).Objects[0].(*fyne.Container) // GridWrap → HBox(check,icon)
	check = hb.Objects[0].(*widget.Check)
	icon = hb.Objects[1].(*widget.Icon)
	r := c.Objects[2].(*fyne.Container) // right HBox of fixed cells
	bpm = r.Objects[0].(*fyne.Container).Objects[0].(*widget.Label)
	kp = r.Objects[1].(*fyne.Container).Objects[0].(*fyne.Container).Objects[0].(*pill)
	tm = r.Objects[2].(*fyne.Container).Objects[0].(*widget.Label)
	genre = r.Objects[3].(*fyne.Container).Objects[0].(*widget.Label)
	rating = r.Objects[4].(*fyne.Container).Objects[0].(*widget.Label)
	plays = r.Objects[5].(*fyne.Container).Objects[0].(*widget.Label)
	bitrate = r.Objects[6].(*fyne.Container).Objects[0].(*widget.Label)
	year = r.Objects[7].(*fyne.Container).Objects[0].(*widget.Label)
	return
}

// fillTrackCols fills a columnar row from a track (key pill harmonic-colored vs ref).
func fillTrackCols(o fyne.CanvasObject, t musiclib.Track, onDisk bool, ref *musiclib.Key) {
	_, icon, title, kp, bpm, tm, genre, rating, plays, bitrate, year := trackColsParts(o)
	title.SetText(strOrDash(t.Artist) + " - " + strOrDash(t.Title))
	if t.BPM > 0 {
		bpm.SetText(fmt.Sprintf("%.0f", t.BPM))
	} else {
		bpm.SetText("")
	}
	tm.SetText(fmtTrackDur(t.DurationSec))
	genre.SetText(t.Genre)
	rating.SetText(ratingStars(t.Rating))
	if t.PlayCount > 0 {
		plays.SetText(fmt.Sprintf("▶%d", t.PlayCount))
	} else {
		plays.SetText("")
	}
	if t.BitrateBps > 0 {
		bitrate.SetText(fmt.Sprintf("%dk", t.BitrateBps/1000))
	} else {
		bitrate.SetText("")
	}
	year.SetText(yearOf(t.ReleaseDate))
	setKeyPill(kp, t.Key, ref)
	if onDisk {
		icon.SetResource(theme.MediaMusicIcon())
	} else {
		icon.SetResource(theme.WarningIcon())
	}
}

// newTrackColsHeader = column header aligned with the rows (same widths + left gutter).
func newTrackColsHeader() fyne.CanvasObject {
	left := colCell(trackColLeftW, layout.NewSpacer())
	right := container.NewHBox(
		colCell(trackColBPMW, smallCaps("BPM")),
		colCell(trackColKeyW, smallCaps("KEY")),
		colCell(trackColTimeW, smallCaps("TIME")),
		colCell(trackColGenreW, smallCaps("GENRE")),
		colCell(trackColRatingW, smallCaps("RATING")),
		colCell(trackColPlaysW, smallCaps("PLAYS")),
		colCell(trackColBitW, smallCaps("KBPS")),
		colCell(trackColYearW, smallCaps("YEAR")),
	)
	return container.NewBorder(nil, nil, left, right, smallCaps("TRACK"))
}

// ratingStars renders a 0-5 rating as filled/empty stars ("" when unrated).
func ratingStars(r int) string {
	if r <= 0 {
		return ""
	}
	if r > 5 {
		r = 5
	}
	return strings.Repeat("★", r) + strings.Repeat("☆", 5-r)
}

// yearOf returns the leading 4-digit year of a source date ("YYYY/M/D" | "YYYY"), else "".
func yearOf(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < 4 {
		return ""
	}
	y := s[:4]
	for _, c := range y {
		if c < '0' || c > '9' {
			return ""
		}
	}
	return y
}

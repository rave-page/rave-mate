package ui

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/logbus"
)

const logMaxSelectLines = 4000 // cap the select-mode text area for responsiveness

// buildLogs renders the Logs area: the app log plus one live monitor tab per interface
// (MIDI, Traktor HTTP, Session observations) - the raw firehoses kept off the structured
// app log. Each tab is an independent bus view (color stream + select/copy).
func (u *UI) buildLogs() fyne.CanvasObject {
	tabs := container.NewAppTabs(
		container.NewTabItem("App log", u.newBusView(u.svc.Log)),
	)
	if u.svc.MIDIMon != nil {
		tabs.Append(container.NewTabItem("MIDI", u.newBusView(u.svc.MIDIMon)))
	}
	if u.svc.TraktorMon != nil {
		tabs.Append(container.NewTabItem("Traktor", u.newBusView(u.svc.TraktorMon)))
	}
	if u.svc.SessionMon != nil {
		tabs.Append(container.NewTabItem("Session", u.newBusView(u.svc.SessionMon)))
	}
	return tabs
}

// newBusView renders one logbus.Bus as a color-coded live stream (RichText per row,
// auto-scrolls) with a "Select" mode that freezes it into a read-only monospace text area
// for drag-select + copy (Fyne has no widget that is both colored and text-selectable).
func (u *UI) newBusView(bus *logbus.Bus) fyne.CanvasObject {
	lv := &logView{bus: bus, minLevel: logbus.Debug, autoscroll: true}
	lv.entries = bus.Snapshot()

	lv.list = widget.NewList(
		func() int { lv.mu.Lock(); defer lv.mu.Unlock(); return len(lv.shown) },
		func() fyne.CanvasObject {
			rt := widget.NewRichText()
			rt.Wrapping = fyne.TextWrapOff
			return rt
		},
		func(i widget.ListItemID, o fyne.CanvasObject) {
			lv.mu.Lock()
			defer lv.mu.Unlock()
			if i < 0 || i >= len(lv.shown) {
				return
			}
			rt := o.(*widget.RichText)
			rt.Segments = logSegments(lv.shown[i])
			rt.Refresh()
		},
	)
	// Tap a row to copy that line (handy without entering select mode).
	lv.list.OnSelected = func(i widget.ListItemID) {
		lv.mu.Lock()
		ok := i >= 0 && i < len(lv.shown)
		var line string
		if ok {
			line = formatEntry(lv.shown[i])
		}
		lv.mu.Unlock()
		lv.list.UnselectAll()
		if ok {
			fyne.CurrentApp().Clipboard().SetContent(line)
			u.Notify("rave-mate", "Copied log line")
		}
	}
	lv.recompute()

	lv.selectBox = newSelectableLog()
	lv.content = container.NewStack(lv.list)

	levelSel := widget.NewSelect([]string{"All", "Info+", "Warn+", "Error"}, func(s string) {
		switch s {
		case "All":
			lv.minLevel = logbus.Debug
		case "Info+":
			lv.minLevel = logbus.Info
		case "Warn+":
			lv.minLevel = logbus.Warn
		case "Error":
			lv.minLevel = logbus.Error
		}
		lv.mu.Lock()
		lv.recompute()
		lv.mu.Unlock()
		fyne.Do(func() {
			lv.list.Refresh()
			if lv.selecting {
				lv.selectBox.SetText(lv.renderText())
			}
		})
	})
	levelSel.SetSelected("All")

	autoBtn := widget.NewCheck("Auto-scroll", func(b bool) { lv.autoscroll = b })
	autoBtn.SetChecked(true)

	// Select mode: freeze → selectable text area; back to live colored stream.
	selectBtn := widget.NewButtonWithIcon("Select / Copy", theme.ContentCopyIcon(), nil)
	selectBtn.OnTapped = func() {
		lv.selecting = !lv.selecting
		if lv.selecting {
			lv.selectBox.SetText(lv.renderText())
			lv.content.Objects = []fyne.CanvasObject{container.NewScroll(lv.selectBox)}
			selectBtn.SetText("Live")
			selectBtn.SetIcon(theme.MediaPlayIcon())
			autoBtn.Disable()
		} else {
			lv.content.Objects = []fyne.CanvasObject{lv.list}
			selectBtn.SetText("Select / Copy")
			selectBtn.SetIcon(theme.ContentCopyIcon())
			autoBtn.Enable()
		}
		lv.content.Refresh()
	}

	copyAllBtn := widget.NewButtonWithIcon("Copy all", theme.ContentCopyIcon(), func() {
		fyne.CurrentApp().Clipboard().SetContent(lv.renderText())
		u.Notify("rave-mate", "Copied all shown logs")
	})

	clearBtn := widget.NewButton("Clear view", func() {
		lv.mu.Lock()
		lv.entries = nil
		lv.recompute()
		lv.mu.Unlock()
		fyne.Do(func() {
			lv.list.Refresh()
			if lv.selecting {
				lv.selectBox.SetText("")
			}
		})
	})

	bar := container.NewBorder(nil, nil,
		container.NewHBox(widget.NewLabel("Level:"), levelSel, autoBtn),
		container.NewHBox(selectBtn, copyAllBtn, clearBtn),
		mutedLabel("tap a line to copy it"),
	)

	ch, unsub := bus.Subscribe()
	u.closers = append(u.closers, unsub)
	goUI("logs", func() { lv.tail(ch) })

	return container.NewBorder(bar, nil, nil, nil, lv.content)
}

type logView struct {
	bus        *logbus.Bus
	mu         sync.Mutex
	entries    []logbus.Entry
	shown      []logbus.Entry
	list       *widget.List
	selectBox  *selectableLog
	content    *fyne.Container
	selecting  bool
	minLevel   logbus.Level
	autoscroll bool
}

func (lv *logView) recompute() {
	out := lv.shown[:0]
	for _, e := range lv.entries {
		if e.Level >= lv.minLevel {
			out = append(out, e)
		}
	}
	lv.shown = out
}

// renderText joins the shown entries as plain text (capped) for copy / select mode.
func (lv *logView) renderText() string {
	lv.mu.Lock()
	defer lv.mu.Unlock()
	start := 0
	if len(lv.shown) > logMaxSelectLines {
		start = len(lv.shown) - logMaxSelectLines
	}
	var b strings.Builder
	for _, e := range lv.shown[start:] {
		b.WriteString(formatEntry(e))
		b.WriteByte('\n')
	}
	return b.String()
}

func (lv *logView) tail(ch <-chan logbus.Entry) {
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	dirty := false
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				return
			}
			lv.mu.Lock()
			lv.entries = append(lv.entries, e)
			if len(lv.entries) > 5000 {
				lv.entries = lv.entries[len(lv.entries)-5000:]
			}
			lv.recompute()
			lv.mu.Unlock()
			dirty = true
		case <-tick.C:
			if !dirty || lv.selecting { // don't churn the text area while the user selects
				continue
			}
			dirty = false
			fyne.Do(func() {
				lv.list.Refresh()
				if lv.autoscroll {
					lv.mu.Lock()
					n := len(lv.shown)
					lv.mu.Unlock()
					if n > 0 {
						lv.list.ScrollToBottom()
					}
				}
			})
		}
	}
}

// logSegments builds the per-token colored rendering of one entry (timestamp / level /
// source dimmed, message foreground, level tinted by severity).
func logSegments(e logbus.Entry) []widget.RichTextSegment {
	mono := fyne.TextStyle{Monospace: true}
	seg := func(text string, color fyne.ThemeColorName) widget.RichTextSegment {
		return &widget.TextSegment{Text: text, Style: widget.RichTextStyle{Inline: true, TextStyle: mono, ColorName: color}}
	}
	out := []widget.RichTextSegment{
		seg(e.Time.Format("15:04:05")+"  ", theme.ColorNameDisabled),
		seg(fmt.Sprintf("%-5s", e.Level), levelColor(e.Level)),
		seg("  ["+e.Source+"]  ", theme.ColorNameDisabled),
		seg(e.Msg, theme.ColorNameForeground),
	}
	if len(e.Fields) > 0 {
		out = append(out, seg("  "+fieldsString(e.Fields), theme.ColorNameDisabled))
	}
	return out
}

func levelColor(l logbus.Level) fyne.ThemeColorName {
	switch l {
	case logbus.Error:
		return theme.ColorNameError
	case logbus.Warn:
		return theme.ColorNameWarning
	case logbus.Info:
		return theme.ColorNamePrimary
	default:
		return theme.ColorNameDisabled
	}
}

func formatEntry(e logbus.Entry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s  %-5s  [%s]  %s", e.Time.Format("15:04:05"), e.Level, e.Source, e.Msg)
	if len(e.Fields) > 0 {
		b.WriteString("  " + fieldsString(e.Fields))
	}
	return b.String()
}

func fieldsString(fields map[string]any) string {
	var b strings.Builder
	b.WriteString("{")
	first := true
	for k, v := range fields {
		if !first {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%s=%v", k, v)
		first = false
	}
	b.WriteString("}")
	return b.String()
}

// selectableLog is a read-only, drag-selectable, monospace multiline text area (Entry with
// edit keys suppressed; mouse selection + Ctrl+C/Ctrl+A still work via shortcuts).
type selectableLog struct {
	widget.Entry
}

func newSelectableLog() *selectableLog {
	e := &selectableLog{}
	e.MultiLine = true
	e.Wrapping = fyne.TextWrapOff
	e.TextStyle = fyne.TextStyle{Monospace: true}
	e.ExtendBaseWidget(e)
	return e
}

func (e *selectableLog) TypedRune(rune)          {} // block typing (read-only)
func (e *selectableLog) TypedKey(*fyne.KeyEvent) {} // block edit keys; copy/select are shortcuts

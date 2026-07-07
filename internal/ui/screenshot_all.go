package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
)

// ScreenshotAll is the whole-UI verification sweep behind `ctl screenshot-all`:
// selects every tab, captures a PNG of each (plus mid/bottom positions of the
// tab's main scroller), collects the snapshot walker's ⚠OVERFLOW findings, and
// writes everything + report.txt to dir. Restores the previously selected tab.
// Returns the report text. Call off the UI thread (ctl handler goroutine).
func (u *UI) ScreenshotAll(dir string) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("empty dir")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	var items []*container.TabItem
	var prev *container.TabItem
	fyne.DoAndWait(func() {
		if u.tabs == nil {
			return
		}
		prev = u.tabs.Selected()
		items = append(items, u.tabs.Items...)
	})
	if len(items) == 0 {
		return "", fmt.Errorf("no tabs (service mode?)")
	}

	var b strings.Builder
	shots, findings := 0, 0
	capture := func(path string) {
		if err := u.Screenshot(path); err != nil {
			fmt.Fprintf(&b, "FAIL %s: %v\n", filepath.Base(path), err)
			return
		}
		shots++
		fmt.Fprintf(&b, "shot %s\n", filepath.Base(path))
	}

	for i, it := range items {
		fyne.DoAndWait(func() { u.tabs.Select(it) })
		time.Sleep(350 * time.Millisecond) // async view fills settle
		base := filepath.Join(dir, fmt.Sprintf("%02d-%s", i, slugify(it.Text)))
		capture(base + ".png")

		// Scroll sweep: the tab's largest visible scroller, middle + bottom.
		var sc *container.Scroll
		var span float32
		fyne.DoAndWait(func() {
			if sc = biggestScroll(it.Content); sc != nil {
				span = sc.Content.MinSize().Height - sc.Size().Height
			}
		})
		if sc != nil && span > 40 {
			fyne.DoAndWait(func() { sc.Offset = fyne.NewPos(sc.Offset.X, span/2); sc.Refresh() })
			time.Sleep(150 * time.Millisecond)
			capture(base + "-mid.png")
			fyne.DoAndWait(func() { sc.ScrollToBottom() })
			time.Sleep(150 * time.Millisecond)
			capture(base + "-end.png")
			fyne.DoAndWait(func() { sc.ScrollToTop() })
		}

		// Layout findings for the selected tab (MinSize > Size = clipped content).
		for _, line := range strings.Split(u.Snapshot(), "\n") {
			if strings.Contains(line, "⚠OVERFLOW") {
				findings++
				fmt.Fprintf(&b, "OVERFLOW [%s] %s\n", it.Text, strings.TrimSpace(line))
			}
		}
	}
	if prev != nil {
		fyne.DoAndWait(func() { u.tabs.Select(prev) })
	}

	report := fmt.Sprintf("tabs=%d shots=%d overflow-findings=%d\n%s", len(items), shots, findings, b.String())
	if err := os.WriteFile(filepath.Join(dir, "report.txt"), []byte(report), 0o644); err != nil {
		return report, err
	}
	return report, nil
}

// biggestScroll returns the largest visible container.Scroll under obj (nil if none).
func biggestScroll(obj fyne.CanvasObject) *container.Scroll {
	var best *container.Scroll
	var bestArea float32
	var walk func(o fyne.CanvasObject)
	walk = func(o fyne.CanvasObject) {
		if o == nil || !o.Visible() {
			return
		}
		if sc, ok := o.(*container.Scroll); ok {
			s := sc.Size()
			if a := s.Width * s.Height; a > bestArea {
				best, bestArea = sc, a
			}
		}
		for _, ch := range childrenOf(o) {
			walk(ch)
		}
	}
	walk(obj)
	return best
}

// slugify makes a tab label filesystem-safe (lower, alnum + dashes).
func slugify(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			if n := b.Len(); n > 0 && b.String()[n-1] != '-' {
				b.WriteByte('-')
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

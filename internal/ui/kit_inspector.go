package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// kitSection is one titled, collapsible block in a kitInspector.
type kitSection struct {
	Key         string            // stable key → persisted open/closed state across rebuilds
	Title       string            // header label
	Help        string            // optional "?" education popup on the header (help.go)
	DefaultOpen bool              // seed state the first time this Key is seen
	Content     fyne.CanvasObject // nil → section skipped
}

// kitInspector is a titled, internally-scrolling right-hand panel of collapsible sections -
// the dense replacement for a long vertical stack of always-open option groups (which forced
// whole-page scrolling). Header = bold title + muted subtitle; body = disclosure sections
// whose open/closed state persists across SetSections calls (keyed by kitSection.Key), so a
// section the user expanded stays expanded across the frequent detail rebuilds. Reusable by
// any detail/side panel, not just the Library.
type kitInspector struct {
	caption *widget.Label
	title   *widget.Label
	sub     *widget.Label
	body    *fyne.Container
	root    fyne.CanvasObject
	open    map[string]bool
}

// newKitInspector builds an empty inspector. caption is the small all-caps eyebrow above the
// title (e.g. "INSPECTOR", "SELECTED").
func newKitInspector(caption string) *kitInspector {
	i := &kitInspector{open: map[string]bool{}}
	i.caption = smallCaps(caption)
	i.title = boldLabel("")
	i.title.Wrapping = fyne.TextWrapWord
	i.sub = mutedLabel("")
	i.body = container.NewVBox()
	head := container.NewVBox(i.caption, i.title, i.sub, widget.NewSeparator())
	scroll := container.NewVScroll(container.NewVBox(head, i.body))
	i.root = shrinkWidth(300, scroll) // shrinkWidth (layout_adaptive.go) caps the pane's min width
	return i
}

// Object returns the inspector's canvas object for placement in a split/border.
func (i *kitInspector) Object() fyne.CanvasObject { return i.root }

// SetHeader updates the title + subtitle line.
func (i *kitInspector) SetHeader(title, subtitle string) {
	i.title.SetText(title)
	i.sub.SetText(subtitle)
}

// SetSections rebuilds the body from sections (nil-content sections are skipped). Open state
// persists by Key.
func (i *kitInspector) SetSections(sections ...kitSection) {
	objs := make([]fyne.CanvasObject, 0, len(sections))
	for _, s := range sections {
		if s.Content == nil {
			continue
		}
		objs = append(objs, i.disclosure(s))
	}
	i.body.Objects = objs
	i.body.Refresh()
}

// disclosure wraps one section in a clickable header (title + chevron) with persisted state.
func (i *kitInspector) disclosure(s kitSection) fyne.CanvasObject {
	open, seen := i.open[s.Key]
	if !seen {
		open = s.DefaultOpen
	}
	content := s.Content
	chev := func(o bool) fyne.Resource {
		if o {
			return theme.MenuDropDownIcon()
		}
		return theme.MenuExpandIcon()
	}
	var hdr *widget.Button
	hdr = widget.NewButtonWithIcon(s.Title, chev(open), func() {
		now := !content.Visible()
		i.open[s.Key] = now
		if now {
			content.Show()
		} else {
			content.Hide()
		}
		hdr.SetIcon(chev(now))
	})
	hdr.Importance = widget.LowImportance
	hdr.Alignment = widget.ButtonAlignLeading
	if open {
		content.Show()
	} else {
		content.Hide()
	}
	var headRow fyne.CanvasObject = hdr
	if s.Help != "" {
		headRow = container.NewBorder(nil, nil, nil, helpIcon(s.Help), hdr)
	}
	return container.NewVBox(headRow, content)
}

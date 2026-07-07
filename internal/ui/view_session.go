package ui

// Merged-session Live cards (the old Session tab, folded into the Live cockpit per
// UI_WORKFLOW_IA.md): per-deck fused metadata with provenance ("decks" card) + the
// source-coverage matrix ("sources" card).

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/session"
)

// shortAgo renders a duration as a compact "3s" / "2m" / "1h".
func shortAgo(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
}

// buildDecksContent is the "decks" Live card: per-deck metadata fused from all enabled
// sources by per-field priority, with provenance tags. nil when the hub is absent.
func (u *UI) buildDecksContent() fyne.CanvasObject {
	if u.svc.Session == nil {
		return nil
	}
	decks := []string{"A", "B", "C", "D"}
	cards := make(map[string]*mergedDeckCard, len(decks))
	grid := container.NewGridWithColumns(2)
	for _, d := range decks {
		mc := newMergedDeckCard(d)
		cards[d] = mc
		grid.Add(mc.card)
	}
	apply := func(st session.UnifiedState) {
		for d, mc := range cards {
			mc.update(st.Decks[d])
		}
	}
	apply(u.svc.Session.Snapshot())

	ch, unsub := u.svc.Session.Subscribe()
	u.closers = append(u.closers, unsub)
	goUI("live-decks", func() {
		for range ch {
			snap := u.svc.Session.Snapshot()
			fyne.Do(func() { apply(snap) })
		}
	})
	return grid
}

// buildSourcesContent is the "sources" Live card: which connection methods are active
// and what each provides. nil when the hub is absent.
func (u *UI) buildSourcesContent() fyne.CanvasObject {
	if u.svc.Session == nil {
		return nil
	}
	coverage := widget.NewRichTextWithText("")
	coverage.Wrapping = fyne.TextWrapWord // wrap so the source list doesn't pin the window wide
	refresh := func() { coverage.ParseMarkdown(u.sourceCoverageMarkdown()) }
	refresh()

	ch, unsub := u.svc.Session.Subscribe()
	u.closers = append(u.closers, unsub)
	goUI("live-sources", func() {
		for range ch {
			fyne.Do(refresh)
		}
	})
	// Periodic refresh so "receiving Ns ago" counts up and a source going silent flips to
	// "idle" even when no merged update arrives.
	stop := make(chan struct{})
	u.closers = append(u.closers, func() { close(stop) })
	goUI("live-sources", func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				fyne.Do(refresh)
			}
		}
	})
	return coverage
}

// sourceCoverageMarkdown renders the source matrix: state, and provided fields per scope.
func (u *UI) sourceCoverageMarkdown() string {
	srcs := u.svc.Session.Sources()
	if len(srcs) == 0 {
		return "_No sources registered._"
	}
	var b strings.Builder
	for _, s := range srcs {
		state := "○ off"
		switch {
		case s.Receiving:
			state = fmt.Sprintf("● receiving (%s ago)", shortAgo(time.Since(s.LastSeen)))
		case s.Running && !s.LastSeen.IsZero():
			state = fmt.Sprintf("◍ connected, idle (last %s ago)", shortAgo(time.Since(s.LastSeen)))
		case s.Running:
			state = "◌ connected, no data yet"
		case s.Enabled:
			state = "○ enabled (not started)"
		}
		fmt.Fprintf(&b, "**%s** - %s\n\n", s.ID, state)
		for _, c := range s.Capabilities {
			scope := string(c.Scope)
			if len(c.IDs) > 0 {
				scope += " " + strings.Join(c.IDs, "/")
			}
			fmt.Fprintf(&b, "- %s: %s\n", scope, strings.Join(c.Fields, ", "))
		}
		b.WriteString("\n")
	}
	return b.String()
}

type mergedDeckCard struct {
	card   *widget.Card
	title  *widget.Label
	artist *widget.Label
	meta   *widget.Label
	prov   *widget.Label // provenance: which sources contributed
}

func newMergedDeckCard(deck string) *mergedDeckCard {
	title := widget.NewLabelWithStyle("-", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	title.Truncation = fyne.TextTruncateEllipsis
	artist := mutedLabel("-")
	meta := mutedLabel("")
	prov := mutedLabel("")
	prov.TextStyle = fyne.TextStyle{Italic: true}
	c := widget.NewCard("Deck "+deck, "", container.NewVBox(title, artist, meta, prov))
	return &mergedDeckCard{card: c, title: title, artist: artist, meta: meta, prov: prov}
}

func (mc *mergedDeckCard) update(fields map[string]session.FieldValue) {
	val := func(f string) any {
		if fv, ok := fields[f]; ok {
			return fv.Value
		}
		return nil
	}
	mc.title.SetText(strOr(val(session.FieldTitle), "-"))
	mc.artist.SetText(strOr(val(session.FieldArtist), "-"))

	var parts []string
	if bpm := numOr(val(session.FieldBPM)); bpm != "" {
		parts = append(parts, bpm+" BPM")
	}
	if key := strOr(val(session.FieldKey), ""); key != "" {
		parts = append(parts, key)
	}
	if b, ok := val(session.FieldIsPlaying).(bool); ok && b {
		parts = append(parts, "▶ playing")
	}
	mc.meta.SetText(strings.Join(parts, " · "))

	// Provenance: distinct winning sources for this deck.
	seen := map[string]struct{}{}
	for _, fv := range fields {
		if fv.Source != "" {
			seen[fv.Source] = struct{}{}
		}
	}
	srcs := make([]string, 0, len(seen))
	for s := range seen {
		srcs = append(srcs, s)
	}
	sort.Strings(srcs)
	if len(srcs) > 0 {
		mc.prov.SetText("via " + strings.Join(srcs, ", "))
	} else {
		mc.prov.SetText("")
	}
}

// strOr returns v as a non-empty string, else fallback.
func strOr(v any, fallback string) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return fallback
}

// numOr renders a numeric field (float/int/string) compactly, "" if absent.
func numOr(v any) string {
	switch n := v.(type) {
	case float64:
		return fmt.Sprintf("%.1f", n)
	case int:
		return fmt.Sprintf("%d", n)
	case string:
		return n
	default:
		return ""
	}
}

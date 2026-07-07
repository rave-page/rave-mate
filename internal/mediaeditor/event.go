package mediaeditor

import (
	"context"
	"image/color"
	"strings"
)

// EventData holds the event fields needed to build a poster.
type EventData struct {
	Title    string
	Date     string
	DJs      []string
	LogoPath string
}

// APISource fetches upcoming events from the rave.page API.
// Implement against api.Client (see INTEGRATION.md for endpoint details).
type APISource interface {
	UpcomingEvents(ctx context.Context) ([]EventData, error)
}

// PosterFromEvent builds a Poster from event data and a template.
// size selects which Template preset to use (index into Templates());
// 0 = social poster (1080×1350), 1 = thumbnail (1280×720).
func PosterFromEvent(e EventData, sizeIdx int) Poster {
	tpls := Templates()
	t := tpls[0]
	if sizeIdx >= 0 && sizeIdx < len(tpls) {
		t = tpls[sizeIdx]
	}

	lines := make([]string, 0, len(e.DJs)+1)
	if e.Date != "" {
		lines = append(lines, e.Date)
	}
	for _, dj := range e.DJs {
		if dj = strings.TrimSpace(dj); dj != "" {
			lines = append(lines, dj)
		}
	}

	return Poster{
		Width:      t.Width,
		Height:     t.Height,
		Background: t.DefaultBg,
		Title:      e.Title,
		Lines:      lines,
		LogoPath:   e.LogoPath,
	}
}

// Template is a named canvas preset.
type Template struct {
	Name      string
	Width     int
	Height    int
	DefaultBg color.Color
}

// Templates returns the built-in presets.
func Templates() []Template {
	return []Template{
		{
			Name:      "Social poster (1080×1350)",
			Width:     1080,
			Height:    1350,
			DefaultBg: color.NRGBA{R: 0x0a, G: 0x0a, B: 0x0a, A: 0xff},
		},
		{
			Name:      "Thumbnail (1280×720)",
			Width:     1280,
			Height:    720,
			DefaultBg: color.NRGBA{R: 0x14, G: 0x14, B: 0x17, A: 0xff},
		},
	}
}

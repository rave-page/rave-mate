package recorder

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Track is one confirmed-played track in a recording, with absolute start/end times.
type Track struct {
	Title       string    `json:"title"`
	Artist      string    `json:"artist"`
	Album       string    `json:"album,omitempty"`
	Key         string    `json:"key,omitempty"`
	BPM         float64   `json:"bpm,omitempty"`
	Deck        string    `json:"deck,omitempty"`
	Path        string    `json:"path,omitempty"` // local file path (deck/NML-reported or history reconcile) - library identity
	StartedAt   time.Time `json:"startedAt"`
	EndedAt     time.Time `json:"endedAt,omitzero"`
	TitleSource string    `json:"titleSource,omitempty"` // provenance of the title/artist
}

// OnAirEvent is one measured per-deck on-air threshold crossing (fader up/down while the
// deck plays), stored with the recording so start times stay reconstructable after the fact.
type OnAirEvent struct {
	Deck string    `json:"deck"`
	Key  string    `json:"key"` // track identity on the deck at the crossing ("title|artist", lowered)
	At   time.Time `json:"at"`
	Up   bool      `json:"up"`
}

// Recording is one captured live session: a named, time-bounded tracklist, optionally
// linked to a live stream.
type Recording struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	StreamID     string    `json:"streamId,omitempty"`
	StartedAt    time.Time `json:"startedAt"`
	EndedAt      time.Time `json:"endedAt,omitzero"`
	Tracks       []Track   `json:"tracks"`
	ReconciledAt time.Time `json:"reconciledAt,omitzero"` // when matched to the authoritative Traktor history
	// LastFaderAt is the last instant ANY channel fader sat above the on-air threshold (fed by MIDI
	// fader CC + Traktor on-air level). When the DJ pulls the final fader down this stops advancing -
	// the true set end, more accurate than the last track's end when the DJ talks after the mix. Zero
	// when no fader data was ever seen (no MIDI controller / Traktor level feed).
	LastFaderAt time.Time `json:"lastFaderAt,omitzero"`
	// OnAirLog records measured per-deck on-air crossings during the set (cap onAirLogCap,
	// stop-appending: the opening crossings are the reconstruction-critical ones). Fed by
	// markOnAirLocked; consumed by PlanFaderFix.
	OnAirLog []OnAirEvent `json:"onAirLog,omitempty"`
}

// clone returns a deep copy safe to hand to subscribers/UI.
func (r *Recording) clone() *Recording {
	if r == nil {
		return nil
	}
	cp := *r
	cp.Tracks = append([]Track(nil), r.Tracks...)
	return &cp
}

// offset formats a track's start time relative to the recording start as mm:ss (or h:mm:ss).
func (r Recording) offset(t time.Time) string {
	d := max(t.Sub(r.StartedAt), 0)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

// Export formats.
const (
	FormatText = "txt"
	FormatCSV  = "csv"
	FormatJSON = "json"
)

// Export renders the tracklist in the given format (FormatText/CSV/JSON).
func (r Recording) Export(format string) (string, error) {
	switch format {
	case FormatJSON:
		raw, err := json.MarshalIndent(r, "", "  ")
		return string(raw), err
	case FormatCSV:
		var b strings.Builder
		w := csv.NewWriter(&b)
		_ = w.Write([]string{"#", "offset", "artist", "title", "bpm", "key", "deck"})
		for i, t := range r.Tracks {
			bpm := ""
			if t.BPM > 0 {
				bpm = fmt.Sprintf("%.1f", t.BPM)
			}
			_ = w.Write([]string{fmt.Sprintf("%d", i+1), r.offset(t.StartedAt), t.Artist, t.Title, bpm, t.Key, t.Deck})
		}
		w.Flush()
		return b.String(), w.Error()
	default: // FormatText
		return r.ExportText(DefaultTextOptions()), nil
	}
}

// TextOptions controls the text export: a per-track line template + optional header block.
// Line placeholders: {n} {nn} {offset} {artist} {title} {track} {album} {key} {bpm} {deck};
// header placeholders: {name} {date} {count}. {track} = "Artist - Title" (title alone when
// the artist is unknown); {nn} zero-pads to the track-count width.
type TextOptions struct {
	Line   string
	Header string // "" = no header block
}

// DefaultTextOptions reproduces the classic text export.
func DefaultTextOptions() TextOptions {
	return TextOptions{Line: "{n}. [{offset}] {track}", Header: "{name} - {date}"}
}

// ExportText renders the tracklist with opts (an empty Line falls back to the default).
func (r Recording) ExportText(opts TextOptions) string {
	if strings.TrimSpace(opts.Line) == "" {
		opts.Line = DefaultTextOptions().Line
	}
	name := r.Name
	if name == "" {
		name = "Live set"
	}
	var b strings.Builder
	if h := strings.TrimSpace(opts.Header); h != "" {
		b.WriteString(strings.NewReplacer(
			"{name}", name,
			"{date}", r.StartedAt.Local().Format("2006-01-02 15:04"),
			"{count}", fmt.Sprint(len(r.Tracks)),
		).Replace(h))
		b.WriteString("\n\n")
	}
	pad := max(2, len(fmt.Sprint(len(r.Tracks))))
	for i, t := range r.Tracks {
		track := t.Title
		if t.Artist != "" {
			track = t.Artist + " - " + t.Title
		}
		bpm := ""
		if t.BPM > 0 {
			bpm = strings.TrimSuffix(fmt.Sprintf("%.1f", t.BPM), ".0")
		}
		b.WriteString(strings.NewReplacer(
			"{n}", fmt.Sprint(i+1),
			"{nn}", fmt.Sprintf("%0*d", pad, i+1),
			"{offset}", r.offset(t.StartedAt),
			"{artist}", t.Artist,
			"{title}", t.Title,
			"{track}", track,
			"{album}", t.Album,
			"{key}", t.Key,
			"{bpm}", bpm,
			"{deck}", t.Deck,
		).Replace(opts.Line))
		b.WriteByte('\n')
	}
	return b.String()
}

// ParseClock parses "h:mm:ss", "m:ss" or plain seconds into a duration (offset edits).
func ParseClock(s string) (time.Duration, error) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) == 0 || len(parts) > 3 {
		return 0, fmt.Errorf("bad time %q", s)
	}
	total := 0
	for _, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil || n < 0 {
			return 0, fmt.Errorf("bad time %q", s)
		}
		total = total*60 + n
	}
	return time.Duration(total) * time.Second, nil
}

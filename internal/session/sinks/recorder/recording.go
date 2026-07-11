package recorder

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
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
		var b strings.Builder
		name := r.Name
		if name == "" {
			name = "Live set"
		}
		fmt.Fprintf(&b, "%s - %s\n\n", name, r.StartedAt.Local().Format("2006-01-02 15:04"))
		for i, t := range r.Tracks {
			artist := t.Artist
			if artist != "" {
				artist += " - "
			}
			fmt.Fprintf(&b, "%d. [%s] %s%s\n", i+1, r.offset(t.StartedAt), artist, t.Title)
		}
		return b.String(), nil
	}
}

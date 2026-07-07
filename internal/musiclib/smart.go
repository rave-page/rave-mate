package musiclib

import (
	"fmt"
	"strings"
)

// SmartRules filters the loaded collection live - a rave-mate smart playlist. Zero fields
// don't constrain. Genre matching is case-insensitive substring per selected genre (so
// "Techno" also catches "Hard Techno"); Search spans title/artist/album/label/comment.
type SmartRules struct {
	Genres       []string `json:"genres,omitempty"`
	BPMMin       float64  `json:"bpmMin,omitempty"`
	BPMMax       float64  `json:"bpmMax,omitempty"`
	KeyContains  string   `json:"keyContains,omitempty"`
	RatingMin    int      `json:"ratingMin,omitempty"`
	PlayCountMin int      `json:"playCountMin,omitempty"`
	Search       string   `json:"search,omitempty"`
}

// Empty reports whether no rule constrains (matches everything).
func (r SmartRules) Empty() bool {
	return len(r.Genres) == 0 && r.BPMMin == 0 && r.BPMMax == 0 &&
		r.KeyContains == "" && r.RatingMin == 0 && r.PlayCountMin == 0 && r.Search == ""
}

// Match reports whether t satisfies every set rule (AND across rules, OR within genres).
func (r SmartRules) Match(t Track) bool {
	if len(r.Genres) > 0 {
		g := strings.ToLower(t.Genre)
		hit := false
		for _, want := range r.Genres {
			if want != "" && strings.Contains(g, strings.ToLower(want)) {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	if r.BPMMin > 0 && t.BPM < r.BPMMin {
		return false
	}
	if r.BPMMax > 0 && t.BPM > r.BPMMax {
		return false
	}
	if r.KeyContains != "" && !strings.Contains(strings.ToLower(t.Key), strings.ToLower(r.KeyContains)) {
		return false
	}
	if r.RatingMin > 0 && StarRating(t.Rating) < r.RatingMin {
		return false
	}
	if r.PlayCountMin > 0 && t.PlayCount < r.PlayCountMin {
		return false
	}
	if q := strings.ToLower(strings.TrimSpace(r.Search)); q != "" {
		hay := strings.ToLower(t.Title + " " + t.Artist + " " + t.Album + " " + t.Label + " " + t.Comment)
		if !strings.Contains(hay, q) {
			return false
		}
	}
	return true
}

// StarRating normalizes a source rating to 0–5 stars (Traktor RANKING is 0–255, 51/star;
// Rekordbox/VirtualDJ already 0–5).
func StarRating(r int) int {
	if r > 5 {
		r = (r + 25) / 51
	}
	if r > 5 {
		r = 5
	}
	if r < 0 {
		r = 0
	}
	return r
}

// FilterSmart returns the tracks matching r, in input order.
func FilterSmart(tracks []Track, r SmartRules) []Track {
	var out []Track
	for _, t := range tracks {
		if r.Match(t) {
			out = append(out, t)
		}
	}
	return out
}

// Describe renders a terse human summary of the rules ("Genre: Techno · BPM 138–150 · ★≥4").
func (r SmartRules) Describe() string {
	var parts []string
	if len(r.Genres) > 0 {
		parts = append(parts, "Genre: "+strings.Join(r.Genres, ", "))
	}
	switch {
	case r.BPMMin > 0 && r.BPMMax > 0:
		parts = append(parts, fmt.Sprintf("BPM %.0f–%.0f", r.BPMMin, r.BPMMax))
	case r.BPMMin > 0:
		parts = append(parts, fmt.Sprintf("BPM ≥%.0f", r.BPMMin))
	case r.BPMMax > 0:
		parts = append(parts, fmt.Sprintf("BPM ≤%.0f", r.BPMMax))
	}
	if r.KeyContains != "" {
		parts = append(parts, "Key ~ "+r.KeyContains)
	}
	if r.RatingMin > 0 {
		parts = append(parts, fmt.Sprintf("★≥%d", r.RatingMin))
	}
	if r.PlayCountMin > 0 {
		parts = append(parts, fmt.Sprintf("played ≥%d×", r.PlayCountMin))
	}
	if r.Search != "" {
		parts = append(parts, "“"+r.Search+"”")
	}
	if len(parts) == 0 {
		return "All tracks (no rules yet)"
	}
	return strings.Join(parts, " · ")
}

// FeelPreset is a quick mood/energy starting point for a smart playlist - BPM is the best
// energy proxy a DJ library carries without audio analysis.
type FeelPreset struct {
	Label  string
	BPMMin float64
	BPMMax float64
}

// FeelPresets returns the feel → BPM-band presets offered in the smart playlist editor.
func FeelPresets() []FeelPreset {
	return []FeelPreset{
		{"Chill / downtempo", 0, 115},
		{"Groovy / house", 118, 128},
		{"Driving / melodic", 126, 138},
		{"Peak time", 138, 150},
		{"Hard / fast", 150, 200},
	}
}

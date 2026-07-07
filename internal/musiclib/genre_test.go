package musiclib

import "testing"

func TestGenreFamily(t *testing.T) {
	cases := map[string]string{
		"Neurofunk":      "Drum & Bass",
		"Liquid DnB":     "Drum & Bass",
		"Drum & Bass":    "Drum & Bass",
		"Jump Up":        "Drum & Bass",
		"Jungle":         "Drum & Bass",
		"Dubstep":        "Dubstep / Bass",
		"Riddim":         "Dubstep / Bass",
		"Bassline":       "Dubstep / Bass",
		"Tech House":     "House",
		"Deep House":     "House",
		"Techno":         "Techno",
		"Psytrance":      "Trance",
		"Happy Hardcore": "Hard Dance",
		"Hardstyle":      "Hard Dance",
		"Hip-Hop":        "Hip-Hop",
		"":               "",
		"Polka":          "Polka", // unknown keeps its own text
	}
	for in, want := range cases {
		if got := GenreFamily(in); got != want {
			t.Errorf("GenreFamily(%q) = %q, want %q", in, got, want)
		}
	}
}

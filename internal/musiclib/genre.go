package musiclib

import "strings"

// genreFamilies maps a keyword (matched as a lowercased substring) to a canonical family, so
// related sub-genres cluster when grouping/sorting ("Neurofunk", "Liquid DnB", "Jump Up" all →
// "Drum & Bass"). First match wins; order most-specific-first where keys overlap (e.g. "drum"
// before "bass" so "drum & bass" → DnB, not Bass; "dubstep" before "dub").
var genreFamilies = []struct{ key, fam string }{
	{"drum", "Drum & Bass"}, {"dnb", "Drum & Bass"}, {"d&b", "Drum & Bass"},
	{"d & b", "Drum & Bass"}, {"neuro", "Drum & Bass"}, {"liquid", "Drum & Bass"},
	{"jungle", "Drum & Bass"}, {"jump up", "Drum & Bass"}, {"jump-up", "Drum & Bass"},
	{"dubstep", "Dubstep / Bass"}, {"riddim", "Dubstep / Bass"}, {"bassline", "Dubstep / Bass"},
	{"breakbeat", "Breaks"}, {"breaks", "Breaks"},
	{"trap", "Trap"},
	{"happy hard", "Hard Dance"}, {"hardstyle", "Hard Dance"}, {"hardcore", "Hard Dance"},
	{"gabber", "Hard Dance"}, {"frenchcore", "Hard Dance"}, {"uptempo", "Hard Dance"},
	{"techno", "Techno"},
	{"tech house", "House"}, {"deep house", "House"}, {"progressive house", "House"},
	{"acid", "House"}, {"disco", "House"}, {"house", "House"},
	{"psytrance", "Trance"}, {"psy", "Trance"}, {"trance", "Trance"},
	{"electro", "Electro"}, {"dub", "Dub / Reggae"}, {"reggae", "Dub / Reggae"},
	{"dancehall", "Dub / Reggae"}, {"hip hop", "Hip-Hop"}, {"hip-hop", "Hip-Hop"},
	{"hiphop", "Hip-Hop"}, {"rap", "Hip-Hop"}, {"grime", "Hip-Hop"},
	{"bass", "Dubstep / Bass"}, // broad fallback AFTER drum&bass/house/etc
	{"pop", "Pop"}, {"rock", "Rock"}, {"metal", "Rock"}, {"ambient", "Ambient"}, {"chill", "Ambient"},
}

// GenreFamily clusters a free-text genre into a broad family for "related genre" grouping/sort.
// Unknown genres keep their own (trimmed) text so identical genres still group; empty → "".
func GenreFamily(genre string) string {
	g := strings.ToLower(strings.TrimSpace(genre))
	if g == "" {
		return ""
	}
	for _, gf := range genreFamilies {
		if strings.Contains(g, gf.key) {
			return gf.fam
		}
	}
	return strings.TrimSpace(genre)
}

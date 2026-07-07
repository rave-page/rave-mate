package musiclib

import "strings"

// Harmonic key model: every notation a DJ library throws at us (musical "Ebm",
// Camelot "8A", Open Key "8m", Traktor MUSICAL_KEY 0-23) normalizes to a Camelot
// wheel position - Num 1-12 + Minor. Camelot is the mixing-compatibility space:
// same slot, ±1 same ring, and the relative slot (other ring) are harmonic.

// Key is a normalized Camelot wheel position.
type Key struct {
	Num   int  // 1-12 (Camelot number)
	Minor bool // true = A ring (minor), false = B ring (major)
}

// camelotMajor[pc] = Camelot number for major key with pitch class pc (C=0).
var camelotMajor = [12]int{8, 3, 10, 5, 12, 7, 2, 9, 4, 11, 6, 1}

// Conventional display names per Camelot slot (index Num-1).
var (
	minorNames = [12]string{"Abm", "Ebm", "Bbm", "Fm", "Cm", "Gm", "Dm", "Am", "Em", "Bm", "F#m", "C#m"}
	majorNames = [12]string{"B", "F#", "Db", "Ab", "Eb", "Bb", "F", "C", "G", "D", "A", "E"}
)

// Camelot renders "8A" / "8B".
func (k Key) Camelot() string {
	if k.Num < 1 || k.Num > 12 {
		return ""
	}
	ring := "B"
	if k.Minor {
		ring = "A"
	}
	return itoaKey(k.Num) + ring
}

// Name renders the conventional musical name ("Am", "F#").
func (k Key) Name() string {
	if k.Num < 1 || k.Num > 12 {
		return ""
	}
	if k.Minor {
		return minorNames[k.Num-1]
	}
	return majorNames[k.Num-1]
}

// KeyRel classifies harmonic compatibility between two keys (Camelot rules).
type KeyRel int

const (
	RelNone     KeyRel = iota // dissonant - no standard harmonic move
	RelSame                   // identical key
	RelRelative               // same number, other ring (relative major/minor)
	RelUp                     // +1 same ring (energy up)
	RelDown                   // -1 same ring (energy down)
)

// KeyRelation classifies k against reference ref.
func KeyRelation(ref, k Key) KeyRel {
	if ref.Num < 1 || k.Num < 1 {
		return RelNone
	}
	switch {
	case ref == k:
		return RelSame
	case ref.Num == k.Num:
		return RelRelative
	case ref.Minor != k.Minor:
		return RelNone
	case k.Num == ref.Num%12+1:
		return RelUp
	case ref.Num == k.Num%12+1:
		return RelDown
	}
	return RelNone
}

// ParseKey normalizes any common key notation. Accepted: Camelot ("8A"),
// Open Key ("8m"/"8d"), musical ("Am", "C#", "Eb min", "F♯ minor", "Cmaj").
func ParseKey(s string) (Key, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Key{}, false
	}
	if s[0] >= '0' && s[0] <= '9' {
		return parseWheelKey(s)
	}
	return parseMusicalKey(s)
}

// parseWheelKey handles "8A" (Camelot) and "8d"/"8m" (Open Key).
func parseWheelKey(s string) (Key, bool) {
	i := 0
	n := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		n = n*10 + int(s[i]-'0')
		i++
	}
	if n < 1 || n > 12 || i != len(s)-1 {
		return Key{}, false
	}
	switch s[i] {
	case 'A', 'a':
		return Key{Num: n, Minor: true}, true
	case 'B', 'b':
		return Key{Num: n, Minor: false}, true
	case 'm', 'M': // Open Key moll → Camelot num = open+7 (wrapped)
		return Key{Num: (n+6)%12 + 1, Minor: true}, true
	case 'd', 'D': // Open Key dur
		return Key{Num: (n+6)%12 + 1, Minor: false}, true
	}
	return Key{}, false
}

// parseMusicalKey handles note names: letter + accidental + mode suffix.
func parseMusicalKey(s string) (Key, bool) {
	low := strings.ToLower(s)
	pcBase := map[byte]int{'c': 0, 'd': 2, 'e': 4, 'f': 5, 'g': 7, 'a': 9, 'b': 11}
	pc, ok := pcBase[low[0]]
	if !ok {
		return Key{}, false
	}
	rest := low[1:]
	// accidental (#/♯ raise, b/♭ lower - bare "b" counts as flat only when the
	// remainder is a mode suffix AND "b"+remainder isn't one itself, so "bm" =
	// B minor but "bbm" = Bb minor)
	switch {
	case strings.HasPrefix(rest, "#"), strings.HasPrefix(rest, "♯"):
		pc++
		rest = strings.TrimPrefix(strings.TrimPrefix(rest, "#"), "♯")
	case strings.HasPrefix(rest, "♭"):
		pc--
		rest = strings.TrimPrefix(rest, "♭")
	case strings.HasPrefix(rest, "b") && isModeOrEmpty(rest[1:]) && !isModeOrEmpty(rest):
		pc--
		rest = rest[1:]
	}
	pc = ((pc % 12) + 12) % 12
	rest = strings.TrimSpace(strings.TrimLeft(rest, " -_"))
	minor, ok := parseMode(rest)
	if !ok {
		return Key{}, false
	}
	if minor {
		pc = (pc + 3) % 12 // relative major shares the Camelot number
	}
	return Key{Num: camelotMajor[pc], Minor: minor}, true
}

// isModeOrEmpty reports whether s alone is a valid mode suffix.
func isModeOrEmpty(s string) bool {
	_, ok := parseMode(strings.TrimSpace(strings.TrimLeft(s, " -_")))
	return ok
}

// parseMode maps a mode suffix to minor-ness ("" = major).
func parseMode(s string) (minor, ok bool) {
	switch s {
	case "", "maj", "major", "dur":
		return false, true
	case "m", "min", "minor", "moll":
		return true, true
	}
	return false, false
}

// KeyFromTraktorValue maps Traktor MUSICAL_KEY VALUE (0-11 major C..B, 12-23 minor Cm..Bm).
func KeyFromTraktorValue(v int) (Key, bool) {
	if v < 0 || v > 23 {
		return Key{}, false
	}
	pc := v % 12
	minor := v >= 12
	if minor {
		pc = (pc + 3) % 12
	}
	return Key{Num: camelotMajor[pc], Minor: minor}, true
}

func itoaKey(n int) string {
	if n >= 10 {
		return string([]byte{'1', byte('0' + n - 10)})
	}
	return string([]byte{byte('0' + n)})
}

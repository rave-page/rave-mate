package musiclib

import "testing"

func TestParseKey(t *testing.T) {
	cases := []struct {
		in   string
		want string // expected Camelot, "" = parse failure
	}{
		// Camelot passthrough
		{"8A", "8A"}, {"12b", "12B"}, {"1a", "1A"}, {"13A", ""}, {"0B", ""},
		// Open Key (1d=C=8B, 1m=Am=8A, 5d=E=12B, 6m=Dm=7A)
		{"1d", "8B"}, {"1m", "8A"}, {"5d", "12B"}, {"6m", "1A"}, {"12m", "7A"},
		// musical major
		{"C", "8B"}, {"G", "9B"}, {"F#", "2B"}, {"Gb", "2B"}, {"Db", "3B"}, {"B", "1B"}, {"F", "7B"},
		// musical minor (all suffix forms)
		{"Am", "8A"}, {"Ebm", "2A"}, {"D#m", "2A"}, {"F#m", "11A"}, {"Bm", "10A"},
		{"Bbm", "3A"}, {"A min", "8A"}, {"c minor", "5A"}, {"E♭m", "2A"}, {"f♯ minor", "11A"},
		{"Cmaj", "8B"}, {"a-moll", "8A"},
		// garbage
		{"", ""}, {"Hm", ""}, {"8", ""}, {"x9", ""}, {"Am7", ""},
	}
	for _, c := range cases {
		k, ok := ParseKey(c.in)
		if c.want == "" {
			if ok {
				t.Errorf("ParseKey(%q) ok, want failure (got %s)", c.in, k.Camelot())
			}
			continue
		}
		if !ok || k.Camelot() != c.want {
			t.Errorf("ParseKey(%q) = %v %s, want %s", c.in, ok, k.Camelot(), c.want)
		}
	}
}

func TestKeyNames(t *testing.T) {
	for _, c := range []struct{ camelot, name string }{
		{"8A", "Am"}, {"8B", "C"}, {"11A", "F#m"}, {"2A", "Ebm"}, {"12B", "E"}, {"1B", "B"},
	} {
		k, ok := ParseKey(c.camelot)
		if !ok || k.Name() != c.name {
			t.Errorf("%s → Name() = %q, want %q", c.camelot, k.Name(), c.name)
		}
	}
}

func TestKeyRelation(t *testing.T) {
	ref, _ := ParseKey("8A") // Am
	for _, c := range []struct {
		other string
		want  KeyRel
	}{
		{"8A", RelSame}, {"8B", RelRelative}, {"9A", RelUp}, {"7A", RelDown},
		{"9B", RelNone}, {"7B", RelNone}, {"10A", RelNone}, {"2B", RelNone},
	} {
		k, _ := ParseKey(c.other)
		if got := KeyRelation(ref, k); got != c.want {
			t.Errorf("KeyRelation(8A, %s) = %d, want %d", c.other, got, c.want)
		}
	}
	// wheel wrap: 12A+1 = 1A, 1A-1 = 12A
	a12, _ := ParseKey("12A")
	a1, _ := ParseKey("1A")
	if KeyRelation(a12, a1) != RelUp || KeyRelation(a1, a12) != RelDown {
		t.Errorf("wheel wrap broken: 12A↔1A")
	}
	if KeyRelation(Key{}, ref) != RelNone || KeyRelation(ref, Key{}) != RelNone {
		t.Errorf("zero keys must be RelNone")
	}
}

func TestKeyFromTraktorValue(t *testing.T) {
	for _, c := range []struct {
		v    int
		want string
	}{
		{0, "8B"} /*C*/, {7, "9B"} /*G*/, {21, "8A"} /*Am*/, {12, "5A"} /*Cm*/, {16, "9A"}, /*Em*/
	} {
		k, ok := KeyFromTraktorValue(c.v)
		if !ok || k.Camelot() != c.want {
			t.Errorf("KeyFromTraktorValue(%d) = %s, want %s", c.v, k.Camelot(), c.want)
		}
	}
	if _, ok := KeyFromTraktorValue(24); ok {
		t.Errorf("24 must fail")
	}
	if _, ok := KeyFromTraktorValue(-1); ok {
		t.Errorf("-1 must fail")
	}
}

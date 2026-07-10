package tagfix

import "testing"

// rs builds a string from code points - fixtures stay byte-exact without non-ASCII
// literals in source (raw C1 chars would themselves be mojibake bait).
func rs(cps ...rune) string { return string(cps) }

func TestRepairMojibake(t *testing.T) {
	tests := []struct {
		name, in, want string
		ok             bool
	}{
		{"ascii clean", "Plain Title 123", "", false},
		// "Caf" + A-tilde(0xC3) + copyright(0xA9): latin1-mislabeled UTF-8 of e-acute.
		{"latin1-mislabeled utf8", "Caf" + rs(0xC3, 0xA9), "Caf" + rs(0xE9), true},
		// "Mot" + A-tilde + 0xB6 + "rhead" -> o-umlaut.
		{"umlaut", "Mot" + rs(0xC3, 0xB6) + "rhead", "Mot" + rs(0xF6) + "rhead", true},
		// a-circ(0xE2) + euro(U+20AC=cp1252 0x80) + tm(U+2122=cp1252 0x99): bytes E2 80 99
		// = right single quote U+2019.
		{"cp1252 double-encoded quote", "It" + rs(0xE2, 0x20AC, 0x2122) + "s", "It" + rs(0x2019) + "s", true},
		// Twice-double-encoded e-acute: A-tilde, f-hook(U+0192=cp1252 0x83), A-circ(0xC2),
		// copyright -> two repair rounds.
		{"double-double encoded", rs(0xC3, 0x192, 0xC2, 0xA9), rs(0xE9), true},
		// Genuine latin1-range text must never be flagged.
		{"genuine latin1 text", "St" + rs(0xF6) + "rung", "", false},
		{"genuine diaeresis", "na" + rs(0xEF) + "ve", "", false},
		{"genuine trademark", "Sound" + rs(0x2122), "", false},
		// C1 control 0x92 = cp1252 right single quote.
		{"c1 curly quote", "It" + rs(0x92) + "s fine", "It" + rs(0x2019) + "s fine", true},
		// C1 control 0x96 = cp1252 en dash.
		{"c1 dash", "A " + rs(0x96) + " B", "A " + rs(0x2013) + " B", true},
		// 0x81 undefined in cp1252: ambiguous, no repair.
		{"c1 undefined byte ambiguous", "bad " + rs(0x81) + " text", "", false},
		// Already-clean unicode punctuation stays.
		{"clean mixed unicode", "Caf" + rs(0xE9) + " " + rs(0x2013) + " na" + rs(0xEF) + "ve", "", false},
	}
	for _, tc := range tests {
		got, detail, ok := repairMojibake(tc.in)
		if ok != tc.ok {
			t.Errorf("%s: ok = %v, want %v (got %q, detail %q)", tc.name, ok, tc.ok, got, detail)
			continue
		}
		if ok && got != tc.want {
			t.Errorf("%s: repaired %q, want %q", tc.name, got, tc.want)
		}
		if ok && detail == "" {
			t.Errorf("%s: empty detail", tc.name)
		}
	}
}

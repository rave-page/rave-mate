package ui

import "testing"

func TestWrapHelp(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"short line", "short line"},
		{"one two three four five", "one two three\nfour five"},
		{"para one\npara two words here", "para one\npara two\nwords here"}, // balanced: no orphan "here"
		{"supercalifragilistic word", "supercalifragilistic\nword"},         // over-cols word gets own line
	}
	for _, c := range cases {
		if got := wrapHelp(c.in, 14); got != c.want {
			t.Errorf("wrapHelp(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestWrapHelpMaxCols(t *testing.T) {
	got := wrapHelp("CPU % of one core. 100% = one full core busy. Sustained >150% while idle usually means a runaway loop.", helpWrapCols)
	for _, line := range splitLines(got) {
		if len(line) > helpWrapCols {
			t.Errorf("line %q exceeds %d cols", line, helpWrapCols)
		}
	}
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

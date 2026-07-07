package automation

import (
	"testing"
	"time"
)

func TestParseCronErrors(t *testing.T) {
	bad := []string{"", "* * *", "* * * * * *", "60 * * * *", "* 24 * * *", "0 0 0 * *", "* * * 13 *", "*/0 * * * *", "5-1 * * * *", "a * * * *"}
	for _, e := range bad {
		if _, err := parseCron(e); err == nil {
			t.Errorf("parseCron(%q) = nil err, want error", e)
		}
	}
}

func TestCronMatches(t *testing.T) {
	at := func(s string) time.Time {
		tm, err := time.ParseInLocation("2006-01-02 15:04", s, time.Local)
		if err != nil {
			t.Fatal(err)
		}
		return tm
	}
	cases := []struct {
		expr string
		when string
		want bool
	}{
		{"*/15 * * * *", "2026-06-05 10:30", true},
		{"*/15 * * * *", "2026-06-05 10:31", false},
		{"0 9 * * 1-5", "2026-06-05 09:00", true},  // 2026-06-05 is a Friday
		{"0 9 * * 1-5", "2026-06-06 09:00", false}, // Saturday
		{"0 9 * * 6", "2026-06-06 09:00", true},    // Saturday
		{"0 9 * * 0", "2026-06-07 09:00", true},    // Sunday via 0
		{"0 9 * * 7", "2026-06-07 09:00", true},    // Sunday via 7
		{"30 2 1 * *", "2026-07-01 02:30", true},   // 1st of month
		{"30 2 1 * *", "2026-07-02 02:30", false},  // wrong day
		{"0 0 13 * 5", "2026-06-13 00:00", true},   // 13th (Sat) - dom OR dow
		{"0 0 13 * 5", "2026-06-12 00:00", true},   // Friday - dom OR dow
		{"0 0 13 * 5", "2026-06-14 00:00", false},  // neither
		{"0 12 * 6 *", "2026-06-15 12:00", true},   // June only
		{"0 12 * 6 *", "2026-07-15 12:00", false},  // not June
	}
	for _, c := range cases {
		ce, err := parseCron(c.expr)
		if err != nil {
			t.Fatalf("parseCron(%q): %v", c.expr, err)
		}
		if got := ce.matches(at(c.when)); got != c.want {
			t.Errorf("%q matches %s = %v, want %v", c.expr, c.when, got, c.want)
		}
	}
}

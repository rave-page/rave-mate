package playerwin

import "testing"

func TestTrimState(t *testing.T) {
	var tr trimState
	tr.clear()
	if tr.in != 0 || tr.out != -1 {
		t.Fatalf("clear → %v/%v", tr.in, tr.out)
	}
	tr.setIn(30)
	tr.setOut(90)
	if tr.in != 30 || tr.out != 90 || tr.keeps(600) != 60 {
		t.Errorf("in/out/keeps = %v/%v/%v", tr.in, tr.out, tr.keeps(600))
	}
	tr.setIn(100) // IN past OUT → OUT resets to end
	if tr.out != -1 {
		t.Errorf("out = %v after in>out, want -1", tr.out)
	}
	if tr.keeps(600) != 500 {
		t.Errorf("keeps to end = %v, want 500", tr.keeps(600))
	}
	tr.setOut(50) // OUT ≤ IN → end
	if tr.out != -1 {
		t.Errorf("out = %v after out<=in, want -1", tr.out)
	}
	tr.setIn(-5)
	if tr.in != 0 {
		t.Errorf("negative IN not clamped: %v", tr.in)
	}
	if tr.keeps(0) != 0 {
		t.Errorf("keeps with no total = %v, want 0", tr.keeps(0))
	}
}

func TestTrimString(t *testing.T) {
	var tr trimState
	tr.clear()
	if got := tr.String(); got != "IN 0:00 · OUT end" {
		t.Errorf("String = %q", got)
	}
	tr.setIn(61)
	tr.setOut(3725)
	if got := tr.String(); got != "IN 1:01 · OUT 1:02:05" {
		t.Errorf("String = %q", got)
	}
}

func TestClock(t *testing.T) {
	for _, c := range []struct {
		sec  float64
		want string
	}{{-3, "0:00"}, {0, "0:00"}, {59.9, "0:59"}, {61, "1:01"}, {3599, "59:59"}, {3600, "1:00:00"}, {7325, "2:02:05"}} {
		if got := clock(c.sec); got != c.want {
			t.Errorf("clock(%v) = %q, want %q", c.sec, got, c.want)
		}
	}
}

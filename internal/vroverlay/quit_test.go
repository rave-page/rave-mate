package vroverlay

import "testing"

// QuitReason codes match the C mate_poll_quit contract (0 none, 1 quit, 2 driver/restart, 3 hmd-lost)
// and stringify distinctly for the reconnect log.
func TestQuitReasonCodesAndStrings(t *testing.T) {
	cases := []struct {
		q    QuitReason
		code int
		s    string
	}{
		{QuitNone, 0, "none"},
		{QuitRequested, 1, "steamvr-quit"},
		{QuitDriver, 2, "driver-quit/restart"},
		{QuitHMDLost, 3, "hmd-lost"},
	}
	for _, c := range cases {
		if int(c.q) != c.code {
			t.Errorf("%s: code %d, want %d", c.s, int(c.q), c.code)
		}
		if got := c.q.String(); got != c.s {
			t.Errorf("String() = %q, want %q", got, c.s)
		}
	}
	if got := QuitReason(9).String(); got != "unknown(9)" {
		t.Errorf("unknown String() = %q", got)
	}
}

// The non-vr stub never reports a fatal event (Manager stays idle, no reconnect churn).
func TestStubPollQuitNone(t *testing.T) {
	if q := NewRuntime().PollQuit(); q != QuitNone {
		t.Fatalf("stub PollQuit = %v, want QuitNone", q)
	}
}

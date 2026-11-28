package mfenc

import "testing"

// TestRCDecode pins the shm rate-control word layout + codes shared with native/zigenc (mf.zig
// RcMode, test "rc: telemetry codes are the wire contract").
func TestRCDecode(t *testing.T) {
	cases := []struct {
		w0, capKbps uint32
		mode        string
		qp          int
	}{
		{0, 0, "", 0},
		{1, 8000, "cbr", 0},
		{2 | 23<<8 | 70<<16, 3000, "cbr+qp-floor", 23},
		{3 | 21<<8 | 80<<16, 6000, "lowdelay-vbr+qp-floor", 21},
		{4 | 23<<8, 6000, "peak-vbr+qp-floor", 23},
		{5 | 23<<8, 3000, "cbr(quality refused)", 23},
		{9, 1, "rc9", 0},
	}
	for _, c := range cases {
		var st ProcStats
		st.setRC(c.w0, c.capKbps)
		if st.RateControl != c.mode || st.RateQP != c.qp || st.RateCapKbps != int(c.capKbps) {
			t.Errorf("setRC(%#x,%d) = %q qp=%d cap=%d, want %q qp=%d cap=%d", c.w0, c.capKbps,
				st.RateControl, st.RateQP, st.RateCapKbps, c.mode, c.qp, c.capKbps)
		}
	}
}

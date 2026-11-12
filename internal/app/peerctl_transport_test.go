package app

import "testing"

func TestCutTransportFlag(t *testing.T) {
	for _, tc := range []struct{ in, rest, tr string }{
		{"Spout2PW (VRChat): VRCSender1", "Spout2PW (VRChat): VRCSender1", ""},
		{"bar test --transport udp", "bar test", "udp"},
		{"--transport=TCP bar", "bar", "tcp"},
		{"bar --transport", "bar --transport", ""},
	} {
		rest, tr := cutTransportFlag(tc.in)
		if rest != tc.rest || tr != tc.tr {
			t.Fatalf("%q -> (%q,%q), want (%q,%q)", tc.in, rest, tr, tc.rest, tc.tr)
		}
	}
}

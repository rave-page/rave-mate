package app

import "testing"

func TestParseActPayload(t *testing.T) {
	cases := []struct {
		name, rest, act, val string
	}{
		{"plain act", "ce-close", "ce-close", ""},
		{"act plus val", "mp-surf:library down:0.3,0.5", "mp-surf:library", "down:0.3,0.5"},
		{"tab split", "key:cueedit\tdel", "key:cueedit", "del"},
		{"quoted act with spaces", `"ce-open:B:\Music\My Track.flac"`, `ce-open:B:\Music\My Track.flac`, ""},
		{"quoted act plus val", `"ce-open:C:\a b.wav" 0.5`, `ce-open:C:\a b.wav`, "0.5"},
		{"quoted UNC path verbatim backslashes", `"ce-open:\\srv\share\a b.wav"`, `ce-open:\\srv\share\a b.wav`, ""},
		{"escaped quote inside act", `"say \"hi\" there" v`, `say "hi" there`, "v"},
		{"malformed quote falls back to raw split", `"oops path`, `"oops`, "path"},
		{"lone quote falls back", `"`, `"`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			act, val := parseActPayload(c.rest)
			if act != c.act || val != c.val {
				t.Fatalf("parseActPayload(%q) = (%q, %q), want (%q, %q)", c.rest, act, val, c.act, c.val)
			}
		})
	}
}

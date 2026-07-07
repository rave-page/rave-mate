package visualeditor

import "testing"

// fakeProvider is a test Provider backed by a map.
type fakeProvider map[string]string

func (f fakeProvider) Value(k string) (string, bool) { v, ok := f[k]; return v, ok }

func TestSubstitute(t *testing.T) {
	p := fakeProvider{
		"track.title":  "Strobe",
		"track.artist": "deadmau5",
		"track.bpm":    "128",
		"time":         "23:45",
	}
	cases := map[string]string{
		"{track.artist} - {track.title}": "deadmau5 - Strobe",
		"BPM {track.bpm} @ {time}":       "BPM 128 @ 23:45",
		"no tokens here":                 "no tokens here",
		"{unknown.key}":                  "{unknown.key}", // unresolved kept literal
		"{track.title} {track.title}":    "Strobe Strobe",
	}
	for in, want := range cases {
		if got := Substitute(in, p); got != want {
			t.Errorf("Substitute(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSubstituteNilProvider(t *testing.T) {
	if got := Substitute("{x}", nil); got != "{x}" {
		t.Fatalf("nil provider should no-op, got %q", got)
	}
}

func TestChainProviderFirstHitWins(t *testing.T) {
	c := ChainProvider{
		fakeProvider{"a": "1"},
		fakeProvider{"a": "2", "b": "3"},
	}
	if v, _ := c.Value("a"); v != "1" {
		t.Fatalf("first hit should win: %q", v)
	}
	if v, ok := c.Value("b"); !ok || v != "3" {
		t.Fatalf("fallthrough failed: %q %v", v, ok)
	}
	if _, ok := c.Value("z"); ok {
		t.Fatal("unknown key should miss")
	}
}

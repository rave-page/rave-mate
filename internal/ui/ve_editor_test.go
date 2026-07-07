package ui

import (
	"testing"

	"rave.page/mate/internal/session"
	"rave.page/mate/internal/visualeditor"
)

func TestRemoveLayerAndIndexOf(t *testing.T) {
	a := visualeditor.NewGroup("a")
	b := visualeditor.NewGroup("b")
	c := visualeditor.NewGroup("c")
	list := []*visualeditor.Layer{a, b, c}
	if indexOf(list, b.ID) != 1 {
		t.Fatal("indexOf b != 1")
	}
	if indexOf(list, "nope") != -1 {
		t.Fatal("indexOf missing != -1")
	}
	list = removeLayer(list, b.ID)
	if len(list) != 2 || list[0] != a || list[1] != c {
		t.Fatalf("removeLayer wrong result: %v", list)
	}
}

func TestBlendNamesCoversAllModes(t *testing.T) {
	if len(blendNames()) != len(visualeditor.BlendModes) {
		t.Fatal("blendNames count mismatch")
	}
	if blendNames()[0] != string(visualeditor.BlendNormal) {
		t.Fatal("first blend should be normal")
	}
}

func TestTrimFloat(t *testing.T) {
	cases := map[float64]string{0: "0", 1.5: "1.5", -12: "-12", 100.25: "100.25"}
	for in, want := range cases {
		if got := trimFloat(in); got != want {
			t.Errorf("trimFloat(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestNowPlayingDeckPicksMaster(t *testing.T) {
	ov := session.Overlay{
		Decks: []session.DeckSnapshot{
			{Deck: "A", Title: "First"},
			{Deck: "B", Title: "Master track"},
		},
		Master: session.MasterSnapshot{Deck: "B"},
	}
	d, ok := nowPlayingDeck(ov)
	if !ok || d.Title != "Master track" {
		t.Fatalf("expected master deck B, got %+v ok=%v", d, ok)
	}
	// No master → first deck.
	ov.Master.Deck = ""
	d, ok = nowPlayingDeck(ov)
	if !ok || d.Deck != "A" {
		t.Fatalf("fallback should be first deck, got %+v", d)
	}
	// Empty → miss.
	if _, ok := nowPlayingDeck(session.Overlay{}); ok {
		t.Fatal("empty overlay should miss")
	}
}

func TestLiveProviderClockKeys(t *testing.T) {
	p := liveProvider{u: &UI{}} // nil session → track.* miss, clock keys resolve
	if v, ok := p.Value("time"); !ok || len(v) != 5 {
		t.Fatalf("time key: %q ok=%v", v, ok)
	}
	if v, ok := p.Value("date"); !ok || len(v) != 10 {
		t.Fatalf("date key: %q ok=%v", v, ok)
	}
	if _, ok := p.Value("track.title"); ok {
		t.Fatal("track.title should miss with no session")
	}
}

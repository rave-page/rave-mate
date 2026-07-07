package overlayobs

import (
	"context"
	"errors"
	"strings"
	"testing"

	"rave.page/mate/internal/obs"
	"rave.page/mate/internal/session"
)

func TestInputNameDerivation(t *testing.T) {
	if got := TextInputName("A"); got != "RaveMate Deck A Text" {
		t.Errorf("TextInputName(A) = %q", got)
	}
	if got := ArtInputName("B"); got != "RaveMate Deck B Art" {
		t.Errorf("ArtInputName(B) = %q", got)
	}
}

func TestLayoutStacking(t *testing.T) {
	l := defaultLayout()
	ax0, ay0 := l.artPos(0)
	ax1, ay1 := l.artPos(1)
	if ax0 != l.MarginX || ax1 != l.MarginX {
		t.Errorf("art X should be constant: %v %v", ax0, ax1)
	}
	if ay1-ay0 != l.RowHeight {
		t.Errorf("row stride = %v, want %v", ay1-ay0, l.RowHeight)
	}
	tx0, _ := l.textPos(0)
	if tx0 != l.MarginX+l.ArtSize+l.Gap {
		t.Errorf("text X = %v, want %v", tx0, l.MarginX+l.ArtSize+l.Gap)
	}
}

func TestPosTransformShape(t *testing.T) {
	tr := posTransform(40, 180)
	if tr["positionX"].(float64) != 40 || tr["positionY"].(float64) != 180 {
		t.Errorf("posTransform = %v", tr)
	}
}

func TestDeckText(t *testing.T) {
	d := session.DeckSnapshot{Title: "Strobe", Artist: "deadmau5", BPM: 128, Key: "8A", ElapsedTime: 75}
	got := deckText(d)
	want := "Strobe\ndeadmau5\n128 BPM  •  8A  •  1:15"
	if got != want {
		t.Errorf("deckText =\n%q\nwant\n%q", got, want)
	}
	d2 := session.DeckSnapshot{Title: "Untitled", ElapsedTime: 5}
	got2 := deckText(d2)
	if strings.Contains(got2, "•  •") || strings.HasPrefix(got2, "\n") {
		t.Errorf("deckText sparse has empty bullets: %q", got2)
	}
	if got2 != "Untitled\n0:05" {
		t.Errorf("deckText sparse = %q", got2)
	}
}

func TestFmtElapsed(t *testing.T) {
	cases := map[float64]string{0: "0:00", 5: "0:05", 65: "1:05", 600: "10:00", -3: "0:00"}
	for in, want := range cases {
		if got := fmtElapsed(in); got != want {
			t.Errorf("fmtElapsed(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestSignatureSensitivity(t *testing.T) {
	a := []session.DeckSnapshot{{Deck: "A", ArtKey: "k1", OnAir: true, ElapsedTime: 10.2}}
	b := []session.DeckSnapshot{{Deck: "A", ArtKey: "k1", OnAir: true, ElapsedTime: 10.9}}
	if signature(a) != signature(b) {
		t.Error("sub-second elapsed jitter should not change signature")
	}
	c := []session.DeckSnapshot{{Deck: "A", ArtKey: "k1", OnAir: true, ElapsedTime: 11.0}}
	if signature(a) == signature(c) {
		t.Error("whole-second elapsed change should change signature")
	}
	d := []session.DeckSnapshot{{Deck: "A", ArtKey: "k2", OnAir: true, ElapsedTime: 10.2}}
	if signature(a) == signature(d) {
		t.Error("track change (artKey) should change signature")
	}
}

func TestApplyGate(t *testing.T) {
	s := New(nil, nil, nil)
	cued := []session.DeckSnapshot{{Deck: "A", ArtKey: "k1", OnAir: false}}
	if got := s.applyGate(cued); len(got) != 0 {
		t.Errorf("cued deck should be gated out, got %d", len(got))
	}
	onair := []session.DeckSnapshot{{Deck: "A", ArtKey: "k1", OnAir: true}}
	if got := s.applyGate(onair); len(got) != 1 {
		t.Errorf("on-air deck should show, got %d", len(got))
	}
	if got := s.applyGate(cued); len(got) != 1 {
		t.Errorf("faded-but-same-track deck should stay shown, got %d", len(got))
	}
	newcued := []session.DeckSnapshot{{Deck: "A", ArtKey: "k2", OnAir: false}}
	if got := s.applyGate(newcued); len(got) != 0 {
		t.Errorf("new cued track should re-gate, got %d", len(got))
	}
	s.applyGate(nil)
	if _, ok := s.gate["A"]; ok {
		t.Error("unloaded deck gate entry should be forgotten")
	}
}

// ── fake OBS client (implements OBSClient) ──

type fakeOBS struct {
	scene     string
	failScene bool
	nextID    int
	inScene   map[string]int // inputName → sceneItemId present in scene
	calls     map[string]int // op → count
	enabled   map[int]bool
	transform map[int]map[string]any
	settings  map[string]map[string]any
}

func newFake(scene string) *fakeOBS {
	return &fakeOBS{
		scene: scene, nextID: 100,
		inScene:   map[string]int{},
		calls:     map[string]int{},
		enabled:   map[int]bool{},
		transform: map[int]map[string]any{},
		settings:  map[string]map[string]any{},
	}
}

var _ OBSClient = (*fakeOBS)(nil)

func (f *fakeOBS) GetCurrentProgramScene(context.Context) (string, error) {
	f.calls["GetCurrentProgramScene"]++
	if f.failScene {
		return "", errors.New("disconnected")
	}
	return f.scene, nil
}

func (f *fakeOBS) GetInputList(context.Context, string) ([]obs.InputInfo, error) {
	f.calls["GetInputList"]++
	return nil, nil
}

func (f *fakeOBS) CreateInput(_ context.Context, p obs.CreateInputParams) (int, error) {
	f.calls["CreateInput"]++
	id := f.nextID
	f.nextID++
	f.inScene[p.InputName] = id
	f.settings[p.InputName] = p.InputSettings
	f.enabled[id] = p.SceneItemEnabled
	return id, nil
}

func (f *fakeOBS) SetInputSettings(_ context.Context, name string, s map[string]any, _ bool) error {
	f.calls["SetInputSettings"]++
	f.settings[name] = s
	return nil
}

func (f *fakeOBS) GetSceneItemID(_ context.Context, _, source string) (int, error) {
	f.calls["GetSceneItemID"]++
	if id, ok := f.inScene[source]; ok {
		return id, nil
	}
	return 0, errors.New("not found")
}

func (f *fakeOBS) SetSceneItemEnabled(_ context.Context, _ string, id int, en bool) error {
	f.calls["SetSceneItemEnabled"]++
	f.enabled[id] = en
	return nil
}

func (f *fakeOBS) SetSceneItemTransform(_ context.Context, _ string, id int, tr map[string]any) error {
	f.calls["SetSceneItemTransform"]++
	f.transform[id] = tr
	return nil
}

// TestPushCreatesAndPositions drives push() with a deck (no art) and asserts the text input is
// created, positioned, enabled. ArtKey="" makes the (nil) art resolver a no-op, so art is skipped.
func TestPushCreatesAndPositions(t *testing.T) {
	f := newFake("Live")
	s := New(nil, f, nil)
	decks := []session.DeckSnapshot{{Deck: "A", Title: "Strobe", Artist: "deadmau5", BPM: 128}}

	if err := s.push(context.Background(), f, decks); err != nil {
		t.Fatalf("push: %v", err)
	}
	if f.calls["CreateInput"] != 1 {
		t.Errorf("CreateInput count = %d, want 1", f.calls["CreateInput"])
	}
	id := f.inScene[TextInputName("A")]
	if !f.enabled[id] {
		t.Error("text item should be enabled")
	}
	wantTX, _ := s.layout.textPos(0)
	if f.transform[id]["positionX"].(float64) != wantTX {
		t.Errorf("text X = %v, want %v", f.transform[id]["positionX"], wantTX)
	}
	txt, _ := f.settings[TextInputName("A")]["text"].(string)
	if !strings.Contains(txt, "Strobe") {
		t.Errorf("text settings = %v", f.settings[TextInputName("A")])
	}

	// Second push, same scene: input already known → updated via SetInputSettings, not recreated.
	if err := s.push(context.Background(), f, decks); err != nil {
		t.Fatalf("push 2: %v", err)
	}
	if f.calls["CreateInput"] != 1 {
		t.Errorf("CreateInput should not run again, count = %d", f.calls["CreateInput"])
	}
	if f.calls["SetInputSettings"] < 1 {
		t.Error("second push should SetInputSettings")
	}
}

// TestPushDisablesVanishedDeck shows then hides a deck across pushes.
func TestPushDisablesVanishedDeck(t *testing.T) {
	f := newFake("Live")
	s := New(nil, f, nil)
	decks := []session.DeckSnapshot{{Deck: "A", Title: "X"}}
	if err := s.push(context.Background(), f, decks); err != nil {
		t.Fatal(err)
	}
	id := f.inScene[TextInputName("A")]
	if !f.enabled[id] {
		t.Fatal("deck A text should be enabled after first push")
	}
	// Deck A gone.
	if err := s.push(context.Background(), f, nil); err != nil {
		t.Fatal(err)
	}
	if f.enabled[id] {
		t.Error("deck A text should be disabled after it vanished")
	}
}

// TestTickNoClient verifies a nil client is a graceful no-op (never panics).
func TestTickNoClient(t *testing.T) {
	s := New(nil, nil, nil)
	st := session.NewMerger().Snapshot()
	s.tick(context.Background(), st) // must not panic
}

// TestPushSceneFailureResets ensures a disconnected OBS doesn't crash and clears cached state.
func TestPushSceneFailureResets(t *testing.T) {
	f := newFake("Live")
	f.failScene = true
	s := New(nil, f, nil)
	if err := s.push(context.Background(), f, []session.DeckSnapshot{{Deck: "A", Title: "X"}}); err == nil {
		t.Error("expected error when scene query fails")
	}
}

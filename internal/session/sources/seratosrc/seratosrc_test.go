package seratosrc

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"unicode/utf16"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/session"
)

// --- minimal Serato .session chunk encoder (numeric adat ids known from the format) ---

const (
	idTitle  = 0x06
	idArtist = 0x07
	idDeck   = 0x1f
	idEnd    = 0x1d
	idPlayed = 0x32
)

func enc16(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, len(u)*2)
	for i, c := range u {
		binary.BigEndian.PutUint16(b[i*2:], c)
	}
	return b
}

func u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

func chunk(tag []byte, payload []byte) []byte {
	h := make([]byte, 8)
	copy(h, tag)
	binary.BigEndian.PutUint32(h[4:], uint32(len(payload)))
	return append(h, payload...)
}

// entry builds an oent/adat history record. end==0 => no endtime field (still on deck).
func entry(title string, deck, end uint32) []byte {
	fields := [][]byte{
		chunk(u32(idTitle), enc16(title)),
		chunk(u32(idArtist), enc16(title+" Artist")),
		chunk(u32(idDeck), u32(deck)),
		chunk(u32(idPlayed), u32(1)),
	}
	if end != 0 {
		fields = append(fields, chunk(u32(idEnd), u32(end)))
	}
	return chunk([]byte("oent"), chunk([]byte("adat"), bytes.Join(fields, nil)))
}

// writeSession writes entries into <dir>/History/Sessions/1.session and returns the source.
func writeSession(t *testing.T, entries ...[]byte) (*Source, string) {
	t.Helper()
	dir := t.TempDir()
	sess := filepath.Join(dir, "History", "Sessions")
	if err := os.MkdirAll(sess, 0o755); err != nil {
		t.Fatal(err)
	}
	buf := append(chunk([]byte("vrsn"), enc16("1.0")), bytes.Join(entries, nil)...)
	if err := os.WriteFile(filepath.Join(sess, "1.session"), buf, 0o644); err != nil {
		t.Fatal(err)
	}
	return New(logbus.New(16), dir, true), dir
}

// collect runs one poll and indexes the emitted observations by scope key.
func collect(s *Source) map[string]session.Observation {
	out := map[string]session.Observation{}
	s.poll(func(o session.Observation) { out[o.Scope.Key()] = o })
	return out
}

func str(o session.Observation, f string) string {
	if v, ok := o.Fields[f].(string); ok {
		return v
	}
	return ""
}
func boolf(o session.Observation, f string) bool {
	b, _ := o.Fields[f].(bool)
	return b
}

// TestTwoDecksEmitIndependently: A + B both live => two deck observations, both playing.
func TestTwoDecksEmitIndependently(t *testing.T) {
	s, _ := writeSession(t, entry("Track A", 1, 0), entry("Track B", 2, 0))
	obs := collect(s)

	a, okA := obs["deck:A"]
	b, okB := obs["deck:B"]
	if !okA || !okB {
		t.Fatalf("want deck A and B observations, got keys %v", keys(obs))
	}
	if str(a, session.FieldTitle) != "Track A" || !boolf(a, session.FieldIsPlaying) {
		t.Errorf("deck A wrong: %+v", a.Fields)
	}
	if str(b, session.FieldTitle) != "Track B" || !boolf(b, session.FieldIsPlaying) {
		t.Errorf("deck B wrong: %+v", b.Fields)
	}
}

// TestDeckLatestEntryWins: a deck's ended track then a new live track => newest shown, playing.
func TestDeckLatestEntryWins(t *testing.T) {
	s, _ := writeSession(t, entry("Old", 1, 1100), entry("New", 1, 0))
	a := collect(s)["deck:A"]
	if str(a, session.FieldTitle) != "New" || !boolf(a, session.FieldIsPlaying) {
		t.Errorf("deck A should show live New track: %+v", a.Fields)
	}
}

// TestEndedDeckNotPlaying: deck's last entry has an endtime => isPlaying=false (deck idle).
func TestEndedDeckNotPlaying(t *testing.T) {
	s, _ := writeSession(t, entry("Done", 1, 1100))
	a := collect(s)["deck:A"]
	if str(a, session.FieldTitle) != "Done" {
		t.Fatalf("deck A missing: %+v", a.Fields)
	}
	if boolf(a, session.FieldIsPlaying) {
		t.Errorf("ended deck must not be playing: %+v", a.Fields)
	}
}

// TestLoadedBoundaryPerDeck: a track change on B doesn't reset A's Loaded on the next poll.
func TestLoadedBoundaryPerDeck(t *testing.T) {
	s, dir := writeSession(t, entry("A1", 1, 0), entry("B1", 2, 0))
	first := collect(s)
	if !first["deck:A"].Loaded || !first["deck:B"].Loaded {
		t.Fatalf("first poll: both decks should be Loaded")
	}
	// B swaps to a new track; A unchanged. Rewrite the session with a newer mtime.
	sess := filepath.Join(dir, "History", "Sessions", "1.session")
	buf := append(chunk([]byte("vrsn"), enc16("1.0")),
		bytes.Join([][]byte{entry("A1", 1, 0), entry("B1", 2, 1200), entry("B2", 2, 0)}, nil)...)
	if err := os.WriteFile(sess, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	touchNewer(t, sess)
	second := collect(s)
	if second["deck:A"].Loaded {
		t.Errorf("deck A must NOT be Loaded (unchanged)")
	}
	if !second["deck:B"].Loaded {
		t.Errorf("deck B must be Loaded (B1->B2)")
	}
	if str(second["deck:B"], session.FieldTitle) != "B2" {
		t.Errorf("deck B should be B2, got %q", str(second["deck:B"], session.FieldTitle))
	}
}

// TestDeckLessFallback: no deck numbers => single master observation.
func TestDeckLessFallback(t *testing.T) {
	s, _ := writeSession(t, entry("Solo", 0, 0))
	obs := collect(s)
	m, ok := obs["master"]
	if !ok {
		t.Fatalf("want master observation, got %v", keys(obs))
	}
	if str(m, session.FieldTitle) != "Solo" || !boolf(m, session.FieldIsPlaying) {
		t.Errorf("master wrong: %+v", m.Fields)
	}
}

func keys(m map[string]session.Observation) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// touchNewer bumps the file mtime past the source's lastMod so poll re-reads it.
func touchNewer(t *testing.T, path string) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	nt := fi.ModTime().Add(2 * 1e9) // +2s
	if err := os.Chtimes(path, nt, nt); err != nil {
		t.Fatal(err)
	}
}

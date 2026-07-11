package libdb

import (
	"path/filepath"
	"testing"
)

func openTestCompatDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "lib.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestNormPair(t *testing.T) {
	a, b := NormPair("b.mp3", "a.mp3")
	if a != "a.mp3" || b != "b.mp3" {
		t.Fatalf("norm: %q %q", a, b)
	}
	a, b = NormPair("a.mp3", "b.mp3")
	if a != "a.mp3" || b != "b.mp3" {
		t.Fatalf("stable: %q %q", a, b)
	}
}

func TestAddCompatPairs(t *testing.T) {
	d := openTestCompatDB(t)
	// 3 tracks (with a dup + empty) = C(3,2) = 3 pairs
	n, err := d.AddCompatPairs("blend", []string{"a.mp3", "b.mp3", "c.mp3", "b.mp3", ""})
	if err != nil || n != 3 {
		t.Fatalf("add: %d %v", n, err)
	}
	// idempotent
	n, err = d.AddCompatPairs("blend", []string{"a.mp3", "b.mp3"})
	if err != nil || n != 0 {
		t.Fatalf("re-add: %d %v", n, err)
	}
	// second kind on the same pair = new row
	n, err = d.AddCompatPairs("energy", []string{"a.mp3", "b.mp3"})
	if err != nil || n != 1 {
		t.Fatalf("second kind: %d %v", n, err)
	}
	if _, err := d.AddCompatPairs("bogus", []string{"a.mp3", "b.mp3"}); err == nil {
		t.Fatal("invalid kind should fail")
	}
	if _, err := d.AddCompatPairs("blend", []string{"a.mp3"}); err == nil {
		t.Fatal("single track should fail")
	}
}

func TestCompatForSymmetric(t *testing.T) {
	d := openTestCompatDB(t)
	if _, err := d.AddCompatPairs("blend", []string{"a.mp3", "b.mp3"}); err != nil {
		t.Fatal(err)
	}
	for _, from := range []string{"a.mp3", "b.mp3"} {
		rows, err := d.CompatFor(from)
		if err != nil || len(rows) != 1 {
			t.Fatalf("for %s: %v %v", from, rows, err)
		}
		want := "b.mp3"
		if from == "b.mp3" {
			want = "a.mp3"
		}
		if rows[0].Path != want || rows[0].Kind != "blend" {
			t.Fatalf("row: %+v", rows[0])
		}
	}
}

func TestCompatForMany(t *testing.T) {
	d := openTestCompatDB(t)
	_, _ = d.AddCompatPairs("blend", []string{"a.mp3", "b.mp3", "c.mp3"})
	_, _ = d.AddCompatPairs("energy", []string{"c.mp3", "d.mp3"})
	m, err := d.CompatForMany([]string{"a.mp3", "c.mp3"})
	if err != nil {
		t.Fatal(err)
	}
	if len(m["a.mp3"]) != 2 {
		t.Fatalf("a: %+v", m["a.mp3"])
	}
	if len(m["c.mp3"]) != 3 { // a+b blend, d energy
		t.Fatalf("c: %+v", m["c.mp3"])
	}
	if len(m["b.mp3"]) != 0 { // not requested
		t.Fatalf("b leaked: %+v", m["b.mp3"])
	}
}

func TestRemoveCompat(t *testing.T) {
	d := openTestCompatDB(t)
	_, _ = d.AddCompatPairs("blend", []string{"a.mp3", "b.mp3"})
	_, _ = d.AddCompatPairs("energy", []string{"a.mp3", "b.mp3"})
	if err := d.RemoveCompat("b.mp3", "a.mp3", "blend"); err != nil { // reversed order normalizes
		t.Fatal(err)
	}
	rows, _ := d.CompatFor("a.mp3")
	if len(rows) != 1 || rows[0].Kind != "energy" {
		t.Fatalf("after kind delete: %+v", rows)
	}
	if err := d.RemoveCompat("a.mp3", "b.mp3", ""); err != nil {
		t.Fatal(err)
	}
	if rows, _ := d.CompatFor("a.mp3"); len(rows) != 0 {
		t.Fatalf("after full delete: %+v", rows)
	}
}

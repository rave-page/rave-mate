package artnet

import (
	"testing"
	"time"
)

func TestStoreSetGetAndGeneration(t *testing.T) {
	s := NewStore()
	now := time.Now()
	if _, ok := s.Get(1); ok {
		t.Fatal("empty universe reported present")
	}
	g0 := s.Generation()
	if !s.Set(1, 1, []byte{10, 20, 30}, "1.2.3.4", now) {
		t.Fatal("first frame rejected")
	}
	if s.Generation() == g0 {
		t.Fatal("generation did not bump on change")
	}
	d, ok := s.Get(1)
	if !ok || d[0] != 10 || d[1] != 20 || d[2] != 30 {
		t.Fatalf("get=%v ok=%v", d[:3], ok)
	}
	// Same values, newer seq → accepted but no generation bump.
	g1 := s.Generation()
	if !s.Set(1, 2, []byte{10, 20, 30}, "1.2.3.4", now) {
		t.Fatal("in-order identical frame rejected")
	}
	if s.Generation() != g1 {
		t.Fatal("generation bumped without a value change")
	}
}

func TestStoreSequenceWrap(t *testing.T) {
	s := NewStore()
	now := time.Now()
	s.Set(1, 100, []byte{1}, "x", now)
	if s.Set(1, 99, []byte{2}, "x", now) {
		t.Fatal("older sequence accepted")
	}
	if d, _ := s.Get(1); d[0] != 1 {
		t.Fatalf("stale frame mutated store: %d", d[0])
	}
	if !s.Set(1, 101, []byte{2}, "x", now) {
		t.Fatal("newer sequence rejected")
	}
	// Wrap: from 250, seq 5 is newer (mod-256 distance 11); 200 (distance 206) is stale.
	s2 := NewStore()
	s2.Set(2, 250, []byte{3}, "x", now)
	if s2.Set(2, 200, []byte{9}, "x", now) {
		t.Fatal("backwards sequence accepted")
	}
	if !s2.Set(2, 5, []byte{4}, "x", now) {
		t.Fatal("wrap-around newer sequence rejected")
	}
	// seq 0 = disabled, always accepted.
	if !s.Set(1, 0, []byte{5}, "x", now) {
		t.Fatal("seq 0 rejected")
	}
}

func TestStoreStatsPPSStale(t *testing.T) {
	s := NewStore()
	base := time.Now()
	s.Set(7, 1, []byte{1}, "10.0.0.9", base)
	st := s.Stats(base, time.Second)
	if len(st) != 1 || st[0].Universe != 7 || st[0].SourceIP != "10.0.0.9" {
		t.Fatalf("stats=%+v", st)
	}
	// Idle beyond staleAfter → PPS zeroed.
	st = s.Stats(base.Add(3*time.Second), time.Second)
	if st[0].PPS != 0 {
		t.Fatalf("stale pps=%v want 0", st[0].PPS)
	}
}

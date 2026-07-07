package store

import (
	"path/filepath"
	"testing"
)

func TestAnalysisCacheMtime(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	if _, ok := s.GetAnalysis(KindWaveform, "a.wav", 100); ok {
		t.Fatal("empty store should miss")
	}
	s.PutAnalysis(KindWaveform, "a.wav", 100, []byte("PNGDATA"))
	if d, ok := s.GetAnalysis(KindWaveform, "a.wav", 100); !ok || string(d) != "PNGDATA" {
		t.Fatalf("hit = %q %v", d, ok)
	}
	if _, ok := s.GetAnalysis(KindWaveform, "a.wav", 101); ok {
		t.Fatal("changed mtime must invalidate")
	}
	if _, ok := s.GetAnalysis(KindTags, "a.wav", 100); ok {
		t.Fatal("different kind must not collide")
	}
}

func TestNilStoreSafe(t *testing.T) {
	var s *Store // nil
	s.PutAnalysis(KindTags, "x", 1, []byte("y"))
	if _, ok := s.GetAnalysis(KindTags, "x", 1); ok {
		t.Fatal("nil store should miss, not panic")
	}
	if err := s.PutJSON(BucketJobs, "k", map[string]int{"a": 1}); err != nil {
		t.Fatalf("nil PutJSON: %v", err)
	}
	_ = s.Close()
}

func TestJSONBucket(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	type job struct {
		ID   string
		Pct  int
		Name string
	}
	if err := s.PutJSON(BucketJobs, "j1", job{ID: "j1", Pct: 50, Name: "a"}); err != nil {
		t.Fatalf("put: %v", err)
	}
	var got job
	ok, err := s.GetJSON(BucketJobs, "j1", &got)
	if err != nil || !ok || got.Pct != 50 || got.Name != "a" {
		t.Fatalf("get = %+v ok=%v err=%v", got, ok, err)
	}
	_ = s.PutJSON(BucketJobs, "j2", job{ID: "j2"})
	all, err := s.ListJSON(BucketJobs)
	if err != nil || len(all) != 2 {
		t.Fatalf("list = %d entries, err=%v", len(all), err)
	}
	if err := s.Delete(BucketJobs, "j1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if ok, _ := s.GetJSON(BucketJobs, "j1", &got); ok {
		t.Fatal("deleted key should miss")
	}
}

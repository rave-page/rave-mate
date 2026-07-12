package webui

import "testing"

// Media-token eviction is per-owner: a remote session's churn must evict its OWN oldest
// token, never the window's live <video> registration; releaseUIState purges the session's.
func TestMediaTokenEvictionPerOwner(t *testing.T) {
	s := &mpMediaSrv{tokens: map[string]string{}, owner: map[string]*UI{}, byPath: map[string]string{}}
	w, r := &UI{}, &UI{}
	add := func(u *UI, tok, path string) {
		s.tokens[tok] = path
		s.owner[tok] = u
		s.byPath[path] = tok
		s.order = append(s.order, tok)
	}
	add(w, "w1", "C:/w1.mp4") // oldest overall - FIFO would pick this
	add(r, "r1", "C:/r1.mp4")
	add(r, "r2", "C:/r2.mp4")

	s.evictMediaLocked(r)
	if _, ok := s.tokens["w1"]; !ok {
		t.Fatal("window token evicted by remote churn")
	}
	if _, ok := s.tokens["r1"]; ok {
		t.Fatal("remote's own oldest token not evicted")
	}

	s.evictMediaLocked(w) // owner with tokens evicts its own even when not globally oldest
	if _, ok := s.tokens["w1"]; ok {
		t.Fatal("window's own token should evict on window churn")
	}

	add(w, "w2", "C:/w2.mp4")
	s.releaseMediaOwner(r)
	if _, ok := s.tokens["r2"]; ok {
		t.Fatal("release did not purge the session's tokens")
	}
	if _, ok := s.tokens["w2"]; !ok {
		t.Fatal("release purged another UI's token")
	}
	if len(s.order) != 1 || s.order[0] != "w2" {
		t.Fatalf("order after release = %v, want [w2]", s.order)
	}
}

// No token owned by the evicting UI degrades to the global oldest (bound still enforced).
func TestMediaTokenEvictionFallback(t *testing.T) {
	s := &mpMediaSrv{tokens: map[string]string{}, owner: map[string]*UI{}, byPath: map[string]string{}}
	w, r := &UI{}, &UI{}
	s.tokens["w1"] = "C:/w1.mp4"
	s.owner["w1"] = w
	s.byPath["C:/w1.mp4"] = "w1"
	s.order = []string{"w1"}
	s.evictMediaLocked(r)
	if len(s.order) != 0 {
		t.Fatal("fallback eviction must drop the global oldest")
	}
}

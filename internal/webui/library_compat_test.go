package webui

import (
	"testing"

	"rave.page/mate/internal/libdb"
)

func TestCompatDiscoverDepth2(t *testing.T) {
	direct := []libdb.CompatRow{
		{Path: "b", Kind: "blend"},
		{Path: "b", Kind: "energy"}, // second kind merges into the same hit
		{Path: "c", Kind: "double_drop"},
	}
	second := map[string][]libdb.CompatRow{
		"b": {{Path: "a", Kind: "blend"}, {Path: "d", Kind: "blend"}, {Path: "c", Kind: "energy"}},
		"c": {{Path: "e", Kind: "double_drop"}},
	}
	hits := compatDiscover("a", direct, second, 10)
	if len(hits) != 4 { // b, c direct; d (via b), e (via c)
		t.Fatalf("hits: %+v", hits)
	}
	if hits[0].path != "b" || hits[0].depth != 1 || len(hits[0].kinds) != 2 {
		t.Fatalf("b: %+v", hits[0])
	}
	if hits[1].path != "c" || hits[1].depth != 1 {
		t.Fatalf("c: %+v", hits[1])
	}
	if hits[2].path != "d" || hits[2].depth != 2 || hits[2].via != "b" {
		t.Fatalf("d: %+v", hits[2])
	}
	if hits[3].path != "e" || hits[3].depth != 2 || hits[3].via != "c" {
		t.Fatalf("e: %+v", hits[3])
	}
}

func TestCompatDiscoverExcludesSelfAndDups(t *testing.T) {
	direct := []libdb.CompatRow{{Path: "b", Kind: "blend"}}
	second := map[string][]libdb.CompatRow{"b": {{Path: "a", Kind: "blend"}, {Path: "b", Kind: "blend"}}}
	hits := compatDiscover("a", direct, second, 10)
	if len(hits) != 1 || hits[0].path != "b" {
		t.Fatalf("hits: %+v", hits)
	}
}

func TestCompatDiscoverLimit(t *testing.T) {
	direct := []libdb.CompatRow{{Path: "b", Kind: "blend"}, {Path: "c", Kind: "blend"}}
	second := map[string][]libdb.CompatRow{
		"b": {{Path: "d", Kind: "blend"}, {Path: "e", Kind: "blend"}},
	}
	hits := compatDiscover("a", direct, second, 3)
	if len(hits) != 3 {
		t.Fatalf("limit: %+v", hits)
	}
	// direct-only truncation
	hits = compatDiscover("a", direct, nil, 1)
	if len(hits) != 1 || hits[0].path != "b" {
		t.Fatalf("direct cap: %+v", hits)
	}
}

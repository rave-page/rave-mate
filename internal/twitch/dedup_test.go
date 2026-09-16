package twitch

import (
	"fmt"
	"testing"
)

func TestIDWindowDedupesAndEvicts(t *testing.T) {
	var w IDWindow
	if w.Seen("m1") {
		t.Fatal("first sighting must not be a duplicate")
	}
	if !w.Seen("m1") {
		t.Fatal("second sighting of the same id must be a duplicate")
	}
	if w.Seen("") || w.Seen("") {
		t.Fatal("an empty id (alerts) is never deduplicated")
	}
	// Fill the ring past capacity: the oldest id falls out and reads as new again, the set never
	// grows past the ring.
	for i := 0; i < idWindowSize; i++ {
		w.Seen(fmt.Sprintf("x%d", i))
	}
	if w.Seen("m1") {
		t.Fatal("m1 must have been evicted after idWindowSize newer ids")
	}
	if len(w.set) > idWindowSize {
		t.Fatalf("set grew to %d, bound is %d", len(w.set), idWindowSize)
	}
}

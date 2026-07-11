package webui

import (
	"testing"

	"rave.page/mate/internal/midi"
)

func TestFbtCount(t *testing.T) {
	es := []midi.TraceEntry{
		{Seq: 1, Dir: midi.TraceDirFromApp},
		{Seq: 2, Dir: midi.TraceDirFeedbackOut},
		{Seq: 3, Dir: midi.TraceDirFeedbackOut},
		{Seq: 4, Dir: midi.TraceDirToApp},
		{Seq: 5, Dir: midi.TraceDirFeedbackOut},
	}
	if got := fbtCount(es, 0); got != 3 {
		t.Fatalf("base 0: got %d, want 3", got)
	}
	if got := fbtCount(es, 2); got != 2 { // only Seq>2 counted
		t.Fatalf("base 2: got %d, want 2", got)
	}
	if got := fbtCount(nil, 0); got != 0 {
		t.Fatalf("nil trace: got %d, want 0", got)
	}
}

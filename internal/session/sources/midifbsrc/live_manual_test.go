//go:build manual

package midifbsrc

import (
	"fmt"
	"testing"
	"time"
)

// TestLiveFeedbackClassify polls the INSTALLED ravemidi driver's LED feedback for ~25s and
// prints per-deck play/pause transitions as the detector sees them. Read-only (QUERY_TRACE),
// so it's safe to run alongside a live rave-mate + DJ software. Run while toggling play/pause:
//
//	go test -tags manual -run TestLiveFeedbackClassify ./internal/session/sources/midifbsrc/ -v -count=1
func TestLiveFeedbackClassify(t *testing.T) {
	d := newDetector()
	start := time.Now()
	prev := map[string]bool{}
	label := map[bool]string{true: "PLAYING", false: "paused "}
	fmt.Println("watching ravemidi LED feedback for 25s - play/pause your decks now...")
	for time.Since(start) < 25*time.Second {
		for deck, playing := range d.step(collect()) {
			if p, ok := prev[deck]; !ok || p != playing {
				fmt.Printf("[%5.1fs] deck %s -> %s\n", time.Since(start).Seconds(), deck, label[playing])
				prev[deck] = playing
			}
		}
		time.Sleep(350 * time.Millisecond)
	}
	fmt.Println("done")
}

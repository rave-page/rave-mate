//go:build manual

package midifbsrc

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"testing"
	"time"

	"rave.page/mate/internal/midi"
)

// TestLiveFeedbackClassify runs the real detector against the INSTALLED driver and prints
// (a) every play/pause classification change and (b) a 2s per-deck heartbeat with the raw
// feedback rate + the LAST note/vel seen (so a blink=paused vs silent=playing transition,
// and exactly how Serato ends it, are both visible next to the verdict). Read-only. Default
// 40s; RAVE_FB_SECONDS overrides. Run it, then play/pause your decks:
//
//	RAVE_FB_SECONDS=40 go test -tags manual -run TestLiveFeedbackClassify ./internal/session/sources/midifbsrc/ -v -count=1
func TestLiveFeedbackClassify(t *testing.T) {
	secs := 40
	if v := os.Getenv("RAVE_FB_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			secs = n
		}
	}
	dur := time.Duration(secs) * time.Second
	label := map[bool]string{true: "PLAYING", false: "paused "}

	d := newDetector()
	prev := map[string]bool{}
	rate := map[string]int{}
	lastEv := map[string]string{}
	seen := map[uint32]uint64{}
	start := time.Now()
	lastBeat := start
	fmt.Printf("=== detector live for %ds - play/pause your decks anytime ===\n", secs)

	for time.Since(start) < dur {
		if inputs, err := midi.QueryDriverInputs(); err == nil {
			for _, in := range inputs {
				if in.ReservedPortID == 0 {
					continue
				}
				es, err := midi.QueryDriverTrace(in.ReservedPortID)
				if err != nil {
					continue
				}
				mx := seen[in.ReservedPortID]
				for _, e := range es {
					if e.Seq > mx {
						mx = e.Seq
					}
					if e.Dir != midi.TraceDirFeedbackOut || e.Seq <= seen[in.ReservedPortID] || len(e.Bytes) < 3 {
						continue
					}
					hi := e.Bytes[0] & 0xF0
					if (hi == 0x90 || hi == 0x80) && int(e.Bytes[0]&0x0F) < maxDeckCh {
						dk := deckLetter(int(e.Bytes[0] & 0x0F))
						rate[dk]++
						lastEv[dk] = fmt.Sprintf("note%d vel%d", e.Bytes[1], e.Bytes[2])
					}
				}
				seen[in.ReservedPortID] = mx
			}
		}

		for deck, playing := range d.step(collect()) {
			if p, ok := prev[deck]; !ok || p != playing {
				fmt.Printf("[%5.1fs] CLASS  deck %s -> %s\n", time.Since(start).Seconds(), deck, label[playing])
				prev[deck] = playing
			}
		}

		if time.Since(lastBeat) >= 2*time.Second {
			decks := map[string]struct{}{}
			for dk := range rate {
				decks[dk] = struct{}{}
			}
			for dk := range prev {
				decks[dk] = struct{}{}
			}
			ks := make([]string, 0, len(decks))
			for dk := range decks {
				ks = append(ks, dk)
			}
			sort.Strings(ks)
			line := ""
			for _, dk := range ks {
				st := "?"
				if v, ok := prev[dk]; ok {
					st = label[v]
				}
				le := lastEv[dk]
				if le == "" {
					le = "-"
				}
				line += fmt.Sprintf("  %s[fb=%2d/2s last=%-11s %s]", dk, rate[dk], le, st)
			}
			fmt.Printf("[%5.1fs]%s\n", time.Since(start).Seconds(), line)
			rate = map[string]int{}
			lastBeat = time.Now()
		}
		time.Sleep(300 * time.Millisecond)
	}
	fmt.Println("=== done ===")
}

package midifbsrc

import "testing"

// A sustained LED flash (new feedback across >=flashStreak polls) reads as PAUSED.
func TestSustainedFlashIsPaused(t *testing.T) {
	d := newDetector()
	// poll 1: flash burst on deck A (warm-up: streak=1, not yet a flash)
	d.step([]fbEvent{{1, "A", true}, {2, "A", false}, {3, "A", true}, {4, "A", false}})
	// poll 2: more flash (new seqs 5,6) → streak=2 → paused
	r := d.step([]fbEvent{{3, "A", true}, {4, "A", false}, {5, "A", true}, {6, "A", false}})
	if r["A"] != false {
		t.Fatalf("sustained flash: deck A = %v, want false (paused)", r["A"])
	}
}

// When the flash STOPS (ring frozen: entries reappear but no new Seq) and the LED settled
// lit, the deck reads as PLAYING.
func TestFlashStopsThenPlaying(t *testing.T) {
	d := newDetector()
	d.step([]fbEvent{{1, "A", true}, {2, "A", false}, {3, "A", true}, {4, "A", false}}) // streak 1
	d.step([]fbEvent{{5, "A", true}, {6, "A", false}})                                  // streak 2 (paused)
	// play starts: NoteOff then solid NoteOn (new 7,8; max-seq event is lit)
	d.step([]fbEvent{{6, "A", false}, {7, "A", false}, {8, "A", true}}) // streak 3 (still flashing this poll)
	// next polls: ring frozen on the solid NoteOn - no new Seq
	d.step([]fbEvent{{7, "A", false}, {8, "A", true}}) // streak 0, ledOn=true
	r := d.step([]fbEvent{{7, "A", false}, {8, "A", true}})
	if r["A"] != true {
		t.Fatalf("settled lit LED: deck A = %v, want true (playing)", r["A"])
	}
}

// A dark LED (last event NoteOff / vel 0) with no flash reads as NOT playing.
func TestDarkLedNotPlaying(t *testing.T) {
	d := newDetector()
	r := d.step([]fbEvent{{1, "B", false}})
	if r["B"] != false {
		t.Fatalf("dark LED: deck B = %v, want false", r["B"])
	}
}

// Decks are independent: A flashing (paused) while B holds solid (playing).
func TestPerDeckIndependent(t *testing.T) {
	d := newDetector()
	d.step([]fbEvent{{1, "A", true}, {2, "A", false}, {10, "B", true}})
	d.step([]fbEvent{{3, "A", true}, {4, "A", false}, {10, "B", true}}) // A new (flash), B stale (frozen solid)
	r := d.step([]fbEvent{{5, "A", true}, {6, "A", false}, {10, "B", true}})
	if r["A"] != false {
		t.Fatalf("deck A = %v, want false (paused/flashing)", r["A"])
	}
	if r["B"] != true {
		t.Fatalf("deck B = %v, want true (settled lit)", r["B"])
	}
}

func TestDeckLetter(t *testing.T) {
	for ch, want := range map[int]string{0: "A", 1: "B", 2: "C", 3: "D"} {
		if got := deckLetter(ch); got != want {
			t.Errorf("deckLetter(%d)=%q want %q", ch, got, want)
		}
	}
}

package midifbsrc

import "testing"

// A sustained LED flash (new feedback across >=flashStreak polls) reads as PAUSED.
func TestSustainedFlashIsPaused(t *testing.T) {
	d := newDetector()
	d.step([]fbEvent{{1, "A"}, {2, "A"}}) // poll 1: streak 1 (transient)
	r := d.step([]fbEvent{{3, "A"}, {4, "A"}})
	if r["A"] != false {
		t.Fatalf("sustained flash: deck A = %v, want false (paused)", r["A"])
	}
}

// A deck that WAS flashing (paused) and then goes SILENT (no new feedback) reads as PLAYING -
// regardless of how Serato ended the flash (velocity is never consulted).
func TestFlashStopsThenPlaying(t *testing.T) {
	d := newDetector()
	d.step([]fbEvent{{1, "A"}, {2, "A"}}) // streak 1
	d.step([]fbEvent{{3, "A"}, {4, "A"}}) // streak 2 → paused (everFlashed)
	r := d.step([]fbEvent{{3, "A"}, {4, "A"}})
	if r["A"] != true {
		t.Fatalf("flash stopped: deck A = %v, want true (playing)", r["A"])
	}
}

// The first (transient) poll must NOT classify - a paused deck's opening flash frame should
// not momentarily read as playing.
func TestFirstPollNoBlip(t *testing.T) {
	d := newDetector()
	if r := d.step([]fbEvent{{1, "A"}, {2, "A"}}); len(r) != 0 {
		t.Fatalf("first poll classified %v, want nothing (transient)", r)
	}
}

// A deck never seen to sustain a flash (so we don't know it as a loaded/paused deck) is left
// UNCLASSIFIED - the Play button + History decide, not a guess.
func TestNeverFlashedNotClassified(t *testing.T) {
	d := newDetector()
	d.step([]fbEvent{{1, "B"}}) // one-poll twitch (streak 1), never a sustained flash
	r := d.step(nil)
	if _, ok := r["B"]; ok {
		t.Fatalf("never-flashed deck B classified %v, want unclassified", r["B"])
	}
}

// A PLAYING deck emits NO feedback (verified live: silent wire). Once it has flashed and gone
// silent it must stay PLAYING even across polls with no events at all (its ring entries age
// out under another deck's blink).
func TestPlayingDeckHeldWhenSilent(t *testing.T) {
	d := newDetector()
	d.step([]fbEvent{{1, "A"}})           // streak 1
	d.step([]fbEvent{{2, "A"}})           // streak 2 → paused (everFlashed)
	d.step([]fbEvent{{2, "A"}})           // settled → playing
	if r := d.step(nil); r["A"] != true { // no events at all
		t.Fatalf("silent aged-out deck A = %v, want PLAYING held", r["A"])
	}
	if r := d.step([]fbEvent{{99, "B"}}); r["A"] != true { // only B active
		t.Fatalf("deck A while only B has feedback = %v, want PLAYING held", r["A"])
	}
}

// Decks are independent: A flashing (paused) while B has flashed-then-settled (playing).
func TestPerDeckIndependent(t *testing.T) {
	d := newDetector()
	d.step([]fbEvent{{1, "A"}, {1, "B"}}) // both streak 1
	d.step([]fbEvent{{2, "A"}, {2, "B"}}) // both streak 2 → paused (everFlashed)
	d.step([]fbEvent{{3, "A"}, {2, "B"}}) // A keeps flashing; B settles → playing
	r := d.step([]fbEvent{{4, "A"}, {2, "B"}})
	if r["A"] != false {
		t.Fatalf("deck A = %v, want false (paused)", r["A"])
	}
	if r["B"] != true {
		t.Fatalf("deck B = %v, want true (playing)", r["B"])
	}
}

func TestDeckLetter(t *testing.T) {
	for ch, want := range map[int]string{0: "A", 1: "B", 2: "C", 3: "D"} {
		if got := deckLetter(ch); got != want {
			t.Errorf("deckLetter(%d)=%q want %q", ch, got, want)
		}
	}
}

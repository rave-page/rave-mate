package videoshare

import (
	"context"
	"image"
	"path/filepath"
	"testing"
	"time"

	"rave.page/mate/internal/deckcard"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/session"
)

// sendRec is one recorded Send: a copy of the frame bounds + whether every pixel is zero
// (fully transparent). The frame pointer is not retained (Send contract).
type sendRec struct {
	bounds  image.Rectangle
	allZero bool
}

// fakeSender records per-deck Send/Remove/Close calls. Drives Sink.publish directly (same package).
type fakeSender struct {
	sends   map[string][]sendRec
	removes map[string]int
	closed  int
}

func newFakeSender() *fakeSender {
	return &fakeSender{sends: map[string][]sendRec{}, removes: map[string]int{}}
}

func frameAllZero(img *image.NRGBA) bool {
	for _, p := range img.Pix {
		if p != 0 {
			return false
		}
	}
	return true
}

func (f *fakeSender) Send(deck string, img *image.NRGBA) error {
	f.sends[deck] = append(f.sends[deck], sendRec{bounds: img.Bounds(), allZero: frameAllZero(img)})
	return nil
}
func (f *fakeSender) Remove(deck string) error { f.removes[deck]++; return nil }
func (f *fakeSender) Close()                   { f.closed++ }

// onAirState builds a UnifiedState whose deck A passes the gate: loaded (title), playing, fader
// unknown → OnAir, not Ended, non-empty ArtKey, fresh.
func onAirState(now time.Time) session.UnifiedState {
	return session.UnifiedState{
		Decks: map[string]map[string]session.FieldValue{
			"A": {
				session.FieldTitle:     {Value: "Strobe", TS: now, Confidence: 1},
				session.FieldArtist:    {Value: "deadmau5", TS: now, Confidence: 1},
				session.FieldIsPlaying: {Value: true, TS: now, Confidence: 1},
			},
		},
	}
}

// TestPublishKeepAliveBlank: a deck going off-air gets ONE transparent frame (never Remove),
// blanks latch, and a return re-sends a real card. This is the interop-churn fix.
func TestPublishKeepAliveBlank(t *testing.T) {
	f, err := deckcard.LoadFacesScale(1)
	if err != nil {
		t.Skipf("deckcard faces unavailable: %v", err)
	}
	defer f.Close()

	s := New(logbus.New(16), nil, nil, nil, nil, filepath.Join(t.TempDir(), "overlay-style.json"))
	s.scale = 1 // publish reads s.scale (set by Start); faces loaded at the same scale
	fake := newFakeSender()
	ctx := context.Background()
	now := time.Now()

	// 1. Deck A on-air → exactly one Send, real card, sent latched.
	s.publish(ctx, fake, f, onAirState(now))
	if got := len(fake.sends["A"]); got != 1 {
		t.Fatalf("step1: want 1 send for A, got %d", got)
	}
	if fake.sends["A"][0].allZero {
		t.Fatalf("step1: first frame must be a real (non-transparent) card")
	}
	cardBounds := fake.sends["A"][0].bounds
	if !s.sent["A"] {
		t.Fatalf("step1: sent[A] not latched")
	}
	if s.blank["A"] {
		t.Fatalf("step1: blank[A] must be false on a real send")
	}

	// 2. Deck A gone → exactly ONE more Send, all-zero, bounds equal the card; zero Remove; sig cleared.
	s.publish(ctx, fake, f, session.UnifiedState{})
	if got := len(fake.sends["A"]); got != 2 {
		t.Fatalf("step2: want 2 total sends for A, got %d", got)
	}
	blank := fake.sends["A"][1]
	if !blank.allZero {
		t.Fatalf("step2: off-air frame must be fully transparent")
	}
	if blank.bounds != cardBounds {
		t.Fatalf("step2: blank bounds %v != card bounds %v (size mismatch re-links interop)", blank.bounds, cardBounds)
	}
	if fake.removes["A"] != 0 {
		t.Fatalf("step2: Remove must not be called, got %d", fake.removes["A"])
	}
	if _, ok := s.sigs["A"]; ok {
		t.Fatalf("step2: sig for A must be cleared")
	}
	if !s.sent["A"] || !s.blank["A"] {
		t.Fatalf("step2: sent must stay true and blank must latch (sent=%v blank=%v)", s.sent["A"], s.blank["A"])
	}

	// 3. Publish again, A still gone → NO further Send (blank latched).
	s.publish(ctx, fake, f, session.UnifiedState{})
	if got := len(fake.sends["A"]); got != 2 {
		t.Fatalf("step3: blank latched, want still 2 sends, got %d", got)
	}

	// 4. Deck A returns with the same track state → a real Send (sig was cleared) and blank unlatches.
	s.publish(ctx, fake, f, onAirState(now))
	if got := len(fake.sends["A"]); got != 3 {
		t.Fatalf("step4: want 3 sends after A returns, got %d", got)
	}
	if fake.sends["A"][2].allZero {
		t.Fatalf("step4: returning deck must send a real card, not a blank")
	}
	if s.blank["A"] {
		t.Fatalf("step4: blank must unlatch on the real send")
	}

	// 5. Never any Remove across the whole test.
	if fake.removes["A"] != 0 {
		t.Fatalf("Remove was called %d times; keep-alive must never Remove mid-session", fake.removes["A"])
	}
}

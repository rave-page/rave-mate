package pngsink

import (
	"context"
	"image"
	"os"
	"path/filepath"
	"testing"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/session"
)

func deck(letter, title string, onAir bool, fader float64) session.DeckSnapshot {
	return session.DeckSnapshot{
		Deck: letter, Title: title, Artist: "Some Artist", BPM: 128, Key: "8A",
		ElapsedTime: 65, TrackLength: 240, IsPlaying: true, OnAir: onAir,
		Fader: fader, HasFader: true,
		EQHigh: 0.5, EQMid: 0.5, EQLow: 0.5, Filter: 0.5, HasMixer: true,
		ArtKey: letter + "-" + title,
	}
}

func newSink(t *testing.T) (*Sink, string) {
	t.Helper()
	dir := t.TempDir()
	return New(logbus.New(16), func() string { return dir }, nil, nil, nil, filepath.Join(dir, "overlay-style.json")), dir
}

func pngPath(dir, letter string) string { return filepath.Join(dir, "deck_"+letter+".png") }

func decodes(t *testing.T, path string) image.Image {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	img, _, err := image.Decode(f)
	if err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return img
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestRenderOnAirDeck(t *testing.T) {
	s, dir := newSink(t)
	f, err := loadFaces()
	if err != nil {
		t.Fatalf("loadFaces: %v", err)
	}
	defer f.Close()

	// On-air deck A renders; B is decoded but never on-air → gated out, no file.
	s.applyGateThenRender(t, f, []session.DeckSnapshot{
		deck("A", "Track A", true, 0.9),
		deck("B", "Track B", false, 0.0),
	})

	pa := pngPath(dir, "A")
	if !exists(pa) {
		t.Fatalf("deck_A.png not written")
	}
	img := decodes(t, pa)
	if img.Bounds().Dx() != cardW || img.Bounds().Dy() != cardH {
		t.Fatalf("deck_A.png size = %v, want %dx%d", img.Bounds(), cardW, cardH)
	}
	if exists(pngPath(dir, "B")) {
		t.Fatalf("deck_B.png should be gated out (never on-air)")
	}
}

func TestGateRevealOnAir(t *testing.T) {
	s, dir := newSink(t)
	f, err := loadFaces()
	if err != nil {
		t.Fatalf("loadFaces: %v", err)
	}
	defer f.Close()

	// First: B cued (fader down, not on-air) → hidden.
	s.applyGateThenRender(t, f, []session.DeckSnapshot{deck("B", "Track B", false, 0.0)})
	if exists(pngPath(dir, "B")) {
		t.Fatalf("cued-not-played deck B should be hidden")
	}
	// Then: B faded in (on-air) → appears.
	s.applyGateThenRender(t, f, []session.DeckSnapshot{deck("B", "Track B", true, 0.8)})
	if !exists(pngPath(dir, "B")) {
		t.Fatalf("deck B should appear once on-air")
	}
}

func TestClearOnUnload(t *testing.T) {
	s, dir := newSink(t)
	f, err := loadFaces()
	if err != nil {
		t.Fatalf("loadFaces: %v", err)
	}
	defer f.Close()

	s.applyGateThenRender(t, f, []session.DeckSnapshot{deck("C", "Track C", true, 0.9)})
	if !exists(pngPath(dir, "C")) {
		t.Fatalf("deck_C.png expected")
	}
	// Unload: no decks → PNG removed.
	s.applyGateThenRender(t, f, nil)
	if exists(pngPath(dir, "C")) {
		t.Fatalf("deck_C.png should be removed on unload")
	}
}

func TestThrottleSkipsRedundant(t *testing.T) {
	s, dir := newSink(t)
	f, err := loadFaces()
	if err != nil {
		t.Fatalf("loadFaces: %v", err)
	}
	defer f.Close()

	d := deck("A", "Track A", true, 0.9)
	s.applyGateThenRender(t, f, []session.DeckSnapshot{d})
	st0, err := os.Stat(pngPath(dir, "A"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// Same content → signature unchanged → skip (no rewrite).
	s.applyGateThenRender(t, f, []session.DeckSnapshot{d})
	if s.sigs["A"] == "" {
		t.Fatalf("signature not recorded")
	}
	st1, err := os.Stat(pngPath(dir, "A"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !st1.ModTime().Equal(st0.ModTime()) {
		t.Fatalf("redundant render rewrote the file (modtime changed)")
	}
}

func TestTransparentBackground(t *testing.T) {
	s, dir := newSink(t)
	f, err := loadFaces()
	if err != nil {
		t.Fatalf("loadFaces: %v", err)
	}
	defer f.Close()

	s.applyGateThenRender(t, f, []session.DeckSnapshot{deck("A", "Track A", true, 0.9)})
	img := decodes(t, pngPath(dir, "A"))
	// Corner pixel (0,0) is outside the rounded card → transparent.
	_, _, _, a := img.At(0, 0).RGBA()
	if a != 0 {
		t.Fatalf("corner pixel alpha = %d, want 0 (transparent)", a)
	}
	// Centre pixel is opaque card.
	_, _, _, ca := img.At(cardW/2, cardH/2).RGBA()
	if ca == 0 {
		t.Fatalf("centre pixel transparent, want opaque card")
	}
}

// applyGateThenRender drives one render cycle from a synthetic overlay deck set,
// exercising the real gate + throttle + write path (renderDecks).
func (s *Sink) applyGateThenRender(t *testing.T, f *faces, decks []session.DeckSnapshot) {
	t.Helper()
	s.renderDecks(context.Background(), f, decks, time.Now())
}

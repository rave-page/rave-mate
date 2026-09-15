package virtualdjsrc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/session"
)

// newNetCtlServer scripts the Network Control plugin: deck ref "1" is a playing track, every
// other deck returns not-playing. Records each script query in seen.
func newNetCtlServer(t *testing.T, seen *sync.Map) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		script := r.URL.Query().Get("script")
		seen.Store(script, true)
		ref, verb, _ := strings.Cut(strings.TrimPrefix(script, "deck "), " ")
		switch verb {
		case "get_artist_title":
			_, _ = w.Write([]byte("Artist Name - Title Name"))
		case "get_bpm":
			_, _ = w.Write([]byte("128.0"))
		case "get_key":
			_, _ = w.Write([]byte("8A"))
		case "get_time elapsed":
			_, _ = w.Write([]byte("65000")) // ms → 65s
		case "play":
			if ref == "1" {
				_, _ = w.Write([]byte("true"))
			} else {
				_, _ = w.Write([]byte("false"))
			}
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
}

// TestPollNetCtlParsesFields: a "true" play response → FieldIsPlaying:true, "false" → false,
// and artist/title/bpm/key/elapsed parse into the right fields with the correct scope+source.
func TestPollNetCtlParsesFields(t *testing.T) {
	var seen sync.Map
	srv := newNetCtlServer(t, &seen)
	defer srv.Close()

	s := New(logbus.New(16), Config{NetCtl: true})
	client := srv.Client()
	last := map[string]string{}
	ctx := context.Background()

	// Deck A (ref "1") = playing track: every field parses.
	a := session.Scope{Kind: session.ScopeDeck, ID: "A"}
	obs, changed, err := s.pollNetCtl(ctx, client, srv.URL, netCtlTarget{ref: "1", scope: a}, last)
	if err != nil {
		t.Fatalf("pollNetCtl deck A: %v", err)
	}
	if obs == nil {
		t.Fatal("deck A: nil observation")
	}
	if obs.Source != session.SourceVDJNetCtl {
		t.Errorf("deck A source: got %q want %q", obs.Source, session.SourceVDJNetCtl)
	}
	if obs.Scope != a {
		t.Errorf("deck A scope: got %+v want %+v", obs.Scope, a)
	}
	if obs.Fields[session.FieldIsPlaying] != true {
		t.Errorf(`deck A isPlaying: got %v want true (proves "play" verb → parseBool("true"))`, obs.Fields[session.FieldIsPlaying])
	}
	if obs.Fields[session.FieldArtist] != "Artist Name" {
		t.Errorf("deck A artist: got %v", obs.Fields[session.FieldArtist])
	}
	if obs.Fields[session.FieldTitle] != "Title Name" {
		t.Errorf("deck A title: got %v", obs.Fields[session.FieldTitle])
	}
	if obs.Fields[session.FieldBPM] != 128.0 {
		t.Errorf("deck A bpm: got %v want 128", obs.Fields[session.FieldBPM])
	}
	if obs.Fields[session.FieldKey] != "8A" {
		t.Errorf("deck A key: got %v want 8A", obs.Fields[session.FieldKey])
	}
	if obs.Fields[session.FieldElapsedTime] != 65.0 {
		t.Errorf("deck A elapsed: got %v want 65", obs.Fields[session.FieldElapsedTime])
	}
	if !changed {
		t.Error("deck A: first non-empty track should flag changed (Loaded boundary)")
	}

	// Deck B (ref "2") = not playing.
	b := session.Scope{Kind: session.ScopeDeck, ID: "B"}
	obsB, _, err := s.pollNetCtl(ctx, client, srv.URL, netCtlTarget{ref: "2", scope: b}, last)
	if err != nil {
		t.Fatalf("pollNetCtl deck B: %v", err)
	}
	if obsB.Fields[session.FieldIsPlaying] != false {
		t.Errorf(`deck B isPlaying: got %v want false (parseBool("false"))`, obsB.Fields[session.FieldIsPlaying])
	}
}

// TestNetCtlPollsFourDecksWithPlayVerb runs the production poller against the scripted server
// and proves F1 (corrected `play` verb, no `is playing`) + F3 (decks 1..4 polled), plus that
// each deck maps to Scope{ScopeDeck,"A".."D"} and master to ScopeMaster.
func TestNetCtlPollsFourDecksWithPlayVerb(t *testing.T) {
	var seen sync.Map
	srv := newNetCtlServer(t, &seen)
	defer srv.Close()

	s := New(logbus.New(16), Config{NetCtl: true, NetCtlURL: srv.URL})

	var mu sync.Mutex
	got := map[string]session.Observation{} // scope key → last observation
	emit := func(o session.Observation) {
		mu.Lock()
		got[o.Scope.Key()] = o
		mu.Unlock()
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.runNetCtl(ctx, emit); close(done) }()

	// Wait (bounded) until the first full poll tick emitted all 5 scopes.
	deadline := time.After(5 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 5 {
			break
		}
		select {
		case <-deadline:
			cancel()
			<-done
			t.Fatalf("only %d scopes emitted before timeout", n)
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done

	// F1 + F3: the exact `play` query for master + decks 1..4 must have been sent.
	for _, script := range []string{"deck master play", "deck 1 play", "deck 2 play", "deck 3 play", "deck 4 play"} {
		if _, ok := seen.Load(script); !ok {
			t.Errorf("missing query %q", script)
		}
	}
	// The other per-deck queries are sent too (spot-check deck 1).
	for _, script := range []string{"deck 1 get_artist_title", "deck 1 get_bpm", "deck 1 get_key", "deck 1 get_time elapsed"} {
		if _, ok := seen.Load(script); !ok {
			t.Errorf("missing query %q", script)
		}
	}
	// The stale "is playing" form must never be sent (regression guard for F1).
	seen.Range(func(k, _ any) bool {
		if strings.Contains(k.(string), "is playing") {
			t.Errorf("stale query form still sent: %q", k)
		}
		return true
	})

	// Per-deck scope IDs A..D + master.
	for _, want := range []session.Scope{
		{Kind: session.ScopeMaster},
		{Kind: session.ScopeDeck, ID: "A"},
		{Kind: session.ScopeDeck, ID: "B"},
		{Kind: session.ScopeDeck, ID: "C"},
		{Kind: session.ScopeDeck, ID: "D"},
	} {
		o, ok := got[want.Key()]
		if !ok {
			t.Errorf("no observation for scope %s", want.Key())
			continue
		}
		if o.Scope != want {
			t.Errorf("scope %s: got %+v", want.Key(), o.Scope)
		}
	}
	if got[(session.Scope{Kind: session.ScopeDeck, ID: "A"}).Key()].Fields[session.FieldIsPlaying] != true {
		t.Error("deck A (ref 1) should be playing")
	}
	if got[(session.Scope{Kind: session.ScopeDeck, ID: "B"}).Key()].Fields[session.FieldIsPlaying] != false {
		t.Error("deck B (ref 2) should not be playing")
	}
}

package webui

// The media plane after the B5 split: the player's <video>/__mse fetches originate in the WINDOW
// CHILD's process, so the per-file token table has to keep authorizing them - and the child's origin
// has to be an identity the daemon HANDED it, not "any loopback caller holding the token".
// mediahttp_owner_test.go covers the eviction-owner rules; this file covers the session scoping the
// procShell adds, and that the default (unsessioned) URLs are byte-for-byte what they always were.

import (
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func newSessSrv() *mpMediaSrv {
	return &mpMediaSrv{tokens: map[string]string{}, owner: map[string]*UI{}, byPath: map[string]string{},
		imgTokens: map[string]imgReq{}, imgByKey: map[string]string{}, imgKeyByTok: map[string]string{},
		imgCache: map[string][]byte{}}
}

// Unsessioned = the historic 2-segment route, unchanged: the in-proc shell, the Fyne renderer and
// headless mirror sessions must see zero difference.
func TestMediaRouteUnsessionedIsUnchanged(t *testing.T) {
	s := newSessSrv()
	if got, ok := s.routeToken("/m/deadbeef", "/m/"); !ok || got != "deadbeef" {
		t.Fatalf("routeToken = %q,%v; want the bare token", got, ok)
	}
	if _, ok := s.routeToken("/m/", "/m/"); ok {
		t.Error("an empty token must be refused")
	}
	if got, ok := s.routeToken("/img/cafe", "/img/"); !ok || got != "cafe" {
		t.Fatalf("img routeToken = %q,%v", got, ok)
	}
	if s.sessPrefixLocked() != "" {
		t.Error("no session must mean no path segment")
	}
}

// With a session active the URL carries it, and only a LIVE session id is served: the child's
// fetches are authorized by the identity the daemon gave it in the init payload.
func TestMediaRouteRequiresTheShellSession(t *testing.T) {
	s := newSessSrv()
	sess := s.newSession()
	if sess == "" {
		t.Fatal("newSession failed")
	}
	if got := s.sessPrefixLocked(); got != sess+"/" {
		t.Fatalf("sessPrefixLocked = %q", got)
	}
	if got, ok := s.routeToken("/m/"+sess+"/tok1", "/m/"); !ok || got != "tok1" {
		t.Fatalf("live session refused: %q,%v", got, ok)
	}
	if _, ok := s.routeToken("/m/tok1", "/m/"); ok {
		t.Error("an unsessioned URL must be refused once sessions are active")
	}
	if _, ok := s.routeToken("/m/"+strings.Repeat("f", len(sess))+"/tok1", "/m/"); ok {
		t.Error("a foreign session id was served")
	}
	if _, ok := s.routeToken("/m/"+sess+"/", "/m/"); ok {
		t.Error("a live session with an empty token was served")
	}
}

// A child restart re-mints the session. The PREVIOUS one stays valid (a <video> mid-play must not
// break under the restart) but the one before that is refused - the window is bounded.
func TestMediaSessionKeepsOneGenerationThenRefuses(t *testing.T) {
	s := newSessSrv()
	gen1 := s.newSession()
	gen2 := s.newSession()
	for _, sess := range []string{gen1, gen2} {
		if _, ok := s.routeToken("/m/"+sess+"/tok", "/m/"); !ok {
			t.Errorf("session %s… should still be served", sess[:6])
		}
	}
	gen3 := s.newSession()
	if _, ok := s.routeToken("/m/"+gen1+"/tok", "/m/"); ok {
		t.Error("a two-generations-old session URL was still served")
	}
	for _, sess := range []string{gen2, gen3} {
		if _, ok := s.routeToken("/m/"+sess+"/tok", "/m/"); !ok {
			t.Errorf("session %s… should be served", sess[:6])
		}
	}
	if len(s.sess) != mediaSessMax {
		t.Errorf("live sessions = %d, want the cap %d", len(s.sess), mediaSessMax)
	}
}

// End to end over the real handler: the child's request (current session + its file token) is served;
// the same token under a retired session 404s. Proves the check by execution, not by inspection.
func TestMediaServeAuthorizesTheChildSessionOnly(t *testing.T) {
	dir := t.TempDir()
	file := dir + "/clip.bin"
	if err := os.WriteFile(file, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newSessSrv()
	u := &UI{}
	tok := "filetok"
	s.tokens[tok] = file
	s.owner[tok] = u
	s.byPath[file] = tok
	s.order = []string{tok}

	dead := s.newSession() // retired below
	_ = s.newSession()     // gen 2
	live := s.newSession() // gen 3 - `dead` is now out of the window

	get := func(path string) int {
		rec := httptest.NewRecorder()
		s.serve(rec, httptest.NewRequest("GET", path, nil))
		return rec.Code
	}
	if code := get("/m/" + live + "/" + tok); code != 200 {
		t.Errorf("the child's own session was refused: %d", code)
	}
	if code := get("/m/" + dead + "/" + tok); code != 404 {
		t.Errorf("a retired session was served: %d", code)
	}
	if code := get("/m/" + tok); code != 404 {
		t.Errorf("an unsessioned URL was served while sessions are active: %d", code)
	}
	if code := get("/m/" + live + "/nope"); code != 404 {
		t.Errorf("an unknown token was served: %d", code)
	}
	// /mi/ (the __mse index the child's runtime fetches) enforces the same gate before it ever looks
	// at the store - a retired session must not even reach the owner check.
	if code := get("/mi/" + dead + "/" + tok); code != 404 {
		t.Errorf("index route served a retired session: %d", code)
	}
}

// The minted URL shape is exactly what the routes parse back, for every route the page fetches.
func TestMediaURLShapeRoundTrips(t *testing.T) {
	s := newSessSrv()
	sess := s.newSession()
	for _, route := range []string{"/m/", "/mi/", "/img/"} {
		url := fmt.Sprintf("http://127.0.0.1:%d%s%s%s", 4711, route, sess+"/", "tok")
		path := strings.TrimPrefix(url, fmt.Sprintf("http://127.0.0.1:%d", 4711))
		got, ok := s.routeToken(path, route)
		if !ok || got != "tok" {
			t.Errorf("%s round trip = %q,%v", route, got, ok)
		}
	}
}

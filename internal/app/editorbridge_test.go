package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/gistseq"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/matebridge"
	"rave.page/mate/internal/matepreset"
	"rave.page/mate/internal/vrchat"
)

// fakeRoster is an always-available RosterPublisher for the HTTP integration test.
type fakeRoster struct{ names []string }

func (f *fakeRoster) Available() bool { return true }
func (f *fakeRoster) PublishRoster(_ context.Context, _, name string, names []string) (string, string, string, int64, error) {
	f.names = names
	return "gid", "https://gist.githubusercontent.com/o/gid/raw/allow.txt", "https://gist.githubusercontent.com/o/gid/raw/allow.json", 1, nil
}

// buildTestServer wires the REAL app-side gateways (logged-out Directory, file preset store, settings)
// against a temp dir - no network, no full app boot.
func buildTestServer(t *testing.T) (*matebridge.Server, *config.WorldSyncFeature, *fakeRoster) {
	t.Helper()
	dir := t.TempDir()
	seq := gistseq.Open(filepath.Join(dir, "seq.json"))
	f := &config.WorldSyncFeature{Enabled: true, PointerGistID: "pg"}
	roster := &fakeRoster{}
	srv := matebridge.New(matebridge.Options{
		Token:     "t",
		Version:   "test-app",
		Directory: &directoryGateway{mgr: vrchat.NewManager(logbus.New(8), nil), enabled: func() bool { return true }},
		Presets:   matepreset.NewStore(filepath.Join(dir, "presets"), seq),
		Settings:  &settingsGateway{cfg: func() *config.WorldSyncFeature { return f }, owner: func() string { return "octo" }, seq: seq},
		Roster:    roster,
	})
	return srv, f, roster
}

func do(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, matebridge.PathPrefix+path, rd)
	req.Header.Set(matebridge.AuthHeader, "Bearer t")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// TestEditorBridgeHTTP drives the wired loopback server end-to-end: preset round-trip through the
// real file store, logged-out vrchat 501, settings moduleUrls, and roster publish - the app-side
// contract behaviour, not just a clean build.
func TestEditorBridgeHTTP(t *testing.T) {
	srv, _, roster := buildTestServer(t)
	h := srv.Handler()

	// health: presets + settings + worldsync available now; vrchat NOT (logged out) => greyed.
	hw := do(t, h, http.MethodGet, "/health", nil)
	if hw.Code != http.StatusOK {
		t.Fatalf("health = %d", hw.Code)
	}
	var health matebridge.Health
	if err := json.Unmarshal(hw.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	caps := map[string]bool{}
	for _, c := range health.Capabilities {
		caps[c] = true
	}
	if caps[matebridge.CapVRChat] {
		t.Fatal("vrchat advertised while logged out")
	}
	if !caps[matebridge.CapPresets] || !caps[matebridge.CapSettings] || !caps[matebridge.CapWorldSync] {
		t.Fatalf("missing caps: %v", health.Capabilities)
	}

	// logged-out vrchat route => 501 (greyed), never a panic.
	if w := do(t, h, http.MethodGet, "/vrchat/friends", nil); w.Code != http.StatusNotImplemented {
		t.Fatalf("logged-out friends = %d, want 501", w.Code)
	}

	// preset PUT -> GET -> LIST through the real file store.
	put := do(t, h, http.MethodPut, "/presets/backdrop/skyline", matebridge.PresetEnvelope{
		Kind: "backdrop", ID: "skyline", Name: "Skyline", Source: "unity",
		Payload: json.RawMessage(`{"minShellRadius":50}`),
	})
	if put.Code != http.StatusOK {
		t.Fatalf("preset put = %d: %s", put.Code, put.Body)
	}
	var pr matebridge.PresetPutResponse
	if err := json.Unmarshal(put.Body.Bytes(), &pr); err != nil {
		t.Fatal(err)
	}
	if !pr.OK || pr.Seq != 1 {
		t.Fatalf("put response = %+v", pr)
	}
	get := do(t, h, http.MethodGet, "/presets/backdrop/skyline", nil)
	if get.Code != http.StatusOK {
		t.Fatalf("preset get = %d", get.Code)
	}
	var pe matebridge.PresetEnvelope
	if err := json.Unmarshal(get.Body.Bytes(), &pe); err != nil {
		t.Fatal(err)
	}
	if pe.ID != "skyline" || pe.Seq != 1 || string(pe.Payload) == "" {
		t.Fatalf("preset get body = %+v", pe)
	}
	lst := do(t, h, http.MethodGet, "/presets?kind=backdrop", nil)
	var pl matebridge.PresetListResponse
	if err := json.Unmarshal(lst.Body.Bytes(), &pl); err != nil {
		t.Fatal(err)
	}
	if len(pl.Presets) != 1 {
		t.Fatalf("list = %d presets", len(pl.Presets))
	}
	// unknown kind => 400 (ErrBadRequest mapping), not 502.
	if w := do(t, h, http.MethodGet, "/presets?kind=bogus", nil); w.Code != http.StatusBadRequest {
		t.Fatalf("bad kind = %d, want 400", w.Code)
	}

	// settings: moduleUrls stamped from the pointer gist id + owner.
	sw := do(t, h, http.MethodGet, "/settings/proj1", nil)
	var st matebridge.Settings
	if err := json.Unmarshal(sw.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if len(st.ModuleURLs) != 1 || st.ModuleURLs[0] != "https://gist.githubusercontent.com/octo/pg/raw/pointer.json" {
		t.Fatalf("settings moduleUrls = %v", st.ModuleURLs)
	}

	// roster publish: names forwarded, world-facing URLs returned.
	rw := do(t, h, http.MethodPost, "/worldsync/gist", matebridge.PublishRosterRequest{
		Kind: "perm", Name: "Lineup", Names: []string{"DJ Nyx", "VJ Kilo"},
	})
	if rw.Code != http.StatusOK {
		t.Fatalf("roster publish = %d: %s", rw.Code, rw.Body)
	}
	var rr matebridge.PublishRosterResponse
	if err := json.Unmarshal(rw.Body.Bytes(), &rr); err != nil {
		t.Fatal(err)
	}
	if rr.RawURL == "" || rr.Seq != 1 || len(roster.names) != 2 {
		t.Fatalf("roster response = %+v names=%v", rr, roster.names)
	}
}

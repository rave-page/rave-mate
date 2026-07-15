package vrcperm

import (
	"context"
	"encoding/json"
	"testing"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/matebridge"
)

// fakeHosted is an in-memory HostedClient for hosted-mode tests.
type fakeHosted struct {
	ready       bool
	reason      string
	calls       int
	lastModule  string
	lastPayload []byte
	seq         int64
	failErr     error
}

func (h *fakeHosted) Ready() (bool, string) {
	if h.ready {
		return true, ""
	}
	return false, h.reason
}

func (h *fakeHosted) PublishModule(_ context.Context, moduleKey string, payload []byte) (string, string, int64, error) {
	h.calls++
	h.lastModule = moduleKey
	h.lastPayload = append([]byte(nil), payload...)
	if h.failErr != nil {
		return "", "", 0, h.failErr
	}
	h.seq++
	return "https://gist.githubusercontent.com/rave-page/hg/raw/" + moduleKey + ".json", "hg", h.seq, nil
}

// TestPublishConfigHosted proves hosted mode PUTs the RAW payload (no local envelope/seq), persists
// the server-returned raw URL + seq into LiveModules (mode-agnostic), and is diff-only on the inner
// payload.
func TestPublishConfigHosted(t *testing.T) {
	f := &config.WorldSyncFeature{Enabled: true, PublishMode: config.WorldSyncModeHosted, HostedWorldID: "wrld_x"}
	s, saves := newTestService(f, &fakeGists{}, &fakeMembers{})
	h := &fakeHosted{ready: true}
	s.hosted = h

	c := matebridge.ConfigModule{Profiles: []matebridge.ConfigProfile{{ID: "main", Values: map[string]string{"a": "1"}}}}
	s.PublishConfig(context.Background(), c)

	if h.calls != 1 || h.lastModule != matebridge.ModuleConfig {
		t.Fatalf("hosted publish: calls=%d module=%q err=%q", h.calls, h.lastModule, s.Status("config").Err)
	}
	// Payload sent RAW: the ConfigModule object, NOT the enveloped {schema,seq,...} form.
	var top map[string]json.RawMessage
	if err := json.Unmarshal(h.lastPayload, &top); err != nil {
		t.Fatalf("payload not JSON: %v (%s)", err, h.lastPayload)
	}
	if _, ok := top["profiles"]; !ok {
		t.Fatalf("hosted payload missing raw module field: %s", h.lastPayload)
	}
	for _, k := range []string{"schema", "seq", "contractVersion", "updatedAt"} {
		if _, ok := top[k]; ok {
			t.Fatalf("hosted payload must NOT be enveloped, found %q: %s", k, h.lastPayload)
		}
	}
	// Server-owned raw URL + seq persisted mode-agnostically for the editor bridge.
	lm := f.LiveModules["config"]
	if lm.RawURL != "https://gist.githubusercontent.com/rave-page/hg/raw/config.json" || lm.Seq != 1 {
		t.Fatalf("LiveModules[config] = %+v", lm)
	}
	if st := s.Status("config"); st.URL != lm.RawURL || st.Skipped {
		t.Fatalf("status = %+v", st)
	}
	if *saves == 0 {
		t.Fatal("config not persisted after hosted publish")
	}

	// Republish identical => diff-only skip (no second PUT, seq stays).
	s.PublishConfig(context.Background(), c)
	if h.calls != 1 {
		t.Fatalf("diff-only violated: calls=%d", h.calls)
	}
	if !s.Status("config").Skipped {
		t.Fatal("want Skipped on identical hosted config")
	}

	// Changed payload => second PUT, seq advances.
	c.Profiles[0].Values["a"] = "2"
	s.PublishConfig(context.Background(), c)
	if h.calls != 2 || f.LiveModules["config"].Seq != 2 {
		t.Fatalf("changed hosted config: calls=%d seq=%d", h.calls, f.LiveModules["config"].Seq)
	}
}

// TestPublishHostedNotReady proves a hosted publish with an unready client records the reason and
// never writes (no silent fallback to the direct gist path).
func TestPublishHostedNotReady(t *testing.T) {
	f := &config.WorldSyncFeature{Enabled: true, PublishMode: config.WorldSyncModeHosted}
	s, _ := newTestService(f, &fakeGists{}, &fakeMembers{})
	h := &fakeHosted{ready: false, reason: "no hosted world id set"}
	s.hosted = h

	s.PublishConfig(context.Background(), matebridge.ConfigModule{})
	if h.calls != 0 {
		t.Fatalf("published while not ready: calls=%d", h.calls)
	}
	if got := s.Status("config").Err; got != "no hosted world id set" {
		t.Fatalf("status err = %q", got)
	}
}

// TestPublishHostedMissingClient proves hosted mode with no wired client surfaces an error rather
// than silently writing a gist via the direct path.
func TestPublishHostedMissingClient(t *testing.T) {
	f := &config.WorldSyncFeature{Enabled: true, PublishMode: config.WorldSyncModeHosted}
	gists := &fakeGists{}
	s, _ := newTestService(f, gists, &fakeMembers{})
	s.seq = newFakeSeq()
	// s.hosted left nil.

	s.PublishConfig(context.Background(), matebridge.ConfigModule{})
	if gists.creates != 0 {
		t.Fatalf("hosted-mode publish fell through to a direct gist write: creates=%d", gists.creates)
	}
	if s.Status("config").Err == "" {
		t.Fatal("want an error when hosted mode has no client")
	}
}

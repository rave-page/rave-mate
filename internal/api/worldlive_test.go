package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"rave.page/mate/internal/logbus"
)

// TestPublishWorldLive proves the hosted publish request is shaped right (PUT, per-module path,
// Bearer token, raw JSON body forwarded verbatim) and the WorldLiveModuleOut is parsed.
func TestPublishWorldLive(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotCT, gotQuery string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotAuth, gotCT, gotQuery = r.Header.Get("Authorization"), r.Header.Get("Content-Type"), r.URL.RawQuery
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"world_id":"wrld_x","user_id":"u1","module":"config",` +
			`"schema":"rave.live/config@1","gist_id":"g1",` +
			`"raw_url":"https://gist.githubusercontent.com/rave-page/g1/raw/config.json",` +
			`"seq":7,"updated_at":"2026-07-15T12:00:00Z"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, logbus.New(8))
	payload := []byte(`{"profiles":[{"id":"main"}]}`)
	out, err := c.PublishWorldLive(context.Background(), "tok", "wrld_x", "config", payload)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	if gotPath != "/worlds/wrld_x/live/config" {
		t.Errorf("path = %q", gotPath)
	}
	if gotQuery != "" {
		t.Errorf("user_id must default to caller (no query), got %q", gotQuery)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("auth = %q", gotAuth)
	}
	if gotCT != "application/json" {
		t.Errorf("content-type = %q", gotCT)
	}
	if string(gotBody) != string(payload) {
		t.Errorf("body = %q, want raw payload %q", gotBody, payload)
	}
	if out.Seq != 7 || out.RawURL != "https://gist.githubusercontent.com/rave-page/g1/raw/config.json" || out.GistID != "g1" || out.Schema != "rave.live/config@1" {
		t.Errorf("parsed out = %+v", out)
	}
}

// TestWorldLiveErrorMapping proves each RFC-7807 problem+json status maps to a *WorldLiveError
// carrying status + details.code + message + trace id (never a panic).
func TestWorldLiveErrorMapping(t *testing.T) {
	cases := []struct {
		status int
		code   string
		msg    string
	}{
		{http.StatusUnauthorized, "UNAUTHORIZED", "Authentication required"},
		{http.StatusNotFound, "NOT_FOUND", "Not found"},
		{http.StatusUnprocessableEntity, "VALIDATION_FAILED", "world_id must be 1-128 chars of [A-Za-z0-9._-]"},
		{http.StatusBadGateway, "EXTERNAL_UNAVAILABLE", "GitHub gist API unavailable"},
		{http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "Datastore temporarily unavailable"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.code, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/problem+json")
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"status":   "error",
					"trace_id": "tr-1",
					"message":  tc.msg,
					"details":  map[string]any{"code": tc.code},
				})
			}))
			defer srv.Close()

			c := New(srv.URL, logbus.New(8))
			_, err := c.PublishWorldLive(context.Background(), "tok", "wrld_x", "config", []byte(`{}`))
			var we *WorldLiveError
			if !errors.As(err, &we) {
				t.Fatalf("err = %v (%T), want *WorldLiveError", err, err)
			}
			if we.Status != tc.status || we.Code != tc.code || we.Message != tc.msg || we.TraceID != "tr-1" {
				t.Fatalf("mapped = %+v", we)
			}
			if (tc.status == http.StatusNotFound) != we.NotFound() {
				t.Fatalf("NotFound() = %v for status %d", we.NotFound(), tc.status)
			}
		})
	}
}

// TestListWorldLive proves the list unwraps {items:[...]}.
func TestListWorldLive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/worlds/wrld_x/live" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"module":"config","seq":1},{"module":"pointer","seq":3}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, logbus.New(8))
	items, err := c.ListWorldLive(context.Background(), "tok", "wrld_x")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 2 || items[0].Module != "config" || items[1].Seq != 3 {
		t.Fatalf("items = %+v", items)
	}
}

package automation

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuildRenameNoEventFallback: when no booked event matches the recording's timestamp,
// rename-from-event must NOT skip - it fills {venueSlug}/{eventSlug} with the no-event
// placeholder so the file still gets renamed (graceful unfillable-variable handling).
func TestBuildRenameNoEventFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/auth/me") {
			_, _ = io.WriteString(w, `{"id":"u1"}`)
			return
		}
		_, _ = io.WriteString(w, `[]`) // no events → no match
	}))
	defer srv.Close()

	m := newTestSvc(t)
	m.SetBackgroundCredentials(srv.URL, "tok")

	src := filepath.Join(t.TempDir(), "set.wav")
	if err := os.WriteFile(src, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	rc := &runContext{currentPath: src}

	proposal, proposed, skip, err := m.buildRename(context.Background(), rc, Action{Type: ActionRename})
	if err != nil {
		t.Fatalf("buildRename: %v", err)
	}
	if skip != "" {
		t.Fatalf("expected no skip, got %q", skip)
	}
	if proposal == nil || proposed == "" {
		t.Fatal("expected a rename proposal, got none")
	}
	if !strings.Contains(filepath.Base(proposed), noEventSlug) {
		t.Fatalf("expected %q placeholder in %q", noEventSlug, filepath.Base(proposed))
	}
}

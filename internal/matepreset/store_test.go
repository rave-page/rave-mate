package matepreset

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"rave.page/mate/internal/matebridge"
)

// compact normalizes JSON whitespace so a semantic (not byte) opaque round-trip can be asserted -
// the stored envelope is pretty-printed, which reindents the embedded RawMessage payload without
// changing its data.
func compact(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestPutGetListRoundTrip(t *testing.T) {
	s := NewStore(t.TempDir(), nil)
	ctx := context.Background()
	payload := json.RawMessage(`{"parallaxExaggeration":1.5,"minShellRadius":50}`)

	seq, err := s.Put(ctx, matebridge.PresetBackdrop, "skyline", matebridge.PresetEnvelope{
		Name: "Skyline", Source: "unity", CoordSpace: "world", Payload: payload,
	})
	if err != nil || seq != 1 {
		t.Fatalf("put: seq=%d err=%v", seq, err)
	}

	got, err := s.Get(ctx, matebridge.PresetBackdrop, "skyline")
	if err != nil || got == nil {
		t.Fatalf("get: %v %v", got, err)
	}
	// Payload survives verbatim (data-identical); metadata normalized.
	if compact(t, got.Payload) != compact(t, payload) {
		t.Fatalf("payload mutated: %s", got.Payload)
	}
	if got.Schema != matebridge.PresetSchema || got.Kind != matebridge.PresetBackdrop ||
		got.ID != "skyline" || got.ContractVersion != matebridge.ContractVersion || got.Seq != 1 {
		t.Fatalf("metadata not normalized: %+v", got)
	}

	// Second preset gets the next seq; List(sinceSeq=1) returns only it.
	if _, err := s.Put(ctx, matebridge.PresetBackdrop, "grid", matebridge.PresetEnvelope{Name: "Grid", Payload: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	since, err := s.List(ctx, matebridge.PresetBackdrop, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(since) != 1 || since[0].ID != "grid" || since[0].Seq != 2 {
		t.Fatalf("since-seq list wrong: %+v", since)
	}
	all, _ := s.List(ctx, matebridge.PresetBackdrop, 0)
	if len(all) != 2 {
		t.Fatalf("full list = %d, want 2", len(all))
	}
}

func TestUnknownKindAndTraversalRejected(t *testing.T) {
	s := NewStore(t.TempDir(), nil)
	ctx := context.Background()
	if _, err := s.Put(ctx, "bogus", "x", matebridge.PresetEnvelope{}); !errors.Is(err, matebridge.ErrBadRequest) {
		t.Fatalf("unknown kind err = %v, want ErrBadRequest", err)
	}
	for _, id := range []string{"../escape", "a/b", ".."} {
		if _, err := s.Put(ctx, matebridge.PresetBackdrop, id, matebridge.PresetEnvelope{Payload: json.RawMessage(`{}`)}); !errors.Is(err, matebridge.ErrBadRequest) {
			t.Fatalf("traversal id %q err = %v, want ErrBadRequest", id, err)
		}
	}
}

func TestGetMissingIsNilNotError(t *testing.T) {
	s := NewStore(t.TempDir(), nil)
	got, err := s.Get(context.Background(), matebridge.PresetFoliage, "nope")
	if err != nil || got != nil {
		t.Fatalf("missing preset: got=%v err=%v (want nil,nil)", got, err)
	}
	// List of a never-used kind is empty, not an error.
	out, err := s.List(context.Background(), matebridge.PresetFoliage, 0)
	if err != nil || len(out) != 0 {
		t.Fatalf("empty-kind list: %v %v", out, err)
	}
}

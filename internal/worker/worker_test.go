package worker

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestServeDispatch runs the worker request/response loop over in-memory streams and
// checks dispatch, the ping handler, and the unknown-method path.
func TestServeDispatch(t *testing.T) {
	in := strings.NewReader(
		`{"id":"1","method":"ping"}` + "\n" +
			`{"id":"2","method":"nope"}` + "\n",
	)
	var out bytes.Buffer
	if code := serve(probeHandlers(), in, &out); code != 0 {
		t.Fatalf("serve exit code = %d, want 0", code)
	}

	dec := json.NewDecoder(&out)
	var r1, r2 Response
	if err := dec.Decode(&r1); err != nil {
		t.Fatal(err)
	}
	if err := dec.Decode(&r2); err != nil {
		t.Fatal(err)
	}
	if r1.ID != "1" || !r1.OK {
		t.Fatalf("ping resp bad: %+v", r1)
	}
	var pong map[string]any
	if err := json.Unmarshal(r1.Result, &pong); err != nil || pong["pong"] != true {
		t.Fatalf("ping result bad: %s", r1.Result)
	}
	if r2.ID != "2" || r2.OK || !strings.Contains(r2.Error, "unknown method") {
		t.Fatalf("unknown-method resp bad: %+v", r2)
	}
}

func TestKnownType(t *testing.T) {
	if !KnownType("probe") {
		t.Error("probe should be a known type")
	}
	if KnownType("bogus") {
		t.Error("bogus should not be known")
	}
}

func TestBucketPeaks(t *testing.T) {
	// 4 samples: 0, 16384, -32768, 8192 → 2 buckets: max(0,16384)=16384→128, max(32768→clamps,8192)=255
	pcm := []byte{0, 0, 0, 0x40, 0, 0x80, 0, 0x20}
	got := bucketPeaks(pcm, 2)
	if len(got) != 2 || got[0] != 128 || got[1] != 255 {
		t.Fatalf("bucketPeaks = %v, want [128 255]", got)
	}
	// fewer samples than buckets → shrink, never divide by zero
	if got := bucketPeaks(pcm, 100); len(got) != 4 {
		t.Fatalf("shrink: %d buckets, want 4", len(got))
	}
}

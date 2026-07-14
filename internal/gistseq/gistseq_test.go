package gistseq

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMonotonicAcrossReopen proves seq strictly increases per module AND survives a restart (the
// SEQ-GATE's hard requirement): a fresh Open must continue past the persisted high-water, never
// re-issue a committed seq.
func TestMonotonicAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seq.json")

	c := Open(path)
	if got := c.Next("pointer"); got != 1 {
		t.Fatalf("first pointer seq = %d, want 1", got)
	}
	if got := c.Next("pointer"); got != 2 {
		t.Fatalf("second pointer seq = %d, want 2", got)
	}
	// Independent module keeps its own counter.
	if got := c.Next("config"); got != 1 {
		t.Fatalf("first config seq = %d, want 1", got)
	}
	if got := c.Peek("pointer"); got != 2 {
		t.Fatalf("peek pointer = %d, want 2", got)
	}

	// Reopen: the counter must resume above the committed high-water, not reset to 0.
	c2 := Open(path)
	if got := c2.Peek("pointer"); got != 2 {
		t.Fatalf("reopened peek pointer = %d, want 2", got)
	}
	if got := c2.Next("pointer"); got != 3 {
		t.Fatalf("reopened next pointer = %d, want 3 (seq reuse would wedge the world)", got)
	}
	if got := c2.Next("config"); got != 2 {
		t.Fatalf("reopened next config = %d, want 2", got)
	}
}

// TestCorruptLedgerStartsEmpty proves a corrupt file degrades to an empty counter, never a panic.
func TestCorruptLedgerStartsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seq.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := Open(path)
	if got := c.Next("pointer"); got != 1 {
		t.Fatalf("seq after corrupt load = %d, want 1", got)
	}
}

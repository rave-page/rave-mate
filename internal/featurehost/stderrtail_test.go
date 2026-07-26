package featurehost

import (
	"fmt"
	"strings"
	"testing"
)

// TestStderrTailLatchesHeader: a goroutine dump (hundreds of lines) must not evict the
// fatal header - the latched header + bounded tail survive for the crash entry.
func TestStderrTailLatchesHeader(t *testing.T) {
	tl := &stderrTail{}
	tl.add("some warmup line")
	tl.add("fatal error: fault")
	tl.add("[signal 0xc0000005 code=0x0 addr=0x30 pc=0x7ff7]")
	for i := 0; i < 500; i++ {
		tl.add(fmt.Sprintf("goroutine %d [running]: frame frame frame", i))
	}
	hdr, tail := tl.snapshot()
	if hdr != "fatal error: fault" {
		t.Fatalf("header lost: %q", hdr)
	}
	lines := strings.Split(tail, "\n")
	if len(lines) > tailMaxLines || len(tail) > tailMaxBytes+256 {
		t.Fatalf("tail unbounded: %d lines / %d bytes", len(lines), len(tail))
	}
	if !strings.Contains(lines[len(lines)-1], "goroutine 499") {
		t.Fatalf("tail should keep the newest lines, last=%q", lines[len(lines)-1])
	}
}

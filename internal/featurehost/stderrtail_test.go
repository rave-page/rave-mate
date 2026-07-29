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

// The regression this pair of fields exists for: Go prints the FAULTING goroutine first and every
// other goroutine after it, so a tail keeps some idle waiter and drops the only stack that names
// the crash. A real media-child `fatal error: fault` (2026-07-29) was logged with header and tail
// intact and its faulting frames already evicted - nothing left to attribute it with.
func TestFatalHeadKeepsTheFaultingGoroutineNotTheLastOne(t *testing.T) {
	tl := &stderrTail{}
	tl.add("some ordinary startup line")
	tl.add("fatal error: fault")
	tl.add("[signal 0xc0000005 code=0x0 addr=0x18 pc=0x7ff712345678]")
	tl.add("")
	tl.add("goroutine 42 gp=0x123 m=4 mp=0x456 [running]:")
	tl.add("rave.page/mate/internal/videoshare._Cfunc_rave_spout_scan(0x1)")
	for i := 0; i < 400; i++ { // ...then hundreds of OTHER goroutines
		tl.add(fmt.Sprintf("goroutine %d [chan receive]:", 1000+i))
		tl.add(fmt.Sprintf("  runtime.gopark(0x%x)", i))
	}

	head := tl.fatalHead()
	for _, want := range []string{
		"fatal error: fault", "[signal 0xc0000005", "goroutine 42", "_Cfunc_rave_spout_scan",
	} {
		if !strings.Contains(head, want) {
			t.Fatalf("fatal_head lost %q - the faulting stack is the whole point:\n%s", want, head)
		}
	}
	if _, tail := tl.snapshot(); strings.Contains(tail, "goroutine 42 ") {
		t.Fatal("test is not exercising eviction - the faulting goroutine is still in the tail")
	}
	if n := len(strings.Split(head, "\n")); n > headMaxLines {
		t.Fatalf("fatal_head grew to %d lines, cap is %d", n, headMaxLines)
	}
}

// A clean exit must not attach a bogus crash stack.
func TestFatalHeadEmptyWithoutAFatalLine(t *testing.T) {
	tl := &stderrTail{}
	for i := 0; i < 20; i++ {
		tl.add(fmt.Sprintf("just logging %d", i))
	}
	if h := tl.fatalHead(); h != "" {
		t.Fatalf("fatal_head = %q on a clean stream", h)
	}
}

// Panics latch the same way - both crash shapes are attributed from the head.
func TestFatalHeadLatchesPanics(t *testing.T) {
	tl := &stderrTail{}
	tl.add("panic: runtime error: invalid memory address or nil pointer dereference")
	tl.add("goroutine 7 [running]:")
	tl.add("rave.page/mate/internal/mediaroute.(*Manager).scan(0x0)")
	for i := 0; i < 200; i++ {
		tl.add(fmt.Sprintf("goroutine %d [select]:", 500+i))
	}
	head := tl.fatalHead()
	if !strings.Contains(head, "goroutine 7 ") || !strings.Contains(head, "(*Manager).scan") {
		t.Fatalf("panic head lost the faulting frames:\n%s", head)
	}
}

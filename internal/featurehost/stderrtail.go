package featurehost

import (
	"strings"
	"sync"
)

// stderrTail keeps a bounded tail of a child's stderr + latches the first fatal header
// line, so a crash can be logged as ONE entry that survives ring eviction (a goroutine
// dump alone is hundreds of lines). Caps: 64 lines / 16 KiB, drop-oldest.
//
// It also latches the HEAD of the dump, which is the part that actually names the crash. Go prints
// a fault as `fatal error: …` then `[signal 0xc0000005 … pc=…]` then THE FAULTING GOROUTINE, and
// only then every other goroutine. A tail therefore keeps the LAST goroutine in the dump - always
// some idle waiter - and drops the one that crashed. That cost a real attribution: a media-child
// `fatal error: fault` on 2026-07-29 was logged with its header and tail intact and its faulting
// stack already evicted, leaving nothing to attribute it with (#61).
type stderrTail struct {
	mu       sync.Mutex
	lines    []string
	bytes    int
	header   string // first "panic:" / "fatal error:" / "Exception 0x..." line seen
	head     []string
	headByte int
	heading  bool // latching the head (set when header is seen, cleared when the head is full)
}

const (
	tailMaxLines = 64
	tailMaxBytes = 16 * 1024
	// The head has to cover signal + blank + goroutine banner + the faulting frames; deep cgo
	// stacks (Spout/DirectShow/MF) run long, so allow noticeably more than a typical Go frame set.
	headMaxLines = 80
	headMaxBytes = 24 * 1024
)

func (t *stderrTail) add(ln string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.header == "" {
		if strings.HasPrefix(ln, "panic:") || strings.HasPrefix(ln, "fatal error:") ||
			strings.Contains(ln, "Exception 0x") || strings.HasPrefix(ln, "runtime: ") {
			t.header = ln
			t.heading = true // this line starts the dump: keep what follows
		}
	}
	if t.heading {
		t.head = append(t.head, ln)
		t.headByte += len(ln)
		if len(t.head) >= headMaxLines || t.headByte >= headMaxBytes {
			t.heading = false // full: never evict, the first frames are the valuable ones
		}
	}
	t.lines = append(t.lines, ln)
	t.bytes += len(ln)
	for (len(t.lines) > tailMaxLines || t.bytes > tailMaxBytes) && len(t.lines) > 1 {
		t.bytes -= len(t.lines[0])
		t.lines = t.lines[1:]
	}
}

func (t *stderrTail) snapshot() (header, tail string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.header, strings.Join(t.lines, "\n")
}

// fatalHead is the latched start of the dump - signal line + faulting goroutine. "" when the child
// never printed a fatal header.
func (t *stderrTail) fatalHead() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(t.head, "\n")
}

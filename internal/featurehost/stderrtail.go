package featurehost

import (
	"strings"
	"sync"
)

// stderrTail keeps a bounded tail of a child's stderr + latches the first fatal header
// line, so a crash can be logged as ONE entry that survives ring eviction (a goroutine
// dump alone is hundreds of lines). Caps: 64 lines / 16 KiB, drop-oldest.
type stderrTail struct {
	mu     sync.Mutex
	lines  []string
	bytes  int
	header string // first "panic:" / "fatal error:" / "Exception 0x..." line seen
}

const (
	tailMaxLines = 64
	tailMaxBytes = 16 * 1024
)

func (t *stderrTail) add(ln string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.header == "" {
		if strings.HasPrefix(ln, "panic:") || strings.HasPrefix(ln, "fatal error:") ||
			strings.Contains(ln, "Exception 0x") || strings.HasPrefix(ln, "runtime: ") {
			t.header = ln
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

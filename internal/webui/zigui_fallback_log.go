package webui

// Debug visibility for SILENT Zig→Go render fallbacks. zigui counts every render that came back
// ok=false; this logs the tally from the ~1 Hz tick - at most once a minute, and only when a count
// changed (same throttle shape as the tick's other dedup guards). Motivation: one nil slice in a
// nested state marshalled JSON null, the Zig parser rejected it, and a WHOLE tab quietly rendered
// from Go for weeks.

import (
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/zigui"
)

const zigFbLogEvery = time.Minute

var zigFb struct {
	mu  sync.Mutex
	key string    // last logged tally - a change is what makes a new log interesting
	at  time.Time // last emit (zero = never)
}

// logZigFallbacks emits the fallback tally at debug level when it changed and the cooldown passed.
func (u *UI) logZigFallbacks() {
	if u.log == nil {
		return
	}
	counts := zigui.FallbackCounts()
	if len(counts) == 0 {
		return
	}
	key := zigFbKey(counts)
	zigFb.mu.Lock()
	if key == zigFb.key || time.Since(zigFb.at) < zigFbLogEvery {
		zigFb.mu.Unlock() // unchanged, or still cooling down (the key stays stale → logs after it)
		return
	}
	zigFb.key, zigFb.at = key, time.Now()
	zigFb.mu.Unlock()
	u.log.Debug("zigui", "renderer fell back to Go", map[string]any{"counts": key})
}

// zigFbKey renders the tally as a stable sorted "name=n" list (doubles as the change key).
func zigFbKey(c map[string]int) string {
	names := make([]string, 0, len(c))
	for k := range c {
		names = append(names, k)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		parts = append(parts, n+"="+strconv.Itoa(c[n]))
	}
	return strings.Join(parts, " ")
}

// Fallback accounting - present in BOTH builds (tagged cgo + untagged stub), so webui can read it
// without build-tag branches. Counts renders that returned ok=false, i.e. the Zig renderer produced
// nothing and the caller silently used its Go renderer. This exists because a single nil slice in a
// nested state (JSON null, which the Zig parser rejects) once dropped a WHOLE tab back to Go with no
// trace at all.
package zigui

import (
	"runtime"
	"strings"
	"sync"
)

var (
	fbMu     sync.Mutex
	fbCounts = map[string]int{}
)

// noteFallback records one ok=false render, keyed by the Render* wrapper skip frames up. Only ever
// called on the failure path (never on a successful render), so the reflection cost is irrelevant.
// A len(state)==0 call is NOT a fallback - nothing was asked of Zig - and is excluded by the caller.
func noteFallback(skip int) {
	name := "unknown"
	if pc, _, _, ok := runtime.Caller(skip); ok {
		if fn := runtime.FuncForPC(pc); fn != nil {
			name = fn.Name()
			if i := strings.LastIndexByte(name, '.'); i >= 0 {
				name = name[i+1:] // strip the package path: RenderSettings
			}
		}
	}
	fbMu.Lock()
	fbCounts[name]++
	fbMu.Unlock()
}

// FallbackCounts snapshots ok=false renders per renderer (empty map = none seen).
// Note: a few renderers legitimately return ok=false for a LEGITIMATELY EMPTY fragment
// (#midi-ctlstat-<i> before the MIDI child reports, the hidden library-remote switcher, #pub-hero
// without a recorder, the hidden update flow) - the Go fallback renders the same empty string, so
// those counts are expected. A count on a whole-view renderer is the real smell.
func FallbackCounts() map[string]int {
	fbMu.Lock()
	defer fbMu.Unlock()
	out := make(map[string]int, len(fbCounts))
	for k, v := range fbCounts {
		out[k] = v
	}
	return out
}

package libsync

import "rave.page/mate/internal/musiclib"

// ApplyCueRules mutates a canonical track per the cue conversion rules before it's written to a
// target. HotcuesToMemory demotes pad-assigned hotcues to memory/stored cues by clearing the slot
// (Hotcue=-1) - on export that becomes Traktor HOTCUE="-1" / Rekordbox Num="-1", the memory-cue
// encoding. Grid cues (no slot) are untouched.
func ApplyCueRules(t *musiclib.Track, hotcuesToMemory bool) {
	if !hotcuesToMemory {
		return
	}
	for i := range t.Cues {
		if t.Cues[i].Hotcue >= 0 {
			t.Cues[i].Hotcue = -1
		}
	}
}

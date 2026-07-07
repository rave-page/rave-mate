package libsync

import (
	"testing"

	"rave.page/mate/internal/musiclib"
)

func TestApplyCueRulesHotcuesToMemory(t *testing.T) {
	tr := musiclib.Track{Cues: []musiclib.CuePoint{
		{Name: "Hot 1", Kind: musiclib.CueHot, Hotcue: 0},
		{Name: "Hot 2", Kind: musiclib.CueHot, Hotcue: 3},
		{Name: "Grid", Kind: musiclib.CueGrid, Hotcue: -1},
	}}
	ApplyCueRules(&tr, true)
	for i, c := range tr.Cues {
		if c.Hotcue != -1 {
			t.Errorf("cue %d (%s): Hotcue = %d; want -1 (memory cue)", i, c.Name, c.Hotcue)
		}
	}

	// off = no change.
	tr2 := musiclib.Track{Cues: []musiclib.CuePoint{{Hotcue: 2}}}
	ApplyCueRules(&tr2, false)
	if tr2.Cues[0].Hotcue != 2 {
		t.Errorf("off: Hotcue changed to %d; want 2", tr2.Cues[0].Hotcue)
	}
}

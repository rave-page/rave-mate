package featurehost

import (
	"testing"

	"rave.page/mate/internal/logbus"
)

// TestPlayerMirrorTracksPause guards the cue-audition "have to hit Stop first" regression: the
// daemon-side mirror must reflect the child's pause state so a hold-Space release followed by a
// fast re-press takes the warm unpause path, not a full re-decode. No child/device needed - drives
// the mirror via the event handlers directly (Running()==false on an unstarted host).
func TestPlayerMirrorTracksPause(t *testing.T) {
	p, err := NewPlayerProxy(logbus.New(16))
	if err != nil {
		t.Fatal(err)
	}

	// Play tick: mirror is playing, not paused.
	p.onTickEvent([]byte(`{"cur":1.5,"total":10,"paused":false}`))
	if st := p.State(); !st.Playing || st.Paused {
		t.Fatalf("after play tick: got %+v, want playing !paused", st)
	}

	// Release (child down here, so Send is skipped) must still snap the mirror to paused@fallback,
	// optimistically - the confirming tick is ~200ms out and a spam-press before it must unpause.
	p.PreviewRelease(1.5)
	if st := p.State(); !st.Playing || !st.Paused || st.Cur != 1.5 {
		t.Fatalf("after previewRelease: got %+v, want playing+paused, cur=1.5", st)
	}

	// Durable backstop: a paused tick keeps Paused set (independent of the optimistic write).
	p.onTickEvent([]byte(`{"cur":1.5,"total":10,"paused":true}`))
	if st := p.State(); !st.Paused {
		t.Fatalf("paused tick must keep Paused: got %+v", st)
	}

	// Resume tick clears it (the unpause landed in the child).
	p.onTickEvent([]byte(`{"cur":1.7,"total":10,"paused":false}`))
	if st := p.State(); st.Paused {
		t.Fatalf("resume tick must clear Paused: got %+v", st)
	}
}

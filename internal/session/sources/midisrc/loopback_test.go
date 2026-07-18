package midisrc

import (
	"reflect"
	"testing"
)

func TestIsLoopbackPort(t *testing.T) {
	cases := map[string]bool{
		"LoopBe Internal MIDI":      true,
		"loopMIDI Port 1":           true,
		"KOMPLETE KONTROL A61 MIDI": false,
		"DJ2GO2 Touch MIDI":         false,
		"Focusrite USB MIDI":        false,
	}
	for name, want := range cases {
		if got := isLoopbackPort(name); got != want {
			t.Errorf("isLoopbackPort(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestSetMutedTracksAndNotifies(t *testing.T) {
	s := New(nil, "", "LoopBe Internal MIDI")
	var lastMuted []string
	calls := 0
	s.SetOnPorts(func(_, _, muted []string) { calls++; lastMuted = muted })

	s.setMuted("LoopBe Internal MIDI", true)
	if want := []string{"LoopBe Internal MIDI"}; !reflect.DeepEqual(s.MutedInputPorts(), want) || !reflect.DeepEqual(lastMuted, want) {
		t.Fatalf("after mute: accessor=%v callback=%v", s.MutedInputPorts(), lastMuted)
	}
	// Re-muting the same port must not duplicate the entry.
	s.setMuted("LoopBe Internal MIDI", true)
	if got := s.MutedInputPorts(); len(got) != 1 {
		t.Fatalf("duplicate muted entry: %v", got)
	}
	s.setMuted("LoopBe Internal MIDI", false)
	if got := s.MutedInputPorts(); len(got) != 0 || len(lastMuted) != 0 {
		t.Fatalf("after unmute: accessor=%v callback=%v", got, lastMuted)
	}
	if calls != 3 {
		t.Fatalf("onPorts calls = %d, want 3", calls)
	}
}

func TestRxCountPerPort(t *testing.T) {
	s := New(nil, "", "p")
	if s.rxCount("p") != 0 {
		t.Fatal("fresh counter not zero")
	}
	s.rxMu.Lock()
	s.rxN["p"] += 2
	s.rxN["q"]++
	s.rxMu.Unlock()
	if s.rxCount("p") != 2 || s.rxCount("q") != 1 {
		t.Fatalf("counters wrong: p=%d q=%d", s.rxCount("p"), s.rxCount("q"))
	}
}

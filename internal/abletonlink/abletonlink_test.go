package abletonlink

import (
	"testing"
	"time"
)

func TestStubUnavailable(t *testing.T) {
	s := NewStub()
	if s.Available() {
		t.Fatal("stub must report unavailable")
	}
	st := s.State(time.Now())
	if st.Available || st.Enabled {
		t.Errorf("stub state = %+v, want unavailable+disabled", st)
	}
	if st.Quantum != DefaultQuantum {
		t.Errorf("stub quantum = %v, want %v", st.Quantum, DefaultQuantum)
	}
	// no-op setters must not panic
	s.SetEnabled(true)
	s.SetTempo(128, time.Now())
	s.ForceBeat(0, time.Now())
	if err := s.Close(); err != nil {
		t.Errorf("Close = %v", err)
	}
}

func TestNewLinkUnavailableInStubBuild(t *testing.T) {
	// Default (untagged) build: NewLink must report the backend is missing.
	if _, err := NewLink(16); err != ErrUnavailable {
		t.Fatalf("NewLink err = %v, want ErrUnavailable (untagged build)", err)
	}
}

func TestTimeSourceStoppedWhenUnavailable(t *testing.T) {
	ts := NewTimeSource(NewStub())
	if _, running := ts.Position(); running {
		t.Error("TimeSource must be stopped when Link unavailable")
	}
}

// fakeSession drives the TimeSource with a controllable State.
type fakeSession struct{ st State }

func (f *fakeSession) Available() bool                { return f.st.Available }
func (f *fakeSession) SetEnabled(bool)                {}
func (f *fakeSession) State(time.Time) State          { return f.st }
func (f *fakeSession) SetTempo(float64, time.Time)    {}
func (f *fakeSession) ForceBeat(float64, time.Time)   {}
func (f *fakeSession) RequestBeat(float64, time.Time) {}
func (f *fakeSession) SetQuantum(float64)             {}
func (f *fakeSession) SetStartStopSyncEnabled(bool)   {}
func (f *fakeSession) SetPlaying(bool, time.Time)     {}
func (f *fakeSession) Close() error                   { return nil }

func TestTimeSourceMusicalPosition(t *testing.T) {
	// 120 BPM → 0.5s/beat; beat 8 → 4.0s.
	f := &fakeSession{st: State{Available: true, Enabled: true, Tempo: 120, Beat: 8, Quantum: 16}}
	ts := NewTimeSource(f)
	pos, running := ts.Position()
	if !running {
		t.Fatal("want running")
	}
	if got := pos.Seconds(); got < 3.999 || got > 4.001 {
		t.Errorf("pos = %vs, want 4.0s", got)
	}
}

func TestPhraseFraction(t *testing.T) {
	cases := []struct {
		phase, quantum, want float64
	}{
		{0, 16, 0},
		{4, 16, 0.25},
		{8, 16, 0.5},
		{15, 16, 0.9375},
	}
	for _, c := range cases {
		got := State{Phase: c.phase, Quantum: c.quantum}.PhraseFraction()
		if got < c.want-1e-9 || got > c.want+1e-9 {
			t.Errorf("PhraseFraction(phase=%v q=%v) = %v, want %v", c.phase, c.quantum, got, c.want)
		}
	}
}

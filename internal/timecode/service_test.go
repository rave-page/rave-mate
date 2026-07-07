package timecode

import (
	"strings"
	"sync"
	"testing"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/medialink"
)

// TestFollowClockDrivesGenerator: with FollowClock the frame counter advances on the medialink
// media clock (ns), not wall time - the P3 egress seam (LTC/MTC/Art-Net all resync per tick off
// the generator, so they follow for free).
func TestFollowClockDrivesGenerator(t *testing.T) {
	var mu sync.Mutex
	ns := int64(0)
	s := New(logbus.New(16), func() config.TimecodeFeature { return config.TimecodeFeature{Rate: "30"} })
	s.FollowClock(func() int64 { mu.Lock(); defer mu.Unlock(); return ns })

	s.gen.Start(medialink.FPS30, Timecode{Rate: medialink.FPS30})
	if got := s.gen.FrameNow(); got != 0 {
		t.Fatalf("start frame = %d, want 0", got)
	}
	mu.Lock()
	ns = int64(2 * time.Second) // +2 s media clock (wall time irrelevant)
	mu.Unlock()
	if got := s.gen.FrameNow(); got != 60 {
		t.Fatalf("after +2s media clock: frame = %d, want 60", got)
	}
	// A slew of the disciplined clock (+500 ms) shifts TC with it - the follow property.
	mu.Lock()
	ns += int64(500 * time.Millisecond)
	mu.Unlock()
	if got := s.gen.FrameNow(); got != 75 {
		t.Fatalf("after +0.5s slew: frame = %d, want 75", got)
	}
}

// fakePlane records the Service↔plane wiring.
type fakePlane struct {
	mu        sync.Mutex
	source    medialink.TCSource
	chase     func(int64, medialink.Rate)
	announces int
	status    medialink.TCStatus
}

func (p *fakePlane) SetLocalSource(fn medialink.TCSource) { p.mu.Lock(); p.source = fn; p.mu.Unlock() }
func (p *fakePlane) SetChase(fn func(frame int64, rate medialink.Rate)) {
	p.mu.Lock()
	p.chase = fn
	p.mu.Unlock()
}
func (p *fakePlane) AnnounceNow() { p.mu.Lock(); p.announces++; p.mu.Unlock() }
func (p *fakePlane) Status() medialink.TCStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status
}
func (p *fakePlane) announced() int { p.mu.Lock(); defer p.mu.Unlock(); return p.announces }

// TestAttachPlaneSourceAndAnnounce: the plane's local source samples the generator, and start/
// jam/stop each force an on-jump announce (§4).
func TestAttachPlaneSourceAndAnnounce(t *testing.T) {
	s := New(logbus.New(16), func() config.TimecodeFeature { return config.TimecodeFeature{Rate: "25"} })
	p := &fakePlane{}
	s.AttachPlane(p)
	if p.source == nil || p.chase == nil {
		t.Fatal("plane not wired")
	}

	if err := s.StartClock(); err != nil { // no sinks on → generator only
		t.Fatal(err)
	}
	if p.announced() == 0 {
		t.Fatal("StartClock must announce")
	}
	frame, rate, running := p.source()
	if !running || rate != medialink.FPS25 || frame < 0 {
		t.Fatalf("source sample = (%d,%v,%v)", frame, rate, running)
	}

	n := p.announced()
	s.Jam(Timecode{H: 1, Rate: medialink.FPS25})
	if p.announced() <= n {
		t.Fatal("Jam must announce")
	}
	if f, _, _ := p.source(); f < (Timecode{H: 1, Rate: medialink.FPS25}).Frames() {
		t.Fatalf("post-jam source frame = %d", f)
	}

	n = p.announced()
	s.StopClock()
	if p.announced() <= n {
		t.Fatal("StopClock must announce")
	}
	if _, _, running := p.source(); running {
		t.Fatal("source must report stopped")
	}
}

// TestChaseRemote: the slave chase jams the running clock only past the ±1-frame dead band and
// never while stopped; the jam adopts the master's rate.
func TestChaseRemote(t *testing.T) {
	s := New(logbus.New(16), func() config.TimecodeFeature { return config.TimecodeFeature{Rate: "30"} })
	var mu sync.Mutex
	ns := int64(0)
	s.FollowClock(func() int64 { mu.Lock(); defer mu.Unlock(); return ns })

	// Stopped clock: chase is a no-op.
	s.chaseRemote(1000, medialink.FPS30)
	if got := s.gen.FrameNow(); got != 0 {
		t.Fatalf("stopped clock jammed to %d", got)
	}

	s.gen.Start(medialink.FPS30, Timecode{Rate: medialink.FPS30})
	// Within dead band: no jam.
	s.chaseRemote(1, medialink.FPS30)
	if got := s.gen.FrameNow(); got != 0 {
		t.Fatalf("dead-band chase jammed to %d", got)
	}
	// Past dead band: jam to the master position (+ rate adopt).
	s.chaseRemote(100, medialink.FPS2997)
	if got := s.gen.FrameNow(); got != 100 {
		t.Fatalf("chase jam frame = %d, want 100", got)
	}
	if r := s.gen.Rate(); r != medialink.FPS2997 {
		t.Fatalf("chase must adopt master rate, got %+v", r)
	}
	// Negative divergence too.
	s.chaseRemote(50, medialink.FPS2997)
	if got := s.gen.FrameNow(); got != 50 {
		t.Fatalf("backward chase frame = %d, want 50", got)
	}
}

// TestStatusLinePlaneRole: ctl tc-status carries the plane role when attached.
func TestStatusLinePlaneRole(t *testing.T) {
	s := New(logbus.New(16), func() config.TimecodeFeature { return config.TimecodeFeature{Rate: "30"} })
	if got := s.StatusLine(); len(got) == 0 || strings.Contains(got, "tcmaster") {
		t.Fatalf("detached status must omit tcmaster: %q", got)
	}
	p := &fakePlane{status: medialink.TCStatus{Role: medialink.TCRoleMaster, Master: "self-node"}}
	s.AttachPlane(p)
	if got := s.StatusLine(); !strings.Contains(got, "tcmaster=self") {
		t.Fatalf("master status = %q", got)
	}
	p.mu.Lock()
	p.status = medialink.TCStatus{Role: medialink.TCRoleSlave, Master: "peer-node", Holdover: true}
	p.mu.Unlock()
	if got := s.StatusLine(); !strings.Contains(got, "tcmaster=peer-node") || !strings.Contains(got, "holdover") {
		t.Fatalf("slave status = %q", got)
	}
}

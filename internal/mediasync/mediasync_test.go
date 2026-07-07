package mediasync

import (
	"context"
	"errors"
	"testing"
	"time"

	"rave.page/mate/internal/obs"
)

// ── fakes ──────────────────────────────────────────────────────────────────

// fakeClock is a manually-driven TimeSource.
type fakeClock struct {
	pos     time.Duration
	running bool
}

func (f *fakeClock) Position() (time.Duration, bool) { return f.pos, f.running }

// fakeCtrl records every media call + returns a scripted status.
type fakeCtrl struct {
	status  obs.MediaInputStatus
	statErr error

	setCursor    []int
	offsetCursor []int
	actions      []string
	statusCalls  int
	failApply    error
}

func (c *fakeCtrl) GetMediaInputStatus(context.Context, string) (obs.MediaInputStatus, error) {
	c.statusCalls++
	return c.status, c.statErr
}
func (c *fakeCtrl) SetMediaInputCursor(_ context.Context, _ string, ms int) error {
	c.setCursor = append(c.setCursor, ms)
	return c.failApply
}
func (c *fakeCtrl) OffsetMediaInputCursor(_ context.Context, _ string, ms int) error {
	c.offsetCursor = append(c.offsetCursor, ms)
	return c.failApply
}
func (c *fakeCtrl) TriggerMediaInputAction(_ context.Context, _, action string) error {
	c.actions = append(c.actions, action)
	return c.failApply
}

func playing(cursorMs int64) obs.MediaInputStatus {
	return obs.MediaInputStatus{State: obs.MediaStatePlaying, Cursor: time.Duration(cursorMs) * time.Millisecond, Duration: 10 * time.Minute}
}

// ── decide ───────────────────────────────────────────────────────────────

func TestDecideDeadBand(t *testing.T) {
	cfg := SourceConfig{} // defaults: 30fps, 2 frames → ~66.7ms dead band
	for _, err := range []float64{0, 30, -50, 66} {
		if a, _ := decide(err, cfg); a != ActNone {
			t.Errorf("decide(%v) = %v, want ActNone (within dead band)", err, a)
		}
	}
}

func TestDecideNudge(t *testing.T) {
	cfg := SourceConfig{}
	a, n := decide(120, cfg)
	if a != ActNudge {
		t.Fatalf("decide(120) action = %v, want nudge", a)
	}
	if n != 120 {
		t.Errorf("nudge = %d, want 120", n)
	}
	a, n = decide(-200, cfg)
	if a != ActNudge || n != -200 {
		t.Errorf("decide(-200) = %v,%d want nudge,-200", a, n)
	}
}

func TestDecideRestart(t *testing.T) {
	cfg := SourceConfig{}
	if a, _ := decide(1500, cfg); a != ActRestartSeek {
		t.Errorf("decide(1500) = %v, want restart-seek", a)
	}
	if a, _ := decide(-5000, cfg); a != ActRestartSeek {
		t.Errorf("decide(-5000) = %v, want restart-seek", a)
	}
}

func TestDecideCustomTunables(t *testing.T) {
	// 60fps, 4-frame dead band → ~66.7ms; restart at 800ms.
	cfg := SourceConfig{Fps: 60, DeadBandFrames: 4, RestartThresholdMs: 800}
	if a, _ := decide(60, cfg); a != ActNone {
		t.Errorf("60ms should be within 66.7ms dead band, got %v", a)
	}
	if a, _ := decide(900, cfg); a != ActRestartSeek {
		t.Errorf("900ms should exceed 800ms restart threshold, got %v", a)
	}
}

// ── Chaser.Tick ─────────────────────────────────────────────────────────

func newTestChaser(cfg SourceConfig, ctrl MediaController, ts TimeSource) *Chaser {
	c := NewChaser(MediaControllerCfg{SourceConfig: cfg, Ctrl: ctrl}, ts)
	return c
}

func TestTickClockStoppedNoOp(t *testing.T) {
	ctrl := &fakeCtrl{status: playing(0)}
	c := newTestChaser(SourceConfig{InputName: "V"}, ctrl, &fakeClock{pos: 5 * time.Second, running: false})
	if err := c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ctrl.statusCalls != 0 {
		t.Errorf("stopped clock must not touch OBS, got %d status calls", ctrl.statusCalls)
	}
	if st := c.Status(); st.Active || st.LastAction != "idle" {
		t.Errorf("status = %+v, want inactive/idle", st)
	}
}

func TestTickWithinDeadBandNoCorrection(t *testing.T) {
	// target = 10s + 0 offset; cursor = 10.03s → 30ms error, within dead band.
	ctrl := &fakeCtrl{status: playing(10030)}
	c := newTestChaser(SourceConfig{InputName: "V"}, ctrl, &fakeClock{pos: 10 * time.Second, running: true})
	if err := c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(ctrl.offsetCursor) != 0 || len(ctrl.setCursor) != 0 || len(ctrl.actions) != 0 {
		t.Errorf("expected no correction, got offset=%v set=%v act=%v", ctrl.offsetCursor, ctrl.setCursor, ctrl.actions)
	}
	if st := c.Status(); st.LastAction != "none" || st.ErrorMs != -30 {
		t.Errorf("status = %+v, want none / errorMs -30", st)
	}
}

func TestTickSmallErrorNudge(t *testing.T) {
	// target 10s, cursor 9.7s → +300ms behind → nudge forward 300ms.
	ctrl := &fakeCtrl{status: playing(9700)}
	c := newTestChaser(SourceConfig{InputName: "V"}, ctrl, &fakeClock{pos: 10 * time.Second, running: true})
	if err := c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(ctrl.offsetCursor) != 1 || ctrl.offsetCursor[0] != 300 {
		t.Errorf("offsetCursor = %v, want [300]", ctrl.offsetCursor)
	}
	if st := c.Status(); st.LastAction != "nudge" || st.CorrectionsPerMin != 1 {
		t.Errorf("status = %+v, want nudge / 1 corr", st)
	}
}

func TestTickLargeErrorRestartSeek(t *testing.T) {
	// target 60s, cursor 10s → 50s error → restart + absolute seek to target.
	ctrl := &fakeCtrl{status: playing(10000)}
	c := newTestChaser(SourceConfig{InputName: "V"}, ctrl, &fakeClock{pos: 60 * time.Second, running: true})
	if err := c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(ctrl.actions) != 1 || ctrl.actions[0] != obs.MediaActionRestart {
		t.Errorf("actions = %v, want [RESTART]", ctrl.actions)
	}
	if len(ctrl.setCursor) != 1 || ctrl.setCursor[0] != 60000 {
		t.Errorf("setCursor = %v, want [60000]", ctrl.setCursor)
	}
}

func TestTickColdStartWhenStopped(t *testing.T) {
	// media stopped → RESTART then seek to target regardless of error size.
	ctrl := &fakeCtrl{status: obs.MediaInputStatus{State: obs.MediaStateStopped}}
	c := newTestChaser(SourceConfig{InputName: "V", StaticOffsetMs: 250}, ctrl, &fakeClock{pos: 5 * time.Second, running: true})
	if err := c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(ctrl.actions) != 1 || ctrl.actions[0] != obs.MediaActionRestart {
		t.Errorf("actions = %v, want [RESTART]", ctrl.actions)
	}
	// target = 5000 + 250 static offset
	if len(ctrl.setCursor) != 1 || ctrl.setCursor[0] != 5250 {
		t.Errorf("setCursor = %v, want [5250] (5s + 250ms static offset)", ctrl.setCursor)
	}
	if st := c.Status(); st.LastAction != "cold-start" {
		t.Errorf("lastAction = %q, want cold-start", st.LastAction)
	}
}

func TestTickStaticOffsetAppliedToNudge(t *testing.T) {
	// house 10s, +500ms static offset → target 10.5s; cursor 10s → +500ms behind → nudge 500.
	ctrl := &fakeCtrl{status: playing(10000)}
	c := newTestChaser(SourceConfig{InputName: "V", StaticOffsetMs: 500}, ctrl, &fakeClock{pos: 10 * time.Second, running: true})
	if err := c.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(ctrl.offsetCursor) != 1 || ctrl.offsetCursor[0] != 500 {
		t.Errorf("offsetCursor = %v, want [500]", ctrl.offsetCursor)
	}
	if st := c.Status(); st.TargetMs != 10500 {
		t.Errorf("targetMs = %d, want 10500", st.TargetMs)
	}
}

func TestTickStatusErrorSurfaced(t *testing.T) {
	ctrl := &fakeCtrl{statErr: errors.New("obs not connected")}
	c := newTestChaser(SourceConfig{InputName: "V"}, ctrl, &fakeClock{pos: time.Second, running: true})
	if err := c.Tick(context.Background()); err == nil {
		t.Fatal("expected error from GetMediaInputStatus")
	}
	if st := c.Status(); st.Err == "" || !st.Active {
		t.Errorf("status = %+v, want active with error set", st)
	}
}

func TestCorrectionsPerMinWindow(t *testing.T) {
	ctrl := &fakeCtrl{status: playing(9000)} // 1s behind → nudge every tick
	c := newTestChaser(SourceConfig{InputName: "V"}, ctrl, &fakeClock{pos: 10 * time.Second, running: true})
	base := time.Unix(1000, 0)
	cur := base
	c.now = func() time.Time { return cur }

	for i := 0; i < 3; i++ {
		if err := c.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
		cur = cur.Add(10 * time.Second)
	}
	// 3 corrections within the last minute.
	if st := c.Status(); st.CorrectionsPerMin != 3 {
		t.Errorf("correctionsPerMin = %v, want 3", st.CorrectionsPerMin)
	}
	// Jump 2 minutes ahead: the window should have expired all corrections.
	cur = cur.Add(2 * time.Minute)
	if got := c.correctionsPerMin(); got != 0 {
		t.Errorf("correctionsPerMin after 2min = %v, want 0", got)
	}
}

func TestKeyframeHint(t *testing.T) {
	vlc := newTestChaser(SourceConfig{InputName: "V", InputKind: obs.KindVLCSource}, &fakeCtrl{}, &fakeClock{})
	if vlc.Status().KeyframeSnapped {
		t.Error("vlc source should not be keyframe-snapped")
	}
	media := newTestChaser(SourceConfig{InputName: "V", InputKind: obs.KindMediaSource}, &fakeCtrl{}, &fakeClock{})
	if !media.Status().KeyframeSnapped || media.Status().Hint == "" {
		t.Errorf("media source should be keyframe-snapped with a hint, got %+v", media.Status())
	}
}

package videoshare

import (
	"testing"
	"time"
)

// driveToIdle feeds code (with dims/buf) until the poller backs off; fails the test if it
// never does within window. Returns the sim clock at backoff.
func driveToIdle(t *testing.T, p *recvPoller, code, w, h, bufLen int, now time.Time, window time.Duration) time.Time {
	t.Helper()
	deadline := now.Add(window)
	for now.Before(deadline) {
		now = now.Add(p.interval)
		if p.apply(code, w, h, bufLen, now).interval == recvPollIdle {
			return now
		}
	}
	t.Fatalf("idle backoff never engaged within %v (code %d)", window, code)
	return now
}

// A 0x0 registry ghost (sender entry with no usable dims, shim answering "needs size"
// forever) must reach the 50ms idle poll - this exact case used to re-arm the 250 Hz
// poll on every tick and serialize keyed-mutex acquisitions against the producer.
func TestRecvPollerStaleSenderReachesIdleBackoff(t *testing.T) {
	start := time.Unix(0, 0)
	p := newRecvPoller(nil, start)
	if act := p.apply(recvUpdated, 0, 0, 0, start); act.resize || act.frame {
		t.Fatalf("0x0 sender update produced action %+v", act)
	}
	now := driveToIdle(t, p, recvNeedSize, 0, 0, 0, start, recvIdleAfter+time.Second)
	if since := now.Sub(start); since > recvIdleAfter+2*recvPollIdle {
		t.Fatalf("backoff engaged too late: %v", since)
	}
	// Must STAY backed off while the sender remains stale.
	for i := 0; i < 100; i++ {
		now = now.Add(p.interval)
		if act := p.apply(recvNeedSize, 0, 0, 0, now); act.interval != recvPollIdle {
			t.Fatalf("poll re-armed fast on a stale sender at step %d", i)
		}
	}
}

// A connected sender that sizes the buffer but never produces a frame backs off too
// (quiet-sender path, unchanged behaviour).
func TestRecvPollerQuietSenderBacksOffAfterSize(t *testing.T) {
	start := time.Unix(0, 0)
	p := newRecvPoller(nil, start)
	act := p.apply(recvUpdated, 1280, 720, 0, start)
	if !act.resize || act.interval != recvPollEvery {
		t.Fatalf("connect pass: %+v", act)
	}
	driveToIdle(t, p, recvNoFrame, 1280, 720, 1280*720*4, start, recvIdleAfter+time.Second)
}

// A fresh sender appearing on an idle receiver gets ONE prompt resize pass (fast poll
// re-armed immediately), then frames flow with no further resizes.
func TestRecvPollerFreshSenderPromptResizeThenFrames(t *testing.T) {
	start := time.Unix(0, 0)
	p := newRecvPoller(nil, start)
	now := driveToIdle(t, p, recvErr, 0, 0, 0, start, recvIdleAfter+time.Second)

	now = now.Add(p.interval)
	act := p.apply(recvUpdated, 1920, 1080, 0, now)
	if !act.resize {
		t.Fatal("fresh sender denied its resize pass")
	}
	if act.interval != recvPollEvery {
		t.Fatalf("fresh sender did not re-arm the fast poll: %v", act.interval)
	}
	for i := 0; i < 10; i++ {
		now = now.Add(p.interval)
		act = p.apply(recvFrame, 1920, 1080, 1920*1080*4, now)
		if !act.frame || act.resize || act.interval != recvPollEvery {
			t.Fatalf("frame %d: %+v", i, act)
		}
	}
}

// "Needs size" with usable dims (undersized buffer, no sender update) grants the resize
// but does NOT re-arm the fast poll - only recvFrame/recvUpdated are activity.
func TestRecvPollerNeedSizeResizesWithoutRearmingFastPoll(t *testing.T) {
	start := time.Unix(0, 0)
	p := newRecvPoller(nil, start)
	now := driveToIdle(t, p, recvErr, 0, 0, 0, start, recvIdleAfter+time.Second)

	now = now.Add(p.interval)
	act := p.apply(recvNeedSize, 1280, 720, 0, now)
	if !act.resize {
		t.Fatal("usable needs-size must grant the resize")
	}
	if act.interval != recvPollIdle {
		t.Fatalf("needs-size re-armed the fast poll: %v", act.interval)
	}
	// Right-sized buffer: repeated needs-size asks for nothing more.
	if act = p.apply(recvNeedSize, 1280, 720, 1280*720*4, now.Add(p.interval)); act.resize {
		t.Fatal("resize repeated on a right-sized buffer")
	}
}

// The FPS gate applies BEFORE ReceiveImage even while unconnected (empty buffer) - the
// old loop bypassed it for len(buf)==0, so a capped receiver still probed at 250 Hz.
func TestRecvPollerGateAppliesWhileUnconnected(t *testing.T) {
	var g fpsGate
	g.setFPS(10)
	p := newRecvPoller(&g, time.Unix(0, 0))
	allowed := 0
	now := time.Unix(0, 0)
	for i := 0; i < 250; i++ { // one simulated second at the 4ms fast poll
		now = now.Add(recvPollEvery)
		if p.allow(now) {
			allowed++
		}
	}
	if allowed == 0 {
		t.Fatal("gate must still let connect probes through")
	}
	if allowed > 12 {
		t.Fatalf("fps gate bypassed while unconnected: %d receives in 1s (cap 10)", allowed)
	}
}

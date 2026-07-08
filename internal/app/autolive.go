package app

import (
	"context"
	"strings"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/session"
	"rave.page/mate/internal/stream"
)

// nowPlayingTitle builds the auto-live title from the merged master deck ("artist - title"); "" when
// nothing is playing (driver falls back to "Live set").
func nowPlayingTitle(m *session.Merger) string {
	if m == nil {
		return ""
	}
	ov := m.Snapshot().BuildOverlay(time.Now(), session.NowPlayingStaleAfter)
	for _, d := range ov.Decks {
		if d.Deck != ov.Master.Deck {
			continue
		}
		t := strings.TrimSpace(d.Artist)
		if t != "" && strings.TrimSpace(d.Title) != "" {
			t += " - "
		}
		return strings.TrimSpace(t + strings.TrimSpace(d.Title))
	}
	return ""
}

// livePublisher is the seam over the stream publisher the auto-live driver reconciles. StreamProxy
// satisfies it; a fake backs the unit test.
type livePublisher interface {
	Start(ctx context.Context, args stream.StartArgs) (stream.Status, error)
	End(ctx context.Context) (stream.Status, error)
}

// autoLiveInputs are the per-tick reconcile inputs (read fresh each sample).
type autoLiveInputs struct {
	signedIn func() bool
	paused   func() bool   // StreamBridge.PauseLiveSignal
	token    func() string // publish auth
	title    func() string // now-playing "artist - title"; "" ⇒ default
}

// autoLiveDriver reconciles the live now-playing broadcast to the OBS-stream signal: publish while a
// stream is live on this machine (signed in + not paused), end when it stops, pause, or sign-out.
// No manual control. Single-caller (driven from watchStreaming's tick) - not goroutine-safe.
type autoLiveDriver struct {
	pub    livePublisher
	log    *logbus.Bus
	notify func() // one-shot "sign in to broadcast" nudge; nil-safe

	live   bool // a stream we started is (believed) up
	warned bool // sign-in nudge throttle (reset on sign-in)
}

// tick reconciles one sample. streaming = OBS stream live here; signedIn/paused gate publishing.
// Only acts on a state change (want≠live), so repeated ticks are no-ops. A failed Start stays
// not-live and retries next tick.
func (d *autoLiveDriver) tick(ctx context.Context, streaming, signedIn, paused bool, token, title string) {
	want := streaming && signedIn && !paused
	switch {
	case want && !d.live:
		if title == "" {
			title = "Live set"
		}
		if _, err := d.pub.Start(ctx, stream.StartArgs{Title: title, UserToken: token}); err != nil {
			if d.log != nil {
				d.log.Warn("autolive", "auto go-live failed", map[string]any{"error": err.Error()})
			}
			return // retry next tick
		}
		d.live = true
		if d.log != nil {
			d.log.Info("autolive", "auto go-live (OBS stream detected)", map[string]any{"title": title})
		}
	case !want && d.live:
		if _, err := d.pub.End(ctx); err != nil && d.log != nil {
			d.log.Warn("autolive", "auto end failed (reaper cleans up)", map[string]any{"error": err.Error()})
		}
		d.live = false
		if d.log != nil {
			d.log.Info("autolive", "auto end (OBS stream stopped / paused)", nil)
		}
	}
	// Streaming but signed out (and not paused): nudge once until sign-in.
	switch {
	case streaming && !paused && !signedIn && !d.warned:
		d.warned = true
		if d.log != nil {
			d.log.Info("autolive", "OBS streaming but signed out - sign in to broadcast now-playing", nil)
		}
		if d.notify != nil {
			d.notify()
		}
	case signedIn:
		d.warned = false
	}
}

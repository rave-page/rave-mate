package webui

import (
	"testing"
	"time"

	"rave.page/mate/internal/session/sinks/recorder"
)

// A DJ armed OBS late: the capture covers only the tail of the set. Tracks that ended before the
// recording started must not appear in the media's jump list; the one still playing at capture
// start (and any later) must. Mirrors the real 2026-07-08 set: capture 12:52-12:57, tracks 12:02-12:49.
func TestTrackInCapture(t *testing.T) {
	base := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	at := func(m, s int) time.Time { return base.Add(time.Duration(m)*time.Minute + time.Duration(s)*time.Second) }
	capStart, capEnd := at(51, 59), at(57, 4)

	cases := []struct {
		name       string
		start, end time.Time
		want       bool
	}{
		{"ended long before capture", at(2, 52), at(41, 22), false},
		{"ended just before capture", at(45, 34), at(49, 11), false},
		{"playing when capture started (last, still open)", at(49, 11), time.Time{}, true},
		{"playing when capture started (bounded end after)", at(49, 11), at(58, 2), true},
		{"started during capture", at(53, 0), at(55, 0), true},
		{"started after capture ended", at(58, 0), time.Time{}, false},
		{"ended exactly at capture start (excluded)", at(48, 0), capStart, false},
	}
	for _, c := range cases {
		got := trackInCapture(recorder.Track{StartedAt: c.start, EndedAt: c.end}, capStart, capEnd)
		if got != c.want {
			t.Errorf("%s: trackInCapture=%v want %v", c.name, got, c.want)
		}
	}

	// Open capture (zero end): only the lower bound gates.
	if !trackInCapture(recorder.Track{StartedAt: at(58, 0)}, capStart, time.Time{}) {
		t.Error("open capture should include a track starting after capStart")
	}
}

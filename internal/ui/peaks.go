package ui

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/store"
)

// resolvePeaks returns waveform peaks for path from the persisted analysis cache
// (mtime-keyed), computing once via the probe worker on miss. Blocking - call off the
// UI thread. The ONE peaks source, shared by the Fyne player panel and the Gio player
// window (never duplicate analysis).
func (u *UI) resolvePeaks(ctx context.Context, path string) (trackPeaks, error) {
	mtime := fileMtime(path)
	var tp trackPeaks
	if data, ok := u.svc.Store.GetAnalysis(store.KindPeaks, path, mtime); ok {
		if json.Unmarshal(data, &tp) == nil && len(tp.Peaks) > 0 {
			return tp, nil
		}
	}
	if u.svc.Workers == nil {
		return trackPeaks{}, fmt.Errorf("no worker runtime")
	}
	raw, err := u.svc.Workers.RunBackground(ctx, "probe", "probe.peaks",
		map[string]any{"path": path, "buckets": 8192})
	if err != nil {
		return trackPeaks{}, err
	}
	var r struct {
		Peaks  string  `json:"peaks"`
		DurSec float64 `json:"durationSeconds"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return trackPeaks{}, err
	}
	if r.Peaks == "" || r.DurSec <= 0 {
		return trackPeaks{}, fmt.Errorf("empty analysis")
	}
	peaks, derr := base64.StdEncoding.DecodeString(r.Peaks)
	if derr != nil || len(peaks) == 0 {
		return trackPeaks{}, fmt.Errorf("bad peaks payload")
	}
	tp = trackPeaks{DurSec: r.DurSec, Peaks: peaks}
	if data, merr := json.Marshal(tp); merr == nil {
		u.svc.Store.PutAnalysis(store.KindPeaks, path, mtime, data)
	}
	return tp, nil
}

// peaksLoader adapts resolvePeaks to playerwin's async LoadPeaks contract (returns
// immediately; done fires from a worker goroutine).
func (u *UI) peaksLoader(path string) func(done func(peaks []byte, durSec float64, err error)) {
	return func(done func([]byte, float64, error)) {
		go func() {
			defer debuglog.Recover(u.svc.Log, "gioplayer-peaks", false)
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()
			tp, err := u.resolvePeaks(ctx, path)
			done(tp.Peaks, tp.DurSec, err)
		}()
	}
}

// Package setfp fingerprints the individual tracks of a captured set: each track's span
// within the broadcast-captured audio (offset = track.StartedAt − capture.StartedAt) is
// fingerprinted via the worker pool's fingerprint.segment job, and the print is recorded in
// the libdb change_log keyed by the portable track_hash (+ track_fp). This gives rave.page's
// music database far better fingerprint coverage than collection files alone - every played
// track yields a real Chromaprint from the actual broadcast audio.
package setfp

import (
	"context"
	"encoding/json"
	"fmt"

	"rave.page/mate/internal/governor"
	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/worker"
)

// Worker runs the fingerprint.segment job on the worker pool (*worker.Supervisor satisfies it).
type Worker interface {
	RunStream(ctx context.Context, typ, method string, params any, onProgress worker.ProgressFunc) (json.RawMessage, error)
}

// TrackSpan is one track to fingerprint: its identity + its span within the captured audio.
type TrackSpan struct {
	Artist        string
	Title         string
	OffsetSeconds float64
	LengthSeconds float64 // <=0 => to end of file
}

// Fingerprinter computes per-track fingerprints from a captured set file and records them.
type Fingerprinter struct {
	w   Worker
	lib *libdb.DB
}

// New constructs a Fingerprinter (w + lib must be non-nil to do work).
func New(w Worker, lib *libdb.DB) *Fingerprinter { return &Fingerprinter{w: w, lib: lib} }

// FingerprintSet fingerprints each track span of audioPath and appends one change_log row per
// success (field "fingerprint", origin "fingerprint", track_fp + new_value = the print).
// onProgress (may be nil) fires after each track. Returns the count fingerprinted. A single
// track's failure is skipped, not fatal - a partial set still improves coverage.
func (f *Fingerprinter) FingerprintSet(ctx context.Context, audioPath string, spans []TrackSpan, onProgress func(done, total int)) (int, error) {
	if f == nil || f.w == nil || f.lib == nil {
		return 0, fmt.Errorf("fingerprint worker or library unavailable")
	}
	done := 0
	var events []libdb.ChangeEvent
	for i, sp := range spans {
		if ctx.Err() != nil {
			break
		}
		governor.WaitWhileBusy(ctx) // defer per-track fingerprinting while streaming / mid window-drag
		params := map[string]any{"path": audioPath, "offsetSeconds": sp.OffsetSeconds, "lengthSeconds": sp.LengthSeconds}
		if raw, err := f.w.RunStream(ctx, "fingerprint", "fingerprint.segment", params, nil); err == nil {
			var res struct {
				Fingerprint string `json:"fingerprint"`
			}
			if json.Unmarshal(raw, &res) == nil && res.Fingerprint != "" {
				nv, _ := json.Marshal(res.Fingerprint)
				events = append(events, libdb.ChangeEvent{
					TrackHash: libdb.TrackHash(sp.Artist, sp.Title, 0),
					TrackFP:   res.Fingerprint,
					Field:     "fingerprint", Op: "set", Origin: "fingerprint", NewValue: string(nv),
				})
				done++
			}
		}
		if onProgress != nil {
			onProgress(i+1, len(spans))
		}
	}
	if len(events) > 0 {
		if err := f.lib.AppendChanges(events); err != nil {
			return done, err
		}
	}
	return done, nil
}

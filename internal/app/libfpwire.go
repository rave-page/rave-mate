package app

import (
	"context"
	"time"

	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/fingerprint"
	"rave.page/mate/internal/governor"
	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/libfp"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/worker"
)

// fpComputer adapts a compute-only fingerprint.Runner (Submit==nil) to libfp.Computer: it
// whole-file fpcalcs a path and returns the Chromaprint string + fpcalc duration. This gives the
// otherwise-unwired fingerprint.Runner its first production caller.
type fpComputer struct{ r *fingerprint.Runner }

func (c fpComputer) Compute(ctx context.Context, path string) (string, float64, error) {
	out, err := c.r.Process(ctx, path)
	return out.Fingerprint, out.DurationSeconds, err
}

// startLibraryFingerprintSweep launches the paced library-coverage fingerprint sweep: it
// proactively fpcalcs local library tracks that lack a print and persists each into the libdb
// change_log, so library sync can carry fingerprint_b64 and the public corpus grows beyond
// captured-set audio. The sweep is idle-gated (governor) and feature-gated (enabled), computes a
// small batch per tick, and takes days to cover a large library by design. It needs the worker
// pool (fpcalc) and the libdb; without either it is a no-op. Safe to call once at app init - the
// goroutine parks harmlessly while the feature is off.
func startLibraryFingerprintSweep(ctx context.Context, workers *worker.Supervisor, lib *libdb.DB, log *logbus.Bus, enabled func() bool, batch int, interval time.Duration) {
	if workers == nil || lib == nil {
		return
	}
	sweeper := libfp.New(lib, fpComputer{&fingerprint.Runner{Workers: workers}}, libfp.Options{
		Batch:    batch,
		Interval: interval,
		Enabled:  enabled,
		Allowed:  governor.BackgroundAllowed, // pause while a stream is live / window mid drag-resize
		Wait:     governor.WaitWhileBusy,
		Log:      log,
	})
	debuglog.Go(log, "libfp-sweep", func() { sweeper.Run(ctx) })
}

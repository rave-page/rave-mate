// Package fingerprint orchestrates audio fingerprinting: dispatches work to the
// "fingerprint" worker subprocess via the worker.Supervisor, parses the result,
// and forwards it to the rave.page ingest API.
package fingerprint

import (
	"context"
	"encoding/json"
	"fmt"
)

// WorkerRunner is satisfied by *worker.Supervisor. Defined locally to avoid a
// circular or tight import coupling.
type WorkerRunner interface {
	Run(ctx context.Context, typ, method string, params any) (json.RawMessage, error)
}

// SubmitIn is the payload forwarded to the rave.page fingerprint ingest API.
type SubmitIn struct {
	Path            string  // local file path / track reference
	Fingerprint     string  // Chromaprint fingerprint string
	DurationSeconds float64 // track duration from fpcalc
}

// Submitter sends a resolved fingerprint to the rave.page API. The real endpoint
// is TBD - implementations are provided by the api/fingerprint adapter.
type Submitter interface {
	SubmitFingerprint(ctx context.Context, in SubmitIn) error
}

// Runner orchestrates one fingerprint job end-to-end.
type Runner struct {
	Workers WorkerRunner
	Submit  Submitter // nil → compute only, skip ingest
}

// workerComputeParams mirrors the worker handler's expected JSON shape.
type workerComputeParams struct {
	Path string `json:"path"`
}

// workerComputeResult mirrors the worker handler's returned JSON shape.
type workerComputeResult struct {
	Fingerprint     string  `json:"fingerprint"`
	DurationSeconds float64 `json:"durationSeconds"`
}

// Process fingerprints the file at path via the worker subprocess. If r.Submit is
// non-nil the result is also forwarded to the ingest API. Returns the resolved SubmitIn.
func (r *Runner) Process(ctx context.Context, path string) (SubmitIn, error) {
	raw, err := r.Workers.Run(ctx, "fingerprint", "fingerprint.compute", workerComputeParams{Path: path})
	if err != nil {
		return SubmitIn{}, fmt.Errorf("fingerprint worker: %w", err)
	}
	var res workerComputeResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return SubmitIn{}, fmt.Errorf("fingerprint worker: bad result JSON: %w", err)
	}
	if res.Fingerprint == "" {
		return SubmitIn{}, fmt.Errorf("fingerprint worker: empty fingerprint for %q", path)
	}
	out := SubmitIn{
		Path:            path,
		Fingerprint:     res.Fingerprint,
		DurationSeconds: res.DurationSeconds,
	}
	if r.Submit != nil {
		if serr := r.Submit.SubmitFingerprint(ctx, out); serr != nil {
			return out, fmt.Errorf("fingerprint ingest: %w", serr)
		}
	}
	return out, nil
}

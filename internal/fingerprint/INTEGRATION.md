# Fingerprint feature - integration guide

## 1. Wire the worker type into runtime.go

Add one line to the `registry` map in `internal/worker/runtime.go`:

```go
var registry = map[string]map[string]Handler{
    "probe":       probeHandlers(),
    "fingerprint": fingerprintHandlers(), // ← add this
}
```

Do not edit any other field in that file.

## 2. Add the config feature toggle

Add a `Fingerprint` field to `Features` in `internal/config/config.go`:

```go
type Features struct {
    // existing fields …
    Fingerprint Toggle `json:"fingerprint"` // Chromaprint fingerprinting - opt-in
}
```

In `Default()`, leave it off (opt-in; requires fpcalc on PATH):

```go
Fingerprint: Toggle{Enabled: false},
```

The module manager checks `cfg.Features.Fingerprint.Enabled` before constructing
a `fingerprint.Runner` - when disabled, no worker subprocess is ever spawned.

## 3. Construct fingerprint.Runner at startup

In `internal/app/app.go` (or the relevant module manager), after the worker
`Supervisor` is created:

```go
import "rave.page/mate/internal/fingerprint"

fpRunner := &fingerprint.Runner{
    Workers: workerSupervisor,           // *worker.Supervisor satisfies WorkerRunner
    Submit:  apiClient.FingerprintIngest, // nil until the API endpoint is ready
}
```

Call `fpRunner.Process(ctx, path)` for each track that needs fingerprinting.

## 4. rave.page API endpoint (TBD)

The `Submitter` interface requires:

```go
SubmitFingerprint(ctx context.Context, in fingerprint.SubmitIn) error
```

`SubmitIn` carries:

| Field | Type | Description |
|---|---|---|
| `Path` | `string` | local file path / track reference sent by the client |
| `Fingerprint` | `string` | Chromaprint fingerprint string |
| `DurationSeconds` | `float64` | track duration reported by fpcalc |

**Endpoint (TBD):** `POST /tracks/{id}/fingerprint` or `POST /fingerprints`  
Expected request body (TBD):
```json
{ "fingerprint": "<string>", "durationSeconds": 183.5, "path": "<track-ref>" }
```

Until the endpoint is defined, pass `Submit: nil` to `Runner` - fingerprinting
still computes and returns results locally; only ingest is skipped.

## 5. External runtime dependency: fpcalc / Chromaprint

fpcalc is **not** a Go dependency. It is an external binary (part of the
[Chromaprint](https://acoustid.org/chromaprint) project) that must be present on
the host PATH.

| Platform | Install |
|---|---|
| Windows | Download from https://acoustid.org/chromaprint (zip → extract `fpcalc.exe`, add to PATH) |
| macOS | `brew install chromaprint` |
| Linux (Debian/Ubuntu) | `apt install libchromaprint-tools` |

### Graceful degradation

`fingerprint.compute` calls `exec.LookPath("fpcalc")`. When fpcalc is absent the
worker returns:

```json
{ "error": "fpcalc not found (install Chromaprint/fpcalc)" }
```

`Runner.Process` surfaces that error to the caller. The daemon does not crash or
hang. The UI / module manager should present an actionable "install fpcalc"
message rather than a generic error when this specific string is detected.

## 6. Verification checklist

- [ ] `go build ./internal/worker/ ./internal/fingerprint/` - clean
- [ ] `go vet ./internal/worker/ ./internal/fingerprint/` - clean
- [ ] `go test ./internal/worker/ ./internal/fingerprint/` - all pass (no fpcalc needed)
- [ ] `rave-mate worker fingerprint` process serves `fingerprint.ping` → `{"pong":true,"pid":<n>}`
- [ ] With fpcalc on PATH: `fingerprint.compute` returns non-empty fingerprint string
- [ ] Without fpcalc: error message contains "install Chromaprint/fpcalc"

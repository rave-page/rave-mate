# Library - integration guide

Wiring steps for the lead. Don't touch files in `internal/library/`; only edit the files listed below.

## 1. Config feature toggle (`internal/config/config.go`)

Add to `Features`:

```go
Library Toggle `json:"library"` // native file browser
```

Add to `Default()`:

```go
Library: Toggle{Enabled: true},
```

## 2. Tab in the main window (`internal/ui/ui.go`)

Import the library package and add a tab. The probe adapter (see §4) is passed here.

```go
import "rave.page/mate/internal/library"
```

In `Services`, add a probe field (or pass `nil` until Transcode is enabled):

```go
type Services struct {
    // existing fields …
    LibraryProbe library.MetadataProbe // nil = duration row omitted
}
```

In `New`, add a tab after "Traktor":

```go
container.NewTabItemWithIcon("Library", theme.FolderOpenIcon(),
    library.New(svc.LibraryProbe).View()),
```

Optionally gate behind the feature flag:

```go
if u.svc.Cfg.Features.Library.Enabled {
    u.tabs.Append(container.NewTabItemWithIcon("Library",
        theme.FolderOpenIcon(), library.New(svc.LibraryProbe).View()))
}
```

## 3. MetadataProbe adapter from worker.Supervisor

The probe calls the `probe` worker type with method `probe.duration` and parses `durationSeconds` from the JSON response. Paste this adapter into `internal/ui/probe.go` (new file, package `ui`) or anywhere that imports `worker`:

```go
package ui

import (
    "context"
    "encoding/json"
    "fmt"

    "rave.page/mate/internal/worker"
)

// WorkerProbe adapts worker.Supervisor to library.MetadataProbe.
type WorkerProbe struct{ sup *worker.Supervisor }

// NewWorkerProbe returns a probe backed by the probe worker subprocess.
func NewWorkerProbe(sup *worker.Supervisor) *WorkerProbe { return &WorkerProbe{sup: sup} }

// Duration calls the probe worker and returns durationSeconds.
func (p *WorkerProbe) Duration(ctx context.Context, path string) (float64, error) {
    raw, err := p.sup.Run(ctx, "probe", "probe.duration", map[string]any{"path": path})
    if err != nil {
        return 0, err
    }
    var res struct {
        DurationSeconds float64 `json:"durationSeconds"`
    }
    if err := json.Unmarshal(raw, &res); err != nil {
        return 0, fmt.Errorf("probe: bad response: %w", err)
    }
    return res.DurationSeconds, nil
}
```

Then in `app.go` / wherever `Services` is assembled:

```go
var probe library.MetadataProbe
if cfg.Features.Transcode.Enabled && supervisor != nil {
    probe = ui.NewWorkerProbe(supervisor)
}
svc := ui.Services{
    // …
    LibraryProbe: probe,
}
```

## 4. Feature flag in settings (`internal/ui/view_settings.go`)

Add a card next to the other feature cards:

```go
func (u *UI) libraryCard() fyne.CanvasObject {
    f := &u.svc.Cfg.Features.Library
    toggle := u.simpleToggle(&f.Enabled)
    return featureCard("Library", "Native file browser + media metadata viewer.", toggle)
}
```

Call it from `buildSettings()`:

```go
u.libraryCard(),
```

## Summary of files to touch

| File | Change |
|---|---|
| `internal/config/config.go` | `Library Toggle` in `Features`; `Library: Toggle{Enabled: true}` in `Default()` |
| `internal/ui/ui.go` | `LibraryProbe library.MetadataProbe` in `Services`; new tab in `New()` |
| `internal/ui/probe.go` | New file - `WorkerProbe` adapter |
| `internal/ui/view_settings.go` | `libraryCard()` helper + call in `buildSettings()` |

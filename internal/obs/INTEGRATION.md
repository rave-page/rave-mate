# OBS Integration - wiring guide for the lead

Describes what the lead must add to shared files to wire `internal/obs` into the app.
Package `internal/obs` itself is complete; no shared file was edited.

## 1. Config schema (`internal/config/config.go`)

Add `OBSFeature` and a field on `Features`:

```go
// OBSFeature configures the OBS obs-websocket v5 client.
type OBSFeature struct {
    Enabled  bool   `json:"enabled"`
    Host     string `json:"host"`     // default 127.0.0.1
    Port     int    `json:"port"`     // default 4455
    Password string `json:"password"` // leave empty if auth disabled in OBS
}

// ResolvedHost returns Host or the default.
func (o OBSFeature) ResolvedHost() string {
    if o.Host != "" { return o.Host }
    return "127.0.0.1"
}

// ResolvedPort returns Port or the default.
func (o OBSFeature) ResolvedPort() int {
    if o.Port > 0 { return o.Port }
    return 4455
}
```

In `Features`:
```go
OBS OBSFeature `json:"obs"`
```

In `Default()` → `Features{…}`:
```go
OBS: OBSFeature{Enabled: false, Host: "127.0.0.1", Port: 4455},
```

OBS is opt-in (disabled by default) - most users don't stream from OBS.

## 2. Settings card (`internal/ui/view_settings.go`)

Add after `u.vrchatCard()` (or wherever suits the order):

```go
u.obsCard(),
```

Implement `obsCard`:

```go
func (u *UI) obsCard() fyne.CanvasObject {
    f := &u.svc.Cfg.Features.OBS

    hostEntry := widget.NewEntry()
    hostEntry.SetText(f.ResolvedHost())
    hostEntry.OnChanged = func(s string) { f.Host = s; u.saveCfg() }

    portEntry := widget.NewEntry()
    portEntry.SetText(strconv.Itoa(f.ResolvedPort()))
    portEntry.OnChanged = func(s string) {
        if n, err := strconv.Atoi(s); err == nil && n > 0 && n < 65536 {
            f.Port = n; u.saveCfg()
        }
    }

    pwEntry := widget.NewPasswordEntry()
    pwEntry.SetPlaceHolder("leave empty if OBS auth is disabled")
    pwEntry.SetText(f.Password)
    pwEntry.OnChanged = func(s string) { f.Password = s; u.saveCfg() }

    connectBtn := widget.NewButton("Connect & validate", func() {
        go func() {
            c, err := obs.Connect(context.Background(),
                f.ResolvedHost(), f.ResolvedPort(), f.Password)
            if err != nil {
                u.Notify("OBS", "connect failed: "+err.Error())
                return
            }
            defer c.Close()
            diffs, err := c.ValidateStreamSettings(obs.DefaultStreamRequirements())
            if err != nil {
                u.Notify("OBS", "validate failed: "+err.Error())
                return
            }
            if len(diffs) == 0 {
                u.Notify("OBS", "Stream settings OK.")
            } else {
                u.Notify("OBS", "Issues: "+strings.Join(diffs, "; "))
            }
        }()
    })

    toggle := u.simpleToggle(&f.Enabled)
    body := container.NewVBox(
        container.NewBorder(nil, nil, widget.NewLabel("Host"), nil, hostEntry),
        container.NewBorder(nil, nil, widget.NewLabel("Port"), nil, portEntry),
        container.NewBorder(nil, nil, widget.NewLabel("Password"), nil, pwEntry),
        connectBtn,
    )
    return featureCard("OBS", "Connect to OBS Studio via obs-websocket v5 to validate stream settings.", toggle, body)
}
```

Imports needed: `"context"`, `"strings"`, `"rave.page/mate/internal/obs"`.

## 3. Live panel (optional tab or dashboard section)

If a persistent OBS panel is wanted (rather than the one-shot connect+validate above),
call `obs.View(client)` and embed it in a tab or the dashboard grid:

```go
// in u.buildDashboard() or a new "OBS" tab:
if u.svc.OBS != nil {
    sections = append(sections, obs.View(u.svc.OBS))
}
```

`View` is safe to call once per client lifetime; it drives itself via goroutines +
`fyne.Do`.

## 4. `obs` module (`internal/module` or new `internal/obs/module.go`)

Wire a module that connects on enable and tears down on disable/stop:

```go
// Module wraps a Client for the module manager lifecycle.
type Module struct {
    cfg *config.OBSFeature // pointer into live config
    mu  sync.Mutex
    c   *obs.Client
}

func (m *Module) Start(ctx context.Context) error {
    c, err := obs.Connect(ctx, m.cfg.ResolvedHost(), m.cfg.ResolvedPort(), m.cfg.Password)
    if err != nil { return err }
    m.mu.Lock(); m.c = c; m.mu.Unlock()
    return nil
}

func (m *Module) Stop() {
    m.mu.Lock(); defer m.mu.Unlock()
    if m.c != nil { _ = m.c.Close(); m.c = nil }
}

func (m *Module) Client() *obs.Client {
    m.mu.Lock(); defer m.mu.Unlock(); return m.c
}
```

Register in `app.go` / `module.Manager` the same way `studio` and `traktor` modules are
registered, gated on `cfg.Features.OBS.Enabled`.

Expose `OBS *obs.Module` on `ui.Services` so `obsCard` and `View` can reach the live
client.

## 5. StreamRequirements from the rave.page API (TBD)

`obs.DefaultStreamRequirements()` returns hard-coded placeholders (6000 kbps / 2s / obs_x264).

The real values should come from the stream-requirements API endpoint - something like
`GET /streams/{id}/requirements` or a field on the stream object - but no such endpoint
exists in the current OpenAPI spec.

**Action for API team:** expose `StreamRequirements{videoBitrateKbps, keyframeSec, encoder}`
on the stream resource or as a static `/streaming/requirements` endpoint.
Once available, run `make generate-api`, read via the generated client, and pass the
result to `obs.ValidateStreamSettings(obs.StreamRequirements{…})`.

## 6. Security note

The OBS WebSocket password is stored in plain JSON under the OS config dir
(`os.UserConfigDir()/rave-mate/config.json`). On Windows this is under `AppData\Roaming`,
readable only by the user account. No additional sealing (DPAPI) is applied for now.
If the team decides to seal it, apply the same DPAPI wrapper used for the auth token
in `internal/auth`.

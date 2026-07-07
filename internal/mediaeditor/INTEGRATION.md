# mediaeditor integration guide

Steps to wire `internal/mediaeditor` into the running app.

## 1. Feature toggle - `internal/config/config.go`

Add `MediaEditor Toggle` to `Features` and default it on:

```go
// in Features struct
MediaEditor Toggle `json:"mediaEditor"` // poster/thumbnail composer

// in Default()
MediaEditor: Toggle{Enabled: true},
```

## 2. "Editor" tab - `internal/ui/ui.go`

Import the package and add a tab. Requires a non-nil `Services.API`.

```go
import "rave.page/mate/internal/mediaeditor"

// in New(), inside container.NewAppTabs(…):
container.NewTabItemWithIcon(
    "Editor", theme.DocumentCreateIcon(),
    mediaeditor.New(newAPISource(svc)),
),
```

Gate on the feature flag if desired:

```go
if svc.Cfg.Features.MediaEditor.Enabled {
    u.tabs.Append(container.NewTabItemWithIcon(
        "Editor", theme.DocumentCreateIcon(),
        mediaeditor.New(newAPISource(svc)),
    ))
}
```

## 3. `APISource` implementation - `internal/ui/` (or `internal/api/`)

Implement `mediaeditor.APISource` against `api.Client`:

```go
type apiSource struct{ c *api.Client; tok func() string }

func newAPISource(svc Services) mediaeditor.APISource {
    return &apiSource{c: svc.API, tok: func() string {
        if svc.Auth == nil { return "" }
        return svc.Auth.Token()
    }}
}

func (s *apiSource) UpcomingEvents(ctx context.Context) ([]mediaeditor.EventData, error) {
    // see §4 below
}
```

## 4. rave.page API endpoints needed

Tested against `https://development.api.rave.page`. Re-run `make generate-api` and
check `internal/apiclient/apiclient.gen.go` for exact method names after the next schema update.

### 4.1 Upcoming events

```
GET /events?filter=upcoming&limit=20
Authorization: Bearer <user_token>
```

Response shape (relevant fields):

```json
{
  "events": [
    {
      "id": "…",
      "name": "Friday Night Techno",
      "starts_at": "2025-06-07T22:00:00Z",
      "cover_image_url": "…",
      "logo_url": "…"
    }
  ]
}
```

Map → `EventData.Title = event.name`, `EventData.Date = formatDate(event.starts_at)`.

**Status**: method TBD - not yet in `apiclient.gen.go`. Add via `apiclient` regen or
hand-write a `api.Client.UpcomingEvents` helper (same pattern as `WhoAmI`).

### 4.2 Booked DJs / performers

```
GET /events/{event_id}/lineup
Authorization: Bearer <user_token>
```

Response:

```json
{
  "lineup": [
    { "id": "…", "display_name": "DJ Alpha", "avatar_url": "…", "logo_url": "…" }
  ]
}
```

Map → `EventData.DJs` (slice of `display_name`).
Optional: download `logo_url` of the first headliner to a temp file → `EventData.LogoPath`.

**Status**: TBD - check generated client after next `make generate-api`.

### 4.3 Artist assets / stream links

```
GET /profiles/{profile_id}/assets
Authorization: Bearer <user_token>
```

Returns downloadable press-kit assets (hi-res logo, EPK PDF). Use logo asset as `LogoPath`
if the lineup endpoint doesn't expose one directly.
Stream links live on `GET /streams?profile_id=…&filter=upcoming`.

**Status**: TBD.

## 5. Native file pickers (follow-up)

Background and logo path entries currently accept typed paths.
Replace with native file pickers once Fyne v2.x `dialog.ShowFileOpen` integration is
confirmed stable on all three platforms. The `Poster` struct is path-based so the change
is local to `view.go` (`bgEntry`/`logoEntry` handlers).

## 6. `Services` field

No new field is strictly required - `Services.API` and `Services.Auth` (for the token) are sufficient. If live-reload of the event list is wanted, add a reload button that calls `UpcomingEvents` again; SSE is not needed here.

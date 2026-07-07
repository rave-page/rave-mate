# Sessions view - integration guide

## What it does

Lists the user's Traktor play-history sets (≈125 NML files) newest-first with date,
track count, and total duration. Clicking a row expands the played tracklist.

## Data flow

```
TraktorInstall.HistoryDir  (from DiscoverTraktor, user picks in Settings)
   └─ LoadSessions(historyDir)
         ├─ os.ReadDir → each *.nml
         ├─ ParseHistory(name, file) → Session{Name, Played, StartedAt=zero}
         ├─ ParseHistoryFilename(name) → stamps Session.StartedAt
         └─ sort newest-first → []Session
              └─ Summarize(s) → SessionSummary{Name, StartedAt, TrackCount, TotalDurationSec}
```

## Fyne view (Sessions tab)

```
┌─────────────────────────────────────────────────────┐
│  Sessions   [refresh icon]                          │
├──────────────────────┬────────────┬─────────────────┤
│  Date                │  Tracks    │  Duration       │
├──────────────────────┼────────────┼─────────────────┤
│  2026-06-04 01:26    │  42        │  3h 12m         │
│  2026-05-28 22:15    │  38        │  2h 58m         │
│  …                   │  …         │  …              │
└──────────────────────┴────────────┴─────────────────┘
                  ▼ (tap row → expand)
  ┌──────────────────────────────────────────────────┐
  │  Played tracks - 2026-06-04                      │
  │  01  B:\Music\Sets\track1.flac         8m 02s    │
  │  02  B:\Music\Sets\track2.flac         7m 45s    │
  │  …                                               │
  └──────────────────────────────────────────────────┘
```

## Widget wiring sketch

```go
// In the Sessions tab constructor:
func newSessionsTab(historyDir string) fyne.CanvasObject {
    sessions, _ := musiclib.LoadSessions(historyDir)
    summaries := make([]musiclib.SessionSummary, len(sessions))
    for i, s := range sessions {
        summaries[i] = musiclib.Summarize(s)
    }

    list := widget.NewList(
        func() int { return len(summaries) },
        func() fyne.CanvasObject {
            return container.NewHBox(
                widget.NewLabel(""), // date
                widget.NewLabel(""), // tracks
                widget.NewLabel(""), // duration
            )
        },
        func(id widget.ListItemID, item fyne.CanvasObject) {
            s := summaries[id]
            box := item.(*fyne.Container)
            box.Objects[0].(*widget.Label).SetText(s.StartedAt.Format("2006-01-02 15:04"))
            box.Objects[1].(*widget.Label).SetText(fmt.Sprintf("%d tracks", s.TrackCount))
            mins := int(s.TotalDurationSec) / 60
            box.Objects[2].(*widget.Label).SetText(fmt.Sprintf("%dh %02dm", mins/60, mins%60))
        },
    )

    list.OnSelected = func(id widget.ListItemID) {
        showTracklist(sessions[id]) // open detail panel / dialog
    }
    return list
}
```

## historyDir source

Populated from the selected `TraktorInstall`:

```go
installs, _ := musiclib.DiscoverTraktor()
// present installs[i].Version in a SmartSelect-equivalent (Fyne Select)
// on confirm: historyDir = installs[selected].HistoryDir
```

`HistoryDir` is already set by `DiscoverTraktor` if the `History/` sub-dir exists under
the Traktor version folder. Empty string = no history yet; gate the tab behind a nil-check
and show a "No history directory found" label.

## Refresh

Re-run `LoadSessions` on demand (button or file-watcher via `fsnotify`, already in
`go.sum`). Traktor appends a new NML after each session ends - no need to poll.

## Performance

`ParseHistory` is token-streaming. 125 small NML files (≈50 KB each) load in <100 ms.
No caching needed for the list view; cache `[]Session` in the tab state for the detail
drill-down to avoid re-reads.

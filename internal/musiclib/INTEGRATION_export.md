# Export Library - Integration Notes

## Purpose

Three export formats for portability + metadata backup:

| Format | Use case |
|---|---|
| M3U8 | Load a rave.page playlist in any media player / DJ software |
| CSV | Full metadata backup; diff-able; importable by spreadsheet tools |
| Rekordbox XML | One-shot collection migration to Rekordbox / other OAS-compatible software |

## API

```go
// All write to a caller-provided io.Writer. Read-only on Track input.
ExportM3U(tracks []Track, w io.Writer) error
ExportCSV(tracks []Track, w io.Writer) error
ExportRekordboxXML(tracks []Track, w io.Writer) error
```

## Wiring into the UI ("Export library" action)

1. Load the library via `ParseCollection` (existing).
2. Present format picker: M3U / CSV / Rekordbox XML.
3. Get destination path.
   - **Now:** a text entry (`widget.NewEntry`) pre-filled with a sensible default
     (`~/Music/rave-mate-export.{m3u|csv|xml}`).
   - **Follow-up:** replace with a native save dialog once Fyne exposes
     `dialog.ShowFileSave` with OS-level path picker (Fyne 2.x roadmap).
4. Open file for writing (`os.Create`), call the chosen exporter, close.
5. Show success notification via `app.SendNotification` or the logbus.

## Suggested placement

Library tab → "Export…" button → format + path dialog → confirm.
Could also live in a context menu on a playlist/collection node.

## Field mapping notes (Rekordbox XML)

| Rekordbox attr | Track field | Notes |
|---|---|---|
| `Name` | `Title` | |
| `Artist` | `Artist` | |
| `Composer` | `Label` | Rekordbox has no Label attr; Composer is the closest available slot |
| `Album` | `Album` | |
| `Genre` | `Genre` | |
| `Tonality` | `Key` | Rekordbox calls key "Tonality" |
| `AverageBpm` | `BPM` | formatted to 2 dp (`"138.00"`) |
| `TotalTime` | `DurationSec` | integer seconds |
| `BitRate` | `BitrateBps / 1000` | kbps |
| `Size` | `FileSizeKB * 1024` | bytes |
| `PlayCount` | `PlayCount` | |
| `Rating` | `Rating * 51` | Rekordbox 0–255; we map 0–5 stars → 0,51,102,153,204,255 |
| `DateAdded` | `ImportDate` | raw source date |
| `Year` | first 4 chars of `ReleaseDate` | extracted if it starts with a 4-digit year |
| `Location` | `Path` | `file://localhost/<url-encoded forward-slash path>` |

Cue points not exported (planned follow-up: emit `<POSITION_MARK>` nodes).

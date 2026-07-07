# VRChat tab - status/bio + animated-emoji flipbook

A VRChat tab (gated on the VRChat feature) for acting on the logged-in account: status/bio
editing with presets + event variables, and an animated-emoji ("flipbook") sprite-sheet
generator. Sign-in/2FA stays on the Settings VRChat card; this tab uses that session.

## Pieces

| pkg / file | role |
|---|---|
| `internal/flipbook/flipbook.go` | video/GIF → 1024² VRChat emoji sprite sheet (ffmpeg extract + Go grid assembly) |
| `internal/vrchat/profile.go` | `UpdateStatus` / `UpdateBio` (PUT /users/{id}) + Manager wrappers + status enum/limits |
| `internal/ui/view_vrchat.go` | the tab: status/bio editor + emote generator + preset/var dialogs |
| `internal/config` (`VRChatFeature`) | status/bio presets, manual bio vars, flipbook output dir |

## Flipbook (sprite sheet)

VRChat emoji spec (wiki.vrchat.com/wiki/Emojis): 1024×1024 PNG, square frames, uniform grid,
ordered L→R then T→B. Tiers: **2×2 = 4 @512px · 4×4 = 16 @256px · 8×8 = 64 @128px** (max 64).
VRChat reads default frame-count + FPS from the filename, so the sheet is named
`<name>_<N>frames_<fps>fps.png`.

`flipbook.Generate(ffmpegPath, Options)`:
1. one ffmpeg pass samples + (optional) crops + scales-to-fit + transparently pads the source to
   exact square tier frames, written as numbered PNGs to a temp dir;
2. Go (`image/draw`) tiles them into the 1024² sheet in grid order. Ping-pong appends the reversed
   middle frames (extracts only `N/2+1` distinct frames). A short clip reuses its last frame so no
   cell is blank.

ffmpeg argv (e.g. 16-frame @20fps, trim 2s, no crop):
```
ffmpeg -y -hide_banner -loglevel error -ss 2.000 -t 1.300 -i <src> \
  -vf "fps=20,scale=256:256:force_original_aspect_ratio=decrease:flags=lanczos,format=rgba,pad=256:256:(ow-iw)/2:(oh-ih)/2:color=black@0.0" \
  -frames:v 16 -start_number 0 <tmp>/f_%05d.png
```
(`crop=W:H:X:Y` is inserted after `fps=` when a crop is set.)

`Options{Input, OutName, Frames(4|16|64), FPS, TrimStart, TrimEnd, Crop *Rect, PingPong, OutDir}`;
helpers `Tiers()`, `TierFor(n)`, `OutFileName(name,n,fps)`, `Options.Validate()`. ffmpeg resolved
via `mediatools.Resolve("ffmpeg")` (reuses the managed-binary plumbing - no new dep).

**Upload:** website-only (Gallery ▸ Emoji ▸ Enable Sprite Sheet Mode); custom emoji need VRC+.
There is **no public VRChat API for emoji upload/listing** (verified vs the wiki + API spec), so the
generator saves the sheet and opens `EmojiUploadURL` - it does not fake an upload.

## Status & bio

`PUT /users/{currentUserId}` partial patch over the existing cookie session:
- `UpdateStatus(status, statusDescription)` - status ∈ {join me, active, ask me, busy};
  statusDescription ≤ 32.
- `UpdateBio(bio, bioLinks)` - bio ≤ 512; bioLinks ≤ 3 (nil = leave links untouched).

UI: status picker + statusDescription entry (live ≤32 counter, turns brand-hot over limit);
multiline bio with live **post-resolution** preview + ≤512 counter. **Bio variables**: `{next_event}`
and `{next_event_date}` resolve from the soonest upcoming rave.page event (`api.ListEvents`);
`{next_event_venue}` (not in the events list) + any custom `{placeholder}` come from manual
`BioVars`. Resolution reuses the pure `twitch.ResolveTemplate`/`TemplateVars` helpers. Status + bio
**presets** (saved in config) quick-apply/manage like Twitch title presets.

## Config (additive, off/empty by default - `VRChatFeature`)

```jsonc
"vrchat": {
  "statusPresets": [{ "name": "...", "status": "active", "description": "..." }],
  "bioPresets":    [{ "name": "...", "template": "Next: {next_event} ({next_event_date})" }],
  "bioVars":       { "next_event_venue": "..." },
  "flipbookDir":   ""   // "" = <configDir>/emoji
}
```
`omitempty`; no schema-version bump (load-over-default zero values = the intended off/empty).

## Verified vs. needs live check

Verified here: `go build ./...` (incl. cgo exe), `go vet`, `golangci-lint` (default + `vr` tags),
`go test ./...` - including a flipbook ffmpeg integration test that synthesizes a clip and produces a
real 1024² sheet.

**Not verified (needs a logged-in VRChat account / live app):**
- Status/bio `PUT /users/{id}` against the real VRChat API (field names + acceptance);
- a generated sheet actually uploading + animating correctly in VRChat;
- the live Fyne tab - the UI could not be driven here because the user's existing rave-mate instance
  holds the single-instance ctl port (47620); launching a second instance would just forward to it.
```

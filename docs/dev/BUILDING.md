# Building rave-mate

## Prerequisites

- Go (version in `go.mod`), a C toolchain (cgo — needed by both the WebView2 default renderer
  and the Fyne fallback: MinGW-w64 on Windows, gcc + GL/X11 dev packages on Linux:
  `libgl1-mesa-dev xorg-dev libxkbcommon-dev`), git.
- **Zig** (native UI/DSP/enc/vfx libs + the `rave-shell.exe` window host): build them first with
  `bash scripts/build-zig.sh` (POSIX; `scripts/build-zig.ps1` on PowerShell). The `zigui`/`shellembed`
  tags below link + embed these; a stale/missing `.a` is not in Go's test cache key and surfaces as a
  bogus "v2 render failed" — rebuild before tagged tests.
- No external shared module: rave-mate is self-contained (formerly-shared code lives in
  `internal/shared`). Builds standalone with `GOWORK=off`.
- Optional feature SDKs:
  - **Spout** (Windows GPU video share): `make spout-sdk` fetches + SHA-verifies the SDK into
    `third_party/spout`; build with `-tags spout`; ship `SpoutLibrary.dll` beside the exe.
  - **VR** (OpenVR overlays/motion): build with `-tags vr`; ship `openvr_api.dll` beside the
    exe. DLLs are runtime-loaded - absence only disables the feature.

## Commands

| Task | Command |
|---|---|
| Build (current OS) | `make build` |
| Everything-on Windows exe (recommended) | `scripts/build-local.ps1` — runs `build-zig.sh` then builds with the full-feature tags below |
| Full-feature Windows exe (manual) | `bash scripts/build-zig.sh && CGO_ENABLED=1 go build -tags "spout vr abletonlink zigdsp zigui zigvr encembed shellembed" -ldflags "-s -w -H windowsgui -linkmode external -extldflags '-static -static-libgcc -static-libstdc++'" -o dist/rave-mate.exe ./cmd/rave-mate` |
| Fyne-fallback-only exe (no webview) | `go build -tags "spout vr" -ldflags "-s -w -H windowsgui -extldflags=-static" -o dist/rave-mate.exe ./cmd/rave-mate` |
| Run | `go run ./cmd/rave-mate` |
| Headless service | `go run ./cmd/rave-mate --service` |
| Tests / vet / fmt | `make test` / `make vet` / `make fmt` |
| Lint | `golangci-lint run ./...` (also with `--build-tags "spout vr"`) |
| Vulnerability scan | `make vuln` |
| Supply-chain soak gate | `make soak` |
| Regenerate API client | `make generate-api` (never hand-edit `internal/apiclient`) |
| Windows icon resource | `make generate-icon` (only when `icon.png` changes) |

`-extldflags=-static` matters on Windows: without it the exe needs MinGW DLLs
(`libgcc_s_seh-1.dll`, `libstdc++-6.dll`) and fails on a clean machine.

**Renderer tags matter.** The default UI is the webview, hosted by `rave-shell.exe`; `zigui` links
the Zig render layer and `shellembed` embeds the shell exe. A build **without** those tags (e.g. plain
`-tags "spout vr"`) has no shell child and **silently falls back to Fyne** — the wrong surface to
verify UI on. Tell them apart at runtime with `ctl snapshot`: the webview prints HTML DOM
(`div`/`a`/`span`), Fyne prints widgets (`button {id} "text"`).

## Verifying changes on the running app

The single-instance guard doubles as a control socket (`127.0.0.1:47620`):

```
rave-mate ctl status | tab <name> | click <text> | read <id> | snapshot
rave-mate ctl screenshot out.png | screenshot-all <dir> | logs | quit
rave-mate ctl act <act> [val]   # post a raw UI action through the page act pipeline
                                # (webview renderer) - drives keyboard scopes / pointer
                                # lanes with no clickable element, e.g.
                                # `act key:cueedit del`, `act mp-surf:library down:0.3,0.5`.
                                # An act with embedded whitespace (paths) must be quoted:
                                # `act '"ce-open:C:\My Music\track.flac"' [val]` - \" is a
                                # literal quote, other backslashes verbatim (no doubling)
```

Build → launch → drive the golden path via ctl → check `logs` → `quit`. Then run
`rave-mate ctl screenshot-all <dir>`: sweeps EVERY tab (+ scroll positions), writes PNGs +
`report.txt` with ⚠OVERFLOW findings - eyeball the shots and fix visual issues you spot, even
pre-existing ones. Docs screenshots come from the same commands.

**Isolated instance** (verify beside a real running rave-mate without touching it):

```
RAVE_MATE_CTL_ADDR=127.0.0.1:47733 RAVE_MATE_CONFIG_DIR=/tmp/mate-test ./dist/rave-mate.exe &
RAVE_MATE_CTL_ADDR=127.0.0.1:47733 ./dist/rave-mate.exe ctl status
```

Both env vars must be set together - the ctl addr is also the single-instance guard, and the
config dir keeps state (config, library, secrets) out of the real instance's.

**Window host** (`RAVE_MATE_SHELL`, webview renderer only): `cgo` (default) runs the WebView2 window
in the daemon; `proc` runs it in a supervised child (`rave-mate feature webview`), so a wedged or
crashed window costs you the window, not the daemon - it is killed and relaunched and the page is
re-rendered from state. ctl behaves identically either way (screenshots included: the daemon captures
the child's window handle directly). Protocol + rationale: `.devnotes/ZIG_UI_GUIDE.md` "Phase B - B5
procShell protocol".

```
RAVE_MATE_SHELL=proc ./dist/rave-mate.exe        # window in a child; tasklist shows rave-mate-feature-webview.exe
```

## API endpoint

Defaults to the rave.page development API; override with `RAVE_API_BASE_URL`. Production only on
explicit opt-in.

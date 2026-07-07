# Building rave-mate

## Prerequisites

- Go (version in `go.mod`), a C toolchain (Fyne/cgo: MinGW-w64 on Windows, gcc + GL/X11 dev
  packages on Linux: `libgl1-mesa-dev xorg-dev libxkbcommon-dev`), git.
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
| Everything-on Windows exe | `go build -tags "spout vr" -ldflags "-s -w -H windowsgui -extldflags=-static" -o dist/rave-mate.exe ./cmd/rave-mate` |
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

## Verifying changes on the running app

The single-instance guard doubles as a control socket (`127.0.0.1:47620`):

```
rave-mate ctl status | tab <name> | click <text> | read <id> | snapshot
rave-mate ctl screenshot out.png | screenshot-all <dir> | logs | quit
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

## API endpoint

Defaults to the rave.page development API; override with `RAVE_API_BASE_URL`. Production only on
explicit opt-in.

# Contributing to rave-mate

Thanks for helping. Read this before opening a PR - the rules below are how the project stays
small, fast and auditable.

## Philosophy

- **Go-driven UI, no JS framework.** UI is transitioning from a native Fyne renderer to a
  Go-driven HTML/CSS webview (`internal/webui`) - Go renders every view and drives the DOM;
  no web server, no JS framework. Brand identity comes from design tokens, not grafted web tech.
- **Stdlib-first, minimal deps.** Every new direct dependency needs a justification row in
  `SUPPLY_CHAIN.md` and must pass the **7-day soak**: pin an exact version at least 7 days old,
  never `go get pkg@latest`. Prefer re-implementing small things over importing.
- **Local-first + transparent.** Nothing leaves the user's machine without being visible in the
  Logs tab. Secrets are sealed at rest (`internal/shared/secureseal`, DPAPI on Windows) and never logged.
- **Features are independent toggles.** Disabled = zero footprint (no ports, goroutines,
  subprocesses). New capabilities get their own `Feature` struct in `internal/config` and, if
  they own runtime state, a `module.Service`.
- **Crash isolation.** Heavy/cgo/flaky work runs in supervised subprocesses (`internal/worker`,
  `internal/featurehost`) so a fault kills a child, not the app.
- **Token-economy style.** Terse code, terse comments, terse docs. Comments only where intent
  isn't obvious; exported decls get one-line Go-doc comments.

## Workflow

1. Fork/branch from `development`.
2. Make a focused change. New config fields → bump `configVersion` (see the version history
   comment in `internal/config/config.go`); loading older files must keep working.
3. Gates - all must pass locally before you push:
   ```
   go build ./...
   go vet ./...
   go test ./...
   gofmt -l .          # must print nothing (outside third_party/)
   golangci-lint run ./...
   ```
   Feature-tagged builds too if you touched them: `CGO_ENABLED=1 go build -tags "spout vr" ./...`.
4. **Verify against the running app.** A clean build proves code correctness, not feature
   correctness. Build, launch, drive it via `rave-mate ctl` (`status`, `tab`, `click`, `read`,
   `snapshot`, `screenshot`, `logs`, `quit`) and confirm your feature actually works. Then run
   `rave-mate ctl screenshot-all <dir>` (every tab + scroll positions + ⚠OVERFLOW report) and
   fix obvious visual issues it surfaces - even ones your change didn't cause. Say what you
   verified in the PR.
5. Commit per logical unit (feature/fix/phase), not one mega-commit. Clean up scratch artifacts.
6. PR against `development` with: what/why, verification notes, screenshots for UI changes.

## UI contributions

- Colors/spacing/fonts come from `internal/ui/theme.go` design tokens - never hardcode hex/px.
- Reuse the kit (`internal/ui/kit_*.go`: buttons, search fields, segmented, tooltips) and
  helpers (`featureCard`, `cardWithHelp`, `formGrid`) before inventing new widgets.
- Every non-obvious control gets a `?` help tooltip (`help.go`).
- UI updates from goroutines go through `fyne.Do`; spawn goroutines via `goUI` (panic-guarded).
- Tray app semantics: closing the window hides it; only tray Quit exits.

## Security

- Never log tokens, cookies, passwords, or 2xx response bodies of authed APIs.
- Secrets at rest go through `internal/shared/secureseal`.
- Found a vulnerability? See `SECURITY.md` - please don't open a public issue first.

## C# / Unity plugin (`internal/unityproj/unitypkg`)

The Go build embeds but cannot compile the C#. If you touch it: keep it minimal, mirror the
existing style, mark anything you didn't run in Unity as unverified in the file header, and
ideally verify in a Unity 2022.3 VRChat project before the PR.

## Licensing of contributions

By submitting a contribution you agree it's licensed under the repo license
(Apache-2.0 + Commons Clause) per Apache-2.0 §5.

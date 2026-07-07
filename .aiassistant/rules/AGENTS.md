---
apply: always
---

# rave-mate agent rules

Canonical rules live in **`CLAUDE.md`** at the repo root - read it fully before writing
code. It defines the design philosophy, architecture, non-negotiable rules, and security
posture. The summary below is a pointer, not a substitute; `CLAUDE.md` is authoritative.

## Non-negotiables (summary)

- **Terse, token-economy** code, comments, and docs. Drop filler; comment only non-obvious
  intent; exported decls get one-line Go-doc comments.
- **Supply chain: 7-day soak, stdlib-first.** Never `go get pkg@latest`; pin exact versions
  ≥7 days old; justify every direct dep in `SUPPLY_CHAIN.md`.
- **Isolate resource-bearing work.** Anything handling media/audio/frames, spawning child
  processes, or using fault-prone cgo runs in a supervised `featurehost`/`worker` subprocess
  with explicitly bounded buffers (cap + drop policy) - never in the daemon. A panic-guard is
  not isolation.
- **UI is Go-driven, no JS framework.** Transitioning from a native Fyne renderer to a
  Go-driven HTML/CSS webview (`internal/webui`): Go renders every view and drives the DOM;
  no web server, no JS framework. Brand comes from design tokens (`internal/ui/theme.go`) -
  never hardcode hex/px.
- **No unchecked `any`.** Concrete types; `any` only at real wire/plugin boundaries.
- **`gofmt` + `go vet` clean;** every `err` checked or explicitly discarded with a reason.
- **Security posture:** never log tokens, cookies, passwords, or authed 2xx response bodies;
  secrets sealed at rest via `internal/shared/secureseal`; control channels are loopback/LAN
  only with ECDH + per-frame HMAC + origin allowlist. Report vulnerabilities per `SECURITY.md`.
- **Verify against the running app** (`rave-mate ctl` / `screenshot-all`), not just a clean
  build - a green build proves code correctness, not feature correctness.
- **Commit per logical unit;** never push or open PRs unless explicitly asked.

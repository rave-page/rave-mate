# Getting started

## Install & run

Grab the installer/binary from Releases (Windows exe is fully static; `openvr_api.dll` and
`SpoutLibrary.dll` ship beside it - deleting them only disables VR/Spout). Or build from source
(`docs/dev/BUILDING.md`).

rave-mate is a **tray app**: closing/minimizing the window hides it; it keeps working in the
tray. Quit via the tray menu. For an always-on box, install it as an OS service:
`rave-mate install` (Windows needs admin; Linux uses systemd --user, macOS launchd) - service
mode is headless, the UI reattaches when you launch normally.

## Release channels - read this

Builds are `alpha` / `beta` / `production`. **Alpha/beta/dev builds warn on launch**: they are
development releases. Always keep backups of your media, library and config before using one -
the authors are not liable for damage to files or systems (see LICENSE). Production builds skip
the warning.

## Sign in (rave.page)

Settings → Account → "Sign in with browser". Your browser holds the login; rave-mate never sees
a password - it receives a one-time grant via a `rave://` deeplink and exchanges it for
tokens, sealed at rest with your OS's secret store (DPAPI on Windows). Most local features work
signed-out; publishing/streaming/events need the account.

## The Settings model

Every capability is an independent card with a toggle. Off = no ports, no goroutines, no
subprocesses. Status dots: grey off · amber attention · mint live. Hover the `?` icons - every
non-obvious control explains itself in-app.

## Logs & transparency

The Logs tab is a live ring buffer of everything the app does, including every HTTP request
that leaves your machine (methods/paths/status only - never tokens or bodies). Per-interface
monitor tabs (MIDI, Traktor, session) show raw event firehoses when debugging.

## Updates

Release builds self-update from their channel feed (Settings → Updates). Manifests are
Ed25519-signature-verified. The updater swaps only the exe; helper DLLs can self-heal from the
same page.

## Config & data

Config lives in your OS user-config dir (`rave-mate/config.json`, versioned + migrated
automatically). Sealed secrets (`*.bin`) sit next to it and are machine-bound - they don't
survive copying to another PC (sign in again there).

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

Cards are grouped into sub-tabs (Account & API, DJ sources, Recording, Streaming & remote,
Library & media, Integrations, System); each pill carries its group's aggregate status dot.
Don't know where a setting lives? Type in the **search box above the pills** - it filters every
setting across all sub-tabs (labels and help texts included) and shows matches grouped by
section. Clear the box to get the sub-tabs back.

Settings → Updates lives under **System**; sign-in and interface language under **Account & API**.

## Logs & transparency

The Logs tab is a live ring buffer of everything the app does, including every HTTP request
that leaves your machine (methods/paths/status only - never tokens or bodies). Per-interface
monitor tabs (MIDI, Traktor, session) show raw event firehoses when debugging.

## Updates

Release builds poll their channel feed every 5 minutes (a tiny manifest fetch; backs off while
offline). The first time a new version is seen you get one notification - tray balloon + in-app
toast - exactly once per version, surviving restarts.

When an update is known, a block appears at the bottom of the navigation rail (and a matching
item in the tray menu) with ONE state-dependent action: **Download vX** → progress →
**Install update** (only after verification) → **Restart to apply**. Nothing is rendered when
you're up to date. The ⓘ tooltip on the block explains channels + verification in depth.
Settings → System → Updates offers the same flow plus a manual check.

Verification: manifests are Ed25519-signature-verified against the key baked into the binary;
every download must match its SHA-256 from the signed manifest; official Windows binaries are
Authenticode-signed. Any failed check discards the download - unverified builds are never
installed. Install swaps only the exe (a `.old` rollback copy is kept until the next good
start); helper DLLs ship as manifest assets or self-heal from the same Settings page.

## Config & data

Config lives in your OS user-config dir (`rave-mate/config.json`, versioned + migrated
automatically). Sealed secrets (`*.bin`) sit next to it and are machine-bound - they don't
survive copying to another PC (sign in again there).

# ravemidi — rave-mate virtual MIDI kernel driver

Windows WDM/PortCls adapter exposing dynamically created virtual MIDI ports to classic
winmm apps (Rekordbox/Traktor/Serato). Key feature: **one-way ports** — apps see an
input-only MIDI port, so DJ software cannot echo LED feedback back into itself.
To our knowledge the first open-source kernel virtual-MIDI-port driver.

Design + research notes: `../../.devnotes/RAVEMIDI_DRIVER_DESIGN.md`.
rave-mate integration: `internal/midi/ravemidi_windows.go` (prefers ravemidi, falls back
to teVirtualMIDI when the driver isn't installed).

## Status

Virtual-port core + mirror-tap + managed-input autonomy implemented; builds clean
(Release/Debug x64). KS streaming paths need on-hardware bring-up (test-signed load)
before they're trusted — see design doc.

## Managed inputs (driver autonomy)

The driver forwards controller MIDI **autonomously** — rave-mate is only a config
manager. `IOCTL_RAVEMIDI_SET_CONFIG` validates + persists a `RAVEMIDI_CONFIG` blob
(REG_BINARY `Config` under the service `Parameters` key, written kernel-side — no
registry rights needed in userland) and applies it live. The persisted config is
re-applied at every `StartDevice`, so forwarding returns after reboot with
rave-mate never launched. `GET_CONFIG` returns the persisted blob, `QUERY_INPUT`
live bind status per input, `RELOAD_CONFIG` re-reads the blob.

Per managed input the driver creates driver-owned ports (no owner file object —
handle close never tears them down):

- one reserved **INTERNAL** port `"<Name> (rave-mate)"` (protocol v3) — no
  winmm/KS presence, so it never appears in any app's MIDI list; rave-mate reads
  it via pended `IOCTL_RAVEMIDI_READ` (reconnects seamlessly after relaunch) and
  with `Feedback=1` its `IOCTL_RAVEMIDI_WRITE`s are teed to the hardware device's
  render pin (LED feedback). DJ software only ever sees the fan-outs below
- `OutCount` extra **BIDI** fan-out ports with the configured names (empty name →
  `"<Name> Out N"`); with `Thru=1` device capture fans into them, and with
  `Feedback=1` the DJ software's writes on them are message-framed and teed to
  the device render pin (LED feedback) — loop-free: a BIDI port has no internal
  render→capture path, so an app never re-hears its own output
- `Filter` (protocol v2) drops message classes (aftertouch / poly pressure /
  pitch bend / active sensing / clock) on the fan-out path only; the reserved
  port always carries the full stream. Fixes DJ-software MIDI-learn latching
  onto keybed aftertouch ("every key triggers the binding")
- `IOCTL_RAVEMIDI_QUERY_TRACE` snapshots a per-port ring of the last 128 data
  events (tap-raw / to-app / read-pop / from-app / feedback-out / loop-drop)
  for live wire diagnosis from rave-mate; LOOPBACK ports suppress self-echo by
  owning-process identity (an app holding both ends never hears itself)
- the tap defends against **replaying capture pins** (seen on NI Komplete Kontrol
  A61): pins whose record re-delivers the full history + new bytes on every
  completion are strict-prefix-deduped (only the tail is forwarded), and the pin
  is PAUSE→RUN-cycled before its frame saturates (OnDead rebind as fallback) —
  downstream apps always receive each message exactly once

Binding: a passive worker + PnP interface-change notification (KSCATEGORY_CAPTURE,
existing interfaces included). Source = exact `SourceIface` symlink, else the first
KSCATEGORY_CAPTURE interface whose FriendlyName contains `SourceMatch`
(case-insensitive; the driver's own ports are excluded). Open failures (device
absent/busy) retry with capped backoff: 1s, 2s, 5s, then every 10s forever
(`RetryCount` counts attempts since last success). Tap read failure / device
removal → clean teardown, rebind loop resumes. Feedback render pin failing to
open leaves the input Bound with `FeedbackBound=0`, still retrying.

Port ids in `QUERY_INPUT` are `0` while port creation is pending (e.g.
`RAVEMIDI_MAX_PORTS` exhausted — creation retries on the same backoff). A managed
port removed from config while a DJ app still holds its pin stays alive until the
pin closes, then is reaped.

Legacy IOCTL ports/mirrors are unchanged: still owned by the creating handle and
cleaned up when it closes.

## Build

Requires VS Build Tools (VCTools + Spectre-mitigated libs) + WDK ≥ 10.0.26100
(installed WDK, or NuGet packages `Microsoft.Windows.SDK.CPP{,.x64}` +
`Microsoft.Windows.WDK.x64` pinned to one version). GitHub CI: `windows-2022` runner
ships a WDK.

```
msbuild ravemidi.vcxproj /p:Configuration=Release /p:Platform=x64
```

## Dev install (test machine / VM only)

Secure Boot OFF + `bcdedit /set testsigning on` + reboot; self-signed cert in
LocalMachine Root + TrustedPublisher; `Inf2Cat` + `signtool sign /fd sha256`; then:

```
pnputil /add-driver ravemidi.inf /install
devgen /add /bus ROOT /hardwareid Root\ravemidi
```

## Release signing / distribution status

**Current reality: test-signing only.** No Microsoft-trusted release exists yet — installing
ravemidi today requires a test-signed dev setup (Secure Boot off + testsigning on, above).
Attestation prerequisites (Partner Center account + EV cert) are pending; see
`../../.devnotes/DRIVER_TRUST_PLAN.md` for the decision doc + user action list.

Planned pipeline (tooling already in-repo):

1. CI `attestation-package` job (`.github/workflows/driver.yml`, manual dispatch) → artifact
   `ravemidi-attestation-cab-unsigned` (`disk1/ravemidi.cab` = inf+sys+pdb).
2. EV-sign the cab locally: `build/sign-cab.ps1` (CI never holds the token).
3. Submit to Partner Center for **attestation** (checklist + API skeleton in `build/attest/`)
   → Microsoft embeds its signature in the .sys + returns an MS-signed .cat — that MS
   signature is what Secure Boot systems load; plain EV Authenticode on the .sys will NOT.

Attestation covers Win10/11 client only: no Windows Server (≥2016 rejects attested
drivers), no Windows Update retail publishing, not "Windows Certified" — WHCP/HLK would be
needed for those, and is deliberately deferred.

## License

AGPL-3.0-or-later (rave-mate). Note: forks that modify the driver cannot reuse our
Microsoft signature — re-sign under your own Partner Center account or run test-signed.

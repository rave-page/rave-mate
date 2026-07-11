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

- one reserved **BIDI** port `"<Name> (rave-mate)"` — rave-mate reconnects here
  seamlessly after relaunch; with `Feedback=1` its app-bound writes are teed to
  the hardware device's render pin (LED feedback) while staying readable via
  `IOCTL_RAVEMIDI_READ`
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

## Release signing

Microsoft attestation via Partner Center (EV cert required for account registration;
plain Authenticode on the .sys will NOT load on Secure Boot systems). Windows Server is
not supported (attestation excludes it). See the design doc for the full pipeline.

## License

AGPL-3.0-or-later (rave-mate). Note: forks that modify the driver cannot reuse our
Microsoft signature — re-sign under your own Partner Center account or run test-signed.

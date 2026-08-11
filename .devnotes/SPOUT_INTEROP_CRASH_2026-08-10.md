# Spout interop crash - root cause + fix (2026-08-10/11)

## Incident

Main daemon (`rave-mate.exe`, NOT a child) died 3× with `0xc0000409` in `SpoutLibrary.dll`:
2026-08-04 22:51 + 22:59, 2026-08-10 22:41:04. Dumps byte-identical:
`SpoutLibrary!GetSpout+0x2743c`, subcode 5 = `FAST_FAIL_INVALID_ARG`. On 08-10 the death
tore down every Spout sender mid-frame; Resolume (consuming them) hit its own
"failed to create OpenGL and DirectX interop" dialog + wedged as a zombie holding the
single-instance lock. Parsec virtual display was investigated and EXONERATED
(`host_virtual_monitor_fallback:false`, no virtual monitor ever created, no TDR, no reboot).

## Root cause (from dump disassembly)

The in-flight message on the crashed stack:

```
spoutGL::LinkGLDXtextures : wglDXOpenDeviceNV(0x0001760) no Interop device - error 3221684274 (0x32).    The dxDevice is not supported.
```

1. **Primary event:** `wglDXOpenDeviceNV` failed, Win32 error 50 (`ERROR_NOT_SUPPORTED`) -
   GL/DX interop refused system-wide (hit Resolume too). Trigger at 22:41:04 = a deck sender
   worker (re)creating its interop; dims 2160×1056 ("RaveMate Deck A") in the crash frames.
   Suspected driver-side interop-resource degradation after hours of per-track worker churn
   (each on-air cycle creates GL context + interop device); unproven.
2. **Fatal wrapper bug (shipped SpoutLibrary.dll 2.007.017, ts 0x68f979e2):**
   `LinkGLDXtextures`'s error path `sprintf_s`'s ~103 chars into `char[128]` then
   `strcat_s`'s ~34 more → static-CRT parameter validation → `__fastfail` (`int 29h`).
   Uncatchable by SEH/VEH; whole process dies. The videoshare sink runs in the MAIN daemon
   (media-plane isolation is the separate tracked P0), so the entire app went down.

## Fix (spout_shim.cpp, commit on `development`)

- `interop_probe()`: replicate the ONE fatal driver call survivably - resolve
  `wglDXOpenDeviceNV`/`wglDXCloseDeviceNV` off the current GL context, create a probe D3D11
  device (same HARDWARE+BGRA shape Spout uses), open+close an interop device, report the
  Win32/HRESULT reason. `RAVE_SPOUT_FORCE_NO_INTEROP=1` forces failure (read via
  `GetEnvironmentVariableA` - MinGW `getenv` snapshots environ at startup and misses Go
  `os.Setenv`).
- Gate `rave_spout_create`: probe fails → release GL context, return NULL,
  `rave_spout_last_interop_error()` carries the reason; deck/frame workers take the existing
  idle-drain path with a distinct Warn (`winErr`). Retry = next worker create (track cycle).
- Relink guards: `rave_spout_send` re-probes when a live handle re-registers under a NEW name
  or changes geometry (both re-enter `LinkGLDXtextures` long after create);
  `rave_spout_open_sender` same when `sent_once`. Refused send returns 0 / -1 - frozen card
  beats a dead process.
- `rave_spout_release` now resets the per-thread send latches (`sent_name/once/w/h`): Go
  reuses OS threads, and a stale latch let a future same-name worker skip the registration
  lock (scan race) + the relink probe.
- Proof: `interop_gate_spout_test.go` - forced-failure degrade (no registration, non-blocking
  Send, process survives) + recovery on a fresh worker. Live-run green on the dev rig
  (needs `SpoutLibrary.dll` on PATH: `PATH="$PATH:.../Programs/rave-mate"`).

## Follow-ups (not done)

- Rebuild/vendor a patched `SpoutLibrary.dll` (bigger buffer or upstream fix) + symbols -
  the complete fix; the probe is best-effort (a driver flipping between probe and Spout's own
  link still dies).
- Media-plane isolation P0 would turn any residual Spout death into a child respawn.
- Reduce interop churn: keep deck workers alive across off-air instead of Remove→recreate
  (suspected driver-degradation trigger).
- Resolume-side note: after such an event Resolume wedges as a zombie → "another instance is
  already running" on relaunch; kill `Arena.exe` first.

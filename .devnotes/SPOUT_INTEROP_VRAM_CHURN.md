# Spout interop VRAM churn - the CRASH TRIGGER (2026-08-29/30)

Companion to `SPOUT_INTEROP_CRASH_2026-08-10.md`. That note + the 2026-08-11 pre-flight
(`7a067d1`) make the crash SURVIVABLE; this note removes the TRIGGER.

## Symptom

~50 min into recorded DJ sets, Resolume Arena throws "Cannot create DirectX/OpenGL interop"
and wedges. rave-mate's own probe refuses interop at the same time - `rave-mate-debug.log`
WARN with `winErr 0x8876017C` = `D3DERR_OUTOFVIDEOMEMORY`:

- 2026-08-29 00:51
- 2026-08-30 02:46

Each set had accumulated ~25 deck sender destroy/create cycles by then.

## Mechanism

`Sink.publish` tore down a deck's Spout sender every time the deck went off-air (track
ended/swapped) and re-created it when the deck returned. Per cycle, in the daemon:

- 1× OpenGL context create/destroy
- 2× `wglDXOpenDeviceNV` open/close (interop pre-flight probe + Spout's own link)
- 1× D3D11 `MISC_SHARED` texture create/destroy

...and every receiver (Resolume, OBS) is forced to destroy + re-create its own GL/DX interop
registration to follow the sender. Driver VRAM/interop degradation from this is CUMULATIVE and
SYSTEM-WIDE - after ~25 cycles the driver fails interop creation with `E_OUTOFVIDEOMEMORY`,
killing both our sender and Resolume.

## Fix

Keep deck senders alive across off-air. On gate-out, publish ONE fully transparent frame
(all-zero NRGBA, sized exactly to the card - a size mismatch would itself re-link interop) and
latch it, instead of calling `Sender.Remove`. Alpha-aware receivers show nothing - visually
identical to today. Senders now churn at most 1× per deck per session (create on first on-air,
destroy only at `Close`/session exit). `internal/videoshare/videoshare.go` + `videoshare_test.go`.

`sender_spout.go` also now decodes the two known interop `winErr`s to an `errName` log hint
(`0x8876017C`=OUTOFVIDEOMEMORY, `50`=NOT_SUPPORTED) and logs `winErr` in hex.

## Watch on the next live set

- No interop WARNs in `rave-mate-debug.log` for the whole set.
- Task Manager per-process GPU memory for `Arena.exe` + `rave-mate.exe` stays FLAT across
  track changes (no sawtooth climbing to exhaustion).

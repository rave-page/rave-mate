# ravemidi crash #4: usbaudio frame overflow (BSOD 0x50, 2026-07-13 22:57)

Dump `071326-9546-01.dmp`: 0x50 PAGE_FAULT_IN_NONPAGED_AREA, write to `0xffffba80bce15000`
(page-aligned), faulting IP `usbaudio!memcpy+54`, PROCESS System. Driver 5.0.0.1 (UAF fix)
WAS live — different bug, third in the tap path.

## Root cause

usbaudio.sys copies a capture pin's KSMUSICFORMAT record into the client frame
**unclamped by FrameExtent**. Combined with the NI A61 replaying-pin quirk (record only
grows until a PAUSE→RUN reset; see RAVEMIDI_UAF_CRASH_071326.md + 4955497):

1. Tap frame was `MIRROR_READ_BUF = 1024`; highwater reset at 952.
2. Pin goes quiet → tap enters the 5.0.0.1 zero-read 5ms backoff → **no read pended**.
3. User turns a knob → CC burst accumulates in the device record while we sleep.
4. Record jumps from <952 to >1024 in one interval; next read completes with usbaudio
   memcpy'ing the FULL record into the 1KB pool block → runs off the pool page → 0x50.

Explains "crashed as soon as I moved a knob": idle backoff + burst is exactly the window.
Also disproves the old assumption that a saturated frame merely "stops completing" — it
overflows.

## Fix (mirror.cpp, 5.0.0.2)

- `MIRROR_READ_BUF` 1024 → **65536** (alloc == FrameExtent). 60KB slack over the reset
  threshold ≫ anything full-speed USB-MIDI queues between reads (~few KB per 5ms max).
- `MIRROR_REC_HIGHWATER` decoupled from the frame: **4096** — record reset while tiny
  relative to the extent.
- Truncated/oversized record in the parse loop now also forces the PAUSE→RUN cycle
  (previously could break out without cycling → unbounded growth if a record ever
  outran a completion).
- `LastRec` sized to the frame (RAVE_TAP ≈ 64KB nonpaged, one per tap — fine).

## Ops

Built locally (WDK toolset shim recipe, memory `ravemidi_kernel_driver_epic`), signed
`CN=ravemidi test signing`, staged build/testsign, DriverVer 07/13/2026,5.0.0.2.
Install = step4-update-driver.ps1 elevated (stops midisrv around devnode restart).

Verify: knob-sweep the A61 with the tap active + Rekordbox/Serato attached; idle a few
seconds, then hard knob sweep (reproduces the exact window). Watch QUERY_TRACE for clean
tails; no BSOD = fix holds.

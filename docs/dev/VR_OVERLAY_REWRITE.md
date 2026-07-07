# VR overlay pointer + edit - clean-slate rewrite (design)

Escapes the accreted 5-layer patch stack in `pointer.go` / `pointer_smooth.go` / `editor.go` that
fought - and never fully fixed - three symptoms: **pointer↔click discrepancy**, **disappearing
menu**, **half-baked edit mode**. Same behaviors, one coherent pipeline, root-cause fixes instead
of band-aids. Nothing here changes the cgo shim (`openvr.go`) except small additive helpers.

## Hard constraints (non-negotiable - the rewrite fails if any breaks)

1. **Runs only in the `feature vr` subprocess.** All OpenVR/pointer/edit code stays behind
   `//go:build vr` in `internal/vroverlay/`, driven by `featurehost` (`feat_vr.go` → `Manager`).
   The daemon (`app/vrsurface.go`) only proxies. A cgo/OpenVR crash kills the child, not rave-mate;
   the host restarts it and re-pushes full config. **State must stay idempotently re-pushable** so a
   respawn rebuilds every overlay cleanly (see `resetSession`).
2. **Never flip `MakeOverlaysInteractiveIfVisible`.** The custom ray is the SOLE cursor. SteamVR's
   native laser globally captures the controller from the running game (VRChat) and suppresses our
   IVRInput binds. Own intersection, own click action, coexists with the game.
3. **90 Hz, zero per-frame alloc**, single VR goroutine (`runConnected` `select` loop) - all OpenVR
   access single-threaded; off-thread requests marshalled via the pend* channels.
4. **Preserve every documented behavior below.** They are live-verified fixes, not incidental.

## Preserved-behavior checklist (each MUST survive the rewrite)

Pointer:
- [ ] **Active-hand model**: the hand that pulls the trigger becomes the sole pointer hand; the hand
      holding the menu and the hand wearing the badge are excluded (else pointer goes dead / jitters
      between hands). Falls back to the free/tracked hand.
- [ ] **Tip pose, not raw device −Z**: prefer the IVRInput `/pose/tip` aim action; learn the
      device→tip correction while live; reconstruct tip from the correction if the aim goes inactive;
      raw device pose is last-resort only (+ warn to reset binding).
- [ ] **UV from the runtime, not ray reconstruction**: derive the cursor world point from the hit UV
      via `GetTransformForOverlayCoordinates` - the UV drives rows, so the dot must sit where the
      runtime says that UV is. Reject degenerate edge-clamped hits (UV at 0/1 edge + >30 mm ray
      divergence).
- [ ] **v is bottom-origin** (`ComputeOverlayIntersection` returns GL vUVs) → `(1-v)` flip to
      texture top-Y. Getting this wrong inverts hover.
- [ ] **Rest-jitter kill without lag**: filter the pointer so it's steady at rest but doesn't trail a
      deliberate sweep (the current 1€ on the hit UV, minCutoff 2.5 / beta 1.0). Row hysteresis (¼-row
      margin) on top.
- [ ] **Near-field behaves**: at point-blank (holding a hand-attached menu + poking with the other),
      tiny angular tremor must NOT sweep whole rows - drive by tip POSITION, not aim angle.

Lifecycle (disappearing menu):
- [ ] **Stable handles** - never Destroy+Create to move/re-anchor (only for a raw-texture dimension
      change or upload self-heal, and then via generation-suffixed keys to dodge SteamVR's async
      `KeyInUse` release race).
- [ ] **Re-assert transform when a pose recovers** - a transform applied while its snap anchor
      (controller) is untracked falls back to playspace origin; must re-apply once the hand re-tracks
      (today's `needsApply`).

Edit:
- [ ] Move / rotate / tilt / depth / scale / reset, with a deliberate entry gate + haptics, and a
      stick-only opt-out (`StickMoveOnly`).

## New architecture

### Pointer: one pipeline, no mode discontinuity

`pointerpipe.go` - a single per-frame function producing one `PointerState{ hand, key, uv, worldPt,
row, element, dist, mode }`. Stages, in order, each pure/testable:

1. **selectHand** - active-hand resolution (checklist #1). Isolated, unit-tested with fake tracking.
2. **ray** - `tipRay(hand)`: tip pose (checklist #2) → `{origin, dir}`. Pose-level 1€ filter here
   (filter the RAY, not the downstream UV) so every consumer sees one stable ray and near/far share
   it - removes the touch-vs-ray *feel* discontinuity.
3. **intersect** - `ComputeOverlayIntersection` across candidates → nearest valid (uv∈[0,1], dist≤max).
   Near-field: instead of a hard 0.28 m switch to a different projection, **continuously blend** the
   aim-ray hit with the tip-position-projected hit by proximity (weight 0 at ≥touchFar, 1 at ≤touchNear),
   so there's no snap as the hand approaches. Keeps the "position not angle at point-blank" property
   without a discontinuity.
4. **resolveUV** - runtime UV→world (checklist #3) + degenerate rejection + `(1-v)` row map with
   hysteresis.
5. **emit** - cursor placement, hover, click/drag edge. Cursor/hover/click all consume the SAME
   `PointerState` (they cannot disagree by construction).

State lives in ONE `pointerState` struct (filters, hysteresis row, active hand, tip corrections) -
not scattered editor fields - reset atomically on hand/target switch or long gap.

### Lifecycle: an explicit overlay owner

`overlayowner.go` - each managed overlay is an `ovl{ key, desiredTf, desiredVis, appliedTf,
appliedVis, gen }`. One `reconcile()` per frame: create-if-missing (stable key), re-assert
transform/visibility whenever desired≠applied **OR the anchor pose recovered**, recreate only on
dimension-change/self-heal (gen-suffixed). This replaces the scattered `cachedTf`/`needsApply`/
`lastTf` compares with one owner that structurally cannot "cache a stale hidden state".

### Edit: grab-first, menu-fine

`editmove.go` (extracted out of `editor.go`) - primary interaction is the **grab-offset** model:
on grab `offset = anchorPose⁻¹ · overlayTf`; per frame `overlayTf = anchorPose · offset` (move +
rotate + tilt in one). We already parent to the controller (device-relative transform) which is
equivalent - formalize it and add scale (stick/scroll → width, clamp) + distance (offset.z). The
positioning menu stays for **discrete fine nudges** (0.05 m / 5°). Thumbstick free-nudge is kept
only under `StickMoveOnly`. Deliberate entry gate + haptics unchanged.

## File plan

- New: `pointerpipe.go` (pipeline), `overlayowner.go` (lifecycle), `editmove.go` (edit).
- Shrink: `editor.go` → just the editor struct + `handleActions` call-ordering + menu build. Pointer
  and edit logic move out (the map flagged `editor.go` as the merge-conflict hot spot - this
  separation is also what lets pointer/edit/lifecycle be worked independently).
- Keep: `openvr.go` (cgo), `mat.go`, `render.go`, `strip.go`, `worldlayout.go`, `manager.go`.
- Retire: `pointer.go`, `pointer_smooth.go` (logic absorbed into `pointerpipe.go` + a `filter.go`).

## Verification (W4 - cannot be parallelized: one Index)

Build+vet+lint (incl `-tags "spout vr"`) + unit tests for the pure stages (selectHand, resolveUV,
hysteresis, grab math) → deploy via the VR loop (push → CI → `ctl remote-update` the VR instance) →
in-headset: pointer lands where aimed at arm's length AND point-blank, hovered row = clicked row,
menu never vanishes across hand-untrack/dashboard/ respawn, edit move/rotate/tilt/scale feels solid.
Sanity-check neighbouring surfaces (wrist badge open/close, content select, VRChat coexistence).

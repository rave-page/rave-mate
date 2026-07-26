# B7 increment (ii): retained-doc delta channel — design (pre-code review gate)

Status: **SHIPPED** (this file is the pre-code design; the AS-BUILT record is
ZIG_UI_GUIDE.md "Phase B — B7 (ii) retained-doc delta channel" + PHASEB_BASELINE.md
"Phase B7 (ii)"). What the implementation changed against this document:

- The provisional opt-in set in §6 was PROVISIONAL and mostly wrong. Per-surface benches
  shipped exactly ONE surface enabled (`#ce-topbar`). The Live tick reads -5.3% on a
  hand-picked step and **+33.0% on the churn the running app actually produces** (measured:
  delta 7.4 kB vs full 11.0 kB over 82 live tick states) because its graphs are pre-rendered
  strings that one sample replaces whole. twitch feed / midi monitor / midi ctlstat /
  `#log-view` all regress too. Their exports + walkers stay for the gates + the bench.
- "Absent = keep" needs a companion: a reserved **clear tag** (field 1023) for a field
  falling back to its zero value. §4 did not name it.
- The delta key is NOT the B-3 per-message hash. Both sides generate a **full-state
  fingerprint walk** and Zig re-fingerprints what it merged against the value Go computed, so
  a codec divergence declines instead of drifting. §3's baseHash is that value.
- Cap breach is its own STATUS and its own counter with 3-strike sticky hysteresis (§1 only
  said "decline"), and the merge clones into a scratch arena because retained strings must
  outlive the document (§4's "no zero-init" understated it).

## What

Today every patch send re-encodes + re-decodes the FULL RZW1 document, even at ~1 Hz
(tick family, midi monitor, ctlstat) and per-event (twitch feed) cadence. (ii) adds an
opt-in **retained-doc channel**: Zig keeps the last-decoded state per patch target; Go
sends only changed field trees (delta doc); Zig merges + re-renders. Full-tab renders
stay stateless full-doc — the retained channel exists ONLY for high-cadence fragment
patch sites.

## Why it's allowed to reverse B3 (scoped, not deleted)

B3 decided: every `rz_ui_render_*` export is a pure fn(state)→html, zero cross-call
state. That stays TRUE for all existing exports. The reversal is scoped to a new,
parallel export family (`rz_ui_patch_*`) that is explicitly stateful, opt-in per
surface, and self-healing back to the stateless path on any doubt. Stateless remains
the default and the fallback; retained is an optimization tier.

## Design

### 1. Handle-per-UI lifetime
- Retained state keyed by explicit handle: `rz_ui_retain_new(root_msg_id) -> u64`,
  freed by `rz_ui_retain_free(h)`. One handle per (UI instance × patch target).
  No globals/singletons — app UI + parallel tests never alias.
- Handle = packed {index:32, gen:32}. Slot reuse bumps gen; a freed/stale handle is
  detected (gen mismatch) → decline, never UB.
- Go owner: the `*UI` holds its handles; `UI` teardown frees all. Zig side caps slot
  count (fixed table, e.g. 64) — allocation past cap → decline → caller stays on the
  stateless path (bounded by design, no growth with traffic).
- Memory: each slot owns ONE arena holding the retained doc + decoded state; freed
  wholesale on drop. Per-slot byte cap (same over-size ceiling as the v1 bridge);
  breach → drop slot → full-doc resend. Explicit cap + drop policy per repo rule.

### 2. Drop-on-patchMain
- Any full main render (tab switch, patchMain, renderer restart, webview reload)
  drops ALL retained handles of that UI; next fragment send is a full doc that
  re-seeds the slot. Gives hard resync points; drift cannot outlive a tab switch.
- Zig-side `rz_ui_retain_drop_all()` is NOT provided — drops are per-handle from the
  Go owner, so a rogue caller can't nuke another UI's slots.

### 3. Generation guard (wire-level)
- Delta doc header: `RZW1` magic + schemaHash (unchanged) + {handle, slotGen,
  baseHash}. Zig verifies: handle live, gen matches, baseHash == hash of currently
  retained doc. Any mismatch → NULL → Go zigWire falls back to full doc + reseed
  (exact same fallback discipline as v1→v2; FallbackCounts gets per-export patch
  counters so gates can assert exact deltas).
- Locale is folded in: retained slots record the i18n generation at seed time;
  locale change bumps it → all merges decline → full-doc reseed. This is the (iii)
  i18n-handshake dependency and must land with (ii)'s guard, not after.

### 4. B-3 hash discipline as the delta key
- The B-3 per-message content hash becomes the change detector: Go keeps the
  last-sent doc per handle, walks new state vs prior hashes, emits only field trees
  whose subtree hash changed. baseHash = full-doc hash of the prior send, so both
  sides agree on the merge base byte-exactly.
- Encoder: generated `deltaX(v, prev *X, w)` beside `wireX` — same field tags, a
  skipped field means "retain prior value". Absent-vs-zero therefore changes meaning
  on this channel ONLY (wire absent = keep, not zero) — this is why deltas must
  never travel to a stateless export; root-msg-id + header shape make the two doc
  kinds mutually unparseable.
- Decoder: generated merge-decode into the retained state (no zero-init). Lists
  replace wholesale when their subtree hash changed (no per-element diff in v1 of
  this design — measure first; per-element splicing is a later increment if rows
  dominate).

### 5. Failure + determinism semantics
- Any decode/merge error → drop slot + decline → caller reseeds. No partial merges:
  merge into a scratch copy, swap on success (patch-then-swap, same discipline as
  the lost-patch render race fix).
- Determinism: html(render(retained ⊕ delta)) must equal html(render(full state)).
  Gates: sequence goldens — apply N random fixture-to-fixture deltas, assert
  byte-equality with the stateless render of the final state at EVERY step, plus
  decline paths asserted with exact fallback deltas.
- Fuzz: new mode over (seed doc, delta*) sequences — mutated deltas onto live
  retained state; assert no crash, bounded memory, deterministic output, and that
  any accepted merge equals some full-doc render (oracle = Go renderer).

### 6. Where it pays (initial opt-in set)
tick family (#tk-live 1 Hz), twitch feed (per chat event), midi monitor rows +
ctlstat (~1 Hz), cue-edit topbar (drag). Everything else stays stateless — measured
first via the PHASEB_BASELINE table before widening.

## Non-goals
- No per-element list diffing (v1 replaces changed lists wholesale).
- No retained channel for full-tab renders.
- No cross-process/persisted retention — process-lifetime only.

## Review asks
1. Accept scoping (stateless default + opt-in patch channel) as B3-compatible?
2. Slot cap 64 / per-slot byte cap = v1 over-size ceiling — right numbers?
3. Locale-generation fold-in with (ii) (making (iii) partially co-dependent) — ok?
4. List wholesale-replace acceptable for v1 of the channel?

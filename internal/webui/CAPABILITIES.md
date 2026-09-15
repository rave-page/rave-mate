# webui UI capability registry

**One capability, one component.** Each UI job (pick an option, show a status,
browse for a file, render an empty list, …) has exactly ONE canonical recipe +
Go helper in this package. A sibling that re-rolls the same job is a defect — grep
the canonical first and extend it. Rules: `docs/dev/DESIGN.md`; research:
`docs/dev/PRINCIPLES.md`.

Discovery before building/fixing a capability: read this table, run
`./harness.ps1 context "<capability words>"`, grep synonyms
(`Select|Chip|Badge|Card|Field|Empty|Browse|Picker|Btn|Toast`). Fix the canonical
and every surface inherits it — discovery is per capability, not per route.

The "component" here is a **class recipe** in `assets/ds/styles.css` (`.rp-*`) plus
the Go helper that emits it. Tokens live in `assets/ds/colors_and_type.css`.

| Capability | Canonical recipe | Canonical Go helper | Notes |
|---|---|---|---|
| Button | `.rp-btn` (+ `--primary`/`--go`/`--outline`/`--ghost`/`--warn`/`--destructive`, `--sm`/`--lg`/`--icon`) | `uiBtn{}.html()`, `btnRow(...)` | ONE filled primary per surface (P16); rest outline/ghost; destructive never beside primary |
| Card | `.rp-card` (+ `__head`/`__title`/`__sub`/`__foot`, `--glow`) | inline in `render_*.go` over the recipe | identity + state + action on one card (P8); `__foot` pinned bottom |
| Disclosure (collapsible group) | `.rp-disclosure` (+ `__sum`) | inline `<details class=rp-disclosure><summary class="rp-disclosure__sum sec-title">` over the recipe | keeps a low-priority chunk out of the first glance (P1/P2); semantic `<details>`, keyboard- + `ctl`-reachable (Live SYSTEM chunk) |
| Select (non-native) | select recipe | `resolveSelectBox`/`selHTML` (`smartselect.go`), `resolveSelectBoxTip` (`components.go`, w/ `?` tip) | replaces `<select>`; never a native dropdown |
| Action menu | `.amenu` | `resolveActionMenu` (`actionmenu.go`) | > 4 actions collapse here (P16) |
| Chip (control) | `.rp-chip` (+ `--active`) | inline over the recipe | filters/toggles/tiers/sub-nav ONLY — a control, never a label |
| Badge / status (label) | `.rp-badge` (+ `--success`/`--warning`/`--error`/`--info`/`--secondary`/`--outline`, `__dot`) | inline over the recipe | state/label ONLY — no control signifier, no per-call colour override |
| Switch / toggle | `.rp-switch` (+ `__track`/`__thumb`/`__label`/`__desc`) | inline over the recipe | label + description in the copy block |
| Search field | `.rp-field` | per-surface input + debounced action (`settingsSearchInput`, `logSetSearch`, `wsGroupSearch`) | the recipe is canonical; wire the input to a `search:`-style act |
| Empty state | `.rp-empty` (+ `__icon`/`__title`/`__desc`) | `emptyState(msg)` (`components.go`) | every list gets one — what's missing + next step (P2), never a bare blank |
| Stat / metric | `.rp-stat` / `.rp-stats-grid` | inline over the recipe | a counter only where the number IS the answer (P7); else draw a shape |
| File / dir browser | `common.browse` ghost `.rp-btn` + path field | `pick-dir:<target>` / `pick-file:<target>` acts → `internal/localmedia` | the in-app browser (NOT a native OS dialog); reused by automations, run-now, video editor — reuse it, never re-roll |
| Toast / notify | in-page toast recipe / OS notify | `u.toast(msg)`, `u.Notify(title, body)` (`ui.go`) | in-page for transient; `Notify` for OS-level |
| Avatar | `.rp-avatar` / `.rp-avatars` | inline over the recipe | circular, soft border |

## Extend, don't fork

1. A new variant of an existing capability → add a modifier to the `.rp-*` recipe
   (+ a helper param), never a new sibling recipe or an inline-styled one-off.
2. A genuinely new capability → add the recipe + helper here, then this row.
3. Keep the Zig render layer in step: the recipe is the contract both the Go
   renderer and `native/zigui` render to; the `internal/webui` golden test pins
   them byte-for-byte.

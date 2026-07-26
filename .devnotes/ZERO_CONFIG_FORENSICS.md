# Zero-config data loss (2026-07-26) — forensics + trap

P0: config.json repeatedly overwritten with a marshaled **zero-value Config** (version 0,
all features off). Guard shipped in `internal/config` (`ErrZeroConfig` + load quarantine).
Root writer NOT yet named — the tripwire will (see below).

## Proven facts

- Artifact bytes = `json.MarshalIndent(config.Config{Features:{AudioRecord:{Format:"wav"}}})`,
  **current schema, 5975 B** — byte-identical across the 05:16 and 13:53 preserved copies and
  a probe marshal from development tip. Writer = a current-schema binary marshaling a
  **never-Loaded** zero Config (version 0, `updateNotifiedFor:""`, `midiController.channels:0`
  ⇒ never passed Load/migrate/normalize; `format:"wav"` = the user's setting, the ONLY field set).
- Debug-log boot fingerprints (module lists) show config.json was ALREADY zero at the
  03:43:52 / 06:04:54 / 12:21:09 / 13:32:52 boots — only always-on modules (automation,
  obscontrol, vrmonitor, session) started. The briefing's "boot loaded config fine at 13:32:52"
  was wrong; `ctl status features=[]` at 13:3x/13:44 = the daemon booted that way
  (appControl.Status reads live `c.cfg` — no mid-session memory zeroing needed or evidenced).
- Good→zero flips bracket **update-install→relaunch windows**: 12:41 full → installed
  13:32:39-50 → 13:32:52 boot zero. Same 05:16:33 full → upd 06:04:37-53 → 06:04:54 zero.
  The 04:55 update did NOT zero. Cadenced re-writes (~2.5-5 min apart: 05:09/05:12,
  14:24/14:28) while a good-config daemon idled with NO acts logged (debug act mirror on).
- Amplification chain (fixed): zero file is valid JSON → old Load() accepted it → migrate
  bumped version to 35 in memory → any save (e.g. updater SetNotified) persisted
  v35-zeros (= `config.json.postcrash-20260726` shape: v35 + notified + channels 4).

## Killed hypotheses

- updater SetNotified as writer: would stamp `updateNotifiedFor` — artifact has `""`.
- webview child (`feature webview`): pure window host, constructs no UI/updater, no config
  read/write (shell_proc_child.go).
- headless remote-UI sessions: `newHeadlessUI(h.u.svc,…)` shares the primary's real Cfg
  pointer; never runs initUpdater.
- cgo/Zig memory corruption: cannot zero fields both before AND after a surviving
  mid-struct string, twice, across two different binaries — the artifact is semantic.
- No `config.Config{}` literal, no reload-into-live-cfg, no reflection/unsafe writer, no
  second Load() call site, no cfg shadowing, no peer/remotectl/studio config surface, no
  bundle-deployed config.json. All 10 Save() call sites go through run()'s `&cfg` or
  `svc.Cfg` (same object).

## The trap (shipped)

`Save()` on a Version==0 receiver refuses (`ErrZeroConfig`) and writes the caller's full
goroutine stack to `<configdir>/zero-config-save-<unix>.stack`. **Next fire names the
writer with file:line — check for these files when triaging.** `Load()` quarantines zero
artifacts as `config.json.zero-<ts>` and recovers from `.bak` (legacy flat v0 files still
migrate; a zero `.bak` is refused too).

## Open

- Name the writer from the first `.stack` file, then root-fix and drop this note's Open item.
- Suspect space left: something alive in the dying daemon's teardown / update overlap
  holding a fresh zero Config with one settings write applied. Cadence ~2.5-5 min.

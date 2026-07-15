# MIDI mapping - rave-mate DJ-data source

rave-mate reads MIDI **input** and decodes it into session fields. Two decoders run over
the configured port(s):

- **Custom map** (`midi.custom`) - our own Traktor mapping. Deterministic, what we ship.
- **Denon HC4500 stock map** (`midi.denon`) - Traktor's built-in mapping reused to stream
  deck A/B track text. Best-effort (see caveat).

Source: `internal/session/sources/midisrc/`. Driver: `internal/midi/` (Windows = `winmm.dll`
via stdlib `syscall`, **no third-party dependency**; other OSes report unsupported).

## Virtual MIDI port drivers (your choice)

rave-mate never bundles a driver - pick whichever virtual-MIDI backend you prefer. The MIDI tab
links all three:

| Driver | Ports | License | Notes |
|---|---|---|---|
| [loopMIDI](https://www.tobias-erichsen.de/software/loopmidi.html) | unlimited, named | freeware | **Recommended.** No admin, no registry, works on every Windows. Create as many named cables as you need. |
| [LoopBe1](https://www.nerds.de/en/loopbe1.html) | 1 (LoopBe30 = 30) | freeware | Single port; fine for a one-app map, but you'll need a second port to read a controller AND feed a DJ app. |
| [Windows MIDI Services](https://microsoft.github.io/MIDI/) | loopback A/B + **multi-client** | **open source (MIT)** | The future built-in stack. Multi-client = two apps open one controller directly, **no loopback/THRU at all**. rave-mate uses it **automatically** once Windows enables it. ⚠️ **Do not force-enable the classic-app switch** (the SDK's `midifixreg`): the service itself already ships in-box, but the winmm handoff is staged-rollout-gated - Windows **reverts** the forced `Drivers32` rewiring on the next boot while leaving the device-transfer flag set, a half-state where **every classic MIDI port disappears** (new-API apps keep working; winmm apps - most DJ software and rave-mate - see nothing). Recovery needs a registry restore + reboot. Just wait for the rollout to flip it. |

## One-way port: stop the DJ app reacting to its own LED echo — shipped

A loopMIDI/LoopBe cable is **bidirectional**: the DJ app opens both ends, and rekordbox
auto-mirrors every indicator function's MIDI IN code back out the same-named output (its
MIDI LEARN guide: *"the same code as the MIDI IN will be sent automatically to the MIDI
OUT"* — not disableable). On a cable that echo loops straight back into the app: play
flickers, buttons re-trigger.

Fix: with the **loopMIDI driver installed**, rave-mate creates its own **one-way virtual
port** via the driver's `teVirtualMIDI` API (`TE_VM_FLAGS_INSTANTIATE_TX_ONLY` — only the
"midi-in" half exists). The DJ app sees an **input-only** port named `rave-mate`; there is
no matching output endpoint, so the echo has nowhere to go. Structural — no filtering, no
timing heuristics.

Use it: MIDI tab → controller **THRU** (or DJ-bridge **to DJ** / mixer **output port**) →
pick **"rave-mate one-way port - no echo (recommended)"**, then select `rave-mate` as the
MIDI device in the DJ software. The option only appears when the loopMIDI driver is
present (`teVirtualMIDI64.dll` in System32); the DLL is loaded at runtime and never
bundled.

> Licensing note (maintainers): the virtualMIDI SDK requires clearance from Tobias
> Erichsen before distributing software that integrates it — see
> [virtualMIDI SDK](https://www.tobias-erichsen.de/software/virtualmidi/virtualmidi-sdk.html).
> rave-mate ships nothing of the SDK (it calls the DLL the user installed with loopMIDI),
> but request clearance (info@tobias-erichsen.de) before a release that advertises this.

## Driver-managed forwarding (ravemidi) — shipped

With the ravemidi kernel driver installed, pick **"ravemidi driver (recommended)"** as a
controller's THRU. The DRIVER then taps the hardware and fans it out — forwarding keeps
running when rave-mate is closed and comes back after a reboot on its own:

- by default the DJ-facing port **clones your controller's own name** (toggle: **"Show
  under the controller's own name"**, on by default): the driver holds the real device's
  pin and the identically-named port is what your DJ app opens, so DJ software that keys
  mappings on the device name (Serato) matches your existing mapping with no re-learn. Turn
  the toggle **off** for a separate **`<Name> THRU`** port instead (avoids a duplicate name
  in the MIDI list). Either way the exact port to select is shown in the UI. It is
  bidirectional: controller MIDI down, LED feedback up (message-framed, teed to the
  device) — loop-free by construction, the port has no internal render→capture path
- rave-mate reads the controller **through the driver directly** instead of the device
  (its internal endpoint is hidden from every app's MIDI list, so there is nothing to
  select by mistake); the hardware hold is released so the driver can bind it
- driver config **syncs automatically** on every controller change (add/remove/port/
  filter) — no manual sync; "Re-apply"/"Reload" in the driver card are fallbacks. A
  version-mismatched (older) installed driver shows a persistent "update the driver"
  hint instead of failing silently
- per-controller **Filter out** chips drop aftertouch / poly pressure / pitch bend /
  active sensing / clock on the DJ-facing port only (rave-mate still sees everything).
  Default drops aftertouch+sensing+clock: keybed aftertouch is what MIDI-learn loves
  to latch onto ("every key triggers the binding, again on release")
- it **repairs misbehaving controllers**: some devices re-deliver their entire MIDI
  history with every new event (seen on NI Komplete Kontrol keyboards) — MIDI-learn
  then fires on every key and the stream eventually goes silent, in ANY app reading
  the device directly. The driver detects the replay and forwards each message exactly
  once, so such controllers stay fully usable through the forwarded port

**Input monitor** (MIDI tab): live decoded feed of every incoming message, newest
first — press a control to see which port belongs to which device. **Wire trace**
(driver card, per input): the driver's per-port ring of raw events at every hop
(device raw / to app / app read / app wrote / feedback out / loop drop) for
on-hardware diagnosis.

### Testing LED feedback

Each managed input whose device render pin is bound (status shows "LED feedback") gets a
**Test LED feedback** button in the driver card. It sends a short burst toward the
hardware — Note-On messages sweep notes 36–51 with rising velocity (~50 ms apart), then
note-offs; under 2 s total — through the reserved port's feedback tee, i.e. the exact
path DJ-software LED output takes. Watch the controller: pads/keys/buttons in that range
should flash. Afterwards the driver's wire trace is diffed and the card reports how many
`feedback out` entries appeared vs messages sent — a full count proves the kernel wrote
every message to the device's MIDI input, even if the device shows nothing (some
controllers ignore plain MIDI note LEDs and want a proprietary mode / specific
channel/velocity map). If the render pin isn't bound the card says so instead (device has
no MIDI input, or another app holds it exclusively; the driver keeps retrying).

## Setup (Windows)

1. Install a virtual MIDI port - **loopMIDI** (or any driver above). Create one port, e.g.
   `RavePage`. Traktor can't send MIDI to an app directly; the virtual port bridges them.
2. In Traktor → Preferences → Controller Manager:
   - **Custom state:** import `RavePage-State.tsi` (or build the Generic-MIDI device per the
     CC table below), set its **Out-Port** to the virtual port.
   - **Deck A/B text (optional):** add the stock **Denon DN-HC4500** device, set its
     Out-Port to a virtual port (use a *second* virtual port if running both).
3. In rave-mate → Settings → MIDI controller: enable, set **Custom-TSI port** (and
   **Denon-map port**) to the virtual port name(s). "Detect ports" lists inputs.

Port names are matched as a case-insensitive **substring**, so `loopMIDI` or `RavePage`
both work.

## Custom CC map (the contract)

MIDI **channels 1–4** = decks **A–D** (channel index 0–3). Control Change only.
Continuous values scale `0..127 → 0.0..1.0`; booleans are **true at value ≥ 64**.

| CC | Field       | Scope             | Type       |
|----|-------------|-------------------|------------|
| 20 | `isPlaying` | deck (A–D)        | bool       |
| 23 | `fader`     | channel (1–4)     | 0..1       |
| 24 | `eqHigh`    | channel (1–4)     | 0..1       |
| 25 | `eqMid`     | channel (1–4)     | 0..1       |
| 26 | `eqLow`     | channel (1–4)     | 0..1       |
| 27 | `filter`    | channel (1–4)     | 0..1       |
| 28 | `cue`       | channel (1–4)     | bool       |

Notes:
- Traktor's Generic-MIDI **Add Out** menu exposes booleans (play/cue) and normalized
  controls (fader/EQ/filter), **not** absolute BPM/key/title - those come from the
  HTTP/QML feed, NML, or the Denon text decoder. The custom map deliberately covers only
  what a Generic-MIDI device can emit.
- `fader` feeds the now-playing derivation (the loudest playing deck wins), so even with no
  title source, the custom map tells the recorder which deck is audible.
- To extend: add a row here, add the CC to `customCC` in `custom.go`, add the matching
  Add-Out assignment in the TSI.

## Serato EQ over MIDI (exploration)

Serato's data files (`database V2`, `History/Sessions/*.session`) carry **no mixer state** -
no EQ, no fader. Serato also has no software mixer panel in external-mixer mode, so it does
**not** broadcast EQ out over MIDI the way Traktor's Add-Out does; the Traktor "Add Out →
virtual port" recipe above has no Serato equivalent. The only way to observe Serato EQ is to
tap the **controller's MIDI input** stream (the CC the hardware sends *into* Serato):

- **Controller / internal-mixer setup (capturable).** EQ low/mid/high are software knobs
  driven by controller CC. Feed those same CC into rave-mate and map them to the existing
  channel EQ fields (`eqLow`/`eqMid`/`eqHigh`, CC 24/25/26 in the custom contract) - the whole
  `midisrc` pipeline is reused, only the per-controller CC numbers differ. Getting the stream
  into rave-mate needs one of: a **multi-client** controller driver (both Serato and rave-mate
  open the port - many vendor drivers allow this; Windows `winmm` MIDI-IN is otherwise
  **exclusive**, so a second opener fails), or a **MIDI splitter/router** (loopMIDI + MIDI-OX
  through) fanning the controller to both apps.
- **DVS / external hardware mixer (NOT capturable).** EQ is analog on the mixer; it never
  reaches software or MIDI. No capture is possible - document the limitation, don't fake it.

**Status:** ingest exists (channel EQ fields land on the merger and flow to the overlay/peer
link today via `midi.custom`). **Missing:** per-controller CC maps for popular Serato
controllers (DDJ-FLX4/SB3, Rane One, …) + a Serato-specific setup guide, each validated
against real hardware before shipping (per the Denon caveat - untested CC layouts lie).

## Native MIDI-learn (read any controller) — shipped

The MIDI tab's **Controllers (MIDI-learn)** card reads a physical DJ controller directly and
learns each control, so EQ/trim/filter/fader/play/cue reach the overlays even when the DJ
software can't emit them (e.g. Rekordbox can't push play state over a loopback port — a Button
LED mirrors its own input code, so on a virtual loopback it self-loops; read the controller
instead). Config: `MIDI.Controllers []MIDIControllerMap`, each with a `Port`, `Enabled`, an
optional `ThruPort`, and learned `Bindings`. Applies live (the midi child rebuilds on
`configure`; no restart).

**Workflow:** add a controller → pick its MIDI input port → per control, per channel, click
**Learn** and move that control on the hardware; the first active-edge message (Note-On, or a
CC with a nonzero value) binds. Bindings match on `(status type-nibble, MIDI channel, data1)`;
a CC binding matches CC, a Note binding matches Note-On/Off. All controllers emit under
`midi.custom`, so several controllers fuse into one deck/channel model. Learned `trim` maps to
`FieldTrim` (Traktor's map keeps it send-only). Implementation: `learnedDecoder` +
`ControllerSpec` in `midisrc`, learn capture via `ArmLearn` proxied through the midi child.

**Sharing one controller with the DJ app (Windows single-client MIDI).** `winmm` MIDI-IN is
exclusive, so two apps can't open the same hardware port. Options, in order:

1. **Multi-client driver / Windows MIDI Services.** Some vendor drivers (and Microsoft's new
   MIDI 2.0 stack) allow multiple readers — then rave-mate opens the port directly alongside
   the DJ app, no extra setup.
2. **Built-in THRU (no MIDI-OX needed).** Set the controller's **THRU** to a loopMIDI port and
   point the DJ app at that virtual port. rave-mate owns the hardware and re-emits every message
   to the loopMIDI cable, so the DJ app still gets it — rave-mate *is* the splitter.
3. **External splitter.** loopMIDI + MIDI-OX fanning the hardware to both apps.

## Control rave-mate from the controller (UI mappings) — shipped

The MIDI tab's **Control rave-mate** card maps controller input to *app actions* — the inverse
of everything above (which reads DJ *state*). Actions are the keyboard shortcuts' MIDI twins
and route through the same handlers, grouped by view:

- **Cue editor** (fires only while the editor is open): audition (hold = play from cursor,
  release = snap back — exact hold-Space semantics incl. Note-Off), cursor ±1 beat / ±jump,
  jump size, prev/next track, beatgrid nudge 10 ms / 1 ms, add/remove drop, memory cue,
  delete selected, undo.
- **Library** (collection list showing): move selection, open selected in the cue editor.
- **Navigation** (global): history back / forward.

### Per-device profiles (v30)

Mappings are organized into **per-device profiles**, one section per configured controller
plus **Any device**. A profile is *derived*, not stored: every mapping belongs to the
controller whose input port matches the port captured at learn time; portless (pre-profile)
mappings live under Any device and keep firing from every controller. A device learned from
but no longer configured keeps its own section under its raw port name.

- **Learn targets the device you touch** — pick an action in the add-mapping picker, touch a
  control; the touched device's profile receives the mapping. Two controllers can drive the
  same action with different controls, or different actions with the same CC number.
- **Pause/resume** a whole profile (`MIDI.disabledBindProfiles`; VR keybinds unaffected),
  **copy** a profile onto another controller (modes/sensitivity/reverse survive, only the
  device changes; already-present mappings are skipped), or **clear** it.
- Dispatch: a message fires its own device's profile + Any device, view-scoped as before.

**Workflow:** pick the action in **Add a mapping**, touch the control. Notes bind as press
(momentary press/release for the audition action, or flip to *toggle*); CCs get a per-bind
**mode**: `absolute knob` (0-127 value deltas become steps — the default for step actions),
or a relative-encoder encoding for endless encoders — `two's complement` (1..63 up,
127..65 down, most common), `sign-magnitude` (bit 6 = direction), `offset-64` (65 = +1,
63 = -1) — plus **sensitivity** (raw ticks per emitted step, steps capped at 8/message) and
**reverse**. Bindings match `(type nibble, MIDI channel, data1)` + the captured source port
name, so identical CC numbers on two devices stay distinct.

While a Learn capture is armed the touched control is captured *only* — it never also fires
an existing mapping. The master toggle (`MIDI.disableUiBinds`, inverted) pauses the UI groups
without deleting them; per-device pauses stack on top (`MIDI.disabledBindProfiles`). Storage
is the same `VROverlay.Binds` list the VR keybinds use (`vrbind.Bind`, one store, two
editors); dispatch: MIDI child forward tap → daemon `FireMIDIMsg` (mode semantics, per-bind
state, group + profile gates) → webview act worker → the same `ceKey`/`libKeyNav` paths the
keyboard uses. Implementation: `internal/vrbind` (modes + dispatcher),
`internal/webui/midibind.go` (handlers + scope gate),
`internal/webui/render_midictl_uimap.go` (card) + `midiprofile.go` (profile copy/clear).

## Two-port loopMIDI DJ bridge — shipped

`MIDI.Bridge` (**DJ bridge** card) routes a paired instance's control out to your DJ software.
Peer-injected MIDI is written to `ToDJPort` (a loopMIDI port the DJ app reads); the DJ app's own
output — where it has any — is read back on `FromDJPort`. Two ports keep the DJ app's read and
write on **different** loopback cables so nothing self-loops. Peer MIDI never hits the local
forward tap (mesh-loop safety); locally rave-mate never re-transmits to the port it reads.

## Denon HC4500 decoder (best-effort)

The stock Denon mapping streams LCD text over CC: **channel 0 → deck A, channel 1 → deck B**,
the **CC number is the character slot** (base `1`), the **value carries the character**. The
decoder reconstructs the text, waits ~300 ms for it to settle, then emits it as the deck's
`title` (splitting on the first ` - ` into `artist`/`title`).

**Caveat:** the exact slot layout - and whether a firmware splits each character across two
nibble messages (`(MSB<<4)|LSB`) instead of a direct 7-bit ASCII value - varies by device
and Traktor version. This decoder assumes **direct ASCII in the value**, the common case
when MIDI-Out is pointed at a virtual port. If your capture shows nibble-pairing, change
`decodeChar`/the slot logic in `denon.go`. Validate against real hardware before trusting
A/B titles from this source; the merger ranks `midi.denon` below the HTTP/QML feed anyway.

## Authoring `RavePage-State.tsi` (official Add-Out recipe)

Source of truth: **Traktor Pro 4 Manual**, "Configuring MIDI Controller for Controlling
Traktor" (pp. 138–153), §"Assignable MIDI Controls" / "About Input and Output Controls".
PDF: `native-instruments.com/fileadmin/ni_media/downloads/manuals/traktor/Traktor-Pro-4-Manual-English-170724.pdf`
(NI's `fileadmin` host WAF allows a `curl/*` User-Agent - same as the Denon zip).

**Mechanism (manual-confirmed).** A Traktor control added via **Add Out…** sends that
control's *state* out over MIDI (the feature exists for LED/level-meter feedback, but the
target is any MIDI Out-Port - including a virtual port). Output values are **numeric only**
(button on/off, fader/knob 0–127); Traktor **cannot send text** (title/artist/key) over MIDI
- that is the documented reason the custom map covers transport+mixer only and titles come
from HTTP/QML/NML/Denon. You **cannot "Learn" an output** (manual §Device Mapping: "the only
way to assign a MIDI output controller… is the Assignment drop-down") - you pick the **MIDI
channel (1–16)** then the CC number manually.

**Build it (≈10 min, or import the shipped `.tsi` once we capture it):**

1. Controller Manager → **Add… > Generic MIDI**, name it `RavePage-State`.
2. **In-Port = None**, **Out-Port =** your virtual port (LoopBe / loopMIDI / `RavePage`).
   Output-only, so In-Port None keeps it from reacting to incoming MIDI.
3. For **each deck A→D** (= **MIDI channel 1→4**), add these 7 outputs. The "Control" column
   is the *exact* Add-Out menu path; set **Assignment = Deck A/B/C/D**, then the channel+CC
   via the Assignment drop-down:

   | Add Out… control            | Category    | Type        | Our field   | MIDI (deck X) |
   |-----------------------------|-------------|-------------|-------------|---------------|
   | **Play/Pause**              | Deck Common | Button      | `isPlaying` | ChX · CC 20   |
   | **Volume Adjust**           | Mixer       | Fader/Knob  | `fader`     | ChX · CC 23   |
   | **EQ > High Adjust**        | Mixer       | Fader/Knob  | `eqHigh`    | ChX · CC 24   |
   | **EQ > Mid Adjust**         | Mixer       | Fader/Knob  | `eqMid`     | ChX · CC 25   |
   | **EQ > Low Adjust**         | Mixer       | Fader/Knob  | `eqLow`     | ChX · CC 26   |
   | **Mixer FX Adjust**         | Mixer       | Fader/Knob  | `filter`    | ChX · CC 27   |
   | **Monitor Cue On**          | Mixer       | Button      | `cue`       | ChX · CC 28   |

   = 7 × 4 = **28 Add-Out assignments**. `Mixer FX Adjust` is the filter/Mixer-FX amount knob
   (Filter is the default Mixer FX). Set **MIDI Range 0–127**; **Controller Range 0–1** for
   buttons (play/cue → 0 or 127, decoder treats ≥64 as true) and **0–1** for the continuous
   knobs/faders (streamed 0–127 as you move them → decoder scales to 0..1).
4. **Export** the device → `RavePage-State.tsi`. Send it over; we then diff it against the
   pre-export snapshot to learn the binary Add-Out (CMAD/DCDT) byte structures and ship it as
   a one-click managed mapping (a second toggle in the Traktor-mappings card, beside Denon) -
   so nobody hand-builds 28 rows again.

The Denon path needs no authored TSI - it reuses Traktor's stock Denon device.

## Beyond MIDI Out: richer / more elegant state paths

Researched against the Traktor 4 manual - what Traktor can natively emit in real time:

| Path | Gives you | Elegance / cost |
|---|---|---|
| **MIDI Out** (above) | play, fader, EQ, filter, cue - per deck, low-latency, **numeric** | Official, robust, survives Traktor updates. No text. **Recommended baseline.** |
| **MIDI Clock send** (Prefs → External Sync → Enable MIDI Clock; Master must be Clock) | **tempo (BPM) + downbeat start/stop** as a clock stream | Official. Perfect for **beat-synced DMX strobe / Resolume BPM**. Tempo only, no per-deck. |
| **Ableton Link** | shared **tempo + beat phase** over LAN | Official, zero-config. Tempo/phase only - not which deck/track. |
| **Icecast broadcast** (Prefs → Broadcasting) | audio + **ICY metadata = current title/artist** | Official text source! Heavy (full audio stream). This is the planned `icecastsrc`. |
| **QML injection** (community, not native) | **everything** - all 4 decks' title/artist/BPM/position/faders over a local socket | Richest, but **invasive** (edits Traktor's QML UI, breaks on updates). The `:8080` HTTP feed + planned `qmlsrc` are this. |

**There is no native OSC and no native HTTP/REST in Traktor 4** (manual has zero OSC/API
mentions; the Electron client's `:8080` listener is fed by the QML hack, not by Traktor).

### rave-mate as the show-control hub (DMX + Resolume)

The elegant architecture is **not** another Traktor output - it is to let rave-mate's
**merger** (which already fuses per-deck `isPlaying`/`fader`/`eq*`/`filter` + BPM) fan out to
lighting/visuals as new **sinks** (same Source→Merger→Sink pattern as `filesink`/`recorder`,
see `DJ_SOURCES.md`):

- **`oscsink`** → emit `/ravemate/deck/{A..D}/{playing,fader,eqHigh,…}` + `/master/bpm` to a
  configured host:port. **Resolume / TouchDesigner / Chataigne speak OSC natively.** OSC is a
  trivial UDP packet format - pure-Go, no third-party dep (7-day-soak-friendly).
- **`artnetsink` / `sacnsink`** → map merger fields → DMX channels → **Art-Net or sACN (E1.31)**
  UDP. Drives QLC+, Resolume Arena DMX, ENTTEC/Art-Net nodes, most lighting desks. Both are
  simple UDP/512-byte-universe protocols (pure-Go feasible). Configurable map, e.g. deck
  fader → dimmer, `isPlaying` → gate, `master/bpm` → strobe rate, EQ-low → bass-reactive.

So: **one Traktor MIDI-Out mapping → LoopBe → rave-mate → {OSC to Resolume, Art-Net to DMX,
rave.page stream}**, with `master/bpm` optionally sourced from Traktor's **MIDI Clock** for
sample-accurate beat sync. This makes rave-mate the bridge from Traktor's numeric-only output
to the lighting/visual world, instead of wiring Traktor → each tool separately.

**Sources:**
- [Traktor Pro 4 Manual (PDF)](https://www.native-instruments.com/fileadmin/ni_media/downloads/manuals/traktor/Traktor-Pro-4-Manual-English-170724.pdf) - Controller Manager / Configuring MIDI Controller (pp. 138–153)
- [Configuring MIDI Controller for Controlling Traktor (online manual)](https://www.native-instruments.com/ni-tech-manuals/traktor-pro-manual/en/configuring-midi-controller-for-controlling-traktor)
- [How to Set Up a Generic MIDI Controller in Traktor](https://support.native-instruments.com/hc/en-us/articles/209570969-How-to-Set-Up-a-Generic-MIDI-Controller-in-Traktor)
- [How to Map LEDs in Traktor (Add Out / output controls)](https://djtechtools.com/2010/05/30/how-to-map-leds-in-traktor/)

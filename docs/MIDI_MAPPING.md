# MIDI mapping - rave-mate DJ-data source

rave-mate reads MIDI **input** and decodes it into session fields. Two decoders run over
the configured port(s):

- **Custom map** (`midi.custom`) - our own Traktor mapping. Deterministic, what we ship.
- **Denon HC4500 stock map** (`midi.denon`) - Traktor's built-in mapping reused to stream
  deck A/B track text. Best-effort (see caveat).

Source: `internal/session/sources/midisrc/`. Driver: `internal/midi/` (Windows = `winmm.dll`
via stdlib `syscall`, **no third-party dependency**; other OSes report unsupported).

## Setup (Windows)

1. Install a virtual MIDI port - **loopMIDI** (Tobias Erichsen). Create one port, e.g.
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

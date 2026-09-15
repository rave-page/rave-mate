# Perception-grounded UI principles

Binding for every rave-mate surface that shows many things at once: the Live
dashboard, the Library, a long Settings section, the VRChat lists, an Automations
editor, a MIDI map, the Logs stream. `DESIGN.md` is the rule layer; this file is
the research each rule rests on and how it applies to a **native desktop live-set
control app**. Ported from rave.page `rave-page-design-system/PRINCIPLES.md`
(2026-09-09) and kept on the SAME P-numbering so rave.page, shingadayo and
rave-mate share one vocabulary. Timeline/music-web-only principles are marked N/A
or reframed for a desktop control surface. A rule here binds like `CLAUDE.md`
§ Design & UX.

The test that motivates it: mid-set, one monitor, glance budget ~1 s. "Is the
stream up, is the route frozen, what's the deck doing, and can I flip this one
toggle" must be answerable at a glance, hands mostly on the decks — and never at
the cost of a frame on the media path.

Domain deltas from rave.page (a mobile-first music-discovery web app):
- **Desktop + pointer-precise + `ctl`-driven.** No touch. The `ctl` control
  plane (`snapshot`/`click`/`read`/`screenshot-all`) is both the accessibility
  layer and the test harness — it is rave-mate's "thumb". See P15.
- **The user is working, not browsing.** Surfaces are tools (configure, monitor,
  route, capture), not a feed. "Relevance now" (P2) means the live/connected
  state, not taste ranking.
- **A frame is sacred.** rave-mate runs during live sets; a stutter is a defect.
  Perception-of-performance (P14) is a hard rule wired to the activity governor.

## The principles

### P1 — Four chunks at a glance
Working memory holds about four chunks at once, not seven (Cowan 2001, revising
Miller 1956). **Rule:** a surface's first view exposes ≤ 4 groups; a group shows
≤ 3–4 primary items; the rest sit behind one disclosure ("+7", a "More"
section, a collapsed card). A group is a chunk only with a name the user would
use (a device, a source, "Recording") — never "Section 2". **Fails:** a Settings
tab with twelve flat cards; a control card with ten sibling toggles and no
grouping; a disclosure per row instead of per group.

### P2 — Default to the live/relevant state, don't demand a choice first
Decision time grows with log(options) (Hick 1952; Hyman 1953); choice overload
bites under time pressure with hard-to-compare options (Iyengar & Lepper 2000;
Scheibehenne, Greifeneder & Todd 2010, meta-analysis). **Rule:** a dense surface
opens on what is true and actionable now — the connected device, the running
route, the current session, the last-used target — ordered by live/recent, never
alphabetical, never a blank "pick something first". Search and filters refine a
usable default. **Fails:** a Library that is empty until a folder is picked; a
VRChat tab that shows nothing until you choose a list; an A–Z device dump.

### P3 — The live state is the landmark
People segment around landmarks and event boundaries, not uniform ticks (Shum
1998; Zacks & Tversky 2001). rave.page's night-phase timeline is N/A (no time
axis here). **Reframed:** on a live surface the primary landmark is **now/live**
— the running route, the armed recorder, the connected peer, "picture frozen
Ns". State is anchored to that landmark, not buried in a uniform list of rows.
"Live" is a distinct, always-locatable region, not only a colour change (see P4,
P5). **Fails:** a Live dashboard where the running thing looks identical to the
idle things; a "recording" state signalled only by a red tint.

### P4 — Spend the pre-attentive channels deliberately
Position, length, luminance, size and motion are read in < 250 ms in parallel;
hue is pre-attentive too but reserved for intent (Treisman & Gelade 1980; Healey
& Enns 2012; Ware 2012). Position/length read most accurately, area less, colour
saturation least (Cleveland & McGill 1984); a lane is also a Gestalt common
region (Palmer 1992). **Rule:** one hue per surface (the brand hue). Category
(device kind, source, log level, tab) is **position** — a lane, a section, a
column — never a colour. State (connected/idle, live/stopped, ok/warn/error) is a
scarce **status token** (`.rp-badge--success|warning|error`) or **luminance**
within the hue, not a new accent. Magnitude (level, VRAM, fps, progress) is
length/area, ordinal. Anything read exactly (a count, a time, a path) is **text**.
**Fails:** rainbow-coloured device rows; a second accent hue meaning "new";
saturation the user must decode; a bare colour swatch as the only state signal.

### P5 — Motion is scarce and meaningful
Moving elements capture attention over colour/shape and distract from the task
(Bartram, Ware & Calvert 2003); animated transitions help track a state change
(Heer & Robertson 2007). **Rule:** ≤ one continuous motion cue per viewport, and
it marks **live/now** (a mint pulse on the running route/recorder). Overview↔detail
transitions animate once, < `--motion-slow` (300 ms), then rest. All motion is
gated: it stops under `prefers-reduced-motion` (the block in `colors_and_type.css`)
AND when the **activity governor** says a set/stream is running
(`governor.UIAnimAllowed()`, ~1 Hz webui ticks). The static rendering must stand
alone. **Fails:** pulsing counters, a marquee log, hover-wiggle cards, two
pulsing markers, an animation that runs while OBS is live.

### P6 — Overview → zoom & filter → details, as focus+context
"Overview first, zoom and filter, then details on demand" (Shneiderman 1996) as
focus+context, not paging (Bederson et al. 2004; Rao & Card 1994; Furnas 1986).
**Rule:** a dense surface is one navigable shape; the focused item expands in
place while the rest compresses (the two-mode editor, the Library nav-rail +
detail). The detail layer **reuses the same components** as the overview (the
player strip in the list is the player strip in the editor). Back never loses the
overview's scroll/selection. **Fails:** paginating the Library destructively; one
tab per device; a "back" that resets the list.

### P7 — Density is a shape, not a number
Many concurrent flows read as one shape (Havre, Hetzler & Nowell 2000,
ThemeRiver; Heer, Kong & Agrawala 2009). **Rule:** where "how much / over time"
is the question — level meters, VRAM/`gpumem` pressure, fps, encode bitrate,
route health, waveform — draw it as a **shape** (a bar/spark/area in the single
hue), not a wall of counters or a badge storm. A counter stays only where the
number IS the answer (track count, capacity, dropped-frame total). Change over
time is legible as a shape; a frozen picture reads as a flat line, not a fine
number (`framedebug` "picture frozen Ns"). **Fails:** a header of six vanity
counters; a per-field metric badge on every row.

### P8 — Keep related things together
Splitting attention across separated sources adds load (Chandler & Sweller 1992;
Mayer, spatial contiguity). **Rule:** a thing's identity, its state, and its one
action sit on the **same card/row** (device name + connected badge + connect
button together). No legend the eye must leave the data for; label a lane/section
at its own head. A `?`-tooltip explains, but is never the ONLY place a live fact
is written. **Fails:** a status legend in the footer; the value in one column and
its control in another; the only copy of an error living in a tooltip.

### P9 — Horizontal text, linear reading
Rotated text reads slower; radial layouts hide ordering (Wigdor & Balakrishnan
2005). **Rule:** horizontal text everywhere; time/progress runs left→right; no
vertical axis labels, no radial gauges or dials for a value the user must read.
On a narrow window sections stack vertically and a wide strip scrolls
horizontally. **Fails:** a rotated column header; a circular level/BPM dial as
the primary readout.

### P10 — Colour follows the emotion of the music
N/A. rave.page-specific (`EventSummary.energy_level` → luminance on a timeline
bar). rave-mate has no per-item energy signal in its control UI; do not invent
one from colour. The narrow transfer: where a media visual encodes intensity
(a waveform, a level meter), express it as **luminance/length inside the brand
hue**, never a second hue — that is already covered by P4.

### P11 — Media reinforces, never informs alone
Cross-modal cues sharpen recognition (Spence 2011), but media that starts itself
interrupts. **Rule:** previews — the player, VRChat/world thumbnails, the emoji
flipbook preview, camera-path previews — **never autoplay**; press/tap to load
and play, level-matched, cross-faded. Every fact a preview carries (a duration, a
resolution, a tier, a frame count) is **also in text** next to it. A preview is a
detail-layer affordance, never an overview interruption. **Fails:** a thumbnail
grid that autoplays audio/video; a preview whose only statement of a fact is the
picture itself.

### P12 — The configured state is the product
Choice satisfaction rises when a choice is easy to revisit and regret is low
(Iyengar & Lepper 2000; Schwartz 2004). rave.page's "the plan" reframes here to
**"the user's setup"**: saved presets, connected devices, armed automations,
routed sources, the library — the state the user built. **Rule:** that state is
marked where the user meets it and persists visibly, not as a transient icon
recolour: a saved preset reads as saved on its card, an armed automation shows
armed, a connected device shows connected, a conflicting binding is flagged
**before** it fires. The setup is a first-class, always-locatable destination,
not a buried list. **Fails:** a toggle that only recolours a glyph with no
persistent, legible state; two automations that clash with no warning.

### P13 — Measure the experience, not the code
`go build`/`vet`/`test` and golden diffs prove correctness, not usefulness (Sauro
& Lewis 2016). **Rule:** every UI change is verified on the **running** app via
`ctl screenshot-all` (CLAUDE.md hard rule), and eyeballed — a clean build is not
a pass. A redesign of a dense surface ships behind a viewer-visible toggle with a
written protocol (time to the first confident read, clicks to the goal, one
misread count) on a representative state; the numbers pick the default. **Fails:**
"typechecks, shipping it"; a dense-surface redesign with no before/after shots
and no task.

### P14 — Performance is a perception rule
< 100 ms feels instant, < 1 s keeps flow, 10 s loses attention (Nielsen 1993;
Card, Robertson & Mackinlay 1991). In a live-set tool this is a **hard rule**, not
a nicety. **Rule:** the UI paints from state already held; derivations are
computed once per input, not per tick; time-dependent state ticks on the shared
~1 Hz webui loop, never a `requestAnimationFrame` busy-loop. The **activity
governor** is the authority on when the machine is busy — it strips animation and
defers heavy work while a set/stream runs (`UIAnimAllowed`, `BackgroundAllowed`);
the static view is the design, motion is garnish. Live updates never swap content
under the pointer without a visible mark — unmarked changes are missed (Rensink,
O'Regan & Clark 1997, change blindness). **Fails:** a render that re-lays-out on
every log line; an animation that competes with encode for the GPU; a value that
silently changes under the cursor.

### P15 — Reachable by pointer, keyboard AND `ctl`
Touch precision follows Fitts's law (Fitts 1954); rave.page's 44 px coarse-pointer
rule is **N/A** — rave-mate is a desktop, pointer-precise app with no touch.
**Reframed:** targets are still comfortably clickable (no 12 px hit areas), but
the binding requirement is that **every control is reachable and identifiable
three ways**: by mouse, by keyboard focus order, and by the `ctl` control plane —
each interactive element carries a stable id/label so `ctl snapshot`/`click`/`read`
can drive it (this is both the a11y contract and the test contract; CLAUDE.md
mandates `ctl` verification). **Fails:** a control only reachable by hover with no
focusable/`ctl`-addressable element; an icon button with no accessible name.

### P16 — Few controls, unequal weight, honest signifiers
Choice time grows with options (Hick 1952); same-looking things read as the same
kind (Gestalt similarity, Wertheimer 1923); people learn what's tappable from
signifiers (Norman 2013). **Rule:** a control row offers ≤ 4 equally weighted
choices; exactly **one filled primary per surface** (`.rp-btn--primary` /
`.rp-btn--go`), everything else `.rp-btn--outline` / `.rp-btn--ghost`; > 4 actions
collapse behind one menu; `.rp-btn--destructive` never sits beside the primary.
**Information and controls never share a look:** a status is a `.rp-badge` (a
label — no border+hover control signifier), a filter/toggle is a `.rp-chip`
(a control); a fact that is not clickable is plain text in position, not a pill.
**Fails:** a filter row of eight chips; a status badge styled like a clickable
chip; a card whose every fact is a pill; two filled primaries on one surface; a
row of five equal buttons.

## Where each principle lives in rave-mate

| Principle | Owner / anchor |
|---|---|
| P1, P2 | Settings section grouping + search (`render_settings*.go`); Library nav-rail; VRChat/Worlds lists default to live/recent |
| P3, P4 | Live dashboard "now/live" region; state as `.rp-badge` status token + position, never hue; `render_live.go` |
| P5, P14 | `governor` (`UIAnimAllowed`/`BackgroundAllowed`); reduced-motion block `colors_and_type.css`; ~1 Hz webui tick |
| P6 | two-mode editor (`render_editor*.go`); Library nav-rail + detail reuse; scroll/selection retention |
| P7 | `framedebug`/`testcard`/`gpumem` as shapes; waveform + level meters; counters only where a number is the answer |
| P8 | card grammar (identity + state + action together) in `components.go` recipes; `help.go`-style `?` tips are secondary |
| P9, P15 | horizontal layout; `ctl` snapshot/click/read reachability + keyboard focus order |
| P11 | player/preview + flipbook/world previews: press-to-play, no autoplay, text mirrors media |
| P12 | presets/automations/devices/library render their persistent saved/armed/connected state; clash flags |
| P13 | `ctl screenshot-all` visual verification (CLAUDE.md); toggle + protocol for a dense-surface redesign |
| P16 | one primary per surface (`.rp-btn--primary`/`--go`); `.rp-badge` (info) vs `.rp-chip` (control); ≤4-control rows |

## References

Bartram, Ware & Calvert 2003, IJHCS 58(5) · Bederson, Clamage, Czerwinski &
Robertson 2004, ACM TOCHI 11(1) · Card, Robertson & Mackinlay 1991, CHI ·
Chandler & Sweller 1992, Br. J. Educ. Psychol. 62(2) · Cleveland & McGill 1984,
JASA 79(387) · Cowan 2001, Behav. Brain Sci. 24(1) · Fitts 1954, J. Exp. Psychol.
47(6) · Furnas 1986, CHI · Havre, Hetzler & Nowell 2000, InfoVis · Healey & Enns
2012, IEEE TVCG 18(7) · Heer & Robertson 2007, IEEE TVCG 13(6) · Heer, Kong &
Agrawala 2009, CHI · Hick 1952, QJEP 4(1) · Hyman 1953, J. Exp. Psychol. 45(3) ·
Iyengar & Lepper 2000, JPSP 79(6) · Mayer 2009, Multimedia Learning · Miller
1956, Psychol. Rev. 63(2) · Nielsen 1993, Usability Engineering · Norman 2013,
The Design of Everyday Things (rev.) · Palmer 1992, Cogn. Psychol. 24(3) · Rao &
Card 1994, CHI · Rensink, O'Regan & Clark 1997, Psychol. Sci. 8(5) · Sauro &
Lewis 2016, Quantifying the User Experience · Scheibehenne, Greifeneder & Todd
2010, JCR 37(3) · Schwartz 2004, The Paradox of Choice · Shneiderman 1996, IEEE
Symp. Visual Languages · Shum 1998, Psychol. Bull. 124(3) · Spence 2011, Atten.
Percept. Psychophys. 73(4) · Treisman & Gelade 1980, Cogn. Psychol. 12(1) · Ware
2012, Information Visualization (3rd ed.) · Wertheimer 1923, Psychol. Forsch. 4 ·
Wigdor & Balakrishnan 2005, ECSCW · Zacks & Tversky 2001, Psychol. Bull. 127(1).

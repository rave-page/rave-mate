# B7 increment (iii): i18n catalog handshake — design + measurement (pre-code review gate)

Status: **DESIGN + NUMBERS. Recommendation: do NOT ship the catalog into Zig.** The Go-side half
of (iii) is already shipped (`perf(i18n)`, this branch). Same gate as (ii)'s design doc: no native
code until reviewed, because this one reverses a recorded **non-negotiable**, not just a phase
decision.

## What (iii) was

(iii) existed only as two forward references — there is no spec:

- `B7_RETAINED_DOC_DESIGN.md` §3: *"Locale is folded in … This is the (iii) i18n-handshake
  dependency and must land with (ii)'s guard, not after."*
- `ZIG_UI_GUIDE.md` (ii)'s as-built: *"No catalog payload - (iii) reuses this plumbing."*

So the plumbing (a Go-owned monotonic locale generation in the slot guard) landed in (ii) and works.
The open question (iii) inherits is the one thing that plumbing would enable: **ship the catalogs to
Zig and put i18n KEYS in the documents instead of resolved strings.**

## The non-negotiable it collides with

"i18n Go-only" is not a phase-B decision that phase B may revisit — it is recorded as a
non-negotiable of the whole render architecture, in five places:

| where | text |
|---|---|
| `.devnotes/UI_RENDER_ARCH_ANALYSIS.md` | *"stays inside every non-negotiable (ctl parity byte-identical, **i18n Go-only**, no framework, single daemon)"* |
| `ZIG_UI_GUIDE.md` rule 6 + pipeline | *"Go resolves a `<tab>State` struct (all data + RESOLVED i18n strings — catalogs stay single-source in Go, rule 6)"* |
| `native/zigui/src/root.zig` header | *"catalogs stay single-source in Go"* |
| `CLAUDE.md` | *"state JSON carries resolved i18n"* |
| the porting recipe | the impure state builder owns `i18n.T`; the pure renderer — the half mirrored in Zig — never sees a key |

It also buys a specific safety property the guide records the hard way: a **blank i18n value must
never decide a branch** (`hasWarn`/`hasGroup`/`hasMsg` are explicit bools for exactly this reason).
Key-side resolution adds a brand-new way to produce "" — a Zig-side catalog miss — on a seam whose
whole gate is byte-equality. And some strings cannot cross as keys at all: `verb`/`rest` carry a
**locale-dependent split point**, and `renderTip` was already refused for doing *"locale-dependent
text processing that belongs on the Go side of the seam"*.

Reversing that needs a payoff. Here it is.

## Measurement 1 — how much of a document is i18n text at all

Method: walk real RZW1 documents generically (the format is self-describing, so every `wt_string`
payload is an `(arena off, len)` pair — no schema needed), collect the **distinct** arena strings
each document references (deduped arena bytes are what the document actually pays), and classify
each against the `en` catalog: exact value, or an interpolation of a templated value. Template
matching requires literal chunks in order, the leading one anchored at the start, the trailing one
at the end, a literal run >= 6 B, and a bounded fill — without those guards a value like
`"{a} · {b}"` "matches" the player's 29 kB SVG and the answer comes out 5x too high.

Corpus: **827 documents** — every golden fixture of every migrated surface (`wireBasesB2` +
`wireBasesB7` + the pilots + the two tick surfaces). en catalog: 3529 keys, 2692 plain values,
475 templates usable as identifiers.

| family | docs | doc B | arena B | i18n B | +templated | % of doc |
|---|--:|--:|--:|--:|--:|--:|
| player (`mp`) | 110 | 1 210 922 | 1 110 603 | 54 119 | 1 970 | **4.6%** |
| settings (`set`) | 39 | 303 862 | 239 251 | 120 496 | 4 360 | **41.1%** |
| library (`lib`) | 65 | 174 134 | 133 927 | 11 616 | 1 590 | 7.6% |
| worlds modals (`ws`) | 35 | 31 148 | 27 414 | 14 673 | 306 | **48.1%** |
| live cockpit | 66 | 61 298 | 56 322 | 640 | 0 | **1.0%** |
| live tick | 6 | 24 904 | 22 224 | 444 | 0 | 1.8% |
| peers | 14 | 47 220 | 40 416 | 1 349 | 538 | 4.0% |
| midi mixer | 25 | 30 863 | 21 006 | 1 104 | 242 | 4.4% |
| **TOTAL** | **827** | **2 439 445** | **2 100 564** | **244 871** | **15 401** | **10.7%** |

→ i18n text is **12.4% of arena bytes, 10.7% of document bytes, 25.8% of distinct strings**.

The distribution is what kills the idea: the surfaces where chrome dominates (settings 41%, worlds
modals 48%) are **rendered on demand**, while the surfaces on the hot path (player 4.6%, live
cockpit 1.0%, live tick 1.8%) are almost pure data. A catalog handshake optimizes the documents
that are not the problem.

## Measurement 2 — what removing document bytes is worth on this seam

(ii) already answered this, decisively, and it is the whole argument:

> `#ce-topbar` retained deltas are **1.3% of the full document** — a 98.7% byte cut — and the
> measured dispatch delta is **+0.8% / -4.5%**, inside the noise band.
> (`PHASEB_BASELINE.md` "Phase B7 (ii)")

A 98.7% byte reduction buys no measurable dispatch time. A **10.7%** reduction cannot. The seam is
not byte-bound: per B-1's split, encode is ~28% of dispatch (44 µs of 158 µs on the log tail) and
the arena is only part of the encode, so 10.7% of document bytes is ~1-3% of dispatch — an order of
magnitude below what this repo will call a win.

And the handshake is not free on the paths it touches:

- every string field needs a **reverse lookup** (resolved value → catalog id) at encode time, so
  the Go side trades ~10 ns of arena append for a map probe per string — the encoder gets *slower*
  on the 74% of strings that are not catalog values (`ws` is the best case at 48%, `live` the worst
  at 1%);
- the RZW1 header must grow a `locale_gen` (14 → 18 B) so a document cannot be resolved against the
  wrong catalog, which is a format break on **every** document and every decoder test;
- Zig gains a second source of truth for locale resolution plus a 261 kB-per-locale resident
  catalog, and a catalog miss becomes a new "" path on a byte-equality seam;
- `Tn`'s plural categories and `{...}` interpolation stay Go-side regardless (they are computation,
  not table lookup), so templated values — 5.9% of the i18n bytes — cannot ride keys anyway.

## Measurement 3 — where i18n actually costs on this seam (and the fix, shipped)

The package had **no benchmarks**, so the cost was unfalsifiable. Baseline + the two fixes it
pointed at are in `perf(i18n)` on this branch:

| | before | after | Δ |
|---|--:|--:|--:|
| `T` (plain key) | 31.4 ns | **12.9 ns** | **-59%** |
| `lookup` alone | 21.4 ns | 9.9 ns | -54% |
| `T`, 32 goroutines | 71.0 ns | **0.90 ns** | -99% |
| `Tn` | 192 ns | 161 ns | -16% |
| `T` + `{interpolation}` | 163 ns | 144 ns | -12% |
| 400-key state-build shape | 13.5 µs | **5.4 µs** | -57% |

Two causes, both Go-side and both invisible from the wire: `load()` took the **exclusive** mutex on
every call just to read a `loaded` bool (~10 ns of a 31 ns `T`, and it serialized concurrent
resolvers behind a writer that never writes), and `lookup` took `mu.RLock` per key even though the
catalogs are immutable after load.

Seam level: a settings state build resolves ~400 keys, so this removes ~7 µs of a ~100 µs build —
7%, inside the noise band, but it is the *right* 7%: it is on the handler lane, it needed no wire
change, and it left the non-negotiable intact.

## Recommendation

1. **Do not ship catalogs into Zig.** Ceiling 10.7% of document bytes on a seam where a 98.7% cut
   measured as noise; it inverts the cost on data-heavy surfaces, breaks the RZW1 header for every
   document, and trades a recorded non-negotiable for it.
2. **(iii) is closed by the Go-side fast path** (shipped) plus this record. The locale-generation
   plumbing from (ii) stays as-is — it is what makes a locale switch safe on the retained channel,
   and it needs no catalog to do that job.
3. **Reopen only against these preconditions**, all three at once:
   - a surface above ~40% i18n bytes appears on a **high-cadence** path (today the >40% surfaces —
     settings, worlds modals — are rendered on demand);
   - the seam becomes byte-bound (e.g. documents cross a process boundary rather than a cgo call —
     the B5 procShell would change this calculus);
   - a catalog miss can be made *structurally* impossible rather than a runtime "" (e.g. wiregen
     emits the id↔value table from the same schema pass, so a missing id fails the build).
4. **If it is reopened anyway**, the shape that preserves the most: keep Go as the single source of
   truth by uploading a **content-addressed intern table** rather than the i18n catalog — Go still
   resolves every string, the table is just "strings this UI has already sent under this locale
   generation", and Zig never knows what a locale is. That confines the change to the encoder + the
   retained channel (which already carries `locale_gen`), needs no RZW1 header break, and cannot
   invent a translation. Its ceiling is the repeat rate of strings across consecutive documents on
   ONE surface, which is worth measuring before anything else — it is strictly larger than 10.7%
   and does not touch the non-negotiable.

## Review asks

1. Accept "do not ship catalogs into Zig", with the numbers above as the record?
2. Accept the Go-side fast path as (iii)'s deliverable (already gated + committed)?
3. Keep the three reopen preconditions as the bar, and the intern-table shape as the preferred
   alternative if it is ever reopened?

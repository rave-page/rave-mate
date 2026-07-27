# P0: CPU readback delivered black frames

Branch `fix/spout-recv-black` off `development` @c3ed1f5. Fixed in `4628139`.

## Root cause

The vendored `SpoutLibrary.h` and the shipped `SpoutLibrary.dll` are **different revisions of the
COM-like interface**, and the mismatch is a **window**, not a uniform shift. Both come from the
2.007.017 release zip, but from different subtrees (`include/SpoutLibrary/` vs `MT/bin/`), and their
slot orders do not agree.

Measured on this box with a canary buffer + ground-truth accessors on a live 3840×2160 sender:

| header slot | method | result |
|---|---|---|
| 5 | `SendImage` | **aligns** - sending has always worked |
| 12 | `GetHandle` | shifted → returns 0 where the registry has a real handle |
| 19 | `ReceiveImage` | shifted → lands on `ReceiveTexture(GLuint,…)` |
| 22 | `IsFrameNew` | shifted → lands on `IsConnected`: permanently "true" |
| 24 | `GetSenderWidth` | shifted → returns `GetSenderName`'s **pointer** (1240326832) |
| 25 | `GetSenderHeight` | shifted → returns the real **width** (3840) |
| 26 | `GetSenderFormat` | shifted → returns the real **height** (2160) |
| 111-114 | `GetSenderCount/GetSender/FindSenderName/GetSenderInfo` | **align** |

`ReceiveImage` therefore executed `ReceiveTexture`, which takes a **GLuint texture id** where we
pass a **pixel pointer**. It returns true, the (shifted) frame-new query returns true, and not one
byte is written: a 33 MB canary survived **12/12 attempts, byte for byte**.

That single fact explains every observation, including the ones that looked contradictory:

- healthy counters + black pixels — the metadata came from methods that *do* respond, just not the
  ones we thought we were calling;
- `GetHandle` returning NULL while `GetSenderInfo` returns a working handle (inc 2 worked around
  this without knowing why);
- `GetSenderFrame` junk and `GetSenderFps` = the monitor's refresh rate (inc 3's M1, recorded then
  as "late-vtable skew" — correct, but the cause was unknown);
- **the pre-rework tree getting zero frames instead of black ones**: its first call passed NULL, so
  `ReceiveTexture(0,…)` simply failed. Same bug, two shapes. "It worked before 9913de1" was never
  true — the CPU readback has been broken for as long as this header/DLL pairing has been vendored.

I own the worst part of this: in increment 2 I hit this exact symptom, wrote a positive control,
watched it fail, and filed it as *"Spout's receive side cannot see a foreign-device write on this
rig — not a usable oracle"*. The control was telling the truth; I mislabelled a product bug as an
instrument bug. The instrument that would have settled it — a canary, distinguishing "copied zeros"
from "never written" — took ten minutes to write once I stopped assuming.

## The memory-share hypothesis: tested, and refuted by execution

The proposed cause was a silent fallback to Spout's **CPU memory-share** mode in our windowless
receivers, returning an empty shared-memory buffer (success + frame-new + right dims + zero pixels).
It fits the field symptom exactly, so it was worth testing first. Three independent results rule it
out:

1. **The canary is the discriminator, and it says "never written".** A memory-share read of an empty
   buffer *copies zeros* - it would destroy a canary. Filling the target with `0xA5` and receiving
   leaves **33,177,600 bytes of canary intact, 12/12 attempts**. Nothing was written at all. "Copied
   zeros" and "never written" are different bugs, and this is the second one.
2. **Three consecutive accessors return each other's values.** `GetSenderWidth()` → a pointer,
   `GetSenderHeight()` → the real **width** (3840), `GetSenderFormat()` → the real **height** (2160).
   A share-mode fallback cannot produce a deterministic off-by-one across neighbouring methods; only
   a slot misalignment can.
3. **Controlled experiment - same process, same publisher, only the call changed.** The new D3D11
   readback delivers `top(255,0,0) bottom(0,0,255) left(0,255,0)` from the *same windowless Go test
   binary* that, moments earlier, got an untouched canary from `ReceiveImage`. My fix creates no
   window and no GL context either. If windowless interop init were the cause, it would still be
   black.

**Trap worth naming:** the two readings that look like confirmation - `cpu=true`, `gldx=false` - come
from `GetSenderCPU()`/`GetSenderGLDX()`, which sit in the same suspect region of the vtable (I proved
misalignment at slots 12-26 and alignment at 111-114; these are at ~31-32, inside the unproven-but-
suspect span). `cpu=true` is most likely `GetSenderTexture()` returning a non-null pointer read as a
bool. So the evidence that appeared to support a memory-share fallback is exactly the evidence that
cannot be trusted - which is the same trap that made me file this as an "instrument problem" in
increment 2.

**What the hypothesis got right:** "refuse loudly instead of shipping zeros" is now the behaviour. A
genuine CPU/memory-share sender has no DX11 shared texture, so `GetSenderInfo` returns `share == 0`
and the new path returns -1 (no frames, sender reported unusable) instead of delivering black. Same
for torn registry geometry. Whatever the mode, zeros are never presented as a frame.

**Strategic point, agreed:** the zero-copy DX path needs no GL context at all, and now neither does
the CPU readback. Nothing on the receive side requires OpenGL any more.

## Fix

The receive path no longer calls into the misaligned window. The only Spout call left is
`GetSenderInfo` (aligns, shared-memory read, already the basis of the zero-copy encode path);
the readback is plain D3D11:

```
GetSenderInfo → share handle + dims + format
OpenSharedResource → CopyResource into a STAGING texture → Map → row copy (BGRA→RGBA)
```

- One **bounded** acquire (3 ms) of the sender's named access mutex around **one** GPU copy — the
  same discipline `cap.zig` uses, so we never serialise against the sending app's submissions.
- A changed share handle / geometry re-opens and reports code 2, so a **re-created** sender can
  never be read through its dead texture.
- **Return codes are unchanged**, so `recvpoll.go`'s state machine, its geometry validation and the
  bounded pixel pool from the OOM fix are untouched. That fix was correct and stays.
- The swizzle is not new work: the old path asked `ReceiveImage` for `GL_RGBA` and the SDK did the
  BGRA→RGBA conversion internally.
- Side effect worth knowing: **receiving no longer needs an OpenGL context** (one of the design's
  standing goals, §2 item 4).

## Where the readback can and cannot serve (input for the default flip)

The directive asks which environments the readback cannot serve. The memory-share/interop hypothesis
was refuted, so the answer is not "windowless processes" - it is a much shorter list, because the new
path needs strictly LESS than the old one:

**Needs:** `SpoutLibrary.dll` resolvable (registry queries only), a D3D11 hardware device, and a
sender with a DX11 shared texture. **No OpenGL context, no window, no interop, no thread affinity** -
so windowless children, services and headless sessions are fine, which is what the live gates here
demonstrate (a windowless Go test binary reads 4K at 58.6 fps).

**Genuinely unavailable (refuses loudly, returns no frames):**

| environment | behaviour | note |
|---|---|---|
| CPU / memory-share sender (`share == 0`), DX9 sender | `-1`, no frames | **The zero-copy path cannot serve these either** - it needs the same handle. So this is not a case where the readback is a fallback for zero-copy; NEITHER path works, and that is worth knowing before the flip. |
| No D3D11 hardware adapter (software-only / no GPU) | receiver open fails with a reason | The old GL path was equally dead here. |
| Torn registry geometry | `-1` until coherent | Pre-existing OOM-fix behaviour, unchanged. |
| Sender on another GPU | **now works** | See below - it would have been a new failure mode. |

**One gap this directive made me close.** The old GL path let Spout's interop hide which adapter owned
the sender's texture. A plain D3D11 device on the *default* adapter cannot open a texture created on
another GPU, so on a multi-GPU box the fallback would have been dead for senders on the second
adapter - silently, with the same "no frames" shape. `recv_rebind_adapter` now walks the DXGI adapters
after a failed open, rebuilds the device on the one that can open the handle, and keeps it for the
receiver's life (bounded: one device creation per adapter, only after a failure). This box has two
adapter LUIDs, which is exactly the configuration that would have hit it.

**Bottom line for the flip:** the readback is now a dependable parity oracle everywhere the zero-copy
path can work, plus it needs no GL. The only senders it cannot serve are the ones zero-copy cannot
serve either.

## Content gates (requirement 2)

Every gate that existed was a metadata gate, which is exactly how this shipped. The new ones assert
pixels:

| gate | asserts |
|---|---|
| `TestRecvContentCarriesPixels` | red top / blue bottom / **green left column** → catches a vertical flip, a horizontal mirror and an R/B swizzle in one frame. Result: `top(255,0,0) bottom(0,0,255) left(0,255,0)` |
| `TestRecvContentStaysNonZero` | 30 **consecutive** frames carry content (one lucky frame is not a pass). 30/30, 0 blank |
| `TestSpout4KCaptureSoak` | now samples a mid-frame pixel: **469 frames, 0 blank, 58.6 fps**, pool flat at 31 MB live / 63 MB idle / 2 buffers. It previously counted frames happily straight through the outage |
| `TestRecvDiag` | the canary instrument, opt-in (`RAVE_SPOUT_RECVDIAG=1`) |

All live tests use **per-attempt unique sender names** — a reused name hands the reader the previous
publisher's dead texture, which produces a blank frame with zero errors, i.e. it would mask the very
bug under test (my own inc-4 finding).

`TestRecvDiag` is opt-in because it deliberately calls the misaligned window and **access-violates**
once a sender is live (0xc0000005, observed). The shipping code never made that call in that order,
which is why the field symptom was black video and not a crash — the deployed peer is not at risk of
an AV from this.

## Answers to the open questions

### 3. The drops are the per-route FPS CAP, working as designed

Superseded twice - first by me (I guessed repeated keyframe resync), then by the encoder-crash
reading. The f0be160 per-route telemetry settles it, and the mechanism is in the code:

`mediaroute`'s shared capture runs ONE readback per sender, fanned out to N routes, and its rate is
`rateOf(subs)` = **the MAXIMUM cap of all attached routes** (`capture.go`, and 0 = uncapped wins).
Each route then re-applies its OWN cap in `spoutSource.Next` via `minGap`, discarding over-budget
frames "before any encode/crypto cost" - that is what the comment there has always said. So on a
**two-route run** where the other route is uncapped or capped higher, the shared capture runs at the
higher rate and the 4K route drops the difference.

The field numbers are that identity, exactly: `fps 30.5-33.3` delivered `+ ~27/s dropped` ≈ 57-60 =
one 60 fps source. Half arriving and half discarded is not a mode/handshake mismatch and not a
fingerprint of the black pixels - it is a 60 fps capture feeding a 30 fps-capped route.

Consistent with everything else reported: `busyDrops:0` (no saturation) and `encFails:0` (no encode
errors) precisely because nothing is failing - the frames are refused upstream of the encoder, on
purpose, by policy.

Caveat: I cannot see the peer's config, so *which* route holds which cap is inferred. The mechanism
is certain from the code; the specific pairing is not. `ctl` showing each route's `maxFps` alongside
the shared `captureFps` (already logged once at attach as "capture shared") would confirm it in
seconds.

**My share of the confusion.** In increment 2 I wired `spoutSource`'s cap-drops into
`PipelineStats.Dropped` via `InnerDrops`, which folds *intentional rate-limiting* into the same
number as *real loss*. That is what makes a healthy capped route look like it is haemorrhaging
frames, and it is why this got read as encoder crash-recovery. The fix is to count them apart -
`RateCapped` next to `Dropped`, rendered as "rate-capped N" - but the one-line junction is
`mediapipe/mf_bridge.go`'s `PipeStats`, inside the current fence, so I have not half-wired it.
It is a small, self-contained change for whoever holds that file.

### 4. The already-deployed peer must update

**Note:** SUS being a single-adapter AMD iGPU box that is *also* black confirms this is
vendor- and adapter-independent, which matches a header/DLL slot mismatch (identical binary on both
machines) and rules out cross-adapter as a factor. It does not change the fix or the verdict below.

**The peer must update.** The break is in the CAPTURE side — the peer's `rave_spout_recv` reading
OBS's sender — and it runs `nightly-2b5babf`, which predates this fix. Nothing on our receiving end
can repair a stream that is black before it is encoded. Our side of the fix matters for the reverse
direction (when this box captures a Spout source) and for any route that republishes.

### 5. `RAVE_MATE_ZIGMEDIA_CAPTURE=1` on the sender — yes, it bypasses this, with caveats

**Verdict: it does bypass the bug, and the evidence is direct.** The zero-copy capture path never
calls `ReceiveImage`. It resolves the sender's share handle through `GetSenderInfo` (which aligns)
and hands it to the encoder child, which reads the texture with its own D3D11 device. Evidence on
this box: inc-1/3/4's live gates decode the probe pattern through that path, and your own texprobe
read content from the live republished sender (`capFlags=0x5`, band means 77.7/79.6/81.7) while the
CPU readback of the same sender was all zeros. Two readers, same texture, opposite results — that is
the bug isolated to the one call, and the zero-copy path on the good side of it.

Before flipping it on the user's second PC, four things must hold — and three are checkable from
`ctl perf` in about a minute:

1. **It must actually come up zero-copy.** If it downgrades, it falls back to the *broken* readback
   and the picture is black again — silently. Require `zeroCopy=1` and `capFrames` rising.
2. **Adapter affinity.** If OBS's sender lives on a different GPU than the encode device,
   `OpenSharedResource` refuses and it downgrades (R7). That rig has two adapter LUIDs, so this is a
   live possibility. `zigAffinity` fixes it but is also default-OFF, so it would need flipping too.
3. **Spout sources only.** It does nothing for a webcam route (no shared texture).
4. **`hello.ver >= 2`** — satisfied by nightly-2b5babf.

So: a legitimate interim mitigation for a Spout route, **not** a blind flag flip — verify
`zeroCopy=1` immediately after enabling, and treat a downgrade warning as "still black".

## Proving the fix live with `bytesPerFrame`

`bytesPerFrame` is a good log-side oracle and it agrees with my measurements: the black 4K route's
**255 bytes/frame** is the same "encoding nothing" class as increment 3's M3 numbers (a STATIC black
720p sender encodes to **49 bytes/AU**; real moving content at the same geometry, 184). A frame with
no content costs almost nothing to encode, whatever its resolution.

Predictions after the peer updates, all falsifiable:

- **`bytesPerFrame` jumps by two orders of magnitude, not one.** At 4K30 on the default 20 Mbps route
  budget that is ~83,000 bytes/frame; even a heavily static OBS scene will sit in the tens of
  thousands. The useful threshold is not "thousands" but **anything sustained above ~1,000 = real
  content; ~255 is the noise floor**. (The webcam route's 3,169 is a smaller frame at a lower
  budget - a fine sanity reference, not the target for 4K.)
- **`kbps` rises from 62-68 to the negotiated budget.**
- **`fps` and `dropped` should NOT change** - they are the FPS cap (question 3), not the bug. If
  `bytesPerFrame` rises while `dropped` stays at ~27/s, that confirms both diagnoses at once. If
  `dropped` also collapses, then something else was wrong too and I want to know.

I cannot run that route from here, and the stage where I *can* measure is one earlier and stronger:
`TestRecvContentCarriesPixels` asserts the exact pixel values (`top(255,0,0) bottom(0,0,255)
left(0,255,0)`), which no bitrate proxy can be fooled about. An end-to-end AU measurement over the
readback path would have to live in `internal/mfenc`, inside the fence.

## Gate results (verbatim)

```
gofmt -l .                                                              (clean)
GOWORK=off go vet ./...                                                 (clean)
GOWORK=off go vet -tags spout ./...                                     (clean)
GOWORK=off go test ./...                                                all ok
GOWORK=off go build -tags "spout vr zigdsp zigui zigvr encembed" ./...   OK
bash scripts/build-zig.sh                  rave-mate-enc built + embed-staged (0.16.0)
go test -tags spout ./internal/videoshare                               PASS
  TestRecvContentCarriesPixels   top(255,0,0) bottom(0,0,255) left(0,255,0)
  TestRecvContentStaysNonZero    30 frames with content, 0 blank
  TestSpout4KCaptureSoak         469 frames in 8s (58.6 fps, 0 blank) peakLive=31MB
go test -tags spout ./internal/mfenc -run 'TestZeroCopyLive|TestDecodeLive|TestFlipLive|TestInc3'  PASS
```

## Follow-ups (not done here)

1. **Re-vendor a matching header.** The real defect is the vendoring: a header that does not match
   the DLL. Fixing it would repair `GetHandle`, `GetSenderFrame`, `GetSenderWidth/Height/Format` and
   re-open inc-3's frame-new gating. It needs a network fetch + a new SHA pin + the 7-day soak, so
   it is a supply-chain change for a human to sign off — `SUPPLY_CHAIN.md`, not a quiet edit. Until
   then the shim's rule is: **nothing past `SendImage` except the registry queries (111-114).**
2. Render `JB.Grows/Decays/Dups` (collected, invisible) so question 3 is answerable from the panel.
3. The SUS webcam route (`mfenc: encode timeout` + child `0xc0000005`) is **not** this bug: that
   path carries real DirectShow pixels, so it never touches `rave_spout_recv`. It belongs to the
   encoder-AV P0.
4. `videoshare/sender_spout_test.go:29` still hard-fails without `SpoutLibrary.dll` where every
   other Spout test skips cleanly (pre-existing, reproducible on `development`).

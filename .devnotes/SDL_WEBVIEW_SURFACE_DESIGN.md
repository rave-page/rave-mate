# Native render surface inside the webview UI — design

Status: **DESIGN ONLY, nothing implemented.** Replaces nothing yet.

Scope: host a real GPU render surface (editor video preview w/ realtime fx, waveforms, future
visualisers) inside the rave-mate webview window, instead of encoding frames and streaming them
into an HTML `<video>` via MSE.

---

## 1. Recommendation

**WebView2 Visual hosting (composition mode) + a DirectComposition visual tree owned by the Zig
shell child; our surface visual sits BELOW the webview visual, which paints a transparent hole.
Frames arrive from a supervised producer child as a shared D3D11 texture / DComp surface handle.
SDL3 is NOT the presenter on Windows — raw D3D11 + DComp is.**

Trade-off, plainly: the DComp route is the only one where HTML still draws **over** the surface —
tooltips (`.tt` → `#__ttlayer`), `.ss-panel`, `#__modal` all must, and a child-HWND ("airspace")
surface makes that impossible. The price is real: `rave-shell.exe` must forward every spatial
input itself, `--disable-gpu-compositing` (today's good-neighbour default) probably has to go, and
`ctl screenshot`'s `PrintWindow` path needs a composite rework. Those three are the whole risk.

If the P0 spike says input parity or `PrintWindow` can't be recovered → **stay on MSE**. MSE is not
deleted by any phase here.

**Owner directive (2026-08-13):** SDL3 not being the Windows presenter is accepted. Pick the most
performant, most direct path per platform for demanding realtime 2D/3D — the requirement is that
the SYSTEM works cross-platform, not that one library does. So: one `Surface` seam in Go, one
per-platform presenter behind it (win = D3D11 + DComp; mac = WKWebView + CAMetalLayer sublayer;
linux = WebKitGTK + GL/Wayland subsurface — both [UNVERIFIED], designed for, not built yet). SDL3
stays in scope where it is genuinely the best tool (input/HID, device enumeration, and SDL_GPU as
the *renderer* feeding our own presenter) rather than as the window/present layer.

**Owner directive #2 (2026-08-13, HARD REQUIREMENT): Zig owns the GUI.** The surface seam - and
the frontend/GUI layer generally - lives in Zig (with C/C++ where a library demands it). Go keeps
computational/service logic: workers, media producers, protocols, storage, state. Concretely for
this design: there is NO `internal/webui/surface.go` owning surfaces. The registry, lifetime,
visual tree, rect tracking and presenter are all `native/zigui/src/shell/`. Go never holds a
surface object, never learns a rect, and does not command open/close - the child derives that from
the DOM it is already hosting.

---

## 2. Ground truth (read, not assumed)

| Fact | Where |
|---|---|
| Window lives in a **child process**, not the daemon | `native/zigui/src/shell/main.zig`, `internal/webui/shell_proc.go` |
| Top-level HWND + `rave_shell_widget` WS_CHILD; WebView2 controller parents to the **widget** | `winshell.zig:562-578`, `:715` |
| Controller is `CreateCoreWebView2Controller` = **windowed hosting** today | `winshell.zig:221`, `:715` |
| GPU compositing **off by default** (`--disable-gpu --disable-gpu-compositing`) | `winshell.zig:582-588`, `webviewAllowGPU` in `shell.go:12` |
| Daemon↔child = newline-JSON PSH1 over stdio, 2 lanes (ordered/direct) | `shell_proc_proto.go` |
| Page↔child = `window.chrome.webview.postMessage({m:'a'\|'r'})` binding shim | `main.zig:58-61`, `winshell.zig:819` |
| Go owns 100% of render; JS is transport + introspection only | `shell.go:62-70` |
| Eval flusher **holds** during size-move; child never buffers | `ui.go:889-926`, `winshell.zig:913-930` |
| ctl screenshot = **child-side** `PrintWindow(PW_RENDERFULLCONTENT)` → PNG on disk | `winshell.zig:1017-1033`, `shell_proc.go:321` |
| Current preview path: ffmpeg decode → `rave-mate-vfx --pipe` → ffmpeg x264 frag-MP4 → `/ms/` tail → `__mst` MSE | `editor_video_stream.go:1-9,270-278`, `shell.go:917-983`, `mediahttp.go:404` |
| zigvfx renders on GPU (WGL+FBO) but **`glReadPixels` back to CPU every frame**, RGBA over stdout | `native/zigvfx/src/isf.zig:3,950`, `main.zig:220-249` |
| Repo already does cross-process **D3D11 shared textures + `IDXGIKeyedMutex`** | `native/zigenc/src/mf.zig:64-65,374-375`, `cap.zig:17,172`, `dec.zig:321`, `internal/videoshare/recvpoll.go:8` |
| Per-stage content oracle + on-demand PNG already exist | `internal/framedebug`, `ctl frame-shot` |
| Webview shell is **Windows-only**; every other target falls back to Fyne | `shell_nocgo.go` (`shellAvailable = false`), `main.zig:76` (`@compileError`) |

---

## 3. Options evaluated

| | 1. Airspace (child HWND) | 2. DComp visual hosting | 3. SDL3 as presenter |
|---|---|---|---|
| HTML draws over surface | **No** (fatal) | Yes | n/a |
| Rect sync artefacts | flicker/lag on scroll+resize | none (one compositor) | — |
| Input rework | none | **large** (all spatial input) | — |
| DPI | manual per-monitor | rasterization-scale API | — |
| `PrintWindow` | likely black on GPU surface | likely black | — |
| Cross-process | `SetParent` attaches input queues (hang source) | shared surface handle, no HWND coupling | — |
| Verdict | **rejected** | **chosen** | **not on Windows** |

### 3.1 Airspace — why rejected

The WPF rule is the same rule here: *"your HWND has a single presentation surface … and that
single presentation surface is always below every child HWND"*
([MS, WPF Interoperation: "Airspace"](https://learn.microsoft.com/en-us/previous-versions/dotnet/netframework-3.0/aa970688(v=vs.85))).
A child HWND over the webview is **always on top of all web content**. That kills, in the exact
pane the preview lives in:

- `.tt` tooltip cards portalled to `#__ttlayer` (`shell.go:522-563`)
- `.ss-panel` smart-select panels promoted to viewport-fixed (`shell.go:664-699`)
- `#__modal` (`ui.go:770`)
- the `[data-actsize]` resize grip and `[data-actpos]` overlays that sit **on** the preview

There is no workaround short of "never overlap", which a full-pane preview cannot honour.
Secondary: MS names *"attached window input queues"* causing cross-process hangs as a thing
Window-to-Visual hosting exists to fix
([windowed-vs-visual-hosting](https://learn.microsoft.com/en-us/microsoft-edge/webview2/concepts/windowed-vs-visual-hosting)).

### 3.2 DComp visual hosting — what it requires

Verified from
[windowed-vs-visual-hosting](https://learn.microsoft.com/en-us/microsoft-edge/webview2/concepts/windowed-vs-visual-hosting)
and
[ICoreWebView2CompositionController](https://learn.microsoft.com/en-us/microsoft-edge/webview2/reference/win32/icorewebview2compositioncontroller):

- `CreateCoreWebView2CompositionController(parentHwnd, handler)` instead of `…Controller`.
- `put_RootVisualTarget(v)` where `v` is an **`IDCompositionVisual`** or a
  `Windows::UI::Composition::ContainerVisual`. **Not** WinAppSDK `Microsoft.UI.Composition`.
  "WebView will connect its visual tree to the provided visual before returning from the property
  setter. The app needs to commit on its device."
- Sizing/visibility/focus still via `ICoreWebView2Controller` (`put_Bounds`, `put_IsVisible`,
  `MoveFocus`) — i.e. `boundsToClient()` survives.
- **The app forwards all spatial input**: "no spatial inputs (such as mouse, touch, or pen) are
  sent to the WebView2 control, unless the app manages such input… the app is responsible for
  forwarding this spatial input… and any necessary transformation of input positions into the
  WebView2's coordinate space." → `SendMouseInput` / `SendPointerInput`, plus `SetCapture` /
  `ReleaseCapture` on drags and explicit `…MOUSE_EVENT_KIND_LEAVE`.
- Cursor is the app's: `add_CursorChanged` → `get_Cursor` / `get_SystemCursorId` → `SetCursor` /
  `SetClassLongPtr`.
- DPI: undo default visual scaling, use the Rasterization Scale APIs, else blurry text.
- **Keyboard/IME are NOT listed as the app's responsibility** — only *spatial* input is. Keyboard
  continues to flow via the parent HWND. (Materially de-risks the rework.)
- "All hosting modes are supported wherever WebView2 is supported." Introduced 1.0.774.44; our
  loader floor is already 1150 (`winshell.zig:695-704`). No new floor.

Transparent hole, verified from
[ICoreWebView2Controller2](https://learn.microsoft.com/en-us/microsoft-edge/webview2/reference/win32/icorewebview2controller2):
`put_DefaultBackgroundColor` with `A = 0` → "WebView will render hosting app content as the
background." Only alpha **0 or 255**; anything else = `E_INVALIDARG`. Known white-flicker at
startup unless set via `WEBVIEW2_DEFAULT_BACKGROUND_COLOR` env var instead — **use the env var**
(the child already sets `WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS` the same way).

Cross-process frames, verified from
[IDCompositionDevice::CreateSurfaceFromHandle](https://learn.microsoft.com/en-us/windows/win32/api/dcomp/nf-dcomp-idcompositiondevice-createsurfacefromhandle):
wraps a handle from `DCompositionCreateSurfaceHandle` — "enables an application to use a **shared**
composition surface in a composition tree." Windows 8+. Alternative: producer creates a composition
swapchain (`IDXGIFactory2::CreateSwapChainForComposition`) and the shell binds it via
`IDCompositionVisual::SetContent`.

### 3.3 SDL3 — can it do this? Plainly: no, not the presenting half

**Can SDL3 adopt an existing HWND?** Yes, verified.
[`SDL_CreateWindowWithProperties`](https://wiki.libsdl.org/SDL3/SDL_CreateWindowWithProperties)
exposes `SDL_PROP_WINDOW_CREATE_WIN32_HWND_POINTER` — "allows wrapping an existing window".
`SDL_WINDOW_EXTERNAL` is documented verbatim as *"window not created by SDL"*
([SDL_WindowFlags](https://wiki.libsdl.org/SDL3/SDL_WindowFlags)). This **closes the
`[UNVERIFIED]` gap at `SDL3_KNOWLEDGEBASE.md:131` and the open item at `:457`** — someone should
fold that back into the KB.

**Can SDL present into a DComp visual?** **No.** SDL always builds its swapchain from the window's
HWND. [`SDL_GetRendererProperties`](https://wiki.libsdl.org/SDL3/SDL_GetRendererProperties) hands
back `SDL_PROP_RENDERER_D3D11_DEVICE_POINTER` and `SDL_PROP_RENDERER_D3D11_SWAPCHAIN_POINTER`
(`IDXGISwapChain1`) — but that is the *HWND* swapchain SDL made; there is no public way to make
SDL create a **composition** swapchain, and `SDL_GPU`'s swapchain likewise comes from
`SDL_ClaimWindowForGPUDevice`. `SDL_GPUSwapchainComposition` is colourspace/HDR, not
DirectComposition.

**Consequence:** SDL3 would only serve the rejected airspace option. **What does the job is raw
D3D11 + DComp in the existing Zig shell** — and this repo already carries the exact COM surface
needed (`native/zigenc/src/mf.zig`: `ID3D11Device`, `IDXGIKeyedMutex`, shared-handle open). Lift
those decls into a shared Zig module rather than writing a second binding.

[UNVERIFIED] whether `SDL_GetTextureProperties` exposes an `ID3D11Texture2D` for an
`SDL_TEXTUREACCESS_TARGET` texture (would enable "SDL draws, DComp presents"). Not on any page
fetched. Worth 20 minutes before P5 if we ever want SDL's 2D renderer for waveforms.

---

## 4. Architecture

### 4.1 Process map

```
daemon (rave-mate.exe)                     Go: renders HTML, owns surface DECLARATION
  │  PSH1 stdio  (+ new `surface` events)
  ▼
rave-shell.exe (Zig child)                 owns HWND + DComp tree + WebView2 composition controller
  │  ▲ binding shim {m:'s'} rect reports   (page → child DIRECT, no daemon hop)
  │  └── page (WebView2, transparent bg, hole divs)
  ▼  shared surface handle (DComp) / shared D3D11 texture + keyed mutex
producer child (rave-mate-vfx, ffmpeg)     GPU work stays out of daemon AND out of the UI child
```

Rule kept: resource-bearing/GPU frame production runs in a **supervised producer child**. The
shell child only *composites* — it binds a handle and sets a transform. It does not decode, encode
or run plugin code.

### 4.2 Visual tree (in `rave-shell.exe`)

```
IDCompositionTarget(hwnd, topmost=TRUE)
└── rootVisual
    ├── surfaceLayer                 ← z BELOW
    │   ├── visual[id="editor-preview"]  content = shared surface/swapchain, Offset+Transform = DOM rect
    │   └── visual[id=…]                 (n surfaces, capped)
    └── webViewVisual                ← z ABOVE, RootVisualTarget, DefaultBackgroundColor A=0
```

Everything in the page composites over every surface. Tooltips, modals, smart-select, the resize
grip: unchanged, no special-casing.

### 4.3 Declaring a surface — data in the DOM, owned by the child

Per directive #2 the child OWNS surfaces. Go's only involvement is that its render state already
emits the hole element, exactly like `data-mse` / `data-msestream` today:

```html
<div id="surf-editor-preview" data-surface="editor-preview" data-surface-kind="vfx"
     data-surface-w="1080" data-surface-h="1920" style="background:transparent"></div>
```

`native/zigui/src/shell/surfaces.zig` owns everything else:
- **Discovery**: the runtime scans `[data-surface]` on load and after every `__patch` (the
  `__mstScan` hook shape, `shell.go:187`) and reports the CURRENT set to the child. Appearance =
  open, disappearance = close. No open/close command crosses from the daemon.
- **Registry**: id → {visual, content, rect, dpr, visible, producer handle}. Capped (8).
- **Visual tree + presenter**: created, transformed and destroyed here. Windows = D3D11 + DComp.
- **Lifetime**: tab switch, modal, `__patch` that drops the element, window close - all resolve to
  "the element is gone" and the child tears the visual down. Nothing to leak daemon-side.

Go's remaining job is the *producer*, which is computational and stays where the repo's isolation
rule puts it: a supervised worker child (`u.svc.Workers.RunStream`), started/stopped by the same
code that does it today (`edvPrevStart`/`edvPrevStop`).

**Handle exchange (preferred): out-of-band, no daemon hop.** The producer publishes its shared
texture under a named convention (`Global
ave-surface-<id>` + a keyed mutex - the pattern
`native/zigenc/src/mf.zig` already uses for Spout-class sharing); the child opens it by name when
the surface appears. Fallback if naming proves unworkable: a PSH1 `surface {op:"attach", id,
handle}` message - the ONLY case where the daemon touches a surface, and only as a courier.

### 4.4 Rect / visibility / z sync — page → child, direct

New runtime-JS block in `shell.go` (transport only, no business logic — house rule):

- `ResizeObserver` + `IntersectionObserver` on every `[data-surface]`.
- rAF-coalesced report on scroll/resize/`__patch` (reuse the existing `MutationObserver` at
  `shell.go:657` and the `__ttrepin`/`__ssplace` rAF-debounce shape).
- Payload → binding shim, **new message kind** `{m:'s', v:[{id,x,y,w,h,vis,dpr}]}`, handled in
  `winshell.zig msgInvoke` and posted to the UI thread as a new `UiMsg.surface` variant.

Why never via the daemon: a rect is pure view geometry, and per directive #2 view geometry is not
Go's to hold. Routing it through `onAction` → act worker → eval queue would also put it behind the
**size-move hold** (`ui.go:918`) and the ordered lane's coalescing — the surface would lag the DOM
by frames during exactly the drag/scroll where it must not.

Bounds: report array capped at 8 surfaces, drop-newest past that; report rate rAF-capped; identical
consecutive rect = not sent.

### 4.5 Frames into the surface

Producer (`rave-mate-vfx --present <spec>`) owns a double-buffered shared D3D11 texture + keyed
mutex — the pattern already proven in `native/zigenc/src/cap.zig:17,172` and `dec.zig:321`.

- Ring depth **2** (present/acquire), cap stated in frames AND bytes, policy **newest-wins,
  drop-oldest**. A producer that outruns the compositor drops; it never accumulates.
- Handle travels once, at surface open, over PSH1 (`surface {op:"bind", id, handle, w, h, fmt}`),
  daemon relaying producer→shell. Pixels never cross a pipe.
- Shell child: `CreateSurfaceFromHandle` (or `SetContent(swapchain)`), `Commit()`, done. No
  per-frame daemon involvement at all.
- Producer death → shell holds the last committed frame, daemon logs + restarts via the existing
  worker supervision; surface visual goes hidden after a stated grace.

Phase 1 of the producer keeps zigvfx's existing `glReadPixels` and uploads the RGBA
(`isf.zig:950`). Phase 2 removes the readback. Either way the **encode + fragment + HTTP + MSE**
stages disappear, which is where the latency lives.

### 4.6 Death / cleanup

| Trigger | Behaviour |
|---|---|
| Tab switch / `__patch` removes the hole | observer reports `vis:false` (or element gone) → child hides visual; daemon's next render drops the spec → `surface{close}` + producer ctx cancel |
| Modal opens over it | nothing — z-order handles it; surface keeps running (or daemon may pause the producer, its call) |
| Window hidden to tray / minimized | `procWin` already reports it; child sets `IsVisible=false` on the surface layer; daemon pauses producers (governor `UIAnimAllowed` already gates this class of work) |
| Shell child crash/restart | featurehost restarts; `onReattach` (`shell_proc.go:246-266`) rebuilds the page; daemon re-sends every open `surface{open,bind}` from state — same "UI is derivable from state" contract |
| Producer crash | shell keeps last frame → hides after grace; daemon restarts it |
| `quit` | `wm_destroy` releases visuals → target → device before `PostQuitMessage`; producers die with their job objects (`sysexec.AssignToJob`) |
| Stale-rect self-heal | no report for >2 s while the surface is open → hide (mirrors the size-move self-heal at `winshell.zig:906`) |

---

## 5. What this replaces — and what it does NOT

**Replaces (for the editor preview only, behind a flag):**
`edvPrevStart`'s ffmpeg-x264 encode leg → growing frag-MP4 → `mp4frag` wait-loop → `/ms/` token →
`__mst` MSE feeder → `<video>`. i.e. `editor_video_stream.go:270-330` + `shell.go:917-983` +
`mediahttp.go`'s `/ms/` route stop being on the preview path.

**Does NOT replace / must keep working:**

| Kept | Why |
|---|---|
| **The whole MSE path, as the shipped default and permanent fallback** | flag `features.ui.surfaces=false` (default) → today's code path verbatim, byte-for-byte. Not deleted by any phase. |
| `data-mse` VOD feeder (`shell.go:758-916`) | that's OBS recording playback, a different problem (mfra scan), untouched |
| `/ms/` and all of `mediahttp.go` | remote-library mirror, images, audio, and the MSE fallback still need it |
| `virtualShell` / headless mirror sessions | no window, no DComp — surfaces are a no-op there, MSE path stays |
| `<video>` transport JS (`__vplay`/`__vpause`/`__vrate`, `shell.go:94-142`) | still the transport for the fallback and for audio hosts |
| Fyne renderer, `shell_cgo.go` in-proc window | unaffected; surfaces are zig-shell + visual-hosting only |
| Spout / medialink / mfenc | different pipeline, untouched |

Fallback must be **automatic**, not just configurable: composition controller creation fails,
`RootVisualTarget` fails, or no `surface{bind}` inside N seconds → child emits `surface{op:"fail"}`,
daemon re-renders the `<video>` variant. Same shape as `__mseFail` → plain `src` (`shell.go:773`).

---

## 6. Phased plan

Each phase = one commit, `go build ./... && go vet ./... && go test ./...` clean, plus the stated
verification. Nothing after P0 starts until P0's gate passes.

### P0 — Spike, throwaway, no product code
Standalone Zig exe: window + DComp tree + solid-magenta visual + composition-hosted WebView2 with
`A=0` background + a page with a transparent hole and a tooltip that overlaps it.
**Verify:** magenta visible through the hole; tooltip draws **over** magenta; click/hover/wheel/
right-click/drag all reach the page via `SendMouseInput`; text cursor changes over text; type into
an input.
**Also answers, and this is the point of P0:** does `PrintWindow(PW_RENDERFULLCONTENT)` capture (a)
the web layer, (b) the DComp visual? Record the answer in this file.
**Gate:** all of the above, or → stop, stay MSE.

#### P0 result (2026-08-13) — **GATE PASSED, proceed to P1**

Ran on this machine, Windows 11 26200, Evergreen WebView2 **151.0.4129.78**, D3D11 hardware
device, 96 dpi. Throwaway Zig exe (window + DComp tree + magenta composition swapchain +
`CreateCoreWebView2CompositionController` + scripted `SendMouseInput` + capture probes). Every
COM HRESULT below was **0x00000000**.

| Goal | Result | Evidence |
|---|---|---|
| Composition controller + `put_RootVisualTarget` | PASS | both S_OK; page renders inside our visual |
| `put_DefaultBackgroundColor` A=0 → hole | PASS | S_OK; magenta visible through the transparent pane |
| HTML draws OVER the surface | PASS | z-9999 tooltip covers magenta; a 50%-alpha div **blends** with it |
| Hit-testing over the hole | PASS | right-click + 341px drag both targeted `#hole`, not the surface |
| `SendMouseInput` click / wheel / right-click / drag | PASS | page echoed every one back over the binding |
| Cursor via `add_CursorChanged`/`get_SystemCursorId` | PASS | 32513 (IDC_IBEAM) over the text input, 32512 elsewhere |
| Keyboard (NOT forwarded by us) | PASS | real `SendInput` typed `RAVE` into the input — §3.2 confirmed |
| **`PrintWindow(PW_RENDERFULLCONTENT)`** | **PASS** | captures **web layer AND DComp visual**, pixel-identical to the on-screen grab (53.8% magenta both) |
| …with the window fully **occluded** (`HWND_BOTTOM`) | **PASS** | screen grab 0% magenta (really covered), PrintWindow still 53.8% — `screenshot-all` is safe |
| `--disable-gpu --disable-gpu-compositing` | **COMPATIBLE** | identical pass + identical pixels; verified the flags reached the browser (`--use-gl=disabled` on the gpu-process, `--disable-gpu-compositing` on the renderer) |

Negative controls: `PrintWindow(flags=0)` → blank white; `BitBlt` from `GetDC(hwnd)` → 100% white.
`PW_RENDERFULLCONTENT` is **mandatory** — which is already what `captureHWND` uses.

**Risks closed:** R1 (PrintWindow) — dead, no composite rework needed, **P6 loses its reason to
exist**. R2 (GPU compositing) — dead, the good-neighbour `--disable-gpu*` default can stay; no
cost to a live encode to measure. §9 Q1 and Q2 answered.

**Three gotchas found by execution — anyone writing P1/P2 must honour them:**

1. **`AddVisual` insertAbove is INVERTED when `referenceVisual` is NULL.** MS docs, Remarks:
   *"If insertAbove is TRUE, the new child visual is above no sibling, therefore it is rendered
   below all of its siblings."* So the surface layer is added with **TRUE** and the webview visual
   with **FALSE**. Getting this backwards paints magenta over the whole window and looks exactly
   like "the web layer never rendered".
2. **MSVC reverses same-name virtual overload groups in the vtable.** `IDCompositionVisual`'s
   `SetOffsetX(float)` is at slot **4**, `SetOffsetX(IDCompositionAnimation*)` at slot 3 (probed:
   slot3(NULL)=0x80070057, slot4(NULL)=S_OK). Same for `SetOffsetY` (5/6), `SetTransform` (7/8),
   `SetClip` (13/14) — declaration order does NOT hold. `SetContent`(15) / `AddVisual`(16) sit
   after every overload group, so they are safe either way. dcomp.h has no C vtbl to copy.
3. **`put_Bounds` reaches the renderer asynchronously.** `get_Bounds` read back correct
   immediately, but the page's first layout still used a stale viewport (1281px vs the 1084px
   bounds) and a `resize` event followed. §4.4's rect reporter must therefore be
   ResizeObserver/`resize`-driven, never a one-shot measurement at document-ready.

Untested here, still open for P1: non-96-dpi rasterization scale (R8), multi-monitor moves, UIA
accessibility (§9 Q3), long-run stability.

Spike lived in the agent scratchpad (`…/scratchpad/p0-dcomp/`), NOT in this repo — throwaway by
design, per the phase definition.

### P1 — Visual hosting behind a flag, zero surfaces
`features.ui.shellHosting = "windowed"|"visual"` (default `windowed`). `winshell.zig` gains the
composition path alongside the existing one; identical UI, identical everything.
**Verify:** `ctl screenshot-all <dir>` full sweep + eyeball `report.txt` for ⚠OVERFLOW;
`GOWORK=off go test -tags zigui ./internal/webui -run TestZig` (all goldens); `ctl click/tap/type/
read/set/snapshot` parity across tabs; drag + resize + maximize + DPI change; modal, smart-select,
tooltip pin, splitter drag, mouse-back/forward (X1/X2); IME/non-ASCII typing.
**This is the soak phase.** Do not stack P2 on an unsoaked P1.

### P2 — Surface manager + solid-colour test surface
Go `internal/webui/surface.go` + `data-surface` in the component layer + PSH1 `surface` events;
Zig `native/zigui/src/shell/surfaces.zig`; runtime-JS rect reporter (`{m:'s'}`).
Content = a solid colour picked in the child. No producer, no frames.
**Verify:** `ctl` opens a test card on Live; `ctl screenshot` shows the colour under the hole;
scroll a long pane → surface tracks with no visible lag; resize the window; open a modal over it;
switch tab → visual gone; `ctl quit` → clean exit, no leaked visual.

### P3 — Frame ingest via shared texture, testcard producer
Lift D3D11/DXGI decls out of `native/zigenc/src/mf.zig` into a shared Zig module. Producer =
`internal/testcard` (already deterministic, in-picture seq+timestamp).
**Verify:** `internal/framedebug` content oracle proves frames **change** (not just arrive — the
4K-frozen-picture lesson: fps 58.5 with one bit-identical frame for 48 min); testcard's own
gap/freeze/drift stats clean over 10 min; RSS flat; bounded-queue caps asserted by a test.

### P4 — Wire zigvfx, editor preview on the flag
`rave-mate-vfx --present` (keeps `glReadPixels`, uploads to the shared texture). `edvPrevStart`
branches on the flag.
**Verify:** side-by-side vs MSE at the same `prevH`; measure click→pixel latency both ways and
record it here; flag off → `edvPrevStart` byte-identical behaviour; seek / param edit / stall /
pause-reap all still correct; `ctl frame-shot` on the producer.

### P5 — Zero-copy
Drop `glReadPixels`: WGL↔D3D interop, or move the ISF backend to an ANGLE/D3D11 context so the FBO
target **is** the shared texture.
**Verify:** framedebug unchanged; measured CPU drop; readback path stays as the fallback (repo's
existing zero-copy-default discipline: readback = fallback, not deleted).

### P6 — ~~ctl screenshot composite~~ (VOIDED by P0), then more surfaces
**The composite is not needed: P0 proved `PrintWindow(PW_RENDERFULLCONTENT)` already captures both
layers, even while occluded.** Keep the rest of the phase (more surfaces); build the composite only
if a future WebView2/OS build regresses that. Original plan, retained for that case:
child-side composite via `ICoreWebView2::CapturePreview` for the web layer + producer `FrameShot`
for the surface rect (both primitives already exist — the vtable slot is at `winshell.zig:290`, the
frame-shot pattern in `internal/framedebug`). Then waveforms, then visualisers.
**Verify:** goldens regenerate cleanly; `screenshot-all` sweep matches the P1 baseline outside the
surface rect.

---

## 7. Risks + kill criteria

| # | Risk | Kill criterion |
|---|---|---|
| R1 | **`PrintWindow` returns black/empty for DComp content** → `ctl screenshot-all`, the repo's mandatory verification workflow, dies | P0 shows no capture path (neither `PrintWindow` nor `CapturePreview`+FrameShot composite) → **abandon, stay MSE.** Non-negotiable: an unverifiable UI is not shippable here. |
| R2 | **GPU compositing must be ON.** Today's default is `--disable-gpu --disable-gpu-compositing` for good-neighbour reasons (`winshell.zig:582`). [UNVERIFIED] whether composition hosting works with it off — almost certainly not. | If forcing GPU compositing measurably costs a live OBS/NVENC encode (governor `Streaming` scenario), the surface flag must hard-default OFF while streaming, or → abandon. |
| R3 | Spatial-input rework regresses a soaked child (hover, drag-with-capture, wheel, right-click, X1/X2, `setPointerCapture` splitters) | Any P1 input regression not closed within one iteration → revert the flag; ship windowed. |
| R4 | Rect sync lags scroll/drag ≥1 frame → the airspace symptom reappears inside DComp | Visible tearing between HTML and surface at P2 → abandon. |
| R5 | WebView2 Evergreen auto-update breaks composition hosting on a user's rig | Mandatory: automatic fallback (§5) must be proven by execution — force the failure and assert the `<video>` comes back. Flag defaults OFF until soaked. |
| R6 | Second renderer sprawl | Hard rule: **no new renderer.** zigvfx is the producer; zigenc's COM decls are the D3D11 binding. If a phase needs a third, stop and re-plan. |
| R7 | Shell child now touches the GPU → a driver AV kills the UI window | Accepted: featurehost restarts + `onReattach` already rebuilds from state (`shell_proc.go:260`). But if P1 shows a restart rate above the current baseline, abandon. |
| R8 | Rasterization-scale/DPI regressions on multi-monitor mixed-DPI | Blurry text or wrong hit-testing after a monitor move that can't be fixed at P1 → abandon. |

No new third-party dependency in any phase. DComp/D3D11/DXGI are OS DLLs; WebView2 is already a
dependency; the COM decls are ours. Nothing to soak under `SUPPLY_CHAIN.md`.

---

## 8. Cross-platform reality check

**Today this is a non-issue: there is no non-Windows webview shell.** `shellAvailable = false`
off cgo-Windows (`shell_nocgo.go`), and `rave-shell` is `@compileError`-guarded to Windows
(`main.zig:76`). Every other OS runs Fyne. Recorded for when that changes:

| OS | Shape |
|---|---|
| **Windows** | As designed. DComp visual hosting. |
| **macOS** | WKWebView is an `NSView` in a layer-backed AppKit hierarchy — **no airspace problem**; a `CAMetalLayer`-backed sibling `NSView` composites by z-order natively. Simpler than Windows. SDL3 could adopt via `SDL_PROP_WINDOW_CREATE_COCOA_VIEW_POINTER` / `…COCOA_WINDOW_POINTER`. [UNVERIFIED] — property names are from the SDL3 wiki list; the AppKit compositing claim is reasoned, not fetched. |
| **Linux** | WebKitGTK. GTK4 has no native subwindows — a GPU surface means `GtkGLArea` inside the same widget tree, or a Wayland subsurface. **X11 has the classic airspace problem**; Wayland subsurfaces have a z-order but ordering vs GTK-drawn content is compositor-dependent. SDL3 offers `SDL_PROP_WINDOW_CREATE_X11_WINDOW_NUMBER` / `…WAYLAND_WL_SURFACE_POINTER`. Not designed here. |

The **surface declaration** layer (§4.3) and the producer/shared-frame contract (§4.5) are
platform-neutral by construction; only the compositing back end differs. Keep the Go side free of
DComp vocabulary so that stays true.

---

## 9. Open questions

1. **Does `PrintWindow(PW_RENDERFULLCONTENT)` capture composition-hosted WebView2?** P0 answers.
   Everything downstream depends on it.
2. **Does composition hosting work with `--disable-gpu-compositing`?** [UNVERIFIED]. If not, what
   does forcing it on cost during a live encode? Measure against the governor's `Streaming` state.
3. **Accessibility under visual hosting.** The windowed docs sell "without having to include
   features for inputs, outputs, and **accessibility**", implying visual hosting shifts some of it
   to the app. The visual-hosting section names only scaling + spatial input. [UNVERIFIED] whether
   UIA still works out of the box.
4. **`SDL_GetTextureProperties` → `ID3D11Texture2D`?** [UNVERIFIED]. Would open "SDL draws
   waveforms, DComp presents" and reuse the repo's stated SDL3 preference. Cheap to check.
5. **Where do producer frames get their timing?** MSE gave us `<video>`'s clock for free. A native
   surface needs an explicit present cadence tied to the transport playhead — reuse the `__rt`
   interpolator's model? Not designed yet.
6. **Audio.** The MSE path carried audio in the same MP4. A native video surface does not. The
   editor preview's audio needs an explicit answer (existing audio engine? a second stream?).
   **This is a gap in the current design and must be closed before P4.**
7. Surface count cap and per-surface VRAM budget for the "many waveforms" case.
8. Does `procShot`'s child-side capture need a new lane, or does the composite fit the existing
   `procEvShot` request/response?

---

## 10. Sources fetched

- https://learn.microsoft.com/en-us/microsoft-edge/webview2/concepts/windowed-vs-visual-hosting
- https://learn.microsoft.com/en-us/microsoft-edge/webview2/reference/win32/icorewebview2compositioncontroller
- https://learn.microsoft.com/en-us/microsoft-edge/webview2/reference/win32/icorewebview2controller2
- https://learn.microsoft.com/en-us/windows/win32/api/dcomp/nf-dcomp-idcompositiondevice-createsurfacefromhandle
- https://learn.microsoft.com/en-us/previous-versions/dotnet/netframework-3.0/aa970688(v=vs.85) (WPF airspace)
- https://wiki.libsdl.org/SDL3/SDL_CreateWindowWithProperties
- https://wiki.libsdl.org/SDL3/SDL_WindowFlags
- https://wiki.libsdl.org/SDL3/SDL_GetRendererProperties
- https://github.com/libsdl-org/SDL/issues/9775 (no SDL3 API for the D3D11 swapchain back buffer)
- Internal: `.devnotes/SDL3_KNOWLEDGEBASE.md`, `.devnotes/SDL3_API_CHEATSHEET.md`

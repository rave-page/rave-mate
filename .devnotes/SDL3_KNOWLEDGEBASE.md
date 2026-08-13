# SDL3 Knowledgebase (internal reference)

Source of truth: official SDL3 wiki, https://wiki.libsdl.org/SDL3/FrontPage. Every name below comes from a page listed in [Links](#links). Anything unverified is flagged **[UNVERIFIED]**.

Companion: [SDL3_API_CHEATSHEET.md](SDL3_API_CHEATSHEET.md) — grep-able name list.

---

## 1. Status, version, license

| Fact | Value | Source |
|---|---|---|
| Release status | "SDL3 has officially released and is ready for you to start using now." | FrontPage |
| Latest release at time of writing | **3.4.14**, 2026-08-03 | GitHub releases |
| Prior tags | 3.4.12 (2026-07-01), 3.4.10 (2026-05-31), 3.4.8 (2026-05-02), 3.4.6 (2026-05-01) | GitHub releases |
| License | zlib. "This license allows you to use SDL freely in any software." | FrontPage |

### Versioning (README-versions)

- Odd/even policy. **Stable = minor AND patch both even** (3.2.6, 3.4.0, 3.4.14). Odd minor or patch = development prerelease, not for stable distribution.
- Patch releases may add small ABI. Backwards-compatible, **not** forwards-compatible: built against 3.2.0 → runs on 3.2.8; reverse fails.
- Same across minor: 3.2.x binary runs on 3.4.x, not the reverse.
- Prereleases are backwards-compatible with older stable branches, **not** with each other.

Consequence for us: pin the build against the **oldest** SDL3 we intend to support, ship a >= that runtime.

Version API: `SDL_GetVersion`, `SDL_GetRevision`, macros `SDL_MAJOR_VERSION` / `SDL_MINOR_VERSION` / `SDL_MICRO_VERSION` / `SDL_REVISION` / `SDL_VERSION` / `SDL_VERSIONNUM` / `SDL_VERSION_ATLEAST` / `SDL_VERSIONNUM_MAJOR|MINOR|MICRO`.

---

## 2. SDL2 → SDL3 differences that bite (README-migration)

| Area | SDL2 | SDL3 |
|---|---|---|
| Bool | `SDL_bool`, `SDL_TRUE`/`SDL_FALSE` | removed — C99 `bool` / `true` / `false` |
| Return convention | `int`, `<0` = error | `bool`, `false` = error. `if (!SDL_Function())` |
| Scope of that rule | — | applies to camel-case `SDL_[A-Z]*` only, not `SDL_strcmp()` etc |
| Init flag | `SDL_INIT_GAMECONTROLLER` | `SDL_INIT_GAMEPAD` |
| Timer init | `SDL_INIT_TIMER` before `SDL_AddTimer()` | no longer required |
| main | SDL auto-included `SDL_main.h` | must include `<SDL3/SDL_main.h>` explicitly in the main source file |
| Renderer create | `SDL_CreateRenderer(win, int index, flags)` | `SDL_CreateRenderer(win, const char *name)` — index and flags gone |
| Window+renderer | — | `SDL_CreateWindowAndRenderer()` takes **title first** |
| Key state | `event.key.state`, `SDL_PRESSED`/`SDL_RELEASED` | `event.key.down` (bool); PRESSED/RELEASED removed |
| Event timestamps | ms | **nanoseconds**, matching `SDL_GetTicksNS()` |
| Window/display events | `SDL_WINDOWEVENT` / `SDL_DISPLAYEVENT` wrapper + subtype | removed; each is a top-level `SDL_EVENT_WINDOW_*` / `SDL_EVENT_DISPLAY_*` |
| Gamepad | `SDL_GameController*`, `SDL_ControllerAxisEvent` | `SDL_Gamepad*`, `SDL_GamepadAxisEvent`; `SDL_GameControllerOpen`→`SDL_OpenGamepad`, `SDL_GameControllerGetAxis`→`SDL_GetGamepadAxis` |
| Face buttons | A/B/X/Y | positional **South/East/West/North** |
| Device enumeration | index-based loops | array getters: `SDL_GetJoysticks()`, `SDL_GetGamepads()`, `SDL_GetAudioPlaybackDevices()`, `SDL_GetAudioRecordingDevices()`, `SDL_GetHaptics()` |
| Audio open | `SDL_OpenAudioDevice()` + callback | `SDL_OpenAudioDeviceStream()`; `SDL_OpenAudio()`, `SDL_QueueAudio()`, `SDL_DequeueAudio()`, `SDL_AudioCVT` removed |
| Audio terms | capture / output | **recording** / **playback** |
| Audio formats | `AUDIO_S16` | `SDL_AUDIO_S16LE` |
| File I/O | `SDL_RWops` | `SDL_IOStream` (opaque). `SDL_RWread(s,p,size,maxnum)`→`SDL_ReadIO(s,p,size)` (POSIX-like, not stdio-like); `SDL_RWwrite`→`SDL_WriteIO`; `RW_SEEK_SET`→`SDL_IO_SEEK_SET`; custom impls via `SDL_OpenIO()` |
| Gestures | `SDL_gesture.h` | removed (split to separate SDL_gesture library) |
| Shaped windows | `SDL_shape.h` | `SDL_SetWindowShape()` in SDL_video.h |
| `SDL_QuitRequested()` | present | removed; use `SDL_PeepEvents()` |
| Env vars | `SDL_VIDEODRIVER`, `SDL_AUDIODRIVER` | `SDL_VIDEO_DRIVER`, `SDL_AUDIO_DRIVER` |
| Hints | many | many removed; properties API replaces them |
| Strings | ASCII `SDL_strcasecmp` | UTF-8 case-folding; `SDL_tolower`/`SDL_toupper` stay ASCII/single-byte |

Upstream ships migration scripts: `rename_symbols.py`, `rename_headers.py`, `rename_macros.py`.

---

## 3. Init + lifecycle

```c
bool SDL_Init(SDL_InitFlags flags);   // true = ok, false = error -> SDL_GetError()
```

- Main-thread only.
- File I/O and threading are initialized by default. Message boxes attempt to work without video.
- Ref-counted: one `SDL_QuitSubSystem()` per `SDL_InitSubSystem()`, or `SDL_Quit()` to force.

### Subsystem flags (SDL_InitFlags)

| Flag | Value | Implies |
|---|---|---|
| `SDL_INIT_AUDIO` | 0x00000010u | `SDL_INIT_EVENTS` |
| `SDL_INIT_VIDEO` | 0x00000020u | `SDL_INIT_EVENTS`; init on main thread |
| `SDL_INIT_JOYSTICK` | 0x00000200u | `SDL_INIT_EVENTS` |
| `SDL_INIT_HAPTIC` | 0x00001000u | — |
| `SDL_INIT_GAMEPAD` | 0x00002000u | `SDL_INIT_JOYSTICK` |
| `SDL_INIT_EVENTS` | 0x00004000u | — |
| `SDL_INIT_SENSOR` | 0x00008000u | `SDL_INIT_EVENTS` |
| `SDL_INIT_CAMERA` | 0x00010000u | `SDL_INIT_EVENTS` |

No `SDL_INIT_TIMER` in SDL3.

Metadata: `SDL_SetAppMetadata`, `SDL_SetAppMetadataProperty`, `SDL_GetAppMetadataProperty`. Threading helpers: `SDL_IsMainThread`, `SDL_RunOnMainThread` (`SDL_MainThreadCallback`).

### Main callbacks (README-main-functions)

```c
#define SDL_MAIN_USE_CALLBACKS
#include <SDL3/SDL_main.h>

SDL_AppResult SDL_AppInit(void **appstate, int argc, char **argv);
SDL_AppResult SDL_AppIterate(void *appstate);
SDL_AppResult SDL_AppEvent(void *appstate, SDL_Event *event);
void          SDL_AppQuit(void *appstate, SDL_AppResult result);
```

- `SDL_AppResult`: `SDL_APP_CONTINUE`, `SDL_APP_SUCCESS`, `SDL_APP_FAILURE`.
- `SDL_AppIterate` runs "over and over, possibly at the refresh rate of the display".
- SDL owns the event queue — **do not call `SDL_PollEvent`** in callback mode.
- With callbacks you write no `main` at all; "if you do, the app will likely fail to link."
- Classic mode instead: `SDL_main`, `SDL_main_func`, `SDL_RunApp`, `SDL_SetMainReady`, `SDL_EnterAppMainCallbacks`; macros `SDL_MAIN_HANDLED`, `SDL_MAIN_NEEDED`, `SDL_MAIN_AVAILABLE`, `SDLMAIN_DECLSPEC`. Windows: `SDL_RegisterApp` / `SDL_UnregisterApp`.

**Go/cgo relevance**: callback mode hijacks `main`, which collides with the Go runtime entry point. For a Go host, use classic mode with `SDL_MAIN_HANDLED` + `SDL_SetMainReady` and drive `SDL_PollEvent` from a locked OS thread. **[UNVERIFIED]** — the wiki documents the macros; it does not document a Go binding.

### Errors

`SDL_GetError`, `SDL_SetError`, `SDL_SetErrorV`, `SDL_ClearError`, `SDL_OutOfMemory`; macros `SDL_InvalidParamError`, `SDL_Unsupported`. Error strings are **per-thread**.

---

## 4. Video / window / display

Create: `SDL_CreateWindow`, `SDL_CreateWindowWithProperties`, `SDL_CreateWindowAndRenderer`, `SDL_CreatePopupWindow`. Destroy: `SDL_DestroyWindow`.

State: `SDL_ShowWindow`, `SDL_HideWindow`, `SDL_RaiseWindow`, `SDL_MaximizeWindow`, `SDL_MinimizeWindow`, `SDL_RestoreWindow`, `SDL_GetWindowFlags`, `SDL_GetWindowID`, `SDL_GetWindowFromID`.

Displays: `SDL_GetDisplays`, `SDL_GetPrimaryDisplay`, `SDL_GetDisplayForWindow`, `SDL_GetDisplayForPoint`, `SDL_GetDisplayName`, `SDL_GetDisplayProperties`. Mode struct: `SDL_DisplayMode`. Enums: `SDL_DisplayOrientation`, `SDL_SystemTheme`, `SDL_FlashOperation`, `SDL_HitTestResult`, `SDL_ProgressState`, `SDL_GLAttr`.

Fullscreen: `SDL_SetWindowFullscreen`, `SDL_SetWindowFullscreenMode`, `SDL_GetWindowFullscreenMode`, `SDL_GetFullscreenDisplayModes`, `SDL_GetClosestFullscreenDisplayMode`.

Position macros: `SDL_WINDOWPOS_CENTERED`, `SDL_WINDOWPOS_CENTERED_DISPLAY()`, `SDL_WINDOWPOS_UNDEFINED`, `SDL_WINDOWPOS_ISCENTERED()`, `SDL_WINDOWPOS_ISUNDEFINED()`.

Window flags of note: `SDL_WINDOW_FULLSCREEN`, `SDL_WINDOW_OPENGL`, `SDL_WINDOW_VULKAN`, `SDL_WINDOW_METAL`, `SDL_WINDOW_HIDDEN` (needs `SDL_ShowWindow`), `SDL_WINDOW_BORDERLESS`, `SDL_WINDOW_RESIZABLE`, `SDL_WINDOW_ALWAYS_ON_TOP`, `SDL_WINDOW_UTILITY` (hidden from taskbar/window list), `SDL_WINDOW_TOOLTIP`, `SDL_WINDOW_POPUP_MENU`, `SDL_WINDOW_TRANSPARENT`, `SDL_WINDOW_NOT_FOCUSABLE`, `SDL_WINDOW_HIGH_PIXEL_DENSITY`, `SDL_WINDOW_EXTERNAL` ("window not created by SDL"), `SDL_WINDOW_MOUSE_GRABBED`, `SDL_WINDOW_KEYBOARD_GRABBED`, `SDL_WINDOW_MOUSE_CAPTURE`, `SDL_WINDOW_MOUSE_RELATIVE_MODE`, `SDL_WINDOW_MODAL`, `SDL_WINDOW_OCCLUDED`, `SDL_WINDOW_MINIMIZED`, `SDL_WINDOW_MAXIMIZED`, `SDL_WINDOW_INPUT_FOCUS`, `SDL_WINDOW_MOUSE_FOCUS`, `SDL_WINDOW_FILL_DOCUMENT` (Emscripten, since 3.4.0).

`SDL_WINDOW_EXTERNAL` is the interesting one for a WebView2 shell — adopting an OS window SDL didn't create. Exact adoption path (properties keys for an HWND) **[UNVERIFIED]** — not on the pages fetched.

### High-DPI (README-highdpi)

- **Pixel density** = window size vs pixel size ratio. **Display scale** = pixel density × content scale = "scale from the pixel resolution to the desired content size".
- `SDL_GetWindowSize` (logical) vs `SDL_GetWindowSizeInPixels` (physical), `SDL_GetWindowPixelDensity`, `SDL_GetWindowDisplayScale`, `SDL_GetDisplayContentScale`.
- Coordinate conversion: `SDL_ConvertEventToRenderCoordinates`, `SDL_RenderCoordinatesFromWindow`, `SDL_RenderCoordinatesToWindow`.
- Platform models: Windows/Android are pixel-native (apply content scale yourself). macOS/iOS are logical-native (opt in with `SDL_WINDOW_HIGH_PIXEL_DENSITY`). X11 follows Windows, Wayland follows macOS.

OpenGL: `SDL_GL_CreateContext`, `SDL_GL_DestroyContext` (note: **not** `SDL_GL_DeleteContext`), `SDL_GL_MakeCurrent`, `SDL_GL_SwapWindow`.

---

## 5. Event loop

Pump/read: `SDL_PumpEvents`, `SDL_PollEvent`, `SDL_WaitEvent`, `SDL_WaitEventTimeout`, `SDL_PeepEvents` (with `SDL_EventAction`), `SDL_PushEvent`, `SDL_RegisterEvents`.
Filter/watch: `SDL_SetEventFilter`, `SDL_GetEventFilter`, `SDL_FilterEvents`, `SDL_AddEventWatch`, `SDL_RemoveEventWatch` (`SDL_EventFilter`).
Query/flush: `SDL_HasEvent`, `SDL_HasEvents`, `SDL_FlushEvent`, `SDL_FlushEvents`, `SDL_SetEventEnabled`, `SDL_EventEnabled`.
Helpers: `SDL_GetWindowFromEvent`, `SDL_GetEventDescription`.

Event types we'd plausibly care about (see cheat sheet for the full list):

| Group | Base | Examples |
|---|---|---|
| App | 0x100 | `SDL_EVENT_QUIT`, `SDL_EVENT_TERMINATING`, `SDL_EVENT_LOW_MEMORY`, `SDL_EVENT_WILL_ENTER_BACKGROUND`, `SDL_EVENT_DID_ENTER_FOREGROUND`, `SDL_EVENT_LOCALE_CHANGED`, `SDL_EVENT_SYSTEM_THEME_CHANGED` |
| Display | 0x151 | `SDL_EVENT_DISPLAY_ADDED/REMOVED/MOVED`, `SDL_EVENT_DISPLAY_ORIENTATION`, `SDL_EVENT_DISPLAY_CURRENT_MODE_CHANGED`, `SDL_EVENT_DISPLAY_CONTENT_SCALE_CHANGED`, `SDL_EVENT_DISPLAY_USABLE_BOUNDS_CHANGED` |
| Window | 0x202 | `SDL_EVENT_WINDOW_RESIZED`, `SDL_EVENT_WINDOW_PIXEL_SIZE_CHANGED`, `SDL_EVENT_WINDOW_EXPOSED`, `SDL_EVENT_WINDOW_CLOSE_REQUESTED`, `SDL_EVENT_WINDOW_DISPLAY_CHANGED`, `SDL_EVENT_WINDOW_DISPLAY_SCALE_CHANGED`, `SDL_EVENT_WINDOW_HDR_STATE_CHANGED`, `SDL_EVENT_WINDOW_OCCLUDED` |
| Gamepad | 0x650 | `SDL_EVENT_GAMEPAD_AXIS_MOTION`, `SDL_EVENT_GAMEPAD_BUTTON_DOWN/UP`, `SDL_EVENT_GAMEPAD_ADDED/REMOVED/REMAPPED`, `SDL_EVENT_GAMEPAD_TOUCHPAD_*`, `SDL_EVENT_GAMEPAD_SENSOR_UPDATE`, `SDL_EVENT_GAMEPAD_UPDATE_COMPLETE` |
| Joystick | 0x600 | `SDL_EVENT_JOYSTICK_AXIS_MOTION`, `SDL_EVENT_JOYSTICK_HAT_MOTION`, `SDL_EVENT_JOYSTICK_ADDED/REMOVED`, `SDL_EVENT_JOYSTICK_BATTERY_UPDATED` |
| Audio hotplug | 0x1100 | `SDL_EVENT_AUDIO_DEVICE_ADDED`, `SDL_EVENT_AUDIO_DEVICE_REMOVED`, `SDL_EVENT_AUDIO_DEVICE_FORMAT_CHANGED` |
| Camera | 0x1400 | `SDL_EVENT_CAMERA_DEVICE_ADDED/REMOVED/APPROVED/DENIED` |
| Render | 0x2000 | `SDL_EVENT_RENDER_TARGETS_RESET`, `SDL_EVENT_RENDER_DEVICE_RESET`, `SDL_EVENT_RENDER_DEVICE_LOST` |
| Drop | 0x1000 | `SDL_EVENT_DROP_BEGIN/FILE/TEXT/POSITION/COMPLETE` |
| Custom | 0x8000 | `SDL_EVENT_USER` (allocate via `SDL_RegisterEvents`) |

`SDL_EVENT_POLL_SENTINEL` (0x7F00) marks the end of a poll cycle. `SDL_EVENT_LAST` = 0xFFFF.

Device add/remove events give us free hotplug for audio, camera, joystick, gamepad, keyboard, mouse — the thing our own pollers currently do by hand.

---

## 6. 2D render API vs GPU API

### Which one

| Need | Use |
|---|---|
| Points, lines, filled rects, textured quads, 2D polygons, blend/add modes | `SDL_Render*` — "designed to accelerate simple 2D operations" |
| 3D, particles, compute, custom shaders | GPU API (or raw OpenGL/D3D) — the render page says so explicitly |

The 2D renderer is a facade over per-platform backends: `SDL_GetNumRenderDrivers`, `SDL_GetRenderDriver`, `SDL_GetRendererName`. `SDL_CreateSoftwareRenderer` and `SDL_CreateGPURenderer` exist; macros `SDL_SOFTWARE_RENDERER` and `SDL_GPU_RENDERER` name those drivers. Hybrid: `SDL_CreateGPURenderState`, `SDL_SetGPURenderState*` let you attach custom GPU shaders/uniforms/bindings to the 2D renderer, and `SDL_GetGPURendererDevice` hands back the underlying `SDL_GPUDevice`.

Useful 2D bits for a preview surface: `SDL_CreateTexture` (`SDL_TextureAccess`), `SDL_UpdateTexture`, `SDL_UpdateYUVTexture`, `SDL_UpdateNVTexture` (NV12 — matters for camera/decoder output), `SDL_LockTexture` / `SDL_LockTextureToSurface` / `SDL_UnlockTexture`, `SDL_SetRenderTarget`, `SDL_RenderReadPixels`, `SDL_SetRenderVSync`, `SDL_SetRenderLogicalPresentation` (`SDL_RendererLogicalPresentation`), `SDL_RenderGeometry` / `SDL_RenderGeometryRaw` (`SDL_Vertex`), `SDL_RenderDebugText`.

### GPU API (CategoryGPU)

Cross-platform 3D + compute "in the style of Metal, Vulkan, and Direct3D 12".

| Backend | Platforms | Requirement |
|---|---|---|
| Vulkan | Windows, Linux, Nintendo Switch, Android | Vulkan 1.0 + specific extensions |
| D3D12 | Windows 10+, Xbox One / Series X\|S | DX12 Feature Level 11_0 |
| Metal | macOS 10.14+, iOS/tvOS 13.0+ | varies by OS |

Shader formats (`SDL_GPUShaderFormat` bitflags):

| Flag | Meaning |
|---|---|
| `SDL_GPU_SHADERFORMAT_INVALID` (0) | unset |
| `SDL_GPU_SHADERFORMAT_PRIVATE` (1<<0) | "Shaders for NDA'd platforms" |
| `SDL_GPU_SHADERFORMAT_SPIRV` (1<<1) | "SPIR-V shaders for Vulkan" |
| `SDL_GPU_SHADERFORMAT_DXBC` (1<<2) | "DXBC SM5_1 shaders for D3D12" |
| `SDL_GPU_SHADERFORMAT_DXIL` (1<<3) | "DXIL SM6_0 shaders for D3D12" |
| `SDL_GPU_SHADERFORMAT_MSL` (1<<4) | "MSL shaders for Metal" |
| `SDL_GPU_SHADERFORMAT_METALLIB` (1<<5) | "Precompiled metallib shaders for Metal" |

```c
SDL_GPUDevice *SDL_CreateGPUDevice(SDL_GPUShaderFormat format_flags,
                                   bool debug_mode, const char *name);
```
`name` = `"vulkan"`, `"direct3d12"`, `"metal"`, or NULL to let SDL pick.

**There is no single portable shader source language.** You declare which formats you can supply, per backend; cross-compilation is on you (SPIRV-Cross / DXC / shadercross class of tooling — **[UNVERIFIED]**, not documented on the pages fetched). That is the real cost of adopting the GPU API.

Model:
- **Command buffers** batch instructions; multiple buffers across threads are fine if submitted in the right order.
- **Render passes** — up to 4 color textures + 1 depth texture; render state resets when a pass ends.
- **Compute passes** — compute shaders with writable textures/buffers.
- **Copy passes** — resource-to-resource transfers.

Verified core calls: `SDL_CreateGPUDevice`, `SDL_AcquireGPUCommandBuffer`, `SDL_BeginGPURenderPass`, `SDL_BindGPUGraphicsPipeline`, `SDL_DrawGPUPrimitives`, `SDL_EndGPURenderPass`, `SDL_SubmitGPUCommandBuffer`, `SDL_CreateGPUShader`, `SDL_CreateGPUTexture`, `SDL_UploadToGPUTexture`, `SDL_BeginGPUComputePass`, `SDL_DispatchGPUCompute`. The category page states ~70 functions total; the rest were not enumerated on the fetched page — **[UNVERIFIED]** individually, check CategoryGPU before citing others.

---

## 7. Audio

"All audio in SDL3 revolves around `SDL_AudioStream`. Whether you want to play or record audio, convert it, stream it, buffer it, or mix it, you're going to be passing it through an audio stream."

Model: open a device → bind N streams to it. Logical devices let independent opens coexist, with automatic migration to different hardware.

```c
SDL_AudioStream *SDL_OpenAudioDeviceStream(SDL_AudioDeviceID devid,
                                           const SDL_AudioSpec *spec,
                                           SDL_AudioStreamCallback callback,
                                           void *userdata);
```
- Opens device, creates stream, binds them — one call.
- **Device begins paused** ("to map more closely to SDL2-style behavior"); resume with `SDL_ResumeAudioStreamDevice()`.
- `callback == NULL` → app queues data with `SDL_PutAudioStreamData` (playback) / drains with `SDL_GetAudioStreamData` (recording). Non-NULL → fires once unpaused.

Devices: `SDL_OpenAudioDevice`, `SDL_CloseAudioDevice`, `SDL_GetAudioPlaybackDevices`, `SDL_GetAudioRecordingDevices`, `SDL_GetAudioDeviceName`, `SDL_GetAudioDeviceFormat`, `SDL_GetAudioDeviceChannelMap`, `SDL_IsAudioDevicePhysical`, `SDL_IsAudioDevicePlayback`, `SDL_PauseAudioDevice`, `SDL_ResumeAudioDevice`, `SDL_AudioDevicePaused`, `SDL_SetAudioDeviceGain`, `SDL_GetAudioDeviceGain`. Default IDs: `SDL_AUDIO_DEVICE_DEFAULT_PLAYBACK`, `SDL_AUDIO_DEVICE_DEFAULT_RECORDING`.

Streams: `SDL_CreateAudioStream`, `SDL_DestroyAudioStream`, `SDL_BindAudioStream(s)`, `SDL_UnbindAudioStream(s)`, `SDL_GetAudioStreamDevice`, `SDL_PutAudioStreamData`, `SDL_PutAudioStreamDataNoCopy`, `SDL_PutAudioStreamPlanarData`, `SDL_GetAudioStreamData`, `SDL_GetAudioStreamQueued`, `SDL_GetAudioStreamAvailable`, `SDL_ClearAudioStream`, `SDL_FlushAudioStream`, `SDL_LockAudioStream`/`SDL_UnlockAudioStream`.

Conversion / DSP-adjacent: `SDL_SetAudioStreamFormat` (resample + reformat on the fly), `SDL_SetAudioStreamFrequencyRatio` (pitch/speed), `SDL_SetAudioStreamGain`, `SDL_SetAudioStreamInputChannelMap` / `SDL_SetAudioStreamOutputChannelMap`, `SDL_ConvertAudioSamples`, `SDL_MixAudio`, `SDL_GetSilenceValueForFormat`. Postmix hook: `SDL_SetAudioPostmixCallback`.

Formats (`SDL_AudioFormat`): `SDL_AUDIO_UNKNOWN`, `SDL_AUDIO_U8`, `SDL_AUDIO_S8`, `SDL_AUDIO_S16LE`, `SDL_AUDIO_S16BE`, `SDL_AUDIO_S32LE`, `SDL_AUDIO_S32BE`, `SDL_AUDIO_F32LE`, `SDL_AUDIO_F32BE`, plus native-endian aliases `SDL_AUDIO_S16`, `SDL_AUDIO_S32`, `SDL_AUDIO_F32`. Note: **no S24, no F64.**

Introspection macros: `SDL_AUDIO_BITSIZE`, `SDL_AUDIO_BYTESIZE`, `SDL_AUDIO_FRAMESIZE`, `SDL_AUDIO_ISFLOAT`, `SDL_AUDIO_ISINT`, `SDL_AUDIO_ISSIGNED`, `SDL_AUDIO_ISUNSIGNED`, `SDL_AUDIO_ISBIGENDIAN`, `SDL_AUDIO_ISLITTLEENDIAN`, masks `SDL_AUDIO_MASK_*`, `SDL_DEFINE_AUDIO_FORMAT`.

Drivers: `SDL_GetNumAudioDrivers`, `SDL_GetAudioDriver`, `SDL_GetCurrentAudioDriver`. WAV only for file loading: `SDL_LoadWAV`, `SDL_LoadWAV_IO`.

Recording = same API with `SDL_GetAudioRecordingDevices` / `SDL_AUDIO_DEVICE_DEFAULT_RECORDING`; you pull frames with `SDL_GetAudioStreamData`.

**Not documented anywhere we fetched: exclusive/WASAPI-exclusive mode, ASIO, per-device latency control.** Treat SDL audio as a portable shared-mode path, not a low-latency DJ output.

---

## 8. Input

### Gamepad (mapping DB on top of joystick)

`SDL_GetGamepads`, `SDL_OpenGamepad`, `SDL_CloseGamepad`, `SDL_IsGamepad`, `SDL_HasGamepad`, `SDL_GamepadConnected`, `SDL_GetGamepadFromID`, `SDL_GetGamepadJoystick`, `SDL_UpdateGamepads`, `SDL_SetGamepadEventsEnabled`.
State: `SDL_GetGamepadAxis`, `SDL_GetGamepadButton`, `SDL_GamepadHasAxis`, `SDL_GamepadHasButton`, `SDL_GetGamepadSensorData`, `SDL_SetGamepadSensorEnabled`, `SDL_GetNumGamepadTouchpads`, `SDL_GetGamepadTouchpadFinger`, `SDL_GetGamepadCapSense`.
Identity: `SDL_GetGamepadName`, `SDL_GetGamepadVendor`, `SDL_GetGamepadProduct`, `SDL_GetGamepadSerial`, `SDL_GetGamepadPath`, `SDL_GetGamepadGUIDForID`, `SDL_GetGamepadType`, `SDL_GetRealGamepadType`, `SDL_GetGamepadPowerInfo`, `SDL_GetGamepadProperties`, `SDL_GetGamepadSteamHandle`.
Mappings: `SDL_AddGamepadMapping`, `SDL_AddGamepadMappingsFromFile`, `SDL_AddGamepadMappingsFromIO`, `SDL_GetGamepadMapping`, `SDL_GetGamepadMappingForGUID`, `SDL_SetGamepadMapping`, `SDL_ReloadGamepadMappings`, `SDL_GetGamepadBindings` (`SDL_GamepadBinding`, `SDL_GamepadBindingType`).
Output: `SDL_RumbleGamepad`, `SDL_RumbleGamepadTriggers`, `SDL_SetGamepadLED`, `SDL_SendGamepadEffect`.
Enums: `SDL_GamepadAxis`, `SDL_GamepadButton`, `SDL_GamepadType`, `SDL_GamepadButtonLabel`, `SDL_GamepadBindingType`, `SDL_GamepadCapSenseType`. Individual enum **members** not fetched — **[UNVERIFIED]** beyond the migration note that face buttons are South/East/West/North.

### Joystick (raw)

`SDL_GetJoysticks`, `SDL_OpenJoystick`, `SDL_CloseJoystick`, `SDL_GetJoystickAxis` / `Button` / `Hat` / `Ball`, `SDL_GetNumJoystickAxes|Buttons|Hats|Balls`, `SDL_GetJoystickGUID`, `SDL_GetJoystickGUIDInfo`, `SDL_GetJoystickVendor|Product|ProductVersion|Serial|Path|Type`, `SDL_GetJoystickPowerInfo`, `SDL_UpdateJoysticks`, `SDL_LockJoysticks`/`SDL_TryLockJoysticks`/`SDL_UnlockJoysticks`. Rumble/LED: `SDL_RumbleJoystick`, `SDL_RumbleJoystickTriggers`, `SDL_SetJoystickLED`, `SDL_SendJoystickEffect`. Axis range macros `SDL_JOYSTICK_AXIS_MIN` (-32768) / `SDL_JOYSTICK_AXIS_MAX` (32767).

**Virtual joysticks**: `SDL_AttachVirtualJoystick` (`SDL_VirtualJoystickDesc`, `SDL_VirtualJoystickSensorDesc`, `SDL_VirtualJoystickTouchpadDesc`), `SDL_DetachVirtualJoystick`, `SDL_SetJoystickVirtualAxis|Button|Hat|Ball|Touchpad`, `SDL_SendJoystickVirtualSensorData`, `SDL_IsJoystickVirtual`. Real option for exposing a synthetic controller to other software.

### HIDAPI

`SDL_hid_*` — "Multi-Platform library for communication with HID devices", adapted from Alan Ott's HIDAPI. Disable at build time with `SDL_HIDAPI_DISABLED`. Full raw report access: `SDL_hid_init`, `SDL_hid_enumerate` (`SDL_hid_device_info`, `SDL_hid_bus_type`), `SDL_hid_open`, `SDL_hid_open_path`, `SDL_hid_read`, `SDL_hid_read_timeout`, `SDL_hid_write`, `SDL_hid_get_feature_report`, `SDL_hid_send_feature_report`, `SDL_hid_get_input_report`, `SDL_hid_get_report_descriptor`, `SDL_hid_set_nonblocking`, `SDL_hid_device_change_count`, `SDL_hid_ble_scan`, `SDL_hid_close`, `SDL_hid_exit`.

### Keyboard / mouse

Keyboard: `SDL_GetKeyboardState`, `SDL_GetModState`, `SDL_SetModState`, `SDL_GetKeyFromScancode`, `SDL_GetScancodeFromKey`, `SDL_GetKeyName`, `SDL_GetScancodeName`, `SDL_GetKeyboards`, `SDL_GetKeyboardFocus`, `SDL_HasKeyboard`, `SDL_ResetKeyboard`. Text/IME: `SDL_StartTextInput`, `SDL_StartTextInputWithProperties`, `SDL_StopTextInput`, `SDL_TextInputActive`, `SDL_SetTextInputArea`, `SDL_GetTextInputArea`, `SDL_ClearComposition`, `SDL_HasScreenKeyboardSupport`, `SDL_ScreenKeyboardShown`; enums `SDL_TextInputType`, `SDL_Capitalization`.

Mouse: `SDL_GetMouseState`, `SDL_GetGlobalMouseState`, `SDL_GetRelativeMouseState`, `SDL_GetMice`, `SDL_GetMouseFocus`, `SDL_CaptureMouse`, `SDL_SetWindowRelativeMouseMode` ("hides the cursor, grabs mouse input to the window"), `SDL_GetWindowRelativeMouseMode`, `SDL_SetWindowMouseGrab`, `SDL_SetWindowMouseRect`, `SDL_WarpMouseInWindow`, `SDL_WarpMouseGlobal`, `SDL_SetRelativeMouseTransform` (`SDL_MouseMotionTransformCallback`). Cursors: `SDL_CreateCursor`, `SDL_CreateColorCursor`, `SDL_CreateSystemCursor` (`SDL_SystemCursor`), `SDL_CreateAnimatedCursor` (`SDL_CursorFrameInfo`), `SDL_SetCursor`, `SDL_ShowCursor`, `SDL_HideCursor`, `SDL_CursorVisible`, `SDL_DestroyCursor`. `SDL_TOUCH_MOUSEID` marks touch-synthesized mouse events.

### Haptic

Flow (per the wiki): init `SDL_INIT_HAPTIC` → open device → build effect → `SDL_CreateHapticEffect()` → `SDL_RunHapticEffect()` → destroy → close.
`SDL_GetHaptics`, `SDL_OpenHaptic`, `SDL_OpenHapticFromJoystick`, `SDL_OpenHapticFromMouse`, `SDL_CloseHaptic`, `SDL_GetHapticFeatures`, `SDL_HapticEffectSupported`, `SDL_CreateHapticEffect`, `SDL_UpdateHapticEffect`, `SDL_RunHapticEffect`, `SDL_StopHapticEffect(s)`, `SDL_DestroyHapticEffect`, `SDL_GetHapticEffectStatus`, `SDL_SetHapticGain`, `SDL_SetHapticAutocenter`, `SDL_PauseHaptic`/`SDL_ResumeHaptic`. Simple rumble path: `SDL_HapticRumbleSupported`, `SDL_InitHapticRumble`, `SDL_PlayHapticRumble`, `SDL_StopHapticRumble`. Effect structs: `SDL_HapticEffect` (union), `SDL_HapticConstant`, `SDL_HapticPeriodic`, `SDL_HapticCondition`, `SDL_HapticRamp`, `SDL_HapticLeftRight`, `SDL_HapticCustom`, `SDL_HapticDirection`.

---

## 9. Properties, timers, threads, I/O, camera

### Properties

Runtime named variables in typed groups; SDL3 uses them where SDL2 used hints and fixed struct fields. `SDL_CreateProperties`, `SDL_DestroyProperties`, `SDL_CopyProperties`, `SDL_LockProperties`/`SDL_UnlockProperties`, `SDL_GetGlobalProperties`, `SDL_SetPointerProperty`, `SDL_SetPointerPropertyWithCleanup` (`SDL_CleanupPropertyCallback`), `SDL_SetStringProperty`, `SDL_SetNumberProperty` (64-bit), `SDL_SetFloatProperty`, `SDL_SetBooleanProperty`, matching `SDL_Get*Property`, `SDL_HasProperty`, `SDL_GetPropertyType` (`SDL_PropertyType`), `SDL_ClearProperty`, `SDL_EnumerateProperties` (`SDL_EnumeratePropertiesCallback`), `SDL_GetNumProperties`. Handle type `SDL_PropertiesID`; naming macro `SDL_PROP_NAME_STRING`.

Objects expose their own groups: `SDL_GetWindowProperties`, `SDL_GetDisplayProperties`, `SDL_GetRendererProperties`, `SDL_GetTextureProperties`, `SDL_GetGamepadProperties`, `SDL_GetJoystickProperties`, `SDL_GetCameraProperties`, `SDL_GetAudioStreamProperties`, `SDL_GetIOProperties`, `SDL_hid_get_properties`.

### Hints

`SDL_SetHint`, `SDL_SetHintWithPriority` (`SDL_HintPriority`), `SDL_GetHint`, `SDL_GetHintBoolean`, `SDL_ResetHint`, `SDL_ResetHints`, `SDL_AddHintCallback`, `SDL_RemoveHintCallback` (`SDL_HintCallback`). Verified hint names: `SDL_HINT_AUDIO_DRIVER`, `SDL_HINT_VIDEO_DRIVER`, `SDL_HINT_RENDER_DRIVER`, `SDL_HINT_GPU_DRIVER`, `SDL_HINT_RENDER_VSYNC`, `SDL_HINT_RENDER_GPU_DEBUG`, `SDL_HINT_JOYSTICK_HIDAPI`, `SDL_HINT_JOYSTICK_RAWINPUT`, `SDL_HINT_GAMECONTROLLERCONFIG`. Others exist — check CategoryHints, don't guess.

### Timers

`SDL_GetTicks` (ms since init), `SDL_GetTicksNS` (ns), `SDL_GetPerformanceCounter`, `SDL_GetPerformanceFrequency`, `SDL_Delay`, `SDL_DelayNS`, `SDL_DelayPrecise`, `SDL_AddTimer`, `SDL_AddTimerNS`, `SDL_RemoveTimer` (`SDL_TimerID`, `SDL_TimerCallback`, `SDL_NSTimerCallback`). Conversion macros: `SDL_MS_TO_NS`, `SDL_NS_TO_MS`, `SDL_US_TO_NS`, `SDL_NS_TO_US`, `SDL_SECONDS_TO_NS`, `SDL_NS_TO_SECONDS`, constants `SDL_NS_PER_SECOND`, `SDL_NS_PER_MS`, `SDL_NS_PER_US`, `SDL_US_PER_SECOND`, `SDL_MS_PER_SECOND`.

`SDL_DelayPrecise` is worth knowing — high-precision sleep without hand-rolling spin loops.

### Threads / sync / atomics

Threads: `SDL_CreateThread`, `SDL_CreateThreadWithProperties`, `SDL_WaitThread`, `SDL_DetachThread`, `SDL_GetThreadID`, `SDL_GetCurrentThreadID`, `SDL_GetThreadName`, `SDL_GetThreadState` (`SDL_ThreadState`), `SDL_SetCurrentThreadPriority` (`SDL_ThreadPriority`), TLS via `SDL_SetTLS`/`SDL_GetTLS`/`SDL_CleanupTLS` (`SDL_TLSID`, `SDL_TLSDestructorCallback`).

Sync: `SDL_CreateMutex`/`SDL_LockMutex`/`SDL_TryLockMutex`/`SDL_UnlockMutex`/`SDL_DestroyMutex`; RW locks `SDL_CreateRWLock`, `SDL_LockRWLockForReading|Writing`, `SDL_TryLockRWLockForReading|Writing`, `SDL_UnlockRWLock`, `SDL_DestroyRWLock`; semaphores `SDL_CreateSemaphore`, `SDL_WaitSemaphore`, `SDL_TryWaitSemaphore`, `SDL_WaitSemaphoreTimeout`, `SDL_SignalSemaphore`, `SDL_GetSemaphoreValue`, `SDL_DestroySemaphore`; conditions `SDL_CreateCondition`, `SDL_WaitCondition`, `SDL_WaitConditionTimeout`, `SDL_SignalCondition`, `SDL_BroadcastCondition`, `SDL_DestroyCondition`. One-time init: `SDL_ShouldInit`, `SDL_SetInitialized`, `SDL_ShouldQuit` (`SDL_InitState`, `SDL_InitStatus`).

Atomics: `SDL_AtomicInt`, `SDL_AtomicU32`, `SDL_SpinLock`; `SDL_GetAtomicInt|U32|Pointer`, `SDL_SetAtomicInt|U32|Pointer`, `SDL_AddAtomicInt|U32`, `SDL_CompareAndSwapAtomicInt|U32|Pointer`, `SDL_LockSpinlock`/`SDL_TryLockSpinlock`/`SDL_UnlockSpinlock`; barriers `SDL_MemoryBarrierAcquire`/`SDL_MemoryBarrierRelease` (+ `*Function` variants), `SDL_CompilerBarrier`, `SDL_CPUPauseInstruction`; refcount macros `SDL_AtomicIncRef`, `SDL_AtomicDecRef`.

### IOStream / Storage

IOStream (SDL2 `SDL_RWops` successor): `SDL_IOFromFile`, `SDL_IOFromMem`, `SDL_IOFromConstMem`, `SDL_IOFromDynamicMem`, `SDL_OpenIO` (`SDL_IOStreamInterface`), `SDL_ReadIO`, `SDL_WriteIO`, `SDL_SeekIO` (`SDL_IOWhence`), `SDL_TellIO`, `SDL_GetIOSize`, `SDL_GetIOStatus` (`SDL_IOStatus`), `SDL_FlushIO`, `SDL_CloseIO`, `SDL_LoadFile`, `SDL_LoadFile_IO`, `SDL_SaveFile`, `SDL_SaveFile_IO`, `SDL_IOprintf`, `SDL_IOvprintf`, plus endian-explicit `SDL_Read/WriteU8|S8|U16LE|U16BE|S16LE|S16BE|U32LE|U32BE|S32LE|S32BE|U64LE|U64BE|S64LE|S64BE`.

Those endian readers/writers are a ready-made binary parser toolkit — relevant if anything ever links SDL alongside our own format code (cue/pdb/tsi parsing).

Storage: abstraction over title (read-only content) vs user (writable) storage, with availability/timing handled. `SDL_OpenTitleStorage`, `SDL_OpenUserStorage`, `SDL_OpenFileStorage`, `SDL_OpenStorage` (`SDL_StorageInterface`), `SDL_StorageReady`, `SDL_ReadStorageFile`, `SDL_WriteStorageFile`, `SDL_GetStorageFileSize`, `SDL_GetStoragePathInfo`, `SDL_GetStorageSpaceRemaining`, `SDL_EnumerateStorageDirectory`, `SDL_GlobStorageDirectory`, `SDL_CreateStorageDirectory`, `SDL_CopyStorageFile`, `SDL_RenameStoragePath`, `SDL_RemoveStoragePath`, `SDL_CloseStorage`. Console-oriented; low value on desktop Windows.

### Camera

Flow: `SDL_GetCameras` → `SDL_OpenCamera` → `SDL_AcquireCameraFrame` → `SDL_ReleaseCameraFrame` → `SDL_CloseCamera`.
Also `SDL_GetCameraSupportedFormats` (`SDL_CameraSpec`), `SDL_GetCameraFormat`, `SDL_GetCameraName`, `SDL_GetCameraID`, `SDL_GetCameraPosition` (`SDL_CameraPosition`), `SDL_GetCameraProperties`, `SDL_GetCameraPermissionState` (`SDL_CameraPermissionState`), drivers `SDL_GetNumCameraDrivers`, `SDL_GetCameraDriver`, `SDL_GetCurrentCameraDriver`.

Permissions: platforms with an approval flow gate frames — "A successfully opened camera will not provide images until permission is granted." Poll `SDL_GetCameraPermissionState` or watch `SDL_EVENT_CAMERA_DEVICE_APPROVED` / `SDL_EVENT_CAMERA_DEVICE_DENIED`. Platforms without approval grant immediately.

Warmup: the docs say to expect and discard initial black/underexposed frames.

Frames come back as surfaces (`SDL_Surface`), convertible via `SDL_ConvertSurface`, `SDL_ConvertSurfaceAndColorspace`, `SDL_ConvertPixels`, `SDL_ConvertPixelsAndColorspace`, `SDL_ScaleSurface`. Pixel/colorspace enums: `SDL_PixelFormat`, `SDL_Colorspace`, `SDL_ColorType` (RGB or YCbCr), `SDL_ColorRange`, `SDL_ChromaLocation`, `SDL_ColorPrimaries`, `SDL_TransferCharacteristics`, `SDL_MatrixCoefficients`.

**No UVC property control (exposure/gain/white balance/pan-tilt) is documented in CategoryCamera.**

---

## 10. Building / linking

### CMake (README-cmake)

| Option / target | Meaning |
|---|---|
| `-DSDL_SHARED=ON/OFF` | build `SDL3.dll` / `libSDL3.so` / `libSDL3.dylib`; on by default where supported |
| `-DSDL_STATIC=ON/OFF` | build `SDL3-static.lib` / `libSDL3.a` |
| `-DCMAKE_POSITION_INDEPENDENT_CODE=ON` | needed if a static SDL goes into a shared lib |
| `find_package(SDL3)` → `SDL3::SDL3` | always exists; aliases shared or static |
| `SDL3::SDL3-shared`, `SDL3::SDL3-static` | specific variants, existence not guaranteed |
| `SDL3::Headers` | headers only |
| `SDL3::SDL3_test` | test lib |
| `add_subdirectory(vendored/SDL)` | vendored build, same targets |
| `-DCMAKE_SYSTEM_NAME=`, `-DCMAKE_OSX_SYSROOT=`, `-DCMAKE_OSX_ARCHITECTURES="arm64;x86_64"` | cross-compile / universal binaries |

### Windows (README-windows)

- MSVC (VS projects), MinGW-w64 >= 8.0.3 via CMake + MSYS2, LLVM/Intel C++ (VS projects may need `-msse3` added manually per file; CMake avoids it).
- Desktop Windows back to XP. **WinRT / Windows Phone / UWP no longer supported.**
- Shipping: "copy SDL3.dll to an appropriate directory so that the game can find it at runtime."

### Dynamic API (README-dynapi)

- SDL routes every call through a jump table filled by `SDL_InitDynamicAPI()` on first call.
- `SDL3_DYNAMIC_API=/path/to/libSDL3.so.0` makes even a **statically linked** SDL defer to an external library at runtime.
- Disabled with a single `#define` if you want the classic behavior.
- Implication: static-linking SDL does not fully freeze the implementation unless dynapi is compiled out. Relevant if we ever ship a static SDL and want deterministic behavior — decide explicitly.

### Our stack

**Go + cgo.** Standard C-library integration: `#cgo` flags to the SDL3 headers + import lib, DLL beside the exe. Two hard constraints from the docs:
1. Callback mode (`SDL_MAIN_USE_CALLBACKS`) forbids you writing `main` — incompatible with Go's runtime entry. Use classic mode + `SDL_MAIN_HANDLED` / `SDL_SetMainReady`.
2. `SDL_Init(SDL_INIT_VIDEO)` and video/window work is main-thread-only → `runtime.LockOSThread` on the goroutine that owns it.
Go binding packages exist in the wild but are **[UNVERIFIED]** here — nothing on the SDL wiki endorses one. If we bind, prefer hand-written cgo over an unvetted third-party wrapper, consistent with our zig/native shims.

**Zig.** `zig cc` is a working C toolchain, so a vendored SDL can be built via its CMake with `zig cc`/`zig c++` as compilers, or linked from `build.zig` against a prebuilt `SDL3.lib` + include dir. SDL does **not** publish a `build.zig` on any page we fetched — **[UNVERIFIED]**; assume CMake-first and verify before planning a `zig build`-native integration.

**DLL shipping.** `dist/` already ships sidecar DLLs (`SpoutLibrary.dll`) next to `rave-mate.exe`, and `internal/spoutdll` + `internal/appdir` already implement probe-and-load. SDL3.dll would follow the same pattern: vendor the DLL, probe from appdir, fail soft when absent. Note SDL's odd/even ABI rule when pinning the vendored version.

---

## 11. Where SDL3 fits in rave-mate / rave-app

### Candidate uses (real gaps we have)

| Use | SDL3 surface | Why it's attractive |
|---|---|---|
| Generic HID / game-controller input for DJ + VJ hardware | `SDL_Gamepad*`, `SDL_Joystick*`, `SDL_hid_*` | We currently have no HID layer at all (`grep` for `hidapi`/`HidD_`/`SetupDiGetClassDevs` in `internal/` returns nothing). SDL gives cross-platform enumeration, hotplug events, a maintained mapping DB, raw report access, and rumble/LED out of the box. |
| Exposing a synthetic controller | `SDL_AttachVirtualJoystick` + `SDL_SetJoystickVirtual*` | Complements `driver/` (ravemidi) for the non-MIDI half of remote control. |
| Audio **device enumeration + hotplug** | `SDL_GetAudioPlaybackDevices`, `SDL_GetAudioRecordingDevices`, `SDL_GetAudioDeviceName`, `SDL_EVENT_AUDIO_DEVICE_ADDED/REMOVED/FORMAT_CHANGED` | Enumeration and hotplug are portable and cheap even if the engine keeps its own output path. |
| Recording/loopback input for `internal/audiorec` | `SDL_OpenAudioDeviceStream` with a recording device | Portable capture without another platform shim. Caveat: no exclusive-mode/ASIO story documented. |
| Display / monitor enumeration for overlay + medialink placement | `SDL_GetDisplays`, `SDL_GetDisplayName`, `SDL_GetDisplayBounds`-class API, `SDL_DisplayMode`, `SDL_GetDisplayContentScale`, `SDL_EVENT_DISPLAY_*` | Cross-platform, event-driven; today this is per-OS code. |
| Camera capture as a **fallback** path | `SDL_GetCameras`, `SDL_OpenCamera`, `SDL_AcquireCameraFrame` | Only where `internal/webcam`'s Windows UVC path can't run (i.e. non-Windows). See non-fits. |
| GPU-accelerated preview rendering | `SDL_Render*` for 2D; `SDL_GPU*` for shaders/compute | `SDL_UpdateNVTexture`/`SDL_UpdateYUVTexture` map straight onto decoder output. Would be a new preview surface, not a replacement for the WebView2 UI. |
| Cross-platform force feedback | `SDL_Haptic*` | Only if hardware demands it; low priority. |

### Where SDL3 does **not** fit — do not rip these out

| Existing | Why SDL doesn't replace it |
|---|---|
| `internal/mfenc` + `native/zigenc` (Media Foundation / D3D11 encode) | **SDL has no encoder, no muxer, no codec API.** Nothing on any category page. |
| `third_party/spout` + `internal/spoutdll` + `internal/medialink` | SDL has no Spout/NDI/shared-texture interop. GPU API is a rendering API, not a texture-sharing bus. |
| WebView2 UI shell (`webview_go`) + `internal/webui` + `internal/zigui` / `native/zigui` | SDL renders 2D primitives and GPU passes; it is not an HTML/DOM host and not a widget toolkit. Our UI stays where it is. |
| `internal/audio` engine (decoders, resampler, DSP via zig; `oto/v3` output) | SDL audio is portable shared-mode plumbing. It offers no FLAC/MP3/Vorbis/AIFF decode (WAV only: `SDL_LoadWAV`), no exclusive mode, no ASIO. `SDL_AudioStream` overlaps our resample/mix path but does not beat it. At most a future alternative **output backend**, behind the existing interface. |
| `internal/midi`, `internal/midimap`, `driver/` (ravemidi kernel driver) | **SDL3 has no MIDI API** — no MIDI category exists on the FrontPage or in the README index. Zero overlap. |
| `internal/webcam` UVC property control (`uvc_props.go`, `uvc_windows.go`) + `spoutsink.go` | `CategoryCamera` documents no exposure/gain/WB/PTZ control and no Spout sink. Replacing our capture path would lose features. |
| `internal/prodjlink`, `internal/traktor*`, `internal/serato*`, `internal/rekordbox*`, `internal/virtualdj` | Network protocols and on-disk database formats. Not an input-device problem. |
| `internal/vrchat`, `internal/vroverlay`, `internal/zigvr` (OpenVR/OpenXR) | SDL has a `README-xr` page we did **not** fetch — **[UNVERIFIED]** scope. Assume no overlap with our OpenVR overlay work until checked. |
| Fyne / Gio remnants in `go.mod` | Separate migration; SDL is not the answer to that question. |

### Adoption rule of thumb

SDL3 earns its place where we currently have **no** implementation and would otherwise write per-OS code (HID, display enumeration, audio/camera device enumeration, gamepad hotplug). It does not earn its place where a Windows-native path already ships and is faster or more capable (encode, Spout, UVC properties, audio engine, MIDI).

---

## 12. Gotchas / pitfalls (all from the fetched pages)

1. **Return convention flip.** `if (SDL_X() < 0)` compiles-ish and is wrong. SDL3 camel-case functions return `bool`; `false` = failure. Lowercase `SDL_strcmp`-style functions did not change.
2. **`SDL_bool` is gone.** `SDL_TRUE`/`SDL_FALSE` too. C99 `bool`.
3. **Callback mode forbids `main`.** "if you do, the app will likely fail to link." Fatal to naive Go/cgo hosting.
4. **`SDL_Init` and video are main-thread only.** `SDL_INIT_VIDEO` "should be initialized on the main thread."
5. **Subsystem init is ref-counted.** One `SDL_QuitSubSystem` per `SDL_InitSubSystem`.
6. **Audio devices open paused.** `SDL_OpenAudioDeviceStream` returns a paused device by design; call `SDL_ResumeAudioStreamDevice()` or you get silence and no error.
7. **No S24 / F64 audio format.** `SDL_AudioFormat` tops out at S32/F32.
8. **`SDL_LoadWAV` is the only file loader in the audio API.** Everything else is your decoder.
9. **Event timestamps are nanoseconds** and window/display events are top-level, not `SDL_WINDOWEVENT` subtypes. SDL2 dispatch tables port wrong.
10. **`event.key.state` is now `event.key.down`.**
11. **Gamepad face buttons are positional** (South/East/West/North), not A/B/X/Y. Label lookup via `SDL_GetGamepadButtonLabel` / `SDL_GetGamepadButtonLabelForType` if you display them.
12. **No `SDL_INIT_TIMER`.** Timers work without it.
13. **`SDL_ReadIO` is POSIX-like, not stdio-like** — signature and semantics differ from `SDL_RWread`.
14. **`SDL_CreateRenderer` lost its flags/index**; second arg is a driver name string. VSync moved to `SDL_SetRenderVSync` / `SDL_HINT_RENDER_VSYNC`.
15. **`SDL_GL_DestroyContext`**, not `SDL_GL_DeleteContext`.
16. **GPU API has no universal shader language.** You must supply per-backend bytecode matching the `SDL_GPU_SHADERFORMAT_*` bits you declared.
17. **GPU render pass caps**: max 4 color targets + 1 depth; state resets at pass end.
18. **Camera returns no frames until permission is granted** on approval platforms, and the first frames may be black/underexposed by design — discard them.
19. **High-DPI is platform-asymmetric.** Windows/X11 pixel-native vs macOS/Wayland logical-native. Never assume window size == pixel size; use `SDL_GetWindowSizeInPixels`.
20. **`SDL_WINDOW_HIDDEN` windows need an explicit `SDL_ShowWindow`.**
21. **Dynamic API can hot-swap even a static SDL** via `SDL3_DYNAMIC_API`. Fine for users, a support variable for us — pin or disable deliberately.
22. **ABI is backwards- but not forwards-compatible.** Build against the oldest supported SDL3; never ship a runtime older than the build headers.
23. **Odd minor/patch = prerelease.** Don't vendor 3.3.x or 3.4.9 into `dist/`.
24. **Env var names changed**: `SDL_VIDEODRIVER`→`SDL_VIDEO_DRIVER`, `SDL_AUDIODRIVER`→`SDL_AUDIO_DRIVER`.
25. **Many SDL2 hints were deleted** in favor of the properties API. Verify a hint still exists on CategoryHints before using it.
26. **Error string is per-thread.** Read `SDL_GetError()` on the thread that failed, immediately.
27. **HIDAPI can be compiled out** (`SDL_HIDAPI_DISABLED`) — a vendored/system SDL might not have it. Feature-detect.
28. **UWP/WinRT support is gone.** Desktop Win32 only on Windows.
29. **Gestures were removed** from core SDL; pinch events (`SDL_EVENT_PINCH_*`) exist but the SDL2 gesture API does not.

---

## 13. Explicitly NOT verified

- Individual members of `SDL_GamepadButton`, `SDL_GamepadAxis`, `SDL_GamepadType`, `SDL_RendererLogicalPresentation`, `SDL_TextureAccess`, `SDL_SystemCursor`, `SDL_PixelFormat`, `SDL_GPUTextureFormat` — only the enum **type** names were fetched.
- The full GPU function set (~70 per the category page); only the subset listed in §6 appeared on the fetched page.
- Exact window-property keys for adopting an existing HWND (`SDL_WINDOW_EXTERNAL` path).
- Whether SDL ships a `build.zig` / Zig package manifest.
- Any Go binding — none is referenced by the wiki.
- Shader cross-compilation tooling for the GPU API (SPIRV-Cross / DXC / shadercross).
- `README-xr` scope vs our OpenVR/OpenXR work — page not fetched.
- No "Best practices" page exists under that name on the SDL3 wiki; the FrontPage offers Tutorials, Examples, Demos, FAQ instead.
- MIDI: absence inferred from the FrontPage category list and README index containing no MIDI entry — inference, not an explicit upstream statement.

---

## Links

Fetched for this document:

- https://wiki.libsdl.org/SDL3/FrontPage
- https://wiki.libsdl.org/SDL3/READMEs
- https://wiki.libsdl.org/SDL3/README-migration
- https://wiki.libsdl.org/SDL3/README-main-functions
- https://wiki.libsdl.org/SDL3/README-versions
- https://wiki.libsdl.org/SDL3/README-highdpi
- https://wiki.libsdl.org/SDL3/README-cmake
- https://wiki.libsdl.org/SDL3/README-dynapi
- https://wiki.libsdl.org/SDL3/README-windows
- https://wiki.libsdl.org/SDL3/CategoryInit
- https://wiki.libsdl.org/SDL3/CategoryMain
- https://wiki.libsdl.org/SDL3/CategoryVersion
- https://wiki.libsdl.org/SDL3/CategoryError
- https://wiki.libsdl.org/SDL3/CategoryHints
- https://wiki.libsdl.org/SDL3/CategoryVideo
- https://wiki.libsdl.org/SDL3/CategoryEvents
- https://wiki.libsdl.org/SDL3/CategoryRender
- https://wiki.libsdl.org/SDL3/CategoryGPU
- https://wiki.libsdl.org/SDL3/CategoryAudio
- https://wiki.libsdl.org/SDL3/CategoryGamepad
- https://wiki.libsdl.org/SDL3/CategoryJoystick
- https://wiki.libsdl.org/SDL3/CategoryHIDAPI
- https://wiki.libsdl.org/SDL3/CategoryHaptic
- https://wiki.libsdl.org/SDL3/CategoryKeyboard
- https://wiki.libsdl.org/SDL3/CategoryMouse
- https://wiki.libsdl.org/SDL3/CategoryProperties
- https://wiki.libsdl.org/SDL3/CategoryTimer
- https://wiki.libsdl.org/SDL3/CategoryThread
- https://wiki.libsdl.org/SDL3/CategoryMutex
- https://wiki.libsdl.org/SDL3/CategoryAtomic
- https://wiki.libsdl.org/SDL3/CategoryIOStream
- https://wiki.libsdl.org/SDL3/CategoryStorage
- https://wiki.libsdl.org/SDL3/CategoryCamera
- https://wiki.libsdl.org/SDL3/CategoryPixels
- https://wiki.libsdl.org/SDL3/CategorySurface
- https://wiki.libsdl.org/SDL3/SDL_Init
- https://wiki.libsdl.org/SDL3/SDL_InitFlags
- https://wiki.libsdl.org/SDL3/SDL_EventType
- https://wiki.libsdl.org/SDL3/SDL_WindowFlags
- https://wiki.libsdl.org/SDL3/SDL_AudioFormat
- https://wiki.libsdl.org/SDL3/SDL_OpenAudioDeviceStream
- https://wiki.libsdl.org/SDL3/SDL_GPUShaderFormat
- https://wiki.libsdl.org/SDL3/SDL_CreateGPUDevice
- https://github.com/libsdl-org/SDL/releases (release/version check only)

Not fetched, cited as index entries only: INSTALL.md, QuickReference, CategoryAPI, examples.libsdl.org, README-xr.

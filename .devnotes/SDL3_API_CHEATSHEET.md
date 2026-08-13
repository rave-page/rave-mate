# SDL3 API Cheat Sheet

Grep-able name index. Every name verified against the SDL3 wiki (see SDL3_KNOWLEDGEBASE.md § Links). Enum **members** only listed where the member page was fetched; otherwise only the enum type name appears.

Convention reminder: camel-case `SDL_*` functions return `bool` (`false` = error → `SDL_GetError()`).

---

## Init / lifecycle

| Name | Meaning |
|---|---|
| `SDL_Init` | `bool SDL_Init(SDL_InitFlags)` — main thread only |
| `SDL_InitSubSystem` / `SDL_QuitSubSystem` | ref-counted per-subsystem init/teardown |
| `SDL_WasInit` | query initialized subsystems |
| `SDL_Quit` | force full shutdown |
| `SDL_IsMainThread` | is caller the main thread |
| `SDL_RunOnMainThread` | marshal a `SDL_MainThreadCallback` to main thread |
| `SDL_SetAppMetadata` / `SDL_SetAppMetadataProperty` / `SDL_GetAppMetadataProperty` | app name/version/id metadata |
| `SDL_InitFlags` | flags bitfield type |
| `SDL_AppResult` | `SDL_APP_CONTINUE` / `SDL_APP_SUCCESS` / `SDL_APP_FAILURE` |
| `SDL_AppInit_func` `SDL_AppIterate_func` `SDL_AppEvent_func` `SDL_AppQuit_func` | callback typedefs |

Flags: `SDL_INIT_AUDIO` 0x10 · `SDL_INIT_VIDEO` 0x20 · `SDL_INIT_JOYSTICK` 0x200 · `SDL_INIT_HAPTIC` 0x1000 · `SDL_INIT_GAMEPAD` 0x2000 · `SDL_INIT_EVENTS` 0x4000 · `SDL_INIT_SENSOR` 0x8000 · `SDL_INIT_CAMERA` 0x10000. (No `SDL_INIT_TIMER`.)
Implies: AUDIO/VIDEO/JOYSTICK/SENSOR/CAMERA → EVENTS; GAMEPAD → JOYSTICK.

## Main entry

| Name | Meaning |
|---|---|
| `SDL_MAIN_USE_CALLBACKS` | define before `<SDL3/SDL_main.h>` to use callback mode (then write no `main`) |
| `SDL_AppInit(void**appstate,int argc,char**argv)` | one-time startup, returns `SDL_AppResult` |
| `SDL_AppIterate(void*appstate)` | per-frame body |
| `SDL_AppEvent(void*appstate,SDL_Event*)` | event delivery (don't call `SDL_PollEvent`) |
| `SDL_AppQuit(void*appstate,SDL_AppResult)` | cleanup |
| `SDL_main` / `SDL_main_func` | classic entry point |
| `SDL_RunApp` | run classic main |
| `SDL_EnterAppMainCallbacks` | enter callback loop manually |
| `SDL_SetMainReady` | tell SDL main-setup already done |
| `SDL_MAIN_HANDLED` / `SDL_MAIN_NEEDED` / `SDL_MAIN_AVAILABLE` / `SDLMAIN_DECLSPEC` | main macros |
| `SDL_RegisterApp` / `SDL_UnregisterApp` | Windows app registration |
| `SDL_GDKSuspendComplete` | GDK suspend ack |

## Error

`SDL_GetError` · `SDL_SetError` · `SDL_SetErrorV` · `SDL_ClearError` · `SDL_OutOfMemory` · macros `SDL_InvalidParamError`, `SDL_Unsupported`. Per-thread error string.

## Version

`SDL_GetVersion` · `SDL_GetRevision` · `SDL_MAJOR_VERSION` · `SDL_MINOR_VERSION` · `SDL_MICRO_VERSION` · `SDL_REVISION` · `SDL_VERSION` · `SDL_VERSIONNUM` · `SDL_VERSION_ATLEAST` · `SDL_VERSIONNUM_MAJOR|MINOR|MICRO`.

## Hints

`SDL_SetHint` · `SDL_SetHintWithPriority` · `SDL_GetHint` · `SDL_GetHintBoolean` · `SDL_ResetHint` · `SDL_ResetHints` · `SDL_AddHintCallback` · `SDL_RemoveHintCallback` · `SDL_HintCallback` · `SDL_HintPriority`.
Verified hint names: `SDL_HINT_AUDIO_DRIVER` `SDL_HINT_VIDEO_DRIVER` `SDL_HINT_RENDER_DRIVER` `SDL_HINT_GPU_DRIVER` `SDL_HINT_RENDER_VSYNC` `SDL_HINT_RENDER_GPU_DEBUG` `SDL_HINT_JOYSTICK_HIDAPI` `SDL_HINT_JOYSTICK_RAWINPUT` `SDL_HINT_GAMECONTROLLERCONFIG`.
Env: `SDL_VIDEO_DRIVER`, `SDL_AUDIO_DRIVER`, `SDL3_DYNAMIC_API`.

---

## Video / window

**Create/destroy**: `SDL_CreateWindow` · `SDL_CreateWindowWithProperties` · `SDL_CreateWindowAndRenderer` (title first) · `SDL_CreatePopupWindow` · `SDL_DestroyWindow`
**Show/state**: `SDL_ShowWindow` · `SDL_HideWindow` · `SDL_RaiseWindow` · `SDL_MaximizeWindow` · `SDL_MinimizeWindow` · `SDL_RestoreWindow` · `SDL_GetWindowFlags`
**Geometry**: `SDL_SetWindowPosition` · `SDL_SetWindowSize` · `SDL_GetWindowSizeInPixels` · `SDL_GetWindowBordersSize`
**Chrome**: `SDL_SetWindowTitle` · `SDL_GetWindowTitle` · `SDL_SetWindowIcon` · `SDL_SetWindowOpacity` · `SDL_GetWindowOpacity` · `SDL_SetWindowBordered` · `SDL_SetWindowResizable` · `SDL_SetWindowAlwaysOnTop` · `SDL_SetWindowFocusable` · `SDL_SetWindowShape`
**Grab**: `SDL_SetWindowKeyboardGrab` · `SDL_SetWindowMouseGrab`
**IDs/props**: `SDL_GetWindowID` · `SDL_GetWindowFromID` · `SDL_GetWindowProperties`
**Displays**: `SDL_GetDisplays` · `SDL_GetPrimaryDisplay` · `SDL_GetDisplayForWindow` · `SDL_GetDisplayForPoint` · `SDL_GetDisplayName` · `SDL_GetDisplayProperties`
**Fullscreen**: `SDL_SetWindowFullscreen` · `SDL_SetWindowFullscreenMode` · `SDL_GetWindowFullscreenMode` · `SDL_GetFullscreenDisplayModes` · `SDL_GetClosestFullscreenDisplayMode`
**HiDPI**: `SDL_GetWindowPixelDensity` · `SDL_GetWindowDisplayScale` · `SDL_GetDisplayContentScale`
**GL**: `SDL_GL_CreateContext` · `SDL_GL_DestroyContext` · `SDL_GL_MakeCurrent` · `SDL_GL_SwapWindow`
**Types**: `SDL_DisplayMode` · `SDL_DisplayOrientation` · `SDL_SystemTheme` · `SDL_FlashOperation` · `SDL_HitTestResult` · `SDL_ProgressState` · `SDL_GLAttr`
**Pos macros**: `SDL_WINDOWPOS_CENTERED` · `SDL_WINDOWPOS_CENTERED_DISPLAY()` · `SDL_WINDOWPOS_UNDEFINED` · `SDL_WINDOWPOS_ISCENTERED()` · `SDL_WINDOWPOS_ISUNDEFINED()`

**`SDL_WINDOW_*`**: `FULLSCREEN` `OPENGL` `VULKAN` `METAL` `OCCLUDED` `HIDDEN` `BORDERLESS` `RESIZABLE` `MINIMIZED` `MAXIMIZED` `MOUSE_GRABBED` `KEYBOARD_GRABBED` `INPUT_FOCUS` `MOUSE_FOCUS` `MOUSE_CAPTURE` `MOUSE_RELATIVE_MODE` `EXTERNAL` `MODAL` `HIGH_PIXEL_DENSITY` `ALWAYS_ON_TOP` `UTILITY` `TOOLTIP` `POPUP_MENU` `TRANSPARENT` `NOT_FOCUSABLE` `FILL_DOCUMENT` (3.4.0+, Emscripten)

---

## Events

**Pump/read**: `SDL_PumpEvents` · `SDL_PollEvent` · `SDL_WaitEvent` · `SDL_WaitEventTimeout` · `SDL_PeepEvents` · `SDL_PushEvent` · `SDL_RegisterEvents`
**Filter/watch**: `SDL_SetEventFilter` · `SDL_GetEventFilter` · `SDL_FilterEvents` · `SDL_AddEventWatch` · `SDL_RemoveEventWatch` · `SDL_EventFilter`
**Query/flush**: `SDL_HasEvent` · `SDL_HasEvents` · `SDL_FlushEvent` · `SDL_FlushEvents` · `SDL_SetEventEnabled` · `SDL_EventEnabled`
**Helpers**: `SDL_GetWindowFromEvent` · `SDL_GetEventDescription` · `SDL_EventAction` · `SDL_EventType` · `SDL_NotificationID`

### `SDL_EVENT_*` (full verified list)

- App 0x100: `QUIT` `TERMINATING` `LOW_MEMORY` `WILL_ENTER_BACKGROUND` `DID_ENTER_BACKGROUND` `WILL_ENTER_FOREGROUND` `DID_ENTER_FOREGROUND` `LOCALE_CHANGED` `SYSTEM_THEME_CHANGED`
- Display 0x151: `DISPLAY_ORIENTATION` `DISPLAY_ADDED` `DISPLAY_REMOVED` `DISPLAY_MOVED` `DISPLAY_DESKTOP_MODE_CHANGED` `DISPLAY_CURRENT_MODE_CHANGED` `DISPLAY_CONTENT_SCALE_CHANGED` `DISPLAY_USABLE_BOUNDS_CHANGED`
- Window 0x202: `WINDOW_SHOWN` `WINDOW_HIDDEN` `WINDOW_EXPOSED` `WINDOW_MOVED` `WINDOW_RESIZED` `WINDOW_PIXEL_SIZE_CHANGED` `WINDOW_METAL_VIEW_RESIZED` `WINDOW_MINIMIZED` `WINDOW_MAXIMIZED` `WINDOW_RESTORED` `WINDOW_MOUSE_ENTER` `WINDOW_MOUSE_LEAVE` `WINDOW_FOCUS_GAINED` `WINDOW_FOCUS_LOST` `WINDOW_CLOSE_REQUESTED` `WINDOW_HIT_TEST` `WINDOW_ICCPROF_CHANGED` `WINDOW_DISPLAY_CHANGED` `WINDOW_DISPLAY_SCALE_CHANGED` `WINDOW_SAFE_AREA_CHANGED` `WINDOW_OCCLUDED` `WINDOW_ENTER_FULLSCREEN` `WINDOW_LEAVE_FULLSCREEN` `WINDOW_DESTROYED` `WINDOW_HDR_STATE_CHANGED` `WINDOW_SETTINGS_CHANGED`
- Keyboard 0x300: `KEY_DOWN` `KEY_UP` `TEXT_EDITING` `TEXT_INPUT` `KEYMAP_CHANGED` `KEYBOARD_ADDED` `KEYBOARD_REMOVED` `TEXT_EDITING_CANDIDATES` `SCREEN_KEYBOARD_SHOWN` `SCREEN_KEYBOARD_HIDDEN`
- Mouse 0x400: `MOUSE_MOTION` `MOUSE_BUTTON_DOWN` `MOUSE_BUTTON_UP` `MOUSE_WHEEL` `MOUSE_ADDED` `MOUSE_REMOVED`
- Joystick 0x600: `JOYSTICK_AXIS_MOTION` `JOYSTICK_BALL_MOTION` `JOYSTICK_HAT_MOTION` `JOYSTICK_BUTTON_DOWN` `JOYSTICK_BUTTON_UP` `JOYSTICK_ADDED` `JOYSTICK_REMOVED` `JOYSTICK_BATTERY_UPDATED` `JOYSTICK_UPDATE_COMPLETE`
- Gamepad 0x650: `GAMEPAD_AXIS_MOTION` `GAMEPAD_BUTTON_DOWN` `GAMEPAD_BUTTON_UP` `GAMEPAD_ADDED` `GAMEPAD_REMOVED` `GAMEPAD_REMAPPED` `GAMEPAD_TOUCHPAD_DOWN` `GAMEPAD_TOUCHPAD_MOTION` `GAMEPAD_TOUCHPAD_UP` `GAMEPAD_SENSOR_UPDATE` `GAMEPAD_UPDATE_COMPLETE` `GAMEPAD_STEAM_HANDLE_UPDATED` `GAMEPAD_CAPSENSE_TOUCH` `GAMEPAD_CAPSENSE_RELEASE`
- Touch 0x700: `FINGER_DOWN` `FINGER_UP` `FINGER_MOTION` `FINGER_CANCELED`
- Pinch 0x710: `PINCH_BEGIN` `PINCH_UPDATE` `PINCH_END`
- Clipboard 0x900: `CLIPBOARD_UPDATE`
- Drop 0x1000: `DROP_FILE` `DROP_TEXT` `DROP_BEGIN` `DROP_COMPLETE` `DROP_POSITION`
- Audio 0x1100: `AUDIO_DEVICE_ADDED` `AUDIO_DEVICE_REMOVED` `AUDIO_DEVICE_FORMAT_CHANGED`
- Sensor 0x1200: `SENSOR_UPDATE`
- Pen 0x1300: `PEN_PROXIMITY_IN` `PEN_PROXIMITY_OUT` `PEN_DOWN` `PEN_UP` `PEN_BUTTON_DOWN` `PEN_BUTTON_UP` `PEN_MOTION` `PEN_AXIS`
- Camera 0x1400: `CAMERA_DEVICE_ADDED` `CAMERA_DEVICE_REMOVED` `CAMERA_DEVICE_APPROVED` `CAMERA_DEVICE_DENIED`
- Notification 0x1500: `NOTIFICATION_ACTION_INVOKED`
- Render 0x2000: `RENDER_TARGETS_RESET` `RENDER_DEVICE_RESET` `RENDER_DEVICE_LOST`
- Reserved: `PRIVATE0..3` 0x4000 · `POLL_SENTINEL` 0x7F00 · `USER` 0x8000 · `LAST` 0xFFFF

---

## 2D Render

**Create**: `SDL_CreateRenderer(win, const char *name)` · `SDL_CreateRendererWithProperties` · `SDL_CreateSoftwareRenderer` · `SDL_CreateGPURenderer` · `SDL_CreateWindowAndRenderer` · `SDL_DestroyRenderer`
**Drivers**: `SDL_GetNumRenderDrivers` · `SDL_GetRenderDriver` · `SDL_GetRendererName` · `SDL_GetRenderer` · `SDL_GetRenderWindow` · `SDL_GetRendererProperties` · macros `SDL_SOFTWARE_RENDERER` `SDL_GPU_RENDERER`
**Textures**: `SDL_CreateTexture` · `SDL_CreateTextureFromSurface` · `SDL_CreateTextureWithProperties` · `SDL_DestroyTexture` · `SDL_UpdateTexture` · `SDL_UpdateYUVTexture` · `SDL_UpdateNVTexture` · `SDL_LockTexture` · `SDL_LockTextureToSurface` · `SDL_UnlockTexture` · `SDL_GetTextureSize` · `SDL_GetTextureProperties` · `SDL_GetRendererFromTexture` · `SDL_GetTexturePalette` / `SDL_SetTexturePalette`
**Texture state**: `SDL_SetTextureBlendMode` · `SDL_SetTextureAlphaMod`(`Float`) · `SDL_SetTextureColorMod`(`Float`) · `SDL_SetTextureScaleMode` · `SDL_GetDefaultTextureScaleMode` / `SDL_SetDefaultTextureScaleMode` · getters mirror all of these
**Draw**: `SDL_RenderClear` · `SDL_RenderPoint(s)` · `SDL_RenderLine(s)` · `SDL_RenderRect(s)` · `SDL_RenderFillRect(s)` · `SDL_RenderTexture` · `SDL_RenderTextureRotated` · `SDL_RenderTextureAffine` · `SDL_RenderTextureTiled` · `SDL_RenderTexture9Grid` · `SDL_RenderTexture9GridTiled` · `SDL_RenderGeometry` · `SDL_RenderGeometryRaw` · `SDL_RenderDebugText` · `SDL_RenderDebugTextFormat` · `SDL_RenderPresent` · `SDL_FlushRenderer`
**Target/viewport**: `SDL_SetRenderTarget` · `SDL_GetRenderTarget` · `SDL_SetRenderViewport` · `SDL_RenderViewportSet` · `SDL_SetRenderClipRect` · `SDL_RenderClipEnabled` · `SDL_SetRenderScale` · `SDL_SetRenderLogicalPresentation` · `SDL_GetRenderLogicalPresentationRect` · `SDL_GetRenderOutputSize` · `SDL_GetCurrentRenderOutputSize` · `SDL_GetRenderSafeArea`
**Color/vsync**: `SDL_SetRenderDrawColor`(`Float`) · `SDL_SetRenderDrawBlendMode` · `SDL_SetRenderColorScale` · `SDL_SetRenderVSync` · `SDL_GetRenderVSync`
**Readback/coords**: `SDL_RenderReadPixels` · `SDL_RenderCoordinatesFromWindow` · `SDL_RenderCoordinatesToWindow` · `SDL_ConvertEventToRenderCoordinates`
**GPU bridge**: `SDL_CreateGPURenderState` · `SDL_DestroyGPURenderState` · `SDL_SetGPURenderState` · `SDL_SetGPURenderStateFragmentUniforms` · `SDL_SetGPURenderStateSamplerBindings` · `SDL_SetGPURenderStateStorageBuffers` · `SDL_SetGPURenderStateStorageTextures` · `SDL_GetGPURendererDevice` · `SDL_GPURenderState` · `SDL_GPURenderStateCreateInfo`
**Platform**: `SDL_GetRenderMetalLayer` · `SDL_GetRenderMetalCommandEncoder` · `SDL_AddVulkanRenderSemaphores` · `SDL_GDKSuspendRenderer` · `SDL_GDKResumeRenderer`
**Types**: `SDL_Renderer` · `SDL_Texture` · `SDL_Vertex` · `SDL_TextureAccess` · `SDL_TextureAddressMode` · `SDL_RendererLogicalPresentation` · `SDL_DEBUG_TEXT_FONT_CHARACTER_SIZE`

## GPU

`SDL_CreateGPUDevice(SDL_GPUShaderFormat format_flags, bool debug_mode, const char *name)` — name = `"vulkan"` | `"direct3d12"` | `"metal"` | NULL.

**Verified calls**: `SDL_AcquireGPUCommandBuffer` · `SDL_SubmitGPUCommandBuffer` · `SDL_BeginGPURenderPass` · `SDL_EndGPURenderPass` · `SDL_BindGPUGraphicsPipeline` · `SDL_DrawGPUPrimitives` · `SDL_BeginGPUComputePass` · `SDL_DispatchGPUCompute` · `SDL_CreateGPUShader` · `SDL_CreateGPUTexture` · `SDL_UploadToGPUTexture`
**Handles**: `SDL_GPUDevice` `SDL_GPUCommandBuffer` `SDL_GPURenderPass` `SDL_GPUComputePass` `SDL_GPUCopyPass` `SDL_GPUBuffer` `SDL_GPUTransferBuffer` `SDL_GPUTexture` `SDL_GPUSampler` `SDL_GPUShader` `SDL_GPUGraphicsPipeline` `SDL_GPUComputePipeline` `SDL_GPUFence` `SDL_GPUShaderFormat`
**Structs**: `SDL_GPUBufferCreateInfo` `SDL_GPUTextureCreateInfo` `SDL_GPUShaderCreateInfo` `SDL_GPUGraphicsPipelineCreateInfo` `SDL_GPUComputePipelineCreateInfo` `SDL_GPUColorTargetInfo` `SDL_GPUDepthStencilTargetInfo` `SDL_GPUSamplerCreateInfo` `SDL_GPUVertexInputState` `SDL_GPUVertexBufferDescription` `SDL_GPUVertexAttribute` `SDL_GPURasterizerState` `SDL_GPUMultisampleState` `SDL_GPUDepthStencilState` `SDL_GPUColorTargetBlendState` `SDL_GPUColorTargetDescription` `SDL_GPUGraphicsPipelineTargetInfo` `SDL_GPUBlitInfo` `SDL_GPUBufferBinding` `SDL_GPUTextureSamplerBinding` `SDL_GPUStorageBufferReadWriteBinding` `SDL_GPUStorageTextureReadWriteBinding` `SDL_GPUViewport` `SDL_GPUBufferRegion` `SDL_GPUTextureRegion` `SDL_GPUBufferLocation` `SDL_GPUTextureLocation` `SDL_GPUTransferBufferLocation` `SDL_GPUVulkanOptions`
**Enums**: `SDL_GPUBlendFactor` `SDL_GPUBlendOp` `SDL_GPUCompareOp` `SDL_GPUCullMode` `SDL_GPUFillMode` `SDL_GPUFilter` `SDL_GPUFrontFace` `SDL_GPUIndexElementSize` `SDL_GPULoadOp` `SDL_GPUStoreOp` `SDL_GPUPresentMode` `SDL_GPUPrimitiveType` `SDL_GPUSampleCount` `SDL_GPUSamplerAddressMode` `SDL_GPUSamplerMipmapMode` `SDL_GPUShaderStage` `SDL_GPUStencilOp` `SDL_GPUSwapchainComposition` `SDL_GPUTextureFormat` `SDL_GPUTextureType` `SDL_GPUTransferBufferUsage` `SDL_GPUVertexElementFormat` `SDL_GPUVertexInputRate` `SDL_GPUCubeMapFace` `SDL_GPUBufferUsageFlags` `SDL_GPUColorComponentFlags` `SDL_GPUTextureUsageFlags`
**Shader formats**: `SDL_GPU_SHADERFORMAT_INVALID` 0 · `_PRIVATE` 1<<0 · `_SPIRV` 1<<1 (Vulkan) · `_DXBC` 1<<2 (D3D12 SM5_1) · `_DXIL` 1<<3 (D3D12 SM6_0) · `_MSL` 1<<4 (Metal) · `_METALLIB` 1<<5 (Metal precompiled)
**Backends**: Vulkan (Win/Linux/Switch/Android, VK 1.0+ext) · D3D12 (Win10+/Xbox, FL 11_0) · Metal (macOS 10.14+, iOS/tvOS 13+)

---

## Audio

**Device**: `SDL_OpenAudioDevice` · `SDL_OpenAudioDeviceStream(devid, spec, callback, userdata)` · `SDL_CloseAudioDevice` · `SDL_GetAudioPlaybackDevices` · `SDL_GetAudioRecordingDevices` · `SDL_GetAudioDeviceName` · `SDL_GetAudioDeviceFormat` · `SDL_GetAudioDeviceChannelMap` · `SDL_IsAudioDevicePhysical` · `SDL_IsAudioDevicePlayback`
**Device control**: `SDL_PauseAudioDevice` · `SDL_ResumeAudioDevice` · `SDL_AudioDevicePaused` · `SDL_SetAudioDeviceGain` · `SDL_GetAudioDeviceGain`
**Stream lifecycle**: `SDL_CreateAudioStream` · `SDL_DestroyAudioStream` · `SDL_BindAudioStream` · `SDL_BindAudioStreams` · `SDL_UnbindAudioStream` · `SDL_UnbindAudioStreams` · `SDL_GetAudioStreamDevice`
**Data**: `SDL_PutAudioStreamData` · `SDL_PutAudioStreamDataNoCopy` · `SDL_PutAudioStreamPlanarData` · `SDL_GetAudioStreamData` · `SDL_GetAudioStreamQueued` · `SDL_GetAudioStreamAvailable` · `SDL_ClearAudioStream` · `SDL_FlushAudioStream`
**Stream state**: `SDL_SetAudioStreamFormat` · `SDL_GetAudioStreamFormat` · `SDL_SetAudioStreamFrequencyRatio` · `SDL_GetAudioStreamFrequencyRatio` · `SDL_SetAudioStreamGain` · `SDL_GetAudioStreamGain` · `SDL_SetAudioStreamInputChannelMap` · `SDL_SetAudioStreamOutputChannelMap` · `SDL_GetAudioStreamInputChannelMap` · `SDL_GetAudioStreamOutputChannelMap` · `SDL_GetAudioStreamProperties` · `SDL_PauseAudioStreamDevice` · `SDL_ResumeAudioStreamDevice` · `SDL_AudioStreamDevicePaused` · `SDL_LockAudioStream` · `SDL_UnlockAudioStream`
**Callbacks**: `SDL_SetAudioStreamGetCallback` · `SDL_SetAudioStreamPutCallback` · `SDL_SetAudioPostmixCallback` · `SDL_AudioStreamCallback` · `SDL_AudioStreamDataCompleteCallback` · `SDL_AudioPostmixCallback`
**Convert/mix**: `SDL_ConvertAudioSamples` · `SDL_MixAudio` · `SDL_GetSilenceValueForFormat` · `SDL_GetAudioFormatName`
**Files**: `SDL_LoadWAV` · `SDL_LoadWAV_IO` (WAV only)
**Drivers**: `SDL_GetNumAudioDrivers` · `SDL_GetAudioDriver` · `SDL_GetCurrentAudioDriver`
**Types**: `SDL_AudioDeviceID` · `SDL_AudioStream` · `SDL_AudioSpec` · `SDL_AudioFormat`
**Formats**: `SDL_AUDIO_UNKNOWN` `SDL_AUDIO_U8` `SDL_AUDIO_S8` `SDL_AUDIO_S16LE` `SDL_AUDIO_S16BE` `SDL_AUDIO_S32LE` `SDL_AUDIO_S32BE` `SDL_AUDIO_F32LE` `SDL_AUDIO_F32BE` + native aliases `SDL_AUDIO_S16` `SDL_AUDIO_S32` `SDL_AUDIO_F32`
**Defaults**: `SDL_AUDIO_DEVICE_DEFAULT_PLAYBACK` · `SDL_AUDIO_DEVICE_DEFAULT_RECORDING`
**Format macros**: `SDL_AUDIO_BITSIZE` `SDL_AUDIO_BYTESIZE` `SDL_AUDIO_FRAMESIZE` `SDL_AUDIO_ISFLOAT` `SDL_AUDIO_ISINT` `SDL_AUDIO_ISSIGNED` `SDL_AUDIO_ISUNSIGNED` `SDL_AUDIO_ISBIGENDIAN` `SDL_AUDIO_ISLITTLEENDIAN` `SDL_AUDIO_MASK_BITSIZE` `SDL_AUDIO_MASK_FLOAT` `SDL_AUDIO_MASK_SIGNED` `SDL_AUDIO_MASK_BIG_ENDIAN` `SDL_DEFINE_AUDIO_FORMAT`

---

## Gamepad

**Lifecycle**: `SDL_GetGamepads` · `SDL_OpenGamepad` · `SDL_CloseGamepad` · `SDL_HasGamepad` · `SDL_IsGamepad` · `SDL_GamepadConnected` · `SDL_GetGamepadConnectionState` · `SDL_GetGamepadFromID` · `SDL_GetGamepadFromPlayerIndex` · `SDL_GetGamepadJoystick` · `SDL_UpdateGamepads` · `SDL_SetGamepadEventsEnabled` · `SDL_GamepadEventsEnabled`
**State**: `SDL_GetGamepadAxis` · `SDL_GetGamepadButton` · `SDL_GamepadHasAxis` · `SDL_GamepadHasButton` · `SDL_GamepadHasSensor` · `SDL_SetGamepadSensorEnabled` · `SDL_GamepadSensorEnabled` · `SDL_GetGamepadSensorData` · `SDL_GetGamepadSensorDataRate` · `SDL_GetNumGamepadTouchpads` · `SDL_GetNumGamepadTouchpadFingers` · `SDL_GetGamepadTouchpadFinger` · `SDL_GamepadHasCapSense` · `SDL_GetGamepadCapSense`
**Identity**: `SDL_GetGamepadName`(`ForID`) · `SDL_GetGamepadPath`(`ForID`) · `SDL_GetGamepadID` · `SDL_GetGamepadGUIDForID` · `SDL_GetGamepadVendor`(`ForID`) · `SDL_GetGamepadProduct`(`ForID`) · `SDL_GetGamepadProductVersion`(`ForID`) · `SDL_GetGamepadFirmwareVersion` · `SDL_GetGamepadSerial` · `SDL_GetGamepadType`(`ForID`) · `SDL_GetRealGamepadType`(`ForID`) · `SDL_GetGamepadPowerInfo` · `SDL_GetGamepadProperties` · `SDL_GetGamepadSteamHandle` · `SDL_GetGamepadPlayerIndex`(`ForID`) · `SDL_SetGamepadPlayerIndex`
**Mappings**: `SDL_AddGamepadMapping` · `SDL_AddGamepadMappingsFromFile` · `SDL_AddGamepadMappingsFromIO` · `SDL_GetGamepadMapping` · `SDL_GetGamepadMappingForGUID` · `SDL_GetGamepadMappingForID` · `SDL_GetGamepadMappings` · `SDL_SetGamepadMapping` · `SDL_ReloadGamepadMappings` · `SDL_GetGamepadBindings`
**String conv**: `SDL_GetGamepadAxisFromString` · `SDL_GetGamepadStringForAxis` · `SDL_GetGamepadButtonFromString` · `SDL_GetGamepadStringForButton` · `SDL_GetGamepadTypeFromString` · `SDL_GetGamepadStringForType` · `SDL_GetGamepadButtonLabel` · `SDL_GetGamepadButtonLabelForType` · `SDL_GetGamepadAppleSFSymbolsNameForAxis` · `SDL_GetGamepadAppleSFSymbolsNameForButton`
**Output**: `SDL_RumbleGamepad` · `SDL_RumbleGamepadTriggers` · `SDL_SetGamepadLED` · `SDL_SendGamepadEffect`
**Types**: `SDL_Gamepad` · `SDL_GamepadBinding` · `SDL_GamepadAxis` · `SDL_GamepadButton` (face buttons are South/East/West/North) · `SDL_GamepadType` · `SDL_GamepadButtonLabel` · `SDL_GamepadBindingType` · `SDL_GamepadCapSenseType`

## Joystick

**Lifecycle**: `SDL_GetJoysticks` · `SDL_OpenJoystick` · `SDL_CloseJoystick` · `SDL_HasJoystick` · `SDL_JoystickConnected` · `SDL_GetJoystickConnectionState` · `SDL_GetJoystickFromID` · `SDL_GetJoystickFromPlayerIndex` · `SDL_UpdateJoysticks` · `SDL_SetJoystickEventsEnabled` · `SDL_JoystickEventsEnabled` · `SDL_LockJoysticks` · `SDL_TryLockJoysticks` · `SDL_UnlockJoysticks`
**State**: `SDL_GetJoystickAxis` · `SDL_GetJoystickAxisInitialState` · `SDL_GetJoystickButton` · `SDL_GetJoystickHat` · `SDL_GetJoystickBall` · `SDL_GetNumJoystickAxes` · `SDL_GetNumJoystickButtons` · `SDL_GetNumJoystickHats` · `SDL_GetNumJoystickBalls`
**Identity**: `SDL_GetJoystickID` · `SDL_GetJoystickName`(`ForID`) · `SDL_GetJoystickPath`(`ForID`) · `SDL_GetJoystickGUID`(`ForID`) · `SDL_GetJoystickGUIDInfo` · `SDL_GetJoystickType`(`ForID`) · `SDL_GetJoystickVendor`(`ForID`) · `SDL_GetJoystickProduct`(`ForID`) · `SDL_GetJoystickProductVersion`(`ForID`) · `SDL_GetJoystickSerial` · `SDL_GetJoystickFirmwareVersion` · `SDL_GetJoystickPowerInfo` · `SDL_GetJoystickProperties` · `SDL_GetJoystickPlayerIndex`(`ForID`) · `SDL_SetJoystickPlayerIndex`
**Output**: `SDL_RumbleJoystick` · `SDL_RumbleJoystickTriggers` · `SDL_SetJoystickLED` · `SDL_SendJoystickEffect`
**Virtual**: `SDL_AttachVirtualJoystick` · `SDL_DetachVirtualJoystick` · `SDL_IsJoystickVirtual` · `SDL_SetJoystickVirtualAxis` · `SDL_SetJoystickVirtualButton` · `SDL_SetJoystickVirtualHat` · `SDL_SetJoystickVirtualBall` · `SDL_SetJoystickVirtualTouchpad` · `SDL_SendJoystickVirtualSensorData` · `SDL_VirtualJoystickDesc` · `SDL_VirtualJoystickSensorDesc` · `SDL_VirtualJoystickTouchpadDesc`
**Types**: `SDL_Joystick` · `SDL_JoystickID` · `SDL_JoystickType` · `SDL_JoystickConnectionState` · `SDL_JOYSTICK_AXIS_MIN` (-32768) · `SDL_JOYSTICK_AXIS_MAX` (32767)

## HIDAPI

`SDL_hid_init` · `SDL_hid_exit` · `SDL_hid_enumerate` · `SDL_hid_free_enumeration` · `SDL_hid_device_change_count` · `SDL_hid_open` · `SDL_hid_open_path` · `SDL_hid_close` · `SDL_hid_read` · `SDL_hid_read_timeout` · `SDL_hid_write` · `SDL_hid_set_nonblocking` · `SDL_hid_get_feature_report` · `SDL_hid_send_feature_report` · `SDL_hid_get_input_report` · `SDL_hid_get_report_descriptor` · `SDL_hid_get_device_info` · `SDL_hid_get_manufacturer_string` · `SDL_hid_get_product_string` · `SDL_hid_get_serial_number_string` · `SDL_hid_get_indexed_string` · `SDL_hid_get_properties` · `SDL_hid_ble_scan` · `SDL_hid_device` · `SDL_hid_device_info` · `SDL_hid_bus_type`. Build flag: `SDL_HIDAPI_DISABLED`.

## Haptic

`SDL_GetHaptics` · `SDL_OpenHaptic` · `SDL_OpenHapticFromJoystick` · `SDL_OpenHapticFromMouse` · `SDL_CloseHaptic` · `SDL_GetHapticFromID` · `SDL_GetHapticID` · `SDL_GetHapticName`(`ForID`) · `SDL_GetHapticFeatures` · `SDL_GetNumHapticAxes` · `SDL_GetMaxHapticEffects` · `SDL_GetMaxHapticEffectsPlaying` · `SDL_HapticEffectSupported` · `SDL_IsJoystickHaptic` · `SDL_IsMouseHaptic` · `SDL_CreateHapticEffect` · `SDL_UpdateHapticEffect` · `SDL_RunHapticEffect` · `SDL_StopHapticEffect` · `SDL_StopHapticEffects` · `SDL_DestroyHapticEffect` · `SDL_GetHapticEffectStatus` · `SDL_SetHapticGain` · `SDL_SetHapticAutocenter` · `SDL_PauseHaptic` · `SDL_ResumeHaptic` · `SDL_HapticRumbleSupported` · `SDL_InitHapticRumble` · `SDL_PlayHapticRumble` · `SDL_StopHapticRumble`
Types: `SDL_Haptic` `SDL_HapticID` `SDL_HapticEffectID` `SDL_HapticEffectType` `SDL_HapticDirectionType` `SDL_HapticEffect` `SDL_HapticConstant` `SDL_HapticPeriodic` `SDL_HapticCondition` `SDL_HapticRamp` `SDL_HapticLeftRight` `SDL_HapticCustom` `SDL_HapticDirection`

## Keyboard

`SDL_GetKeyboards` · `SDL_HasKeyboard` · `SDL_GetKeyboardNameForID` · `SDL_GetKeyboardFocus` · `SDL_GetKeyboardState` · `SDL_ResetKeyboard` · `SDL_GetModState` · `SDL_SetModState` · `SDL_GetKeyFromScancode` · `SDL_GetScancodeFromKey` · `SDL_GetKeyFromName` · `SDL_GetKeyName` · `SDL_GetScancodeFromName` · `SDL_GetScancodeName` · `SDL_SetScancodeName` · `SDL_StartTextInput` · `SDL_StartTextInputWithProperties` · `SDL_StopTextInput` · `SDL_TextInputActive` · `SDL_SetTextInputArea` · `SDL_GetTextInputArea` · `SDL_ClearComposition` · `SDL_HasScreenKeyboardSupport` · `SDL_ScreenKeyboardShown` · `SDL_KeyboardID` · `SDL_TextInputType` · `SDL_Capitalization`

## Mouse

`SDL_GetMice` · `SDL_HasMouse` · `SDL_GetMouseNameForID` · `SDL_GetMouseFocus` · `SDL_GetMouseState` · `SDL_GetGlobalMouseState` · `SDL_GetRelativeMouseState` · `SDL_CaptureMouse` · `SDL_SetWindowRelativeMouseMode` · `SDL_GetWindowRelativeMouseMode` · `SDL_SetWindowMouseGrab` · `SDL_GetWindowMouseGrab` · `SDL_SetWindowMouseRect` · `SDL_GetWindowMouseRect` · `SDL_WarpMouseInWindow` · `SDL_WarpMouseGlobal` · `SDL_SetRelativeMouseTransform` · `SDL_CreateCursor` · `SDL_CreateColorCursor` · `SDL_CreateSystemCursor` · `SDL_CreateAnimatedCursor` · `SDL_SetCursor` · `SDL_GetCursor` · `SDL_GetDefaultCursor` · `SDL_DestroyCursor` · `SDL_ShowCursor` · `SDL_HideCursor` · `SDL_CursorVisible`
Types: `SDL_Cursor` `SDL_MouseID` `SDL_MouseButtonFlags` `SDL_MouseWheelDirection` `SDL_SystemCursor` `SDL_CursorFrameInfo` `SDL_MouseMotionTransformCallback` `SDL_TOUCH_MOUSEID`

---

## Properties

`SDL_CreateProperties` · `SDL_DestroyProperties` · `SDL_CopyProperties` · `SDL_GetGlobalProperties` · `SDL_LockProperties` · `SDL_UnlockProperties` · `SDL_SetPointerProperty` · `SDL_SetPointerPropertyWithCleanup` · `SDL_SetStringProperty` · `SDL_SetNumberProperty` · `SDL_SetFloatProperty` · `SDL_SetBooleanProperty` · `SDL_GetPointerProperty` · `SDL_GetStringProperty` · `SDL_GetNumberProperty` · `SDL_GetFloatProperty` · `SDL_GetBooleanProperty` · `SDL_HasProperty` · `SDL_GetPropertyType` · `SDL_ClearProperty` · `SDL_EnumerateProperties` · `SDL_GetNumProperties`
Types: `SDL_PropertiesID` `SDL_PropertyType` `SDL_CleanupPropertyCallback` `SDL_EnumeratePropertiesCallback` `SDL_PROP_NAME_STRING`

## Timer

`SDL_GetTicks` · `SDL_GetTicksNS` · `SDL_GetPerformanceCounter` · `SDL_GetPerformanceFrequency` · `SDL_Delay` · `SDL_DelayNS` · `SDL_DelayPrecise` · `SDL_AddTimer` · `SDL_AddTimerNS` · `SDL_RemoveTimer` · `SDL_TimerID` · `SDL_TimerCallback` · `SDL_NSTimerCallback`
Macros: `SDL_MS_TO_NS` `SDL_NS_TO_MS` `SDL_US_TO_NS` `SDL_NS_TO_US` `SDL_SECONDS_TO_NS` `SDL_NS_TO_SECONDS` `SDL_NS_PER_SECOND` `SDL_NS_PER_MS` `SDL_NS_PER_US` `SDL_US_PER_SECOND` `SDL_MS_PER_SECOND`

## Threads / sync / atomics

**Thread**: `SDL_CreateThread` · `SDL_CreateThreadWithProperties` · `SDL_WaitThread` · `SDL_DetachThread` · `SDL_GetThreadID` · `SDL_GetCurrentThreadID` · `SDL_GetThreadName` · `SDL_GetThreadState` · `SDL_SetCurrentThreadPriority` · `SDL_SetTLS` · `SDL_GetTLS` · `SDL_CleanupTLS` · `SDL_Thread` · `SDL_ThreadID` · `SDL_ThreadFunction` · `SDL_ThreadPriority` · `SDL_ThreadState` · `SDL_TLSID` · `SDL_TLSDestructorCallback`
**Mutex**: `SDL_CreateMutex` · `SDL_LockMutex` · `SDL_TryLockMutex` · `SDL_UnlockMutex` · `SDL_DestroyMutex` · `SDL_Mutex`
**RWLock**: `SDL_CreateRWLock` · `SDL_LockRWLockForReading` · `SDL_LockRWLockForWriting` · `SDL_TryLockRWLockForReading` · `SDL_TryLockRWLockForWriting` · `SDL_UnlockRWLock` · `SDL_DestroyRWLock` · `SDL_RWLock`
**Semaphore**: `SDL_CreateSemaphore` · `SDL_WaitSemaphore` · `SDL_TryWaitSemaphore` · `SDL_WaitSemaphoreTimeout` · `SDL_SignalSemaphore` · `SDL_GetSemaphoreValue` · `SDL_DestroySemaphore` · `SDL_Semaphore`
**Condition**: `SDL_CreateCondition` · `SDL_WaitCondition` · `SDL_WaitConditionTimeout` · `SDL_SignalCondition` · `SDL_BroadcastCondition` · `SDL_DestroyCondition` · `SDL_Condition`
**One-time init**: `SDL_ShouldInit` · `SDL_SetInitialized` · `SDL_ShouldQuit` · `SDL_InitState` · `SDL_InitStatus`
**Atomics**: `SDL_GetAtomicInt` · `SDL_SetAtomicInt` · `SDL_AddAtomicInt` · `SDL_CompareAndSwapAtomicInt` · `SDL_GetAtomicU32` · `SDL_SetAtomicU32` · `SDL_AddAtomicU32` · `SDL_CompareAndSwapAtomicU32` · `SDL_GetAtomicPointer` · `SDL_SetAtomicPointer` · `SDL_CompareAndSwapAtomicPointer` · `SDL_LockSpinlock` · `SDL_TryLockSpinlock` · `SDL_UnlockSpinlock` · `SDL_MemoryBarrierAcquireFunction` · `SDL_MemoryBarrierReleaseFunction` · `SDL_AtomicInt` · `SDL_AtomicU32` · `SDL_SpinLock`
Macros: `SDL_MemoryBarrierAcquire` `SDL_MemoryBarrierRelease` `SDL_CompilerBarrier` `SDL_CPUPauseInstruction` `SDL_AtomicIncRef` `SDL_AtomicDecRef`

## IOStream

`SDL_IOFromFile` · `SDL_IOFromMem` · `SDL_IOFromConstMem` · `SDL_IOFromDynamicMem` · `SDL_OpenIO` · `SDL_CloseIO` · `SDL_ReadIO` · `SDL_WriteIO` · `SDL_SeekIO` · `SDL_TellIO` · `SDL_FlushIO` · `SDL_GetIOSize` · `SDL_GetIOStatus` · `SDL_GetIOProperties` · `SDL_LoadFile` · `SDL_LoadFile_IO` · `SDL_SaveFile` · `SDL_SaveFile_IO` · `SDL_IOprintf` · `SDL_IOvprintf`
Endian I/O: `SDL_ReadU8` `SDL_ReadS8` `SDL_ReadU16LE` `SDL_ReadU16BE` `SDL_ReadS16LE` `SDL_ReadS16BE` `SDL_ReadU32LE` `SDL_ReadU32BE` `SDL_ReadS32LE` `SDL_ReadS32BE` `SDL_ReadU64LE` `SDL_ReadU64BE` `SDL_ReadS64LE` `SDL_ReadS64BE` + matching `SDL_Write*`
Types: `SDL_IOStream` `SDL_IOStreamInterface` `SDL_IOStatus` `SDL_IOWhence` (`SDL_IO_SEEK_SET`)

## Storage

`SDL_OpenTitleStorage` · `SDL_OpenUserStorage` · `SDL_OpenFileStorage` · `SDL_OpenStorage` · `SDL_CloseStorage` · `SDL_StorageReady` · `SDL_ReadStorageFile` · `SDL_WriteStorageFile` · `SDL_GetStorageFileSize` · `SDL_GetStoragePathInfo` · `SDL_GetStorageSpaceRemaining` · `SDL_EnumerateStorageDirectory` · `SDL_GlobStorageDirectory` · `SDL_CreateStorageDirectory` · `SDL_CopyStorageFile` · `SDL_RenameStoragePath` · `SDL_RemoveStoragePath` · `SDL_Storage` · `SDL_StorageInterface`

## Camera

`SDL_GetCameras` · `SDL_OpenCamera` · `SDL_CloseCamera` · `SDL_AcquireCameraFrame` · `SDL_ReleaseCameraFrame` · `SDL_GetCameraFormat` · `SDL_GetCameraSupportedFormats` · `SDL_GetCameraName` · `SDL_GetCameraID` · `SDL_GetCameraPosition` · `SDL_GetCameraProperties` · `SDL_GetCameraPermissionState` · `SDL_GetNumCameraDrivers` · `SDL_GetCameraDriver` · `SDL_GetCurrentCameraDriver`
Types: `SDL_Camera` `SDL_CameraID` `SDL_CameraSpec` `SDL_CameraPermissionState` `SDL_CameraPosition`
Flow: enumerate → open → acquire → release → close. No frames until approved (`SDL_EVENT_CAMERA_DEVICE_APPROVED` / `_DENIED`). Discard warmup frames.

## Surface / pixels

**Surface**: `SDL_CreateSurface` · `SDL_CreateSurfaceFrom` · `SDL_DuplicateSurface` · `SDL_DestroySurface` · `SDL_ConvertSurface` · `SDL_ConvertSurfaceAndColorspace` · `SDL_ConvertPixels` · `SDL_ConvertPixelsAndColorspace` · `SDL_ScaleSurface` · `SDL_StretchSurface` · `SDL_RotateSurface` · `SDL_FlipSurface` · `SDL_LockSurface` · `SDL_UnlockSurface` · `SDL_ReadSurfacePixel`(`Float`) · `SDL_WriteSurfacePixel`(`Float`) · `SDL_MapSurfaceRGB`(`A`) · `SDL_BlitSurface` · `SDL_BlitSurfaceScaled` · `SDL_BlitSurfaceUnchecked` · `SDL_BlitSurfaceTiled` · `SDL_BlitSurface9Grid` · `SDL_FillSurfaceRect`(`s`) · `SDL_ClearSurface` · `SDL_LoadBMP` · `SDL_LoadPNG` · `SDL_LoadJPG` · `SDL_SaveBMP` · `SDL_SavePNG` · `SDL_Get/SetSurfaceClipRect` · `SDL_Get/SetSurfaceColorKey` · `SDL_Get/SetSurfaceAlphaMod` · `SDL_Get/SetSurfaceColorMod` · `SDL_Get/SetSurfaceBlendMode` · `SDL_Get/SetSurfaceColorspace` · `SDL_Surface` · `SDL_FlipMode` · `SDL_ScaleMode`
**Pixels**: `SDL_GetPixelFormatDetails` · `SDL_GetPixelFormatName` · `SDL_MapRGB` · `SDL_MapRGBA` · `SDL_GetRGB` · `SDL_GetRGBA` · `SDL_CreatePalette` · `SDL_DestroyPalette` · `SDL_Color` · `SDL_FColor` · `SDL_Palette` · `SDL_PixelFormatDetails` · `SDL_PixelFormat` · `SDL_Colorspace` · `SDL_ColorType` · `SDL_ColorRange` · `SDL_ChromaLocation` · `SDL_ColorPrimaries` · `SDL_TransferCharacteristics` · `SDL_MatrixCoefficients`

---

## Build knobs

`-DSDL_SHARED=ON|OFF` · `-DSDL_STATIC=ON|OFF` · `-DCMAKE_POSITION_INDEPENDENT_CODE=ON` · `find_package(SDL3)` → `SDL3::SDL3` / `SDL3::SDL3-shared` / `SDL3::SDL3-static` / `SDL3::Headers` / `SDL3::SDL3_test` · `add_subdirectory(vendored/SDL)` · `-DCMAKE_SYSTEM_NAME=` · `-DCMAKE_OSX_SYSROOT=` · `-DCMAKE_OSX_ARCHITECTURES="arm64;x86_64"` · env `SDL3_DYNAMIC_API=/path/to/libSDL3.so.0` · build flag `SDL_HIDAPI_DISABLED`

## Migration quick-swap (SDL2 → SDL3)

`SDL_bool`→`bool` · `SDL_TRUE/FALSE`→`true/false` · `ret < 0`→`!ret` · `SDL_INIT_GAMECONTROLLER`→`SDL_INIT_GAMEPAD` · `SDL_GameControllerOpen`→`SDL_OpenGamepad` · `SDL_GameControllerGetAxis`→`SDL_GetGamepadAxis` · `SDL_ControllerAxisEvent`→`SDL_GamepadAxisEvent` · `event.key.state`→`event.key.down` · `SDL_WINDOWEVENT`→`SDL_EVENT_WINDOW_*` · `SDL_DISPLAYEVENT`→`SDL_EVENT_DISPLAY_*` · `SDL_RWops`→`SDL_IOStream` · `SDL_RWread`→`SDL_ReadIO` · `SDL_RWwrite`→`SDL_WriteIO` · `RW_SEEK_SET`→`SDL_IO_SEEK_SET` · `AUDIO_S16`→`SDL_AUDIO_S16LE` · `SDL_OpenAudioDevice`+cb→`SDL_OpenAudioDeviceStream` · `SDL_QueueAudio`/`SDL_DequeueAudio`/`SDL_AudioCVT`/`SDL_OpenAudio` → gone · `SDL_GL_DeleteContext`→`SDL_GL_DestroyContext` · `SDL_QuitRequested`→`SDL_PeepEvents` · `SDL_VIDEODRIVER`→`SDL_VIDEO_DRIVER` · `SDL_AUDIODRIVER`→`SDL_AUDIO_DRIVER`
Upstream scripts: `rename_symbols.py` `rename_headers.py` `rename_macros.py`

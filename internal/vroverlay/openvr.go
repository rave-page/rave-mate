//go:build vr

package vroverlay

/*
#cgo CFLAGS: -I${SRCDIR}/sdk -std=gnu11

#include <stdlib.h>
#include <stdio.h>
#include <stdint.h>
#include <string.h>
#include <math.h>
#include "openvr_capi.h"

#ifdef _WIN32
#include <windows.h>
#define VRLIB_NAME "openvr_api.dll"
#else
#include <dlfcn.h>
#define VRLIB_NAME "libopenvr_api.so"
#endif

// openvr_api's flat global entry points (the header keeps them behind #if 0). We resolve them at
// RUNTIME (LoadLibrary/dlopen + GetProcAddress/dlsym) rather than link-time, so a MISSING openvr
// library only disables VR - the rest of rave-mate runs fine. Mirrors the Spout backend; means the
// vr-tagged build no longer hard-depends on openvr_api.dll being present to launch.
typedef intptr_t (*pfn_VR_InitInternal)(EVRInitError *, EVRApplicationType);
typedef void (*pfn_VR_ShutdownInternal)(void);
typedef intptr_t (*pfn_VR_GetGenericInterface)(const char *, EVRInitError *);
typedef bool (*pfn_VR_IsRuntimeInstalled)(void);

static pfn_VR_InitInternal        p_VR_InitInternal = NULL;
static pfn_VR_ShutdownInternal    p_VR_ShutdownInternal = NULL;
static pfn_VR_GetGenericInterface p_VR_GetGenericInterface = NULL;
static pfn_VR_IsRuntimeInstalled  p_VR_IsRuntimeInstalled = NULL;
static int g_vr_loaded = 0;

#ifdef _WIN32
static void *vr_sym(HMODULE h, const char *n) { return (void *)GetProcAddress(h, n); }
#else
static void *vr_sym(void *h, const char *n) { return dlsym(h, n); }
#endif

// mate_load_openvr resolves the entry points at runtime. Returns 1 once loaded (idempotent), 0 if
// the library is absent or missing a symbol. Safe to call repeatedly + before every VR use.
static int mate_load_openvr(void) {
	if (g_vr_loaded) return 1;
#ifdef _WIN32
	HMODULE h = LoadLibraryA(VRLIB_NAME);
#else
	void *h = dlopen(VRLIB_NAME, RTLD_NOW);
#endif
	if (!h) return 0;
	p_VR_InitInternal = (pfn_VR_InitInternal)vr_sym(h, "VR_InitInternal");
	p_VR_ShutdownInternal = (pfn_VR_ShutdownInternal)vr_sym(h, "VR_ShutdownInternal");
	p_VR_GetGenericInterface = (pfn_VR_GetGenericInterface)vr_sym(h, "VR_GetGenericInterface");
	p_VR_IsRuntimeInstalled = (pfn_VR_IsRuntimeInstalled)vr_sym(h, "VR_IsRuntimeInstalled");
	if (!p_VR_InitInternal || !p_VR_ShutdownInternal || !p_VR_GetGenericInterface || !p_VR_IsRuntimeInstalled)
		return 0;
	g_vr_loaded = 1;
	return 1;
}

static struct VR_IVROverlay_FnTable      *g_ov = NULL;
static struct VR_IVRSystem_FnTable       *g_sys = NULL;
static struct VR_IVRApplications_FnTable  *g_app = NULL;
static struct VR_IVRCompositor_FnTable    *g_comp = NULL;
static struct VR_IVRInput_FnTable         *g_input = NULL;

// SteamVR Input action handles (resolved from our action manifest). Bindings are user-editable in
// SteamVR's controller-binding UI - that's the "pick any input / combine inputs" surface.
static VRActionSetHandle_t g_set_main   = 0;
static VRActionHandle_t    g_act_toggle = 0; // /actions/main/in/toggle_editor
static VRActionHandle_t    g_act_hide   = 0; // /actions/main/in/toggle_overlays
static VRActionHandle_t    g_act_grab   = 0; // /actions/main/in/grab
static VRActionHandle_t    g_act_summon = 0; // /actions/main/in/summon (open editor / tap-hide; replaces dead legacy button poll)
static VRActionHandle_t    g_act_pclick = 0; // /actions/main/in/pointer_click (trigger; activate the ray-pointed rave-mate overlay - coexists with the game)
static VRActionHandle_t    g_act_pp     = 0; // /actions/main/in/push_pull (vector2)
static VRActionHandle_t    g_act_aim    = 0; // /actions/main/in/aim (pose; the controller AIM/tip pose for the ray pointer - where you point, not the raw device forward)
static VRActionHandle_t    g_act_haptic = 0; // /actions/main/out/haptic (vibration; rumble on grab engage/drop)
static VRInputValueHandle_t g_src_left  = 0; // /user/hand/left  input source (restrict the aim pose to a hand)
static VRInputValueHandle_t g_src_right = 0; // /user/hand/right
static VRActionHandle_t    g_act_slot[8] = {0}; // /actions/main/in/slot1..8 (user-mapped to app actions)

// mate_init starts an overlay-app session + resolves the IVROverlay/IVRSystem fn-tables.
// Returns 0 on success, else the EVRInitError code (nonzero) - SteamVR not running yields nonzero.
static int mate_init(void) {
	if (!mate_load_openvr()) return -3; // openvr_api library absent → VR unavailable (app unaffected)
	if (!p_VR_IsRuntimeInstalled()) return -1;
	EVRInitError e = EVRInitError_VRInitError_None;
	p_VR_InitInternal(&e, EVRApplicationType_VRApplication_Overlay);
	if (e != EVRInitError_VRInitError_None) return (int)e;
	char buf[128];
	snprintf(buf, sizeof(buf), "FnTable:%s", IVROverlay_Version);
	g_ov = (struct VR_IVROverlay_FnTable *)p_VR_GetGenericInterface(buf, &e);
	if (e != EVRInitError_VRInitError_None || !g_ov) return e ? (int)e : -2;
	snprintf(buf, sizeof(buf), "FnTable:%s", IVRSystem_Version);
	g_sys = (struct VR_IVRSystem_FnTable *)p_VR_GetGenericInterface(buf, &e); // optional (controller snap)
	snprintf(buf, sizeof(buf), "FnTable:%s", IVRApplications_Version);
	g_app = (struct VR_IVRApplications_FnTable *)p_VR_GetGenericInterface(buf, &e); // optional (auto-start)
	snprintf(buf, sizeof(buf), "FnTable:%s", IVRCompositor_Version);
	g_comp = (struct VR_IVRCompositor_FnTable *)p_VR_GetGenericInterface(buf, &e); // optional (perf telemetry)
	snprintf(buf, sizeof(buf), "FnTable:%s", IVRInput_Version);
	g_input = (struct VR_IVRInput_FnTable *)p_VR_GetGenericInterface(buf, &e); // optional (custom keybinds)
	return 0;
}

// mate_input_init loads our action manifest + resolves the action/set handles. Returns 0 on success.
// SteamVR generates default bindings from the manifest and exposes them in its binding UI for rebind.
static int mate_input_init(const char *manifestPath) {
	if (!g_input) return -1;
	EVRInputError e = g_input->SetActionManifestPath((char *)manifestPath);
	if (e != EVRInputError_VRInputError_None) return (int)e;
	g_input->GetActionSetHandle("/actions/main", &g_set_main);
	g_input->GetActionHandle("/actions/main/in/toggle_editor", &g_act_toggle);
	g_input->GetActionHandle("/actions/main/in/toggle_overlays", &g_act_hide);
	g_input->GetActionHandle("/actions/main/in/grab", &g_act_grab);
	g_input->GetActionHandle("/actions/main/in/summon", &g_act_summon);
	g_input->GetActionHandle("/actions/main/in/pointer_click", &g_act_pclick);
	g_input->GetActionHandle("/actions/main/in/push_pull", &g_act_pp);
	g_input->GetActionHandle("/actions/main/in/aim", &g_act_aim);
	g_input->GetActionHandle("/actions/main/out/haptic", &g_act_haptic);
	g_input->GetInputSourceHandle("/user/hand/left", &g_src_left);
	g_input->GetInputSourceHandle("/user/hand/right", &g_src_right);
	for (int i = 0; i < 8; i++) {
		char path[64];
		snprintf(path, sizeof(path), "/actions/main/in/slot%d", i + 1);
		g_input->GetActionHandle(path, &g_act_slot[i]);
	}
	return g_set_main ? 0 : -2;
}

static int mate_input_ready(void) { return (g_input && g_set_main) ? 1 : 0; }

// Per-frame device-pose cache. GetDeviceToAbsoluteTrackingPose fetches EVERY device's pose - calling
// it per-object (aim + HMD + billboard, 2–4×/frame at 90 Hz) is the exact anti-pattern OpenVR warns
// about and showed up as measurable system load. mate_input_update (already called exactly once per
// input frame, before any reads) bumps the frame; mate_device_pose refetches the array only when the
// frame changed - so every DevicePose caller in a frame shares ONE fetch.
static uint64_t g_pose_frame = 1;      // current input frame (starts >0 so cache_frame=0 is stale)
static uint64_t g_pose_cache_frame = 0;
static struct TrackedDevicePose_t g_pose_cache[64];

// mate_input_update pumps the action set each frame (required before reading action data).
static void mate_input_update(void) {
	g_pose_frame++;
	if (!g_input || !g_set_main) return;
	struct VRActiveActionSet_t as; memset(&as, 0, sizeof(as));
	as.ulActionSet = g_set_main;
	g_input->UpdateActionState(&as, sizeof(as), 1);
}

// mate_digital returns bit0=state(held) | bit1=changed(this update); 0 if inactive/unbound.
static int mate_digital(VRActionHandle_t a) {
	if (!g_input || !a) return 0;
	struct InputDigitalActionData_t d; memset(&d, 0, sizeof(d));
	if (g_input->GetDigitalActionData(a, &d, sizeof(d), 0) != EVRInputError_VRInputError_None) return 0;
	if (!d.bActive) return 0;
	int r = 0;
	if (d.bState)   r |= 1;
	if (d.bChanged) r |= 2;
	return r;
}

static int mate_act_toggle(void) { return mate_digital(g_act_toggle); }
static int mate_act_hide(void)   { return mate_digital(g_act_hide); }
static int mate_act_grab(void)   { return mate_digital(g_act_grab); }
static int mate_act_summon(void) { return mate_digital(g_act_summon); }
static int mate_act_pclick(void) { return mate_digital(g_act_pclick); }

// mate_digital_hand reads a digital action restricted to one hand's input source (which hand pulled).
static int mate_digital_hand(VRActionHandle_t a, VRInputValueHandle_t src) {
	if (!g_input || !a) return 0;
	struct InputDigitalActionData_t d; memset(&d, 0, sizeof(d));
	if (g_input->GetDigitalActionData(a, &d, sizeof(d), src) != EVRInputError_VRInputError_None) return 0;
	if (!d.bActive) return 0;
	int r = 0;
	if (d.bState)   r |= 1;
	if (d.bChanged) r |= 2;
	return r;
}

// mate_pclick_hand returns the pointer_click trigger state for ONE hand (bit0=held, bit1=changed) -
// for active-hand detection (the hand that pulls the trigger drives the ray pointer).
static int mate_pclick_hand(int leftHand) { return mate_digital_hand(g_act_pclick, leftHand ? g_src_left : g_src_right); }

// mate_slot_edges returns a bitmask of slots pressed THIS tick (bit i = slot i+1 rising edge).
static unsigned int mate_slot_edges(void) {
	unsigned int m = 0;
	for (int i = 0; i < 8; i++) {
		int d = mate_digital(g_act_slot[i]);
		if ((d & 1) && (d & 2)) m |= (1u << i); // state && changed = press edge
	}
	return m;
}

// mate_open_binding_ui opens SteamVR's controller-binding screen for this app's action set, so the
// user can assign physical inputs (incl. chords) to the slots. Returns the EVRInputError (0 = ok).
static int mate_open_binding_ui(void) {
	if (!g_input) return -1;
	return (int)g_input->OpenBindingUI("", g_set_main, 0, 0); // bShowOnDesktop=false → open IN the headset (true opened on the 2D desktop, invisible in VR)
}

static float mate_act_pushpull(void) {
	if (!g_input || !g_act_pp) return 0;
	struct InputAnalogActionData_t d; memset(&d, 0, sizeof(d));
	if (g_input->GetAnalogActionData(g_act_pp, &d, sizeof(d), 0) != EVRInputError_VRInputError_None) return 0;
	if (!d.bActive) return 0;
	return d.y;
}

// mate_thumb_vec_hand reads the push_pull thumbstick vector2 (x,y in −1..1) for ONE hand's input
// source. Reuses the already-bound push_pull action so edit-mode nudge works with existing bindings
// (no new actions that a stale user profile wouldn't have). x/y stay 0 when the hand is inactive.
static void mate_thumb_vec_hand(int leftHand, float *outX, float *outY) {
	*outX = 0; *outY = 0;
	if (!g_input || !g_act_pp) return;
	struct InputAnalogActionData_t d; memset(&d, 0, sizeof(d));
	if (g_input->GetAnalogActionData(g_act_pp, &d, sizeof(d), leftHand ? g_src_left : g_src_right) != EVRInputError_VRInputError_None) return;
	if (!d.bActive) return;
	*outX = d.x; *outY = d.y;
}

// mate_origin_names appends the localized names of every physical input bound to an action (what
// the user's SteamVR binding maps it to) - "(nothing)" if unbound.
static void mate_origin_names(VRActionHandle_t a, char *buf, int buflen) {
	buf[0] = 0;
	if (!g_input || !a) return;
	VRInputValueHandle_t origins[8]; memset(origins, 0, sizeof(origins));
	if (g_input->GetActionOrigins(g_set_main, a, origins, 8) != EVRInputError_VRInputError_None) return;
	for (int i = 0; i < 8 && origins[i]; i++) {
		char name[128]; name[0] = 0;
		g_input->GetOriginLocalizedName(origins[i], name, sizeof(name), 7); // hand|controllerType|inputSource
		if (buf[0]) strncat(buf, ", ", buflen - strlen(buf) - 1);
		strncat(buf, name, buflen - strlen(buf) - 1);
	}
}

// mate_input_diag writes a human-readable dump of the action set: whether the manifest loaded, and
// per action its live active/state + what physical input(s) it's bound to. For debugging why a
// binding "does nothing" (unbound / manifest not loaded / not tracked).
static void mate_input_diag(char *buf, int buflen) {
	buf[0] = 0;
	if (!g_input) { snprintf(buf, buflen, "IVRInput unavailable (old SteamVR?)\n"); return; }
	if (!g_set_main) { snprintf(buf, buflen, "action manifest NOT loaded - SetActionManifestPath failed (check binding files)\n"); return; }
	struct { const char *n; VRActionHandle_t h; } acts[7] = {
		{"summon", g_act_summon}, {"toggle_editor", g_act_toggle}, {"toggle_overlays", g_act_hide},
		{"grab", g_act_grab}, {"push_pull", g_act_pp}, {"pointer_click", g_act_pclick},
		{"aim (pose/tip)", g_act_aim}, // pose action: bound=[…] confirms the ray-pointer aim binding applied
	};
	for (int i = 0; i < 7; i++) {
		int active = 0, state = 0;
		if (acts[i].h && acts[i].h == g_act_aim) {
			// pose action: reading it as digital always reports inactive (the old false "aim active=0")
			struct InputPoseActionData_t pd; memset(&pd, 0, sizeof(pd));
			if (g_input->GetPoseActionDataForNextFrame(acts[i].h, ETrackingUniverseOrigin_TrackingUniverseStanding, &pd, sizeof(pd), 0) == EVRInputError_VRInputError_None) {
				active = pd.bActive; state = pd.pose.bPoseIsValid;
			}
		} else if (acts[i].h) {
			struct InputDigitalActionData_t d; memset(&d, 0, sizeof(d));
			if (g_input->GetDigitalActionData(acts[i].h, &d, sizeof(d), 0) == EVRInputError_VRInputError_None) {
				active = d.bActive; state = d.bState;
			}
		}
		char origins[256]; mate_origin_names(acts[i].h, origins, sizeof(origins));
		char line[384];
		snprintf(line, sizeof(line), "%-15s active=%d state=%d bound=[%s]\n",
			acts[i].n, active, state, origins[0] ? origins : "(nothing)");
		strncat(buf, line, buflen - strlen(buf) - 1);
	}
}

// mate_action_binding fills buf with the localized physical inputs bound to the action at actionPath
// ("" if unbound / unknown / manifest not loaded). Resolves the handle by path, then reuses the shared
// mate_origin_names (GetActionOrigins + GetOriginLocalizedName) for that one action.
static void mate_action_binding(const char *actionPath, char *buf, int buflen) {
	buf[0] = 0;
	if (!g_input || !g_set_main) return;
	VRActionHandle_t a = 0;
	if (g_input->GetActionHandle((char *)actionPath, &a) != EVRInputError_VRInputError_None || !a) return;
	mate_origin_names(a, buf, buflen);
}

// mate_digital_live / mate_analog_live: 1 if the action is bound to a LIVE input source right now
// (bActive) - SteamVR's honest "this action resolves to a real input" flag. Unlike GetActionOrigins it
// doesn't lag SteamVR's async binding load, so it doesn't false-report a just-loaded profile as empty.
static int mate_digital_live(VRActionHandle_t a) {
	if (!g_input || !a) return 0;
	struct InputDigitalActionData_t d; memset(&d, 0, sizeof(d));
	if (g_input->GetDigitalActionData(a, &d, sizeof(d), 0) != EVRInputError_VRInputError_None) return 0;
	return d.bActive ? 1 : 0;
}
static int mate_analog_live(VRActionHandle_t a) {
	if (!g_input || !a) return 0;
	struct InputAnalogActionData_t d; memset(&d, 0, sizeof(d));
	if (g_input->GetAnalogActionData(a, &d, sizeof(d), 0) != EVRInputError_VRInputError_None) return 0;
	return d.bActive ? 1 : 0;
}

// mate_binding_status: -1 = manifest not loaded, 0 = loaded but NO action bound to a live input
// (a genuinely empty binding), 1 = ≥1 action bound. Uses bActive (SteamVR's truthful signal) rather
// than GetActionOrigins - the latter lags the async binding load AND needs origins re-resolved, so it
// false-reported a user's CUSTOM (renamed) profile as fully "unbound" right after load. That drove both
// the bogus "no controller bindings" warning and the aim-pose drift. Must be evaluated on the LIVE tick.
static int mate_binding_status(void) {
	if (!g_input || !g_set_main) return -1;
	VRActionHandle_t digs[5] = { g_act_summon, g_act_toggle, g_act_hide, g_act_grab, g_act_pclick };
	for (int i = 0; i < 5; i++) if (mate_digital_live(digs[i])) return 1;
	for (int i = 0; i < 8; i++) if (mate_digital_live(g_act_slot[i])) return 1;
	if (mate_analog_live(g_act_pp)) return 1;
	return 0;
}

// mate_perf samples compositor frame timing + cumulative stats + the HMD refresh + model. Frame
// counters are cumulative (Go deltas them per sample). Returns 0 if the compositor isn't available.
typedef struct {
	float displayHz;
	float frameMs;      // m_flClientFrameIntervalMs
	float gpuMs;        // m_flTotalRenderGpuMs
	float compositorMs; // m_flCompositorRenderCpuMs
	int   reprojecting;
	unsigned int cumDropped;
	unsigned int cumReprojected;
} mate_perf;

static int mate_perf_get(mate_perf *o, char *model, int modelLen) {
	if (!g_comp) return 0;
	memset(o, 0, sizeof(*o));
	struct Compositor_FrameTiming ft; memset(&ft, 0, sizeof(ft)); ft.m_nSize = sizeof(ft);
	if (g_comp->GetFrameTiming(&ft, 0)) {
		o->frameMs = ft.m_flClientFrameIntervalMs;
		o->gpuMs = ft.m_flTotalRenderGpuMs;
		o->compositorMs = ft.m_flCompositorRenderCpuMs;
		o->reprojecting = (ft.m_nReprojectionFlags != 0) || (ft.m_nNumFramePresents > 1);
	}
	struct Compositor_CumulativeStats cs; memset(&cs, 0, sizeof(cs));
	g_comp->GetCumulativeStats(&cs, sizeof(cs));
	o->cumDropped = cs.m_nNumDroppedFrames;
	o->cumReprojected = cs.m_nNumReprojectedFrames;
	if (g_sys) {
		ETrackedPropertyError pe = ETrackedPropertyError_TrackedProp_Success;
		o->displayHz = g_sys->GetFloatTrackedDeviceProperty(0, ETrackedDeviceProperty_Prop_DisplayFrequency_Float, &pe);
		if (model && modelLen > 0) {
			g_sys->GetStringTrackedDeviceProperty(0, ETrackedDeviceProperty_Prop_ModelNumber_String, model, (uint32_t)modelLen, &pe);
		}
	}
	return 1;
}

// mate_runtime_installed reports whether a SteamVR runtime is installed (cheap; no session needed).
static int mate_runtime_installed(void) { return (mate_load_openvr() && p_VR_IsRuntimeInstalled()) ? 1 : 0; }

// mate_poll_quit drains system events; returns the first session-fatal one (caller then tears down +
// reconnects): 0 = none, 1 = Quit/ProcessQuit, 2 = DriverRequestedQuit/RestartRequested, 3 = HMD lost
// (TrackedDeviceDeactivated for device 0 ONLY - controllers deactivate routinely on standby).
static int mate_poll_quit(void) {
	if (!g_sys) return 0;
	struct VREvent_t ev;
	while (g_sys->PollNextEvent(&ev, sizeof(ev))) {
		switch (ev.eventType) {
		case EVREventType_VREvent_Quit:
		case EVREventType_VREvent_ProcessQuit:
			return 1; // caller will VR_ShutdownInternal()
		case EVREventType_VREvent_DriverRequestedQuit:
		case EVREventType_VREvent_RestartRequested:
			return 2;
		case EVREventType_VREvent_TrackedDeviceDeactivated:
			if (ev.trackedDeviceIndex == k_unTrackedDeviceIndex_Hmd) return 3;
			break;
		default:
			break;
		}
	}
	return 0;
}

// mate_register_app installs our .vrmanifest + sets auto-launch (so SteamVR lists + can start us).
static int mate_register_app(const char *manifest, const char *appkey, int autolaunch) {
	if (!g_app) return -1;
	g_app->AddApplicationManifest((char *)manifest, 0); // idempotent; ignore "already added"
	g_app->SetApplicationAutoLaunch((char *)appkey, autolaunch ? 1 : 0);
	return 0;
}

static unsigned long long mate_create(const char *key, const char *name) {
	if (!g_ov) return 0;
	VROverlayHandle_t h = 0;
	if (g_ov->CreateOverlay((char*)key, (char*)name, &h) != EVROverlayError_VROverlayError_None) return 0;
	return (unsigned long long)h;
}

static int mate_create_dashboard(const char *key, const char *name, unsigned long long *mainH, unsigned long long *thumbH) {
	if (!g_ov) return -1;
	VROverlayHandle_t mh = 0, th = 0;
	EVROverlayError e = g_ov->CreateDashboardOverlay((char*)key, (char*)name, &mh, &th);
	if (e != EVROverlayError_VROverlayError_None) return (int)e;
	*mainH = (unsigned long long)mh; *thumbH = (unsigned long long)th;
	return 0;
}

static int mate_set_raw(unsigned long long h, void *buf, int w, int hh) {
	if (!g_ov) return -1;
	return (int)g_ov->SetOverlayRaw((VROverlayHandle_t)h, buf, (uint32_t)w, (uint32_t)hh, 4);
}

// mate_texture_size fetches the GPU-side texture dims SteamVR holds for the overlay (0 = unavailable).
// Lets Go VERIFY a resize actually replaced the displayed texture instead of trusting SetOverlayRaw.
static int mate_texture_size(unsigned long long h, uint32_t *w, uint32_t *hh) {
	if (!g_ov || !g_ov->GetOverlayTextureSize) return 0;
	*w = 0; *hh = 0;
	if (g_ov->GetOverlayTextureSize((VROverlayHandle_t)h, w, hh) != EVROverlayError_VROverlayError_None) return 0;
	return 1;
}

// mate_texture_info = mate_texture_size + current texture bounds (diag: prove displayed == uploaded).
static int mate_texture_info(unsigned long long h, uint32_t *w, uint32_t *hh, float *b4) {
	if (!g_ov) return 0;
	struct VRTextureBounds_t b; memset(&b, 0, sizeof(b));
	g_ov->GetOverlayTextureBounds((VROverlayHandle_t)h, &b);
	b4[0] = b.uMin; b4[1] = b.vMin; b4[2] = b.uMax; b4[3] = b.vMax;
	return mate_texture_size(h, w, hh);
}

static void mate_basic(unsigned long long h, float widthM, float alpha) {
	if (!g_ov) return;
	g_ov->SetOverlayWidthInMeters((VROverlayHandle_t)h, widthM);
	g_ov->SetOverlayAlpha((VROverlayHandle_t)h, alpha);
}

static void mate_show(unsigned long long h, int show) {
	if (!g_ov) return;
	if (show) g_ov->ShowOverlay((VROverlayHandle_t)h);
	else      g_ov->HideOverlay((VROverlayHandle_t)h);
}

static void mate_destroy(unsigned long long h) { if (g_ov) g_ov->DestroyOverlay((VROverlayHandle_t)h); }

// transforms: Go builds the 3x4 matrix; world = absolute (standing), device = relative to a tracked
// device (controller idx, or 0 = HMD for a head-locked "visor").
static void mate_set_matrix_world(unsigned long long h, float *m12) {
	if (!g_ov) return;
	struct HmdMatrix34_t m; memcpy(m.m, m12, sizeof(float)*12);
	g_ov->SetOverlayTransformAbsolute((VROverlayHandle_t)h, ETrackingUniverseOrigin_TrackingUniverseStanding, &m);
}

static void mate_set_matrix_device(unsigned long long h, int idx, float *m12) {
	if (!g_ov) return;
	struct HmdMatrix34_t m; memcpy(m.m, m12, sizeof(float)*12);
	g_ov->SetOverlayTransformTrackedDeviceRelative((VROverlayHandle_t)h, (TrackedDeviceIndex_t)idx, &m);
}

static void mate_shutdown(void) { if (p_VR_ShutdownInternal) p_VR_ShutdownInternal(); g_ov=NULL; g_sys=NULL; g_app=NULL; g_comp=NULL; g_input=NULL; g_set_main=0; }

// ── in-VR editing: interactive overlays, laser events, controller poses ──────────

typedef struct { int type; float x; float y; int device; float scroll; } mate_event;

// mate_overlay_intersect casts a world-space ray (standing universe) at an overlay + returns hit
// UV + distance. Unlike the global MakeOverlaysInteractiveIfVisible laser, we compute the hit
// ourselves - it never takes the controller from the running game (how XSOverlay/OVRAS give a hover
// cursor that coexists with VRChat).
static int mate_overlay_intersect(unsigned long long h, float sx, float sy, float sz,
                                  float dx, float dy, float dz, float *outU, float *outV, float *outDist, float *outN) {
	if (!g_ov) return 0;
	struct VROverlayIntersectionParams_t p;
	p.vSource.v[0] = sx; p.vSource.v[1] = sy; p.vSource.v[2] = sz;
	p.vDirection.v[0] = dx; p.vDirection.v[1] = dy; p.vDirection.v[2] = dz;
	p.eOrigin = ETrackingUniverseOrigin_TrackingUniverseStanding;
	struct VROverlayIntersectionResults_t r; memset(&r, 0, sizeof(r));
	if (!g_ov->ComputeOverlayIntersection((VROverlayHandle_t)h, &p, &r)) return 0;
	*outU = r.vUVs.v[0]; *outV = r.vUVs.v[1]; *outDist = r.fDistance;
	outN[0] = r.vNormal.v[0]; outN[1] = r.vNormal.v[1]; outN[2] = r.vNormal.v[2]; // surface normal (perpendicular tip projection for near-field touch)
	return 1;
}

// mate_overlay_uv_world maps an overlay UV (bottom-left origin, 0..1 like ComputeOverlayIntersection's
// vUVs) to its world position via the runtime's own GetTransformForOverlayCoordinates - the SAME
// mapping the compositor draws with. A cursor placed here can never disagree with the hover/click row
// derived from the same UV (live trace 2026-07-02: vUVs vs our src+dir*d reconstruction diverged ~1
// menu row on a hand-held menu, firing the row above the visible dot).
static int mate_overlay_uv_world(unsigned long long h, float u, float v, float *outPos) {
	if (!g_ov) return 0;
	struct HmdVector2_t ms; ms.v[0] = 1; ms.v[1] = 1;
	g_ov->GetOverlayMouseScale((VROverlayHandle_t)h, &ms); // coordinatesInOverlay are in mouse-scale space
	if (ms.v[0] <= 0 || ms.v[1] <= 0) { ms.v[0] = 1; ms.v[1] = 1; }
	struct HmdVector2_t co; co.v[0] = u * ms.v[0]; co.v[1] = v * ms.v[1];
	struct HmdMatrix34_t m;
	if (g_ov->GetTransformForOverlayCoordinates((VROverlayHandle_t)h,
		ETrackingUniverseOrigin_TrackingUniverseStanding, co, &m) != EVROverlayError_VROverlayError_None) return 0;
	outPos[0] = m.m[0][3]; outPos[1] = m.m[1][3]; outPos[2] = m.m[2][3];
	return 1;
}

static void mate_set_interactive(unsigned long long h, int w, int hh, int on) {
	if (!g_ov) return;
	// NEVER enable VROverlayFlags_MakeOverlaysInteractiveIfVisible. It is GLOBAL: while ANY visible
	// overlay carries it, SteamVR enters laser-mouse mode and steals ALL controller input from the
	// running game (VRChat locomotion) AND our own IVRInput binds (summon/close/etc). That is the
	// "menu open = everything dead" bug. Every scene overlay (menu, content, wrist, camera-path) is
	// driven by OUR ray→ComputeOverlayIntersection cursor + the pointer_click action, which coexists
	// with the game and only "clicks" what the pointer is explicitly on. So we ACTIVELY CLEAR the flag
	// on every call - no overlay may ever capture input. Dashboard overlays stay interactive via the
	// SteamVR dashboard itself (mouse input method below), which is fine (the game is paused there).
	g_ov->SetOverlayFlag((VROverlayHandle_t)h, VROverlayFlags_MakeOverlaysInteractiveIfVisible, 0);
	g_ov->SetOverlayFlag((VROverlayHandle_t)h, VROverlayFlags_SendVRSmoothScrollEvents, 0);
	if (on) {
		g_ov->SetOverlayInputMethod((VROverlayHandle_t)h, VROverlayInputMethod_Mouse);
		struct HmdVector2_t sc; sc.v[0]=(float)w; sc.v[1]=(float)hh;
		g_ov->SetOverlayMouseScale((VROverlayHandle_t)h, &sc);
	} else {
		g_ov->SetOverlayInputMethod((VROverlayHandle_t)h, VROverlayInputMethod_None);
	}
}

static int mate_poll_event(unsigned long long h, mate_event *out) {
	if (!g_ov) return 0;
	struct VREvent_t ev;
	if (!g_ov->PollNextOverlayEvent((VROverlayHandle_t)h, &ev, sizeof(ev))) return 0;
	out->type=0; out->x=0; out->y=0; out->scroll=0; out->device=(int)ev.trackedDeviceIndex;
	switch (ev.eventType) {
		case EVREventType_VREvent_MouseMove:       out->type=1; out->x=ev.data.mouse.x; out->y=ev.data.mouse.y; break;
		case EVREventType_VREvent_MouseButtonDown: out->type=2; out->x=ev.data.mouse.x; out->y=ev.data.mouse.y; break;
		case EVREventType_VREvent_MouseButtonUp:   out->type=3; out->x=ev.data.mouse.x; out->y=ev.data.mouse.y; break;
		case EVREventType_VREvent_ScrollDiscrete:
		case EVREventType_VREvent_ScrollSmooth:    out->type=4; out->scroll=ev.data.scroll.ydelta; break;
		default: break;
	}
	return 1;
}

static int mate_device_pose(int idx, float *out12) {
	if (!g_sys || idx < 0 || idx >= (int)k_unMaxTrackedDeviceCount) return 0;
	if (g_pose_cache_frame != g_pose_frame) { // one whole-array fetch per input frame, shared by all callers
		g_sys->GetDeviceToAbsoluteTrackingPose(ETrackingUniverseOrigin_TrackingUniverseStanding, 0.0f, g_pose_cache, k_unMaxTrackedDeviceCount);
		g_pose_cache_frame = g_pose_frame;
	}
	if (!g_pose_cache[idx].bPoseIsValid) return 0;
	memcpy(out12, g_pose_cache[idx].mDeviceToAbsoluteTracking.m, sizeof(float)*12);
	return 1;
}

// mate_aim_pose fills the AIM/tip pose (where the controller POINTS, not the raw device forward) for a
// hand via the `aim` pose action bound to /pose/tip. Using the raw device pose makes the ray-pointer
// dot land off from where the user aims, with the error growing with distance - the tip pose fixes both.
static int mate_aim_pose(int leftHand, float *out12) {
	if (!g_input || !g_act_aim) return 0;
	struct InputPoseActionData_t d; memset(&d, 0, sizeof(d));
	VRInputValueHandle_t src = leftHand ? g_src_left : g_src_right;
	if (g_input->GetPoseActionDataForNextFrame(g_act_aim, ETrackingUniverseOrigin_TrackingUniverseStanding, &d, sizeof(d), src) != EVRInputError_VRInputError_None) return 0;
	if (!d.bActive || !d.pose.bPoseIsValid) return 0;
	memcpy(out12, d.pose.mDeviceToAbsoluteTracking.m, sizeof(float)*12);
	return 1;
}

// mate_haptic fires a rumble pulse on ONE hand via the haptic output action (grab feedback). Restricted
// to the hand's input source so only the active controller buzzes. No-op if input/the action isn't ready.
static void mate_haptic(int leftHand, float durationSec, float freq, float amplitude) {
	if (!g_input || !g_act_haptic) return;
	VRInputValueHandle_t src = leftHand ? g_src_left : g_src_right;
	g_input->TriggerHapticVibrationAction(g_act_haptic, 0.0f, durationSec, freq, amplitude, src);
}

// mate_tracker_poses fills (class, deviceIndex, m[12]) for every valid HMD + controller + generic
// tracker in ONE pose fetch (motion capture). Controllers ARE included (most users have hands but
// no trackers - w/o this only the head recorded). classes[i] = ETrackedDeviceClass, indices[i] =
// OpenVR device index (lets Go map controllers to left/right-hand roles), mats[i*12..] = the 3x4.
// Returns count. Device-index order, so the HMD (idx 0) sorts first → head.
static int mate_tracker_poses(int *classes, int *indices, float *mats, int max) {
	if (!g_sys) return 0;
	struct TrackedDevicePose_t poses[64];
	g_sys->GetDeviceToAbsoluteTrackingPose(ETrackingUniverseOrigin_TrackingUniverseStanding, 0.0f, poses, k_unMaxTrackedDeviceCount);
	int n = 0;
	for (int i = 0; i < (int)k_unMaxTrackedDeviceCount && n < max; i++) {
		if (!poses[i].bPoseIsValid) continue;
		ETrackedDeviceClass c = g_sys->GetTrackedDeviceClass((TrackedDeviceIndex_t)i);
		if (c != ETrackedDeviceClass_TrackedDeviceClass_HMD &&
		    c != ETrackedDeviceClass_TrackedDeviceClass_Controller &&
		    c != ETrackedDeviceClass_TrackedDeviceClass_GenericTracker) continue;
		classes[n] = (int)c;
		indices[n] = i;
		memcpy(&mats[n*12], poses[i].mDeviceToAbsoluteTracking.m, sizeof(float)*12);
		n++;
	}
	return n;
}

static int mate_controller_index(int leftHand) {
	if (!g_sys) return -1;
	ETrackedControllerRole role = leftHand ? ETrackedControllerRole_TrackedControllerRole_LeftHand : ETrackedControllerRole_TrackedControllerRole_RightHand;
	TrackedDeviceIndex_t idx = g_sys->GetTrackedDeviceIndexForControllerRole(role);
	if (idx == (TrackedDeviceIndex_t)k_unTrackedDeviceIndexInvalid) return -1;
	return (int)idx;
}

// mate_thumb_y returns the strongest |y| across the 2D axes (thumbstick/trackpad push), so it works
// regardless of which axis index a controller maps its stick to.
static float mate_thumb_y(int idx) {
	if (!g_sys || idx < 0) return 0;
	VRControllerState_t st;
	if (!g_sys->GetControllerState((TrackedDeviceIndex_t)idx, &st, sizeof(st))) return 0;
	float best = 0;
	for (int i = 0; i < 5; i++) { float y = st.rAxis[i].y; if (fabsf(y) > fabsf(best)) best = y; }
	return best;
}
*/
import "C"

import (
	"fmt"
	"image"
	"unsafe"

	"rave.page/mate/internal/vrmotion"
	"rave.page/mate/internal/vrstats"
)

// openvrRuntime is the OpenVR/SteamVR backend (built with -tags vr). Requires SteamVR running +
// openvr_api.dll alongside the exe.
type openvrRuntime struct {
	ok         bool
	inputReady bool // SteamVR Input action manifest loaded (custom keybinds available)
	handles    map[string]C.ulonglong
	rawSize    map[string][2]int    // overlay key → last-uploaded raw texture w,h (recreate-on-resize)
	names      map[string]string    // overlay key → display name (recreate needs the original name)
	state      map[string]*ovlState // overlay key → last-applied pose/visibility (re-applied on recreate)
	dash       map[string]bool      // overlay key → created via CreateDashboardOverlay (recreate must re-add the tab)
	gen        map[string]int       // overlay key → recreate generation (SteamVR keys are never reused: instant same-key re-create races the async release → KeyInUse → overlay silently vanishes)

	// Perf delta state: compositor cumulative counters at the last PerfStats sample.
	lastDropped uint32
	lastReproj  uint32
	havePerf    bool
}

// ovlState mirrors everything SteamVR forgets when an overlay is destroyed, so recreate() can
// restore it invisibly. Recorded on every SetTransform/SetTransformMatrix*/Show/SetInteractive.
type ovlState struct {
	widthM, alpha float32
	matKind       int // 0 = none yet, 1 = world, 2 = device-relative
	matIdx        int
	mat           Mat34
	visible       bool
	interW        int
	interH        int
	interOn       bool
	hasBasic      bool
	hasInter      bool
}

func (r *openvrRuntime) st(key string) *ovlState {
	s, ok := r.state[key]
	if !ok {
		s = &ovlState{}
		r.state[key] = s
	}
	return s
}

// NewRuntime returns the OpenVR backend on `vr` builds.
func NewRuntime() Runtime {
	return &openvrRuntime{handles: map[string]C.ulonglong{}, rawSize: map[string][2]int{},
		names: map[string]string{}, state: map[string]*ovlState{}, dash: map[string]bool{}, gen: map[string]int{}}
}

func (r *openvrRuntime) Available() bool { return r.ok }

// RuntimeInstalled reports whether SteamVR is installed (no session needed).
func (r *openvrRuntime) RuntimeInstalled() bool { return C.mate_runtime_installed() != 0 }

// PollQuit reports (+ acknowledges) a session-fatal SteamVR event: quit, driver quit/restart, HMD lost.
func (r *openvrRuntime) PollQuit() QuitReason {
	if !r.ok {
		return QuitNone
	}
	return QuitReason(C.mate_poll_quit())
}

// RegisterApp installs the .vrmanifest + sets auto-launch so SteamVR lists/can start the overlay.
func (r *openvrRuntime) RegisterApp(manifestPath, appKey string, autoLaunch bool) error {
	if !r.ok {
		return fmt.Errorf("openvr: not connected")
	}
	cm, ck := C.CString(manifestPath), C.CString(appKey)
	defer C.free(unsafe.Pointer(cm))
	defer C.free(unsafe.Pointer(ck))
	al := C.int(0)
	if autoLaunch {
		al = 1
	}
	if rc := C.mate_register_app(cm, ck, al); rc != 0 {
		return fmt.Errorf("openvr: register app failed (no applications interface)")
	}
	return nil
}

func (r *openvrRuntime) Init() error {
	if rc := C.mate_init(); rc != 0 {
		r.ok = false
		return nil // SteamVR not running / not installed - manager idles cleanly
	}
	r.ok = true
	return nil
}

func (r *openvrRuntime) EnsureOverlay(key, name string) error {
	if !r.ok {
		return nil
	}
	if _, ok := r.handles[key]; ok {
		return nil
	}
	ck, cn := C.CString(key), C.CString(name)
	defer C.free(unsafe.Pointer(ck))
	defer C.free(unsafe.Pointer(cn))
	h := C.mate_create(ck, cn)
	if h == 0 {
		return fmt.Errorf("openvr: CreateOverlay %q failed", key)
	}
	r.handles[key] = h
	r.names[key] = name
	return nil
}

// recreate destroys + re-creates an overlay and restores its cached pose/visibility/mouse-scale.
// Used on raw-texture dimension changes (SetOverlayRaw cannot resize an existing allocation - a
// clear+re-upload can silently keep the old texture OR wedge the overlay into persistent
// RequestFailed, both seen live) and as a self-heal when an upload fails outright.
func (r *openvrRuntime) recreate(key string) (C.ulonglong, bool) {
	name, ok := r.names[key]
	if !ok || !r.ok {
		return 0, false // unknown overlay (never Ensure'd)
	}
	if h, ok := r.handles[key]; ok {
		C.mate_destroy(h)
	}
	delete(r.handles, key)
	delete(r.rawSize, key)
	// Fresh SteamVR key per generation: re-creating the JUST-destroyed key races SteamVR's
	// async release (KeyInUse → create fails → handle entry gone → every later call no-ops →
	// the overlay is permanently invisible; seen live as the strip/dash err-23 loop).
	r.gen[key]++
	ck, cn := C.CString(fmt.Sprintf("%s~g%d", key, r.gen[key])), C.CString(name)
	defer C.free(unsafe.Pointer(ck))
	defer C.free(unsafe.Pointer(cn))
	var h C.ulonglong
	if r.dash[key] {
		// Dashboard overlays resize too (live: the dash menu's row count changed → SetOverlayRaw
		// err 23 looped forever because recreate refused dashboard keys). Destroy tab + thumb and
		// re-add via CreateDashboardOverlay - SteamVR re-registers the tab.
		if th, ok := r.handles[key+".thumb"]; ok {
			C.mate_destroy(th)
			delete(r.handles, key+".thumb")
		}
		var th C.ulonglong
		if C.mate_create_dashboard(ck, cn, &h, &th) != 0 {
			return 0, false
		}
		r.handles[key+".thumb"] = th
	} else {
		h = C.mate_create(ck, cn)
	}
	if h == 0 {
		return 0, false
	}
	r.handles[key] = h
	if s, ok := r.state[key]; ok {
		if s.hasBasic {
			C.mate_basic(h, C.float(s.widthM), C.float(s.alpha))
		}
		switch s.matKind {
		case 1:
			r.setMatrixWorld(h, s.mat)
		case 2:
			r.setMatrixDevice(h, s.matIdx, s.mat)
		}
		if s.hasInter {
			on := C.int(0)
			if s.interOn {
				on = 1
			}
			C.mate_set_interactive(h, C.int(s.interW), C.int(s.interH), on)
		}
		if s.visible {
			C.mate_show(h, 1)
		}
	}
	return h, true
}

func (r *openvrRuntime) SetTexture(key string, img *image.NRGBA) error {
	if !r.ok || img == nil || len(img.Pix) == 0 {
		return nil
	}
	h, ok := r.handles[key]
	if !ok {
		if _, known := r.names[key]; !known {
			return nil
		}
		h2, ok2 := r.recreate(key) // a prior failed recreate dropped the handle - heal it
		if !ok2 {
			return fmt.Errorf("openvr: overlay %q lost and recreate failed", key)
		}
		h = h2
	}
	w := img.Bounds().Dx()
	hh := img.Bounds().Dy()
	// SetOverlayRaw cannot resize an existing raw texture. ClearOverlayTexture + re-upload was tried
	// and failed live BOTH ways: SteamVR either silently kept the old allocation (displayed image
	// scaled relative to the quad ComputeOverlayIntersection measures → cursor/hover offset flipping
	// sign around the quad center) or wedged the overlay into persistent SetOverlayRaw RequestFailed
	// (menu went invisible). A fresh overlay is the only reliable resize: recreate + restore pose.
	if last, has := r.rawSize[key]; has && (last[0] != w || last[1] != hh) {
		if h2, ok := r.recreate(key); ok {
			h = h2
		}
	}
	rc := C.mate_set_raw(h, unsafe.Pointer(&img.Pix[0]), C.int(w), C.int(hh))
	if rc != 0 {
		// Self-heal: a wedged overlay (persistent upload failure) gets one destroy+recreate+retry.
		if h2, ok := r.recreate(key); ok {
			if rc2 := C.mate_set_raw(h2, unsafe.Pointer(&img.Pix[0]), C.int(w), C.int(hh)); rc2 == 0 {
				r.rawSize[key] = [2]int{w, hh}
				return nil
			}
		}
		return fmt.Errorf("openvr: SetOverlayRaw err %d", int(rc))
	}
	r.rawSize[key] = [2]int{w, hh}
	return nil
}

// TextureInfo reports the GPU-side texture size + bounds SteamVR holds for an overlay - a remote
// diag can PROVE the displayed texture matches the last upload (vs. the click/hover row mapping).
func (r *openvrRuntime) TextureInfo(key string) (int, int, [4]float32, bool) {
	h, ok := r.handles[key]
	if !ok || !r.ok {
		return 0, 0, [4]float32{}, false
	}
	var w, hh C.uint32_t
	var b [4]C.float
	if C.mate_texture_info(h, &w, &hh, &b[0]) == 0 {
		return 0, 0, [4]float32{}, false
	}
	return int(w), int(hh), [4]float32{float32(b[0]), float32(b[1]), float32(b[2]), float32(b[3])}, true
}

func (r *openvrRuntime) SetTransform(key string, t Transform) error {
	h, ok := r.handles[key]
	if !ok || !r.ok {
		return nil
	}
	C.mate_basic(h, C.float(t.WidthM), C.float(t.Opacity))
	s := r.st(key)
	s.widthM, s.alpha, s.hasBasic = float32(t.WidthM), float32(t.Opacity), true
	mat := EulerToMat(t.Yaw, t.Pitch, t.Roll, t.X, t.Y, t.Z)
	switch t.Snap {
	case HandHead: // head-locked "visor" - relative to the HMD (device 0)
		r.setMatrixDevice(h, 0, mat)
		s.matKind, s.matIdx, s.mat = 2, 0, mat
	case HandLeft, HandRight:
		if idx, ok := r.ControllerIndex(t.Snap); ok {
			r.setMatrixDevice(h, idx, mat)
			s.matKind, s.matIdx, s.mat = 2, idx, mat
		} else {
			r.setMatrixWorld(h, mat) // controller not tracked → fall back to world
			s.matKind, s.mat = 1, mat
		}
	default:
		r.setMatrixWorld(h, mat)
		s.matKind, s.mat = 1, mat
	}
	return nil
}

func cmat(m Mat34) [12]C.float {
	var cm [12]C.float
	for i := 0; i < 12; i++ {
		cm[i] = C.float(m[i])
	}
	return cm
}

func (r *openvrRuntime) setMatrixWorld(h C.ulonglong, m Mat34) {
	cm := cmat(m)
	C.mate_set_matrix_world(h, &cm[0])
}

func (r *openvrRuntime) setMatrixDevice(h C.ulonglong, idx int, m Mat34) {
	cm := cmat(m)
	C.mate_set_matrix_device(h, C.int(idx), &cm[0])
}

func (r *openvrRuntime) Show(key string, visible bool) error {
	h, ok := r.handles[key]
	if !ok || !r.ok {
		return nil
	}
	s := C.int(0)
	if visible {
		s = 1
	}
	C.mate_show(h, s)
	r.st(key).visible = visible
	return nil
}

func (r *openvrRuntime) DestroyOverlay(key string) error {
	h, ok := r.handles[key]
	if !ok {
		return nil
	}
	C.mate_destroy(h)
	delete(r.handles, key)
	delete(r.rawSize, key)
	delete(r.names, key)
	delete(r.state, key)
	delete(r.dash, key)
	return nil
}

func (r *openvrRuntime) Shutdown() {
	if r.ok {
		C.mate_shutdown()
		r.ok = false
	}
	r.handles = map[string]C.ulonglong{}
	r.rawSize = map[string][2]int{}
	r.names = map[string]string{}
	r.state = map[string]*ovlState{}
}

// ── Editor implementation (in-VR editing) ────────────────────────────────────────

func (r *openvrRuntime) SetInteractive(key string, w, h int, on bool) {
	hd, ok := r.handles[key]
	if !ok || !r.ok {
		return
	}
	o := C.int(0)
	if on {
		o = 1
	}
	C.mate_set_interactive(hd, C.int(w), C.int(h), o)
	s := r.st(key)
	s.interW, s.interH, s.interOn, s.hasInter = w, h, on, true
}

func (r *openvrRuntime) PollEvents(key string) []OverlayEvent {
	hd, ok := r.handles[key]
	if !ok || !r.ok {
		return nil
	}
	var out []OverlayEvent
	for {
		var ev C.mate_event
		if C.mate_poll_event(hd, &ev) == 0 {
			break
		}
		out = append(out, OverlayEvent{
			Type: EventType(ev._type), X: float32(ev.x), Y: float32(ev.y),
			Device: int(ev.device), Scroll: float32(ev.scroll),
		})
	}
	return out
}

func (r *openvrRuntime) DevicePose(idx int) (Mat34, bool) {
	if !r.ok {
		return Mat34{}, false
	}
	var m [12]C.float
	if C.mate_device_pose(C.int(idx), &m[0]) == 0 {
		return Mat34{}, false
	}
	var out Mat34
	for i := 0; i < 12; i++ {
		out[i] = float32(m[i])
	}
	return out, true
}

// AimPose returns a hand's AIM/tip pose (where the controller points) for the ray pointer. false if
// input/the aim action isn't ready - callers fall back to the raw device pose.
func (r *openvrRuntime) AimPose(hand Hand) (Mat34, bool) {
	if !r.ok || !r.inputReady {
		return Mat34{}, false
	}
	left := C.int(0)
	if hand == HandLeft {
		left = 1
	}
	var m [12]C.float
	if C.mate_aim_pose(left, &m[0]) == 0 {
		return Mat34{}, false
	}
	var out Mat34
	for i := 0; i < 12; i++ {
		out[i] = float32(m[i])
	}
	return out, true
}

// Haptic fires a short rumble pulse on a hand (grab engage/drop feedback). No-op if input isn't ready.
func (r *openvrRuntime) Haptic(hand Hand, durationSec, freq, amplitude float32) {
	if !r.ok || !r.inputReady {
		return
	}
	left := C.int(0)
	if hand == HandLeft {
		left = 1
	}
	C.mate_haptic(left, C.float(durationSec), C.float(freq), C.float(amplitude))
}

// Intersect casts a world-space ray at an overlay, returning hit UV (0..1, origin top-left) +
// distance (m). ok=false on miss / unknown overlay. Manual - coexists with the game (no capture).
func (r *openvrRuntime) Intersect(key string, src, dir [3]float32) (u, v, dist float32, ok bool) {
	u, v, dist, _, ok = r.IntersectN(key, src, dir)
	return
}

// IntersectN is Intersect + the hit's surface normal (unit, out of the overlay face) - the near-field
// touch cast projects the tip PERPENDICULARLY onto the surface with it (gaze projection parallax-offset
// the cursor from the tip, with the sign flipping as the hand crossed the gaze line).
func (r *openvrRuntime) IntersectN(key string, src, dir [3]float32) (u, v, dist float32, n [3]float32, ok bool) {
	hd, has := r.handles[key]
	if !has || !r.ok {
		return 0, 0, 0, n, false
	}
	var cu, cv, cd C.float
	var cn [3]C.float
	if C.mate_overlay_intersect(hd, C.float(src[0]), C.float(src[1]), C.float(src[2]),
		C.float(dir[0]), C.float(dir[1]), C.float(dir[2]), &cu, &cv, &cd, &cn[0]) == 0 {
		return 0, 0, 0, n, false
	}
	return float32(cu), float32(cv), float32(cd), [3]float32{float32(cn[0]), float32(cn[1]), float32(cn[2])}, true
}

// UVWorld maps an overlay UV (bottom-origin, as IntersectN returns it) to the point's world position
// using the runtime's own overlay→world mapping. ok=false on unknown overlay / API error.
func (r *openvrRuntime) UVWorld(key string, u, v float32) ([3]float32, bool) {
	hd, has := r.handles[key]
	if !has || !r.ok {
		return [3]float32{}, false
	}
	var p [3]C.float
	if C.mate_overlay_uv_world(hd, C.float(u), C.float(v), &p[0]) == 0 {
		return [3]float32{}, false
	}
	return [3]float32{float32(p[0]), float32(p[1]), float32(p[2])}, true
}

func (r *openvrRuntime) ControllerIndex(hand Hand) (int, bool) {
	if !r.ok {
		return 0, false
	}
	left := C.int(0)
	if hand == HandLeft {
		left = 1
	}
	idx := int(C.mate_controller_index(left))
	if idx < 0 {
		return 0, false
	}
	return idx, true
}

func (r *openvrRuntime) ThumbY(idx int) float32 {
	if !r.ok {
		return 0
	}
	return float32(C.mate_thumb_y(C.int(idx)))
}

// ThumbVec returns a hand's thumbstick (x,y in −1..1) via the push_pull action's per-hand source.
func (r *openvrRuntime) ThumbVec(hand Hand) (float32, float32) {
	if !r.ok {
		return 0, 0
	}
	var x, y C.float
	left := C.int(0)
	if hand == HandLeft {
		left = 1
	}
	C.mate_thumb_vec_hand(left, &x, &y)
	return float32(x), float32(y)
}

// TrackerPoses captures the current HMD + controllers + generic trackers as world poses for
// motion recording. Keys are ROLE-STABLE: 0 = head, 1 = left hand, 2 = right hand (SteamVR
// controller roles), 3.. = generic trackers / roleless controllers in device order. Playback
// additionally classifies keys geometrically (vrmik.Calibrate), so legacy index-order takes
// still map correctly. One pose fetch; nil if SteamVR has no valid devices.
func (r *openvrRuntime) TrackerPoses() map[int]vrmotion.Pose {
	if !r.ok {
		return nil
	}
	var classes, indices [64]C.int
	var mats [64 * 12]C.float
	n := int(C.mate_tracker_poses(&classes[0], &indices[0], &mats[0], 64))
	if n == 0 {
		return nil
	}
	left, right := int(C.mate_controller_index(1)), int(C.mate_controller_index(0))
	out := make(map[int]vrmotion.Pose, n)
	tracker := 3
	for i := 0; i < n; i++ {
		var m Mat34
		for j := 0; j < 12; j++ {
			m[j] = float32(mats[i*12+j])
		}
		key := 0 // HMD → head
		switch {
		case int(classes[i]) == 1: // HMD
		case left >= 0 && int(indices[i]) == left:
			key = 1
		case right >= 0 && int(indices[i]) == right:
			key = 2
		default:
			if tracker > 8 {
				continue
			}
			key, tracker = tracker, tracker+1
		}
		px, py, pz := MatPos(m)
		out[key] = vrmotion.Pose{Pos: [3]float32{float32(px), float32(py), float32(pz)}, Rot: MatToQuat(m)}
	}
	return out
}

// InputInit loads the SteamVR Input action manifest; reports whether actions are ready (custom binds).
func (r *openvrRuntime) InputInit(manifestPath string) bool {
	if !r.ok {
		return false
	}
	cp := C.CString(manifestPath)
	defer C.free(unsafe.Pointer(cp))
	rc := int(C.mate_input_init(cp))
	r.inputReady = rc == 0
	return r.inputReady
}

func (r *openvrRuntime) InputReady() bool { return r.ok && r.inputReady }

// InputDiag returns a human-readable dump of the SteamVR Input action set: manifest-loaded state +
// per-action live active/state + the bound physical inputs (for debugging "my binding does nothing").
func (r *openvrRuntime) InputDiag() string {
	if !r.ok {
		return "VR not connected (start SteamVR)"
	}
	var buf [4096]C.char
	C.mate_input_diag(&buf[0], C.int(len(buf)))
	return C.GoString(&buf[0])
}

// BindingStatus reports whether SteamVR has usable bindings for our action set: Unbound means the
// manifest loaded but a stale custom binding leaves every action unbound (summon/pointer/grab dead).
func (r *openvrRuntime) BindingStatus() BindingStatus {
	if !r.ok || !r.inputReady {
		return BindingNotReady
	}
	switch int(C.mate_binding_status()) {
	case 1:
		return BindingOK
	case 0:
		return BindingUnbound
	default:
		return BindingNotReady
	}
}

// InputUpdate pumps the action set; call once per tick before reading actions.
func (r *openvrRuntime) InputUpdate() {
	if r.ok && r.inputReady {
		C.mate_input_update()
	}
}

// digital decodes the C bitfield: state (held), changed (edge this update).
func digital(v C.int) (state, changed bool) { return v&1 != 0, v&2 != 0 }

func (r *openvrRuntime) ActToggleEditorEdge() bool {
	if !r.ok || !r.inputReady {
		return false
	}
	s, c := digital(C.mate_act_toggle())
	return s && c // rising edge only (press, not release)
}

func (r *openvrRuntime) ActToggleOverlaysEdge() bool {
	if !r.ok || !r.inputReady {
		return false
	}
	s, c := digital(C.mate_act_hide())
	return s && c
}

func (r *openvrRuntime) ActGrabHeld() bool {
	if !r.ok || !r.inputReady {
		return false
	}
	s, _ := digital(C.mate_act_grab())
	return s
}

// ActSummonHeld reports the summon action's held state (open-editor / tap-hide button). Uses
// IVRInput - legacy GetControllerState returns nothing for face buttons on Index/Touch.
func (r *openvrRuntime) ActSummonHeld() bool {
	if !r.ok || !r.inputReady {
		return false
	}
	s, _ := digital(C.mate_act_summon())
	return s
}

// ActPointerClickEdge reports the pointer-click action (trigger) rising edge this tick - used to
// activate the ray-pointed rave-mate overlay. Manual ray-hit gating means it never affects in-game
// trigger use when you're not pointing at our overlay.
func (r *openvrRuntime) ActPointerClickEdge() bool {
	if !r.ok || !r.inputReady {
		return false
	}
	s, c := digital(C.mate_act_pclick())
	return s && c
}

// ActPointerClickHeld reports the pointer-click action's held state (trigger down) - used to drag a
// menu slider continuously so the user can pull it to the 0%/100% edges a single click can't reach.
func (r *openvrRuntime) ActPointerClickHeld() bool {
	if !r.ok || !r.inputReady {
		return false
	}
	s, _ := digital(C.mate_act_pclick())
	return s
}

// PointerClickState reports one hand's pointer_click trigger: held now + a rising edge this tick. Used
// for active-hand detection (pulling a hand's trigger makes it the pointer hand → no hand-jitter).
func (r *openvrRuntime) PointerClickState(hand Hand) (held, edge bool) {
	if !r.ok || !r.inputReady {
		return false, false
	}
	left := C.int(0)
	if hand == HandLeft {
		left = 1
	}
	held, changed := digital(C.mate_pclick_hand(left))
	return held, changed && held
}

func (r *openvrRuntime) ActPushPull() float32 {
	if !r.ok || !r.inputReady {
		return 0
	}
	return float32(C.mate_act_pushpull())
}

// ActSlotEdges returns a bitmask of user-mappable slots pressed this tick (bit i = slot i+1).
func (r *openvrRuntime) ActSlotEdges() uint32 {
	if !r.ok || !r.inputReady {
		return 0
	}
	return uint32(C.mate_slot_edges())
}

// OpenBindingUI opens SteamVR's controller-binding screen for our action set.
func (r *openvrRuntime) OpenBindingUI() error {
	if !r.ok || !r.inputReady {
		return fmt.Errorf("openvr: input not ready")
	}
	if rc := int(C.mate_open_binding_ui()); rc != 0 {
		return fmt.Errorf("OpenBindingUI failed: EVRInputError %d", rc)
	}
	return nil
}

// ActionBinding returns the human-readable physical inputs SteamVR binds to the action at the given
// action path (e.g. "Left Hand Index Controller A Button, …"); "" when unbound or input not ready.
func (r *openvrRuntime) ActionBinding(action string) string {
	if !r.ok || !r.inputReady {
		return ""
	}
	ca := C.CString(action)
	defer C.free(unsafe.Pointer(ca))
	var buf [256]C.char
	C.mate_action_binding(ca, &buf[0], C.int(len(buf)))
	return C.GoString(&buf[0])
}

func (r *openvrRuntime) EnsureDashboard(key, name string) (bool, error) {
	if !r.ok {
		return false, nil
	}
	if _, ok := r.handles[key]; ok {
		return true, nil
	}
	ck, cn := C.CString(key), C.CString(name)
	defer C.free(unsafe.Pointer(ck))
	defer C.free(unsafe.Pointer(cn))
	var mh, th C.ulonglong
	if rc := C.mate_create_dashboard(ck, cn, &mh, &th); rc != 0 {
		return false, fmt.Errorf("openvr: CreateDashboardOverlay err %d", int(rc))
	}
	r.handles[key] = mh
	r.handles[key+".thumb"] = th
	r.names[key] = name
	r.dash[key] = true // recreate() must go through CreateDashboardOverlay again (re-adds the tab)
	return true, nil
}

func (r *openvrRuntime) SetTransformMatrixWorld(key string, m Mat34) {
	hd, ok := r.handles[key]
	if !ok || !r.ok {
		return
	}
	r.setMatrixWorld(hd, m)
	s := r.st(key)
	s.matKind, s.mat = 1, m
}

// SetTransformMatrixDevice parents the overlay to a tracked device with a fixed offset. SteamVR then
// keeps it rigidly attached at full framerate (no per-tick pose math), so a grabbed surface follows
// the hand smoothly. m is the overlay pose in the device's frame.
func (r *openvrRuntime) SetTransformMatrixDevice(key string, idx int, m Mat34) {
	hd, ok := r.handles[key]
	if !ok || !r.ok {
		return
	}
	r.setMatrixDevice(hd, idx, m)
	s := r.st(key)
	s.matKind, s.matIdx, s.mat = 2, idx, m
}

// OverlayQuad returns an overlay's CURRENT world-space quad - center pose + width + height in metres -
// reconstructed from the state we already own: the last-applied transform (device-relative ones folded
// through the live device pose) + width + the uploaded texture aspect. This is the input to the
// analytic pointer (hitQuad/projectPoint), replacing ComputeOverlayIntersection + the mouse-scale
// GetTransformForOverlayCoordinates round-trip that disagreed by a center-scaled factor.
func (r *openvrRuntime) OverlayQuad(key string) (Mat34, float32, float32, bool) {
	s, ok := r.state[key]
	if !ok || !s.hasBasic || s.widthM <= 0 {
		return Mat34{}, 0, 0, false // no width set yet → not a hit-testable quad
	}
	wh, ok := r.rawSize[key]
	if !ok || wh[0] <= 0 || wh[1] <= 0 {
		return Mat34{}, 0, 0, false // no texture uploaded → aspect unknown
	}
	heightM := s.widthM * float32(wh[1]) / float32(wh[0]) // OpenVR sizes a quad by width × texture aspect
	switch s.matKind {
	case 1: // world-anchored → the stored matrix IS the world pose
		return s.mat, s.widthM, heightM, true
	case 2: // device-relative → world = devicePose × localOffset (exact every frame, incl. hand-held)
		dev, dok := r.DevicePose(s.matIdx)
		if !dok {
			return Mat34{}, 0, 0, false
		}
		return MulMat(dev, s.mat), s.widthM, heightM, true
	}
	return Mat34{}, 0, 0, false
}

// PerfStats samples compositor frame timing + HMD debug. Frame counters are deltas since the prior
// call (the first call seeds the baseline → 0 deltas). false when no session/compositor.
func (r *openvrRuntime) PerfStats() (vrstats.PerfStats, bool) {
	if !r.ok {
		r.havePerf = false
		return vrstats.PerfStats{}, false
	}
	var p C.mate_perf
	var model [128]C.char
	if C.mate_perf_get(&p, &model[0], C.int(len(model))) == 0 {
		return vrstats.PerfStats{Connected: true}, true // session up but compositor perf unavailable
	}
	fps := 0.0
	if p.frameMs > 0 {
		fps = 1000.0 / float64(p.frameMs)
	}
	dropped, reproj := 0, 0
	if r.havePerf {
		dropped = int(uint32(p.cumDropped) - r.lastDropped)
		reproj = int(uint32(p.cumReprojected) - r.lastReproj)
		if dropped < 0 {
			dropped = 0
		}
		if reproj < 0 {
			reproj = 0
		}
	}
	r.lastDropped, r.lastReproj, r.havePerf = uint32(p.cumDropped), uint32(p.cumReprojected), true
	return vrstats.PerfStats{
		Connected:    true,
		HMDModel:     C.GoString(&model[0]),
		DisplayHz:    float64(p.displayHz),
		FPS:          fps,
		FrameMs:      float64(p.frameMs),
		GpuMs:        float64(p.gpuMs),
		CompositorMs: float64(p.compositorMs),
		Reprojecting: p.reprojecting != 0,
		Dropped:      dropped,
		Reprojected:  reproj,
	}, true
}

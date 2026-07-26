// Package vroverlay renders bus-sourced content (Twitch chat, alerts) into VR overlays via OpenVR
// (SteamVR). The OpenVR binding is cgo behind the `vr` build tag; a no-op stub backs every other
// build so the default app compiles + runs without the SDK. The Manager subscribes to the event bus
// - so a VR PC shows the chat from another rave-mate instance that owns the Twitch connection.
package vroverlay

import (
	"fmt"
	"image"

	"rave.page/mate/internal/vrmotion"
	"rave.page/mate/internal/vrstats"
)

// Hand selects a controller for snap-to-controller placement.
type Hand int

const (
	HandNone Hand = iota
	HandLeft
	HandRight
	HandHead // head-locked "visor" (relative to the HMD)
)

// HandFromString maps the config SnapTo string to a Hand.
func HandFromString(s string) Hand {
	switch s {
	case "left":
		return HandLeft
	case "right":
		return HandRight
	case "head":
		return HandHead
	default:
		return HandNone
	}
}

// Transform places an overlay. Snap==HandNone → world-anchored (X/Y/Z in standing room space);
// else parented to that controller with the offset. Angles in degrees. WidthM = width in meters.
type Transform struct {
	Snap             Hand
	X, Y, Z          float64
	Yaw, Pitch, Roll float64
	WidthM           float64
	Opacity          float64
}

// QuitReason classifies a session-fatal SteamVR event surfaced by PollQuit. Any nonzero reason
// forces the same clean disconnect → supervise-loop reconnect.
type QuitReason int

const (
	QuitNone      QuitReason = iota // no fatal event this poll
	QuitRequested                   // Quit/ProcessQuit - SteamVR exiting
	QuitDriver                      // DriverRequestedQuit/RestartRequested - driver/runtime restart
	QuitHMDLost                     // HMD (device 0) deactivated; controller standby is routine, NOT fatal
)

func (q QuitReason) String() string {
	switch q {
	case QuitNone:
		return "none"
	case QuitRequested:
		return "steamvr-quit"
	case QuitDriver:
		return "driver-quit/restart"
	case QuitHMDLost:
		return "hmd-lost"
	default:
		return fmt.Sprintf("unknown(%d)", int(q))
	}
}

// Runtime is the VR backend. OpenVR when built with `-tags vr` + SteamVR running; otherwise a no-op
// stub (Available()==false). Not safe for concurrent use - the Manager calls from one goroutine.
type Runtime interface {
	Available() bool                               // a usable VR runtime is present (SteamVR up)
	Init() error                                   // connect to the runtime (overlay app)
	EnsureOverlay(key, name string) error          // create the overlay if absent
	SetTexture(key string, img *image.NRGBA) error // upload an RGBA frame
	SetTransform(key string, t Transform) error    // position/size/opacity (world or controller)
	Show(key string, visible bool) error           // show/hide
	DestroyOverlay(key string) error               // remove
	Shutdown()                                     // release the runtime

	RuntimeInstalled() bool                                         // SteamVR installed (no session needed)
	HMDPresent() bool                                               // a headset is connected (VR_IsHmdPresent - never launches SteamVR)
	PollQuit() QuitReason                                           // session-fatal SteamVR event (quit / driver restart / HMD lost); QuitNone = healthy
	RegisterApp(manifestPath, appKey string, autoLaunch bool) error // register .vrmanifest + auto-launch
	PerfStats() (vrstats.PerfStats, bool)                           // compositor frame timing + HMD debug (false if unavailable)
}

// BindingStatus reports whether SteamVR has usable controller bindings for rave-mate's action set.
type BindingStatus int

const (
	BindingNotReady BindingStatus = iota // manifest not loaded (non-vr build / SteamVR down / old SteamVR)
	BindingUnbound                       // manifest loaded but NO action bound (stale custom binding overrides our defaults → controls silently dead)
	BindingOK                            // ≥1 action bound
)

// EventType is an in-VR overlay input event from the laser pointer.
type EventType int

const (
	EvNone EventType = iota
	EvMouseMove
	EvMouseDown
	EvMouseUp
	EvScroll
)

// OverlayEvent is one laser-pointer event on an interactive overlay. X/Y are in the overlay's pixel
// space (set via mouse-scale); Device is the firing controller's tracked-device index.
type OverlayEvent struct {
	Type   EventType
	X, Y   float32
	Device int
	Scroll float32
}

// Editor is the in-VR editing surface - implemented only by the OpenVR backend (the stub does not,
// so default builds have no in-VR editor). Lets overlays receive laser input + be grabbed/moved by
// reading controller poses. Callers type-assert: ed, ok := rt.(Editor).
type Editor interface {
	SetInteractive(key string, w, h int, on bool)                                           // enable/disable laser input (mouse-scaled to w×h)
	PollEvents(key string) []OverlayEvent                                                   // drain queued laser events for an overlay
	DevicePose(idx int) (Mat34, bool)                                                       // world transform of a tracked device
	AimPose(hand Hand) (Mat34, bool)                                                        // hand's AIM/tip pose (where it points) for the ray pointer; false → fall back to DevicePose
	Haptic(hand Hand, durationSec, freq, amplitude float32)                                 // rumble pulse on a hand (grab engage/drop feedback)
	PointerClickState(hand Hand) (held, edge bool)                                          // one hand's trigger (held + rising edge) for active-hand detection
	Intersect(key string, src, dir [3]float32) (u, v, dist float32, ok bool)                // manual ray→overlay hit (coexists with the game; no capture)
	IntersectN(key string, src, dir [3]float32) (u, v, dist float32, n [3]float32, ok bool) // Intersect + surface normal (perpendicular tip projection for near-field touch)
	UVWorld(key string, u, v float32) ([3]float32, bool)                                    // overlay UV (bottom-origin) → world position via the runtime's own mapping (legacy; validation trace only)
	OverlayQuad(key string) (center Mat34, widthM, heightM float32, ok bool)                // owned world-space quad (pose×size) for EXACT analytic hit-testing (no runtime round-trip)
	ControllerIndex(hand Hand) (int, bool)                                                  // tracked-device index for a hand
	ThumbY(idx int) float32                                                                 // strongest thumbstick/trackpad y (push/pull while grabbed)
	ThumbVec(hand Hand) (x, y float32)                                                      // a hand's thumbstick x,y (−1..1) for edit-mode nudge (reuses push_pull)
	SetTransformMatrixWorld(key string, m Mat34)                                            // place via raw world matrix (drop)
	SetTransformMatrixDevice(key string, idx int, m Mat34)                                  // attach rigidly to a tracked device (grab follow; SteamVR tracks it at full fps)
	EnsureDashboard(key, name string) (bool, error)                                         // add a SteamVR dashboard tab hosting an overlay
	TextureInfo(key string) (w, h int, bounds [4]float32, ok bool)                          // GPU-side texture size + bounds SteamVR holds (diag: displayed == uploaded?)

	// SteamVR Input actions (user-rebindable in SteamVR's controller-binding UI). InputReady reports
	// whether the action manifest loaded; the *Edge accessors report a press (rising edge) this tick.
	InputInit(manifestPath string) bool // load the action manifest; true if actions are usable
	InputReady() bool                   // actions available
	BindingStatus() BindingStatus       // NotReady / Unbound (stale custom binding leaves every action unbound) / OK
	InputDiag() string                  // human-readable action-set dump (manifest state + bound origins) for debugging
	InputUpdate()                       // pump the action set (once per tick, before reading)
	ActToggleEditorEdge() bool          // "open/close editor" pressed this tick
	ActToggleOverlaysEdge() bool        // "show/hide overlays" pressed this tick
	ActGrabHeld() bool                  // "grab" currently held (hold-to-move)
	ActSummonHeld() bool                // "summon" (open editor / tap-hide) currently held - IVRInput, not dead legacy poll
	ActPointerClickEdge() bool          // "pointer_click" (trigger) pressed this tick - activate the ray-pointed overlay (coexists; no laser capture)
	ActPointerClickHeld() bool          // "pointer_click" (trigger) currently held - continuous slider drag/pull
	ActPushPull() float32               // "push/pull" analog y (move grabbed overlay nearer/farther)
	ActSlotEdges() uint32               // bitmask of user-mappable slots pressed this tick (bit i = slot i+1)
	OpenBindingUI() error               // open SteamVR's controller-binding screen for our action set
	ActionBinding(action string) string // human-readable physical inputs bound to an action path ("" = unbound)

	TrackerPoses() map[int]vrmotion.Pose // HMD (key 0) + generic trackers (1..8) world poses (motion capture)
}

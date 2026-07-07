package vroverlay

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"rave.page/mate/internal/config"
)

// SteamVR Input action manifest (this is exactly how OVR Advanced Settings does input - IVRInput, not
// legacy polling, which returns nothing for face buttons on Index/Touch). We declare the actions +
// ship per-controller default bindings, so the controls work out of the box AND rave-mate shows up in
// SteamVR's controller-binding UI for rebinding to any input/combo.
//
// Coexistence: IVRInput fans the same physical input out to EVERY app - binding grip/stick/A here does
// NOT take them from VRChat (both apps see them). Grab (grip) + push/pull (stick) only act while the
// editor is open + the laser is on a surface. The SUMMON button (A/X by default) opens the editor on a
// long hold + optionally tap-hides - a quick in-game press won't open it. toggle_editor/toggle_overlays
// are declared but unbound by default (advanced: bind them to anything in SteamVR).

func boolAct(name string) map[string]any { return map[string]any{"name": name, "type": "boolean"} }
func vec2Act(name string) map[string]any { return map[string]any{"name": name, "type": "vector2"} }
func poseAct(name string) map[string]any { return map[string]any{"name": name, "type": "pose"} }
func vibAct(name string) map[string]any  { return map[string]any{"name": name, "type": "vibration"} }
func clickSrc(path, action string) map[string]any {
	return map[string]any{"path": path, "mode": "button",
		"inputs": map[string]any{"click": map[string]any{"output": action}}}
}

// gripForceSrc binds an action to a grip as a FORCE button with hysteresis: engage needs a firm squeeze
// (0.90) and release requires dropping to a near-open 0.35 - live diag showed a normal Index carry-grip
// sits ABOVE the old 0.55 release, latching "grab held" for minutes. Wide hysteresis: hard to trigger,
// easy to keep, releases when the hand actually relaxes. Controllers without a force sensor (Vive)
// treat grip as a plain digital button; SteamVR harmlessly ignores the thresholds there.
func gripForceSrc(path, action string) map[string]any {
	return map[string]any{"path": path, "mode": "button",
		"inputs": map[string]any{"click": map[string]any{"output": action}},
		"parameters": map[string]any{
			"click_activate_threshold":   "0.90",
			"click_deactivate_threshold": "0.35",
		}}
}
func joySrc(path, action string) map[string]any {
	return map[string]any{"path": path, "mode": "joystick",
		"inputs": map[string]any{"position": map[string]any{"output": action}}}
}

const (
	actToggle   = "/actions/main/in/toggle_editor"
	actOverlays = "/actions/main/in/toggle_overlays"
	actGrab     = "/actions/main/in/grab"
	actSummon   = "/actions/main/in/summon"
	actPClick   = "/actions/main/in/pointer_click"
	actPushPull = "/actions/main/in/push_pull"
	actAim      = "/actions/main/in/aim"     // pose: controller AIM/tip (where it points) for the ray pointer
	actHaptic   = "/actions/main/out/haptic" // vibration: rumble feedback (grab engage/drop) on the active hand
)

// ActionToggleEditor / ActionToggleOverlays are the SteamVR action paths for the two default-UNBOUND
// in-headset actions, exported so the UI can query their live bind state via Manager.ActionBinding
// (single source of truth - aliases of the unexported paths above).
const (
	ActionToggleEditor   = actToggle
	ActionToggleOverlays = actOverlays
)

// slotActions are generic, UNBOUND-by-default boolean actions the user maps to physical inputs (incl.
// chords) in SteamVR's binding UI, then maps each slot → an app action in rave-mate.
func slotActions() []map[string]any {
	out := make([]map[string]any, 0, 8)
	for i := 1; i <= 8; i++ {
		out = append(out, boolAct(fmt.Sprintf("/actions/main/in/slot%d", i)))
	}
	return out
}

// controllerInputs is rave-mate's per-controller input map - the single source of truth for both the
// shipped default bindings (writeInputManifest) and the user-binding merge (defaultBindingFor).
func controllerInputs() []ctrlInputs {
	const vive = "/user/hand/left/input/application_menu"
	const viveR = "/user/hand/right/input/application_menu"
	return []ctrlInputs{
		{controller: "knuckles", leftStick: "/user/hand/left/input/thumbstick", rightStick: "/user/hand/right/input/thumbstick",
			leftA: "/user/hand/left/input/a", rightA: "/user/hand/right/input/a", leftB: "/user/hand/left/input/b", rightB: "/user/hand/right/input/b"},
		{controller: "oculus_touch", leftStick: "/user/hand/left/input/joystick", rightStick: "/user/hand/right/input/joystick",
			leftA: "/user/hand/left/input/x", rightA: "/user/hand/right/input/a", leftB: "/user/hand/left/input/y", rightB: "/user/hand/right/input/b"},
		{controller: "vive_controller", leftStick: "/user/hand/left/input/trackpad", rightStick: "/user/hand/right/input/trackpad",
			leftA: vive, rightA: viveR, leftB: vive, rightB: viveR},
		{controller: "generic", leftA: vive, rightA: viveR, leftB: vive, rightB: viveR},
	}
}

// defaultBindingFor builds rave-mate's current default binding for a controller type (nil if unknown),
// in JSON shape for the binding merge.
func defaultBindingFor(controller, summonBtn string) map[string]any {
	for _, c := range controllerInputs() {
		if c.controller == controller {
			return normalizeBinding(binding(controller, defaultSources(c, summonBtn)))
		}
	}
	return nil
}

// binding builds one controller's default binding file (sources under the /actions/main set).
func binding(controller string, sources []map[string]any) map[string]any {
	return map[string]any{
		"controller_type": controller,
		"name":            "rave-mate default",
		"description":     "rave-mate overlay default bindings (rebind in SteamVR)",
		"category":        "steamvr_input",
		"bindings": map[string]any{
			"/actions/main": map[string]any{
				"sources": sources,
				// Rumble output → each hand's haptic actuator (grab engage/drop feedback).
				"haptics": []map[string]any{
					{"output": actHaptic, "path": "/user/hand/left/output/haptic"},
					{"output": actHaptic, "path": "/user/hand/right/output/haptic"},
				},
				// Bind the aim pose to each hand's /pose/tip (where the controller points) so the ray
				// pointer aims like SteamVR's laser, not the offset raw device forward.
				"poses": []map[string]any{
					{"output": actAim, "path": "/user/hand/left/pose/tip"},
					{"output": actAim, "path": "/user/hand/right/pose/tip"},
				},
			},
		},
	}
}

// ctrlInputs names a controller's input paths for the default binding builder. leftA/rightA + leftB/rightB
// are the face buttons the summon action can bind to ("" = none → falls back to application_menu).
type ctrlInputs struct {
	controller            string
	leftStick, rightStick string // joystick/trackpad path per hand ("" = no push/pull binding)
	leftA, rightA         string // "A/X" face buttons
	leftB, rightB         string // "B/Y" face buttons
}

// summonSources binds the summon action to the chosen face button. btn: "ax" | "by"; anything else
// leaves summon unbound (user binds it in SteamVR).
func summonSources(c ctrlInputs, btn string) []map[string]any {
	var lp, rp string
	switch btn {
	case "ax":
		lp, rp = c.leftA, c.rightA
	case "by":
		lp, rp = c.leftB, c.rightB
	default:
		return nil
	}
	var s []map[string]any
	if lp != "" {
		s = append(s, clickSrc(lp, actSummon))
	}
	if rp != "" {
		s = append(s, clickSrc(rp, actSummon))
	}
	return s
}

// defaultSources builds one controller's default sources: grab (grip) + push/pull (stick) always,
// pointer_click on BOTH triggers (activate the ray-pointed overlay; manual ray-hit gating means an
// in-game trigger pull is unaffected when not pointing at a rave-mate overlay), plus the summon action
// on the chosen face button.
func defaultSources(c ctrlInputs, summonBtn string) []map[string]any {
	s := []map[string]any{
		gripForceSrc("/user/hand/left/input/grip", actGrab), // force+hysteresis → no accidental light-grip grabs
		gripForceSrc("/user/hand/right/input/grip", actGrab),
		clickSrc("/user/hand/left/input/trigger", actPClick),
		clickSrc("/user/hand/right/input/trigger", actPClick),
	}
	if c.leftStick != "" {
		s = append(s, joySrc(c.leftStick, actPushPull))
	}
	if c.rightStick != "" {
		s = append(s, joySrc(c.rightStick, actPushPull))
	}
	return append(s, summonSources(c, summonBtn)...)
}

// bindingFile is the on-disk name for a controller's default binding (single source of truth so the
// manifest reference + the written file never diverge).
func bindingFile(controller string) string { return "rave-mate_binding_" + controller + ".json" }

// userSavedBinding reads the user's SteamVR-saved personal binding for a controller (nil if none).
// SteamVR writes personal bindings to <home>/Documents/steamvr/input/<appkey>_<controller>.json.
func userSavedBinding(controller string) []byte {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	p := filepath.Join(home, "Documents", "steamvr", "input", vrAppKey+"_"+controller+".json")
	if fi, err := os.Stat(p); err != nil || fi.Size() > 1<<20 { // sane cap; binding files are a few KB
		return nil
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	return raw
}

// writeInputManifest writes actions.json + per-controller binding files to the data dir and returns the
// actions.json path. summonBtn ("ax"|"by"|"custom") picks which face button opens the editor.
func writeInputManifest(summonBtn string) (string, error) {
	dir, err := config.DataPath("rave-mate_actions.json")
	if err != nil {
		return "", err
	}
	base := filepath.Dir(dir)

	var defBindings []map[string]any
	current := map[string]bool{}
	for _, c := range controllerInputs() {
		def := binding(c.controller, defaultSources(c, summonBtn))
		body := def
		// Fold the user's SteamVR-saved personal binding into the shipped default: SteamVR reverts an
		// updated/re-registered app to its DEFAULT binding, which used to silently kill every bind the
		// user made themselves (summon, show/hide, slots). With the merge, the default IS their binding
		// plus whatever new actions this build added.
		if raw := userSavedBinding(c.controller); raw != nil {
			if merged := mergeUserIntoDefault(raw, def); merged != nil {
				body = merged
			}
		}
		if err := writeJSON(filepath.Join(base, bindingFile(c.controller)), body); err != nil {
			return "", err
		}
		defBindings = append(defBindings, map[string]any{"controller_type": c.controller, "binding_url": bindingFile(c.controller)})
		current[bindingFile(c.controller)] = true
	}
	pruneOrphanBindings(base, current) // drop stale files from an old naming scheme (e.g. vive→vive_controller rename), else SteamVR may load a dead default

	manifest := map[string]any{
		"default_bindings": defBindings,
		"actions": append([]map[string]any{
			boolAct(actToggle), boolAct(actOverlays), boolAct(actGrab), boolAct(actSummon), boolAct(actPClick), vec2Act(actPushPull), poseAct(actAim), vibAct(actHaptic),
		}, slotActions()...),
		"action_sets": []map[string]any{
			{"name": "/actions/main", "usage": "leftright"},
		},
		"localization": []map[string]any{{
			"language_tag":  "en_US",
			"/actions/main": "rave-mate overlay",
			actToggle:       "Open / close in-world editor",
			actOverlays:     "Show / hide overlays",
			actGrab:         "Grab / move overlay (hold)",
			actSummon:       "Open editor (hold) / show-hide (tap)",
			actPClick:       "Point + click a rave-mate overlay (trigger)",
			actPushPull:     "Push / pull grabbed overlay",
			actHaptic:       "Rumble feedback (grab)",
		}},
	}
	if err := writeJSON(dir, manifest); err != nil {
		return "", err
	}
	return dir, nil
}

// pruneOrphanBindings deletes rave-mate_binding_*.json files in dir that aren't in the current set,
// so a renamed controller's stale binding (e.g. rave-mate_binding_vive.json) can't override defaults.
// Scoped to our own rave-mate_binding_* glob - never touches other files.
func pruneOrphanBindings(dir string, keep map[string]bool) {
	matches, err := filepath.Glob(filepath.Join(dir, "rave-mate_binding_*.json"))
	if err != nil {
		return
	}
	for _, p := range matches {
		if !keep[filepath.Base(p)] {
			_ = os.Remove(p) // best-effort; a leftover only risks a stale binding, not correctness
		}
	}
}

func writeJSON(path string, body any) error {
	raw, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

package vroverlay

import (
	"encoding/json"
	"testing"
)

// knucklesDefault is rave-mate's current default binding for Index controllers (grip→grab, trigger→
// pointer_click, stick→push_pull, A→summon, aim pose, haptics).
func knucklesDefault() map[string]any {
	c := ctrlInputs{controller: "knuckles",
		leftStick: "/user/hand/left/input/thumbstick", rightStick: "/user/hand/right/input/thumbstick",
		leftA: "/user/hand/left/input/a", rightA: "/user/hand/right/input/a",
		leftB: "/user/hand/left/input/b", rightB: "/user/hand/right/input/b"}
	return normalizeBinding(binding("knuckles", defaultSources(c, "ax")))
}

func parseBinding(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("parse: %v", err)
	}
	return m
}

// sourceWithPath returns the first /actions/main source whose physical path == path (nil = none).
func sourceWithPath(b map[string]any, path string) map[string]any {
	for _, s := range sliceOf(actionSet(b), "sources") {
		if str(asMap(s), "path") == path {
			return asMap(s)
		}
	}
	return nil
}

func bindsOutput(b map[string]any, action string) bool {
	bound, _ := scanBound(actionSet(b))
	return bound[action]
}

const trigL = "/user/hand/left/input/trigger"

// A user binding that customised grab→grip + summon→A + push_pull→stick but predates pointer_click and
// the aim pose (triggers FREE). Merge should ADD pointer_click on both triggers + the aim poses, no
// conflicts, and keep every user source.
func TestMergeAddsMissingActionsWhenInputFree(t *testing.T) {
	user := parseBinding(t, `{
      "controller_type":"knuckles","name":"my custom","category":"steamvr_input",
      "bindings":{"/actions/main":{"sources":[
        {"path":"/user/hand/left/input/grip","mode":"button","inputs":{"click":{"output":"/actions/main/in/grab"}}},
        {"path":"/user/hand/right/input/grip","mode":"button","inputs":{"click":{"output":"/actions/main/in/grab"}}},
        {"path":"/user/hand/left/input/a","mode":"button","inputs":{"click":{"output":"/actions/main/in/summon"}}}
      ]}}}`)

	merged, conflicts := mergeBinding(user, knucklesDefault())
	if len(conflicts) != 0 {
		t.Fatalf("want no conflicts, got %+v", conflicts)
	}
	if src := sourceWithPath(merged, trigL); src == nil {
		t.Fatal("pointer_click not merged onto the free left trigger")
	} else if got := sourceOutputs(src); len(got) != 1 || got[0] != actPClick {
		t.Fatalf("left trigger outputs = %v, want [%s]", got, actPClick)
	}
	if !bindsOutput(merged, actAim) {
		t.Fatal("aim pose not merged in (drift heal)")
	}
	// user's own mappings survive
	if !bindsOutput(merged, actSummon) || !bindsOutput(merged, actGrab) {
		t.Fatal("user's summon/grab mappings dropped by merge")
	}
}

// User remapped the trigger to grab. Our pointer_click default wants the trigger → conflict reported,
// pointer_click NOT force-added over the user's mapping.
func TestMergeReportsConflictOnUsedInput(t *testing.T) {
	user := parseBinding(t, `{
      "controller_type":"knuckles","name":"my custom","category":"steamvr_input",
      "bindings":{"/actions/main":{"sources":[
        {"path":"/user/hand/left/input/trigger","mode":"button","inputs":{"click":{"output":"/actions/main/in/grab"}}},
        {"path":"/user/hand/right/input/trigger","mode":"button","inputs":{"click":{"output":"/actions/main/in/grab"}}}
      ]}}}`)

	merged, conflicts := mergeBinding(user, knucklesDefault())
	if len(conflicts) == 0 {
		t.Fatal("want a pointer_click/trigger conflict, got none")
	}
	var found bool
	for _, c := range conflicts {
		if c.Action == actPClick && c.Input == trigL {
			found = true
			if c.UsedBy != actGrab {
				t.Fatalf("conflict UsedBy = %q, want %s", c.UsedBy, actGrab)
			}
		}
	}
	if !found {
		t.Fatalf("no pointer_click/left-trigger conflict in %+v", conflicts)
	}
	// the user's trigger→grab mapping is untouched
	if src := sourceWithPath(merged, trigL); src == nil || sourceOutputs(src)[0] != actGrab {
		t.Fatal("user's trigger→grab mapping was clobbered")
	}
}

// If the user already outputs pointer_click (bound elsewhere, e.g. B button), the default trigger source
// is neither duplicated nor a conflict.
func TestMergeSkipsAlreadyBoundAction(t *testing.T) {
	user := parseBinding(t, `{
      "controller_type":"knuckles","name":"my custom","category":"steamvr_input",
      "bindings":{"/actions/main":{"sources":[
        {"path":"/user/hand/left/input/b","mode":"button","inputs":{"click":{"output":"/actions/main/in/pointer_click"}}},
        {"path":"/user/hand/right/input/b","mode":"button","inputs":{"click":{"output":"/actions/main/in/pointer_click"}}}
      ]}}}`)

	merged, conflicts := mergeBinding(user, knucklesDefault())
	for _, c := range conflicts {
		if c.Action == actPClick {
			t.Fatalf("unexpected pointer_click conflict: %+v", c)
		}
	}
	if sourceWithPath(merged, trigL) != nil {
		t.Fatal("duplicated pointer_click onto the trigger though it was already bound")
	}
}

// A user binding with no /actions/main set adopts our default wholesale (no conflicts).
func TestMergeAdoptsDefaultWhenNoActionSet(t *testing.T) {
	user := parseBinding(t, `{"controller_type":"knuckles","name":"empty","bindings":{}}`)
	merged, conflicts := mergeBinding(user, knucklesDefault())
	if len(conflicts) != 0 {
		t.Fatalf("want no conflicts, got %+v", conflicts)
	}
	if !bindsOutput(merged, actGrab) || !bindsOutput(merged, actPClick) || !bindsOutput(merged, actAim) {
		t.Fatal("default set not adopted when the user had no /actions/main")
	}
}

// A user source that IS our old mapping verbatim (same path/mode/outputs, no params - inherited from a
// pre-grip-force default) gets upgraded to the default's parameterised version (the force thresholds).
func TestMergeUpgradesParamOnlySource(t *testing.T) {
	user := parseBinding(t, `{
      "controller_type":"knuckles","name":"my custom","category":"steamvr_input",
      "bindings":{"/actions/main":{"sources":[
        {"path":"/user/hand/left/input/grip","mode":"button","inputs":{"click":{"output":"/actions/main/in/grab"}}}
      ]}}}`)
	merged, _ := mergeBinding(user, knucklesDefault())
	src := sourceWithPath(merged, "/user/hand/left/input/grip")
	if src == nil {
		t.Fatal("grip source missing after merge")
	}
	params := asMap(src["parameters"])
	if str(params, "click_activate_threshold") == "" {
		t.Fatal("inherited grip source not upgraded with the force thresholds")
	}
}

// A DELIBERATE rebind (different mode) is never replaced by the default's parameterised source.
func TestMergeKeepsDeliberateRebind(t *testing.T) {
	user := parseBinding(t, `{
      "controller_type":"knuckles","name":"my custom","category":"steamvr_input",
      "bindings":{"/actions/main":{"sources":[
        {"path":"/user/hand/left/input/grip","mode":"force_sensor","inputs":{"click":{"output":"/actions/main/in/grab"}}}
      ]}}}`)
	merged, _ := mergeBinding(user, knucklesDefault())
	src := sourceWithPath(merged, "/user/hand/left/input/grip")
	if src == nil || str(src, "mode") != "force_sensor" {
		t.Fatalf("user's deliberate force_sensor grip rebind was clobbered: %+v", src)
	}
}

// mergeUserIntoDefault: the shipped default carries the user's binds (summon survives an update even
// when SteamVR reverts to the default binding) while keeping the default's controller identity.
func TestMergeUserIntoDefaultCarriesUserBinds(t *testing.T) {
	userJSON := []byte(`{
      "controller_type":"knuckles","name":"my custom","category":"steamvr_input",
      "bindings":{"/actions/main":{"sources":[
        {"path":"/user/hand/left/input/a","mode":"button","inputs":{"click":{"output":"/actions/main/in/summon"}}},
        {"path":"/user/hand/right/input/b","mode":"button","inputs":{"click":{"output":"/actions/main/in/toggle_overlays"}}}
      ]}}}`)
	// summonBtn "custom" → the raw default binds NO summon (the exact state that killed the menu keybind)
	c := ctrlInputs{controller: "knuckles",
		leftStick: "/user/hand/left/input/thumbstick", rightStick: "/user/hand/right/input/thumbstick",
		leftA: "/user/hand/left/input/a", rightA: "/user/hand/right/input/a"}
	def := binding("knuckles", defaultSources(c, "custom"))
	merged := mergeUserIntoDefault(userJSON, def)
	if merged == nil {
		t.Fatal("merge returned nil")
	}
	if !bindsOutput(merged, actSummon) || !bindsOutput(merged, actOverlays) {
		t.Fatal("user's summon/toggle_overlays binds not carried into the default")
	}
	if !bindsOutput(merged, actGrab) || !bindsOutput(merged, actPClick) || !bindsOutput(merged, actAim) {
		t.Fatal("default grab/pointer_click/aim missing from the merged default")
	}
	if str(merged, "controller_type") != "knuckles" {
		t.Fatalf("controller_type = %q", str(merged, "controller_type"))
	}
}

// Garbage user JSON → nil (caller falls back to the plain default).
func TestMergeUserIntoDefaultBadJSON(t *testing.T) {
	if mergeUserIntoDefault([]byte("{nope"), knucklesDefault()) != nil {
		t.Fatal("want nil for unparseable user binding")
	}
}

// The merge must not mutate the caller's input map (we build a fresh binding).
func TestMergeDoesNotMutateInput(t *testing.T) {
	user := parseBinding(t, `{
      "controller_type":"knuckles","bindings":{"/actions/main":{"sources":[
        {"path":"/user/hand/left/input/grip","mode":"button","inputs":{"click":{"output":"/actions/main/in/grab"}}}
      ]}}}`)
	before := len(sliceOf(actionSet(user), "sources"))
	_, _ = mergeBinding(user, knucklesDefault())
	if after := len(sliceOf(actionSet(user), "sources")); after != before {
		t.Fatalf("input mutated: sources %d → %d", before, after)
	}
}

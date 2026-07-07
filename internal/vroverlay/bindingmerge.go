package vroverlay

// Binding merge (task #12 Part B). When rave-mate adds NEW actions, a user who already customised their
// SteamVR binding would otherwise keep their old profile (missing our new actions → those controls dead)
// or be clobbered by our default (losing their changes). Instead we build a THIRD, merged binding: the
// user's saved binding verbatim, PLUS our default source for every action the user's binding doesn't
// already output - skipping (and reporting) any default whose physical input the user already uses.
//
// This is a pure function over the SteamVR binding JSON shape (map[string]any / []any, i.e. the result
// of json.Unmarshal). Discovery of the user's active binding file + activating the merged one (the
// per-app CurrentURL setting) is SteamVR-internals and lives behind the cgo `vr` build - this core is
// verified offline. Callers normalize both inputs through normalizeBinding first.

import (
	"encoding/json"
	"fmt"

	"rave.page/mate/internal/config"
)

// BuildMergedBinding merges the user's saved binding (raw SteamVR binding JSON) with rave-mate's current
// default for controller, writes the merged result to a NEW file in the data dir (the user's file is
// NEVER touched), and returns its path + any unresolved conflicts. Activating it - pointing the app's
// per-controller CurrentURL setting at the returned path - is the SteamVR-side step done under `vr`.
func BuildMergedBinding(controller, summonBtn string, userBindingJSON []byte) (string, []BindingConflict, error) {
	var user map[string]any
	if err := json.Unmarshal(userBindingJSON, &user); err != nil {
		return "", nil, fmt.Errorf("parse user binding: %w", err)
	}
	def := defaultBindingFor(controller, summonBtn)
	if def == nil {
		return "", nil, fmt.Errorf("unknown controller %q", controller)
	}
	merged, conflicts := mergeBinding(user, def)
	merged["name"] = "rave-mate merged (user + defaults)"
	merged["description"] = "Auto-merged from your saved binding + rave-mate defaults - rebind in SteamVR to resolve conflicts"
	path, err := config.DataPath(bindingFile(controller + "_merged"))
	if err != nil {
		return "", nil, err
	}
	if err := writeJSON(path, merged); err != nil {
		return "", nil, err
	}
	return path, conflicts, nil
}

// mergeUserIntoDefault merges the user's saved binding JSON over def (a built default map), keeping
// def's controller identity. The SHIPPED DEFAULT then carries the user's binds - so when SteamVR
// reverts an updated/re-registered app to its default binding (which is what killed summon after the
// 2026-07-01 update), the user's summon/slot mappings survive. nil when the user JSON doesn't parse.
func mergeUserIntoDefault(userJSON []byte, def map[string]any) map[string]any {
	var user map[string]any
	if json.Unmarshal(userJSON, &user) != nil {
		return nil
	}
	nd := normalizeBinding(def)
	if nd == nil {
		return nil
	}
	merged, _ := mergeBinding(user, nd)
	if merged == nil || actionSet(merged) == nil {
		return nil
	}
	merged["controller_type"] = def["controller_type"]
	merged["name"] = "rave-mate default (+ your saved binds)"
	merged["description"] = "rave-mate defaults merged with your saved SteamVR binding"
	merged["category"] = "steamvr_input"
	return merged
}

// BindingConflict is a new-action default we could NOT merge because the physical input it needs is
// already mapped by the user. The user resolves it in SteamVR (or accepts the action stays unbound).
type BindingConflict struct {
	Action string // action path we wanted to bind (e.g. /actions/main/in/pointer_click)
	Input  string // physical input path our default wanted (already user-mapped)
	UsedBy string // the action the user already mapped that input to ("" = mapped but output unknown)
}

// normalizeBinding round-trips a binding through JSON so both user-parsed maps ([]any slices) and our
// builder output ([]map[string]any slices) share one shape for the merge walk. Returns nil on failure.
func normalizeBinding(v any) map[string]any {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m
}

// mergeBinding merges defaultBinding's sources/poses/haptics into userBinding without dropping any user
// mapping. Returns the merged binding (a fresh map - inputs untouched) + the unresolved conflicts. Both
// args must be JSON-shape (see normalizeBinding). Only the /actions/main set is merged; any other set in
// the user binding is preserved verbatim.
func mergeBinding(userBinding, defaultBinding map[string]any) (map[string]any, []BindingConflict) {
	merged := normalizeBinding(userBinding) // deep copy so we never mutate the user's file in memory
	if merged == nil {
		merged = map[string]any{}
	}

	uMain := actionSet(merged)
	dMain := actionSet(defaultBinding)
	if dMain == nil { // nothing to merge in
		return merged, nil
	}
	if uMain == nil { // user has no /actions/main at all → adopt our default set wholesale (no conflicts)
		setActionSet(merged, normalizeBinding(dMain))
		return merged, nil
	}

	uSources := sliceOf(uMain, "sources")
	boundActions, usedInputs := scanBound(uMain)

	// Param-only upgrade: when the user's source is OUR mapping verbatim (same path/mode/outputs - i.e.
	// inherited from an older default, not a deliberate rebind) and only our new "parameters" differ,
	// take the default version. This is how a binding saved before the grip force thresholds still gets
	// the no-accidental-grab fix; a user who deliberately changed mode/path/output keeps theirs.
	uIdx := map[string]int{}
	for i, s := range uSources {
		if p := str(asMap(s), "path"); p != "" {
			if _, seen := uIdx[p]; !seen {
				uIdx[p] = i
			}
		}
	}

	var conflicts []BindingConflict
	for _, ds := range sliceOf(dMain, "sources") {
		dsm := asMap(ds)
		acts := sourceOutputs(dsm)
		if i, ok := uIdx[str(dsm, "path")]; ok {
			us := asMap(uSources[i])
			if sameOutputs(us, dsm) && str(us, "mode") == str(dsm, "mode") &&
				us["parameters"] == nil && dsm["parameters"] != nil {
				uSources[i] = dsm
			}
		}
		if len(acts) == 0 || allBound(acts, boundActions) {
			continue // user already outputs every action this default source would add
		}
		path := str(dsm, "path")
		if owner, taken := usedInputs[path]; taken {
			for _, a := range acts {
				if !boundActions[a] {
					conflicts = append(conflicts, BindingConflict{Action: a, Input: path, UsedBy: owner})
				}
			}
			continue
		}
		uSources = append(uSources, dsm) // input is free → add our default source
		usedInputs[path] = firstNew(acts, boundActions)
		for _, a := range acts {
			boundActions[a] = true
		}
	}
	uMain["sources"] = uSources

	// Poses + haptics carry no exclusive physical button (aim = /pose/tip, haptic = actuator), so they
	// never conflict - add any our default declares that the user's binding lacks. This is what heals the
	// pointer-drift case: a binding saved before we added the aim pose gets /pose/tip merged in.
	uMain["poses"] = mergeOutputs(sliceOf(uMain, "poses"), sliceOf(dMain, "poses"))
	uMain["haptics"] = mergeOutputs(sliceOf(uMain, "haptics"), sliceOf(dMain, "haptics"))
	return merged, conflicts
}

// scanBound collects every action the user's set already outputs + every physical input path it uses
// (path → the first action bound to it, for conflict reporting).
func scanBound(main map[string]any) (bound map[string]bool, usedInputs map[string]string) {
	bound = map[string]bool{}
	usedInputs = map[string]string{}
	for _, s := range sliceOf(main, "sources") {
		sm := asMap(s)
		p := str(sm, "path")
		outs := sourceOutputs(sm)
		for _, a := range outs {
			bound[a] = true
		}
		if p != "" {
			if _, ok := usedInputs[p]; !ok {
				owner := ""
				if len(outs) > 0 {
					owner = outs[0]
				}
				usedInputs[p] = owner
			}
		}
	}
	for _, key := range []string{"poses", "haptics"} {
		for _, o := range sliceOf(main, key) {
			if a := str(asMap(o), "output"); a != "" {
				bound[a] = true
			}
		}
	}
	return bound, usedInputs
}

// sourceOutputs lists the action paths a source outputs (across all its input modes: click/position/…).
func sourceOutputs(src map[string]any) []string {
	var out []string
	for _, in := range asMap(src["inputs"]) {
		if a := str(asMap(in), "output"); a != "" {
			out = append(out, a)
		}
	}
	return out
}

// mergeOutputs appends default {output,path} entries whose output isn't already present in user.
func mergeOutputs(user, def []any) []any {
	have := map[string]bool{}
	for _, o := range user {
		have[str(asMap(o), "output")+"\x00"+str(asMap(o), "path")] = true
	}
	for _, o := range def {
		om := asMap(o)
		if !have[str(om, "output")+"\x00"+str(om, "path")] {
			user = append(user, om)
		}
	}
	return user
}

// sameOutputs reports whether two sources output the same action set.
func sameOutputs(a, b map[string]any) bool {
	ao, bo := sourceOutputs(a), sourceOutputs(b)
	if len(ao) != len(bo) {
		return false
	}
	set := map[string]bool{}
	for _, x := range ao {
		set[x] = true
	}
	for _, x := range bo {
		if !set[x] {
			return false
		}
	}
	return true
}

func allBound(acts []string, bound map[string]bool) bool {
	for _, a := range acts {
		if !bound[a] {
			return false
		}
	}
	return true
}

func firstNew(acts []string, bound map[string]bool) string {
	for _, a := range acts {
		if !bound[a] {
			return a
		}
	}
	if len(acts) > 0 {
		return acts[0]
	}
	return ""
}

// actionSet returns the binding's /actions/main object (nil if absent).
func actionSet(b map[string]any) map[string]any {
	return asMap(asMap(b["bindings"])["/actions/main"])
}

func setActionSet(b map[string]any, main map[string]any) {
	bindings := asMap(b["bindings"])
	if bindings == nil {
		bindings = map[string]any{}
		b["bindings"] = bindings
	}
	bindings["/actions/main"] = main
}

func sliceOf(m map[string]any, key string) []any {
	s, _ := m[key].([]any)
	return s
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func str(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

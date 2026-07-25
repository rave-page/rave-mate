package webui

// Nil-slice guard for the settings sub-view states: a nil Go slice marshals to JSON null, which the
// Zig state parser REJECTS - the body (and with it the whole settings tab) would then silently fall
// back to the Go renderer. Every slice these builders emit must be non-nil even in its zero state.

import (
	"encoding/json"
	"strings"
	"testing"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/gridfix"
	"rave.page/mate/internal/gridfix/train"
	"rave.page/mate/internal/updater"
)

func TestSettingsSubStatesHaveNoNullSlices(t *testing.T) {
	zero := &config.GridFixFeature{}
	states := map[string]any{
		"gridfixProbing":  gridfixCardStateOf(zero, gridfix.EnvStatus{}, false),
		"gridfixVariants": gridfixCardStateOf(zero, gridfix.EnvStatus{BasePython: "py"}, true),
		"gridfixModel":    gridfixModelStateOf(emptySel(), false, 0, false, train.TrainEvent{}, nil, ""),
		"bridgeNoGate":    bridgeCardStateOf(bridgeBits{}),
		"bridgeGate":      bridgeCardStateOf(bridgeBits{HasGate: true}),
		"updHidden":       updFlowStateOf(updater.Status{}),
	}
	for name, st := range states {
		js, err := json.Marshal(st)
		if err != nil {
			t.Fatalf("%s: marshal: %v", name, err)
		}
		if s := string(js); strings.Contains(s, ":null") {
			t.Errorf("%s: JSON carries a null (Zig parse would fail → silent Go fallback): %s", name, s)
		}
	}
}

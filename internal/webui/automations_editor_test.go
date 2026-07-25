package webui

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"rave.page/mate/internal/automation"
)

// aeSteps wraps a chain in form steps with fresh keys - the shape aeSt.acts holds.
func aeSteps(s *aeSt, acts ...automation.Action) []aeStep {
	out := make([]aeStep, 0, len(acts))
	for _, a := range acts {
		out = append(out, aeStep{s.nextKey(), a})
	}
	return out
}

// aeOpenForm puts the editor on screen and hands back its session - every mutator now demands one
// (a write with no form on screen belongs to nothing).
func aeOpenForm(u *UI) modalTok { return u.openModalAs(aeOwner, u.aeModalHTML()) }

// aeKeyAt is the identity of the step at index i.
func aeKeyAt(u *UI, i int) uint64 {
	u.ae.mu.Lock()
	defer u.ae.mu.Unlock()
	return u.ae.acts[i].key
}

// aeK renders a step key the way an act argument carries it.
func aeK(k uint64) string { return strconv.FormatUint(k, 10) }

func TestAeParseExts(t *testing.T) {
	got := aeParseExts(" wav, .FLAC  aiff;.M4A ")
	want := []string{".wav", ".flac", ".aiff", ".m4a"} // lower-case + dot-prefixed = Match.matches' form
	if !slices.Equal(got, want) {
		t.Fatalf("aeParseExts = %v, want %v", got, want)
	}
	if got := aeParseExts("   "); got != nil {
		t.Fatalf("blank exts = %v, want nil (= match any)", got)
	}
}

// aeBuild must apply the wire-shape checks itself and delegate the chain verdict to
// automation.ValidateActions (no second copy of the engine's rules).
func TestAeBuildValidation(t *testing.T) {
	ok := []automation.Action{{Type: automation.ActionTranscode, PresetID: "remux"}}
	cases := []struct {
		name    string
		mut     func(*aeSt)
		wantErr string
	}{
		{"no label", func(s *aeSt) { s.label = "  " }, "name"},
		{"no watch dir", func(s *aeSt) { s.watch = "" }, "folder"},
		{"bad regex", func(s *aeSt) { s.pattern = "([a-z" }, "regular expression"},
		{"empty chain", func(s *aeSt) { s.acts = nil }, "at least one action"},
		{"transcode without preset", func(s *aeSt) {
			s.acts = aeSteps(s, automation.Action{Type: automation.ActionTranscode})
		}, "requires a preset"},
		{"move without dir", func(s *aeSt) {
			s.acts = aeSteps(s, automation.Action{Type: automation.ActionMove})
		}, "output directory"},
		{"delete not last", func(s *aeSt) {
			s.acts = aeSteps(s, automation.Action{Type: automation.ActionDelete}, ok[0])
		}, "delete must be the last action"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u := &UI{}
			u.ae.label, u.ae.watch = "Sets", `C:\sets`
			u.ae.acts = aeSteps(&u.ae, ok...)
			c.mut(&u.ae)
			_, err := u.aeBuild()
			if err == nil {
				t.Fatalf("%s: saved without error", c.name)
			}
			if !strings.Contains(strings.ToLower(err.Error()), c.wantErr) {
				t.Fatalf("%s: err = %q, want it to mention %q", c.name, err, c.wantErr)
			}
		})
	}

	// happy path: a full chain round-trips into the engine's shape
	u := &UI{}
	u.ae.label, u.ae.watch, u.ae.enabled = " Sets ", `C:\sets`, true
	u.ae.extsTx, u.ae.minAge, u.ae.minSize = "wav, flac", 30, 5*aeMiB
	u.ae.acts = aeSteps(&u.ae, ok[0], automation.Action{Type: automation.ActionDelete})
	a, err := u.aeBuild()
	if err != nil {
		t.Fatalf("valid form rejected: %v", err)
	}
	if a.Label != "Sets" { // trimmed
		t.Fatalf("label = %q, want trimmed", a.Label)
	}
	if !slices.Equal(a.Match.Extensions, []string{".wav", ".flac"}) {
		t.Fatalf("exts = %v", a.Match.Extensions)
	}
	if a.Match.MinAgeDays != 30 || a.Match.MinSizeBytes != 5*aeMiB || !a.Enabled {
		t.Fatalf("match/enabled not carried: %+v enabled=%v", a.Match, a.Enabled)
	}
	// the built chain must not alias the form's slice - a later edit would mutate what we saved
	if len(a.Actions) > 0 {
		a.Actions[0].PresetID = "mutated"
		if u.ae.acts[0].act.PresetID == "mutated" {
			t.Fatal("aeBuild aliased the form's action slice")
		}
	}
}

// Editing an unrelated field must not re-round a byte threshold another client wrote:
// minSize stays exact until the MB field itself is edited.
func TestAeMinSizeExactUnlessEdited(t *testing.T) {
	u := &UI{}
	u.ae.load(automation.Automation{
		Label: "A", WatchDir: `C:\x`,
		Match:   automation.Match{MinSizeBytes: 1500000}, // not a whole number of MB
		Actions: []automation.Action{{Type: automation.ActionTrimSilence}},
	})
	tok := aeOpenForm(u)
	u.aeField(tok, "label", "renamed") // an edit elsewhere in the form
	a, err := u.aeBuild()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if a.Match.MinSizeBytes != 1500000 {
		t.Fatalf("unrelated edit re-rounded minSize to %d, want 1500000", a.Match.MinSizeBytes)
	}
	u.aeField(tok, "minsize", "2") // now the user edits it
	if a, _ := u.aeBuild(); a.Match.MinSizeBytes != 2*aeMiB {
		t.Fatalf("edited minSize = %d, want %d", a.Match.MinSizeBytes, 2*aeMiB)
	}
}

func TestAeChainEditing(t *testing.T) {
	u := &UI{} // nil shell: openModal's eval is a no-op, so handlers exercise state only
	tok := aeOpenForm(u)
	u.aeAdd(tok, automation.ActionTrimSilence)
	u.aeAdd(tok, automation.ActionTranscode)
	u.aeAdd(tok, automation.ActionDelete)
	types := func() []automation.ActionType {
		out := []automation.ActionType{}
		for _, st := range u.ae.acts {
			out = append(out, st.act.Type)
		}
		return out
	}
	want := []automation.ActionType{automation.ActionTrimSilence, automation.ActionTranscode, automation.ActionDelete}
	if !slices.Equal(types(), want) {
		t.Fatalf("after adds = %v, want %v", types(), want)
	}
	trim, transcode := aeKeyAt(u, 0), aeKeyAt(u, 1)
	u.aeMove(tok, trim, 1) // swap trim/transcode
	if !slices.Equal(types(), []automation.ActionType{automation.ActionTranscode, automation.ActionTrimSilence, automation.ActionDelete}) {
		t.Fatalf("after move = %v", types())
	}
	u.aeMove(tok, transcode, -1) // off the top edge: no-op, must not panic or drop a step
	if len(u.ae.acts) != 3 {
		t.Fatalf("out-of-range move mutated the chain: %v", types())
	}
	u.aeRemove(tok, trim)
	if !slices.Equal(types(), []automation.ActionType{automation.ActionTranscode, automation.ActionDelete}) {
		t.Fatalf("after remove = %v", types())
	}
	u.aeRemove(tok, trim) // the key is dead now: a no-op, and NOT a hit on whatever took its place
	if len(u.ae.acts) != 2 {
		t.Fatalf("removing a dead key mutated the chain: %v", types())
	}

	// per-step fields land on the step they NAME
	u.aeActField(tok, aeK(transcode)+":preset", "flac")
	u.aeActField(tok, aeK(transcode)+":loudon", "true")
	u.aeActField(tok, aeK(transcode)+":loudi", "-9")
	if u.ae.acts[0].act.PresetID != "flac" || !u.ae.acts[0].act.LoudnessOn || u.ae.acts[0].act.LoudnessI != -9 {
		t.Fatalf("step 0 = %+v", u.ae.acts[0].act)
	}
	if u.ae.acts[1].act.PresetID != "" || u.ae.acts[1].act.LoudnessOn {
		t.Fatalf("step 1 was touched: %+v", u.ae.acts[1].act)
	}
	u.aeActField(tok, "9999:preset", "x") // unknown key: no-op, no panic
	if u.ae.acts[0].act.PresetID != "flac" || u.ae.acts[1].act.PresetID != "" {
		t.Fatal("an unknown step key wrote into a live step")
	}
}

// The form is the last stop before a run: per-type fields only, the destructive step is called
// out, and nothing is a browser-native control.
func TestAeModalRendersPerTypeFields(t *testing.T) {
	u, _ := newTestHeadless(t)
	u.ae.load(automation.Automation{
		Label: "Archive", WatchDir: `C:\sets`,
		Actions: []automation.Action{
			{Type: automation.ActionTranscode, PresetID: "remux"},
			{Type: automation.ActionMove, OutputDir: `C:\arch`},
			{Type: automation.ActionDelete},
		},
	})
	k0, k1 := aeK(aeKeyAt(u, 0)), aeK(aeKeyAt(u, 1))
	h := u.aeModalHTML()
	for _, want := range []string{
		"auto-ed:label", "auto-ed:watch", "pick-dir:auto-ed:watch", // identity + dir picker
		"auto-ed:minage", "auto-ed:pattern", "auto-ed:exts", // match editor
		"auto-ed-preset-" + k0, "auto-ed-af:" + k0 + ":loudon", // transcode: preset + loudness override
		"auto-ed-af:" + k1 + ":dir", "pick-dir:auto-ed-af:" + k1 + ":dir", // move: output dir + its picker
		"auto-ed-up:" + k1, "auto-ed-down:" + k1, "auto-ed-rm:" + k1, // reorder/remove
		"auto-ed-add:delete", "auto-ed-save",
	} {
		if !strings.Contains(h, want) {
			t.Fatalf("modal missing %q", want)
		}
	}
	if strings.Contains(h, "<select") {
		t.Fatal("modal used a browser-native <select>")
	}
	// delete is destructive: the warning rides the step, not just the tooltip
	if !strings.Contains(h, "no undo") || !strings.Contains(h, "Must be the last step") {
		t.Fatal("delete step did not surface its destructive/terminal warning")
	}
	// move takes no preset and no loudness block
	if strings.Contains(h, "auto-ed-preset-"+k1) || strings.Contains(h, "auto-ed-af:"+k1+":loudon") {
		t.Fatal("move step rendered transcode-only fields")
	}
	// loudness targets stay hidden until the override is on
	if strings.Contains(h, "auto-ed-af:"+k0+":loudi") {
		t.Fatal("loudness targets shown while the override is off")
	}
	u.aeActField(aeOpenForm(u), k0+":loudon", "true")
	if h := u.aeModalHTML(); !strings.Contains(h, "auto-ed-af:"+k0+":loudi") ||
		!strings.Contains(h, "auto-ed-af:"+k0+":loudtp") || !strings.Contains(h, "auto-ed-af:"+k0+":loudraise") {
		t.Fatal("loudness override on but targets not rendered")
	}
}

// A field that gates conditional copy must re-render, or the user sets it and sees nothing.
// The age gate is the case that matters: it silently turns a watch automation into a no-op.
func TestAeMinAgeWarningReRenders(t *testing.T) {
	const warn = "only does anything on a schedule"
	u, c := newTestHeadless(t)
	u.aeNew() // opens the modal at minAge=0 - this eval must NOT carry the warning
	tok := u.modalCur()
	u.aeField(tok, "minage", "30")
	// The warning can only reach the page if the field re-patched the modal: asserting on
	// aeModalHTML() instead would pass even with the re-render dropped.
	c.waitEval(t, warn)
	u.aeField(tok, "minage", "0")
	if strings.Contains(u.aeModalHTML(), warn) {
		t.Fatal("age gate cleared but the warning stuck")
	}
}

// The live banner tells the user before the save click, using the engine's own verdict.
func TestAeChainHTMLLiveValidation(t *testing.T) {
	u, _ := newTestHeadless(t)
	u.ae.load(automation.Automation{Actions: []automation.Action{
		{Type: automation.ActionDelete},
		{Type: automation.ActionTranscode, PresetID: "remux"},
	}})
	u.ae.mu.Lock()
	var st aeModalSt
	u.aeChainState(&st, &u.ae, nil)
	u.ae.mu.Unlock()
	h := aeChainHTMLOf(st)
	if !strings.Contains(h, "delete must be the last action") {
		t.Fatal("invalid chain rendered no live validation banner")
	}
}

func TestAutoChainSummary(t *testing.T) {
	got := autoChainSummary([]automation.Action{
		{Type: automation.ActionTrimSilence}, {Type: automation.ActionTranscode}, {Type: automation.ActionDelete},
	})
	if !strings.Contains(got, "→") || !strings.Contains(got, "Trim silence") || !strings.Contains(got, "Delete") {
		t.Fatalf("summary = %q", got)
	}
}

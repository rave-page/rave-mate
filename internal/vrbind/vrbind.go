// Package vrbind is the action-binding core for rave-mate's VR/MIDI hotkeys: a catalog of
// bindable app actions, a user binding list (a SteamVR action and/or a MIDI input → an
// action + target), and a dispatcher that fires the matching handler. Pure Go - no cgo, VR,
// or MIDI deps - so the binding logic is unit-testable; the VR/MIDI/UI layers feed it.
//
// Multiple binds may target the same action (assign several inputs to one action), and a
// single bind may carry both a VR action and a MIDI key (fires on either input).
package vrbind

import "sync"

// ActionID identifies a bindable app action.
type ActionID string

const (
	ActEditorToggle   ActionID = "editor.toggle"
	ActOverlaysToggle ActionID = "overlays.toggle"
	ActOverlayShow    ActionID = "overlay.show"
	ActOverlayHide    ActionID = "overlay.hide"
	ActOverlayToggle  ActionID = "overlay.toggle"
	ActOBSRecord      ActionID = "obs.record"
	ActOBSStream      ActionID = "obs.stream"
	ActOBSMic         ActionID = "obs.mic"
	// Speech-to-text (Whisper → Twitch chat) controls.
	ActSTTRecord    ActionID = "stt.record"    // start/stop dictation (push-to-talk style)
	ActSTTSend      ActionID = "stt.send"      // transcribe + send the buffered utterance to chat
	ActSTTDiscard   ActionID = "stt.discard"   // drop the buffered utterance without sending
	ActSTTClipboard ActionID = "stt.clipboard" // copy the last transcript to the OS clipboard
	// Application groups (relaunch a DJ-rig app set after a crash).
	ActAppGroupLaunch ActionID = "appgroup.launch" // launch every not-running app in a group

	// Desktop-UI actions (webview renderer registers the handlers; MIDI-mappable keybind twins).
	// Cue editor - fire only while the editor is open (handlers gate on view scope).
	ActCEAudition      ActionID = "ui.ce.audition"      // Hold: play from cursor, release = snap back (Space twin)
	ActCECursor        ActionID = "ui.ce.cursor"        // Step: cursor ±1 beat (←/→)
	ActCECursorJump    ActionID = "ui.ce.cursorJump"    // Step: cursor ±jump beats (Shift+←/→)
	ActCEJumpSize      ActionID = "ui.ce.jumpSize"      // Step: grow/shrink the jump size (Shift+↑/↓)
	ActCETrack         ActionID = "ui.ce.track"         // Step: editor to prev/next collection track (↑/↓)
	ActCEGridNudge     ActionID = "ui.ce.gridNudge"     // Step: beatgrid ±10ms (Ctrl+←/→)
	ActCEGridNudgeFine ActionID = "ui.ce.gridNudgeFine" // Step: beatgrid ±1ms (Ctrl+Shift+←/→)
	ActCEDropAdd       ActionID = "ui.ce.dropAdd"       // Trigger: drop marker at cursor (Enter/T)
	ActCEDropDel       ActionID = "ui.ce.dropDel"       // Trigger: remove drop at cursor (Shift+Enter)
	ActCECue           ActionID = "ui.ce.cue"           // Trigger: memory cue at cursor (Shift+Space)
	ActCEDelSel        ActionID = "ui.ce.delSel"        // Trigger: delete selected markers (Del)
	ActCEUndo          ActionID = "ui.ce.undo"          // Trigger: one-deep undo/redo (Ctrl+Z)
	// Library collection browsing.
	ActLibNav  ActionID = "ui.lib.nav"  // Step: move the collection selection (↑/↓)
	ActLibOpen ActionID = "ui.lib.open" // Trigger: open the selected track in the cue editor
	// Global navigation.
	ActNavBack ActionID = "ui.nav.back" // Trigger: history back (Alt+←)
	ActNavFwd  ActionID = "ui.nav.fwd"  // Trigger: history forward (Alt+→)
)

// ActionKind is an action's input shape: one-shot, press/release pair, or signed steps.
type ActionKind int

const (
	KindTrigger ActionKind = iota // one-shot on press
	KindHold                      // down on press, up on release (momentary)
	KindStep                      // signed increments (encoder detents / ± buttons)
)

// Action groups (UI sectioning + the group enable filter).
const (
	GroupVR      = "vr"
	GroupCueEdit = "cueedit"
	GroupLibrary = "library"
	GroupNav     = "nav"
)

// TargetKind says what the bind's Target string names (drives the UI picker).
type TargetKind int

const (
	TargetNone     TargetKind = iota // no target
	TargetInstance                   // a rave-mate/OBS instance id (bus-routed)
	TargetOverlay                    // an overlay name
	TargetOBSInput                   // an OBS input/source name
	TargetAppGroup                   // an application-group id
)

// Action describes one bindable action for the registry + UI. Kind zero = Trigger,
// Group zero-value "" = GroupVR (the legacy catalog).
type Action struct {
	ID     ActionID
	Label  string
	Target TargetKind
	Kind   ActionKind
	Group  string
}

// ResolvedGroup returns the action's group ("" = GroupVR, the pre-group catalog).
func (a Action) ResolvedGroup() string {
	if a.Group == "" {
		return GroupVR
	}
	return a.Group
}

// Actions returns the catalog in stable display order.
func Actions() []Action {
	return []Action{
		{ID: ActEditorToggle, Label: "Open / close in-world editor", Target: TargetNone},
		{ID: ActOverlaysToggle, Label: "Show / hide all overlays", Target: TargetNone},
		{ID: ActOverlayToggle, Label: "Toggle a specific overlay", Target: TargetOverlay},
		{ID: ActOverlayShow, Label: "Show a specific overlay", Target: TargetOverlay},
		{ID: ActOverlayHide, Label: "Hide a specific overlay", Target: TargetOverlay},
		{ID: ActOBSRecord, Label: "OBS record start/stop", Target: TargetInstance},
		{ID: ActOBSStream, Label: "OBS stream start/stop", Target: TargetInstance},
		{ID: ActOBSMic, Label: "OBS mic mute toggle", Target: TargetOBSInput},
		{ID: ActSTTRecord, Label: "Speak-to-chat: start/stop dictation", Target: TargetNone},
		{ID: ActSTTSend, Label: "Speak-to-chat: send message", Target: TargetNone},
		{ID: ActSTTDiscard, Label: "Speak-to-chat: discard message", Target: TargetNone},
		{ID: ActSTTClipboard, Label: "Speak-to-chat: copy transcript to clipboard", Target: TargetNone},
		{ID: ActAppGroupLaunch, Label: "Launch an application group", Target: TargetAppGroup},

		{ID: ActCEAudition, Label: "Cue editor: audition (hold = play, release = snap back)", Kind: KindHold, Group: GroupCueEdit},
		{ID: ActCECursor, Label: "Cue editor: move cursor (1 beat)", Kind: KindStep, Group: GroupCueEdit},
		{ID: ActCECursorJump, Label: "Cue editor: move cursor (jump size)", Kind: KindStep, Group: GroupCueEdit},
		{ID: ActCEJumpSize, Label: "Cue editor: adjust jump size", Kind: KindStep, Group: GroupCueEdit},
		{ID: ActCETrack, Label: "Cue editor: previous / next track", Kind: KindStep, Group: GroupCueEdit},
		{ID: ActCEGridNudge, Label: "Cue editor: nudge beatgrid (10 ms)", Kind: KindStep, Group: GroupCueEdit},
		{ID: ActCEGridNudgeFine, Label: "Cue editor: nudge beatgrid (1 ms)", Kind: KindStep, Group: GroupCueEdit},
		{ID: ActCEDropAdd, Label: "Cue editor: add drop at cursor", Group: GroupCueEdit},
		{ID: ActCEDropDel, Label: "Cue editor: remove drop at cursor", Group: GroupCueEdit},
		{ID: ActCECue, Label: "Cue editor: memory cue at cursor", Group: GroupCueEdit},
		{ID: ActCEDelSel, Label: "Cue editor: delete selected markers", Group: GroupCueEdit},
		{ID: ActCEUndo, Label: "Cue editor: undo / redo", Group: GroupCueEdit},
		{ID: ActLibNav, Label: "Library: move track selection", Kind: KindStep, Group: GroupLibrary},
		{ID: ActLibOpen, Label: "Library: open selected track in cue editor", Group: GroupLibrary},
		{ID: ActNavBack, Label: "Navigate back", Group: GroupNav},
		{ID: ActNavFwd, Label: "Navigate forward", Group: GroupNav},
	}
}

// ActionByID returns the catalog entry + ok.
func ActionByID(id ActionID) (Action, bool) {
	for _, a := range Actions() {
		if a.ID == id {
			return a, true
		}
	}
	return Action{}, false
}

// MIDIKey modes: how data2 is interpreted. "" = press (legacy: fire on press edge).
const (
	ModePress  = ""       // fire on press edge (note-on / CC value>0)
	ModeToggle = "toggle" // Hold actions: each press flips down/up
	ModeAbs    = "abs"    // absolute CC knob: value deltas become steps; on Hold, >0=down 0=up
	ModeRel2C  = "rel2c"  // relative encoder, two's complement (1..63=+, 127..65=-)
	ModeRelSM  = "relsm"  // relative encoder, sign-magnitude (bit6=direction)
	ModeRel64  = "rel64"  // relative encoder, offset-64 (65=+1, 63=-1)
)

// MIDIKey matches an incoming MIDI message by status byte (message type + channel) and data1
// (note / CC number). All fields beyond Status/Data1 are additive (zero values = the legacy
// press-edge behavior, so pre-existing configs decode unchanged).
type MIDIKey struct {
	Status byte   `json:"status"`
	Data1  byte   `json:"data1"`
	Port   string `json:"port,omitempty"` // input-port substring match; "" = any port
	Mode   string `json:"mode,omitempty"` // Mode* above; "" = press
	Step   int    `json:"step,omitempty"` // raw ticks per emitted step (encoder/abs sensitivity); <=0 = 1
	Rev    bool   `json:"rev,omitempty"`  // reverse step direction (buttons step -1; encoders negate)
}

// Matches reports whether status+data1 identify this key (exact status - press edges only).
func (k MIDIKey) Matches(status, data1 byte) bool {
	return k.Status == status && k.Data1 == data1
}

// matchesMsg matches the full message family: a Note key (learned on note-on) also matches the
// corresponding note-off (hold-release), same data1 + MIDI channel; CC keys stay exact.
func (k MIDIKey) matchesMsg(status, data1 byte) bool {
	if data1 != k.Data1 || (status&0x0F) != (k.Status&0x0F) {
		return false
	}
	if status == k.Status {
		return true
	}
	kt, mt := k.Status&0xF0, status&0xF0
	return (kt == 0x90 || kt == 0x80) && (mt == 0x90 || mt == 0x80)
}

// matchesPort reports whether the key binds the given input port ("" = any; substring,
// case-insensitive - same convention as the MIDI-learn port match).
func (k MIDIKey) matchesPort(port string) bool {
	if k.Port == "" {
		return true
	}
	return containsFold(port, k.Port)
}

func containsFold(s, sub string) bool {
	return len(sub) <= len(s) && indexFold(s, sub) >= 0
}

func indexFold(s, sub string) int {
	n := len(sub)
	for i := 0; i+n <= len(s); i++ {
		ok := true
		for j := 0; j < n; j++ {
			a, b := s[i+j], sub[j]
			if 'A' <= a && a <= 'Z' {
				a += 'a' - 'A'
			}
			if 'A' <= b && b <= 'Z' {
				b += 'a' - 'A'
			}
			if a != b {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}

// relDelta decodes a relative-encoder CC value per mode; 0 for non-relative modes.
func relDelta(mode string, v byte) int {
	switch mode {
	case ModeRel2C:
		if v == 0 {
			return 0
		}
		if v < 64 {
			return int(v)
		}
		return int(v) - 128
	case ModeRelSM:
		if v&0x40 != 0 {
			return -int(v & 0x3F)
		}
		return int(v & 0x3F)
	case ModeRel64:
		return int(v) - 64
	}
	return 0
}

// isRel reports whether mode is a relative-encoder encoding.
func isRel(mode string) bool { return mode == ModeRel2C || mode == ModeRelSM || mode == ModeRel64 }

// Bind maps one or more input sources to an action+target. At least one of VRAction / MIDI
// should be set; if both are, the action fires on either input.
type Bind struct {
	Action   ActionID `json:"action"`
	Target   string   `json:"target,omitempty"`   // instance id / overlay name / OBS input; "" for TargetNone
	VRAction string   `json:"vrAction,omitempty"` // SteamVR action suffix (e.g. "slot1"); "" = none
	MIDI     *MIDIKey `json:"midi,omitempty"`     // MIDI trigger; nil = none
}

// Handler runs an action for a target ("" when the action takes no target).
type Handler func(target string)

// HoldHandler runs a KindHold action's press (down=true) / release (down=false).
type HoldHandler func(target string, down bool)

// StepHandler runs a KindStep action with a signed step count (never 0; |delta| ≤ stepCap).
type StepHandler func(target string, delta int)

// stepCap bounds steps emitted per MIDI message (an absolute-knob 0→127 jump must not
// explode into 127 dispatches).
const stepCap = 8

// bindState is FireMIDIMsg's per-bind memory: toggle latch, hold latch, last absolute value,
// relative-tick accumulator. Keyed by the bind's identity; entries are tiny and the bind set is
// user-authored (bounded), so the map never grows past the config's bind count × identity churn.
type bindState struct {
	on      bool // toggle/hold latch (down state)
	lastAbs int  // last absolute CC value; -1 = unseen
	acc     int  // relative/abs raw ticks pending toward one emitted step
}

// Dispatcher routes (action,target) to registered handlers. Trigger, hold, and step handlers
// live in separate registries keyed by the action's Kind. Handler maps are written during
// startup registration only; FireMIDIMsg's bind state is mutex-guarded (MIDI + VR event
// goroutines may interleave).
type Dispatcher struct {
	h     map[ActionID]Handler
	hold  map[ActionID]HoldHandler
	step  map[ActionID]StepHandler
	group func(group string) bool // nil = all groups enabled

	stMu sync.Mutex
	st   map[string]*bindState
}

// NewDispatcher builds an empty dispatcher.
func NewDispatcher() *Dispatcher {
	return &Dispatcher{h: map[ActionID]Handler{}, hold: map[ActionID]HoldHandler{},
		step: map[ActionID]StepHandler{}, st: map[string]*bindState{}}
}

// Register sets the trigger handler for an action (replacing any prior one).
func (d *Dispatcher) Register(id ActionID, fn Handler) { d.h[id] = fn }

// RegisterHold sets the press/release handler for a KindHold action.
func (d *Dispatcher) RegisterHold(id ActionID, fn HoldHandler) { d.hold[id] = fn }

// RegisterStep sets the signed-step handler for a KindStep action.
func (d *Dispatcher) RegisterStep(id ActionID, fn StepHandler) { d.step[id] = fn }

// SetGroupFilter installs the group enable gate consulted by FireMIDIMsg (nil = all on).
// Call during startup wiring, before events flow.
func (d *Dispatcher) SetGroupFilter(fn func(group string) bool) { d.group = fn }

// Fire runs the handler for b.Action with b.Target. Returns false if no handler is registered.
// Hold/step actions fired this way (VR slots, quick buttons - press-only sources) degrade
// sensibly: hold flips its latch (toggle), step emits +1.
func (d *Dispatcher) Fire(b Bind) bool {
	if fn := d.h[b.Action]; fn != nil {
		fn(b.Target)
		return true
	}
	if fn := d.hold[b.Action]; fn != nil {
		d.stMu.Lock()
		s := d.state(b, MIDIKey{})
		s.on = !s.on
		on := s.on
		d.stMu.Unlock()
		fn(b.Target, on)
		return true
	}
	if fn := d.step[b.Action]; fn != nil {
		fn(b.Target, 1)
		return true
	}
	return false
}

// FireMIDI fires every bind whose MIDI key matches status+data1 (press edges only - the caller
// pre-filters releases). Kept for press-only callers; FireMIDIMsg is the full-semantics entry.
func (d *Dispatcher) FireMIDI(binds []Bind, status, data1 byte) int {
	n := 0
	for _, b := range binds {
		if b.MIDI != nil && b.MIDI.Matches(status, data1) && d.Fire(b) {
			n++
		}
	}
	return n
}

// state returns (allocating) the per-bind memory. Caller holds stMu.
func (d *Dispatcher) state(b Bind, k MIDIKey) *bindState {
	key := string(b.Action) + "|" + b.Target + "|" + k.Port + "|" + k.Mode + "|" +
		string([]byte{k.Status, k.Data1})
	s := d.st[key]
	if s == nil {
		s = &bindState{lastAbs: -1}
		d.st[key] = s
	}
	return s
}

// FireMIDIMsg dispatches one raw MIDI message (with its source port) through the binds with
// full mode semantics: press/release edges, toggle latching, absolute-knob deltas, and
// relative-encoder decoding with Step sensitivity. Returns the number of handler invocations.
//
// Edge derivation: note-on vel>0 = press; note-off / note-on vel 0 = release; non-relative CC
// value>0 = press, 0 = release.
func (d *Dispatcher) FireMIDIMsg(binds []Bind, port string, status, data1, data2 byte) int {
	n := 0
	for i := range binds {
		b := binds[i]
		k := b.MIDI
		if k == nil || !k.matchesMsg(status, data1) || !k.matchesPort(port) {
			continue
		}
		act, ok := ActionByID(b.Action)
		if !ok {
			continue
		}
		if d.group != nil && !d.group(act.ResolvedGroup()) {
			continue
		}
		if d.fireOne(b, *k, act.Kind, status, data2) {
			n++
		}
	}
	return n
}

// fireOne applies one matched bind. Returns true if a handler ran.
func (d *Dispatcher) fireOne(b Bind, k MIDIKey, kind ActionKind, status, data2 byte) bool {
	isNote := status&0xF0 == 0x90 || status&0xF0 == 0x80
	press := (status&0xF0 == 0x90 && data2 > 0) || (!isNote && !isRel(k.Mode) && data2 > 0)
	release := (isNote && !press) || (!isNote && !isRel(k.Mode) && data2 == 0)

	switch kind {
	case KindHold:
		fn := d.hold[b.Action]
		if fn == nil {
			return false
		}
		if isRel(k.Mode) {
			return false // an encoder can't hold
		}
		if k.Mode == ModeToggle {
			if !press {
				return false
			}
			d.stMu.Lock()
			s := d.state(b, k)
			s.on = !s.on
			on := s.on
			d.stMu.Unlock()
			fn(b.Target, on)
			return true
		}
		// momentary (press/abs): mirror the physical edge, dedup repeats (CC value wiggle
		// while held must not re-fire "down")
		down := press
		d.stMu.Lock()
		s := d.state(b, k)
		changed := s.on != down
		s.on = down
		d.stMu.Unlock()
		if !changed {
			return false
		}
		fn(b.Target, down)
		return true

	case KindStep:
		fn := d.step[b.Action]
		if fn == nil {
			return false
		}
		delta := 0
		switch {
		case isRel(k.Mode):
			delta = relDelta(k.Mode, data2)
		case k.Mode == ModeAbs && !isNote:
			d.stMu.Lock()
			s := d.state(b, k)
			if s.lastAbs < 0 {
				s.lastAbs = int(data2)
				d.stMu.Unlock()
				return false // first touch calibrates
			}
			delta = int(data2) - s.lastAbs
			s.lastAbs = int(data2)
			d.stMu.Unlock()
		default: // button / plain CC: one step per press
			if !press {
				return false
			}
			delta = 1
		}
		if k.Rev {
			delta = -delta
		}
		steps := d.accumulate(b, k, delta)
		if steps == 0 {
			return false
		}
		fn(b.Target, steps)
		return true

	default: // KindTrigger
		fn := d.h[b.Action]
		if fn == nil {
			return false
		}
		if isRel(k.Mode) {
			if relDelta(k.Mode, data2) == 0 {
				return false
			}
		} else if !press {
			_ = release
			return false
		}
		fn(b.Target)
		return true
	}
}

// accumulate folds raw ticks into emitted steps at the bind's Step sensitivity, capped at
// ±stepCap per message. Direction flips clear the remainder (no stale momentum).
func (d *Dispatcher) accumulate(b Bind, k MIDIKey, delta int) int {
	div := k.Step
	if div <= 0 {
		div = 1
	}
	d.stMu.Lock()
	defer d.stMu.Unlock()
	s := d.state(b, k)
	if (s.acc > 0 && delta < 0) || (s.acc < 0 && delta > 0) {
		s.acc = 0
	}
	s.acc += delta
	steps := s.acc / div
	if steps > stepCap {
		steps = stepCap
		s.acc = 0
	} else if steps < -stepCap {
		steps = -stepCap
		s.acc = 0
	} else {
		s.acc -= steps * div
	}
	return steps
}

// FireVR fires every bind whose VRAction equals action (the SteamVR action suffix). Returns
// the number fired. An empty action matches nothing.
func (d *Dispatcher) FireVR(binds []Bind, action string) int {
	if action == "" {
		return 0
	}
	n := 0
	for _, b := range binds {
		if b.VRAction == action && d.Fire(b) {
			n++
		}
	}
	return n
}

// VRActionSlots is the fixed set of generic SteamVR action suffixes users can assign in
// SteamVR's binding UI and then map (in rave-mate) to any app action+target. Fixed so the
// action manifest is stable (SteamVR caches manifests by app key + content).
func VRActionSlots() []string {
	return []string{"slot1", "slot2", "slot3", "slot4", "slot5", "slot6", "slot7", "slot8"}
}

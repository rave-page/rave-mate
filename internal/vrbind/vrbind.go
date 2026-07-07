// Package vrbind is the action-binding core for rave-mate's VR/MIDI hotkeys: a catalog of
// bindable app actions, a user binding list (a SteamVR action and/or a MIDI input → an
// action + target), and a dispatcher that fires the matching handler. Pure Go - no cgo, VR,
// or MIDI deps - so the binding logic is unit-testable; the VR/MIDI/UI layers feed it.
//
// Multiple binds may target the same action (assign several inputs to one action), and a
// single bind may carry both a VR action and a MIDI key (fires on either input).
package vrbind

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

// Action describes one bindable action for the registry + UI.
type Action struct {
	ID     ActionID
	Label  string
	Target TargetKind
}

// Actions returns the catalog in stable display order.
func Actions() []Action {
	return []Action{
		{ActEditorToggle, "Open / close in-world editor", TargetNone},
		{ActOverlaysToggle, "Show / hide all overlays", TargetNone},
		{ActOverlayToggle, "Toggle a specific overlay", TargetOverlay},
		{ActOverlayShow, "Show a specific overlay", TargetOverlay},
		{ActOverlayHide, "Hide a specific overlay", TargetOverlay},
		{ActOBSRecord, "OBS record start/stop", TargetInstance},
		{ActOBSStream, "OBS stream start/stop", TargetInstance},
		{ActOBSMic, "OBS mic mute toggle", TargetOBSInput},
		{ActSTTRecord, "Speak-to-chat: start/stop dictation", TargetNone},
		{ActSTTSend, "Speak-to-chat: send message", TargetNone},
		{ActSTTDiscard, "Speak-to-chat: discard message", TargetNone},
		{ActSTTClipboard, "Speak-to-chat: copy transcript to clipboard", TargetNone},
		{ActAppGroupLaunch, "Launch an application group", TargetAppGroup},
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

// MIDIKey matches an incoming MIDI message by status byte (message type + channel) and data1
// (note / CC number). data2 (velocity / value) is ignored - the caller decides edge semantics
// (e.g. drop note-off / zero-velocity before firing a toggle).
type MIDIKey struct {
	Status byte `json:"status"`
	Data1  byte `json:"data1"`
}

// Matches reports whether status+data1 identify this key.
func (k MIDIKey) Matches(status, data1 byte) bool {
	return k.Status == status && k.Data1 == data1
}

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

// Dispatcher routes (action,target) to registered handlers.
type Dispatcher struct{ h map[ActionID]Handler }

// NewDispatcher builds an empty dispatcher.
func NewDispatcher() *Dispatcher { return &Dispatcher{h: map[ActionID]Handler{}} }

// Register sets the handler for an action (replacing any prior one).
func (d *Dispatcher) Register(id ActionID, fn Handler) { d.h[id] = fn }

// Fire runs the handler for b.Action with b.Target. Returns false if no handler is registered.
func (d *Dispatcher) Fire(b Bind) bool {
	fn := d.h[b.Action]
	if fn == nil {
		return false
	}
	fn(b.Target)
	return true
}

// FireMIDI fires every bind whose MIDI key matches status+data1. Returns the number fired.
func (d *Dispatcher) FireMIDI(binds []Bind, status, data1 byte) int {
	n := 0
	for _, b := range binds {
		if b.MIDI != nil && b.MIDI.Matches(status, data1) && d.Fire(b) {
			n++
		}
	}
	return n
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

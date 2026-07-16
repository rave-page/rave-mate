package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"rave.page/mate/internal/automation"
)

// Studio "Automations" surface (14 methods, parity w/ electron/src/main/ipc/automations.ts).
// The web client (RemoteStudioProxy) speaks the camel-case wire shape in global.d.ts; the Go
// engine uses its own field names - these mappers bridge the two. Run events flow over the
// long-lived automations.subscribe stream (see subscribeAutomations).
//
// Action-chain validation is NOT duplicated here: the mappers convert to []automation.Action and
// defer to automation.ValidateActions + ValidateLoudness, so the wire and the engine can never
// drift on what a runnable chain is (e.g. delete-is-terminal), and neither can the wire and the
// webui editor - which calls the same two. Only wire-shape checks the engine can't make - the
// filenamePattern regex, required label/watchDirectory - stay in this layer.

// ── wire shapes (byte-exact with the web Automation/AutomationAction contract) ─
//
// ABSENT ≠ ZERO, for every field added after the deployed web client's typed Automation model was
// frozen (match.minAgeDays + the action loudness quartet). Those are pointer-typed so an update
// patch can tell "the client cleared it" from "the client has never heard of it": the deployed app
// drops keys its model lacks when it decodes a GET, and a rename/toggle PATCHes the whole object
// back - a plain int/bool would decode absent→zero and silently ERASE the field. For minAgeDays
// that turns "delete raw sets older than 30 days" into "delete every matching set, nightly", armed
// by an unrelated rename from another surface. updateAutomation preserves the current value on an
// absent key; see mergeWireMatch / mergeWireActions.
//
// RULE FOR NEW FIELDS: anything added here from now on is pointer-typed, for the same reason.
// Fields the deployed client DOES know (extensions, minSizeBytes, filenamePattern, trimStart/End)
// stay plain - it can express clearing them, and absent-preserves would break that.
//
// Wire bytes are unchanged: toWire maps a zero value back to nil, so `omitempty` omits exactly the
// keys it omitted before, and payloads written by any client still decode to the same behavior.

type wireAction struct {
	Type              string  `json:"type"`
	PresetID          string  `json:"presetId,omitempty"`
	OutputDirectory   string  `json:"outputDirectory,omitempty"`
	ThresholdDb       float64 `json:"thresholdDb,omitempty"`
	MinSilenceSeconds float64 `json:"minSilenceSeconds,omitempty"`
	TrimStart         *bool   `json:"trimStart,omitempty"`
	TrimEnd           *bool   `json:"trimEnd,omitempty"`
	BufferMinutes     int     `json:"bufferMinutes,omitempty"`
	Template          string  `json:"template,omitempty"`
	// transcode loudness override. loudnessOn=true replaces the resolved preset's loudness block
	// wholesale; false/absent leaves the preset's own loudness untouched, so payloads written
	// before these fields existed decode to the pre-existing behavior. Optional-typed: see above.
	LoudnessOn        *bool    `json:"loudnessOn,omitempty"`
	LoudnessI         *float64 `json:"loudnessI,omitempty"`
	LoudnessTP        *float64 `json:"loudnessTP,omitempty"`
	LoudnessRaiseOnly *bool    `json:"loudnessRaiseOnly,omitempty"`
}

type wireMatch struct {
	Extensions      []string `json:"extensions,omitempty"`
	MinSizeBytes    int64    `json:"minSizeBytes,omitempty"`
	FilenamePattern string   `json:"filenamePattern,omitempty"`
	// mtime gate for scheduled sweeps; 0 = off (pre-existing behavior). Optional-typed: absent on
	// an update patch means "preserve", NOT "off" - it gates deletes (see the wire-shape note).
	MinAgeDays *int `json:"minAgeDays,omitempty"`
}

type wireAutomation struct {
	ID             string       `json:"id"`
	Label          string       `json:"label"`
	WatchDirectory string       `json:"watchDirectory"`
	Enabled        bool         `json:"enabled"`
	Match          wireMatch    `json:"match"`
	Actions        []wireAction `json:"actions"`
	CreatedAt      string       `json:"createdAt"`
	LastRunAt      string       `json:"lastRunAt,omitempty"`
	LastRunStatus  string       `json:"lastRunStatus,omitempty"`
	LastRunError   string       `json:"lastRunError,omitempty"`
}

// ── mappers ──────────────────────────────────────────────────────────────────

// optInt/optFloat/optBool render a zero value as an ABSENT wire key - byte-identical to the
// `omitempty` output these fields produced before they became pointer-typed.
func optInt(v int) *int {
	if v == 0 {
		return nil
	}
	return &v
}

func optFloat(v float64) *float64 {
	if v == 0 {
		return nil
	}
	return &v
}

func optBool(v bool) *bool {
	if !v {
		return nil
	}
	return &v
}

// orIntPtr/orFloatPtr/orBoolPtr resolve an optional patch field: absent (nil) → keep cur.
func orIntPtr(p *int, cur int) int {
	if p == nil {
		return cur
	}
	return *p
}

func orFloatPtr(p *float64, cur float64) float64 {
	if p == nil {
		return cur
	}
	return *p
}

func orBoolPtr(p *bool, cur bool) bool {
	if p == nil {
		return cur
	}
	return *p
}

func toWire(a automation.Automation) wireAutomation {
	acts := make([]wireAction, 0, len(a.Actions))
	for _, ac := range a.Actions {
		acts = append(acts, wireAction{
			Type:              string(ac.Type),
			PresetID:          ac.PresetID,
			OutputDirectory:   ac.OutputDir,
			ThresholdDb:       ac.ThresholdDb,
			MinSilenceSeconds: ac.MinSilenceSeconds,
			TrimStart:         ac.TrimStart,
			TrimEnd:           ac.TrimEnd,
			BufferMinutes:     ac.BufferMinutes,
			Template:          ac.Template,
			LoudnessOn:        optBool(ac.LoudnessOn),
			LoudnessI:         optFloat(ac.LoudnessI),
			LoudnessTP:        optFloat(ac.LoudnessTP),
			LoudnessRaiseOnly: optBool(ac.LoudnessRaiseOnly),
		})
	}
	return wireAutomation{
		ID: a.ID, Label: a.Label, WatchDirectory: a.WatchDir, Enabled: a.Enabled,
		Match: wireMatch{
			Extensions: a.Match.Extensions, MinSizeBytes: a.Match.MinSizeBytes,
			FilenamePattern: a.Match.FilenamePattern, MinAgeDays: optInt(a.Match.MinAgeDays),
		},
		Actions: acts, CreatedAt: a.CreatedAt,
		LastRunAt: a.LastRunAt, LastRunStatus: a.LastStatus, LastRunError: a.LastError,
	}
}

func toWireList(as []automation.Automation) []wireAutomation {
	out := make([]wireAutomation, 0, len(as))
	for _, a := range as {
		out = append(out, toWire(a))
	}
	return out
}

// wireActionsToGo maps a CREATE payload: there is no current chain, so an absent optional key is
// the zero value (identical to the pre-existing mapping).
func wireActionsToGo(ws []wireAction) []automation.Action { return mergeWireActions(nil, ws) }

// wireMatchToGo maps a CREATE payload (no current match ⇒ absent = zero).
func wireMatchToGo(w wireMatch) automation.Match { return mergeWireMatch(automation.Match{}, w) }

// mergeWireMatch maps an update patch's match onto the current one. Fields the deployed client
// knows are replaced wholesale (it can express clearing them); minAgeDays is PRESERVED when absent,
// so a client whose model predates it cannot silently disarm the age gate on a scheduled delete
// chain by round-tripping the object. A client that wants the gate off sends minAgeDays: 0.
func mergeWireMatch(cur automation.Match, w wireMatch) automation.Match {
	exts := make([]string, 0, len(w.Extensions))
	for _, e := range w.Extensions {
		exts = append(exts, strings.ToLower(e))
	}
	return automation.Match{
		Extensions: exts, MinSizeBytes: w.MinSizeBytes, FilenamePattern: w.FilenamePattern,
		MinAgeDays: orIntPtr(w.MinAgeDays, cur.MinAgeDays),
	}
}

// mergeWireActions maps an update patch's action list, restoring loudness values the client omitted
// from the CURRENT chain at the same index - but ONLY when sameLegacyShape proves the client did not
// restructure the chain. Actions carry no stable id, so index correlation is a guess in general;
// gating it on "positionally identical everywhere the client can see" is what makes it exact.
// A client that DID edit the chain is authoritative over it as-is: an old client's edit therefore
// drops the loudness override (a visible, recoverable loss - not a silent one, and not the erase
// the absent-vs-zero rule exists to stop).
func mergeWireActions(cur []automation.Action, ws []wireAction) []automation.Action {
	merge := sameLegacyShape(cur, ws)
	out := make([]automation.Action, 0, len(ws))
	for i, w := range ws {
		var c automation.Action // create / restructured chain: absent → zero
		if merge {
			c = cur[i]
		}
		out = append(out, automation.Action{
			Type:              automation.ActionType(w.Type),
			PresetID:          w.PresetID,
			OutputDir:         w.OutputDirectory,
			ThresholdDb:       w.ThresholdDb,
			MinSilenceSeconds: w.MinSilenceSeconds,
			TrimStart:         w.TrimStart,
			TrimEnd:           w.TrimEnd,
			BufferMinutes:     w.BufferMinutes,
			Template:          w.Template,
			LoudnessOn:        orBoolPtr(w.LoudnessOn, c.LoudnessOn),
			LoudnessI:         orFloatPtr(w.LoudnessI, c.LoudnessI),
			LoudnessTP:        orFloatPtr(w.LoudnessTP, c.LoudnessTP),
			LoudnessRaiseOnly: orBoolPtr(w.LoudnessRaiseOnly, c.LoudnessRaiseOnly),
		})
	}
	return out
}

// sameLegacyShape reports whether a patch's actions are positionally identical to cur on every
// field a client whose model predates the loudness keys can express. True ⇒ the client changed
// nothing it can see - the dangerous case, a label edit that PATCHes the whole object back - so the
// indices line up exactly and the omitted loudness values are safe to carry forward.
//
// Residual, stated rather than hidden: two actions identical in every legacy field but differing in
// loudness, swapped by such a client, read as unchanged, so the loudness stays at its original
// index. The intent is unknowable (that client renders the two rows identically) and preserving is
// the fail-safe read.
func sameLegacyShape(cur []automation.Action, ws []wireAction) bool {
	if len(cur) != len(ws) {
		return false
	}
	for i, w := range ws {
		c := cur[i]
		if automation.ActionType(w.Type) != c.Type || w.PresetID != c.PresetID ||
			w.OutputDirectory != c.OutputDir || w.ThresholdDb != c.ThresholdDb ||
			w.MinSilenceSeconds != c.MinSilenceSeconds || w.BufferMinutes != c.BufferMinutes ||
			w.Template != c.Template ||
			!eqBoolPtr(w.TrimStart, c.TrimStart) || !eqBoolPtr(w.TrimEnd, c.TrimEnd) {
			return false
		}
	}
	return true
}

func eqBoolPtr(a, b *bool) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func validateRegex(p string) error {
	if p == "" {
		return nil
	}
	if _, err := regexp.Compile(p); err != nil {
		return fmt.Errorf("invalid filenamePattern regex: %w", err)
	}
	return nil
}

// remarshal re-encodes a wire sub-object (map) into the typed target.
func remarshal(v any, target any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, target)
}

// ── dispatch ─────────────────────────────────────────────────────────────────

func (s *session) automationsCall(method, reqID string, p map[string]any) {
	defer func() {
		if r := recover(); r != nil {
			s.srv.log.Warn(source, "automations panic", map[string]any{"method": method, "panic": fmt.Sprint(r)})
			s.sendErr(reqID, errInternal, "internal error")
		}
	}()
	autos := s.srv.autos
	if autos == nil {
		s.sendErr(reqID, errInternal, "automations unavailable")
		return
	}
	switch method {
	case "automations.list":
		s.reply(reqID, toWireList(autos.List()), "", nil)
	case "automations.create":
		s.createAutomation(reqID, autos, p["input"])
	case "automations.update":
		s.updateAutomation(reqID, autos, asString(p["id"]), p["patch"])
	case "automations.delete":
		if err := autos.Delete(asString(p["id"])); err != nil {
			s.sendErr(reqID, errInternal, err.Error())
			return
		}
		s.reply(reqID, toWireList(autos.List()), "", nil)
	case "automations.setEnabled":
		s.setEnabled(reqID, autos, asString(p["id"]), asBool(p["enabled"]))
	case "automations.setBackgroundCredentials":
		// Web only sends apiBaseUrl; the desktop injects its OWN access token.
		base := asString(p["apiBaseUrl"])
		token := ""
		if base != "" {
			token = s.srv.desktopToken()
		}
		autos.SetBackgroundCredentials(base, token)
		s.reply(reqID, map[string]any{"ok": true}, "", nil)
	case "automations.runOnce":
		s.startRun(reqID, autos, automation.ModeOnce, p)
	case "automations.runManual":
		s.startRun(reqID, autos, automation.ModeManual, p)
	case "automations.commitStep":
		s.replyOK(reqID, autos.CommitStep(asString(p["runId"])))
	case "automations.skipStep":
		s.replyOK(reqID, autos.SkipStep(asString(p["runId"])))
	case "automations.abortRun":
		s.replyOK(reqID, autos.AbortRun(asString(p["runId"])))
	case "automations.probeSilence":
		ctx, cancel := s.autoCtx()
		defer cancel()
		thr, _ := asFloat(p["thresholdDb"])
		minSil, _ := asFloat(p["minSilenceSeconds"])
		res, err := autos.ProbeSilence(ctx, asString(p["path"]), thr, minSil)
		s.reply(reqID, res, errFFmpeg, err)
	case "automations.listEvents":
		ctx, cancel := s.autoCtx()
		defer cancel()
		buf, _ := asInt(p["bufferMinutes"])
		res, err := autos.ListEvents(ctx, asString(p["mtimeIso"]), buf)
		s.reply(reqID, res, errInternal, err)
	default:
		s.sendErr(reqID, errUnknownMethod, "unknown "+method)
	}
}

func (s *session) createAutomation(reqID string, autos automation.Manager, input any) {
	var in struct {
		Label          string       `json:"label"`
		WatchDirectory string       `json:"watchDirectory"`
		Enabled        bool         `json:"enabled"`
		Match          wireMatch    `json:"match"`
		Actions        []wireAction `json:"actions"`
	}
	if err := remarshal(input, &in); err != nil {
		s.sendErr(reqID, errBadRequest, "invalid input")
		return
	}
	if strings.TrimSpace(in.Label) == "" {
		s.sendErr(reqID, errBadRequest, "automation label is required")
		return
	}
	if strings.TrimSpace(in.WatchDirectory) == "" {
		s.sendErr(reqID, errBadRequest, "watch directory is required")
		return
	}
	acts := wireActionsToGo(in.Actions)
	if err := automation.ValidateActions(acts); err != nil {
		s.sendErr(reqID, errBadRequest, err.Error())
		return
	}
	// The wire has no channel to WARN over (the deployed client drops response keys its model
	// lacks - the same trap the optional-typed fields exist for), so a setting that would be
	// stored and then ignored on every run is refused outright rather than accepted in silence.
	// The webui editor refuses the identical chain via this same call, so neither surface can
	// author an automation the other cannot edit.
	if err := automation.ValidateLoudness(acts, s.srv.presets); err != nil {
		s.sendErr(reqID, errBadRequest, err.Error())
		return
	}
	if err := validateRegex(in.Match.FilenamePattern); err != nil {
		s.sendErr(reqID, errBadRequest, err.Error())
		return
	}
	saved, err := autos.Save(automation.Automation{
		Label: strings.TrimSpace(in.Label), WatchDir: in.WatchDirectory, Enabled: in.Enabled,
		Match: wireMatchToGo(in.Match), Actions: acts,
	})
	if err != nil {
		s.sendErr(reqID, errInternal, err.Error())
		return
	}
	s.reply(reqID, toWire(saved), "", nil)
}

// updateAutomation applies a patch onto the stored automation. An omitted top-level key leaves that
// part untouched; a PRESENT match/actions is replaced - except for the optional-typed fields, which
// merge off cur so a client that predates them cannot erase them (see the wire-shape note).
func (s *session) updateAutomation(reqID string, autos automation.Manager, id string, patch any) {
	cur, ok := autos.Get(id)
	if !ok {
		s.sendErr(reqID, errBadRequest, "automation not found")
		return
	}
	pm, _ := patch.(map[string]any)
	if v, ok := pm["label"]; ok {
		if lbl := strings.TrimSpace(asString(v)); lbl != "" {
			cur.Label = lbl
		}
	}
	if v, ok := pm["watchDirectory"]; ok {
		cur.WatchDir = asString(v)
	}
	if v, ok := pm["enabled"]; ok {
		cur.Enabled = asBool(v)
	}
	if v, ok := pm["match"]; ok {
		var wm wireMatch
		if err := remarshal(v, &wm); err != nil {
			s.sendErr(reqID, errBadRequest, "invalid match")
			return
		}
		if err := validateRegex(wm.FilenamePattern); err != nil {
			s.sendErr(reqID, errBadRequest, err.Error())
			return
		}
		cur.Match = mergeWireMatch(cur.Match, wm)
	}
	if v, ok := pm["actions"]; ok {
		var wa []wireAction
		if err := remarshal(v, &wa); err != nil {
			s.sendErr(reqID, errBadRequest, "invalid actions")
			return
		}
		acts := mergeWireActions(cur.Actions, wa)
		if err := automation.ValidateActions(acts); err != nil {
			s.sendErr(reqID, errBadRequest, err.Error())
			return
		}
		// Scoped to a patch that CARRIES actions, like the rule above it: a rename/toggle patch
		// leaves the chain untouched and must keep working against data saved before this check.
		if err := automation.ValidateLoudness(acts, s.srv.presets); err != nil {
			s.sendErr(reqID, errBadRequest, err.Error())
			return
		}
		cur.Actions = acts
	}
	saved, err := autos.Save(cur)
	if err != nil {
		s.sendErr(reqID, errInternal, err.Error())
		return
	}
	s.reply(reqID, toWire(saved), "", nil)
}

func (s *session) setEnabled(reqID string, autos automation.Manager, id string, enabled bool) {
	cur, ok := autos.Get(id)
	if !ok {
		s.sendErr(reqID, errBadRequest, "automation not found")
		return
	}
	cur.Enabled = enabled
	saved, err := autos.Save(cur)
	if err != nil {
		s.sendErr(reqID, errInternal, err.Error())
		return
	}
	s.reply(reqID, toWire(saved), "", nil)
}

func (s *session) startRun(reqID string, autos automation.Manager, mode automation.RunMode, p map[string]any) {
	// Inject the desktop's creds so rename-from-event can hit the events API.
	if base := asString(p["apiBaseUrl"]); base != "" {
		autos.SetBackgroundCredentials(base, s.srv.desktopToken())
	}
	runID, err := autos.StartRun(mode, asString(p["id"]), asString(p["filePath"]))
	if err != nil {
		s.sendErr(reqID, errBadRequest, err.Error())
		return
	}
	s.reply(reqID, map[string]any{"runId": runID}, "", nil)
}

// subscribeAutomations registers a long-lived run-event stream (no terminal res). The web
// routes notify frames by `for: reqID`; the sub releases on cancel/teardown.
func (s *session) subscribeAutomations(reqID string) {
	if s.srv.autos == nil {
		s.sendErr(reqID, errInternal, "automations unavailable")
		return
	}
	unsub := s.srv.autos.OnEvent(func(ev automation.RunEvent) {
		s.notifyStream(reqID, "automation-event", ev)
	})
	s.registerSub(reqID, &subRec{unsub: unsub})
}

func (s *session) replyOK(reqID string, err error) {
	if err != nil {
		s.sendErr(reqID, errBadRequest, err.Error())
		return
	}
	s.send(map[string]any{"t": "res", "id": reqID, "ok": true, "result": map[string]any{"ok": true}})
}

func (s *session) autoCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 60*time.Second)
}

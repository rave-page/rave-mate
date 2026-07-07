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

// ── wire shapes (byte-exact with the web Automation/AutomationAction contract) ─

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
}

type wireMatch struct {
	Extensions      []string `json:"extensions,omitempty"`
	MinSizeBytes    int64    `json:"minSizeBytes,omitempty"`
	FilenamePattern string   `json:"filenamePattern,omitempty"`
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
		})
	}
	return wireAutomation{
		ID: a.ID, Label: a.Label, WatchDirectory: a.WatchDir, Enabled: a.Enabled,
		Match:   wireMatch{Extensions: a.Match.Extensions, MinSizeBytes: a.Match.MinSizeBytes, FilenamePattern: a.Match.FilenamePattern},
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

func wireActionsToGo(ws []wireAction) []automation.Action {
	out := make([]automation.Action, 0, len(ws))
	for _, w := range ws {
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
		})
	}
	return out
}

func wireMatchToGo(w wireMatch) automation.Match {
	exts := make([]string, 0, len(w.Extensions))
	for _, e := range w.Extensions {
		exts = append(exts, strings.ToLower(e))
	}
	return automation.Match{Extensions: exts, MinSizeBytes: w.MinSizeBytes, FilenamePattern: w.FilenamePattern}
}

// validateActions mirrors the Electron validation (label/dir checked by the caller).
func validateActions(acts []wireAction) error {
	if len(acts) == 0 {
		return fmt.Errorf("automation must have at least one action")
	}
	for _, a := range acts {
		switch automation.ActionType(a.Type) {
		case automation.ActionRename, automation.ActionTrimSilence:
		case automation.ActionTranscode:
			if a.PresetID == "" {
				return fmt.Errorf("transcode action requires presetId")
			}
		case automation.ActionMove, automation.ActionCopy:
			if a.OutputDirectory == "" {
				return fmt.Errorf("move-to action requires outputDirectory")
			}
		default:
			return fmt.Errorf("unknown automation action: %s", a.Type)
		}
	}
	return nil
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
	if err := validateActions(in.Actions); err != nil {
		s.sendErr(reqID, errBadRequest, err.Error())
		return
	}
	if err := validateRegex(in.Match.FilenamePattern); err != nil {
		s.sendErr(reqID, errBadRequest, err.Error())
		return
	}
	saved, err := autos.Save(automation.Automation{
		Label: strings.TrimSpace(in.Label), WatchDir: in.WatchDirectory, Enabled: in.Enabled,
		Match: wireMatchToGo(in.Match), Actions: wireActionsToGo(in.Actions),
	})
	if err != nil {
		s.sendErr(reqID, errInternal, err.Error())
		return
	}
	s.reply(reqID, toWire(saved), "", nil)
}

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
		cur.Match = wireMatchToGo(wm)
	}
	if v, ok := pm["actions"]; ok {
		var wa []wireAction
		if err := remarshal(v, &wa); err != nil {
			s.sendErr(reqID, errBadRequest, "invalid actions")
			return
		}
		if err := validateActions(wa); err != nil {
			s.sendErr(reqID, errBadRequest, err.Error())
			return
		}
		cur.Actions = wireActionsToGo(wa)
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

package studio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"rave.page/mate/internal/obs"
	"rave.page/mate/internal/obscontrol"
)

// ObsGateway surfaces the desktop's LOCAL OBS to the web client (obs.* +
// quickAction.* RPCs): status, profile/scene-collection lists, settings reads,
// preset capture/apply, stream/record control. nil ⇒ unavailable. obs.* methods
// are advertised per-connection only when Enabled() AND Connected();
// quickAction.streamReady needs only Enabled() (its launch step may bring OBS
// up). Implemented in the app package over the featurehost proxy + obscontrol.
type ObsGateway interface {
	Enabled() bool   // config feature flag
	Connected() bool // live obs-websocket session (child mirror)
	Statuses() []obscontrol.Instance
	ListProfiles(ctx context.Context) (current string, profiles []string, err error)
	ListSceneCollections(ctx context.Context) (current string, collections []string, err error)
	GetSettings(ctx context.Context) (obs.StreamServiceSettings, obs.VideoSettings, error)
	CapturePreset(ctx context.Context) (obs.Preset, error)
	ApplyPreset(ctx context.Context, p obs.Preset) error
	StartStream(ctx context.Context) error
	StopStream(ctx context.Context) error
	StartRecord(ctx context.Context) error
	StopRecord(ctx context.Context) error
}

// AppGroupInfo is one configured app group + its live run state (appgroup.list).
type AppGroupInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Apps    int    `json:"apps"`
	Running int    `json:"running"`
}

// AppGroupGateway surfaces configured app groups (appgroup.* RPCs). nil ⇒
// unavailable; advertised per-connection only when Configured() (feature on +
// ≥1 group). Implemented in the app package over appgroups.Service.
type AppGroupGateway interface {
	Configured() bool
	List() []AppGroupInfo
	Launch(id string) (started, skipped []string, err error)
	Readiness(id string) (running, total int, err error)
}

// Like peers.*/vrchat.*, these are handled locally and never forwarded to a
// remote context. Advertised per-connection - see Server.capabilities.
var (
	obsMethods = []string{
		"obs.status",
		"obs.listProfiles",
		"obs.listSceneCollections",
		"obs.getSettings",
		"obs.capturePreset",
		"obs.applyPreset",
		"obs.startStream",
		"obs.stopStream",
		"obs.startRecord",
		"obs.stopRecord",
	}
	appgroupMethods = []string{
		"appgroup.list",
		"appgroup.launch",
		"appgroup.readiness",
	}
	quickActionMethods = []string{
		"quickAction.streamReady",
	}
)

func isObsFamilyMethod(m string) bool {
	switch m {
	case "obs.status", "obs.listProfiles", "obs.listSceneCollections", "obs.getSettings",
		"obs.capturePreset", "obs.applyPreset", "obs.startStream", "obs.stopStream",
		"obs.startRecord", "obs.stopRecord",
		"appgroup.list", "appgroup.launch", "appgroup.readiness",
		"quickAction.streamReady":
		return true
	}
	return false
}

// obsMutating gates the audit log (mutations only; reads stay quiet).
func obsMutating(m string) bool {
	switch m {
	case "obs.applyPreset", "obs.startStream", "obs.stopStream", "obs.startRecord",
		"obs.stopRecord", "appgroup.launch", "quickAction.streamReady":
		return true
	}
	return false
}

// ── result shapes ─────────────────────────────────────────────────────────────

// obsNameList is obs.listProfiles / obs.listSceneCollections.
type obsNameList struct {
	Current string   `json:"current"`
	Names   []string `json:"names"`
}

// obsSettingsOut is obs.getSettings: the current stream-service + video settings.
type obsSettingsOut struct {
	StreamService obs.StreamServiceSettings `json:"streamService"`
	Video         obs.VideoSettings         `json:"video"`
}

// obsAck is the generic mutation acknowledgement.
type obsAck struct {
	OK bool `json:"ok"`
}

// appGroupLaunchOut is appgroup.launch: apps started vs already running.
type appGroupLaunchOut struct {
	Started []string `json:"started"`
	Skipped []string `json:"skipped"`
}

// appGroupReadinessOut is appgroup.readiness.
type appGroupReadinessOut struct {
	Running int  `json:"running"`
	Total   int  `json:"total"`
	Ready   bool `json:"ready"`
}

// streamReadyStep is one composite step's outcome (quickAction.streamReady).
type streamReadyStep struct {
	Step   string         `json:"step"` // launch | readiness | applyPreset | startRecord | startStream
	OK     bool           `json:"ok"`
	Error  string         `json:"error,omitempty"`
	Detail map[string]any `json:"detail,omitempty"`
}

// streamReadyOut is the composite result: per-step outcomes, fail-fast.
type streamReadyOut struct {
	OK         bool              `json:"ok"`
	FailedStep string            `json:"failedStep,omitempty"`
	Steps      []streamReadyStep `json:"steps"`
}

// streamReady bounds: readiness poll + per-call ceiling.
const (
	streamReadyPollEvery = time.Second
	streamReadyPollMax   = 60 * time.Second
	streamReadyBudget    = 90 * time.Second
)

// obsCall runs one obs.*/appgroup.*/quickAction.* method off the read loop and
// replies. Local desktop only - never forwarded to a remote context. Mutations
// are audit-logged to the logbus.
func (s *session) obsCall(method, id string, p map[string]any) {
	timeout := 20 * time.Second
	if method == "quickAction.streamReady" {
		timeout = streamReadyBudget
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	result, code, err := dispatchObs(ctx, s.srv.obsGw, s.srv.appGrp, method, p)
	if obsMutating(method) {
		fields := map[string]any{"method": method, "sub": s.sub}
		if err != nil {
			fields["error"] = err.Error()
			s.srv.log.Warn(source, "obs call failed", fields)
		} else {
			s.srv.log.Info(source, "obs call", fields)
		}
	}
	if err != nil {
		s.sendErr(id, code, err.Error())
		return
	}
	s.send(map[string]any{"t": "res", "id": id, "ok": true, "result": result})
}

// dispatchObs maps a method to the gateways. Returns (result, errCode, err).
func dispatchObs(ctx context.Context, gw ObsGateway, ag AppGroupGateway, method string, p map[string]any) (any, errorCode, error) {
	switch method {
	case "appgroup.list", "appgroup.launch", "appgroup.readiness":
		if ag == nil || !ag.Configured() {
			return nil, errUnknownMethod, errors.New("app groups unavailable")
		}
		return dispatchAppGroup(ag, method, p)
	}
	if gw == nil || !gw.Enabled() {
		return nil, errUnknownMethod, errors.New("obs unavailable")
	}
	switch method {
	case "obs.status":
		return gw.Statuses(), "", nil
	case "obs.listProfiles":
		cur, names, err := gw.ListProfiles(ctx)
		if err != nil {
			return nil, errInternal, err
		}
		return obsNameList{Current: cur, Names: names}, "", nil
	case "obs.listSceneCollections":
		cur, names, err := gw.ListSceneCollections(ctx)
		if err != nil {
			return nil, errInternal, err
		}
		return obsNameList{Current: cur, Names: names}, "", nil
	case "obs.getSettings":
		ss, vs, err := gw.GetSettings(ctx)
		if err != nil {
			return nil, errInternal, err
		}
		return obsSettingsOut{StreamService: ss, Video: vs}, "", nil
	case "obs.capturePreset":
		preset, err := gw.CapturePreset(ctx)
		if err != nil {
			return nil, errInternal, err
		}
		return preset, "", nil
	case "obs.applyPreset":
		preset, err := presetOf(p["preset"])
		if err != nil {
			return nil, errBadRequest, err
		}
		if err := gw.ApplyPreset(ctx, preset); err != nil {
			return nil, errInternal, err
		}
		return obsAck{OK: true}, "", nil
	case "obs.startStream":
		return obsCtl(gw.StartStream(ctx))
	case "obs.stopStream":
		return obsCtl(gw.StopStream(ctx))
	case "obs.startRecord":
		return obsCtl(gw.StartRecord(ctx))
	case "obs.stopRecord":
		return obsCtl(gw.StopRecord(ctx))
	case "quickAction.streamReady":
		return streamReady(ctx, gw, ag, p), "", nil
	}
	return nil, errUnknownMethod, errors.New("unknown obs method " + method)
}

func dispatchAppGroup(ag AppGroupGateway, method string, p map[string]any) (any, errorCode, error) {
	switch method {
	case "appgroup.list":
		return ag.List(), "", nil
	case "appgroup.launch":
		id := asString(p["id"])
		if id == "" {
			return nil, errBadRequest, errors.New("appgroup.launch: id required")
		}
		started, skipped, err := ag.Launch(id)
		if err != nil {
			return nil, errInternal, err
		}
		// nil-safe: JSON [] not null.
		return appGroupLaunchOut{Started: orEmpty(started), Skipped: orEmpty(skipped)}, "", nil
	case "appgroup.readiness":
		id := asString(p["id"])
		if id == "" {
			return nil, errBadRequest, errors.New("appgroup.readiness: id required")
		}
		running, total, err := ag.Readiness(id)
		if err != nil {
			return nil, errInternal, err
		}
		return appGroupReadinessOut{Running: running, Total: total, Ready: running == total}, "", nil
	}
	return nil, errUnknownMethod, errors.New("unknown appgroup method " + method)
}

// streamReady is the one-tap composite: launch app group → poll readiness (group
// fully running + OBS connected, bounded) → apply preset → start record/stream.
// Fail-fast: the first failed step ends the run; every attempted step is reported.
func streamReady(ctx context.Context, gw ObsGateway, ag AppGroupGateway, p map[string]any) streamReadyOut {
	out := streamReadyOut{OK: true}
	fail := func(step string, err error, detail map[string]any) streamReadyOut {
		out.OK = false
		out.FailedStep = step
		out.Steps = append(out.Steps, streamReadyStep{Step: step, Error: err.Error(), Detail: detail})
		return out
	}
	pass := func(step string, detail map[string]any) {
		out.Steps = append(out.Steps, streamReadyStep{Step: step, OK: true, Detail: detail})
	}

	groupID := asString(p["groupId"])
	if groupID != "" {
		if ag == nil || !ag.Configured() {
			return fail("launch", errors.New("app groups unavailable"), nil)
		}
		started, skipped, err := ag.Launch(groupID)
		if err != nil {
			return fail("launch", err, nil)
		}
		pass("launch", map[string]any{"started": orEmpty(started), "skipped": orEmpty(skipped)})
	}

	if err := pollStreamReady(ctx, gw, ag, groupID, func(d map[string]any) { pass("readiness", d) }); err != nil {
		return fail("readiness", err, readinessDetail(gw, ag, groupID))
	}

	if rawPreset, ok := p["preset"]; ok && rawPreset != nil {
		preset, err := presetOf(rawPreset)
		if err != nil {
			return fail("applyPreset", err, nil)
		}
		if err := gw.ApplyPreset(ctx, preset); err != nil {
			return fail("applyPreset", err, nil)
		}
		pass("applyPreset", nil)
	}

	if asBool(p["startRecord"]) {
		if err := gw.StartRecord(ctx); err != nil {
			return fail("startRecord", err, nil)
		}
		pass("startRecord", nil)
	}
	if asBool(p["startStream"]) {
		if err := gw.StartStream(ctx); err != nil {
			return fail("startStream", err, nil)
		}
		pass("startStream", nil)
	}
	return out
}

// pollStreamReady waits (bounded) until OBS is connected and - when a group was
// launched - every app in it is running. Reports the passed step via ok.
func pollStreamReady(ctx context.Context, gw ObsGateway, ag AppGroupGateway, groupID string, ok func(map[string]any)) error {
	deadline := time.Now().Add(streamReadyPollMax)
	for {
		ready := gw.Connected()
		if ready && groupID != "" {
			running, total, err := ag.Readiness(groupID)
			ready = err == nil && running == total
		}
		if ready {
			ok(readinessDetail(gw, ag, groupID))
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("readiness timeout after %s", streamReadyPollMax)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(streamReadyPollEvery):
		}
	}
}

// readinessDetail snapshots the readiness state for step reporting.
func readinessDetail(gw ObsGateway, ag AppGroupGateway, groupID string) map[string]any {
	d := map[string]any{"obsConnected": gw.Connected()}
	if groupID != "" && ag != nil {
		if running, total, err := ag.Readiness(groupID); err == nil {
			d["running"], d["total"] = running, total
		}
	}
	return d
}

// ── helpers ──────────────────────────────────────────────────────────────────

// obsCtl wraps a control call into the dispatch triple.
func obsCtl(err error) (any, errorCode, error) {
	if err != nil {
		return nil, errInternal, err
	}
	return obsAck{OK: true}, "", nil
}

// presetOf coerces a wire params value (map decoded with UseNumber) into an
// obs.Preset via re-marshal - the JSON boundary IS the contract.
func presetOf(v any) (obs.Preset, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return obs.Preset{}, fmt.Errorf("preset encode: %w", err)
	}
	var p obs.Preset
	if err := json.Unmarshal(raw, &p); err != nil {
		return obs.Preset{}, fmt.Errorf("preset decode: %w", err)
	}
	if p.IsEmpty() {
		return obs.Preset{}, errors.New("empty preset")
	}
	return p, nil
}

// orEmpty maps nil to an empty slice so results marshal as [] not null.
func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

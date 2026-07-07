package studio

import (
	"context"
	"errors"
	"testing"

	"rave.page/mate/internal/obs"
	"rave.page/mate/internal/obscontrol"
)

// fakeObsGw scripts studio.ObsGateway for dispatch tests.
type fakeObsGw struct {
	enabled, connected bool
	connectedAfter     int // Connected() flips true after N calls (readiness poll)
	connCalls          int
	applied            *obs.Preset
	started            []string // control calls in order
	failStartStream    bool
}

func (f *fakeObsGw) Enabled() bool { return f.enabled }
func (f *fakeObsGw) Connected() bool {
	f.connCalls++
	if f.connectedAfter > 0 && f.connCalls > f.connectedAfter {
		return true
	}
	return f.connected
}
func (f *fakeObsGw) Statuses() []obscontrol.Instance {
	return []obscontrol.Instance{{Node: "n1", Local: true, Status: obscontrol.Status{ID: "n1", Connected: true}}}
}
func (f *fakeObsGw) ListProfiles(context.Context) (string, []string, error) {
	return "Live", []string{"Default", "Live"}, nil
}
func (f *fakeObsGw) ListSceneCollections(context.Context) (string, []string, error) {
	return "Rave", []string{"Rave"}, nil
}
func (f *fakeObsGw) GetSettings(context.Context) (obs.StreamServiceSettings, obs.VideoSettings, error) {
	return obs.StreamServiceSettings{Type: "rtmp_custom"}, obs.VideoSettings{OutputWidth: 1920}, nil
}
func (f *fakeObsGw) CapturePreset(context.Context) (obs.Preset, error) {
	return obs.Preset{Profile: "Live"}, nil
}
func (f *fakeObsGw) ApplyPreset(_ context.Context, p obs.Preset) error {
	f.applied = &p
	return nil
}
func (f *fakeObsGw) StartStream(context.Context) error {
	if f.failStartStream {
		return errors.New("boom")
	}
	f.started = append(f.started, "stream")
	return nil
}
func (f *fakeObsGw) StopStream(context.Context) error { return nil }
func (f *fakeObsGw) StartRecord(context.Context) error {
	f.started = append(f.started, "record")
	return nil
}
func (f *fakeObsGw) StopRecord(context.Context) error { return nil }

// fakeAppGrp scripts studio.AppGroupGateway.
type fakeAppGrp struct {
	configured bool
	launched   []string
	running    int
	total      int
	launchErr  error
}

func (f *fakeAppGrp) Configured() bool { return f.configured }
func (f *fakeAppGrp) List() []AppGroupInfo {
	return []AppGroupInfo{{ID: "g1", Name: "DJ rig", Apps: f.total, Running: f.running}}
}
func (f *fakeAppGrp) Launch(id string) ([]string, []string, error) {
	f.launched = append(f.launched, id)
	if f.launchErr != nil {
		return nil, nil, f.launchErr
	}
	f.running = f.total
	return []string{"obs64.exe"}, []string{"traktor.exe"}, nil
}
func (f *fakeAppGrp) Readiness(string) (int, int, error) { return f.running, f.total, nil }

func TestIsObsFamilyMethod(t *testing.T) {
	for _, m := range obsMethods {
		if !isObsFamilyMethod(m) {
			t.Fatalf("%s not recognized", m)
		}
	}
	for _, m := range appgroupMethods {
		if !isObsFamilyMethod(m) {
			t.Fatalf("%s not recognized", m)
		}
	}
	for _, m := range quickActionMethods {
		if !isObsFamilyMethod(m) {
			t.Fatalf("%s not recognized", m)
		}
	}
	for _, m := range []string{"obs.nope", "vrchat.status", "localMedia.probe", ""} {
		if isObsFamilyMethod(m) {
			t.Fatalf("%s wrongly recognized", m)
		}
	}
}

func TestDispatchObsGating(t *testing.T) {
	// nil / disabled gateway → unknown-method error.
	if _, code, err := dispatchObs(context.Background(), nil, nil, "obs.status", nil); err == nil || code != errUnknownMethod {
		t.Fatal("nil gateway not gated")
	}
	gw := &fakeObsGw{enabled: false}
	if _, _, err := dispatchObs(context.Background(), gw, nil, "obs.startStream", nil); err == nil {
		t.Fatal("disabled gateway not gated")
	}
	// appgroup gated independently of obs.
	if _, _, err := dispatchObs(context.Background(), gw, &fakeAppGrp{configured: false}, "appgroup.list", nil); err == nil {
		t.Fatal("unconfigured appgroups not gated")
	}
}

func TestDispatchObsReadsAndControl(t *testing.T) {
	ctx := context.Background()
	gw := &fakeObsGw{enabled: true, connected: true}
	res, _, err := dispatchObs(ctx, gw, nil, "obs.listProfiles", nil)
	if err != nil {
		t.Fatal(err)
	}
	if nl := res.(obsNameList); nl.Current != "Live" || len(nl.Names) != 2 {
		t.Fatalf("listProfiles: %+v", nl)
	}
	if _, _, err := dispatchObs(ctx, gw, nil, "obs.startRecord", nil); err != nil {
		t.Fatal(err)
	}
	if len(gw.started) != 1 || gw.started[0] != "record" {
		t.Fatalf("control calls: %v", gw.started)
	}
	// applyPreset: params → obs.Preset roundtrip.
	p := map[string]any{"preset": map[string]any{"profile": "Live"}}
	if _, _, err := dispatchObs(ctx, gw, nil, "obs.applyPreset", p); err != nil {
		t.Fatal(err)
	}
	if gw.applied == nil || gw.applied.Profile != "Live" {
		t.Fatalf("applied: %+v", gw.applied)
	}
	// empty preset rejected.
	if _, code, err := dispatchObs(ctx, gw, nil, "obs.applyPreset", map[string]any{"preset": map[string]any{}}); err == nil || code != errBadRequest {
		t.Fatal("empty preset not rejected")
	}
}

func TestDispatchAppGroup(t *testing.T) {
	ag := &fakeAppGrp{configured: true, total: 2, running: 1}
	res, _, err := dispatchObs(context.Background(), nil, ag, "appgroup.readiness", map[string]any{"id": "g1"})
	if err != nil {
		t.Fatal(err)
	}
	if r := res.(appGroupReadinessOut); r.Ready || r.Running != 1 || r.Total != 2 {
		t.Fatalf("readiness: %+v", r)
	}
	if _, code, err := dispatchObs(context.Background(), nil, ag, "appgroup.launch", nil); err == nil || code != errBadRequest {
		t.Fatal("missing id not rejected")
	}
	res, _, err = dispatchObs(context.Background(), nil, ag, "appgroup.launch", map[string]any{"id": "g1"})
	if err != nil {
		t.Fatal(err)
	}
	if l := res.(appGroupLaunchOut); len(l.Started) != 1 || len(l.Skipped) != 1 {
		t.Fatalf("launch: %+v", l)
	}
}

func TestStreamReadyHappyPath(t *testing.T) {
	gw := &fakeObsGw{enabled: true, connectedAfter: 1} // connected on 2nd poll
	ag := &fakeAppGrp{configured: true, total: 2, running: 0}
	out := streamReady(context.Background(), gw, ag, map[string]any{
		"groupId":     "g1",
		"preset":      map[string]any{"profile": "Live"},
		"startRecord": true,
		"startStream": true,
	})
	if !out.OK || out.FailedStep != "" {
		t.Fatalf("streamReady failed: %+v", out)
	}
	wantSteps := []string{"launch", "readiness", "applyPreset", "startRecord", "startStream"}
	if len(out.Steps) != len(wantSteps) {
		t.Fatalf("steps: %+v", out.Steps)
	}
	for i, s := range out.Steps {
		if s.Step != wantSteps[i] || !s.OK {
			t.Fatalf("step %d: %+v", i, s)
		}
	}
	if gw.applied == nil || len(gw.started) != 2 || gw.started[0] != "record" || gw.started[1] != "stream" {
		t.Fatalf("side effects: applied=%v started=%v", gw.applied, gw.started)
	}
	if len(ag.launched) != 1 || ag.launched[0] != "g1" {
		t.Fatalf("launched: %v", ag.launched)
	}
}

func TestStreamReadyFailFast(t *testing.T) {
	// launch fails → nothing after runs.
	gw := &fakeObsGw{enabled: true, connected: true}
	ag := &fakeAppGrp{configured: true, total: 1, launchErr: errors.New("no exe")}
	out := streamReady(context.Background(), gw, ag, map[string]any{"groupId": "g1", "startStream": true})
	if out.OK || out.FailedStep != "launch" || len(out.Steps) != 1 {
		t.Fatalf("fail-fast launch: %+v", out)
	}
	if len(gw.started) != 0 {
		t.Fatal("steps ran after failure")
	}

	// startStream fails after record started → failedStep=startStream, record step reported ok.
	gw2 := &fakeObsGw{enabled: true, connected: true, failStartStream: true}
	out2 := streamReady(context.Background(), gw2, nil, map[string]any{"startRecord": true, "startStream": true})
	if out2.OK || out2.FailedStep != "startStream" {
		t.Fatalf("fail-fast stream: %+v", out2)
	}
	if len(out2.Steps) != 3 || !out2.Steps[1].OK || out2.Steps[2].OK {
		t.Fatalf("steps: %+v", out2.Steps)
	}
}

func TestStreamReadyReadinessTimeout(t *testing.T) {
	// OBS never connects; cancelled ctx bounds the poll instead of the 60s cap.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	gw := &fakeObsGw{enabled: true, connected: false}
	out := streamReady(ctx, gw, nil, map[string]any{"startStream": true})
	if out.OK || out.FailedStep != "readiness" {
		t.Fatalf("readiness timeout: %+v", out)
	}
}

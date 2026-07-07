package featurehost

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/eventbus"
	"rave.page/mate/internal/vrbind"
)

// vrInitParams is an init payload: config snapshot with one enabled overlay.
const vrInitParams = `{"config":{"enabled":true,"editHand":"right","overlays":[{"id":"chat1","type":"chat","enabled":true}]}}`

// initVROK sends init with the config snapshot and requires ready.
func initVROK(t *testing.T, h *harness) {
	t.Helper()
	h.send(t, frame{ID: "1", Method: methodInit, Params: json.RawMessage(vrInitParams)})
	if fr := h.next(t, func(f frame) bool { return f.ID == "1" }); !fr.OK {
		t.Fatalf("init failed: %s", fr.Error)
	}
}

// The vr feature child inits from a config snapshot, runs the manager lifecycle in Start, and unwinds
// cleanly (exit 0) on stop - the contract the daemon module relies on.
func TestVRFeatureLifecycle(t *testing.T) {
	h := newHarness(&vrFeature{})
	initVROK(t, h)
	h.send(t, frame{ID: "9", Method: methodStop})
	if fr := h.next(t, func(f frame) bool { return f.ID == "9" }); !fr.OK {
		t.Fatalf("stop: %s", fr.Error)
	}
	if c := h.exitCode(t); c != 0 {
		t.Fatalf("exit code %d, want 0", c)
	}
}

// logFrameContains matches an EventLog frame whose message contains sub (log forwarding is async
// vs responses, so log tests match the log frame directly instead of the init response).
func logFrameContains(sub string) func(frame) bool {
	return func(f frame) bool {
		if f.Event != EventLog {
			return false
		}
		var le logEvent
		return json.Unmarshal(f.Data, &le) == nil && strings.Contains(le.Msg, sub)
	}
}

// Child logs flow to the daemon as EventLog frames (Runtime log convention): the init log carries the
// snapshot's overlay count.
func TestVRFeatureLogsFlowToDaemon(t *testing.T) {
	h := newHarness(&vrFeature{})
	h.send(t, frame{ID: "1", Method: methodInit, Params: json.RawMessage(vrInitParams)})
	fr := h.next(t, logFrameContains("vr overlay subprocess up"))
	var le logEvent
	if err := json.Unmarshal(fr.Data, &le); err != nil {
		t.Fatalf("log event decode: %v", err)
	}
	if le.Source != "feature:vr" || le.Fields["overlays"] != float64(1) {
		t.Fatalf("log event source=%q fields=%v", le.Source, le.Fields)
	}
}

// On a non-vr build (this test binary) the runtime is the stub: the manager reports VR/SteamVR
// unavailable via a forwarded log and keeps supervising - no crash, and stop still exits 0.
func TestVRFeatureUnavailableGraceful(t *testing.T) {
	h := newHarness(&vrFeature{})
	h.send(t, frame{ID: "1", Method: methodInit, Params: json.RawMessage(vrInitParams)})
	// Manager.Start logs an idle/waiting SteamVR line immediately (stub runtime never connects).
	h.next(t, logFrameContains("SteamVR"))
	h.send(t, frame{ID: "9", Method: methodStop})
	if fr := h.next(t, func(f frame) bool { return f.ID == "9" }); !fr.OK {
		t.Fatalf("stop: %s", fr.Error)
	}
	if c := h.exitCode(t); c != 0 {
		t.Fatalf("exit code %d, want 0", c)
	}
}

// Unknown methods stay a clean error, never a crash.
func TestVRFeatureUnknownMethod(t *testing.T) {
	h := newHarness(&vrFeature{})
	initVROK(t, h)
	h.send(t, frame{ID: "2", Method: "anything", Params: json.RawMessage(`{}`)})
	fr := h.next(t, func(f frame) bool { return f.ID == "2" })
	if fr.OK || fr.Error == "" {
		t.Fatalf("want unknown-method error, got ok=%v err=%q", fr.OK, fr.Error)
	}
}

// Surface methods answer over the pipe: on the stub build inputDiag explains unavailability,
// bindingStatus is NotReady, and the toggle controls ack cleanly.
func TestVRFeatureSurfaceMethods(t *testing.T) {
	h := newHarness(&vrFeature{})
	initVROK(t, h)

	h.send(t, frame{ID: "2", Method: vrMethInputDiag})
	fr := h.next(t, func(f frame) bool { return f.ID == "2" })
	var diag string
	if !fr.OK || json.Unmarshal(fr.Result, &diag) != nil || !strings.Contains(diag, "unavailable") {
		t.Fatalf("inputDiag: ok=%v result=%s", fr.OK, fr.Result)
	}

	h.send(t, frame{ID: "3", Method: vrMethBindingStatus})
	fr = h.next(t, func(f frame) bool { return f.ID == "3" })
	if !fr.OK || string(fr.Result) != "0" { // BindingNotReady
		t.Fatalf("bindingStatus: ok=%v result=%s", fr.OK, fr.Result)
	}

	h.send(t, frame{ID: "4", Method: vrMethToggleAll})
	if fr := h.next(t, func(f frame) bool { return f.ID == "4" }); !fr.OK {
		t.Fatalf("toggleAll: %s", fr.Error)
	}
	h.send(t, frame{ID: "5", Method: vrMethEditorToggle})
	if fr := h.next(t, func(f frame) bool { return f.ID == "5" }); !fr.OK {
		t.Fatalf("editorToggle: %s", fr.Error)
	}
	h.send(t, frame{ID: "6", Method: vrMethSetHidden, Params: json.RawMessage(`{"id":"chat1","hidden":true}`)})
	if fr := h.next(t, func(f frame) bool { return f.ID == "6" }); !fr.OK {
		t.Fatalf("setHidden: %s", fr.Error)
	}
}

// Parent→child state pushes (config / world / campaths / stats) are full replacements, visible in
// the snapshot diag - the daemon re-pushes exactly these on every child (re)spawn.
func TestVRFeatureStatePushes(t *testing.T) {
	f := &vrFeature{}
	h := newHarness(f)
	initVROK(t, h)

	send := func(event string, payload string) {
		h.send(t, frame{Event: event, Data: json.RawMessage(payload)})
	}
	send(vrEvConfig, `{"enabled":true,"overlays":[{"id":"a","type":"chat","enabled":true},{"id":"b","type":"alerts","enabled":true}]}`)
	send(vrEvWorld, `{"id":"wrld_1","name":"Club","ok":true}`)
	send(vrEvCamPaths, `{"items":[{"label":"Club · sweep","file":"p1.dolly","pts":[[1,2,3]],"spd":[0.5],"dur":[2]}]}`)
	send(vrEvStats, `{"perf":[{"CPUPct":5}],"net":{"peerIn":[1,null,3]}}`)

	h.send(t, frame{ID: "2", Method: vrMethSnapshot})
	fr := h.next(t, func(f frame) bool { return f.ID == "2" })
	var s vrSnapshot
	if !fr.OK || json.Unmarshal(fr.Result, &s) != nil {
		t.Fatalf("snapshot: ok=%v err=%s", fr.OK, fr.Error)
	}
	if s.Overlays != 2 || s.WorldID != "wrld_1" || s.CamPaths != 1 || !s.StatsOK || s.Available {
		t.Fatalf("snapshot after pushes: %+v", s)
	}

	// Provider views reflect the pushed state (what the Manager renders from).
	if id, name, ok := f.worldSrc(); id != "wrld_1" || name != "Club" || !ok {
		t.Fatalf("worldSrc: %s %s %v", id, name, ok)
	}
	if items := f.camList(); len(items) != 1 || items[0].File != "p1.dolly" {
		t.Fatalf("camList: %+v", items)
	}
	if g := f.camGeom("p1.dolly"); len(g.Pts) != 1 || g.Pts[0] != [3]float32{1, 2, 3} || g.Spd[0] != 0.5 {
		t.Fatalf("camGeom: %+v", g)
	}
	if n := f.netStats(); len(n.PeerIn) != 3 {
		t.Fatalf("netStats: %+v", n)
	}
}

// An in-VR config edit streams the FULL feature back to the daemon (declarative persist).
func TestVRFeatureMutateEmitsConfig(t *testing.T) {
	f := &vrFeature{}
	h := newHarness(f)
	initVROK(t, h)

	// Off-thread: Emit blocks on the un-drained in-mem pipe until h.next reads.
	go f.mutate(func(c *config.VROverlayFeature) { c.StickMoveOnly = true })
	fr := h.next(t, func(fr frame) bool { return fr.Event == vrEvConfig })
	var c config.VROverlayFeature
	if json.Unmarshal(fr.Data, &c) != nil || !c.StickMoveOnly || len(c.Overlays) != 1 {
		t.Fatalf("config event: %s", fr.Data)
	}
}

// A camera-path load request forwards to the daemon (vrctools owns OSC + backup there).
func TestVRFeatureCamLoadForwards(t *testing.T) {
	f := &vrFeature{}
	h := newHarness(f)
	initVROK(t, h)

	go func() { _ = f.camLoad("p1.dolly") }() // off-thread: Emit blocks until h.next reads
	fr := h.next(t, func(fr frame) bool { return fr.Event == vrEvCamLoad })
	var ev vrCamLoadEvent
	if json.Unmarshal(fr.Data, &ev) != nil || ev.File != "p1.dolly" {
		t.Fatalf("campath event: %s", fr.Data)
	}
}

// Non-local bind actions (OBS record etc.) forward as action events; overlay-local ones stay
// in-child.
func TestVRFeatureDispatcherForwards(t *testing.T) {
	f := &vrFeature{}
	h := newHarness(f)
	initVROK(t, h)

	d := f.dispatcher()
	go d.Fire(vrbind.Bind{Action: vrbind.ActOBSRecord, Target: "node-b"}) // off-thread: Emit blocks until h.next reads
	fr := h.next(t, func(fr frame) bool { return fr.Event == vrEvAction })
	var a vrActionEvent
	if json.Unmarshal(fr.Data, &a) != nil || a.Action != string(vrbind.ActOBSRecord) || a.Target != "node-b" {
		t.Fatalf("action event: %s", fr.Data)
	}
	d.Fire(vrbind.Bind{Action: vrbind.ActOverlaysToggle}) // local: handled by the manager, no frame
}

// The child bus preserves Origin/Local on inject (eventbus.Inbound would force Local=false and
// break "(this PC)" tagging) and forwards local publishes to the emitter.
func TestVRChildBus(t *testing.T) {
	var mu sync.Mutex
	var emitted []string
	b := &vrChildBus{emit: func(topic string, data json.RawMessage) {
		mu.Lock()
		emitted = append(emitted, topic+":"+string(data))
		mu.Unlock()
	}}

	var got []eventbus.Event
	unsub := b.Subscribe("t1", func(e eventbus.Event) { got = append(got, e) })
	b.inject(eventbus.Event{Topic: "t1", Origin: "node-a", Local: true, Data: json.RawMessage(`1`)})
	b.inject(eventbus.Event{Topic: "other", Origin: "x", Data: json.RawMessage(`2`)}) // no subscriber
	if len(got) != 1 || got[0].Origin != "node-a" || !got[0].Local {
		t.Fatalf("inject: %+v", got)
	}
	unsub()
	b.inject(eventbus.Event{Topic: "t1", Data: json.RawMessage(`3`)})
	if len(got) != 1 {
		t.Fatal("unsubscribe leaked")
	}

	b.Publish("vr.perf", json.RawMessage(`{"fps":90}`))
	mu.Lock()
	defer mu.Unlock()
	if len(emitted) != 1 || emitted[0] != `vr.perf:{"fps":90}` {
		t.Fatalf("publish: %v", emitted)
	}
}

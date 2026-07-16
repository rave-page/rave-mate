package crewlink

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"rave.page/mate/internal/mocapmaster"
	"rave.page/mate/internal/mocapnode"
	"rave.page/mate/internal/mocappanel"
)

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

type staticTokens string

func (s staticTokens) Token() string { return string(s) }

// waitFor polls cond until true or the deadline; fails the test on timeout.
func waitFor(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// goldenPacket is the frozen contract vector as a node packet captured `age` ago.
func goldenPacket(age time.Duration) mocapnode.Packet {
	h, dancers := mocappanel.GoldenFrame()
	return mocapnode.Packet{CapturedAt: time.Now().Add(-age), Header: h, Dancers: dancers}
}

// ── golden wire JSON (§4 - field names are the frozen contract) ──────────────

func TestGoldenWireJSON(t *testing.T) {
	pose := PoseFrame{
		T:            FrameTypePose,
		CapturedAtNs: 123456789,
		Header: WireHeader{
			Version: 1, Flags: 2, SourceTag: 0xC0FFEE, SessionNonce: 0xBEEF,
			PanelSeq: 7, ServerTimeMs: 1234567890123, NetUtcTicks: 638600000000000000,
			BpmX100: 12800, DownbeatServerTimeMs: 1234567890000,
			BoneSlots: 2, DancerCount: 1, FrameCounter: 42,
			StageMin: [3]float64{-8, 0, -6}, StageSize: [3]float64{16, 4, 12},
		},
		Dancers: []WireDancer{{
			LocalID: 7, Flags: 1, BoneMask: 3,
			HipsQ: [3]uint16{0x9400, 0x4000, 0x7555}, Rots: []uint32{111, 222},
		}},
	}
	wantPose := `{"t":"pose","capturedAtNs":123456789,` +
		`"header":{"version":1,"flags":2,"sourceTag":12648430,"sessionNonce":48879,` +
		`"panelSeq":7,"serverTimeMs":1234567890123,"netUtcTicks":638600000000000000,` +
		`"bpmX100":12800,"downbeatServerTimeMs":1234567890000,` +
		`"boneSlots":2,"dancerCount":1,"frameCounter":42,` +
		`"stageMin":[-8,0,-6],"stageSize":[16,4,12]},` +
		`"dancers":[{"localId":7,"flags":1,"boneMask":3,"hipsQ":[37888,16384,30037],"rots":[111,222]}]}`
	got, err := json.Marshal(pose)
	if err != nil {
		t.Fatalf("marshal pose: %v", err)
	}
	if string(got) != wantPose {
		t.Errorf("pose wire JSON:\n got %s\nwant %s", got, wantPose)
	}
	var poseBack PoseFrame
	if err := json.Unmarshal(got, &poseBack); err != nil {
		t.Fatalf("unmarshal pose: %v", err)
	}
	if !reflect.DeepEqual(poseBack, pose) {
		t.Errorf("pose round trip mismatch:\n got %+v\nwant %+v", poseBack, pose)
	}

	ping := SyncFrame{T: FrameTypeSync, ID: 3, T1: 1000}
	if got, _ := json.Marshal(ping); string(got) != `{"t":"sync","id":3,"t1":1000}` {
		t.Errorf("sync ping JSON = %s", got)
	}
	pong := SyncFrame{T: FrameTypeSync, ID: 3, T1: 1000, T2: 2000, T3: 3000}
	if got, _ := json.Marshal(pong); string(got) != `{"t":"sync","id":3,"t1":1000,"t2":2000,"t3":3000}` {
		t.Errorf("sync pong JSON = %s", got)
	}
	if ping.IsPong() || !pong.IsPong() {
		t.Errorf("IsPong: ping=%v pong=%v", ping.IsPong(), pong.IsPong())
	}

	kick := CtrlFrame{T: FrameTypeCtrl, Op: CtrlOpKick}
	if got, _ := json.Marshal(kick); string(got) != `{"t":"ctrl","op":"kick"}` {
		t.Errorf("ctrl kick JSON = %s", got)
	}
	mn, sz := [3]float64{-8, 0, -6}, [3]float64{16, 4, 12}
	cfgF := CtrlFrame{T: FrameTypeCtrl, Op: CtrlOpConfig, BoneSlots: 22, StageMin: &mn, StageSize: &sz}
	wantCfg := `{"t":"ctrl","op":"config","boneSlots":22,"stageMin":[-8,0,-6],"stageSize":[16,4,12]}`
	if got, _ := json.Marshal(cfgF); string(got) != wantCfg {
		t.Errorf("ctrl config JSON = %s, want %s", got, wantCfg)
	}
}

// TestPoseRoundTripsPacket pins wire → packet reconstruction: Quats/Present nil (the store
// recomputes), everything else field-exact, CapturedAt = time.Unix(0, ns).
func TestPoseRoundTripsPacket(t *testing.T) {
	pkt := goldenPacket(0)
	const stamp = int64(1_700_000_000_000_000_000)
	back := PoseFromPacket(pkt, stamp).Packet()

	if !back.CapturedAt.Equal(time.Unix(0, stamp)) {
		t.Errorf("CapturedAt = %v, want %v", back.CapturedAt, time.Unix(0, stamp))
	}
	if !reflect.DeepEqual(back.Header, pkt.Header) {
		t.Errorf("header mismatch:\n got %+v\nwant %+v", back.Header, pkt.Header)
	}
	if len(back.Dancers) != len(pkt.Dancers) {
		t.Fatalf("dancer count %d, want %d", len(back.Dancers), len(pkt.Dancers))
	}
	for i := range back.Dancers {
		g, w := back.Dancers[i], pkt.Dancers[i]
		if g.Quats != nil || g.Present != nil {
			t.Errorf("dancer %d: Quats/Present crossed the wire", i)
		}
		if g.LocalID != w.LocalID || g.Flags != w.Flags || g.BoneMask != w.BoneMask ||
			g.HipsQ != w.HipsQ || !reflect.DeepEqual(g.Rots, w.Rots) {
			t.Errorf("dancer %d field mismatch:\n got %+v\nwant %+v", i, g, w)
		}
	}
}

// ── clock restamp under a skewed node clock (§5) ─────────────────────────────

// TestClockRestampUnderSkew disciplines a node clock against a simulated master domain 5h
// ahead of local, then checks stampNs lands inside the master's clamp window.
func TestClockRestampUnderSkew(t *testing.T) {
	const skew = 5 * time.Hour
	masterNow := func() time.Time { return time.Now().Add(skew) }

	n := NewNode(NodeConfig{Client: NewClient("http://unused", staticTokens("t")), EventID: "ev"})
	n.mu.Lock()
	n.syncSID = "m1"
	n.mu.Unlock()

	// Feed pongs the way the wire would: T1 from the node clock, T2/T3 from the master.
	for i := 0; i < 8; i++ {
		ping := SyncFrame{T: FrameTypeSync, ID: uint32(i), T1: n.clock.Now()}
		remote := masterNow().UnixNano()
		pong := SyncFrame{T: FrameTypeSync, ID: ping.ID, T1: ping.T1, T2: remote, T3: remote}
		b, _ := json.Marshal(pong)
		n.onRelay("m1", b, func() {})
	}
	if !n.Status().Locked {
		t.Fatalf("clock not locked after 8 pong samples")
	}

	// A packet captured 100ms ago restamps into the master domain within tolerance.
	pkt := goldenPacket(100 * time.Millisecond)
	ns := n.stampNs(pkt)
	want := masterNow().Add(-100 * time.Millisecond).UnixNano()
	if diff := ns - want; diff < -50e6 || diff > 50e6 {
		t.Errorf("restamp off master domain by %dms", diff/1e6)
	}

	// The master's clamp accepts it (Now seam = the skewed master domain).
	var injected int
	ml := NewMaster(MasterConfig{
		Client: NewClient("http://unused", staticTokens("t")), EventID: "ev",
		Inject: func(mocapnode.Packet) bool { injected++; return true },
		Now:    masterNow,
	})
	b, _ := json.Marshal(PoseFromPacket(pkt, ns))
	ml.ingestPose(b)
	if st := ml.Status(); injected != 1 || st.Clamped != 0 {
		t.Errorf("skew-restamped pose not accepted: injected=%d status=%+v", injected, st)
	}

	// Pongs from a non-elected master never discipline the clock.
	before := n.clock.Now()
	stray := SyncFrame{T: FrameTypeSync, ID: 99, T1: before, T2: 1, T3: 1}
	sb, _ := json.Marshal(stray)
	n.onRelay("intruder", sb, func() {})
	if got := n.clock.Now() - before; got < 0 || got > int64(time.Second) {
		t.Errorf("stray pong moved the disciplined clock by %dns", got)
	}
}

// ── master ingest clamp (§5) ─────────────────────────────────────────────────

func TestMasterClampRejects(t *testing.T) {
	var injected int
	ml := NewMaster(MasterConfig{
		Client: NewClient("http://unused", staticTokens("t")), EventID: "ev",
		Inject: func(mocapnode.Packet) bool { injected++; return true },
	})
	send := func(ns int64) {
		b, _ := json.Marshal(PoseFromPacket(goldenPacket(0), ns))
		ml.ingestPose(b)
	}

	send(time.Now().Add(-10 * time.Second).UnixNano()) // stale → clamp
	send(time.Now().Add(time.Second).UnixNano())       // future → clamp
	send(time.Now().Add(-100 * time.Millisecond).UnixNano())

	st := ml.Status()
	if injected != 1 || st.Clamped != 2 || st.Frames != 3 {
		t.Errorf("clamp: injected=%d clamped=%d frames=%d (want 1/2/3)", injected, st.Clamped, st.Frames)
	}

	// Garbage payload: dropped, counted, never fatal.
	ml.ingestPose([]byte(`{"t":"pose","capturedAtNs":`))
	if st := ml.Status(); st.Dropped != 1 {
		t.Errorf("garbage pose: dropped=%d, want 1", st.Dropped)
	}
}

// ── bounded uplink queue (drop-newest) ───────────────────────────────────────

func TestNodeQueueDropsNewest(t *testing.T) {
	n := NewNode(NodeConfig{Client: NewClient("http://unused", staticTokens("t")), EventID: "ev", QueueCap: 2})
	for i := 0; i < 5; i++ {
		n.Enqueue(goldenPacket(0))
	}
	if st := n.Status(); st.Dropped != 3 {
		t.Errorf("dropped = %d, want 3 (cap 2, 5 enqueued)", st.Dropped)
	}
}

// ── ctrl kick (unit) ─────────────────────────────────────────────────────────

func TestNodeCtrlKickEndsSession(t *testing.T) {
	n := NewNode(NodeConfig{Client: NewClient("http://unused", staticTokens("t")), EventID: "ev"})
	cancelled := false
	b, _ := json.Marshal(CtrlFrame{T: FrameTypeCtrl, Op: CtrlOpKick})
	n.onRelay("master1", b, func() { cancelled = true })
	n.mu.Lock()
	kicked := n.kicked
	n.mu.Unlock()
	if !cancelled || !kicked {
		t.Errorf("ctrl kick: cancelled=%v kicked=%v, want true/true", cancelled, kicked)
	}
}

// ── full round trip over the stub relay ──────────────────────────────────────

// TestRoundTripOverStubRelay drives the whole path: node packet → uplink wire JSON → relay →
// master ingest → PoseStore, with the node clock disciplined over the same relay. Asserts the
// store's ActiveDancers match the golden vector to quantization.
func TestRoundTripOverStubRelay(t *testing.T) {
	stub := newStubRelay()
	srv := httptest.NewServer(stub)
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // before srv.Close (LIFO): streams drop, handlers exit

	store, err := mocapmaster.New(mocapmaster.Config{
		BoneSlots: 22, StageMin: [3]float64{-8, 0, -6}, StageSize: [3]float64{16, 4, 12},
	})
	if err != nil {
		t.Fatalf("mocapmaster.New: %v", err)
	}
	var lastInjected time.Time
	ml := NewMaster(MasterConfig{
		Client: NewClient(srv.URL, staticTokens("tok")), EventID: "ev1", Label: "stage",
		Inject: func(pkt mocapnode.Packet) bool {
			lastInjected = pkt.CapturedAt
			store.OnPacket(pkt)
			return true
		},
		Logf: t.Logf,
	})
	go ml.Run(ctx)
	waitFor(t, "master joined", 5*time.Second, func() bool { return ml.Status().SID != "" })

	node := NewNode(NodeConfig{
		Client: NewClient(srv.URL, staticTokens("tok")), EventID: "ev1", Label: "cam rig",
		BurstEvery: 5 * time.Millisecond, SteadyEvery: 25 * time.Millisecond,
		Logf: t.Logf,
	})
	go node.Run(ctx)
	waitFor(t, "node sees master + clock lock", 10*time.Second, func() bool {
		st := node.Status()
		return st.SID != "" && st.Masters == 1 && st.Locked
	})

	// Keep enqueueing fresh golden frames until the store answers (staleness 500ms).
	waitFor(t, "poses in the store", 10*time.Second, func() bool {
		node.Enqueue(goldenPacket(0))
		return len(store.Store().ActiveDancers(time.Now())) == 2
	})

	// Restamped CapturedAt landed in the master's wall-clock domain.
	if age := time.Since(lastInjected); age < 0 || age > time.Second {
		t.Errorf("injected CapturedAt %v off the master domain (age %v)", lastInjected, age)
	}
	if st := ml.Status(); st.Clamped != 0 || st.Dropped != 0 {
		t.Errorf("master rejected frames: %+v", st)
	}

	// Store contents match the golden vector to quantization (HipsQ + Rots verbatim; Quats/
	// Present recomputed by the store from the wire truth = the golden values).
	_, want := mocappanel.GoldenFrame()
	got := store.Store().ActiveDancers(time.Now())
	if len(got) != 2 {
		t.Fatalf("active dancers = %d, want 2", len(got))
	}
	for i := range got {
		if !reflect.DeepEqual(got[i].Dancer, want[i]) {
			t.Errorf("dancer %d mismatch:\n got %+v\nwant %+v", i, got[i].Dancer, want[i])
		}
	}
}

// TestKickRejoinsAndRevocation drives the §8 failure envelope: a server kick ends the session
// (presence kick surface) and the supervisor re-joins with a fresh sid.
func TestKickRejoinsAndRevocation(t *testing.T) {
	stub := newStubRelay()
	srv := httptest.NewServer(stub)
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	node := NewNode(NodeConfig{
		Client: NewClient(srv.URL, staticTokens("tok")), EventID: "ev1",
		BurstEvery: 5 * time.Millisecond, SteadyEvery: 25 * time.Millisecond,
		Logf: t.Logf,
	})
	go node.Run(ctx)
	waitFor(t, "node joined", 5*time.Second, func() bool { return node.Status().SID != "" })
	first := node.Status().SID

	stub.kick(first)
	waitFor(t, "node re-joined after kick", 10*time.Second, func() bool {
		st := node.Status()
		return st.SID != "" && st.SID != first
	})
	if stub.joinCount(RoleNode) < 2 {
		t.Errorf("join count = %d, want >= 2", stub.joinCount(RoleNode))
	}

	// A master joining later shows up via presence and becomes the sync target.
	ml := NewMaster(MasterConfig{
		Client: NewClient(srv.URL, staticTokens("tok")), EventID: "ev1",
		Inject: func(mocapnode.Packet) bool { return true },
		Logf:   t.Logf,
	})
	go ml.Run(ctx)
	waitFor(t, "node sees the late master", 5*time.Second, func() bool { return node.Status().Masters == 1 })

	// Master-issued ctrl kick over the relay ends the node session too (re-join follows).
	sid := node.Status().SID
	b, _ := json.Marshal(CtrlFrame{T: FrameTypeCtrl, Op: CtrlOpKick})
	stub.relayFrom(ml.Status().SID, sid, b)
	waitFor(t, "node re-joined after ctrl kick", 10*time.Second, func() bool {
		st := node.Status()
		return st.SID != "" && st.SID != sid
	})
}

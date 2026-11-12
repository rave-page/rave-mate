package featurehost

import (
	"context"
	"encoding/json"
	"os"
	"runtime/debug"
	"sync"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/eventbus"
	"rave.page/mate/internal/gpubench"
	"rave.page/mate/internal/gpumem"
	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/mediapipe"
	"rave.page/mate/internal/mediaroute"
	"rave.page/mate/internal/mfenc"
	"rave.page/mate/internal/pwcapture"
	"rave.page/mate/internal/webcam"
)

func init() { Register("media", func() Feature { return &mediaFeature{} }) }

// telemetryEvery paces the mirrored control-surface + clock snapshot (poll-style UI reads stay local).
const telemetryEvery = time.Second

// mediaBusTopics are the low-rate negotiation topics bridged both ways between the child's local bus
// and the daemon mesh bus. NOT media.tc - the timecode plane stays daemon-side and owns that topic.
var mediaBusTopics = []string{
	medialink.TopicAdvert, medialink.TopicOffer, medialink.TopicAnswer,
	webcam.TopicStatus, webcam.TopicCmd,
}

// mediaFeature hosts the whole media plane (medialink route manager + mediaroute receive manager +
// webcam manager) in ONE isolated child. Frames + Spout handles + the func-callback registration graph
// all stay in-proc HERE; only the Phase-1 control surfaces + a low-rate negotiation bus cross to the
// daemon. See mediawire.go for the boundary rationale.
type mediaFeature struct {
	rt   *Runtime
	self string

	bus     *eventbus.Bus // child-local mesh bus (broadcast=nil - no peerlink in the child)
	clock   *medialink.SoftwareClock
	router  *medialink.RouteManager
	routes  *mediaroute.Manager
	cam     *webcam.Manager
	pw      *pwcapture.Manager // Linux PipeWire video-node capture (no-op where unavailable)
	secrets *mediaSecretStore
	vram    *VramBudget      // unified VRAM admission (decode/sender/encode); ctl explainability
	bench   *gpubench.Runner // throughput micro-benchmark (feeds plan-driven auto); lazy, idle-gated

	mu       sync.Mutex
	mediaCfg config.MediaLinkFeature
	camCfg   config.WebcamFeature

	upUnsub []func()

	seqMu       sync.Mutex
	seqByOrigin map[string]uint64 // synthetic per-origin seq for down-injected events (dedup-safe)
}

// withVAAPINativeEncoders appends the Linux zero-copy VA engine's encoder names (H.264 + HEVC) to the
// advertised set when this box can run it (libva + PipeWire capture). The names are honoured natively
// only for a dmabuf source; any other source (webcam) falls back to the ffmpeg *_vaapi readback, same
// wire codec. Advertising both lets negotiation prefer the native zero-copy path for a dmabuf 4K route
// over the ffmpeg *_vaapi readback (which stalls at 4K), for whichever codec the peer decodes.
func withVAAPINativeEncoders(encoders []string) []string {
	if !pwcapture.VAEncodeAvailable() {
		return encoders
	}
	out := append([]string(nil), encoders...)
	for _, want := range []string{medialink.EncoderVAAPINativeHEVC, medialink.EncoderVAAPINativeH264} {
		has := false
		for _, e := range out {
			if e == want {
				has = true
				break
			}
		}
		if !has {
			out = append(out, want)
		}
	}
	return out
}

func (f *mediaFeature) Init(params json.RawMessage, rt *Runtime) error {
	var in mediaInit
	if err := json.Unmarshal(params, &in); err != nil {
		return err
	}
	f.rt = rt
	f.self = in.Self
	setChildMemoryLimit(in.MemLimitMB, rt)
	f.mediaCfg = in.MediaCfg
	f.camCfg = in.CamCfg
	f.seqByOrigin = map[string]uint64{}
	f.secrets = newMediaSecretStore(in.Secrets)
	f.bus = eventbus.New(rt.Log, in.Self)
	f.clock = medialink.NewSoftwareClock()

	encFac, decFac := mediapipe.Factories(rt.Log)
	// Live media config (the daemon pushes updates into f.mediaCfg), so the sender-side codec
	// preference / encoder pin / device policy behave the same in the isolated child as in-proc.
	liveCfg := func() config.MediaLinkFeature { f.mu.Lock(); defer f.mu.Unlock(); return f.mediaCfg }
	// zigmedia: the zero-copy Spout->encoder capture path, DEFAULT ON since inc 5. Same live config
	// as the daemon, so the isolated child behaves identically.
	mediapipe.ZeroCopyCapture = func() bool { return liveCfg().ZeroCopyCapture() }
	// zigmedia inc 2: native GPU-resident decode+publish, DEFAULT ON since inc 5 (config.ZeroCopyDecode).
	mediapipe.ZeroCopyDecode = func() bool { return liveCfg().ZeroCopyDecode() }
	// zigmedia inc 3: adapter-affinity re-placement. Still default OFF - see config.ZigAffinity.
	mediapipe.ZeroCopyAffinity = func() bool { return liveCfg().ZeroCopyAffinity() }
	// VRAM congestion governor: gate decode recycling on primary-adapter headroom so a reopen never
	// frees a resident pipeline it can't rebuild on a full card (=> ffmpeg => new shared texture =>
	// Resolume re-registers interop => E_OUTOFVIDEOMEMORY crash). Fails open when the governor is off
	// or headroom is unknown. One sampler for the child; read only when a recycle is about to fire.
	gpuSampler := gpumem.NewSampler()
	mfenc.DecHeadroom = func() gpumem.Headroom {
		if !liveCfg().VramGovernorEnabled() {
			return gpumem.Headroom{}
		}
		return gpumem.ReadHeadroom(gpuSampler)
	}
	if r := liveCfg().ResolvedVramReserveMB(); r > 0 {
		mfenc.DecRebuildFloorMB = uint64(r)
	}
	// Unified VRAM budget: ONE admission policy over decode, sender and encode (geometry-aware cost +
	// reserve + self-cap), installed into the mfenc decode/encode seams. Its sender gate is handed to
	// mediaroute below. DecHeadroom/DecRebuildFloorMB above stay as the static-floor fallback.
	f.vram = WireVramBudget(liveCfg, gpuSampler, VramCostPath(), rt.Log)
	// Resolution-cap changes grant the local decoder a re-init grace so a coded-size switch is not
	// mistaken for a frozen-destination wedge (the destination geometry stays nominal regardless).
	medialink.OnResolutionCapChange = mfenc.NoteDecodeResizeGrace
	// Plan-driven AUTO device selection (throughput-aware; pin/avoid unchanged; unmeasured => engine
	// default). The sender-side CPU% is not sampled here (a HW encoder is the common case); nil.
	encDevResolver := NewEncodeDeviceResolver(liveCfg, f.vram.Budget, nil, rt.Log)
	f.router = medialink.New(medialink.Options{
		Self: in.Self, Bus: mediaBusAdapter{f.bus}, Secrets: f.secrets, Clock: f.clock,
		Log: rt.Log, Encoder: encFac, Decoder: decFac, UDP: in.MediaCfg.UDPEnabled(), // §2.5 UDP profile (falls back to TCP; mediaLink.transport="tcp" disables)
		EncodeMaxHeight: in.MediaCfg.MaxHeight,
		EncodePolicy: func() (string, string) {
			c := liveCfg()
			return c.PreferCodec, c.PinnedEncoder()
		},
		EncodeDevice:       encDevResolver,
		Headroom:           func() (uint64, bool) { h := gpumem.ReadHeadroom(gpuSampler); return h.FreeMB, h.Present },
		GovernorEnabled:    func() bool { return liveCfg().VramGovernorEnabled() },
		GovernorFloorMB:    uint64(liveCfg().ResolvedVramReserveMB()),
		GovernorBaseKbps:   func() int { return liveCfg().Bitrate() },
		BudgetMode:         func() string { return liveCfg().BitrateModeString() },
		BudgetUserCeilKbps: func() int { return liveCfg().UserBitrateCeilKbps() },
		BudgetLinkFracPct:  func() int { return liveCfg().LinkFractionPct() },
	})
	if len(in.Encoders) > 0 || len(in.Decoders) > 0 {
		f.router.SetCodecCaps(withVAAPINativeEncoders(in.Encoders), in.Decoders)
	}
	f.router.SetSyncPeer(in.SyncPeer)

	f.routes = mediaroute.New(mediaroute.Options{
		Log: rt.Log, Router: f.router,
		Cfg:      liveCfg,
		SameHost: nil, // the daemon proxy applies the same-host guard (peerMgr is daemon-side)
		Headroom: func() gpumem.Headroom {
			if !liveCfg().VramGovernorEnabled() {
				return gpumem.Headroom{}
			}
			return gpumem.ReadHeadroom(gpuSampler)
		},
		AdmitSender: f.vram.AdmitSender, // unified budget gate (static-floor fallback stays via Headroom)
	})

	// Throughput micro-benchmark: lazy + idle-gated, skips while any route is live. Feeds the
	// plan-driven auto device selection. Runs only in this supervised child (never the daemon).
	f.bench = NewBenchRunner(f.vram.Budget, gpuSampler, func() bool { return len(f.router.Stats()) > 0 }, rt.Log)

	f.cam = webcam.New(rt.Log, f.bus, in.Self, in.Label, func() config.WebcamFeature {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.camCfg
	})
	f.cam.SetRouter(f.router)

	// PipeWire video-node capture (Linux sender): discovers Stream/Output/Video + Video/Source
	// nodes and offers the capturable ones as medialink sources. No-op where libpipewire/-tags
	// pipewire is absent (every non-Linux build, and Linux without the shim).
	f.pw = pwcapture.New(rt.Log, pwcapture.Config{})
	f.pw.SetRouter(f.router)
	return nil
}

func (f *mediaFeature) Start(ctx context.Context) error {
	// Windows: 1 ms system timer so the PTS-paced jitter-buffer playout wakes precisely (stopgap for
	// the arrival-jitter stutter until the native receiver lands). No-op elsewhere.
	defer raiseTimerResolution()()
	if err := f.router.Start(ctx); err != nil {
		return err
	}
	f.routes.Start(ctx)
	if err := f.cam.Start(ctx); err != nil {
		f.router.Stop()
		return err
	}
	_ = f.pw.Start(ctx) // best-effort; Start no-ops (logged) when capture is unavailable
	f.bridgeUp()

	t := time.NewTicker(telemetryEvery)
	defer t.Stop()
	var lastClock clockOffsetEvent
	for {
		f.rt.Beat()
		f.emitTelemetry()
		f.benchTick(ctx)
		if c := f.clockEvent(); c != lastClock {
			f.rt.Emit(evMediaClockOffset, c)
			lastClock = c
		}
		select {
		case <-ctx.Done():
			for _, u := range f.upUnsub {
				u()
			}
			f.pw.Stop()
			f.cam.Stop()
			f.router.Stop()
			return nil
		case <-t.C:
		}
	}
}

// HandleEvent applies parent→child declarative pushes (secret / syncPeer / codecs / advert / bus).
func (f *mediaFeature) HandleEvent(event string, data json.RawMessage) {
	switch event {
	case evMediaSecret:
		var ps peerSecret
		if json.Unmarshal(data, &ps) == nil {
			f.secrets.set(ps)
		}
	case evMediaSyncPeer:
		var sp syncPeerEvent
		if json.Unmarshal(data, &sp) == nil {
			f.router.SetSyncPeer(sp.Node)
		}
	case evMediaCodecs:
		var cc codecCaps
		if json.Unmarshal(data, &cc) == nil {
			f.router.SetCodecCaps(withVAAPINativeEncoders(cc.Encoders), cc.Decoders)
		}
	case evMediaAdvert:
		f.router.Advertise()
	case evMediaBus:
		var be busEvent
		if json.Unmarshal(data, &be) == nil {
			f.injectBus(be)
		}
	}
}

// Handle services the control methods that need a return value / delivery confirmation.
func (f *mediaFeature) Handle(_ context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	switch method {
	case mStartReceive:
		var req startReceiveReq
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		sess, err := f.routes.StartReceive(req.Peer, req.SourceID)
		if err != nil {
			return nil, err
		}
		return json.Marshal(startReceiveResp{Session: sess})
	case mStopReceive:
		var req stopReceiveReq
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		f.routes.StopReceive(req.Session)
		return nil, nil
	case mCommand:
		var req commandReq
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		return nil, f.cam.Command(req.Cmd)
	case mFrameShot:
		var req frameShotReq
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		shot, err := f.routes.FrameShot(req.Sender, req.N, req.IntervalMs, req.Scale, req.Crop)
		if err != nil {
			return nil, err
		}
		return json.Marshal(shot)
	case mTestcard:
		var req testcardReq
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		rep, err := f.routes.Testcard(req.Op, req.W, req.H, req.FPS)
		if err != nil {
			return nil, err
		}
		return json.Marshal(rep)
	}
	return nil, errUnknownMethod(method)
}

// ── telemetry + clock mirror (child → daemon) ──

// benchTick drives the lazy throughput benchmark: when no route is live, enqueue the standard encode
// buckets for the working hardware encoder, then let the Runner measure one (gated + background).
func (f *mediaFeature) benchTick(ctx context.Context) {
	if f.bench == nil {
		return
	}
	if len(f.router.Stats()) == 0 {
		if caps, ok := mediapipe.Cached(); ok {
			if enc, ok := FirstHWEncoder(caps.Encoders); ok {
				for _, t := range gpubench.EncodeTargets("", enc, EncCodec(enc), 60) {
					f.bench.Want(t)
				}
			}
		}
	}
	f.bench.Tick(ctx)
}

func (f *mediaFeature) emitTelemetry() {
	cams := f.cam.Instances()
	camRunning := false
	for _, c := range cams {
		if c.Local && c.Running {
			camRunning = true
			break
		}
	}
	proc := f.router.ProcRuntime() // STEP-0: sample the media CHILD's Go runtime pauses here (right process)
	f.rt.Emit(evMediaTelemetry, mediaTelemetry{
		Routes:       f.router.Stats(),
		Proc:         &proc,
		Sync:         f.router.SyncStats(),
		Clock:        f.router.ClockQuality(),
		Encoders:     f.router.Encoders(),
		LocalSources: f.router.LocalSources(),
		Remote:       f.routes.RemoteVideoSources(),
		Receives:     f.routes.Receives(),
		Cams:         cams,
		CamRunning:   camRunning,
	})
}

func (f *mediaFeature) clockEvent() clockOffsetEvent {
	q := f.clock.Quality()
	return clockOffsetEvent{NowNs: f.clock.Now(), Tier: string(q.Tier), Locked: q.Locked}
}

// ── bus bridge (loop-free, split by the Local flag) ──

// bridgeUp forwards child-produced (Local) negotiation events up to the daemon, which republishes
// them on the mesh bus. Remote (down-injected) events have Local=false and are ignored here.
func (f *mediaFeature) bridgeUp() {
	for _, topic := range mediaBusTopics {
		tp := topic
		unsub := f.bus.Subscribe(tp, func(ev eventbus.Event) {
			if !ev.Local {
				return
			}
			f.rt.Emit(evMediaBusUp, busEvent{Topic: tp, Data: ev.Data})
		})
		f.upUnsub = append(f.upUnsub, unsub)
	}
}

// injectBus delivers a daemon-forwarded REMOTE event into the child bus via Inbound, with a synthetic
// monotonic per-origin seq (the daemon already deduped/ordered, so injection always Accepts).
func (f *mediaFeature) injectBus(ev busEvent) {
	if ev.Local || ev.Origin == "" {
		return
	}
	f.seqMu.Lock()
	f.seqByOrigin[ev.Origin]++
	seq := f.seqByOrigin[ev.Origin]
	f.seqMu.Unlock()
	payload, err := json.Marshal(eventbus.Envelope{Topic: ev.Topic, Origin: ev.Origin, Epoch: 1, Seq: seq, Data: ev.Data})
	if err != nil {
		return
	}
	f.bus.Inbound(ev.Origin, payload)
}

// mediaBusAdapter satisfies medialink.Bus over the child's eventbus (event-type conversion only).
type mediaBusAdapter struct{ b *eventbus.Bus }

func (m mediaBusAdapter) Publish(topic string, data json.RawMessage) { m.b.Publish(topic, data) }
func (m mediaBusAdapter) Subscribe(topic string, fn func(medialink.Event)) func() {
	return m.b.Subscribe(topic, func(ev eventbus.Event) {
		fn(medialink.Event{Origin: ev.Origin, Local: ev.Local, Data: ev.Data})
	})
}

// mediaSecretStore is the child's SecretProvider, seeded at spawn + updated per-peer on connect/drop.
// It also carries the enc-mode metadata (Addr/EncMode) so the child evaluates the media-plane
// auto-encryption decision locally (PeerAddr + MediaEncMode, consulted by the medialink router).
type mediaSecretStore struct {
	mu sync.Mutex
	m  map[string]peerMeta
}

type peerMeta struct {
	secret  []byte
	addr    string
	encMode string
}

func newMediaSecretStore(seed []peerSecret) *mediaSecretStore {
	s := &mediaSecretStore{m: map[string]peerMeta{}}
	for _, ps := range seed {
		if ps.Secret != nil {
			s.m[ps.Node] = peerMeta{secret: ps.Secret, addr: ps.Addr, encMode: ps.EncMode}
		}
	}
	return s
}

// set upserts (secret!=nil) or drops (secret==nil) one peer's media key + enc metadata.
func (s *mediaSecretStore) set(ps peerSecret) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ps.Secret == nil {
		delete(s.m, ps.Node)
		return
	}
	s.m[ps.Node] = peerMeta{secret: ps.Secret, addr: ps.Addr, encMode: ps.EncMode}
}

func (s *mediaSecretStore) MediaSecret(node string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.m[node]
	return p.secret, ok
}

// PeerAddr yields a peer's link address (same-subnet input for the enc=auto decision).
func (s *mediaSecretStore) PeerAddr(node string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.m[node]
	if !ok || p.addr == "" {
		return "", false
	}
	return p.addr, true
}

// MediaEncMode yields a peer's three-state media-plane enc preference ("" → "auto").
func (s *mediaSecretStore) MediaEncMode(node string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.m[node]; ok && p.encMode != "" {
		return p.encMode
	}
	return "auto"
}

// childMemSoftPct is how much of the child's hard job-object RAM cap the Go soft limit sits at. The
// job cap KILLS the process; GOMEMLIMIT only makes the GC work harder - so the soft limit must bite
// first and give a frame-buffer runaway a chance to be collected instead of respawning the plane.
const childMemSoftPct = 80

// setChildMemoryLimit gives the media child its own Go soft memory limit, below the hard job-object
// cap the parent applied (daemon precedent: app.setMemoryLimitGuard). capMB<=0 leaves the runtime
// default; an explicit GOMEMLIMIT from the operator always wins.
func setChildMemoryLimit(capMB int, rt *Runtime) {
	if capMB <= 0 || os.Getenv("GOMEMLIMIT") != "" {
		return
	}
	soft := int64(capMB) * childMemSoftPct / 100 * 1024 * 1024
	debug.SetMemoryLimit(soft)
	if rt != nil && rt.Log != nil {
		rt.Log.Info("media", "child memory limit set", map[string]any{"softMb": soft >> 20, "hardCapMb": capMB})
	}
}

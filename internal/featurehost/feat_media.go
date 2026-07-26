package featurehost

import (
	"context"
	"encoding/json"
	"os"
	"runtime/debug"
	"sync"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/encoderscan"
	"rave.page/mate/internal/eventbus"
	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/mediapipe"
	"rave.page/mate/internal/mediaroute"
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
	secrets *mediaSecretStore

	mu       sync.Mutex
	mediaCfg config.MediaLinkFeature
	camCfg   config.WebcamFeature

	upUnsub []func()

	seqMu       sync.Mutex
	seqByOrigin map[string]uint64 // synthetic per-origin seq for down-injected events (dedup-safe)
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
	// zigmedia inc 1: gate the zero-copy Spout->encoder capture path (default OFF). Same live
	// config as the daemon, so the isolated child behaves identically.
	mediapipe.ZeroCopyCapture = func() bool { return liveCfg().ZeroCopyCapture() }
	devSel := encoderscan.NewDeviceSelector(func() (string, string) { return liveCfg().DevicePref() }, nil)
	f.router = medialink.New(medialink.Options{
		Self: in.Self, Bus: mediaBusAdapter{f.bus}, Secrets: f.secrets, Clock: f.clock,
		Log: rt.Log, Encoder: encFac, Decoder: decFac,
		EncodeMaxHeight: in.MediaCfg.MaxHeight,
		EncodePolicy: func() (string, string) {
			c := liveCfg()
			return c.PreferCodec, c.PinnedEncoder()
		},
		EncodeDevice: func() (string, int) { d := devSel(); return d.LUID, d.Index },
	})
	if len(in.Encoders) > 0 || len(in.Decoders) > 0 {
		f.router.SetCodecCaps(in.Encoders, in.Decoders)
	}
	f.router.SetSyncPeer(in.SyncPeer)

	f.routes = mediaroute.New(mediaroute.Options{
		Log: rt.Log, Router: f.router,
		Cfg:      liveCfg,
		SameHost: nil, // the daemon proxy applies the same-host guard (peerMgr is daemon-side)
	})

	f.cam = webcam.New(rt.Log, f.bus, in.Self, in.Label, func() config.WebcamFeature {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.camCfg
	})
	f.cam.SetRouter(f.router)
	return nil
}

func (f *mediaFeature) Start(ctx context.Context) error {
	if err := f.router.Start(ctx); err != nil {
		return err
	}
	f.routes.Start(ctx)
	if err := f.cam.Start(ctx); err != nil {
		f.router.Stop()
		return err
	}
	f.bridgeUp()

	t := time.NewTicker(telemetryEvery)
	defer t.Stop()
	var lastClock clockOffsetEvent
	for {
		f.rt.Beat()
		f.emitTelemetry()
		if c := f.clockEvent(); c != lastClock {
			f.rt.Emit(evMediaClockOffset, c)
			lastClock = c
		}
		select {
		case <-ctx.Done():
			for _, u := range f.upUnsub {
				u()
			}
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
			f.secrets.set(ps.Node, ps.Secret)
		}
	case evMediaSyncPeer:
		var sp syncPeerEvent
		if json.Unmarshal(data, &sp) == nil {
			f.router.SetSyncPeer(sp.Node)
		}
	case evMediaCodecs:
		var cc codecCaps
		if json.Unmarshal(data, &cc) == nil {
			f.router.SetCodecCaps(cc.Encoders, cc.Decoders)
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
	}
	return nil, errUnknownMethod(method)
}

// ── telemetry + clock mirror (child → daemon) ──

func (f *mediaFeature) emitTelemetry() {
	cams := f.cam.Instances()
	camRunning := false
	for _, c := range cams {
		if c.Local && c.Running {
			camRunning = true
			break
		}
	}
	f.rt.Emit(evMediaTelemetry, mediaTelemetry{
		Routes:     f.router.Stats(),
		Sync:       f.router.SyncStats(),
		Clock:      f.router.ClockQuality(),
		Encoders:   f.router.Encoders(),
		Remote:     f.routes.RemoteVideoSources(),
		Receives:   f.routes.Receives(),
		Cams:       cams,
		CamRunning: camRunning,
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
type mediaSecretStore struct {
	mu sync.Mutex
	m  map[string][]byte
}

func newMediaSecretStore(seed []peerSecret) *mediaSecretStore {
	s := &mediaSecretStore{m: map[string][]byte{}}
	for _, ps := range seed {
		if ps.Secret != nil {
			s.m[ps.Node] = ps.Secret
		}
	}
	return s
}

func (s *mediaSecretStore) set(node string, secret []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if secret == nil {
		delete(s.m, node)
		return
	}
	s.m[node] = secret
}

func (s *mediaSecretStore) MediaSecret(node string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.m[node]
	return b, ok
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

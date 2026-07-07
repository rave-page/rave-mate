package featurehost

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/eventbus"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/mediaroute"
	"rave.page/mate/internal/webcam"
)

// mediaHost is the daemon-side core for the isolated media child (#44): it owns the supervising Host,
// mirrors the child's control-surface telemetry (so UI polls are local reads), slews the daemon
// mediaClock to the child's clock, and bridges the negotiation bus both ways. Three thin wrappers
// (MediaProxy / MediaRoutesProxy / WebcamProxy) expose it as the three Phase-1 interfaces - separate
// types because the interfaces' Start()/Stop() signatures collide on one struct.
type MediaHost struct {
	host  *Host
	bus   *eventbus.Bus            // daemon mesh bus (peer-connected)
	clock *medialink.SoftwareClock // daemon-side clock the TC plane reads; mirrors the child's
	same  func(peer string) bool   // same-host guard (peerMgr is daemon-side)

	mu       sync.Mutex
	appCtx   context.Context
	tel      mediaTelemetry
	camAdv   bool
	busUnsub []func()
}

// MediaHostDeps carries the daemon-side accessors the child spawn snapshot re-reads on every
// (re)spawn, so a restarted child rebuilds full desired state. Kept as funcs (not a struct) because
// the wire's mediaInit is package-private; app.go can't construct it.
type MediaHostDeps struct {
	Self, Label string
	Cfg         func() (config.MediaLinkFeature, config.WebcamFeature)
	Secrets     func() map[string][]byte // ALL currently-connected peers' media AEAD keys
	Codecs      func() (enc, dec []string)
	SyncPeer    func() string
	SameHost    func(peer string) bool // same-host guard (peerMgr is daemon-side)
	MemLimitMB  int                    // >0 caps the child's RAM (kill-on-close job)
}

// NewMediaHost builds the media child host. deps.* are re-evaluated on every (re)spawn to seed the
// child with full desired state (config + ALL current peer secrets + codec caps + sync peer). bus +
// clock stay daemon-side; the child mirrors them.
func NewMediaHost(log *logbus.Bus, bus *eventbus.Bus, clock *medialink.SoftwareClock, deps MediaHostDeps) (*MediaHost, error) {
	h := &MediaHost{bus: bus, clock: clock, same: deps.SameHost}
	host, err := New(Options{
		Name:       "media",
		Log:        log,
		MemLimitMB: deps.MemLimitMB,
		Init: func() any {
			mcfg, ccfg := deps.Cfg()
			enc, dec := deps.Codecs()
			in := mediaInit{Self: deps.Self, Label: deps.Label, MediaCfg: mcfg, CamCfg: ccfg, Encoders: enc, Decoders: dec, SyncPeer: deps.SyncPeer()}
			for node, sec := range deps.Secrets() {
				in.Secrets = append(in.Secrets, peerSecret{Node: node, Secret: sec})
			}
			return in
		},
		OnDown: h.onDown,
		OnEvent: map[string]func(json.RawMessage){
			evMediaTelemetry:   h.onTelemetry,
			evMediaClockOffset: h.onClock,
			evMediaBusUp:       h.onBusUp,
		},
	})
	if err != nil {
		return nil, err
	}
	h.host = host
	return h, nil
}

// PushSecrets sends the full current peer-secret set to a running child (call on peer connect/disconnect).
func (h *MediaHost) PushSecrets(secrets map[string][]byte) {
	for node, sec := range secrets {
		_ = h.host.Send(evMediaSecret, peerSecret{Node: node, Secret: sec})
	}
}

// Host exposes the supervisor for module Start/Stop + Stats + SetNotifier wiring.
func (h *MediaHost) Host() *Host { return h.host }

// Media / MediaRoutes / Webcam return the three interface facades.
func (h *MediaHost) Media() *MediaProxy             { return &MediaProxy{h} }
func (h *MediaHost) MediaRoutes() *MediaRoutesProxy { return &MediaRoutesProxy{h} }
func (h *MediaHost) Webcam() *WebcamProxy           { return &WebcamProxy{h} }

func (h *MediaHost) start(ctx context.Context) error {
	h.mu.Lock()
	h.appCtx = ctx
	h.mu.Unlock()
	if err := h.host.Start(ctx); err != nil {
		return err
	}
	h.subscribeBus()
	return nil
}

func (h *MediaHost) stop() {
	h.mu.Lock()
	subs := h.busUnsub
	h.busUnsub = nil
	h.mu.Unlock()
	for _, u := range subs {
		u()
	}
	h.host.Stop()
}

func (h *MediaHost) snap() mediaTelemetry {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.tel
}

// ── child → daemon ──

func (h *MediaHost) onTelemetry(data json.RawMessage) {
	var t mediaTelemetry
	if json.Unmarshal(data, &t) != nil {
		return
	}
	h.mu.Lock()
	h.tel = t
	advertise := t.CamRunning && !h.camAdv
	if advertise {
		h.camAdv = true
	}
	h.mu.Unlock()
	if advertise && h.bus != nil { // child bus can't reach peers → advertise CapCam daemon-side
		h.bus.AddCap(webcam.CapCam)
	}
}

func (h *MediaHost) onClock(data json.RawMessage) {
	var c clockOffsetEvent
	if json.Unmarshal(data, &c) != nil || h.clock == nil {
		return
	}
	h.clock.MirrorNow(c.NowNs, c.Locked)
}

// onBusUp republishes a child-produced negotiation event on the daemon mesh bus (→ peers). The daemon
// bus stamps it Origin=self/Local=true; the down-forwarder ignores Local events, so no echo loop.
func (h *MediaHost) onBusUp(data json.RawMessage) {
	var be busEvent
	if json.Unmarshal(data, &be) != nil || be.Topic == "" || h.bus == nil {
		return
	}
	h.bus.Publish(be.Topic, be.Data)
}

func (h *MediaHost) onDown() {
	h.mu.Lock()
	h.tel = mediaTelemetry{}
	h.mu.Unlock()
}

// subscribeBus forwards REMOTE negotiation events down to the child (Local events are the child's own,
// republished up - forwarding them down would loop).
func (h *MediaHost) subscribeBus() {
	if h.bus == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.busUnsub != nil {
		return
	}
	for _, topic := range mediaBusTopics {
		tp := topic
		h.busUnsub = append(h.busUnsub, h.bus.Subscribe(tp, func(ev eventbus.Event) {
			if ev.Local {
				return
			}
			_ = h.host.Send(evMediaBus, busEvent{Topic: tp, Origin: ev.Origin, Local: ev.Local, Data: ev.Data})
		}))
	}
}

// ── daemon → child (pushes + calls) ──

func (h *MediaHost) callCtx() (context.Context, context.CancelFunc) {
	h.mu.Lock()
	base := h.appCtx
	h.mu.Unlock()
	if base == nil {
		base = context.Background()
	}
	return context.WithTimeout(base, 8*time.Second)
}

// ── MediaProxy: medialink.MediaControl ──

type MediaProxy struct{ h *MediaHost }

func (p *MediaProxy) Start(ctx context.Context) error { return p.h.start(ctx) }
func (p *MediaProxy) Stop()                           { p.h.stop() }
func (p *MediaProxy) Advertise()                      { _ = p.h.host.Send(evMediaAdvert, struct{}{}) }
func (p *MediaProxy) SetCodecCaps(enc, dec []string) {
	_ = p.h.host.Send(evMediaCodecs, codecCaps{Encoders: enc, Decoders: dec})
}
func (p *MediaProxy) SetSyncPeer(node string) {
	_ = p.h.host.Send(evMediaSyncPeer, syncPeerEvent{Node: node})
}
func (p *MediaProxy) Encoders() []string { return p.h.snap().Encoders }
func (p *MediaProxy) Stats() []medialink.RouteStat {
	return p.h.snap().Routes
}
func (p *MediaProxy) SyncStats() []medialink.SyncStat      { return p.h.snap().Sync }
func (p *MediaProxy) ClockQuality() medialink.ClockQuality { return p.h.snap().Clock }

// PushSecret sends (or drops, secret==nil) one peer's media AEAD key to the running child. Call on
// peer connect/disconnect; the full set is also re-seeded via the spawn snapshot.
func (p *MediaProxy) PushSecret(node string, secret []byte) {
	_ = p.h.host.Send(evMediaSecret, peerSecret{Node: node, Secret: secret})
}

var _ medialink.MediaControl = (*MediaProxy)(nil)

// ── MediaRoutesProxy: mediaroute.ReceiveControl ──

type MediaRoutesProxy struct{ h *MediaHost }

var errSameHost = errors.New("same PC: pick the Spout sender directly - a route would re-encode for nothing (§3)")

func (p *MediaRoutesProxy) Start(ctx context.Context)                     { _ = p.h.start(ctx) }
func (p *MediaRoutesProxy) RemoteVideoSources() []mediaroute.RemoteSource { return p.h.snap().Remote }
func (p *MediaRoutesProxy) Receives() []mediaroute.Receive                { return p.h.snap().Receives }
func (p *MediaRoutesProxy) StartReceive(peer, sourceID string) (string, error) {
	if p.h.same != nil && p.h.same(peer) {
		return "", errSameHost
	}
	ctx, cancel := p.h.callCtx()
	defer cancel()
	raw, err := p.h.host.Call(ctx, mStartReceive, startReceiveReq{Peer: peer, SourceID: sourceID})
	if err != nil {
		return "", err
	}
	var r startReceiveResp
	_ = json.Unmarshal(raw, &r)
	return r.Session, nil
}
func (p *MediaRoutesProxy) StopReceive(session string) {
	ctx, cancel := p.h.callCtx()
	defer cancel()
	_, _ = p.h.host.Call(ctx, mStopReceive, stopReceiveReq{Session: session})
}

var _ mediaroute.ReceiveControl = (*MediaRoutesProxy)(nil)

// ── WebcamProxy: webcam.CamControl ──

type WebcamProxy struct{ h *MediaHost }

func (p *WebcamProxy) Start(ctx context.Context) error { return p.h.start(ctx) }
func (p *WebcamProxy) Stop()                           { p.h.stop() }
func (p *WebcamProxy) Instances() []webcam.Instance    { return p.h.snap().Cams }
func (p *WebcamProxy) Command(cmd webcam.Cmd) error {
	ctx, cancel := p.h.callCtx()
	defer cancel()
	_, err := p.h.host.Call(ctx, mCommand, commandReq{Cmd: cmd})
	return err
}

var _ webcam.CamControl = (*WebcamProxy)(nil)

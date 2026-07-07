package medialink

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"rave.page/mate/internal/procstat"
)

// router.go is the session layer (§8 P1 base + P2 telemetry/sync): it ties the peerlink control
// plane (Advert/Offer/Answer on the eventbus) to the AEAD media transport (transport.go). Flow:
//
//	requester (owns SinkID)              target (owns SourceID)
//	  Offer{session,target,source,sink} ───────────▶            (bus: TopicOffer)
//	            ◀─────────── Answer{session,addr,stream}         (bus: TopicAnswer)
//	  dial addr, send session preamble ────────────▶ accept
//	  NewConn(initiator) → runRecv(conn→Sink)        NewConn(responder) → runSend(Source→conn)
//
// The AEAD master is NEVER on the bus - both ends derive the identical MediaSecret from the peerlink
// handshake (SecretProvider). Every route is its own pairwise Conn, so a mesh stays pairwise (§2.1).
// Sources/Sinks are attached by later phases via RegisterSource/RegisterSink; hardware backends are
// still pending. P2 additions, all gated on negotiated session caps (P1 peers see pure P1 wire):
// report meta both ways + jitter/latency accounting (telemetry.go), clock-sync probes (syncclock.go),
// NACK/retransmit (nack.go), §3.2 codec-matrix pick in onOffer (codec.go). FEC stays wire-reserved.

// Bus is the control-plane pub/sub medialink negotiates over (the eventbus). Publish emits a JSON
// payload on a topic to the mesh; Subscribe delivers matching events (fn runs on the publisher
// goroutine - keep it quick). Adapted from *eventbus.Bus by the app.
type Bus interface {
	Publish(topic string, data json.RawMessage)
	Subscribe(topic string, fn func(ev Event)) func()
}

// Event is one bus delivery: Origin = producer node id, Local = produced on this node.
type Event struct {
	Origin string
	Local  bool
	Data   json.RawMessage
}

// SecretProvider yields the per-peer medialink AEAD master (peerlink.Manager.MediaSecret). Second
// return false when there's no live, connected link to the peer.
type SecretProvider interface {
	MediaSecret(nodeID string) ([]byte, bool)
}

// Logger is the optional log sink (satisfied by *logbus.Bus). Nil = silent.
type Logger interface {
	Info(tag, msg string, fields map[string]any)
	Warn(tag, msg string, fields map[string]any)
}

const logTag = "medialink"

// mediaPortRange is the media listener's LAN port range (§8 P1). Distinct from peerlink
// (47631-47635), studio (47615-47619), and the single-instance ctl socket (47620).
var mediaPortRange = []int{47641, 47642, 47643, 47644, 47645}

const (
	maxPreamble  = 128              // session-id preamble cap (hostile-length guard)
	dialTimeout  = 5 * time.Second  // media socket dial deadline
	pendingTTL   = 30 * time.Second // an accepted offer with no dial is GC'd after this
	preambleWait = 5 * time.Second  // listener wait for the dialer's session preamble
	advertEvery  = 5 * time.Second  // periodic re-advert cadence - self-heals a missed event-driven advert

	// Aggregate media-plane guards. The plane runs in the MAIN daemon process (subprocess isolation is
	// WIP), so a runaway must never be allowed to starve the host - a raw cross-PC video firehose OOM'd
	// a machine + killed Parsec. Per-route caps aren't enough: bound the ROUTE COUNT and shed everything
	// on an RSS breach. maxRoutes covers send+recv+pending; a peer can open routes, so this is a hard cap.
	maxRoutes      = 8
	memCeilingMB   = 2048 // shed all routes + refuse new above this daemon RSS
	memRecoverMB   = 1536 // resume accepting once RSS falls back below this (hysteresis)
	memSampleEvery = 2 * time.Second
)

var errNoSource = errors.New("medialink: no such source")

// SourceOpen creates a live Source for an accepted offer (opened when the peer dials in).
type SourceOpen func(ctx context.Context, o Offer) (Source, error)

// SinkOpen creates a live Sink for an accepted answer (opened before dialing out).
type SinkOpen func(ctx context.Context, a Answer) (Sink, error)

type sourceReg struct {
	desc SourceDesc
	open SourceOpen
}
type sinkReg struct {
	desc SinkDesc
	open SinkOpen
}

// sessionCaps is the granted per-route capability set (both ends agree via Offer/Answer Caps -
// absent on either side = P1 peer = everything off).
type sessionCaps struct {
	report bool // emit + consume RFC 3550-style reports (§7)
	sync   bool // clock-sync probes (§2.3 tier 2)
	nack   bool // NACK/retransmit (§2.5)
}

// capsFromAnswer derives the granted set on the requester from the target's Answer.
func capsFromAnswer(a Answer) sessionCaps {
	c := sessionCaps{nack: a.NACK}
	if a.Caps != nil {
		c.report, c.sync = a.Caps.Report, a.Caps.Sync
	}
	return c
}

// grantCaps intersects an Offer's requested caps with local (P2) support.
func grantCaps(o Offer) sessionCaps {
	c := sessionCaps{nack: o.NACK}
	if o.Caps != nil {
		c.report, c.sync = o.Caps.Report, o.Caps.Sync
	}
	return c
}

// routeIO bundles one live route's conn + counters + granted caps. metaSeq is the stream-0 seq
// allocator shared by every goroutine that writes meta frames on this route.
type routeIO struct {
	conn    *Conn
	st      *routeStat
	caps    sessionCaps
	rebuf   *retransmitBuf // send side, nil unless nack negotiated (§2.5)
	metaSeq atomic.Uint32
}

// writeMeta seals a stream-0 meta payload with the next meta seq.
func (r *routeIO) writeMeta(payload any, pts int64) error {
	f, err := MetaFrame(payload, pts)
	if err != nil {
		return err
	}
	f.Seq = r.metaSeq.Add(1) - 1
	r.st.sent(f)
	return r.conn.WriteFrame(f)
}

// pendingAnswer is a route we accepted and are waiting to be dialed on (source side).
type pendingAnswer struct {
	peer    string // offerer node id (will dial us)
	offer   Offer
	open    SourceOpen
	srcDesc SourceDesc
	choice  *CodecChoice // §3.2 negotiated encode (nil = raw echo, no encode child)
	stream  uint16
	caps    sessionCaps
	timer   *time.Timer
}

// pendingOffer is a route we offered and are waiting for an Answer on (sink side).
type pendingOffer struct {
	target   string
	sourceID string
	sinkKind Kind
	open     SinkOpen
	timer    *time.Timer
}

type activeRoute struct {
	cancel context.CancelFunc
	conn   *Conn
	stat   *routeStat
	jb     *jitterBuffer    // recv video routes (§3.3); nil otherwise
	pipe   PipelineReporter // encode/decode child telemetry; nil = passthrough
}

// Options configures a RouteManager. Bus, Secrets, Self are required.
type Options struct {
	Self       string
	Bus        Bus
	Secrets    SecretProvider
	Clock      ClockSource // default: NewMonotonicClock()
	Log        Logger      // optional
	AdvertHost string      // host placed in Answer.Addr; default: autodetected LAN IPv4, else 127.0.0.1
	Ports      []int       // listener candidates; default mediaPortRange; []int{0} = ephemeral (tests)

	// SyncPeer pins the clock-sync master (§2.3 tier 2, D6): sync samples from this peer
	// discipline Clock when it implements DisciplinedClock. "" = measure-only (telemetry).
	// P3: the TCPlane election retargets it live via SetSyncPeer (elected TC master = sync master).
	SyncPeer string

	// §3.2 codec capability sets (probed by mediapipe; empty = no video negotiation, P1 codec
	// echo). Encoders = working video encoders on this node; Decoders = decodable codecs.
	// SetCodecCaps feeds async probe results after Start.
	Encoders []string
	Decoders []string

	// P4 encode/decode children (mediapipe factories). nil = passthrough (raw frames on the
	// wire, P1–P3 behaviour) - negotiation then never picks a compressed codec because
	// Encoders/Decoders should also be empty.
	Encoder EncoderFactory
	Decoder DecoderFactory
}

// RouteManager owns the media listener + negotiation. Create with New, attach sources/sinks, then
// Start. Safe for concurrent use.
type RouteManager struct {
	self    string
	bus     Bus
	secrets SecretProvider
	clock   ClockSource
	log     Logger
	host    string
	ports   []int

	streamSeq atomic.Uint32 // stream-id allocator (starts at 1; 0 = meta)

	reportEvery time.Duration // report meta cadence (§2.1 ~1/s; shortened in tests)
	syncBurst   time.Duration // probe cadence while acquiring lock (§2.3: 8 probes / 2 s)
	syncSteady  time.Duration // probe cadence once locked (§2.3: 1 / 10 s)
	syncPeer    string        // pinned sync master ("" = telemetry only)

	syncMu    sync.Mutex
	syncPeers map[string]*OffsetEstimator // per-peer pairwise offset telemetry

	encoders []string // §3.2 probed working video encoders (advertised + drives onOffer matrix; mu)
	decoders []string // §3.2 decodable video codecs (advertised + carried in offers; mu)
	encFac   EncoderFactory
	decFac   DecoderFactory

	sampler procstat.Sampler // daemon RSS sampler for the media memory watchdog

	mu           sync.Mutex
	ln           net.Listener
	addr         string
	memTripped   bool // watchdog shed routes on an RSS breach; refuse new until recovered
	sources      map[string]sourceReg
	sinks        map[string]sinkReg
	remoteAdvert map[string]Advert // peer node id → last advert
	pendingAns   map[string]*pendingAnswer
	pendingOff   map[string]*pendingOffer
	active       map[string]*activeRoute
	unsub        []func()

	ctx    context.Context
	cancel context.CancelFunc
}

// New builds a RouteManager (does not bind - call Start).
func New(opts Options) *RouteManager {
	clk := opts.Clock
	if clk == nil {
		clk = NewMonotonicClock()
	}
	host := opts.AdvertHost
	if host == "" {
		host = localIPv4()
	}
	ports := opts.Ports
	if ports == nil {
		ports = mediaPortRange
	}
	rm := &RouteManager{
		self: opts.Self, bus: opts.Bus, secrets: opts.Secrets, clock: clk, log: opts.Log,
		host: host, ports: ports, reportEvery: time.Second,
		syncBurst: 250 * time.Millisecond, syncSteady: 10 * time.Second, syncPeer: opts.SyncPeer,
		syncPeers: map[string]*OffsetEstimator{},
		encoders:  opts.Encoders, decoders: opts.Decoders,
		encFac: opts.Encoder, decFac: opts.Decoder,
		sources: map[string]sourceReg{}, sinks: map[string]sinkReg{},
		remoteAdvert: map[string]Advert{}, pendingAns: map[string]*pendingAnswer{},
		pendingOff: map[string]*pendingOffer{}, active: map[string]*activeRoute{},
	}
	rm.streamSeq.Store(1)
	return rm
}

// RegisterSource attaches a media source (the P1 seam later phases fill: dshow audio, Spout, webcam).
func (rm *RouteManager) RegisterSource(desc SourceDesc, open SourceOpen) {
	rm.mu.Lock()
	rm.sources[desc.ID] = sourceReg{desc: desc, open: open}
	rm.mu.Unlock()
	rm.Advertise()
}

// RegisterSink attaches a media sink (audio out, Spout sender, …).
func (rm *RouteManager) RegisterSink(desc SinkDesc, open SinkOpen) {
	rm.mu.Lock()
	rm.sinks[desc.ID] = sinkReg{desc: desc, open: open}
	rm.mu.Unlock()
	rm.Advertise()
}

// UnregisterSink detaches a media sink (per-request net sinks, P4) and re-advertises.
func (rm *RouteManager) UnregisterSink(id string) {
	rm.mu.Lock()
	_, ok := rm.sinks[id]
	delete(rm.sinks, id)
	rm.mu.Unlock()
	if ok {
		rm.Advertise()
	}
}

// UnregisterSource detaches a media source (a Spout sender that vanished) and re-advertises.
func (rm *RouteManager) UnregisterSource(id string) {
	rm.mu.Lock()
	_, ok := rm.sources[id]
	delete(rm.sources, id)
	rm.mu.Unlock()
	if ok {
		rm.Advertise()
	}
}

// SetCodecCaps feeds the async §3.2 probe results (mediapipe) and re-advertises.
func (rm *RouteManager) SetCodecCaps(encoders, decoders []string) {
	rm.mu.Lock()
	rm.encoders, rm.decoders = encoders, decoders
	rm.mu.Unlock()
	rm.Advertise()
}

// Addr returns the bound media listener "host:port" (empty until Start). Uses the actual bound
// address (ephemeral-port aware for tests).
func (rm *RouteManager) Addr() string { rm.mu.Lock(); defer rm.mu.Unlock(); return rm.addr }

// Encoders returns the probed working video-encoder names (§3.2), for the encode-affinity planner.
func (rm *RouteManager) Encoders() []string {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	return append([]string(nil), rm.encoders...)
}

// Start binds the media listener, subscribes to the negotiation topics, and advertises.
func (rm *RouteManager) Start(ctx context.Context) error {
	ln, err := listenRange(rm.ports)
	if err != nil {
		return err
	}
	cctx, cancel := context.WithCancel(ctx)
	rm.mu.Lock()
	rm.ln = ln
	rm.ctx, rm.cancel = cctx, cancel
	// Advertise the bound port with the configured/auto host (ephemeral port for tests).
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	rm.addr = net.JoinHostPort(rm.host, portStr)
	rm.unsub = []func(){
		rm.bus.Subscribe(TopicOffer, rm.onOffer),
		rm.bus.Subscribe(TopicAnswer, rm.onAnswer),
		rm.bus.Subscribe(TopicAdvert, rm.onAdvert),
	}
	rm.mu.Unlock()

	go rm.acceptLoop(cctx, ln)
	go rm.advertiseLoop(cctx)
	go rm.memWatchdog(cctx)
	rm.infof("media listener", map[string]any{"addr": rm.addr})
	rm.Advertise()
	return nil
}

// advertiseLoop re-broadcasts this node's source/sink advert every advertEvery. Adverts were
// event-only (register/unregister/start/peer-connect), so a source registered before the peer
// or listener was ready - e.g. a webcam started while the far PC was reconnecting - was
// silently never seen as a receivable source. Periodic re-advert self-heals it within one
// interval, mirroring the TC-announce + webcam-status tickers that already work reliably.
func (rm *RouteManager) advertiseLoop(ctx context.Context) {
	t := time.NewTicker(advertEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			rm.Advertise()
		}
	}
}

// memWatchdog samples the daemon's own RSS and, above memCeilingMB, sheds EVERY media route + refuses
// new ones until RSS recovers below memRecoverMB. Last-resort host protection: the media plane runs in
// the main process (subprocess isolation is WIP), and a raw cross-PC video firehose OOM'd a machine +
// killed Parsec. Per-route caps + the Go memory limit bound the heap; this bounds the aggregate route
// load and guarantees the plane sheds itself before starving the host.
func (rm *RouteManager) memWatchdog(ctx context.Context) {
	t := time.NewTicker(memSampleEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		_, rss, ok := rm.sampler.CPURSS()
		if !ok {
			continue
		}
		rm.mu.Lock()
		tripped := rm.memTripped
		switch {
		case !tripped && rss >= memCeilingMB:
			rm.memTripped = true
			rm.mu.Unlock()
			n := rm.shedAllRoutes()
			rm.warnf("media RSS ceiling - shed all routes to protect the host", map[string]any{
				"rssMB": int(rss), "ceilingMB": memCeilingMB, "routesShed": n})
		case tripped && rss <= memRecoverMB:
			rm.memTripped = false
			rm.mu.Unlock()
			rm.infof("media RSS recovered - accepting routes again", map[string]any{"rssMB": int(rss)})
		default:
			rm.mu.Unlock()
		}
	}
}

// shedAllRoutes cancels + closes every active route (memory shed). Returns how many were dropped.
func (rm *RouteManager) shedAllRoutes() int {
	rm.mu.Lock()
	active := make([]*activeRoute, 0, len(rm.active))
	for _, r := range rm.active {
		active = append(active, r)
	}
	rm.active = map[string]*activeRoute{}
	rm.mu.Unlock()
	for _, r := range active {
		r.cancel()
		_ = r.conn.Close()
	}
	return len(active)
}

// admit reports whether a new route may start: not memory-tripped AND under the concurrent cap. Caller
// holds mu. Empty reason on success.
func (rm *RouteManager) admit() (bool, string) {
	if rm.memTripped {
		return false, "media routes paused (RSS ceiling) - host protection"
	}
	if n := len(rm.active) + len(rm.pendingAns) + len(rm.pendingOff); n >= maxRoutes {
		return false, fmt.Sprintf("media route cap reached (%d concurrent)", maxRoutes)
	}
	return true, ""
}

// Stop closes the listener, cancels every active route, and unsubscribes.
func (rm *RouteManager) Stop() {
	rm.mu.Lock()
	if rm.cancel != nil {
		rm.cancel()
	}
	ln := rm.ln
	unsub := rm.unsub
	active := make([]*activeRoute, 0, len(rm.active))
	for _, r := range rm.active {
		active = append(active, r)
	}
	rm.active = map[string]*activeRoute{}
	for _, p := range rm.pendingAns {
		if p.timer != nil {
			p.timer.Stop()
		}
	}
	for _, p := range rm.pendingOff {
		if p.timer != nil {
			p.timer.Stop()
		}
	}
	rm.pendingAns = map[string]*pendingAnswer{}
	rm.pendingOff = map[string]*pendingOffer{}
	rm.ln, rm.unsub = nil, nil
	rm.mu.Unlock()

	for _, u := range unsub {
		u()
	}
	for _, r := range active {
		r.cancel()
		_ = r.conn.Close()
	}
	if ln != nil {
		_ = ln.Close()
	}
}

// Advertise publishes this node's sources + sinks on TopicAdvert (no-op before Start).
func (rm *RouteManager) Advertise() {
	rm.mu.Lock()
	if rm.ln == nil {
		rm.mu.Unlock()
		return
	}
	ad := Advert{Node: rm.self,
		Caps: &Caps{Report: true, Sync: true, Clock: true, Encoders: rm.encoders, Decoders: rm.decoders}}
	for _, s := range rm.sources {
		ad.Sources = append(ad.Sources, s.desc)
	}
	for _, s := range rm.sinks {
		ad.Sinks = append(ad.Sinks, s.desc)
	}
	rm.mu.Unlock()
	if raw, err := json.Marshal(ad); err == nil {
		rm.bus.Publish(TopicAdvert, raw)
	}
}

// RemoteAdverts snapshots the last advert seen from each peer.
func (rm *RouteManager) RemoteAdverts() map[string]Advert {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	out := make(map[string]Advert, len(rm.remoteAdvert))
	for k, v := range rm.remoteAdvert {
		out[k] = v
	}
	return out
}

// OfferOptions tunes an OfferRoute call (§3.2 P4 surface).
type OfferOptions struct {
	Codec       Codec    // explicit codec (raw/audio echo); CodecNone on video = matrix negotiation
	BitrateKbps int      // video bitrate budget (Offer.Bitrate; 0 = sender default)
	Decoders    []string // per-offer decode-cap override (preferred-codec filter); nil = node set
}

// Offer requests to receive target's SourceID into local SinkID (must be a registered sink). Codec
// is the requester's choice (echoed unless the §3.2 matrix negotiates). Returns the session id.
func (rm *RouteManager) Offer(target, sourceID, sinkID string, codec Codec) (string, error) {
	return rm.OfferRoute(target, sourceID, sinkID, OfferOptions{Codec: codec})
}

// OfferRoute is Offer with the P4 options (bitrate budget + decode-cap filter).
func (rm *RouteManager) OfferRoute(target, sourceID, sinkID string, opt OfferOptions) (string, error) {
	rm.mu.Lock()
	sink, ok := rm.sinks[sinkID]
	started := rm.ln != nil
	decoders := opt.Decoders
	if decoders == nil {
		decoders = rm.decoders
	}
	admit, reason := rm.admit()
	rm.mu.Unlock()
	if !started {
		return "", errors.New("medialink: not started")
	}
	if !ok {
		return "", fmt.Errorf("medialink: no such sink %q", sinkID)
	}
	if !admit { // route cap / RSS ceiling - host protection
		return "", errors.New("medialink: " + reason)
	}
	session := newSession()
	po := &pendingOffer{target: target, sourceID: sourceID, sinkKind: sink.desc.Kind, open: sink.open}
	po.timer = time.AfterFunc(pendingTTL, func() { rm.expireOffer(session) })
	rm.mu.Lock()
	rm.pendingOff[session] = po
	rm.mu.Unlock()

	off := Offer{Session: session, Target: target, SourceID: sourceID, SinkID: sinkID, Codec: opt.Codec,
		Transport: TransportTCP, NACK: true, Bitrate: opt.BitrateKbps,
		Caps: &Caps{Report: true, Sync: true, Decoders: decoders}}
	if raw, err := json.Marshal(off); err == nil {
		rm.bus.Publish(TopicOffer, raw)
	}
	return session, nil
}

// CloseRoute tears down one active route by session id (UI stop). No-op on unknown sessions.
func (rm *RouteManager) CloseRoute(session string) {
	rm.mu.Lock()
	r := rm.active[session]
	rm.mu.Unlock()
	if r != nil {
		r.cancel()
		_ = r.conn.Close()
	}
}

// ── bus handlers ───────────────────────────────────────────────────────────────

// onOffer (source side): if we own the offered SourceID and are the target, accept + answer.
func (rm *RouteManager) onOffer(ev Event) {
	if ev.Local {
		return
	}
	var off Offer
	if json.Unmarshal(ev.Data, &off) != nil || off.Session == "" {
		return
	}
	if off.Target != "" && off.Target != rm.self {
		return
	}
	rm.mu.Lock()
	src, ok := rm.sources[off.SourceID]
	encoders := rm.encoders
	admit, reason := rm.admit()
	rm.mu.Unlock()
	if !ok {
		if off.Target == rm.self {
			rm.answer(Answer{Session: off.Session, Accept: false, Reason: errNoSource.Error()})
		}
		return
	}
	if !admit { // route cap / RSS ceiling - refuse rather than let the media plane run away (host protection)
		rm.warnf("offer refused - media guard", map[string]any{"session": off.Session, "reason": reason})
		rm.answer(Answer{Session: off.Session, Accept: false, Reason: reason})
		return
	}
	codec := off.Codec
	if codec == CodecNone {
		codec = src.desc.Codec
	}
	// §3.2 codec matrix for video routes: requester's decoders × our probed encoders, highest
	// common tier wins. No overlap / no caps = P1 echo (raw frames, no encode child).
	var choice *CodecChoice
	if src.desc.Kind == KindVideo && off.Caps != nil {
		if ch, ok := NegotiateCodec(encoders, off.Caps.Decoders); ok {
			codec, choice = ch.Codec, &ch
			if w := ch.Warning(); w != "" {
				rm.warnf("codec negotiated on a software tier", map[string]any{
					"session": off.Session, "encoder": ch.Encoder, "warning": w})
			}
		}
	}
	stream := uint16(rm.streamSeq.Add(1))
	granted := grantCaps(off)
	pa := &pendingAnswer{peer: ev.Origin, offer: off, open: src.open, srcDesc: src.desc,
		choice: choice, stream: stream, caps: granted}
	pa.timer = time.AfterFunc(pendingTTL, func() { rm.expireAnswer(off.Session) })
	rm.mu.Lock()
	rm.pendingAns[off.Session] = pa
	rm.mu.Unlock()
	addr := rm.answerAddr(ev.Origin)

	ans := Answer{Session: off.Session, Accept: true, Addr: addr, Codec: codec, Stream: stream,
		Transport: TransportTCP, NACK: granted.nack}
	if granted.report || granted.sync || choice != nil {
		ans.Caps = &Caps{Report: granted.report, Sync: granted.sync}
		if choice != nil {
			ans.Caps.Encoders = []string{choice.Encoder}
		}
	}
	rm.answer(ans)
}

// answerAddr returns the media listener "host:port" to hand a specific requesting peer: the local
// IPv4 that ROUTES toward that peer (correct across multiple NICs / subnets - a Hyper-V/WSL/VPN
// virtual adapter never wins), keeping the bound listener port. Falls back to the startup host when
// the peer's link address is unknown or resolves to loopback (same-host route, refused elsewhere).
func (rm *RouteManager) answerAddr(peer string) string {
	rm.mu.Lock()
	base := rm.addr
	rm.mu.Unlock()
	_, port, err := net.SplitHostPort(base)
	if err != nil {
		return base
	}
	ap, ok := rm.secrets.(interface {
		PeerAddr(string) (string, bool)
	})
	if !ok {
		return base
	}
	paddr, ok := ap.PeerAddr(peer)
	if !ok {
		return base
	}
	h, _, err := net.SplitHostPort(paddr)
	if err != nil || h == "" {
		return base
	}
	src := hostToward(net.JoinHostPort(h, port))
	if src == "" || src == "127.0.0.1" {
		return base
	}
	return net.JoinHostPort(src, port)
}

// onAnswer (sink side): our offer was accepted → dial the source + pump into the sink.
func (rm *RouteManager) onAnswer(ev Event) {
	if ev.Local {
		return
	}
	var ans Answer
	if json.Unmarshal(ev.Data, &ans) != nil || ans.Session == "" {
		return
	}
	rm.mu.Lock()
	po := rm.pendingOff[ans.Session]
	if po != nil {
		delete(rm.pendingOff, ans.Session)
		if po.timer != nil {
			po.timer.Stop()
		}
	}
	ctx := rm.ctx
	rm.mu.Unlock()
	if po == nil {
		return // not our offer / already handled
	}
	if !ans.Accept {
		rm.warnf("offer rejected", map[string]any{"session": ans.Session, "reason": ans.Reason})
		return
	}
	go rm.guard("dialAndReceive", func() { rm.dialAndReceive(ctx, ev.Origin, ans, po) })
}

// onAdvert records a peer's advertised sources/sinks (for the UI + routing lookups).
func (rm *RouteManager) onAdvert(ev Event) {
	if ev.Local {
		return
	}
	var ad Advert
	if json.Unmarshal(ev.Data, &ad) != nil {
		return
	}
	node := ad.Node
	if node == "" {
		node = ev.Origin
	}
	rm.mu.Lock()
	rm.remoteAdvert[node] = ad
	rm.mu.Unlock()
}

func (rm *RouteManager) answer(a Answer) {
	if raw, err := json.Marshal(a); err == nil {
		rm.bus.Publish(TopicAnswer, raw)
	}
}

// guard runs fn with panic recovery so one route's Go-level fault (bad frame, nil deref) tears down
// that route instead of crashing the whole daemon. Isolation step 1: this catches Go panics only -
// a cgo fault in a Spout Source/Sink still needs the per-route subprocess (WIP). Any deferred cleanup
// inside fn (route cancel, conn close) runs before the panic reaches this recover.
func (rm *RouteManager) guard(where string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			rm.warnf("route goroutine panic recovered (route dropped, daemon survives)",
				map[string]any{"where": where, "panic": fmt.Sprint(r)})
		}
	}()
	fn()
}

// ── transport wiring ─────────────────────────────────────────────────────────

func (rm *RouteManager) acceptLoop(ctx context.Context, ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return // listener closed
		}
		go rm.guard("serveInbound", func() { rm.serveInbound(ctx, c) })
	}
}

// serveInbound handles a dialed-in media socket (source side): read the session preamble, correlate
// to a pending answer, derive the AEAD key, open the source, and pump source→conn.
func (rm *RouteManager) serveInbound(ctx context.Context, c net.Conn) {
	setNoDelay(c)
	session, err := readPreamble(c)
	if err != nil {
		_ = c.Close()
		return
	}
	rm.mu.Lock()
	pa := rm.pendingAns[session]
	if pa != nil {
		delete(rm.pendingAns, session)
		if pa.timer != nil {
			pa.timer.Stop()
		}
	}
	rm.mu.Unlock()
	if pa == nil {
		_ = c.Close()
		return
	}
	secret, ok := rm.secrets.MediaSecret(pa.peer)
	if !ok {
		rm.warnf("no media secret for peer", map[string]any{"peer": pa.peer})
		_ = c.Close()
		return
	}
	conn, err := NewConn(c, secret, false) // accepting end = responder
	if err != nil {
		_ = c.Close()
		return
	}
	src, err := pa.open(ctx, pa.offer)
	if err != nil {
		rm.warnf("source open failed", map[string]any{"source": pa.offer.SourceID, "error": err.Error()})
		_ = conn.Close()
		return
	}
	rctx, cancel := context.WithCancel(ctx)
	// §3.2/P4: negotiated compressed video → wrap the raw source with the encode child.
	if pa.choice != nil {
		if rm.encFac == nil {
			rm.warnf("compressed codec negotiated but no encoder wired", map[string]any{"session": session})
			cancel()
			_ = src.Close()
			_ = conn.Close()
			return
		}
		spec := EncodeSpec{Encoder: pa.choice.Encoder, Codec: pa.choice.Codec, Tier: pa.choice.Tier,
			Software: pa.choice.Software, Width: pa.srcDesc.Width, Height: pa.srcDesc.Height,
			FPS: pa.srcDesc.FPS, BitrateKbps: pa.offer.Bitrate}
		esrc, err := rm.encFac(rctx, spec, src)
		if err != nil {
			rm.warnf("encode child failed", map[string]any{"session": session, "encoder": spec.Encoder, "error": err.Error()})
			cancel()
			_ = src.Close()
			_ = conn.Close()
			return
		}
		src = esrc
	}
	st := newRouteStat(session, pa.peer, pa.stream, "send")
	if pa.choice != nil {
		st.setCodec(pa.choice.Encoder, pa.choice.Tier, pa.choice.Software)
	}
	rio := &routeIO{conn: conn, st: st, caps: pa.caps}
	if pa.caps.nack {
		rio.rebuf = newRetransmitBuf(0, 0)
	}
	ar := &activeRoute{cancel: cancel, conn: conn, stat: st}
	if pr, ok := src.(PipelineReporter); ok {
		ar.pipe = pr
	}
	rm.trackRoute(session, ar)
	rm.infof("route up (send)", map[string]any{"session": session, "peer": pa.peer, "source": pa.offer.SourceID, "stream": pa.stream})
	go rm.guard("sendControl", func() { rm.sendControl(cancel, rio, src) })
	if pa.caps.report {
		go rm.guard("senderReports", func() { rm.senderReports(rctx, rio) })
	}
	err = rm.runSend(rctx, rio, src)
	_ = src.Close()
	rm.endRoute(session, conn, err, "send")
}

// sendControl is the send side's read loop: the peer's receiver reports, clock-sync pings, and
// NACKs arrive here (§7/§2.3/§2.5). Exits (cancelling the route) when the conn dies.
func (rm *RouteManager) sendControl(cancel context.CancelFunc, rio *routeIO, src Source) {
	defer cancel()
	for {
		f, err := rio.conn.ReadFrame()
		if err != nil {
			return
		}
		arrival := rm.clock.Now() // T2 for sync pings - stamp before decode work
		if f.Kind != KindMeta || f.Stream != metaStream {
			continue // a send route expects only control traffic inbound
		}
		rio.st.recvMeta(f)
		t, err := metaType(f)
		if err != nil {
			continue // unknown/garbled meta: a newer peer's extension - ignore (§2.1)
		}
		switch t {
		case MetaReport:
			if r, err := DecodeReport(f); err == nil {
				rio.st.applyRemote(r)
			}
		case MetaSync:
			if !rio.caps.sync {
				continue
			}
			if p, err := DecodeSyncPing(f); err == nil {
				pong := SyncPong{Type: MetaSyncReply, ID: p.ID, T1: p.T1, T2: arrival, T3: rm.clock.Now()}
				if rio.writeMeta(pong, pong.T3) != nil {
					return
				}
			}
		case MetaNACK:
			if !rio.caps.nack {
				continue
			}
			n, err := DecodeNACK(f)
			if err != nil {
				continue
			}
			if rio.rebuf != nil {
				// Selective retransmit (RFC 4585 semantics): re-send what the buffer still holds,
				// original Stream/Seq intact - evicted seqs stay lost (receiver counted them).
				for _, rf := range rio.rebuf.get(n.Stream, n.From, n.To) {
					if rio.conn.WriteFrame(rf) != nil {
						return
					}
					rio.st.addRetransmit()
				}
			}
			if n.FrameLevel {
				rio.st.addPLI()
				if ks, ok := src.(KeyframeSource); ok {
					ks.RequestKeyframe() // PLI-style recovery (§2.5); sources without it just resync on the next keyframe
				}
			}
		}
	}
}

// senderReports emits the RFC 3550 SR-semantic report at the negotiated cadence (§2.1 ~1/s).
func (rm *RouteManager) senderReports(ctx context.Context, rio *routeIO) {
	t := time.NewTicker(rm.reportEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r := rio.st.senderReport(time.Now().UnixNano(), rm.clock.Now())
			if rio.writeMeta(r, rm.clock.Now()) != nil {
				return
			}
		}
	}
}

// receiverReports emits the RR-semantic report (loss/jitter) back to the sender.
func (rm *RouteManager) receiverReports(ctx context.Context, rio *routeIO) {
	t := time.NewTicker(rm.reportEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r, ok := rio.st.receiverReport(time.Now().UnixNano(), rm.clock.Now())
			if !ok {
				continue // no media received yet
			}
			if rio.writeMeta(r, rm.clock.Now()) != nil {
				return
			}
		}
	}
}

// syncProbe sends clock-sync pings on a recv route: burst cadence until the peer estimate locks,
// then steady-state (§2.3: 8/2 s → 1/10 s). Pongs are handled by runReceive.
func (rm *RouteManager) syncProbe(ctx context.Context, rio *routeIO, peer string) {
	var id uint32
	for {
		interval := rm.syncBurst
		if est, ok := rm.peerSyncEstimate(peer); ok && est.Locked {
			interval = rm.syncSteady
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
		id++
		p := SyncPing{Type: MetaSync, ID: id, T1: rm.clock.Now()}
		if rio.writeMeta(p, p.T1) != nil {
			return
		}
	}
}

// onSyncPong turns a pong into an offset/RTT sample: telemetry always; clock discipline only for
// the pinned sync peer (Options.SyncPeer) on a DisciplinedClock.
func (rm *RouteManager) onSyncPong(peer string, pong SyncPong, t4 int64) {
	offset := ((pong.T2 - pong.T1) + (pong.T3 - t4)) / 2
	rtt := (t4 - pong.T1) - (pong.T3 - pong.T2)
	if rtt < 0 {
		return // clock stepped mid-probe; discard
	}
	rm.syncMu.Lock()
	e := rm.syncPeers[peer]
	if e == nil {
		e = &OffsetEstimator{}
		rm.syncPeers[peer] = e
	}
	master := rm.syncPeer
	rm.syncMu.Unlock()
	e.Add(offset, rtt, time.Now())
	if master != "" && peer == master {
		if dc, ok := rm.clock.(DisciplinedClock); ok {
			dc.AddSample(offset, rtt)
		}
	}
}

// SetSyncPeer retargets clock-sync discipline live (P3: the elected TC master becomes the sync
// master, §2.3/D6; "" = telemetry-only). Applies from the next sync sample.
func (rm *RouteManager) SetSyncPeer(nodeID string) {
	rm.syncMu.Lock()
	rm.syncPeer = nodeID
	rm.syncMu.Unlock()
}

// peerSyncEstimate returns the current filtered estimate for a peer.
func (rm *RouteManager) peerSyncEstimate(peer string) (SyncEstimate, bool) {
	rm.syncMu.Lock()
	e := rm.syncPeers[peer]
	rm.syncMu.Unlock()
	if e == nil {
		return SyncEstimate{}, false
	}
	return e.Estimate(time.Now())
}

// dialAndReceive handles the sink side: dial the answered addr, send the session preamble, derive
// the AEAD key, open the sink, and pump conn→(jitter buffer→decode child→)sink.
func (rm *RouteManager) dialAndReceive(ctx context.Context, peer string, ans Answer, po *pendingOffer) {
	secret, ok := rm.secrets.MediaSecret(peer)
	if !ok {
		rm.warnf("no media secret for peer", map[string]any{"peer": peer})
		return
	}
	d := net.Dialer{Timeout: dialTimeout}
	c, err := d.DialContext(ctx, "tcp", ans.Addr)
	if err != nil {
		rm.warnf("media dial failed", map[string]any{"addr": ans.Addr, "error": err.Error()})
		return
	}
	setNoDelay(c)
	if err := writePreamble(c, ans.Session); err != nil {
		_ = c.Close()
		return
	}
	conn, err := NewConn(c, secret, true) // dialing end = initiator
	if err != nil {
		_ = c.Close()
		return
	}
	sink, err := po.open(ctx, ans)
	if err != nil {
		rm.warnf("sink open failed", map[string]any{"session": ans.Session, "error": err.Error()})
		_ = conn.Close()
		return
	}
	rctx, cancel := context.WithCancel(ctx)
	st := newRouteStat(ans.Session, peer, ans.Stream, "recv")
	// §3.2: the answered encode choice drives the recv-side stat tier + decode spec.
	if ans.Caps != nil && len(ans.Caps.Encoders) == 1 {
		if tier, sw, ok := EncoderTier(ans.Caps.Encoders[0]); ok {
			st.setCodec(ans.Caps.Encoders[0], tier, sw)
		}
	}
	srcDesc, haveDesc := rm.remoteSourceDesc(peer, po.sourceID)
	final := sink
	if ans.Codec.CompressedVideo() {
		if rm.decFac == nil || !haveDesc || srcDesc.Width <= 0 || srcDesc.Height <= 0 {
			rm.warnf("no decode path for negotiated codec - passing compressed frames through",
				map[string]any{"session": ans.Session, "codec": ans.Codec, "haveDims": haveDesc})
		} else {
			spec := DecodeSpec{Codec: ans.Codec, Width: srcDesc.Width, Height: srcDesc.Height, FPS: srcDesc.FPS}
			dsink, err := rm.decFac(rctx, spec, sink)
			if err != nil {
				rm.warnf("decode child failed", map[string]any{"session": ans.Session, "error": err.Error()})
				cancel()
				_ = sink.Close()
				_ = conn.Close()
				return
			}
			final = dsink
		}
	}
	rio := &routeIO{conn: conn, st: st, caps: capsFromAnswer(ans)}
	ar := &activeRoute{cancel: cancel, conn: conn, stat: st}
	if pr, ok := final.(PipelineReporter); ok {
		ar.pipe = pr
	}
	// §3.3: video recv routes get the adaptive jitter buffer + keyframe-aware policy drops;
	// NACK-unrecoverable loss triggers a PLI over the control plane (§2.5).
	var jb *jitterBuffer
	if po.sinkKind == KindVideo || (haveDesc && srcDesc.Kind == KindVideo) {
		jb = newJitterBuffer(srcDesc.FPS, ans.Codec.IntraOnly())
		stream := ans.Stream
		jb.pli = func() {
			nk := NACK{Type: MetaNACK, Stream: stream, From: 1, To: 0, FrameLevel: true}
			if rio.writeMeta(nk, rm.clock.Now()) == nil {
				st.addPLI()
			}
		}
		ar.jb = jb
	}
	rm.trackRoute(ans.Session, ar)
	rm.infof("route up (recv)", map[string]any{"session": ans.Session, "peer": peer, "stream": ans.Stream,
		"codec": ans.Codec, "compressed": ans.Codec.CompressedVideo(), "decoded": final != sink})
	if rio.caps.report {
		go rm.guard("receiverReports", func() { rm.receiverReports(rctx, rio) })
	}
	if rio.caps.sync {
		go rm.guard("syncProbe", func() { rm.syncProbe(rctx, rio, peer) })
	}
	if jb != nil {
		done := make(chan struct{})
		go func() {
			defer close(done) // always unblock the waiter below, even if runJitter panics
			rm.guard("runJitter", func() { rm.runJitter(rctx, conn, jb, final, st) })
		}()
		err = rm.runReceive(rctx, rio, jbSink{jb: jb, clock: rm.clock})
		cancel()
		<-done
	} else {
		err = rm.runReceive(rctx, rio, final)
	}
	_ = final.Close()
	rm.endRoute(ans.Session, conn, err, "recv")
}

// remoteSourceDesc looks a peer's advertised source up (decode dims + fps).
func (rm *RouteManager) remoteSourceDesc(peer, sourceID string) (SourceDesc, bool) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	ad, ok := rm.remoteAdvert[peer]
	if !ok {
		return SourceDesc{}, false
	}
	for _, s := range ad.Sources {
		if s.ID == sourceID {
			return s, true
		}
	}
	return SourceDesc{}, false
}

// jbSink feeds the recv loop into the jitter buffer (arrival = media clock at read).
type jbSink struct {
	jb    *jitterBuffer
	clock ClockSource
}

func (s jbSink) Write(f *Frame) error { s.jb.push(f, s.clock.Now()); return nil }
func (s jbSink) Close() error         { return nil }

// runJitter drains the §3.3 buffer into the sink at release pace. A sink error tears the route
// down (conn close unblocks the recv loop).
func (rm *RouteManager) runJitter(ctx context.Context, conn *Conn, jb *jitterBuffer, sink Sink, st *routeStat) {
	for {
		if ctx.Err() != nil {
			return
		}
		f, wake := jb.pop(rm.clock.Now())
		if f != nil {
			if err := sink.Write(f); err != nil {
				rm.warnf("video sink write failed", map[string]any{"session": st.session, "error": err.Error()})
				_ = conn.Close()
				return
			}
			continue
		}
		if wake == 0 { // empty - wait for a push
			select {
			case <-ctx.Done():
				return
			case <-jb.notify:
			}
			continue
		}
		wait := time.Duration(wake - rm.clock.Now())
		if wait <= 0 {
			continue
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-jb.notify:
			t.Stop()
		case <-t.C:
		}
	}
}

// runSend stamps + writes source frames to the peer. Seq is per-stream monotonic (media stream
// here; meta seq is owned by routeIO); PTS is filled from the media clock when the source left it
// zero.
func (rm *RouteManager) runSend(ctx context.Context, rio *routeIO, src Source) error {
	var seq uint32
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		f, err := src.Next(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		f.Stream = rio.st.stream
		f.Seq = seq
		seq++
		if f.PTS == 0 {
			f.PTS = rm.clock.Now()
		}
		if err := rio.conn.WriteFrame(f); err != nil {
			return err
		}
		rio.st.sent(f)
		if rio.rebuf != nil {
			rio.rebuf.add(f) // NACK retransmit window (§2.5)
		}
	}
}

// runReceive reads peer frames into the sink. Stream-0 meta frames are control traffic (reports,
// sync replies) - consumed here, never delivered to the media sink. Media frames feed the §7
// seq/jitter/latency accounting.
func (rm *RouteManager) runReceive(ctx context.Context, rio *routeIO, sink Sink) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		f, err := rio.conn.ReadFrame()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		arrival := rm.clock.Now() // T4 for sync pongs / arrival for jitter+latency
		if f.Kind == KindMeta && f.Stream == metaStream {
			rio.st.recvMeta(f)
			switch t, err := metaType(f); {
			case err != nil:
				// unknown/garbled meta: a newer peer's extension - ignore (§2.1)
			case t == MetaReport:
				if r, err := DecodeReport(f); err == nil {
					rio.st.applyRemote(r)
				}
			case t == MetaSyncReply:
				if pong, err := DecodeSyncPong(f); err == nil {
					rm.onSyncPong(rio.st.peer, pong, arrival)
				}
			}
			continue
		}
		if gap := rio.st.recvMedia(f, arrival); gap.Gapped && rio.caps.nack {
			// Request retransmit of the missing range (RFC 4585 semantics). A write failure here
			// means the conn is dying - the next ReadFrame surfaces it.
			nk := NACK{Type: MetaNACK, Stream: f.Stream, From: gap.From, To: gap.To}
			if rio.writeMeta(nk, arrival) == nil {
				rio.st.addNACKSent()
			}
		}
		if err := sink.Write(f); err != nil {
			return err
		}
	}
}

func (rm *RouteManager) trackRoute(session string, r *activeRoute) {
	rm.mu.Lock()
	if old := rm.active[session]; old != nil {
		old.cancel()
		_ = old.conn.Close()
	}
	rm.active[session] = r
	rm.mu.Unlock()
}

func (rm *RouteManager) endRoute(session string, conn *Conn, err error, dir string) {
	rm.mu.Lock()
	if r := rm.active[session]; r != nil && r.conn == conn {
		delete(rm.active, session)
	}
	rm.mu.Unlock()
	_ = conn.Close()
	if err != nil && !errors.Is(err, context.Canceled) {
		rm.warnf("route ended", map[string]any{"session": session, "dir": dir, "error": err.Error()})
	} else {
		rm.infof("route ended", map[string]any{"session": session, "dir": dir})
	}
}

func (rm *RouteManager) expireOffer(session string) {
	rm.mu.Lock()
	delete(rm.pendingOff, session)
	rm.mu.Unlock()
}

func (rm *RouteManager) expireAnswer(session string) {
	rm.mu.Lock()
	delete(rm.pendingAns, session)
	rm.mu.Unlock()
}

// ── telemetry (§7; counters live in telemetry.go) ─────────────────────────────

// Stats snapshots every active route (drives the Peers → Media telemetry panel, §7).
func (rm *RouteManager) Stats() []RouteStat {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	out := make([]RouteStat, 0, len(rm.active))
	for _, r := range rm.active {
		s := r.stat.snapshot()
		if r.jb != nil {
			js := r.jb.stats()
			s.JB = &js
		}
		if r.pipe != nil {
			ps := r.pipe.PipeStats()
			s.Pipe = &ps
		}
		out = append(out, s)
	}
	return out
}

// SyncStat is one peer's pairwise clock-sync telemetry (§2.3/§7).
type SyncStat struct {
	Peer string
	SyncEstimate
}

// SyncStats snapshots the filtered offset estimate per probed peer.
func (rm *RouteManager) SyncStats() []SyncStat {
	rm.syncMu.Lock()
	peers := make(map[string]*OffsetEstimator, len(rm.syncPeers))
	for k, v := range rm.syncPeers {
		peers[k] = v
	}
	rm.syncMu.Unlock()
	now := time.Now()
	out := make([]SyncStat, 0, len(peers))
	for peer, e := range peers {
		if est, ok := e.Estimate(now); ok {
			out = append(out, SyncStat{Peer: peer, SyncEstimate: est})
		}
	}
	return out
}

// ClockQuality reports the media clock's active tier + lock state (gates the timecode plane, §2.3).
func (rm *RouteManager) ClockQuality() ClockQuality { return rm.clock.Quality() }

// ── helpers ──────────────────────────────────────────────────────────────────

func (rm *RouteManager) infof(msg string, f map[string]any) {
	if rm.log != nil {
		rm.log.Info(logTag, msg, f)
	}
}
func (rm *RouteManager) warnf(msg string, f map[string]any) {
	if rm.log != nil {
		rm.log.Warn(logTag, msg, f)
	}
}

func newSession() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// writePreamble sends the plaintext session-id correlation header ([2B len][id]). It's not secret -
// authenticity comes from the AEAD keys that follow; a wrong id just fails to correlate.
func writePreamble(w io.Writer, session string) error {
	if len(session) > maxPreamble {
		return errors.New("medialink: session id too long")
	}
	buf := make([]byte, 2+len(session))
	binary.BigEndian.PutUint16(buf, uint16(len(session)))
	copy(buf[2:], session)
	_, err := w.Write(buf)
	return err
}

// readPreamble reads the session-id correlation header with a deadline (if the conn supports one).
func readPreamble(c net.Conn) (string, error) {
	_ = c.SetReadDeadline(time.Now().Add(preambleWait))
	var lp [2]byte
	if _, err := io.ReadFull(c, lp[:]); err != nil {
		return "", err
	}
	n := binary.BigEndian.Uint16(lp[:])
	if n == 0 || n > maxPreamble {
		return "", errors.New("medialink: bad preamble length")
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(c, b); err != nil {
		return "", err
	}
	_ = c.SetReadDeadline(time.Time{}) // clear
	return string(b), nil
}

func setNoDelay(c net.Conn) {
	if tcp, ok := c.(*net.TCPConn); ok {
		_ = tcp.SetNoDelay(true) // §2.5: latency-bound the send path
	}
}

func listenRange(ports []int) (net.Listener, error) {
	var lastErr error
	for _, p := range ports {
		ln, err := net.Listen("tcp", "0.0.0.0:"+strconv.Itoa(p))
		if err == nil {
			return ln, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("medialink: no listener ports configured")
	}
	return nil, lastErr
}

// localIPv4 picks a routable LAN IPv4 for Answer.Addr; falls back to loopback.
func localIPv4() string { return hostToward("8.8.8.8:80") }

// hostToward returns the local IPv4 the OS routing table would use to reach dst. A UDP "connect"
// sends NO packet - it just fixes the local endpoint via the route lookup, so this picks the NIC
// holding the route to dst (the physical LAN adapter for a LAN peer, the default-route adapter for
// the 8.8.8.8 fallback) and SKIPS Hyper-V/WSL/VirtualBox/VPN virtual switches, which hold no route
// to dst and would otherwise win the naive "first up non-loopback iface" scan. dst is "host:port".
func hostToward(dst string) string {
	if c, err := net.Dial("udp4", dst); err == nil {
		la := c.LocalAddr()
		_ = c.Close()
		if ua, ok := la.(*net.UDPAddr); ok {
			if ip4 := ua.IP.To4(); ip4 != nil && !ip4.IsLoopback() && !ip4.IsUnspecified() {
				return ip4.String()
			}
		}
	}
	return scanIPv4() // route lookup failed (no default route / offline) - fall back to iface scan
}

// scanIPv4 is the fallback when route resolution fails: first up, non-loopback, non-virtual IPv4.
// Virtual adapters (Hyper-V/WSL/VMware/VirtualBox/VPN) are name-filtered so they never win over a
// real NIC when both are up.
func scanIPv4() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "127.0.0.1"
	}
	var virtual string // remember a virtual-adapter IP as a last resort
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipn.IP.To4()
			if ip4 == nil || ip4.IsLoopback() {
				continue
			}
			if isVirtualIface(ifc.Name) {
				if virtual == "" {
					virtual = ip4.String()
				}
				continue
			}
			return ip4.String()
		}
	}
	if virtual != "" {
		return virtual
	}
	return "127.0.0.1"
}

// isVirtualIface reports whether an interface name looks like a hypervisor/VPN virtual adapter
// (never a machine's real LAN NIC). Case-insensitive substring match on the usual suspects.
func isVirtualIface(name string) bool {
	n := strings.ToLower(name)
	for _, p := range []string{"vethernet", "hyper-v", "wsl", "vmware", "virtualbox", "vbox",
		"hamachi", "tailscale", "zerotier", "wireguard", "wg", "tun", "tap", "docker", "loopback"} {
		if strings.Contains(n, p) {
			return true
		}
	}
	return false
}

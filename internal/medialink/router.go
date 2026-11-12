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
	"sort"
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
var errNoMediaSecret = errors.New("medialink: no media secret for peer (no live link)")

// mediaConn builds the media transport for a route: AEAD (default) unless the route negotiated the
// both-peers-opted-out plaintext media plane. salt is the PER-ROUTE key salt (nil = legacy, an old
// peer that did not contribute a route nonce). Errors only when an ENCRYPTED route has no live
// peerlink secret; the plaintext path needs none.
func (rm *RouteManager) mediaConn(c io.ReadWriteCloser, peer string, salt []byte, encrypt, initiator bool) (*Conn, error) {
	if !encrypt {
		return NewPlainConn(c), nil
	}
	secret, ok := rm.secrets.MediaSecret(peer)
	if !ok {
		return nil, errNoMediaSecret
	}
	if salt == nil {
		// Pre-fix peer: legacy session-less key derivation. Safe for ONE non-restarting route to
		// this peer; with >1 concurrent route or a reconnect in one peerlink session the key repeats
		// with a counter-from-0 nonce (GCM reuse). New peers always send a route nonce (salt != nil).
		rm.warnf("AEAD media with a pre-fix peer: legacy key derivation - key reuse risk if >1 route or a reconnect to this peer",
			map[string]any{"peer": peer})
	}
	return NewConn(c, secret, salt, initiator)
}

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
	conn    frameConn // *Conn (TCP) or *udpConn (UDP datagram profile)
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
	peer      string // offerer node id (will dial us)
	offer     Offer
	open      SourceOpen
	srcDesc   SourceDesc
	choice    *CodecChoice // §3.2 negotiated encode (nil = raw echo, no encode child)
	stream    uint16
	caps      sessionCaps
	encrypt   bool   // resolved media-plane AEAD decision (default true; false = both peers opted out)
	transport string // negotiated media transport (TransportTCP/TransportUDP)
	token     uint32 // §2.5 UDP route token (demux + anti-injection); 0 on TCP
	salt      []byte // per-route key salt (routeKeySalt; nil = legacy/old-peer)
	timer     *time.Timer
}

// pendingOffer is a route we offered and are waiting for an Answer on (sink side).
type pendingOffer struct {
	target     string
	sourceID   string
	sinkID     string
	sinkKind   Kind
	open       SinkOpen
	enc        string // our advertised media-plane pref (EncAuto/EncOn/EncOff), for the answer-time decision
	local      bool   // we see the target on a directly-connected private subnet (enc=auto input)
	noUDP      bool   // this offer forced TCP (blackout re-offer) - don't fall back again
	bitrate    int    // original bitrate budget (for a TCP-fallback re-offer)
	decoders   []string
	routeNonce []byte // our ≥128-bit contribution to the per-route key salt (Offer.RouteNonce)
	timer      *time.Timer
}

type activeRoute struct {
	cancel    context.CancelFunc
	conn      frameConn
	stat      *routeStat
	peer      string           // recv-side peer (dedup key)
	sourceID  string           // recv-side source (dedup key: one recv route per peer+source)
	dir       string           // "send" | "recv"
	jb        *jitterBuffer    // recv video routes (§3.3); nil otherwise
	pipe      PipelineReporter // encode/decode child telemetry; nil = passthrough
	udp       *udpConn         // non-nil on a UDP route (transport telemetry + congestion input)
	tcpReason string           // why this route is on TCP although UDP was possible ("" = UDP route / not applicable)
}

// recvTCPReason explains, on the dialing (receive) side, why a route landed on TCP rather than the
// §2.5 UDP datagram profile - shown on the Peers/ctl route line (brief item 2). Order matters: it is
// evaluated the same way dialConn decides whether to even attempt UDP.
func recvTCPReason(udpEnabled bool, ans Answer, salt []byte) string {
	switch {
	case !udpEnabled:
		return "udp disabled locally"
	case ans.Transport != TransportUDP:
		return "peer selected tcp (no udp support)"
	case salt == nil:
		return "no per-route key (legacy peer)"
	default:
		return "udp unreachable (reachability handshake failed)"
	}
}

// sendTCPReason explains, on the answering (send) side, why a route is on TCP rather than UDP.
func sendTCPReason(udpReady bool, pa *pendingAnswer) string {
	switch {
	case !udpReady:
		return "udp disabled locally"
	case pa.salt == nil:
		return "no per-route key (legacy peer)"
	case !containsStr(pa.offer.Transports, TransportUDP):
		return "peer did not offer udp"
	case pa.transport == TransportUDP:
		return "udp unreachable (peer fell back to tcp)"
	default:
		return "negotiated tcp"
	}
}

// Options configures a RouteManager. Bus, Secrets, Self are required.
type Options struct {
	Self       string
	Bus        Bus
	Secrets    SecretProvider
	Clock      ClockSource // default: NewMonotonicClock()
	Log        Logger      // optional
	AdvertHost string      // host placed in Answer.Addr; default: autodetected LAN IPv4, else 127.0.0.1
	BindHost   string      // listener bind host; default 0.0.0.0, or AdvertHost when it is loopback
	Ports      []int       // listener candidates; default mediaPortRange; []int{0} = ephemeral (tests)

	// UDP enables the §2.5 UDP datagram media profile: offers list udp first, and a route uses UDP
	// when both peers support it AND a short reachability handshake succeeds, else it falls back to
	// the TCP path unchanged. Same port range as TCP (47641-5). Default false (TCP-only) to keep
	// older call sites + tests on the proven path; the app sets it true.
	UDP bool

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

	// EncodeMaxHeight is the encoder downscale policy (MediaLink.MaxHeight verbatim):
	// 0 = auto (native everywhere except tier-4 software encodes, capped 1080p),
	// >0 = always cap at that height, -1 = never scale.
	EncodeMaxHeight int

	// EncodePolicy supplies the SENDER-side negotiation preferences LIVE (read per offer, so a
	// settings change lands on the next route): prefer = MediaLink.PreferCodec mirrored onto the
	// send side, pin = MediaLink.Encoder. nil = no preference (pure §3.2 tier order + the
	// pipe-free preemption). See Negotiate for the precedence.
	EncodePolicy func() (prefer, pin string)

	// EncodeDevice resolves which GPU to encode on, LIVE (read per route open; the resolver is
	// TTL-cached in encoderscan). Args = the route's geometry + negotiated encoder so AUTO can run the
	// throughput-aware planner. Returns the adapter LUID key + its DXGI ordinal; ("", -1) = the
	// engine's own default device, which emits no device flags at all. nil = always default.
	EncodeDevice func(w, h, fps int, codec string) (luid string, index int)

	// VRAM congestion governor (recv side): the DJ-PC emits MetaRate backpressure to trim the
	// sender's bitrate (then fps; resolution last-resort) when primary-adapter free VRAM drops.
	// All nil/zero = no governor (fail open). Headroom returns (freeMB, present); GovernorEnabled
	// gates it live; GovernorFloorMB is the free-VRAM floor to protect (0 = default); Governor
	// BaseKbps is the route's healthy bitrate ceiling (config.Bitrate).
	Headroom         func() (freeMB uint64, present bool)
	GovernorEnabled  func() bool
	GovernorFloorMB  uint64
	GovernorBaseKbps func() int
	// Adaptive bitrate budget (auto mode): BudgetMode returns "auto"/"fixed" (""=auto) and
	// BudgetUserCeilKbps the user's bitrate ceiling (0=none). nil => fixed behaviour (GovernorBaseKbps).
	BudgetMode         func() string
	BudgetUserCeilKbps func() int
	BudgetLinkFracPct  func() int // static link-ceiling fraction (0 => DefaultLinkFracPct); nil => default
}

// RouteManager owns the media listener + negotiation. Create with New, attach sources/sinks, then
// Start. Safe for concurrent use.
type RouteManager struct {
	self     string
	bus      Bus
	secrets  SecretProvider
	clock    ClockSource
	log      Logger
	host     string
	bindHost string
	ports    []int

	streamSeq atomic.Uint32 // stream-id allocator (starts at 1; 0 = meta)

	proc *ProcRuntime // STEP-0 Go GC/scheduler pause sampler (this process = the media plane)

	reportEvery time.Duration // report meta cadence (§2.1 ~1/s; shortened in tests)
	syncBurst   time.Duration // probe cadence while acquiring lock (§2.3: 8 probes / 2 s)
	syncSteady  time.Duration // probe cadence once locked (§2.3: 1 / 10 s)
	syncPeer    string        // pinned sync master ("" = telemetry only)

	syncMu    sync.Mutex
	syncPeers map[string]*OffsetEstimator // per-peer pairwise offset telemetry

	encoders     []string // §3.2 probed working video encoders (advertised + drives onOffer matrix; mu)
	decoders     []string // §3.2 decodable video codecs (advertised + carried in offers; mu)
	encFac       EncoderFactory
	decFac       DecoderFactory
	encMaxHeight int                                                      // encoder downscale ceiling (Options.EncodeMaxHeight; 0 = native)
	encPolicy    func() (prefer, pin string)                              // sender-side codec preference + encoder pin (live)
	encDevice    func(w, h, fps int, codec string) (luid string, idx int) // sender-side encode-device resolver (live)

	headroom    func() (freeMB uint64, present bool) // recv-side VRAM governor input (nil = no governor)
	governorOn  func() bool                          // governor live enable (nil = on when headroom present)
	govFloorMB  uint64                               // free-VRAM floor the governor protects (0 = default)
	govBaseKbps func() int                           // route base bitrate ceiling (config.Bitrate / fixed mode)
	budgetMode  func() string                        // adaptive bitrate mode ("auto"/"fixed"; nil=fixed)
	budgetUserK func() int                           // adaptive user ceiling kbps (0=none)
	budgetLinkF func() int                           // static link-ceiling fraction pct (0=default)

	sampler procstat.Sampler // daemon RSS sampler for the media memory watchdog

	udp bool    // §2.5 UDP datagram profile enabled (Options.UDP)
	mux *udpMux // shared answerer UDP socket + token demux (nil until Start when udp)

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
	bind := opts.BindHost
	if bind == "" {
		bind = "0.0.0.0"
		// loopback advert = loopback-only reachability; binding loopback too keeps
		// go test binaries off the Windows Firewall listener prompt (wildcard binds
		// in per-build temp exes re-prompt every run).
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			bind = host
		}
	}
	ports := opts.Ports
	if ports == nil {
		ports = mediaPortRange
	}
	rm := &RouteManager{
		self: opts.Self, bus: opts.Bus, secrets: opts.Secrets, clock: clk, log: opts.Log,
		host: host, bindHost: bind, ports: ports, reportEvery: time.Second,
		syncBurst: 250 * time.Millisecond, syncSteady: 10 * time.Second, syncPeer: opts.SyncPeer,
		syncPeers: map[string]*OffsetEstimator{},
		encoders:  opts.Encoders, decoders: opts.Decoders,
		encFac: opts.Encoder, decFac: opts.Decoder, encMaxHeight: opts.EncodeMaxHeight,
		encPolicy: opts.EncodePolicy, encDevice: opts.EncodeDevice,
		headroom: opts.Headroom, governorOn: opts.GovernorEnabled,
		govFloorMB: opts.GovernorFloorMB, govBaseKbps: opts.GovernorBaseKbps,
		budgetMode: opts.BudgetMode, budgetUserK: opts.BudgetUserCeilKbps, budgetLinkF: opts.BudgetLinkFracPct,
		udp:     opts.UDP,
		sources: map[string]sourceReg{}, sinks: map[string]sinkReg{},
		remoteAdvert: map[string]Advert{}, pendingAns: map[string]*pendingAnswer{},
		pendingOff: map[string]*pendingOffer{}, active: map[string]*activeRoute{},
		proc: NewProcRuntime(),
	}
	rm.streamSeq.Store(1)
	return rm
}

// ProcRuntime samples this process's Go GC/scheduler pauses since the last call (STEP-0 hang
// evidence). Called at ~1 Hz by the media child's telemetry emit (child mode) or on ctl read (in-proc).
func (rm *RouteManager) ProcRuntime() ProcRuntimeSnapshot { return rm.proc.Snapshot() }

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
	// §2.5: the TCP listener + the shared answerer UDP socket share ONE port (brief req 4). For an
	// EPHEMERAL request (Ports [0], tests) the bind retries until a port is free for BOTH, so a running
	// daemon holding UDP ports (incl. 0.0.0.0 ephemeral) can't flake a loopback test.
	ln, upc, err := listenMediaSocket(rm.bindHost, rm.ports, rm.udp)
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
	if upc != nil {
		rm.mux = newUDPMux(upc, rm.serveUDPFirst)
	} else if rm.udp {
		rm.udp = false
		rm.warnf("udp media socket bind failed - routes will use TCP", nil)
	}
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
	mux := rm.mux
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
	rm.ln, rm.unsub, rm.mux = nil, nil, nil
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
	if mux != nil {
		mux.Close()
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

// LocalSources snapshots this node's currently-registered media sources (read-only; drives
// `ctl media-sources`). Stable order by ID so the listing is deterministic.
func (rm *RouteManager) LocalSources() []SourceDesc {
	rm.mu.Lock()
	out := make([]SourceDesc, 0, len(rm.sources))
	for _, s := range rm.sources {
		out = append(out, s.desc)
	}
	rm.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
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
	BitrateKbps int      // video bitrate budget/ceiling (Offer.Bitrate; 0 = sender default)
	Decoders    []string // per-offer decode-cap override (preferred-codec filter); nil = node set
	ForceTCP    bool     // offer TCP only (the §2.5 mid-route UDP-blackout fallback re-offer)
	QualityMode bool     // auto budget: ask the sender for constant-quality-with-ceiling rate control
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
	var srcDesc *SourceDesc
	if adv, ok := rm.remoteAdvert[target]; ok {
		for i := range adv.Sources {
			if adv.Sources[i].ID == sourceID {
				srcDesc = &adv.Sources[i]
				break
			}
		}
	}
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
	// Raw-video guard, requester side: with no local decoders a big video source could only
	// arrive raw (the sender refuses that) - fail at the click with the real reason instead.
	if srcDesc != nil && srcDesc.Kind == KindVideo && len(decoders) == 0 && !rawVideoOK(*srcDesc) {
		return "", errors.New("medialink: video decoders not ready (ffmpeg missing or codec probe still running) - retry in a few seconds")
	}
	session := newSession()
	enc := rm.mediaPref(target)
	local := rm.sameSubnet(target)
	offerNonce := newRouteNonce() // our ≥128-bit contribution to the per-route key salt
	po := &pendingOffer{target: target, sourceID: sourceID, sinkID: sinkID, sinkKind: sink.desc.Kind,
		open: sink.open, enc: enc, local: local, noUDP: opt.ForceTCP, bitrate: opt.BitrateKbps,
		decoders: opt.Decoders, routeNonce: offerNonce}
	po.timer = time.AfterFunc(pendingTTL, func() { rm.expireOffer(session) })
	rm.mu.Lock()
	rm.pendingOff[session] = po
	rm.mu.Unlock()

	off := Offer{Session: session, Target: target, SourceID: sourceID, SinkID: sinkID, Codec: opt.Codec,
		Transport: TransportTCP, Transports: rm.offerTransports(opt.ForceTCP), NACK: true, Bitrate: opt.BitrateKbps,
		QualityMode: opt.QualityMode,
		Enc:         enc, Local: local, RouteNonce: offerNonce, Caps: &Caps{Report: true, Sync: true, Decoders: decoders}}
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
		px := float64(src.desc.Width) * float64(src.desc.Height) * src.desc.FPS
		nopt := NegotiateOpts{PixelRate: px}
		if rm.encPolicy != nil {
			nopt.Prefer, nopt.PinEncoder = rm.encPolicy()
		}
		if ch, ok := Negotiate(encoders, off.Caps.Decoders, nopt); ok {
			codec, choice = ch.Codec, &ch
			if w := ch.Warning(); w != "" {
				rm.warnf("codec negotiated on a software tier", map[string]any{
					"session": off.Session, "encoder": ch.Encoder, "warning": w})
			}
		}
	}
	// Raw-video guard: refusing beats melting. An uncompressed route above the pixel cap
	// AES-seals + TCP-writes every full frame (~500 MB/s at 1080p60) - that pinned a source
	// PC into a hard reset once. Tiny frames stay allowed (P1 echo path + tests).
	if src.desc.Kind == KindVideo && choice == nil && !rawVideoOK(src.desc) {
		reason := "no compatible video codec between the peers - raw streaming at this size would overload the sender"
		if len(encoders) == 0 {
			reason = "video encoders not ready on the sender (ffmpeg missing or codec probe still running) - retry in a few seconds"
		}
		rm.warnf("offer refused - raw video guard", map[string]any{"session": off.Session,
			"source": off.SourceID, "w": src.desc.Width, "h": src.desc.Height, "reason": reason})
		rm.answer(Answer{Session: off.Session, Accept: false, Reason: reason})
		return
	}
	stream := uint16(rm.streamSeq.Add(1))
	granted := grantCaps(off)
	ourEnc := rm.mediaPref(ev.Origin)
	ourLocal := rm.sameSubnet(ev.Origin)
	// Per-route key salt: our fresh nonce + the offerer's (routeKeySalt returns nil for an old peer
	// that sent none → legacy derivation on TCP). This makes every route's AES-GCM keys + nonce
	// prefix + token UNIQUE, so no (key,nonce) reuse across routes/restarts/fallbacks.
	answerNonce := newRouteNonce()
	salt := routeKeySalt(off.RouteNonce, answerNonce, off.Session)
	// §2.5 transport selection: UDP only when both ends support it AND we can derive the route token
	// (needs the peerlink media secret AND a per-route salt - old peers never reach here). The dialer
	// still runs a reachability handshake; on failure it falls back to TCP on the SAME addr.
	transport := TransportTCP
	var token uint32
	if rm.udp && rm.mux != nil && salt != nil && containsStr(off.Transports, TransportUDP) {
		if sec, ok := rm.secrets.MediaSecret(ev.Origin); ok {
			transport, token = TransportUDP, routeToken(sec, salt)
		}
	}
	pa := &pendingAnswer{peer: ev.Origin, offer: off, open: src.open, srcDesc: src.desc,
		choice: choice, stream: stream, caps: granted, encrypt: resolveEnc(off.Enc, ourEnc, off.Local && ourLocal),
		transport: transport, token: token, salt: salt}
	pa.timer = time.AfterFunc(pendingTTL, func() { rm.expireAnswer(off.Session) })
	rm.mu.Lock()
	rm.pendingAns[off.Session] = pa
	rm.mu.Unlock()
	if transport == TransportUDP {
		rm.mux.expect(token, off.Session) // first AUTHENTICATED datagram for this token starts the UDP source pump
	}
	addr := rm.answerAddr(ev.Origin)

	ans := Answer{Session: off.Session, Accept: true, Addr: addr, Codec: codec, Stream: stream,
		Transport: transport, NACK: granted.nack, Enc: ourEnc, Local: ourLocal, RouteNonce: answerNonce}
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

// serveInbound handles a dialed-in TCP media socket (source side): read the session preamble, claim
// the pending answer, derive the AEAD key, and run the source pump. The UDP equivalent is
// serveUDPFirst (mux-driven); both share pumpSource.
func (rm *RouteManager) serveInbound(ctx context.Context, c net.Conn) {
	setNoDelay(c)
	session, err := readPreamble(c)
	if err != nil {
		_ = c.Close()
		return
	}
	pa := rm.claimPendingAnswer(session)
	if pa == nil {
		_ = c.Close() // not our pending answer, or already claimed by the UDP path
		return
	}
	conn, err := rm.mediaConn(c, pa.peer, pa.salt, pa.encrypt, false) // accepting end = responder
	if err != nil {
		rm.warnf("media conn setup failed", map[string]any{"peer": pa.peer, "error": err.Error()})
		_ = c.Close()
		return
	}
	rm.pumpSource(ctx, session, pa, conn, nil)
}

// peekPendingAnswer returns the pending answer for a session WITHOUT claiming it (so a UDP first
// datagram can be authenticated before the answer is consumed - finding 2).
func (rm *RouteManager) peekPendingAnswer(session string) *pendingAnswer {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	return rm.pendingAns[session]
}

// claimPendingAnswer atomically removes + returns the pending answer for a session (whichever
// transport connects first wins), stopping its TTL timer and clearing any UDP mux registration.
func (rm *RouteManager) claimPendingAnswer(session string) *pendingAnswer {
	rm.mu.Lock()
	pa := rm.pendingAns[session]
	if pa != nil {
		delete(rm.pendingAns, session)
		if pa.timer != nil {
			pa.timer.Stop()
		}
	}
	mux := rm.mux
	rm.mu.Unlock()
	if pa != nil && mux != nil && pa.transport == TransportUDP {
		mux.unexpect(pa.token)
	}
	return pa
}

// pumpSource opens the source for a claimed pending answer and pumps source→conn (encode child +
// NACK window + reports). Shared by the TCP (serveInbound) and UDP (serveUDPFirst) source sides;
// udp is the *udpConn when the route is UDP (for telemetry), nil on TCP.
func (rm *RouteManager) pumpSource(ctx context.Context, session string, pa *pendingAnswer, conn frameConn, udp *udpConn) {
	// belt-and-braces for the onOffer raw-video guard: never open capture for a raw route
	// above the pixel cap, whatever path produced the pending answer.
	if pa.choice == nil && pa.srcDesc.Kind == KindVideo && !rawVideoOK(pa.srcDesc) {
		rm.warnf("raw video route blocked at serve (guard)", map[string]any{
			"session": session, "w": pa.srcDesc.Width, "h": pa.srcDesc.Height})
		_ = conn.Close()
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
		// Downscale policy: hardware encoders take native res (4K60 is nearly free on the
		// silicon - NDI parity); only the tier-4 SOFTWARE fallback is auto-capped at 1080p.
		// Explicit user cap (>0) always applies; -1 = never scale.
		maxH := rm.encMaxHeight
		if maxH == 0 && pa.choice.Tier == 4 {
			maxH = swAutoMaxHeight
		}
		if maxH < 0 {
			maxH = 0
		}
		spec := EncodeSpec{Encoder: pa.choice.Encoder, Codec: pa.choice.Codec, Tier: pa.choice.Tier,
			Software: pa.choice.Software, Width: pa.srcDesc.Width, Height: pa.srcDesc.Height,
			FPS: pa.srcDesc.FPS, BitrateKbps: pa.offer.Bitrate, MaxHeight: maxH, DeviceIndex: -1}
		if pa.offer.QualityMode { // adaptive budget: encode constant-quality capped at BitrateKbps
			spec.QualityTarget = QualityDefault
		}
		if rm.encDevice != nil {
			spec.DeviceLUID, spec.DeviceIndex = rm.encDevice(spec.Width, spec.Height, int(spec.FPS), spec.Encoder)
		}
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
	st.setEncrypted(pa.encrypt)
	st.setTransport(transportName(udp))
	if pa.choice != nil {
		st.setCodec(pa.choice.Encoder, pa.choice.Tier, pa.choice.Software)
	}
	rio := &routeIO{conn: conn, st: st, caps: pa.caps}
	if pa.caps.nack {
		rio.rebuf = newRetransmitBuf(0, 0)
	}
	ar := &activeRoute{cancel: cancel, conn: conn, stat: st, udp: udp, dir: "send", peer: pa.peer, sourceID: pa.offer.SourceID}
	if udp == nil {
		ar.tcpReason = sendTCPReason(rm.udp && rm.mux != nil, pa) // why this send route is on TCP, not UDP (brief item 2)
	}
	if pr, ok := src.(PipelineReporter); ok {
		ar.pipe = pr
	}
	rm.trackRoute(session, ar)
	rm.dropDuplicateSend(pa.peer, pa.offer.SourceID, session) // one send route per (peer, source): newest wins, bye the old
	// Native send path (owner directive 2026-11-12): a source that can push its encoded AUs straight to
	// the native sender takes over the per-frame path; the Go media pump + Go control/report loops do
	// NOT run - the native sender owns its own socket, NACK, keepalive and liveness. The receiver rebinds
	// its remote to the native socket on the native sender's first (strictly-newest) datagram. Anything
	// unsupported (TCP, legacy peer, attach error) falls through to the Go pump unchanged.
	if tx, ok := rm.tryAttachNativeTX(pa, udp, src); ok {
		st.setTransportImpl("native")
		rm.infof("route up (send) [native tx]", map[string]any{"session": session, "peer": pa.peer,
			"source": pa.offer.SourceID, "stream": pa.stream, "transport": transportName(udp)})
		if udp != nil {
			udp.Close() // stop the Go answerer conn's media role; the native sender is the sole sender
		}
		rm.superviseNativeTX(rctx, cancel, tx, rio)
		_ = src.Close()
		rm.endRoute(session, conn, tx.Err(), "send")
		return
	}
	st.setTransportImpl("go")
	rm.infof("route up (send)", map[string]any{"session": session, "peer": pa.peer, "source": pa.offer.SourceID,
		"stream": pa.stream, "transport": transportName(udp)})
	go rm.guard("sendControl", func() { rm.sendControl(cancel, rio, src) })
	if pa.caps.report {
		go rm.guard("senderReports", func() { rm.senderReports(rctx, rio) })
	}
	err = rm.runSend(rctx, rio, src)
	_ = src.Close()
	if udp != nil {
		udp.sendBye() // §2.5: notify the receiver of a clean end so it ends now, not via a liveness blackout
	}
	rm.endRoute(session, conn, err, "send")
}

// tryAttachNativeTX hands a native-capable source's encoded AU stream to the native sender when the
// route is natively supported (UDP + per-route keys + the receiver's remote known). Returns (tx,true)
// on success; (nil,false) → the caller runs the Go pump unchanged.
func (rm *RouteManager) tryAttachNativeTX(pa *pendingAnswer, udp *udpConn, src Source) (NativeTX, bool) {
	nts, ok := src.(NativeTXSource)
	if !ok {
		return nil, false
	}
	if udp == nil || pa.salt == nil { // TCP not native yet; legacy peer has no per-route nonce
		return nil, false
	}
	raddr := udp.remoteAddr()
	if raddr == nil {
		return nil, false // receiver not bound yet
	}
	sec, ok := rm.secrets.MediaSecret(pa.peer)
	if !ok {
		return nil, false
	}
	sk, skp, rk, rkp, err := deriveRouteKeysRaw(sec, pa.salt, false) // answerer = responder
	if err != nil {
		return nil, false
	}
	codec := CodecH264
	if pa.choice != nil {
		codec = pa.choice.Codec
	}
	params := NativeTXParams{
		Transport: TransportUDP, Codec: codec, Stream: pa.stream, BodyMax: dgDefaultPayload,
		PeerAddr: raddr.String(), SocketFD: -1, Encrypt: pa.encrypt,
		SendKey: sk, SendPrefix: skp, RecvKey: rk, RecvPrefix: rkp, Token: pa.token,
		InitialSeq: udp.sendSeq(), BitrateKbps: pa.offer.Bitrate, FPS: int(pa.srcDesc.FPS),
		StartupGrace: udpDefStartupGrace,
	}
	tx, err := nts.AttachNativeTX(params)
	if err != nil {
		rm.warnf("native tx attach failed - Go pump fallback", map[string]any{"session": pa.offer.Session, "error": err.Error()})
		return nil, false
	}
	return tx, true
}

// superviseNativeTX supervises a native sender: 1 Hz stats into RouteStat, Done()/ctx → route end.
func (rm *RouteManager) superviseNativeTX(ctx context.Context, cancel context.CancelFunc, tx NativeTX, rio *routeIO) {
	defer cancel()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = tx.Close()
			return
		case <-tx.Done():
			_ = tx.Close() // native ended on its own (blackout/fatal/bye) - free it
			return
		case <-t.C:
			rio.st.setNativeStats(tx.Stats())
		}
	}
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
		case MetaRate:
			// Receiver congestion backpressure (§2.5): its VRAM governor asks us to cap output so
			// fewer bytes reach a pressured decode/publish pipeline. Apply what the encoder can;
			// a source without RateControlSource ignores it.
			if h, err := DecodeRate(f); err == nil {
				if rc, ok := src.(RateControlSource); ok {
					rc.SetRateHint(h)
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
	encrypt := resolveEnc(po.enc, ans.Enc, po.local && ans.Local)
	salt := routeKeySalt(po.routeNonce, ans.RouteNonce, ans.Session) // per-route keys (nil = old peer → legacy)
	conn, udp, err := rm.dialConn(ctx, peer, ans, salt, encrypt)     // UDP (reachable) else TCP fallback
	if err != nil {
		rm.warnf("media dial failed", map[string]any{"addr": ans.Addr, "transport": ans.Transport, "error": err.Error()})
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
	st.setEncrypted(encrypt)
	st.setTransport(transportName(udp))
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
	ar := &activeRoute{cancel: cancel, conn: conn, stat: st, udp: udp, dir: "recv", peer: peer, sourceID: po.sourceID}
	if udp == nil {
		ar.tcpReason = recvTCPReason(rm.udp, ans, salt) // why this recv route is on TCP, not UDP (brief item 2)
	}
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
	rm.dropDuplicateRecv(peer, po.sourceID, ans.Session) // one recv route per (peer, source): replace atomically
	rm.infof("route up (recv)", map[string]any{"session": ans.Session, "peer": peer, "stream": ans.Stream,
		"codec": ans.Codec, "compressed": ans.Codec.CompressedVideo(), "decoded": final != sink})
	if rio.caps.report {
		go rm.guard("receiverReports", func() { rm.receiverReports(rctx, rio) })
	}
	if rio.caps.sync {
		go rm.guard("syncProbe", func() { rm.syncProbe(rctx, rio, peer) })
	}
	// Recv-side rate backpressure (MetaRate; bitrate first, then fps). On a UDP route the §-congestion
	// controller (loss + delay-gradient) drives it, min()-combined with the VRAM governor so the two
	// compose. On a TCP route (no per-datagram loss/delay signal) only the VRAM governor runs.
	// Compressed video only.
	if ans.Codec.CompressedVideo() {
		// Adaptive budget: the governor ceiling is the quality-model target for THIS route's codec +
		// geometry (fixed mode => the configured value), following the CODED size per tick (item 2) so
		// a resolution cap lowers the bitrate and a restore raises it.
		frac := DefaultLinkFracPct
		if rm.budgetLinkF != nil {
			if f := rm.budgetLinkF(); f > 0 {
				frac = f
			}
		}
		linkCeil := rm.localLinkCeilKbps(ans.Addr, frac) // item 4: static link ceiling (0 = unknown)
		budget := rm.budgetKbpsFn(ans.Codec, srcDesc.Width, srcDesc.Height, int(srcDesc.FPS), linkCeil)
		switch {
		case udp != nil:
			go rm.guard("congestGovernor", func() { rm.congestGovernor(rctx, rio, udp, ans.Stream, budget, int(srcDesc.FPS)) })
		case rm.headroom != nil:
			go rm.guard("rateGovernor", func() { rm.rateGovernor(rctx, rio, ans.Stream, budget, int(srcDesc.FPS)) })
		}
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
	if udp != nil && errors.Is(err, errUDPEnded) {
		// §2.5: the SENDER signalled a clean end-of-stream (source EOF / route stop). End the route with
		// the real reason instead of the misleading liveness "blackout"; the receive layer re-offers on
		// its normal cadence if the source reappears.
		rm.infof("remote source ended (clean end-of-stream) - route ended",
			map[string]any{"session": ans.Session, "peer": peer})
	} else if udp != nil && errors.Is(err, errUDPDead) {
		// §2.5 mid-route UDP blackout. The route ends here; re-establishment is the RECEIVE layer's
		// job (mediaroute auto-resume re-offers via StartReceive, and dialConn's reachability probe
		// falls back to TCP if UDP is still blocked). The RouteManager must NOT re-offer directly:
		// that produced an UNTRACKED route (bypassing mediaroute's receive bookkeeping) which showed
		// up as a second live route for one source and which `route-stop all` could not find.
		rm.warnf("udp route blackout - route ended; receive layer will re-establish (TCP if UDP stays blocked)",
			map[string]any{"session": ans.Session, "peer": peer})
	}
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
func (rm *RouteManager) runJitter(ctx context.Context, conn frameConn, jb *jitterBuffer, sink Sink, st *routeStat) {
	for {
		if ctx.Err() != nil {
			return
		}
		f, wake := jb.pop(rm.clock.Now())
		if f != nil {
			st.stages.JBRelease.Mark(monoNs()) // STEP-0: jitter buffer released a frame for playout
			if err := sink.Write(f); err != nil {
				rm.warnf("video sink write failed", map[string]any{"session": st.session, "error": err.Error()})
				_ = conn.Close()
				return
			}
			st.stages.DecSubmit.Mark(monoNs()) // STEP-0: cadence of frames accepted by the decode/publish sink (backpressure)
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
// here; meta seq is owned by routeIO).
//
// PTS domain: the ROUTER owns it. A source that left PTS zero is stamped from the media clock; a
// source that stamped its own (an encode child times AUs on the wall epoch - mediapipe/mfenc
// pts0) is REBASED onto the media clock once, and the same shift rides every later frame so
// inter-frame deltas - the jitter buffer's pacing input - survive intact. Without the rebase the
// receiver's e2e latency is arrival(media clock, process-relative) − PTS(wall epoch), i.e. an
// epoch timestamp rendered as a duration ("latency 1785118072019.6 ms" in the field).
func (rm *RouteManager) runSend(ctx context.Context, rio *routeIO, src Source) error {
	var seq uint32
	var ptsShift int64
	rebased := false
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
		rio.st.stages.EncDone.Mark(monoNs()) // STEP-0: cadence of encoded AUs reaching the send loop
		f.Stream = rio.st.stream
		f.Seq = seq
		seq++
		if f.PTS == 0 {
			// Clamp ≥1: Windows interrupt-time granularity (~0.5ms) can hold Now() at 0 for
			// the first frames after clock start, and PTS==0 is the unstamped sentinel.
			if f.PTS = rm.clock.Now(); f.PTS == 0 {
				f.PTS = 1
			}
		} else {
			if !rebased {
				ptsShift, rebased = rm.clock.Now()-f.PTS, true
			}
			f.PTS += ptsShift
		}
		if err := rio.conn.WriteFrame(f); err != nil {
			return err
		}
		rio.st.stages.TxQueue.Mark(monoNs()) // STEP-0: cadence of frames packetized + queued to the pacer
		rio.st.sent(f)
		rio.retainOrRelease(f) // NACK window (§2.5) vs pooled capture buffer (see nack.go)
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
		rio.st.stages.RxComplete.Mark(monoNs()) // STEP-0: a media frame left the transport (reassembled+auth)
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

// dropDuplicateRecv closes any OTHER active recv route for the same (peer, sourceID) - the atomic
// "one route per source" guarantee on a receiver. A transport fallback/upgrade, a mediaroute
// re-offer, the §-asymmetric-reachability edge, or a UI/auto-resume race can each produce a second
// offer for a source already being received; when the new route comes up it REPLACES the old (tear
// down the loser: its goroutine, decoder + sink follow the conn close), so a receiver never runs two
// live routes + two 4K decodes into one Spout sink for one source.
func (rm *RouteManager) dropDuplicateRecv(peer, sourceID, keep string) {
	rm.mu.Lock()
	var victims []*activeRoute
	for sess, r := range rm.active {
		if sess == keep || r.dir != "recv" || r.peer != peer || r.sourceID != sourceID {
			continue
		}
		victims = append(victims, r)
		delete(rm.active, sess)
	}
	rm.mu.Unlock()
	for _, r := range victims {
		rm.warnf("replaced a duplicate recv route (one per peer+source)",
			map[string]any{"peer": peer, "source": sourceID, "keep": keep})
		r.cancel()
		_ = r.conn.Close()
	}
}

// dropDuplicateSend closes any OTHER active SEND route for the same (peer, sourceID) when a new one
// comes up - the sender's "one send route per source" guarantee (newest wins). A duplicate offer (a
// receiver start-up race, a re-offer after a blip) would otherwise leave two send routes for one
// source; the old one is ended with a BYE first, so the receiver's old route ends CLEANLY (errUDPEnded)
// and switches to the new route instead of suffering a 2s liveness blackout.
func (rm *RouteManager) dropDuplicateSend(peer, sourceID, keep string) {
	rm.mu.Lock()
	var victims []*activeRoute
	for sess, r := range rm.active {
		if sess == keep || r.dir != "send" || r.peer != peer || r.sourceID != sourceID {
			continue
		}
		victims = append(victims, r)
		delete(rm.active, sess)
	}
	rm.mu.Unlock()
	for _, r := range victims {
		rm.warnf("replaced a duplicate send route (one per peer+source) - newest wins",
			map[string]any{"peer": peer, "source": sourceID, "keep": keep})
		if r.udp != nil {
			r.udp.sendBye() // old receiver route ends cleanly + switches to the new one (no blackout)
		}
		r.cancel()
		_ = r.conn.Close()
	}
}

func (rm *RouteManager) endRoute(session string, conn frameConn, err error, dir string) {
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
	pa := rm.pendingAns[session]
	delete(rm.pendingAns, session)
	mux := rm.mux
	rm.mu.Unlock()
	if pa != nil && mux != nil && pa.transport == TransportUDP {
		mux.unexpect(pa.token)
	}
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
		if r.udp != nil {
			us := r.udp.Stats()
			s.UDP = &us
		}
		s.TCPReason = r.tcpReason
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

// newRouteNonce returns a fresh ≥128-bit per-route key-salt contribution (crypto/rand).
func newRouteNonce() []byte {
	b := make([]byte, routeNonceLen)
	_, _ = rand.Read(b)
	return b
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

// listenMediaSocket binds the TCP listener and (when udp) the shared answerer UDP socket on the SAME
// port. FIXED ports (production): a UDP-bind failure keeps the TCP listener with udp off (upc nil) -
// the plane runs over TCP, prior behaviour. EPHEMERAL (Ports contains 0, tests): retry fresh ephemeral
// ports until one is free for BOTH TCP and UDP, so a running daemon's UDP sockets never flake the gate.
func listenMediaSocket(host string, ports []int, udp bool) (net.Listener, *net.UDPConn, error) {
	ephemeral := false
	for _, p := range ports {
		if p == 0 {
			ephemeral = true
		}
	}
	tries := 1
	if ephemeral {
		tries = 16
	}
	var lastErr error
	for i := 0; i < tries; i++ {
		ln, err := listenRange(host, ports)
		if err != nil {
			lastErr = err
			continue
		}
		if !udp {
			return ln, nil, nil
		}
		tcpAddr, ok := ln.Addr().(*net.TCPAddr)
		if !ok {
			return ln, nil, nil // non-TCP listener: udp off
		}
		upc, uerr := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP(host), Port: tcpAddr.Port})
		if uerr == nil {
			return ln, upc, nil
		}
		if !ephemeral {
			return ln, nil, nil // fixed port, UDP taken: keep TCP (graceful), don't race a re-bind
		}
		_ = ln.Close() // ephemeral: this port's UDP is held elsewhere - free the TCP + try another
		lastErr = uerr
	}
	if lastErr == nil {
		lastErr = errors.New("medialink: no media socket")
	}
	return nil, nil, lastErr
}

func listenRange(host string, ports []int) (net.Listener, error) {
	var lastErr error
	for _, p := range ports {
		ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(p)))
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

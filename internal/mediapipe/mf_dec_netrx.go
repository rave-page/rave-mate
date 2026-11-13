package mediapipe

// mf_dec_netrx.go - the native decode sink takes the route's NETWORK source (medialink.NativeRXSink):
// the child's zigmedia receiver feeds the decoder at each frame's playout deadline, so on such a route
// no AU (and no frame) ever enters the Go heap. medialink decides when to hand over; this file only
// adapts the mfenc session surface.

import (
	"sync"

	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/mfenc"
)

// AttachNativeRX implements medialink.NativeRXSink.
func (m *mfDecoder) AttachNativeRX(p medialink.NativeRXParams) (medialink.NativeRX, error) {
	if !m.dec.NetCapable() {
		return nil, mfenc.ErrNetRXUnsupported
	}
	err := m.dec.AttachNet(mfenc.NetRXParams{Socket: p.Socket, Peer: p.Peer, Encrypt: p.Encrypt, Token: p.Token,
		OpenKey: p.OpenKey, OpenPrefix: p.OpenPrefix, SendKey: p.SendKey, SendPrefix: p.SendPrefix,
		BodyMax: p.BodyMax, FPS: p.FPS, Preset: p.Preset, Stream: p.Stream, InitialSeq: p.InitialSeq,
		MaxFrameBytes: p.MaxFrameBytes})
	if err != nil {
		return nil, err
	}
	n := &mfNetRX{dec: m.dec, meta: make(chan medialink.NativeRXMeta, 32), stop: make(chan struct{})}
	go n.forwardMeta()
	m.netMu.Lock()
	m.net = n
	m.netMu.Unlock()
	m.log.Info(source, "native receive attached (socket -> playout -> decode -> publish in the child)",
		map[string]any{"peer": p.Peer, "preset": p.Preset, "decoder": m.dec.Name()})
	return n, nil
}

// mfNetRX adapts an attached mfenc decode session to medialink.NativeRX.
type mfNetRX struct {
	dec      *mfenc.ProcDecSession
	meta     chan medialink.NativeRXMeta
	stop     chan struct{}
	stopOnce sync.Once
}

func (n *mfNetRX) SendMeta(js []byte) error { return n.dec.SendMeta(js) }
func (n *mfNetRX) Err() error               { return n.dec.Err() }

func (n *mfNetRX) Meta() <-chan medialink.NativeRXMeta { return n.meta }

// forwardMeta re-types the session's meta channel (control plane, a few frames/s). Bounded: drop-newest.
func (n *mfNetRX) forwardMeta() {
	src := n.dec.NetMeta()
	for {
		select {
		case <-n.stop:
			return
		case m := <-src:
			select {
			case n.meta <- medialink.NativeRXMeta{JSON: m.JSON, Age: m.Age}:
			default:
			}
		}
	}
}

func (n *mfNetRX) close() { n.stopOnce.Do(func() { close(n.stop) }) }

func (n *mfNetRX) Stats() (medialink.NativeRXStats, bool) {
	s, ok := n.dec.NetStats()
	if !ok {
		return medialink.NativeRXStats{}, false
	}
	return medialink.NativeRXStats{
		Datagrams: s.Datagrams, Delivered: s.Delivered, Published: s.Published,
		NacksSent: s.NacksSent, NackRecovered: s.NackRecovered, NackExpired: s.NackExpired,
		PLIsSent: s.PLIsSent, ReasmDrops: s.ReasmDrops, SlotDrops: s.SlotDrops,
		LossPct: s.LossPct, OwdNs: s.OwdNs, OwdJitterNs: s.OwdJitterNs, RTTns: s.RTTns,
		PlayoutMs: s.PlayoutMs, TargetMs: s.TargetMs, MaxMs: s.MaxMs,
		Misses: s.Misses, Misses10m: s.Misses10m, Repeats: s.Repeats, Repeats10m: s.Repeats10m,
		Rebases: s.Rebases, GapDrops: s.GapDrops, Grows: s.Grows, Decays: s.Decays, Buffered: s.Buffered,
		PubCount: s.PubCount, PubP50Ms: s.PubP50Ms, PubP99Ms: s.PubP99Ms, PubMaxMs: s.PubMaxMs, PubMeanMs: s.PubMeanMs,
		PubOver25: s.PubOver25, PubOver50: s.PubOver50, PubOver100: s.PubOver100,
		RelP99Ms: s.RelP99Ms, RelMaxMs: s.RelMaxMs, RelOver25: s.RelOver25,
		KeyWaits: s.KeyWaits, Rebinds: s.Rebinds, MediaBytes: s.MediaBytes, Keyframes: s.Keyframes,
		DecErrors: s.DecErrors, RxErrors: s.RxErrors, Bye: s.Bye, SinceRx: s.SinceRx,
	}, true
}

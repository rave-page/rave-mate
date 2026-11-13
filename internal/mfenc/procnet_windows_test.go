//go:build windows && cgo

package mfenc

import (
	"bufio"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// GPU-free tests of the network source (child proto v5): a null_dec session (no decoder, no
// destination) receives from the native zigmedia sender (rave-mate-txtest) over real loopback UDP,
// through the real parent ops (open/netrx/meta) and the real child. They override RAVE_MATE_ENC_EXE
// with the in-repo build so they still run when the gate hides the child from the GPU tests.

// netExes resolves the in-repo child + txtest builds (env overrides: RAVE_MATE_NETRX_ENC_EXE,
// RAVE_MATE_TXTEST_EXE). Skips when not built.
func netExes(t *testing.T) (enc, tx string) {
	t.Helper()
	enc, tx = os.Getenv("RAVE_MATE_NETRX_ENC_EXE"), os.Getenv("RAVE_MATE_TXTEST_EXE")
	if enc == "" || tx == "" {
		wd, _ := os.Getwd()
		for d := wd; ; d = filepath.Dir(d) {
			bin := filepath.Join(d, "native", "zigenc", "zig-out", "bin")
			if _, err := os.Stat(filepath.Join(bin, "rave-mate-txtest.exe")); err == nil {
				if enc == "" {
					enc = filepath.Join(bin, "rave-mate-enc.exe")
				}
				if tx == "" {
					tx = filepath.Join(bin, "rave-mate-txtest.exe")
				}
				break
			}
			if filepath.Dir(d) == d {
				t.Skip("native/zigenc not built (rave-mate-txtest.exe missing) - run zig build in native/zigenc")
			}
		}
	}
	t.Setenv("RAVE_MATE_ENC_EXE", enc)
	return enc, tx
}

// ciTimingRelaxed reports a shared CI runner (GitHub Actions / generic CI=true) where publish-cadence
// acceptance cannot be judged: a 4-core VM preempts the receiver thread. Functional asserts still bind;
// RAVE_MATE_CHAOS_STRICT=1 forces the timing verdict back on anywhere.
func ciTimingRelaxed() bool {
	if os.Getenv("RAVE_MATE_CHAOS_STRICT") == "1" {
		return false
	}
	return os.Getenv("GITHUB_ACTIONS") == "true" || os.Getenv("CI") == "true"
}

// parkUDP binds a loopback UDP socket through Go's net package (IOCP-registered, exactly like the
// route's udpConn), duplicates it into this process (the "parked" handle the session owns) and closes
// the Go handle - the production handover sequence.
func parkUDP(t *testing.T) (uintptr, int) {
	t.Helper()
	pc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	port := pc.LocalAddr().(*net.UDPAddr).Port
	rc, err := pc.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var parked windows.Handle
	var derr error
	if err := rc.Control(func(fd uintptr) {
		var info windows.WSAProtocolInfo
		if derr = windows.WSADuplicateSocket(windows.Handle(fd), windows.GetCurrentProcessId(), &info); derr != nil {
			return
		}
		parked, derr = windows.WSASocket(-1, -1, -1, &info, 0, windows.WSA_FLAG_OVERLAPPED)
	}); err != nil || derr != nil {
		t.Fatalf("park: %v %v", err, derr)
	}
	_ = pc.Close()
	return uintptr(parked), port
}

type txStats struct {
	Ev        string `json:"ev"`
	Local     string `json:"local"`
	Submitted uint64 `json:"submitted"`
	Sent      uint64 `json:"sent"`
	Dropped   uint64 `json:"dropped"`
	Retx      uint64 `json:"retx"`
	Nacks     uint64 `json:"nacks"`
	Dead      int    `json:"dead"`
	Gen       uint64 `json:"gen"`
	Br        int    `json:"br"`
	FPS       int    `json:"fps"`
	IDR       uint32 `json:"idr"`
	RelayLost uint64 `json:"relay_lost"`
}

// txProc drives one rave-mate-txtest (native sender, optional chaos relay).
type txProc struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	mu    sync.Mutex
	last  txStats
	up    chan string
	done  chan struct{}
	pipe  bool // au "pipe": stdin carries AU records
}

type netKeys struct {
	k1, k2 [32]byte // k1 = sender->receiver media, k2 = receiver->sender control
	p1, p2 uint32
	token  uint32
}

func newNetKeys(t *testing.T) netKeys {
	var b [76]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	var k netKeys
	copy(k.k1[:], b[0:32])
	copy(k.k2[:], b[32:64])
	k.p1, k.p2, k.token = binary.BigEndian.Uint32(b[64:]), binary.BigEndian.Uint32(b[68:]), binary.BigEndian.Uint32(b[72:])
	return k
}

func startTx(t *testing.T, exe string, port int, k netKeys, extra map[string]any) *txProc {
	t.Helper()
	p := map[string]any{"peer": fmt.Sprintf("127.0.0.1:%d", port), "token": k.token,
		"skey": hex.EncodeToString(k.k1[:]), "spfx": k.p1, "rkey": hex.EncodeToString(k.k2[:]), "rpfx": k.p2,
		"enc": true, "iseq": 5000, "stream": 1, "codec": 4}
	for kk, v := range extra {
		p[kk] = v
	}
	line, _ := json.Marshal(p)
	cmd := exec.Command(exe)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	tp := &txProc{cmd: cmd, stdin: stdin, up: make(chan string, 1), done: make(chan struct{}), pipe: extra["au"] == "pipe"}
	go func() {
		defer close(tp.done)
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			var ev txStats
			if json.Unmarshal(sc.Bytes(), &ev) != nil {
				continue
			}
			if ev.Ev == "up" {
				tp.up <- ev.Local
				continue
			}
			tp.mu.Lock()
			tp.last = ev
			tp.mu.Unlock()
		}
	}()
	if _, err := stdin.Write(append(line, '\n')); err != nil {
		t.Fatal(err)
	}
	select {
	case <-tp.up:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("txtest never came up")
	}
	t.Cleanup(func() { tp.quit() })
	return tp
}

func (tp *txProc) stats() txStats {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	return tp.last
}

func (tp *txProc) quit() {
	if tp.pipe {
		_, _ = tp.stdin.Write(make([]byte, 4)) // zero-length record ends pipe mode
	} else {
		_, _ = tp.stdin.Write([]byte("quit\n"))
	}
	select {
	case <-tp.done:
	case <-time.After(5 * time.Second):
		_ = tp.cmd.Process.Kill()
	}
	_ = tp.cmd.Wait()
}

func openNullNet(t *testing.T, luid int64, k netKeys, preset string) (*ProcDecSession, int) {
	t.Helper()
	d, err := OpenProcDecSession(ProcDecOpts{LUID: luid, InW: 1280, InH: 720, OutW: 1280, OutH: 720, FPS: 60,
		KbpsHint: 8000, NullDecode: true})
	if err != nil {
		t.Fatalf("null decode session: %v", err)
	}
	t.Cleanup(d.Close)
	if !d.NetCapable() {
		t.Fatal("child v5 session must carry the telemetry block")
	}
	h, port := parkUDP(t)
	err = d.AttachNet(NetRXParams{Socket: h, Encrypt: true, Token: k.token, OpenKey: k.k1, OpenPrefix: k.p1,
		SendKey: k.k2, SendPrefix: k.p2, FPS: 60, Preset: preset, Stream: 1, InitialSeq: 1000})
	if err != nil {
		_ = windows.Closesocket(windows.Handle(h))
		t.Fatalf("attach: %v", err)
	}
	return d, port
}

func waitNet(t *testing.T, d *ProcDecSession, budget time.Duration, ok func(NetRXStats) bool) NetRXStats {
	t.Helper()
	dl := time.Now().Add(budget)
	for {
		s, _ := d.NetStats()
		if ok(s) {
			return s
		}
		if time.Now().After(dl) {
			t.Fatalf("timeout; last net stats %+v", s)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestNetRXNullDecodeLoopback: native sender -> real UDP loopback -> child network source -> playout ->
// (null) publish. Frames counted, playout sane, MetaRate round trip reaches the sender, a sync ping's
// pong comes back as forwarded meta, bye observed.
func TestNetRXNullDecodeLoopback(t *testing.T) {
	_, txExe := netExes(t)
	k := newNetKeys(t)
	d, port := openNullNet(t, 0x6e6574, k, "low-latency")
	tx := startTx(t, txExe, port, k, map[string]any{"frames": 120, "au": "small"})

	s := waitNet(t, d, 6*time.Second, func(s NetRXStats) bool { return s.Published >= 120 && s.Delivered >= 120 })
	if s.Delivered < 120 || s.Datagrams < 120 || s.PubCount < 100 || s.PlayoutMs <= 0 || s.MaxMs != 60 ||
		s.KeyWaits != 0 || s.GapDrops != 0 || s.MediaBytes != 120*3000 || s.Keyframes != 2 || !s.Running {
		t.Fatalf("receive stats: %+v", s)
	}
	if s.PubMaxMs > 50 && !ciTimingRelaxed() {
		t.Errorf("publish max interval %.1f ms on a clean loopback", s.PubMaxMs)
	}

	// MetaRate through the child's control seq/nonce counter reaches the native sender.
	if err := d.SendMeta([]byte(`{"t":"rate","stream":0,"br":12000,"fps":30}`)); err != nil {
		t.Fatal(err)
	}
	dl := time.Now().Add(3 * time.Second)
	for tx.stats().Gen == 0 && time.Now().Before(dl) {
		time.Sleep(20 * time.Millisecond)
	}
	if st := tx.stats(); st.Gen == 0 || st.Br != 12000 || st.FPS != 30 {
		t.Fatalf("MetaRate did not reach the sender: %+v", st)
	}
	// Sync ping -> the sender's pong is forwarded as meta (with its arrival age).
	if err := d.SendMeta([]byte(`{"t":"sync","id":5,"t1":123}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-d.NetMeta():
		if !strings.Contains(string(m.JSON), `"syncr"`) || m.Age < 0 || m.Age > time.Second {
			t.Fatalf("forwarded meta %q age %v", m.JSON, m.Age)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no sender meta forwarded")
	}
	waitNet(t, d, 2*time.Second, func(s NetRXStats) bool {
		return s.RTTns > 0 && s.MetasSent >= 2 && s.CtlSeq > 1000 && s.SinceRx >= 0 && s.SinceRx < 600*time.Millisecond
	})
	tx.quit() // bye
	waitNet(t, d, 3*time.Second, func(s NetRXStats) bool { return s.Bye })
}

// TestNetRXAttachRefusals: an attach the child cannot honour fails cleanly and leaves the caller's
// socket ownership intact (the Go receive path keeps it).
func TestNetRXAttachRefusals(t *testing.T) {
	netExes(t)
	k := newNetKeys(t)
	d, err := OpenProcDecSession(ProcDecOpts{LUID: 0x6e6575, InW: 640, InH: 360, OutW: 640, OutH: 360, FPS: 30,
		KbpsHint: 4000, NullDecode: true})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.SendMeta([]byte(`{"t":"rate"}`)); err == nil {
		t.Fatal("meta before attach must be refused (never a second nonce space)")
	}
	h, _ := parkUDP(t)
	defer windows.Closesocket(windows.Handle(h))
	if err := d.AttachNet(NetRXParams{Socket: h, Peer: "[::1]:5000", Token: k.token}); err == nil {
		t.Fatal("IPv6 peer must be refused by the child")
	}
	if err := d.AttachNet(NetRXParams{Socket: 0}); err == nil {
		t.Fatal("no socket must be refused")
	}
	if _, ok := d.NetStats(); ok {
		t.Fatal("no stats before a successful attach")
	}
}

// TestNetRXChaosRig: the owner's "this rig" profile (receiver arrival stalls + 0.03 % loss) through the
// chaos relay into the child, 4K60-sized AUs, balanced preset: after convergence (10 s) zero publish
// intervals > 1.5 frames (25 ms), no gap drops, no PLI. ~26 s; -short skips.
func TestNetRXChaosRig(t *testing.T) {
	if testing.Short() {
		t.Skip("26 s chaos run")
	}
	_, txExe := netExes(t)
	k := newNetKeys(t)
	d, port := openNullNet(t, 0x6e6576, k, "balanced")
	// Stalls after convergence, inside the rig's 20-140 ms band (denser than 0.5-2/min on purpose).
	startTx(t, txExe, port, k, map[string]any{"frames": 25 * 60, "au": "realistic", "loss": 0.0003,
		"stalls": [][2]int{{12000, 60}, {16000, 130}, {20000, 40}, {23000, 100}}, "seed": 7})
	conv := waitNet(t, d, 15*time.Second, func(s NetRXStats) bool { return s.Published >= 10*60 })
	end := waitNet(t, d, 25*time.Second, func(s NetRXStats) bool { return s.Published >= 25*60-2 })
	t.Logf("chaos rig: published=%d playout=%.1fms target=%.1fms misses=%d repeats=%d gapDrops=%d nacks=%d recovered=%d expired=%d pli=%d publish p50/p99/max=%.1f/%.1f/%.1fms over25(conv/end)=%d/%d release p99/max=%.1f/%.1fms",
		end.Published, end.PlayoutMs, end.TargetMs, end.Misses, end.Repeats, end.GapDrops, end.NacksSent,
		end.NackRecovered, end.NackExpired, end.PLIsSent, end.PubP50Ms, end.PubP99Ms, end.PubMaxMs,
		conv.PubOver25, end.PubOver25, end.RelP99Ms, end.RelMaxMs)
	// Acceptance (RAVE_MATE_CHAOS_STRICT=1, quiet machine): zero. In the default gate the whole suite
	// runs in parallel on a shared dev PC and can preempt the receiver thread itself (release late, not
	// decode) - same slack as native/zigmedia e2e_test.zig. A shared CI VM gets no timing verdict at
	// all (functional asserts below still bind).
	slack := uint64(2)
	if os.Getenv("RAVE_MATE_CHAOS_STRICT") == "1" {
		slack = 0
	}
	if d := end.PubOver25 - conv.PubOver25; d > slack {
		if ciTimingRelaxed() {
			t.Logf("CI host: %d publish intervals > 25 ms after convergence (allowed %d on a capable machine) - timing not asserted", d, slack)
		} else {
			t.Fatalf("%d publish intervals > 25 ms after convergence (allowed %d)", d, slack)
		}
	}
	if end.GapDrops != 0 || end.PLIsSent != 0 || end.KeyWaits != 0 {
		t.Fatalf("recovery failed: gapDrops=%d pli=%d keyWaits=%d", end.GapDrops, end.PLIsSent, end.KeyWaits)
	}
}

// TestNetRXSurvivesChildCrash: the child dies mid-route (vendor-driver AV class); the supervisor
// respawns it, re-places the decode session and RE-ATTACHES the network source from the parked
// socket with the control seq continued above the last published one (one nonce space) - publishing
// resumes on the same route, no renegotiation.
func TestNetRXSurvivesChildCrash(t *testing.T) {
	_, txExe := netExes(t)
	k := newNetKeys(t)
	d, port := openNullNet(t, 0x6e6578, k, "low-latency")
	startTx(t, txExe, port, k, map[string]any{"frames": 0, "au": "small"}) // until quit
	before := waitNet(t, d, 5*time.Second, func(s NetRXStats) bool { return s.Published >= 60 })
	d.child.mu.Lock()
	proc := d.child.cmd.Process
	d.child.mu.Unlock()
	if err := proc.Kill(); err != nil {
		t.Fatal(err)
	}
	after := waitNet(t, d, 10*time.Second, func(s NetRXStats) bool {
		return s.Running && s.CtlSeq >= before.CtlSeq+netSeqMargin && s.Published >= 60
	})
	if err := d.Err(); err != nil {
		t.Fatalf("session failed instead of re-attaching: %v", err)
	}
	t.Logf("re-attached: ctlSeq %d -> %d, published after restart %d", before.CtlSeq, after.CtlSeq, after.Published)
}

// TestNetRXPipeMode validates the GPU test's harness path without a GPU: AUs streamed on txtest's stdin
// (the encoder pump format) reach the child's null-decode network session in order and cadence.
func TestNetRXPipeMode(t *testing.T) {
	_, txExe := netExes(t)
	k := newNetKeys(t)
	d, port := openNullNet(t, 0x6e657a, k, "low-latency")
	tx := startTx(t, txExe, port, k, map[string]any{"au": "pipe"})
	var hdr [16]byte
	au := make([]byte, 5000)
	t0 := time.Now()
	for i := 0; i < 90; i++ {
		binary.LittleEndian.PutUint32(hdr[0:], uint32(len(au)))
		fl := uint32(0)
		if i%30 == 0 {
			fl = 1
		}
		binary.LittleEndian.PutUint32(hdr[4:], fl)
		binary.LittleEndian.PutUint64(hdr[8:], uint64(int64(i)*16_666_667))
		au[0] = byte(i)
		if _, err := tx.stdin.Write(hdr[:]); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.stdin.Write(au); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Until(t0.Add(time.Duration(i+1) * time.Second / 60)))
	}
	s := waitNet(t, d, 5*time.Second, func(s NetRXStats) bool { return s.Published >= 90 && s.Delivered >= 90 })
	if s.KeyWaits != 0 || s.Keyframes != 3 || s.MediaBytes != 90*5000 {
		t.Fatalf("pipe mode: %+v", s)
	}
}

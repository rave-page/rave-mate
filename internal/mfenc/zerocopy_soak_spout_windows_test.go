//go:build windows && cgo && spout

package mfenc

// Single-box 4K60 soak, both arms, with the SAME real Spout sender in a second process:
//
//	arm A  zero-copy (src:"spout") - the child reads the sender's shared texture itself
//	arm B  readback  (src:"shm")   - today's path: GPU→CPU readback → Go frame → SHM frame slot
//
// THE gate is RSS flatness on arm A (the regression this increment exists to kill: a 4K60 route
// OOM-killed the media child through allocation churn over 33 MB frame buffers), plus the control
// arm's churn being visibly larger - a soak where both arms look the same is measuring nothing.
//
//	RAVE_MATE_ZIGMEDIA_SOAK=1 SOAK_SECS=60 \
//	  go test -tags spout ./internal/mfenc -run TestZeroCopy4KSoak -v -timeout 10m

import (
	"fmt"
	"image"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/videoshare"
)

const (
	soakSenderZC = "rave-mate zerocopy soak"
	soakW, soakH = 3840, 2160
)

// ── RSS sampling (any pid; psapi, no new dep) ──

var (
	soakPsapi      = syscall.NewLazyDLL("psapi.dll")
	soakGetMemInfo = soakPsapi.NewProc("GetProcessMemoryInfo")
)

type soakPMC struct {
	cb                                                                   uint32
	pageFaultCount                                                       uint32
	peakWS, ws, qPeakPaged, qPaged, qPeakNonPaged, qNonPaged, pf, peakPF uintptr
}

func rssOf(h syscall.Handle) uint64 {
	var p soakPMC
	p.cb = uint32(unsafe.Sizeof(p))
	if r, _, _ := soakGetMemInfo.Call(uintptr(h), uintptr(unsafe.Pointer(&p)), uintptr(p.cb)); r == 0 {
		return 0
	}
	return uint64(p.ws)
}

func openForQuery(pid int) (syscall.Handle, error) {
	const queryLimited = 0x1000
	return syscall.OpenProcess(queryLimited, false, uint32(pid))
}

// trend reports mean(first third) and mean(last third) of a series (MB).
func trend(s []uint64) (first, last float64) {
	if len(s) < 6 {
		return 0, 0
	}
	n := len(s) / 3
	sum := func(v []uint64) float64 {
		var t float64
		for _, x := range v {
			t += float64(x)
		}
		return t / float64(len(v)) / (1 << 20)
	}
	return sum(s[:n]), sum(s[len(s)-n:])
}

// TestZeroCopy4KSoakPublisher is the child role: a real 4K60 Spout sender.
func TestZeroCopy4KSoakPublisher(t *testing.T) {
	if os.Getenv("RAVE_ZC_ROLE") != "soaksend" {
		t.Skip("child role only (set by TestZeroCopy4KSoak)")
	}
	fs, err := videoshare.NewFrameSender(logbus.New(64), soakSenderZC)
	if err != nil {
		fmt.Fprintln(os.Stderr, "[zc-soak-send] no sender:", err)
		return
	}
	defer fs.Close()
	img := image.NewNRGBA(image.Rect(0, 0, soakW, soakH))
	for i := range img.Pix {
		img.Pix[i] = byte(i)
	}
	tk := time.NewTicker(time.Second / 60)
	defer tk.Stop()
	stop := time.After(20 * time.Minute)
	for n := 0; ; n++ {
		select {
		case <-stop:
			return
		case <-tk.C:
			img.Pix[0] = byte(n) // every frame distinct (no encoder short-circuit)
			_ = fs.Send(img)
		}
	}
}

func TestZeroCopy4KSoak(t *testing.T) {
	if os.Getenv("RAVE_MATE_ZIGMEDIA_SOAK") == "" {
		t.Skip("set RAVE_MATE_ZIGMEDIA_SOAK=1 (needs a GPU + SpoutLibrary.dll)")
	}
	if !Available() {
		t.Skip("no hardware H.264 MFT / D3D11 device")
	}
	requireEncExe(t)
	secs := 60
	if v := os.Getenv("SOAK_SECS"); v != "" {
		if k, err := strconv.Atoi(v); err == nil && k > 0 {
			secs = k
		}
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pub := exec.Command(exe, "-test.run=TestZeroCopy4KSoakPublisher", "-test.v")
	pub.Env = append(os.Environ(), "RAVE_ZC_ROLE=soaksend")
	pub.Stderr = os.Stderr
	if err := pub.Start(); err != nil {
		t.Fatalf("spawn publisher: %v", err)
	}
	defer func() { _ = pub.Process.Kill(); _, _ = pub.Process.Wait() }()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, _, w, h, ok := videoshare.SenderShare(soakSenderZC); ok && w == soakW && h == soakH {
			break
		}
		if time.Now().After(deadline) {
			t.Skip("4K publisher never registered a shared texture")
		}
		time.Sleep(200 * time.Millisecond)
	}

	zc := soakArm(t, true, secs)
	readback := soakArm(t, false, secs)

	t.Logf("SOAK 4K60 %ds  zero-copy: selfRSS %.0f→%.0f MB childRSS %.0f→%.0f MB "+
		"goAlloc %.1f MB/s aus=%d", secs, zc.selfFirst, zc.selfLast, zc.childFirst, zc.childLast,
		zc.allocMBs, zc.aus)
	t.Logf("SOAK 4K60 %ds  readback : selfRSS %.0f→%.0f MB childRSS %.0f→%.0f MB "+
		"goAlloc %.1f MB/s aus=%d", secs, readback.selfFirst, readback.selfLast,
		readback.childFirst, readback.childLast, readback.allocMBs, readback.aus)

	// THE gate: no monotone RSS trend on the zero-copy arm.
	if zc.selfLast > zc.selfFirst*1.10+8 {
		t.Errorf("zero-copy host RSS trending up: %.0f → %.0f MB", zc.selfFirst, zc.selfLast)
	}
	if zc.childLast > zc.childFirst*1.10+16 {
		t.Errorf("zero-copy encoder-child RSS trending up: %.0f → %.0f MB", zc.childFirst, zc.childLast)
	}
	// The control must show the cost this increment removes, or the measurement means nothing.
	// Note it is RESIDENCY, not allocation rate: the bounded size-keyed pixel pool already took
	// the Go churn out of the readback path (goAlloc is ~0 on both arms), so what is left to see
	// is the host frame plane itself - the pool's retained buffers plus the 33 MB SHM frame slot.
	if readback.selfFirst < zc.selfFirst*2 {
		t.Errorf("readback host RSS %.0f MB vs zero-copy %.0f MB - the arms are indistinguishable, so this soak is measuring nothing",
			readback.selfFirst, zc.selfFirst)
	}
	if readback.childFirst < zc.childFirst*1.3 {
		t.Errorf("readback child RSS %.0f MB vs zero-copy %.0f MB - the 33 MB SHM frame slot is not visible",
			readback.childFirst, zc.childFirst)
	}
	// Both arms must really have encoded at ~the paced rate (a soak on a stalled route is a
	// flatness pass for the wrong reason).
	for _, a := range []struct {
		name string
		r    soakResult
	}{{"zero-copy", zc}, {"readback", readback}} {
		if want := secs * 40; a.r.aus < want {
			t.Errorf("%s arm produced %d AUs over %ds, want >= %d (route stalled?)", a.name, a.r.aus, secs, want)
		}
	}
}

type soakResult struct {
	selfFirst, selfLast   float64
	childFirst, childLast float64
	allocMBs              float64
	aus                   int
}

// soakArm runs one arm for secs seconds and returns its RSS trend + Go allocation rate.
func soakArm(t *testing.T, zeroCopy bool, secs int) soakResult {
	t.Helper()
	opts := ProcOpts{LUID: 0, InW: soakW, InH: soakH, OutW: soakW, OutH: soakH,
		FPS: 60, Kbps: 50000, Gop: 120}
	if zeroCopy {
		opts.Spout = &SpoutSource{Name: soakSenderZC, Resolve: func() (uint64, uint32, int, int, bool) {
			return videoshare.SenderShare(soakSenderZC)
		}}
	}
	s, err := OpenProcSessionOpts(opts)
	if err != nil {
		t.Fatalf("open (zeroCopy=%v): %v", zeroCopy, err)
	}
	aus := 0
	drained := make(chan struct{})
	go func() {
		for range s.Output() {
			aus++
		}
		close(drained)
	}()

	// The readback arm needs a real capture + submit loop (that IS the path under test).
	stopFeed := make(chan struct{})
	if !zeroCopy {
		rx, err := videoshare.NewFrameReceiverOpts(logbus.New(64), soakSenderZC,
			videoshare.RecvOptions{MaxFPS: 60})
		if err != nil {
			t.Fatalf("receiver: %v", err)
		}
		go func() {
			defer rx.Close()
			for {
				select {
				case <-stopFeed:
					return
				case img, ok := <-rx.Frames():
					if !ok {
						return
					}
					_ = s.Encode(img.Pix, time.Now().UnixNano())
					videoshare.PutPix(img.Pix)
				}
			}
		}()
	}

	selfH, _ := openForQuery(os.Getpid())
	s.child.mu.Lock()
	childPid := s.child.cmd.Process.Pid
	s.child.mu.Unlock()
	childH, _ := openForQuery(childPid)
	defer func() {
		if selfH != 0 {
			_ = syscall.CloseHandle(selfH)
		}
		if childH != 0 {
			_ = syscall.CloseHandle(childH)
		}
	}()

	var m0, m1 runtime.MemStats
	time.Sleep(3 * time.Second) // warm-up: device/MFT/pool steady state before the baseline
	runtime.ReadMemStats(&m0)
	var selfS, childS []uint64
	t0 := time.Now()
	for time.Since(t0) < time.Duration(secs)*time.Second {
		time.Sleep(time.Second)
		selfS = append(selfS, rssOf(selfH))
		childS = append(childS, rssOf(childH))
	}
	runtime.ReadMemStats(&m1)
	dur := time.Since(t0).Seconds()
	close(stopFeed)
	s.Close()
	<-drained

	r := soakResult{aus: aus, allocMBs: float64(m1.TotalAlloc-m0.TotalAlloc) / (1 << 20) / dur}
	r.selfFirst, r.selfLast = trend(selfS)
	r.childFirst, r.childLast = trend(childS)
	return r
}

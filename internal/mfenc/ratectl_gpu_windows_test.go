//go:build windows && cgo

package mfenc

// Real-encoder gates for quality-mode rate control (native/zigenc mf.zig RcMode): constant quality
// capped by the ceiling vs fixed CBR, on whatever hardware MFT this box has. Rates are measured over
// MEDIA time (AU bytes / frame count / fps): the MFT's rate control runs on sample timestamps, so the
// loop may submit faster than real time without changing the result.

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"os/exec"
	"strings"
	"testing"
	"time"

	"rave.page/mate/internal/mediatools"
)

const (
	rcW, rcH = 1280, 720
	rcFPS    = 60
	rcCap    = 3000 // kbps
	// rcCapSmooth: the simple-motion case runs at a cap well above what QP 23 needs for it, so CBR
	// visibly spends the target while constant quality does not.
	rcCapSmooth = 6000
	rcQP        = 23 // medialink.QualityToQP(H.264, QualityDefault=70)
	rcQ         = 70
	// rcOvershoot: a 1 s VBV at the cap lets one window carry up to one extra second of cap; over a
	// 5 s measurement that bounds the average at 1.2x. Asserted tighter (1.15x), measured ~1.0x.
	rcOvershoot = 1.15
)

// rcMotion builds n frames of a full-detail grey texture (per-pixel random, bilinear-free) scrolling
// 6x3 px per frame: wants ~5.7 Mbps at QP 23 at 720p60 on NVENC, i.e. it BINDS a 3 Mbps cap, yet is
// motion-compensable so the cap is reachable. (Per-frame chroma grain is not: NVENC needs ~11 Mbps
// for it at QP 51, so no cap below that can hold - measured, not a rate-control failure.)
func rcMotion(n int) [][]byte {
	r := rand.New(rand.NewSource(7))
	tw, th := rcW+n*6, rcH+n*3
	tex := make([]byte, tw*th)
	for i := range tex {
		tex[i] = byte(r.Intn(256))
	}
	out := make([][]byte, n)
	for f := 0; f < n; f++ {
		px := make([]byte, rcW*rcH*4)
		ox, oy := f*6, f*3
		for y := 0; y < rcH; y++ {
			row := tex[(y+oy)*tw+ox:]
			for x := 0; x < rcW; x++ {
				p := px[(y*rcW+x)*4:]
				p[0], p[1], p[2], p[3] = row[x], row[x], row[x], 255
			}
		}
		out[f] = px
	}
	return out
}

// rcSmooth: SIMPLE motion - a soft (bilinear 8 px cells) grey texture scrolling 6x3 px per frame.
// A CBR encoder still spends its whole target on it; constant quality needs a fraction.
func rcSmooth(n int) [][]byte {
	const cell = 8
	r := rand.New(rand.NewSource(11))
	tw, th := rcW+n*6, rcH+n*3
	cw := tw/cell + 2
	coarse := make([]float64, cw*(th/cell+2))
	for i := range coarse {
		coarse[i] = float64(r.Intn(256))
	}
	tex := make([]byte, tw*th)
	for y := 0; y < th; y++ {
		iy, ay := y/cell, float64(y%cell)/cell
		for x := 0; x < tw; x++ {
			ix, ax := x/cell, float64(x%cell)/cell
			v := coarse[iy*cw+ix]*(1-ax)*(1-ay) + coarse[iy*cw+ix+1]*ax*(1-ay) +
				coarse[(iy+1)*cw+ix]*(1-ax)*ay + coarse[(iy+1)*cw+ix+1]*ax*ay
			tex[y*tw+x] = byte(v)
		}
	}
	out := make([][]byte, n)
	for f := 0; f < n; f++ {
		px := make([]byte, rcW*rcH*4)
		for y := 0; y < rcH; y++ {
			row := tex[(y+f*3)*tw+f*6:]
			for x := 0; x < rcW; x++ {
				p := px[(y*rcW+x)*4:]
				p[0], p[1], p[2], p[3] = row[x], row[x], row[x], 255
			}
		}
		out[f] = px
	}
	return out
}

// rcBands: top half red, bottom half blue (static picture, decodable oracle).
func rcBands() []byte {
	px := make([]byte, rcW*rcH*4)
	for i := 0; i < rcW*rcH; i++ {
		p := px[i*4:]
		if i/rcW < rcH/2 {
			p[0] = 255
		} else {
			p[2] = 255
		}
		p[3] = 255
	}
	return px
}

type rcRun struct {
	kbps   float64 // whole run, media time
	kbpsSS float64 // steady state: frames from 1 s on (first IDR + rate-control settle excluded)
	aus    []AU
	st     ProcStats
}

// encodeRC runs n frames through a real session. at(i) supplies frame i; hook(i, s) runs before it.
func encodeRC(t *testing.T, o ProcOpts, n int, at func(int) []byte, hook func(int, *ProcSession)) rcRun {
	t.Helper()
	s, err := OpenProcSessionOpts(o)
	if err != nil {
		t.Skipf("no encode session: %v", err)
	}
	if s.IsSoftware() {
		s.child.mu.Lock()
		t.Logf("child stderr tail: %s", s.child.stderrTail)
		s.child.mu.Unlock()
		s.Close()
		t.Skip("hardware MFT unavailable (software tier bound) - rate-control gate needs the hardware encoder")
	}
	var aus []AU
	done := make(chan struct{})
	go func() {
		for au := range s.Output() {
			aus = append(aus, AU{Data: append([]byte(nil), au.Data...), PTSNs: au.PTSNs, Keyframe: au.Keyframe})
		}
		close(done)
	}()
	frameNs := int64(time.Second) / rcFPS
	for i := 0; i < n; i++ {
		if hook != nil {
			hook(i, s)
		}
		if err := s.Encode(at(i), int64(i)*frameNs); err != nil {
			t.Fatalf("encode %d: %v", i, err)
		}
	}
	time.Sleep(200 * time.Millisecond)
	st := s.Stats()
	s.Close()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("encoder output never closed")
	}
	if len(aus) < n*9/10 {
		t.Fatalf("aus=%d for %d frames", len(aus), n)
	}
	return rcRun{kbps: rateKbps(aus, 0, n), kbpsSS: rateKbps(aus, rcFPS, n), aus: aus, st: st}
}

// rateKbps = AU bits of frames [from, to) by PTS over their media duration.
func rateKbps(aus []AU, from, to int) float64 {
	frameNs := int64(time.Second) / rcFPS
	var b int
	for _, au := range aus {
		f := int((au.PTSNs + frameNs/2) / frameNs)
		if f >= from && f < to {
			b += len(au.Data)
		}
	}
	return float64(b) * 8 / 1000 / (float64(to-from) / rcFPS)
}

// decodeFrame decodes frame idx of an H.264 AU stream to RGB24 with ffmpeg ("" skip reason if none).
func decodeFrame(t *testing.T, aus []AU, idx int) ([]byte, string) {
	t.Helper()
	ffmpeg, ok := mediatools.Resolve("ffmpeg")
	if !ok {
		return nil, "ffmpeg not found"
	}
	var in bytes.Buffer
	for _, au := range aus {
		in.Write(au.Data)
	}
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "h264", "-i", "pipe:0",
		"-vf", "select=eq(n\\,"+itoa(idx)+")", "-frames:v", "1", "-pix_fmt", "rgb24", "-f", "rawvideo", "pipe:1")
	cmd.Stdin = &in
	out, err := cmd.Output()
	if err != nil || len(out) < rcW*rcH*3 {
		t.Fatalf("bitstream does not decode (%v), got %d bytes want %d", err, len(out), rcW*rcH*3)
	}
	return out, ""
}

func itoa(i int) string {
	var b [20]byte
	n := len(b)
	for {
		n--
		b[n] = byte('0' + i%10)
		i /= 10
		if i == 0 {
			return string(b[n:])
		}
	}
}

// meanAbsDiff compares decoded RGB24 against the RGBA source on the red channel (grey content).
func meanAbsDiff(rgb, rgba []byte) float64 {
	var sum int
	for i := 0; i < rcW*rcH; i++ {
		d := int(rgb[i*3]) - int(rgba[i*4])
		if d < 0 {
			d = -d
		}
		sum += d
	}
	return float64(sum) / float64(rcW*rcH)
}

func requireRCGPU(t *testing.T) {
	t.Helper()
	if !Available() {
		t.Skip("no hardware H.264 MFT / D3D11 device")
	}
	requireEncExe(t)
}

// TestQualityModeFollowsContent: same quality target + cap, (a) static picture spends a small
// fraction of the cap, (b) high-motion content is held at/under the cap, (c) SIMPLE motion spends
// well under what fixed CBR spends; fixed mode on the same content for comparison (CBR target = the
// cap). Decoded pictures are checked for both modes.
func TestQualityModeFollowsContent(t *testing.T) {
	requireRCGPU(t)
	const n = 6 * rcFPS
	static := rcBands()
	motion := rcMotion(n)
	base := ProcOpts{InW: rcW, InH: rcH, OutW: rcW, OutH: rcH, FPS: rcFPS, Kbps: rcCap, Gop: 2 * rcFPS}
	qual := base
	qual.Quality, qual.QP = rcQ, rcQP

	qs := encodeRC(t, qual, n, func(int) []byte { return static }, nil)
	qm := encodeRC(t, qual, n, func(i int) []byte { return motion[i] }, nil)
	fs := encodeRC(t, base, n, func(int) []byte { return static }, nil)
	fm := encodeRC(t, base, n, func(i int) []byte { return motion[i] }, nil)
	smooth := rcSmooth(n)
	baseS, qualS := base, qual
	baseS.Kbps, qualS.Kbps = rcCapSmooth, rcCapSmooth
	qsm := encodeRC(t, qualS, n, func(i int) []byte { return smooth[i] }, nil)
	fsm := encodeRC(t, baseS, n, func(i int) []byte { return smooth[i] }, nil)
	t.Logf("encoder=%q cap=%d kbps qp=%d", qs.st.Name, rcCap, rcQP)
	for _, r := range []struct {
		name string
		r    rcRun
	}{{"quality/static", qs}, {"quality/motion", qm}, {"quality/smooth", qsm},
		{"fixed/static", fs}, {"fixed/motion", fm}, {"fixed/smooth", fsm}} {
		t.Logf("%-15s rc=%-22s cap=%d kbps: %7.0f kbps whole, %7.0f kbps steady (%.1f%% of cap)",
			r.name, r.r.st.RateControl, r.r.st.RateCapKbps, r.r.kbps, r.r.kbpsSS, 100*r.r.kbpsSS/float64(r.r.st.RateCapKbps))
	}
	if qs.st.RateControl == "" || qs.st.RateControl == "cbr" || qs.st.RateControl == "cbr(quality refused)" {
		t.Fatalf("quality session engaged %q - no quality rung on this encoder", qs.st.RateControl)
	}
	if fs.st.RateControl != "cbr" {
		t.Fatalf("fixed session engaged %q, want cbr", fs.st.RateControl)
	}
	if qs.st.RateQP != rcQP || qs.st.RateCapKbps != rcCap {
		t.Fatalf("telemetry qp=%d cap=%d, want %d/%d", qs.st.RateQP, qs.st.RateCapKbps, rcQP, rcCap)
	}
	if qs.kbpsSS > 0.10*rcCap {
		t.Errorf("quality/static steady %.0f kbps > 10%% of the %d kbps cap: bits do not follow content", qs.kbpsSS, rcCap)
	}
	if qm.kbps > rcOvershoot*rcCap || qm.kbpsSS > rcOvershoot*rcCap {
		t.Errorf("quality/motion %.0f/%.0f kbps exceeds %.2fx the %d kbps cap", qm.kbps, qm.kbpsSS, rcOvershoot, rcCap)
	}
	if qm.kbpsSS < 0.5*rcCap {
		t.Errorf("quality/motion steady %.0f kbps: content meant to bind the cap did not - test content too easy", qm.kbpsSS)
	}
	if fm.kbpsSS < 0.85*rcCap || fm.kbpsSS > rcOvershoot*rcCap {
		t.Errorf("fixed/motion steady %.0f kbps, want ~%d (0.85..%.2fx)", fm.kbpsSS, rcCap, rcOvershoot)
	}
	// Fixed mode on NVIDIA adds no filler, so it also undershoots static/simple content - the
	// measured, unchanged legacy behaviour (bit-identical to the pre-change child). The quality gate
	// is relative: on the same simple motion, constant quality must spend clearly less.
	if qsm.kbpsSS > 0.7*fsm.kbpsSS {
		t.Errorf("quality/smooth steady %.0f kbps vs fixed %.0f: constant quality should need far less on simple motion", qsm.kbpsSS, fsm.kbpsSS)
	}

	// Decoded-picture sanity: static bands survive in both modes; motion decodes close to source.
	for _, r := range []struct {
		name string
		r    rcRun
	}{{"quality/static", qs}, {"fixed/static", fs}} {
		rgb, skip := decodeFrame(t, r.r.aus, n-1)
		if skip != "" {
			t.Log("decode sanity skipped: ", skip)
			return
		}
		at := func(x, y int) (int, int) { i := (y*rcW + x) * 3; return int(rgb[i]), int(rgb[i+2]) }
		tr, tb := at(rcW/2, rcH/8)
		br, bb := at(rcW/2, rcH*7/8)
		t.Logf("%s decoded bands: top r=%d b=%d, bottom r=%d b=%d", r.name, tr, tb, br, bb)
		if tr < 200 || tb > 56 || bb < 200 || br > 56 {
			t.Errorf("%s: decoded static picture wrong (top r=%d b=%d, bottom r=%d b=%d)", r.name, tr, tb, br, bb)
		}
	}
	for _, r := range []struct {
		name string
		r    rcRun
		src  []byte
	}{{"quality/motion", qm, motion[n-1]}, {"fixed/motion", fm, motion[n-1]},
		{"quality/smooth", qsm, smooth[n-1]}, {"fixed/smooth", fsm, smooth[n-1]}} {
		rgb, _ := decodeFrame(t, r.r.aus, n-1)
		d := meanAbsDiff(rgb, r.src)
		t.Logf("%s decoded frame %d: mean |diff| vs source = %.1f / 255", r.name, n-1, d)
		if d > 40 {
			t.Errorf("%s: decoded motion frame too far from source (%.1f)", r.name, d)
		}
	}
}

// TestQualityModeRuntimeCapCut: the receiver governor's live SetBitrate must move the PEAK in
// quality mode - output on cap-binding content drops to the new cap, and telemetry follows.
func TestQualityModeRuntimeCapCut(t *testing.T) {
	requireRCGPU(t)
	const n = 8 * rcFPS
	const cut = 4 * rcFPS
	const low = 1500
	motion := rcMotion(n)
	o := ProcOpts{InW: rcW, InH: rcH, OutW: rcW, OutH: rcH, FPS: rcFPS, Kbps: rcCap, Gop: 2 * rcFPS, Quality: rcQ, QP: rcQP}
	var mid ProcStats
	r := encodeRC(t, o, n, func(i int) []byte { return motion[i] }, func(i int, s *ProcSession) {
		if i == cut {
			mid = s.Stats()
			s.SetBitrate(low)
			time.Sleep(100 * time.Millisecond) // op is async over stdin; land it before frame `cut`
		}
	})
	before := rateKbps(r.aus, rcFPS, cut)
	after := rateKbps(r.aus, cut+rcFPS, n) // 1 s for the VBV to drain to the new cap
	t.Logf("rc=%s: before cut %.0f kbps (cap %d, telemetry %d), after cut %.0f kbps (cap %d, telemetry %d)",
		r.st.RateControl, before, rcCap, mid.RateCapKbps, after, low, r.st.RateCapKbps)
	if r.st.RateCapKbps != low || mid.RateCapKbps != rcCap {
		t.Errorf("telemetry cap %d -> %d, want %d -> %d", mid.RateCapKbps, r.st.RateCapKbps, rcCap, low)
	}
	if after > rcOvershoot*low {
		t.Errorf("after the live cut %.0f kbps > %.2fx the new %d kbps cap", after, rcOvershoot, low)
	}
	if before < 0.5*rcCap {
		t.Errorf("before the cut %.0f kbps: content did not bind the original cap - cut proves nothing", before)
	}
}

// TestOpenCmdQualityFields: quality mode rides "q"/"qp"; a fixed-mode open stays byte-identical to
// the pre-quality one (both omitempty), so an older child sees nothing new.
func TestOpenCmdQualityFields(t *testing.T) {
	fixed := openCmd{Op: "open", SID: 1, Shm: `Local\x`, InW: 1280, InH: 720, OutW: 1280, OutH: 720,
		FpsN: 60, FpsD: 1, Kbps: 3000, Gop: 120}
	b, _ := json.Marshal(fixed)
	if strings.Contains(string(b), `"q"`) || strings.Contains(string(b), `"qp"`) {
		t.Fatalf("fixed open carries quality fields: %s", b)
	}
	q := fixed
	q.Q, q.QP = 70, 23
	b, _ = json.Marshal(q)
	if !strings.Contains(string(b), `"q":70`) || !strings.Contains(string(b), `"qp":23`) {
		t.Fatalf("quality open lost q/qp: %s", b)
	}
}

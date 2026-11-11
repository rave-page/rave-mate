//go:build windows && cgo

package mfenc

// Real-GPU verification of the native LIVE resolution cap (GAP 1 + GAP 2): one encode child session
// re-types its MFT+pool to the capped coded size in place, and one decode child session (same
// per-adapter child) handles the in-band SPS resize in place, publishing into an UNCHANGED
// nominal-size destination texture. Gated like the other hardware tests (skips without an MF
// pipeline / enc exe). Short + VRAM-bounded per the lead's revised gate: free >= 4 GB before each
// run, aborts if free < 3 GB mid-run, never allocates beyond the 4K encode+decode sessions.
//
//	RAVE_MATE_ENC_EXE=...\zig-out\bin\rave-mate-enc.exe go test ./internal/mfenc -run TestNativeLiveCapToggle -v

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"rave.page/mate/internal/gpumem"
)

const (
	capNomW, capNomH = 3840, 2160
	capLowW, capLowH = 1920, 1080
	capToggles       = 10
)

func TestNativeLiveCapToggle(t *testing.T) {
	if !Available() {
		t.Skip("no hardware MF encode/decode pipeline")
	}
	requireEncExe(t)
	sampler := gpumem.NewSampler()
	requireFreeVRAM(t, sampler, 4096) // revised gate: >= 4 GB free before the run

	// Capture the supervisor log seams: a recycle or ffmpeg-pin decision would surface here, and the
	// brief requires zero of either across the toggles.
	var logMu sync.Mutex
	var logLines []string
	capture := func(f string, a ...any) {
		logMu.Lock()
		logLines = append(logLines, fmt.Sprintf(f, a...))
		logMu.Unlock()
	}
	oldWarn, oldInfo := Warnf, Infof
	Warnf, Infof = capture, capture
	defer func() { Warnf, Infof = oldWarn, oldInfo }()

	// Destination: a nominal-size shared D3D11 texture (no Spout dependency). Its handle + size never
	// change for the whole route - the owner invariant under test.
	dst, err := newTestTexture(0, capNomW, capNomH, dxgiB8G8R8A8UNorm, false, nil)
	if err != nil {
		t.Skipf("no destination shared texture on this host: %v", err)
	}
	defer dst.Close()
	dstHandle := dst.Share

	enc, err := OpenProcSession(0, capNomW, capNomH, capNomW, capNomH, 30, 20000, 60)
	if err != nil {
		t.Skipf("no encode session: %v", err)
	}
	defer enc.Close()

	dec, err := OpenProcDecSession(ProcDecOpts{
		LUID: 0, InW: capNomW, InH: capNomH, OutW: capNomW, OutH: capNomH, FPS: 30, KbpsHint: 20000,
		Dest: &DecodeDest{Name: "rave-mate livecap dst", Resolve: func() (uint64, uint32, int, int, bool) {
			return dstHandle, dxgiB8G8R8A8UNorm, capNomW, capNomH, dstHandle != 0
		}},
	})
	if err != nil {
		t.Fatalf("native decode open: %v", err)
	}
	defer dec.Close()
	t.Logf("encoder=%q decoder=%q hw=%v dst=%#x %dx%d", enc.Name(), dec.Name(), dec.IsHardware(), dstHandle, capNomW, capNomH)

	// enc + dec share the ONE per-adapter child (LUID 0): its PID is what gpumem attributes VRAM to.
	enc.child.mu.Lock()
	childPID := uint32(enc.child.cmd.Process.Pid)
	enc.child.mu.Unlock()

	// Pump the encoder's AUs straight into the decoder, recording each AU so the post-switch IDR/SPS
	// can be inspected. Bounded: `pending` is cleared at every switch boundary.
	var mu sync.Mutex
	var pending []auRec
	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		for au := range enc.Output() {
			cp := append([]byte(nil), au.Data...)
			_ = dec.Decode(cp, au.PTSNs, au.Keyframe)
			mu.Lock()
			pending = append(pending, auRec{cp, au.Keyframe})
			mu.Unlock()
		}
	}()

	frame := makeCapFrame(capNomW, capNomH)
	pts := time.Now().UnixNano()
	feed := func(n int) {
		for i := 0; i < n; i++ {
			if err := enc.Encode(frame, pts); err != nil {
				t.Fatalf("encode: %v", err)
			}
			pts += 33_333_333
			time.Sleep(8 * time.Millisecond)
		}
	}
	snapshot := func() []auRec {
		mu.Lock()
		defer mu.Unlock()
		return append([]auRec(nil), pending...)
	}
	clearPending := func() {
		mu.Lock()
		pending = pending[:0]
		mu.Unlock()
	}

	// Baseline: a few frames uncapped, then VRAM + the SPS-parser self-check.
	feed(6)
	time.Sleep(300 * time.Millisecond)
	baseVRAM := childVRAM(sampler, childPID)
	t.Logf("baseline child VRAM: %d MB", baseVRAM)

	spsTrust := true
	if snap := snapshot(); len(snap) == 0 || !snap[0].key {
		t.Fatalf("baseline: first AU is not a keyframe (got %d AUs)", len(snap))
	} else if w, h, ok := codedSizeFromAUs(snap); !ok || w != capNomW || h != capNomH {
		spsTrust = false
		t.Logf("SPS parser self-check: parsed %dx%d ok=%v (want %dx%d) - falling back to decoder telemetry for coded-size asserts", w, h, ok, capNomW, capNomH)
	} else {
		t.Logf("SPS parser self-check OK: baseline IDR parses to %dx%d", w, h)
	}

	var cappedVRAM, restoredVRAM, endVRAM uint64
	lastDecFrames := dec.Stats().DecFrames
	for i := 0; i < capToggles; i++ {
		requireFreeVRAM(t, sampler, 3072) // abort immediately if free < 3 GB
		low := i%2 == 0
		wantW, wantH := capNomW, capNomH
		if low {
			wantW, wantH = capLowW, capLowH
		}
		// Settle so every pre-switch AU has drained, then mark the boundary.
		feed(5)
		time.Sleep(250 * time.Millisecond)
		clearPending()
		NoteDecodeResizeGrace() // production grants the decoder re-init grace on a cap change
		if !enc.SetCapHeight(wantW, wantH) {
			t.Fatalf("toggle %d: SetCapHeight(%d,%d) returned false", i, wantW, wantH)
		}
		feed(6) // frames at the new coded size - the first is a fresh-SPS IDR
		time.Sleep(350 * time.Millisecond)

		snap := snapshot()
		if len(snap) == 0 {
			t.Fatalf("toggle %d: no AUs after the switch", i)
		}
		// (2) an IDR with fresh SPS/PPS is the first AU after the switch.
		if !snap[0].key || !hasSPS(snap[0].data) {
			t.Fatalf("toggle %d: first AU after switch key=%v hasSPS=%v, want IDR+SPS", i, snap[0].key, hasSPS(snap[0].data))
		}
		// (1) the cap actually ENGAGES - coded size of the post-switch stream alternates.
		st := dec.Stats()
		if spsTrust {
			if w, h, ok := codedSizeFromAUs(snap); !ok || w != wantW || h != wantH {
				t.Fatalf("toggle %d: SPS coded size %dx%d ok=%v, want %dx%d (cap did not engage)", i, w, h, ok, wantW, wantH)
			}
		}
		// (5) risk #2 - the DECODER's coded size is the display size (1080), not a 16-row-aligned 1088.
		if st.DecCodedW != wantW || st.DecCodedH != wantH {
			t.Fatalf("toggle %d: decoder coded size %dx%d, want %dx%d (16-row squash / wrong src rect)", i, st.DecCodedW, st.DecCodedH, wantW, wantH)
		}
		// (4) decoded frames keep arriving (count continuity; the brief hole is tolerated).
		if st.DecFrames <= lastDecFrames {
			t.Fatalf("toggle %d: decoder published no new frames across the switch (%d <= %d)", i, st.DecFrames, lastDecFrames)
		}
		lastDecFrames = st.DecFrames
		// (3) destination unchanged; no decode errors mid-switch.
		if st.DecErrors != 0 {
			t.Fatalf("toggle %d: decErrors=%d during the resize", i, st.DecErrors)
		}
		t.Logf("toggle %d -> %dx%d: decCoded=%dx%d decResizes=%d decFrames=%d decErrors=%d inDropped=%d",
			i, wantW, wantH, st.DecCodedW, st.DecCodedH, st.DecResizes, st.DecFrames, st.DecErrors, st.InDropped)

		if low && cappedVRAM == 0 {
			cappedVRAM = childVRAM(sampler, childPID)
		}
		if !low && restoredVRAM == 0 {
			restoredVRAM = childVRAM(sampler, childPID)
		}
	}
	endVRAM = childVRAM(sampler, childPID)

	st := dec.Stats()
	// (3) the decoder reported an in-band resize for (nearly) every switch, dest handle unchanged,
	// zero recycle, zero pin, no wedge. DecResizes counts only SIZE changes.
	if st.DecResizes < capToggles-1 {
		t.Fatalf("decoder reported %d in-band resizes, want >= %d", st.DecResizes, capToggles-1)
	}
	if dstHandle != dst.Share {
		t.Fatalf("destination handle changed %#x -> %#x", dstHandle, dst.Share)
	}
	if st.Restarts != 0 {
		t.Fatalf("decode session restarts=%d (a recycle happened)", st.Restarts)
	}
	// Specific recycle/pin/demote signatures only (Restarts==0 above already proves no recycle; these
	// catch a pin to the ffmpeg path). "pinned adapter" in the device-open trace is benign, so the
	// phrases are precise rather than the bare word "pinned".
	logMu.Lock()
	joined := strings.ToLower(strings.Join(logLines, "\n"))
	logMu.Unlock()
	for _, bad := range []string{"reopen failed", "pinning this sender", "pinned to the", "ffmpeg decode path", "recycling ("} {
		if strings.Contains(joined, bad) {
			t.Fatalf("supervisor log contains %q (a recycle/pin during the toggles):\n%s", bad, joined)
		}
	}

	// (6) per-process VRAM: drop on the decode side when capped, return to baseline after restore, no
	// growth after 10 toggles.
	t.Logf("child VRAM MB: baseline=%d capped=%d restored=%d after-%d-toggles=%d", baseVRAM, cappedVRAM, restoredVRAM, capToggles, endVRAM)
	if endVRAM > baseVRAM+64 {
		t.Fatalf("child VRAM grew across %d toggles: %d -> %d MB (> baseline+64)", capToggles, baseVRAM, endVRAM)
	}
	if baseVRAM > 0 && cappedVRAM > 0 && cappedVRAM >= baseVRAM {
		t.Logf("NOTE: capped VRAM (%d) not below baseline (%d) - expected a decode-DPB drop; attribution is primary-adapter + shared child, so a same-size encode pool can mask it", cappedVRAM, baseVRAM)
	}

	enc.Close()
	dec.Close()
	<-pumpDone
	t.Logf("live-cap toggle: %d switches, decFrames=%d decResizes=%d, SPS-trusted=%v", capToggles, st.DecFrames, st.DecResizes, spsTrust)
}

// requireFreeVRAM skips/aborts unless the primary adapter has at least minMB free.
func requireFreeVRAM(t *testing.T, s gpumem.Sampler, minMB uint64) {
	t.Helper()
	ads, err := s.Sample()
	if err != nil || len(ads) == 0 {
		t.Skipf("no GPU adapter sample (%v)", err)
	}
	var free uint64
	for _, a := range ads {
		if a.Primary {
			free = a.FreeMB
		}
	}
	if free == 0 && len(ads) > 0 {
		free = ads[0].FreeMB
	}
	if free < minMB {
		t.Skipf("free VRAM %d MB < required %d MB (card busy) - run on an idle card", free, minMB)
	}
}

// childVRAM returns the dedicated VRAM (MB) attributed to pid across all adapters.
func childVRAM(s gpumem.Sampler, pid uint32) uint64 {
	aps, err := s.SampleProcesses(64)
	if err != nil {
		return 0
	}
	var mb uint64
	for _, ap := range aps {
		for _, p := range ap.Procs {
			if p.PID == pid {
				mb += p.MB
			}
		}
	}
	return mb
}

// makeCapFrame builds one w*h RGBA frame (a deterministic gradient - the input is the SAME every
// frame; only the coded OUTPUT size changes under the cap).
func makeCapFrame(w, h int) []byte {
	pix := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		row := pix[y*w*4:]
		for x := 0; x < w; x++ {
			p := row[x*4:]
			p[0] = byte(x * 255 / w)
			p[1] = byte(y * 255 / h)
			p[2] = byte((x + y) & 0xFF)
			p[3] = 255
		}
	}
	return pix
}

// ── minimal Annex-B / H.264 SPS dimension parser (test-only cross-check for assertion 1) ──

// auRec is one recorded access unit (payload + keyframe flag).
type auRec struct {
	data []byte
	key  bool
}

// codedSizeFromAUs returns the coded (cropped) size of the first SPS found in the AUs.
func codedSizeFromAUs(aus []auRec) (int, int, bool) {
	for _, au := range aus {
		if sps := findNAL(au.data, 7); sps != nil {
			return parseSPSDims(rbsp(sps))
		}
	}
	return 0, 0, false
}

// hasSPS reports whether an Annex-B AU carries an SPS NAL (type 7).
func hasSPS(au []byte) bool { return findNAL(au, 7) != nil }

// findNAL returns the payload (after the 1-byte NAL header) of the first NAL of nalType, or nil.
func findNAL(b []byte, nalType byte) []byte {
	i := 0
	for i+3 < len(b) {
		// start code 00 00 01 or 00 00 00 01
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
			start := i + 3
			if (b[start] & 0x1F) == nalType {
				return b[start+1 : nalEnd(b, start+1)]
			}
			i = start
			continue
		}
		i++
	}
	return nil
}

func nalEnd(b []byte, from int) int {
	for i := from; i+2 < len(b); i++ {
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
			end := i
			if i > from && b[i-1] == 0 {
				end = i - 1
			}
			return end
		}
	}
	return len(b)
}

// rbsp strips H.264 emulation-prevention bytes (00 00 03 -> 00 00).
func rbsp(nal []byte) []byte {
	out := make([]byte, 0, len(nal))
	for i := 0; i < len(nal); i++ {
		if i >= 2 && nal[i] == 3 && nal[i-1] == 0 && nal[i-2] == 0 && i+1 <= len(nal) {
			continue // drop the 0x03
		}
		out = append(out, nal[i])
	}
	return out
}

type bitReader struct {
	data []byte
	pos  int
}

func (r *bitReader) bit() uint {
	idx := r.pos >> 3
	if idx >= len(r.data) {
		r.pos++
		return 0
	}
	v := (r.data[idx] >> (7 - uint(r.pos&7))) & 1
	r.pos++
	return uint(v)
}

func (r *bitReader) u(n int) uint {
	var v uint
	for i := 0; i < n; i++ {
		v = (v << 1) | r.bit()
	}
	return v
}

func (r *bitReader) ue() uint {
	lz := 0
	for lz < 32 && r.bit() == 0 {
		lz++
	}
	if lz == 0 {
		return 0
	}
	return (1 << uint(lz)) - 1 + r.u(lz)
}

func (r *bitReader) se() int {
	k := r.ue()
	if k&1 == 1 {
		return int((k + 1) / 2)
	}
	return -int(k / 2)
}

func skipScalingList(r *bitReader, size int) {
	last, next := 8, 8
	for j := 0; j < size; j++ {
		if next != 0 {
			delta := r.se()
			next = (last + delta + 256) % 256
		}
		if next != 0 {
			last = next
		}
	}
}

// parseSPSDims extracts the cropped display width/height from an SPS RBSP (payload, no NAL header).
func parseSPSDims(sps []byte) (int, int, bool) {
	if len(sps) < 4 {
		return 0, 0, false
	}
	r := &bitReader{data: sps}
	profile := r.u(8)
	r.u(8) // constraint flags + reserved
	r.u(8) // level_idc
	r.ue() // seq_parameter_set_id
	cfi := uint(1)
	switch profile {
	case 100, 110, 122, 244, 44, 83, 86, 118, 128, 138, 139, 134, 135:
		cfi = r.ue()
		if cfi == 3 {
			r.u(1) // separate_colour_plane_flag
		}
		r.ue()           // bit_depth_luma_minus8
		r.ue()           // bit_depth_chroma_minus8
		r.u(1)           // qpprime_y_zero_transform_bypass
		if r.u(1) == 1 { // seq_scaling_matrix_present
			lists := 8
			if cfi == 3 {
				lists = 12
			}
			for i := 0; i < lists; i++ {
				if r.u(1) == 1 {
					if i < 6 {
						skipScalingList(r, 16)
					} else {
						skipScalingList(r, 64)
					}
				}
			}
		}
	}
	r.ue() // log2_max_frame_num_minus4
	poc := r.ue()
	switch poc {
	case 0:
		r.ue()
	case 1:
		r.u(1)
		r.se()
		r.se()
		n := r.ue()
		for i := uint(0); i < n; i++ {
			r.se()
		}
	}
	r.ue() // max_num_ref_frames
	r.u(1) // gaps_in_frame_num_value_allowed
	wMbs := r.ue() + 1
	hMap := r.ue() + 1
	frameMbsOnly := r.u(1)
	if frameMbsOnly == 0 {
		r.u(1) // mb_adaptive_frame_field
	}
	r.u(1) // direct_8x8_inference
	w := int(wMbs) * 16
	h := int((2-frameMbsOnly)*hMap) * 16
	if r.u(1) == 1 { // frame_cropping_flag
		cl := r.ue()
		cr := r.ue()
		ct := r.ue()
		cb := r.ue()
		subW, subH := 1, 1
		switch cfi {
		case 1:
			subW, subH = 2, 2
		case 2:
			subW, subH = 2, 1
		}
		cuX := subW
		cuY := subH * (2 - int(frameMbsOnly))
		if cfi == 0 {
			cuX, cuY = 1, 2-int(frameMbsOnly)
		}
		w -= cuX * int(cl+cr)
		h -= cuY * int(ct+cb)
	}
	if w <= 0 || h <= 0 || w > 16384 || h > 16384 {
		return 0, 0, false
	}
	return w, h, true
}

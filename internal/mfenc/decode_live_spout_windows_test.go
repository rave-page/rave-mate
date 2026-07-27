//go:build windows && cgo && spout

package mfenc

// Live increment-2 gate: a REAL round trip. The probe pattern is encoded by a real encode session,
// its access units are fed to a real dir:"dec" session, and the frame the child publishes into a
// REAL Spout sender's shared texture is read back and checked.
//
//	go test -tags spout ./internal/mfenc -run TestDecodeLive -v
//
// What no mock can prove: OpenSharedResource + CreateVideoProcessorOutputView over a Spout sender's
// texture, binding a D3D11-aware decoder MFT, IMFDXGIBuffer → texture + array slice, the source-rect
// handling for a 16-row-aligned decoder surface, the access-mutex handshake, and that the published
// picture has the right ROW ORDER and CHANNEL ORDER (the two failure modes that look perfect in
// every counter).
//
// The read-back runs in a SECOND process: this process owns the sender's GL context, and a
// same-process Spout send+receive deadlocks in the driver (same reason inc 1's harness re-execs).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/mediatools"
	"rave.page/mate/internal/videoshare"
)

const (
	decSender = "rave-mate link decode gate"
	decW      = 640
	decH      = 360
)

// decSenderName makes a PER-ATTEMPT unique sender name. A reused Spout sender name hands the next
// reader the previous publisher's DEAD texture, which reads back blank with zero errors in every
// counter - so a fixed name masks exactly the bug these gates test for (risk R1; the inc-4 bInvert
// experiment "proved" a working code path publishes black twice before this was understood, and the
// P0 receive-black work adopted per-attempt names for the same reason). These gates were still on a
// fixed name, which is why their read-back oracle had been reported as "cannot see a picture".
func decSenderName(suffix string) string {
	return fmt.Sprintf("%s %s %d-%d", decSender, suffix, os.Getpid(), time.Now().UnixNano()%100000)
}

// publishRepeatedly publishes img the way a real sender does: continuously. ONE SendImage is not
// visible to another process's D3D11 device (the GL/DX interop write is not flushed until further GL
// work is submitted), which is the instrument bug that made increment 2 abandon its cross-process
// read-back oracle and record the limitation as a product one.
func publishRepeatedly(s interface {
	Send(*image.NRGBA) error
}, img *image.NRGBA) error {
	for i := 0; i < 40; i++ {
		if err := s.Send(img); err != nil {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil
}

// decPattern is the probe image: top half RED, bottom half BLUE, so a vertical flip and a
// red/blue swizzle are both visible in ONE published frame.
func decPattern() *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, decW, decH))
	for y := 0; y < decH; y++ {
		r, b := byte(255), byte(0)
		if y >= decH/2 {
			r, b = 0, 255
		}
		for x := 0; x < decW; x++ {
			p := img.Pix[y*img.Stride+x*4:]
			p[0], p[1], p[2], p[3] = r, 0, b, 255
		}
	}
	return img
}

// bandSample is the grabber child's report. One tag per FIELD: declaring two fields on a line
// gives them the SAME tag, so TopB/BottomB silently decoded from tr/br (caught by
// `go vet -tags spout`, a lane the untagged sweep never compiles).
type bandSample struct {
	TopR    int `json:"tr"`
	TopB    int `json:"tb"`
	BottomR int `json:"br"`
	BottomB int `json:"bb"`
}

// TestDecodeLiveGrabber is the child role: read the published texture back and report the bands.
func TestDecodeLiveGrabber(t *testing.T) {
	if os.Getenv("RAVE_DEC_ROLE") != "grab" {
		t.Skip("child role only (set by TestDecodeLiveSession)")
	}
	name := os.Getenv("RAVE_DEC_SENDER")
	if name == "" {
		name = decSender
	}
	img, err := videoshare.GrabSenderFrame(name, decW, decH)
	if err != nil {
		fmt.Fprintln(os.Stderr, "[dec-grab] "+err.Error())
		return
	}
	at := func(x, y int) (int, int) {
		p := img.Pix[y*img.Stride+x*4:]
		return int(p[0]), int(p[2])
	}
	tr, tb := at(decW/2, decH/8)
	br, bb := at(decW/2, decH*7/8)
	b, _ := json.Marshal(bandSample{TopR: tr, TopB: tb, BottomR: br, BottomB: bb})
	fmt.Println("BANDS " + string(b))
}

func TestDecodeLiveSession(t *testing.T) {
	if !Available() {
		t.Skip("no D3D11 / MF pipeline")
	}
	requireEncExe(t)
	// The child's GPU band probe is the oracle that works here: it reads the destination texture back
	// inside the process that wrote it. Set BEFORE any child is spawned - the per-adapter child is
	// cached and inherits the environment it was started with.
	t.Setenv("RAVE_MATE_MFDEC_PROBE_BANDS", "1")

	// 1. Encode the probe pattern with the PROVEN readback encode path: real annex-B AUs with
	//    inband parameter sets, i.e. exactly what arrives off the wire on a live route.
	enc, err := OpenProcSession(0, decW, decH, decW, decH, 30, 4000, 30)
	if err != nil {
		t.Skipf("no encode session to produce a bitstream: %v", err)
	}
	var aus []AU
	encDone := make(chan struct{})
	go func() {
		for au := range enc.Output() {
			cp := AU{Data: append([]byte(nil), au.Data...), PTSNs: au.PTSNs, Keyframe: au.Keyframe}
			aus = append(aus, cp)
		}
		close(encDone)
	}()
	img := decPattern()
	base := time.Now().UnixNano()
	for i := 0; i < 40; i++ {
		if err := enc.Encode(img.Pix, base+int64(i)*33_333_333); err != nil {
			t.Fatalf("encode frame %d: %v", i, err)
		}
	}
	enc.Close()
	select {
	case <-encDone:
	case <-time.After(10 * time.Second):
		t.Fatal("encoder Output never closed")
	}
	if len(aus) < 5 || !aus[0].Keyframe {
		t.Fatalf("produced %d AUs (first keyframe %v): not a decodable bitstream", len(aus), len(aus) > 0 && aus[0].Keyframe)
	}
	t.Logf("bitstream: %d AUs, first %d bytes, keyframe=%v", len(aus), len(aus[0].Data), aus[0].Keyframe)
	// Cross-check the SOURCE bitstream with ffmpeg first: if the probe pattern is not in here, the
	// gate is measuring the encoder, not the decode+publish path under test.
	assertBitstreamBands(t, aus, decW, decH)

	// 2. The DESTINATION sender: Go creates it (SPOUTLIBRARY has no CreateSender - one zeroed frame
	//    forces the texture) and hands its handle to the child.
	sender := decSenderName("dest")
	ss, err := videoshare.NewSharedSender(logbus.New(64), sender, decW, decH)
	if err != nil {
		t.Skipf("no GPU destination texture on this host: %v", err)
	}
	defer ss.Close()
	t.Logf("destination sender %q: handle=%#x fmt=%d %dx%d", sender, ss.Handle(), ss.Format(), decW, decH)

	// 2b. POSITIVE CONTROL for the read-back oracle. Publish the probe through the ORDINARY frame
	//     path and read it back: if that fails, the oracle cannot see a known-good picture, so a
	//     black read-back later says nothing about the decode path and the band assertion must not
	//     run. Validate the instrument before trusting it.
	//
	//     Publish CONTINUOUSLY, not once. A single SendImage is not visible to another process's
	//     D3D11 device - the GL/DX interop write is not flushed until further GL work is submitted -
	//     and a one-shot publish here is what made increment 2 record "Spout's own receive side
	//     cannot see a foreign-device write on this rig" and abandon this gate. Every real sender
	//     publishes continuously, so the product was never affected; only the instrument was.
	oracleOK := false
	if publishRepeatedly(ss, decPattern()) == nil {
		if b := grabBandsNamed(t, sender); b != nil && b.TopR >= 128 && b.BottomB >= 128 {
			oracleOK = true
			t.Logf("read-back oracle validated on the frame path: top r=%d b=%d, bottom r=%d b=%d",
				b.TopR, b.TopB, b.BottomR, b.BottomB)
		} else {
			t.Log("read-back oracle CANNOT see a frame-path publish - the published PICTURE will not be verified this run")
		}
	}

	// 3. The decode session.
	d, err := OpenProcDecSession(ProcDecOpts{
		LUID: 0, InW: decW, InH: decH, OutW: decW, OutH: decH, FPS: 30, KbpsHint: 4000,
		Dest: &DecodeDest{Name: sender, Resolve: func() (uint64, uint32, int, int, bool) {
			return ss.Handle(), ss.Format(), decW, decH, ss.Handle() != 0
		}},
	})
	if err != nil {
		t.Fatalf("native decode open: %v", err)
	}
	t.Logf("decoder: %q hardwareMFT=%v", d.Name(), d.IsHardware())

	// 4. Feed the bitstream, keyframe first (a fresh decoder cannot use anything else).
	// The interval-derived rates ride ratewin's sliding window, so the window must BRACKET the
	// publishing: plant the first sample before the burst, read after >= ratewin.MinSpan. Priming
	// after the burst would measure a window in which nothing happened - correctly reporting 0.
	_ = d.Stats()
	for i, au := range aus {
		if err := d.Decode(au.Data, au.PTSNs, au.Keyframe); err != nil {
			t.Fatalf("Decode %d: %v", i, err)
		}
		time.Sleep(5 * time.Millisecond) // the child paces itself; give it room to drain
	}
	time.Sleep(rateWindowSettle)
	st := d.Stats()
	t.Logf("decode live: decFrames=%d decFPS=%.1f busy=%.2fms inDropped=%d decDropped=%d "+
		"errors=%d mtxTimeouts=%d staleMs=%.0f flags=%#x queueKiB=%d cpu=%.1f%%",
		st.DecFrames, st.DecFPS, st.DecBusyMs, st.InDropped, st.DecDropped, st.DecErrors,
		st.MtxTimeouts, st.DecStaleMs, st.DecFlags, st.QueueDepth, st.ChildCPUPct)

	if st.DecFlags&1 == 0 {
		t.Fatalf("decFlags %#x: the child never marked the session live", st.DecFlags)
	}
	if st.DecErrors != 0 {
		t.Fatalf("decErrors=%d on a healthy destination", st.DecErrors)
	}
	if st.InDropped != 0 {
		t.Fatalf("inDropped=%d: the ring dropped AUs it should have had room for", st.InDropped)
	}
	if st.DecFrames < uint64(len(aus)/2) {
		dumpChildTail(t, d.child)
		t.Fatalf("published %d frames from %d AUs, want at least half", st.DecFrames, len(aus))
	}
	if st.DecBusyMs <= 0 || st.DecBusyMs > 50 {
		t.Fatalf("decBusyMs=%.2f, want a plausible per-AU decode+publish cost", st.DecBusyMs)
	}
	// Freshness oracle: the last publish is rateWindowSettle old by the time we read (the rate
	// window has to be given a span), so the bound is relative to that, not an absolute 1 s.
	if maxStale := float64((rateWindowSettle + time.Second).Milliseconds()); st.DecStaleMs > maxStale {
		t.Fatalf("decStaleMs=%.0f (> %.0f) after publishing: the freshness oracle would false-positive", st.DecStaleMs, maxStale)
	}

	// 5a. The child's own GPU read-back of the destination texture: row order + channel order.
	assertChildBands(t, d.child)
	// 5b. Read the published texture back from a SECOND process and check the probe bands - the
	//    orientation + colour gate, since nothing in the counters above would notice a flip. Only
	//    meaningful when 2b proved the oracle can see a picture at all.
	if !oracleOK {
		t.Log("published PICTURE not verified this run (see 2b): counters only")
		d.Close()
		return
	}
	bands := grabBandsNamed(t, sender)
	if bands == nil {
		t.Fatal("the oracle saw the frame path but not the decode publish")
	} else {
		t.Logf("published bands: top r=%d b=%d, bottom r=%d b=%d", bands.TopR, bands.TopB, bands.BottomR, bands.BottomB)
		if bands.TopR < 128 || bands.TopB > 96 {
			t.Fatalf("top band published as r=%d b=%d, want RED - rows or channels are swapped", bands.TopR, bands.TopB)
		}
		if bands.BottomB < 128 || bands.BottomR > 96 {
			t.Fatalf("bottom band published as r=%d b=%d, want BLUE - rows or channels are swapped", bands.BottomR, bands.BottomB)
		}
	}
	d.Close()
}

// grabBandsNamed re-execs this test binary in the grabber role against name and parses its report.
func grabBandsNamed(t *testing.T, name string) *bandSample {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Logf("no test executable to re-exec: %v", err)
		return nil
	}
	cmd := exec.Command(exe, "-test.run=TestDecodeLiveGrabber", "-test.v")
	cmd.Env = append(os.Environ(), "RAVE_DEC_ROLE=grab", "RAVE_DEC_SENDER="+name)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		t.Logf("grabber exited %v", err)
	}
	for _, ln := range strings.Split(string(out), "\n") {
		ln = strings.TrimSpace(ln)
		if !strings.HasPrefix(ln, "BANDS ") {
			continue
		}
		var b bandSample
		if json.Unmarshal([]byte(strings.TrimPrefix(ln, "BANDS ")), &b) == nil {
			return &b
		}
	}
	return nil
}

// dumpChildTail prints the encoder child's stderr breadcrumbs (stage traces name the failing call).
func dumpChildTail(t *testing.T, c *procChild) {
	t.Helper()
	c.mu.Lock()
	tail := string(c.stderrTail)
	c.mu.Unlock()
	t.Logf("child stderr tail: %s", tail)
}

// assertBitstreamBands decodes the bitstream with ffmpeg and checks the probe bands, so a failure
// upstream of the decode session is attributed to the ENCODER instead of to dec.zig.
func assertBitstreamBands(t *testing.T, aus []AU, w, h int) {
	t.Helper()
	ffmpeg, ok := mediatools.Resolve("ffmpeg")
	if !ok {
		t.Log("ffmpeg not found - source bitstream NOT cross-checked this run")
		return
	}
	var in bytes.Buffer
	for _, au := range aus {
		in.Write(au.Data)
	}
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "h264", "-i", "pipe:0",
		"-frames:v", "1", "-pix_fmt", "rgb24", "-f", "rawvideo", "pipe:1")
	cmd.Stdin = &in
	out, err := cmd.Output()
	if err != nil || len(out) < w*h*3 {
		t.Fatalf("source bitstream does not decode (%v), got %d bytes want %d", err, len(out), w*h*3)
	}
	at := func(x, y int) (int, int) {
		i := (y*w + x) * 3
		return int(out[i]), int(out[i+2])
	}
	tr, tb := at(w/2, h/8)
	br, bb := at(w/2, h*7/8)
	t.Logf("source bitstream bands: top r=%d b=%d, bottom r=%d b=%d", tr, tb, br, bb)
	if tr < 128 || tb > 96 || bb < 128 || br > 96 {
		t.Fatalf("the ENCODER did not produce the probe pattern (top r=%d b=%d, bottom r=%d b=%d) - fix the harness, not dec.zig", tr, tb, br, bb)
	}
}

// TestDecodeLiveGrabOracle validates the READ-BACK ORACLE itself before it is trusted, and - since
// zigmedia inc 5 - DISCRIMINATES between the two things that can make it blank. Without both arms,
// "published bands are black" cannot be told apart from "the grab is broken", which is the mistake
// increment 2 made: it filed a real product bug as an instrument failure.
//
// Arm A publishes through a plain FrameSender, arm B through a SharedSender (the eagerly-created
// destination the decoder child renders into). videoshare's own gates prove the readback delivers
// real pixels for a sender published from ANOTHER process
// (TestRecvContentCarriesPixels: top(255,0,0) bottom(0,0,255)), so if arm A is blank the asymmetry
// is "publisher and reader are parent/child of each other"; if only arm B is blank it is
// rave_spout_open_sender's texture that a foreign process cannot read.
func TestDecodeLiveGrabOracle(t *testing.T) {
	// Arm A: a PLAIN frame sender, the exact object videoshare's passing content gates use.
	plainName := decSenderName("oracle-plain")
	fs, err := videoshare.NewFrameSender(logbus.New(64), plainName)
	if err != nil {
		t.Skipf("no frame sender on this host: %v", err)
	}
	if err := publishRepeatedly(fs, decPattern()); err != nil {
		fs.Close()
		t.Fatalf("plain Send: %v", err)
	}
	plainBands := grabBandsNamed(t, plainName)
	fs.Close()

	// Arm B: the SharedSender the decode path actually publishes into.
	name := decSenderName("oracle")
	ss, err := videoshare.NewSharedSender(logbus.New(64), name, decW, decH)
	if err != nil {
		t.Skipf("no GPU destination texture on this host: %v", err)
	}
	defer ss.Close()
	if err := publishRepeatedly(ss, decPattern()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	sharedBands := grabBandsNamed(t, name)

	describe := func(b *bandSample) string {
		if b == nil {
			return "no frame read back"
		}
		return fmt.Sprintf("top r=%d b=%d bottom r=%d b=%d", b.TopR, b.TopB, b.BottomR, b.BottomB)
	}
	plainOK := plainBands != nil && plainBands.TopR >= 128 && plainBands.BottomB >= 128
	sharedOK := sharedBands != nil && sharedBands.TopR >= 128 && sharedBands.BottomB >= 128
	t.Logf("oracle arm A (FrameSender, same process as the reader's parent): %s -> ok=%v",
		describe(plainBands), plainOK)
	t.Logf("oracle arm B (SharedSender, the decode destination):            %s -> ok=%v",
		describe(sharedBands), sharedOK)

	switch {
	case sharedOK:
		return // the oracle works: the decode gate's band assertion is trustworthy
	case !plainOK:
		// Both blank while videoshare's cross-process gates pass: the limitation is the harness
		// topology (a child reading a sender its own parent publishes), not the decode path.
		t.Skipf("the read-back oracle is blank for a PLAIN sender too, while videoshare's " +
			"cross-process content gates pass - a child cannot read a sender published by its own " +
			"parent on this rig, so the published PICTURE cannot be verified from here")
	default:
		t.Fatalf("a plain sender reads back fine (%s) but the eagerly-created SharedSender does not (%s) "+
			"- rave_spout_open_sender's texture is not what the registry advertises to other processes",
			describe(plainBands), describe(sharedBands))
	}
}

// assertChildBands parses the child's GPU band probe (RAVE_MATE_MFDEC_PROBE_BANDS) out of its
// stderr and asserts the probe pattern survived decode + colour conversion + publish.
func assertChildBands(t *testing.T, c *procChild) {
	t.Helper()
	c.mu.Lock()
	tail := string(c.stderrTail)
	c.mu.Unlock()
	const marker = "dec bands: "
	i := strings.LastIndex(tail, marker)
	if i < 0 {
		t.Logf("child GPU probe produced no bands line; stderr tail: %s", tail)
		t.Fatal("the destination texture was never read back - the published PICTURE is unverified")
	}
	line := tail[i+len(marker):]
	if j := strings.IndexByte(line, '\n'); j >= 0 {
		line = line[:j]
	}
	var tr, tb, br, bb int
	if _, err := fmt.Sscanf(line, "top r=%d b=%d bottom r=%d b=%d", &tr, &tb, &br, &bb); err != nil {
		t.Fatalf("cannot parse the child bands line %q: %v", line, err)
	}
	t.Logf("destination texture bands (child GPU read-back): top r=%d b=%d, bottom r=%d b=%d", tr, tb, br, bb)
	if tr < 128 || tb > 96 {
		t.Fatalf("top band published as r=%d b=%d, want RED - rows or channels are swapped", tr, tb)
	}
	if bb < 128 || br > 96 {
		t.Fatalf("bottom band published as r=%d b=%d, want BLUE - rows or channels are swapped", br, bb)
	}
}

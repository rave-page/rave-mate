package vrslgrid

// transcode_test.go - H.264 TRANSCODE-SURVIVAL integration test: does the extended-mode VRSL grid
// survive a CDN-grade x264 encode within the transport's own tolerances? Locally closes the main
// risk of the never-run live CDN pass. Round-trips a canonical 1920x1080 mono/1-universe extended
// frame through ffmpeg (libx264 yuv420p, CBR-ish 6000k - VRCDN/Twitch-shaped) and decodes the
// mid-stream frame back. One ffmpeg process per direction; hermetic (t.TempDir, deterministic
// pattern, no network). Skips only when ffmpeg is unresolvable; a resized frame or tolerance
// violations FAIL - that is the CDN-risk signal this test exists to catch.
//
// TWO decode surfaces, both asserted (single decode process, two outputs):
//   - "wire-luma": the decoded Y plane, sampled directly. This is what H.264 itself did to the
//     bytes - the CDN risk proper. Headers must decode EXACT after calibration here.
//   - "rgb-receiver": RGBA reconstruction sampled as BT.709 luminance at cell centre, mirroring
//     the shipped world decoder (RaveVRSLGridReader mono path). Header tolerance +-1.
//
// Why the surfaces differ (measured, not speculation): swscale's FULL-RANGE RGB->YUV writes
// U=V=127 (not 128) for pure greys - our own encode leg, not H.264, which carries luma
// bit-exact at these settings. Any receiver reconstructing RGB from the biased chroma skews
// R/B down ~1.4, and luminance sampling turns that into a constant ~-0.6 that black-anchored
// two-point calibration cannot remove (black clamps at 0). Net: dark bytes read -1 on the RGB
// surface. RUNBOOK: the flags byte (written 2) is therefore bit-fragile in truncating receivers
// (2 -> 1 flips loFrameValid AND fakes RGB9); values >=64 and the v1 header's 1-valued cells
// decode exact. CRC cell deliberately not asserted (contract: meaningless over a lossy codec).
//
// NOTE: the ~90 encoded frames are identical (static DMX snapshot), so the decoded mid-GOP frame
// converges to I-frame quality via P-skips - a fair stand-in for the steady-state grid between
// DMX changes, which is exactly when the world decodes values.

import (
	"bytes"
	"fmt"
	"image"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"rave.page/mate/internal/mediatools"
)

// transcodePattern maps ch -> byte deterministically; gcd(7,256)=1 so all 256 values (incl. the
// 0/255 extremes and every mid value) appear exactly twice across the 512 channels.
func transcodePattern(ch int) byte { return byte((ch * 7) % 256) }

// runFFmpegCmd runs one ffmpeg process to completion (one process per direction, like a CDN leg).
func runFFmpegCmd(t *testing.T, ffmpeg string, args ...string) {
	t.Helper()
	cmd := exec.Command(ffmpeg, args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("ffmpeg %v: %v\n%s", args, err, out.String())
	}
}

func TestTranscodeSurvivalH264(t *testing.T) {
	ffmpeg, ok := mediatools.Resolve("ffmpeg")
	if !ok {
		t.Skip("ffmpeg not resolvable (app-managed bin dir or PATH) - skipping H.264 transcode-survival test")
	}

	// Canonical extended frame: mono/1-universe 1920x1080 (the transport-safe production shape),
	// all 512 channels patterned, metadata band populated.
	var d [512]byte
	for ch := range d {
		d[ch] = transcodePattern(ch)
	}
	spec := CompositeSpec{
		Universes: []int{1}, Mono: true, Extended: true,
		FrameCounter: 200, LookID: 7, SceneID: 3, Blackout: 0,
	}
	src := RenderComposite(mapReader{1: d}, spec)
	if src.Bounds().Dx() != FrameWidth || src.Bounds().Dy() != FrameRefHeight {
		t.Fatalf("source frame = %v, want %dx%d", src.Bounds(), FrameWidth, FrameRefHeight)
	}

	dir := t.TempDir()
	rawIn := filepath.Join(dir, "frame.rgba")
	encoded := filepath.Join(dir, "cdn.mp4")
	rgbaOut := filepath.Join(dir, "decoded.rgba")
	yuvOut := filepath.Join(dir, "decoded.yuv")
	if err := os.WriteFile(rawIn, src.Pix, 0o644); err != nil {
		t.Fatal(err)
	}

	// Encode leg: ~3s @30fps (stream_loop repeats the single raw frame) with CDN-ish x264
	// settings: veryfast, CBR-ish 6000k, 2s GOP. Mirrors the production push contract: yuv420p,
	// FULL range, NO -vf colorspace/gamma (frames are raw DMX bytes, LINEAR).
	size := fmt.Sprintf("%dx%d", FrameWidth, FrameRefHeight)
	runFFmpegCmd(t, ffmpeg,
		"-hide_banner", "-loglevel", "error", "-y",
		"-stream_loop", "89",
		"-f", "rawvideo", "-pix_fmt", "rgba", "-video_size", size, "-framerate", "30",
		"-i", rawIn, "-an",
		"-c:v", "libx264", "-preset", "veryfast",
		"-b:v", "6000k", "-maxrate", "6000k", "-bufsize", "12000k",
		"-g", "60",
		"-pix_fmt", "yuv420p", "-color_range", "pc",
		encoded,
	)

	// Decode leg: the mid-stream frame (n=45, mid-GOP), one process, both surfaces.
	runFFmpegCmd(t, ffmpeg,
		"-hide_banner", "-loglevel", "error", "-y",
		"-i", encoded,
		"-vf", `select=eq(n\,45)`, "-vsync", "0", "-frames:v", "1",
		"-f", "rawvideo", "-pix_fmt", "rgba", rgbaOut,
		"-vf", `select=eq(n\,45)`, "-vsync", "0", "-frames:v", "1",
		"-f", "rawvideo", "-pix_fmt", "yuv420p", yuvOut,
	)

	rgbaPix, err := os.ReadFile(rgbaOut)
	if err != nil {
		t.Fatal(err)
	}
	if want := FrameWidth * FrameRefHeight * 4; len(rgbaPix) != want {
		t.Fatalf("decoded RGBA frame = %d bytes, want %d (%s) - frame came back resized/malformed: CDN-RISK SIGNAL", len(rgbaPix), want, size)
	}
	yuvPix, err := os.ReadFile(yuvOut)
	if err != nil {
		t.Fatal(err)
	}
	if want := FrameWidth * FrameRefHeight * 3 / 2; len(yuvPix) != want {
		t.Fatalf("decoded YUV frame = %d bytes, want %d (%s yuv420p) - frame came back resized/malformed: CDN-RISK SIGNAL", len(yuvPix), want, size)
	}
	dec := &image.RGBA{Pix: rgbaPix, Stride: FrameWidth * 4, Rect: image.Rect(0, 0, FrameWidth, FrameRefHeight)}

	// Wire chroma neutrality: greys must stay grey through a transcode (a CDN applying a
	// colorspace/gamma conversion shifts this). Our own swscale leg already writes 127 - tolerate
	// 128+-3, log the actual values as runbook evidence.
	uAt := func(x, y int) byte {
		return yuvPix[FrameWidth*FrameRefHeight+(y/2)*(FrameWidth/2)+x/2]
	}
	vAt := func(x, y int) byte {
		return yuvPix[FrameWidth*FrameRefHeight+(FrameWidth/2)*(FrameRefHeight/2)+(y/2)*(FrameWidth/2)+x/2]
	}
	whiteX, whiteY := MetaBandX0+metaColCal2*MetaCellPx+MetaCellPx/2, MetaCellPx/2
	u, v := uAt(whiteX, whiteY), vAt(whiteX, whiteY)
	t.Logf("wire chroma at white cal cell: U=%d V=%d (neutral grey = 128; swscale full-range writes 127)", u, v)
	if u < 125 || u > 131 || v < 125 || v > 131 {
		t.Errorf("wire chroma U=%d V=%d, want 128+-3 - transcode is not colour-neutral: CDN-RISK SIGNAL", u, v)
	}

	wireLuma := func(x, y int) float64 { return float64(yuvPix[y*FrameWidth+x]) }
	rgbLuma := func(x, y int) float64 { // reader-faithful: BT.709 luminance (RaveVRSLGridReader mono path)
		c := dec.RGBAAt(x, y)
		return 0.2126*float64(c.R) + 0.7152*float64(c.G) + 0.0722*float64(c.B)
	}
	checkSurface(t, "wire-luma", wireLuma, 0)
	checkSurface(t, "rgb-receiver", rgbLuma, 1)
}

// checkSurface runs the full contract assertions against one decode surface. hdrTol is the header
// tolerance: 0 (exact) on the wire; 1 on the RGB receiver surface (chroma-bias skew, see header).
func checkSurface(t *testing.T, name string, lumaAt func(x, y int) float64, hdrTol int) {
	t.Run(name, func(t *testing.T) {
		cell := func(xOff, cx, cy int) float64 {
			return lumaAt(xOff+cx*CellPx+CellPx/2, cy*CellPx+CellPx/2)
		}
		meta := func(col, row int) float64 {
			return lumaAt(MetaBandX0+col*MetaCellPx+MetaCellPx/2, row*MetaCellPx+MetaCellPx/2)
		}

		// Calibration triad: pre-correction sanity, then the two-point gain/offset correction the
		// world reader applies to every sampled cell.
		rawBlack, rawMid, rawWhite := meta(metaColCal0, 0), meta(metaColCal1, 0), meta(metaColCal2, 0)
		t.Logf("calibration triad pre-correction: black=%.2f mid=%.2f white=%.2f (written 0/128/255)", rawBlack, rawMid, rawWhite)
		if rawBlack > 8 {
			t.Errorf("calibration BLACK = %.2f pre-correction, want <=8", rawBlack)
		}
		if rawWhite < 247 {
			t.Errorf("calibration WHITE = %.2f pre-correction, want >=247", rawWhite)
		}
		if t.Failed() {
			t.Fatal("calibration triad out of range - transport broke the level mapping (range/gamma): CDN-RISK SIGNAL")
		}
		cal := func(v float64) int {
			c := int(math.Round((v - rawBlack) * 255 / (rawWhite - rawBlack)))
			if c < 0 {
				c = 0
			}
			if c > 255 {
				c = 255
			}
			return c
		}
		if mid := cal(rawMid); mid < 128-6 || mid > 128+6 {
			t.Errorf("calibrated MID = %d, want 128+-6", mid)
		}

		// MAGIC (+-4/byte, read through calibration like the reader) + header cells.
		if m0 := cal(meta(metaColMagic0, 0)); m0 < MagicR-4 || m0 > MagicR+4 {
			t.Errorf("MAGIC0 = %d, want %d+-4", m0, MagicR)
		}
		if m1 := cal(meta(metaColMagic1, 0)); m1 < MagicV-4 || m1 > MagicV+4 {
			t.Errorf("MAGIC1 = %d, want %d+-4", m1, MagicV)
		}
		headers := []struct {
			name     string
			col, row int
			want     int
		}{
			{"version", metaColVersion, 0, Version},
			{"flags", metaColFlags, 0, FlagLoFrameValid}, // mono -> no FlagRGB9
			{"baseUniverse", metaColBaseUni, 0, 1},
			{"universeCount", metaColUniCount, 0, 1},
			{"frameCounter", metaColFrameCtr, 0, 200},
			{"lookId", metaLaneLookID, 1, 7},
			{"sceneId", metaLaneSceneID, 1, 3},
			{"blackout", metaLaneBlackout, 1, 0},
		}
		for _, h := range headers {
			if got := cal(meta(h.col, h.row)); abs(got-h.want) > hdrTol {
				t.Errorf("header %s = %d after calibration, want %d (+-%d)", h.name, got, h.want, hdrTol)
			}
		}

		// Channel cells: high-byte strip +-2 post-calibration; 16-bit combine (strip hi + LEFT low
		// mirror, bit-replicated at write time so v16written = v*257) within +-513.
		const hiTol, v16Tol = 2, 513
		var maxHi, maxLo, maxV16, badCells int
		for ch := 0; ch < ChPerUni; ch++ {
			wantV := int(transcodePattern(ch))
			cx, cy := cellForChannel(ch)
			hi := cal(cell(StripX0, cx, cy))
			lo := cal(cell(LowGridX0, cx, cy))

			errHi := abs(hi - wantV)
			errLo := abs(lo - wantV)
			errV16 := abs((hi<<8 | lo) - wantV*257)
			if errHi > maxHi {
				maxHi = errHi
			}
			if errLo > maxLo {
				maxLo = errLo
			}
			if errV16 > maxV16 {
				maxV16 = errV16
			}
			if errHi > hiTol || errV16 > v16Tol {
				badCells++
				if badCells <= 10 {
					t.Errorf("ch%d (cell %d,%d): written=%d hi=%d lo=%d errHi=%d errV16=%d (tol %d/%d)",
						ch, cx, cy, wantV, hi, lo, errHi, errV16, hiTol, v16Tol)
				}
			}
		}
		// Runbook evidence: observed maxima vs tolerances - do NOT tighten tolerances off one run.
		t.Logf("observed max errors post-calibration: high-byte=%d (tol %d), low-byte=%d, v16=%d (tol %d) over %d channels",
			maxHi, hiTol, maxLo, maxV16, v16Tol, ChPerUni)
		if badCells > 0 {
			pct := float64(badCells) * 100 / float64(ChPerUni)
			t.Errorf("%d/%d channel cells (%.2f%%) exceeded transport tolerance - CDN-RISK SIGNAL%s",
				badCells, ChPerUni, pct, map[bool]string{true: " (>1%)", false: ""}[pct > 1])
		}
	})
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

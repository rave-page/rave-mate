package worker

import (
	"bufio"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"rave.page/mate/internal/mediatools"
	"rave.page/mate/internal/musiclib"
)

// Golden cross-test (ZIG_MIGRATION P4): the Zig rave-probe exe must produce
// byte-identical peaks/bands payloads and matching envelope/tags for the same input as
// the in-Go probe handlers. Skips when zig-out/bin/rave-probe or ffmpeg/ffprobe are absent.

func zigProbeExe(t *testing.T) string {
	t.Helper()
	name := "rave-probe"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	p := filepath.Join("..", "..", "native", "zigcore", "zig-out", "bin", name)
	if _, err := os.Stat(p); err != nil {
		t.Skip("rave-probe not built (zig build -Drelease in native/zigcore)")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

// writeFixtureWAV writes a deterministic 2 s 44.1 kHz stereo s16le WAV: 220 Hz left,
// 440 Hz right, linear fade-in so peaks vary across buckets.
func writeFixtureWAV(t *testing.T) string {
	t.Helper()
	const (
		rate = 44100
		secs = 2
		n    = rate * secs
	)
	data := make([]byte, 4*n)
	for i := 0; i < n; i++ {
		ts := float64(i) / rate
		amp := 0.9 * float64(i) / n
		l := int16(amp * 32000 * math.Sin(2*math.Pi*220*ts))
		r := int16(amp * 32000 * math.Sin(2*math.Pi*440*ts))
		binary.LittleEndian.PutUint16(data[4*i:], uint16(l))
		binary.LittleEndian.PutUint16(data[4*i+2:], uint16(r))
	}
	var hdr []byte
	hdr = append(hdr, "RIFF"...)
	hdr = binary.LittleEndian.AppendUint32(hdr, uint32(36+len(data)))
	hdr = append(hdr, "WAVEfmt "...)
	hdr = binary.LittleEndian.AppendUint32(hdr, 16)
	hdr = binary.LittleEndian.AppendUint16(hdr, 1) // PCM
	hdr = binary.LittleEndian.AppendUint16(hdr, 2) // stereo
	hdr = binary.LittleEndian.AppendUint32(hdr, rate)
	hdr = binary.LittleEndian.AppendUint32(hdr, rate*4) // byte rate
	hdr = binary.LittleEndian.AppendUint16(hdr, 4)      // block align
	hdr = binary.LittleEndian.AppendUint16(hdr, 16)     // bits
	hdr = append(hdr, "data"...)
	hdr = binary.LittleEndian.AppendUint32(hdr, uint32(len(data)))
	path := filepath.Join(t.TempDir(), "fixture.wav")
	if err := os.WriteFile(path, append(hdr, data...), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// zigProbe drives the exe over its stdio protocol like the supervisor does.
type zigProbe struct {
	cmd   *exec.Cmd
	stdin *json.Encoder
	dec   *json.Decoder
	seq   int
}

func startZigProbe(t *testing.T, exe string) *zigProbe {
	t.Helper()
	cmd := exec.Command(exe)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	z := &zigProbe{cmd: cmd, stdin: json.NewEncoder(stdin), dec: json.NewDecoder(bufio.NewReader(stdout))}
	t.Cleanup(func() {
		_ = stdin.Close() // EOF → clean exit 0
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case werr := <-done:
			if werr != nil {
				t.Errorf("rave-probe did not exit cleanly on EOF: %v", werr)
			}
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			t.Error("rave-probe did not exit on stdin EOF")
		}
	})
	return z
}

func (z *zigProbe) call(t *testing.T, method string, params any) (json.RawMessage, string) {
	t.Helper()
	z.seq++
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	if err := z.stdin.Encode(Request{ID: fmt.Sprintf("%d", z.seq), Method: method, Params: raw}); err != nil {
		t.Fatal(err)
	}
	var resp Response
	if err := z.dec.Decode(&resp); err != nil {
		t.Fatalf("%s: decode response: %v", method, err)
	}
	if resp.ID != fmt.Sprintf("%d", z.seq) {
		t.Fatalf("%s: response id %q != request id %d", method, resp.ID, z.seq)
	}
	if !resp.OK {
		return nil, resp.Error
	}
	return resp.Result, ""
}

func goHandler(t *testing.T, h Handler, params any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	res, err := h(raw, func(string, any) {})
	if err != nil {
		t.Fatalf("go handler: %v", err)
	}
	return res
}

func TestZigProbeParity(t *testing.T) {
	if _, ok := mediatools.Resolve("ffmpeg"); !ok {
		t.Skip("ffmpeg not installed")
	}
	if _, ok := mediatools.Resolve("ffprobe"); !ok {
		t.Skip("ffprobe not installed")
	}
	exe := zigProbeExe(t)
	wav := writeFixtureWAV(t)
	z := startZigProbe(t, exe)

	t.Run("protocol", func(t *testing.T) {
		res, errMsg := z.call(t, "ping", nil)
		if errMsg != "" {
			t.Fatalf("ping: %s", errMsg)
		}
		var ping struct {
			Pong bool `json:"pong"`
			PID  int  `json:"pid"`
		}
		if err := json.Unmarshal(res, &ping); err != nil || !ping.Pong {
			t.Fatalf("ping result %s (err %v)", res, err)
		}
		if _, errMsg := z.call(t, "does.not.exist", nil); errMsg != "unknown method does.not.exist" {
			t.Fatalf("unknown-method error = %q", errMsg)
		}
		if _, errMsg := z.call(t, "probe.peaks", map[string]any{}); errMsg != "missing path" {
			t.Fatalf("missing-path error = %q", errMsg)
		}
	})

	type peaksOut struct {
		Peaks           string  `json:"peaks"`
		Bands           string  `json:"bands"`
		DurationSeconds float64 `json:"durationSeconds"`
		Rate            int     `json:"rate"`
		Samples         int     `json:"samples"`
		LeadSkipMs      float64 `json:"leadSkipMs"`
	}
	t.Run("peaks", func(t *testing.T) {
		params := map[string]any{"path": wav, "binRateHz": 100}
		var goRes, zigRes peaksOut
		if err := json.Unmarshal(goHandler(t, peaksHandler, params), &goRes); err != nil {
			t.Fatal(err)
		}
		res, errMsg := z.call(t, "probe.peaks", params)
		if errMsg != "" {
			t.Fatalf("zig probe.peaks: %s", errMsg)
		}
		if err := json.Unmarshal(res, &zigRes); err != nil {
			t.Fatal(err)
		}
		if zigRes.Peaks != goRes.Peaks {
			t.Errorf("peaks b64 differ (go %d chars, zig %d chars)", len(goRes.Peaks), len(zigRes.Peaks))
		}
		if zigRes.Bands != goRes.Bands {
			t.Errorf("bands b64 differ (go %d chars, zig %d chars)", len(goRes.Bands), len(zigRes.Bands))
		}
		if zigRes.Rate != goRes.Rate || zigRes.Samples != goRes.Samples || zigRes.LeadSkipMs != goRes.LeadSkipMs {
			t.Errorf("scalar mismatch: go %+v zig %+v", goRes, zigRes)
		}
		if math.Abs(zigRes.DurationSeconds-goRes.DurationSeconds) > 1e-9 {
			t.Errorf("duration: go %v zig %v", goRes.DurationSeconds, zigRes.DurationSeconds)
		}
	})

	type envOut struct {
		Env             string  `json:"env"`
		RateHz          float64 `json:"rateHz"`
		DurationSeconds float64 `json:"durationSeconds"`
	}
	t.Run("envelope", func(t *testing.T) {
		params := map[string]any{"path": wav, "rateHz": 50}
		var goRes, zigRes envOut
		if err := json.Unmarshal(goHandler(t, envelopeHandler, params), &goRes); err != nil {
			t.Fatal(err)
		}
		res, errMsg := z.call(t, "probe.envelope", params)
		if errMsg != "" {
			t.Fatalf("zig probe.envelope: %s", errMsg)
		}
		if err := json.Unmarshal(res, &zigRes); err != nil {
			t.Fatal(err)
		}
		if zigRes.RateHz != goRes.RateHz {
			t.Errorf("rateHz: go %v zig %v", goRes.RateHz, zigRes.RateHz)
		}
		if math.Abs(zigRes.DurationSeconds-goRes.DurationSeconds) > 1e-9 {
			t.Errorf("duration: go %v zig %v", goRes.DurationSeconds, zigRes.DurationSeconds)
		}
		if zigRes.Env == goRes.Env {
			return // byte-exact (expected: identical f64 op order)
		}
		// Tolerance fallback: same length, per-bucket |Δ| ≤ 1e-6.
		gb, err1 := base64.StdEncoding.DecodeString(goRes.Env)
		zb, err2 := base64.StdEncoding.DecodeString(zigRes.Env)
		if err1 != nil || err2 != nil || len(gb) != len(zb) || len(gb)%4 != 0 {
			t.Fatalf("env payloads not comparable: go %d bytes zig %d bytes", len(gb), len(zb))
		}
		for i := 0; i+4 <= len(gb); i += 4 {
			gv := math.Float32frombits(binary.LittleEndian.Uint32(gb[i:]))
			zv := math.Float32frombits(binary.LittleEndian.Uint32(zb[i:]))
			if math.Abs(float64(gv-zv)) > 1e-6 {
				t.Fatalf("env bucket %d: go %v zig %v", i/4, gv, zv)
			}
		}
		t.Log("env within 1e-6 tolerance (not byte-exact)")
	})

	t.Run("tags", func(t *testing.T) {
		params := map[string]any{"path": wav}
		var goRes, zigRes musiclib.Track
		if err := json.Unmarshal(goHandler(t, tagsHandler, params), &goRes); err != nil {
			t.Fatal(err)
		}
		res, errMsg := z.call(t, "probe.tags", params)
		if errMsg != "" {
			t.Fatalf("zig probe.tags: %s", errMsg)
		}
		if err := json.Unmarshal(res, &zigRes); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(goRes, zigRes) {
			t.Errorf("tags mismatch:\n go: %+v\nzig: %+v", goRes, zigRes)
		}
	})

	t.Run("duration", func(t *testing.T) {
		params := map[string]any{"path": wav}
		type durOut struct {
			DurationSeconds *float64 `json:"durationSeconds"`
		}
		var goRes, zigRes durOut
		if err := json.Unmarshal(goHandler(t, durationHandler, params), &goRes); err != nil {
			t.Fatal(err)
		}
		res, errMsg := z.call(t, "probe.duration", params)
		if errMsg != "" {
			t.Fatalf("zig probe.duration: %s", errMsg)
		}
		if err := json.Unmarshal(res, &zigRes); err != nil {
			t.Fatal(err)
		}
		if (goRes.DurationSeconds == nil) != (zigRes.DurationSeconds == nil) {
			t.Fatalf("duration nil mismatch: go %v zig %v", goRes.DurationSeconds, zigRes.DurationSeconds)
		}
		if goRes.DurationSeconds != nil && math.Abs(*goRes.DurationSeconds-*zigRes.DurationSeconds) > 1e-9 {
			t.Errorf("duration: go %v zig %v", *goRes.DurationSeconds, *zigRes.DurationSeconds)
		}
	})
}

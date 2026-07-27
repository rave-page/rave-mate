package encoderscan

import (
	"strings"
	"testing"
)

func TestFamilyFromOBSID(t *testing.T) {
	cases := map[string]EncoderFamily{
		"jim_nvenc":          FamilyNVENC,
		"obs_nvenc_h264_tex": FamilyNVENC,
		"ffmpeg_hevc_nvenc":  FamilyNVENC,
		"nvenc":              FamilyNVENC,
		"h264_texture_amf":   FamilyAMF,
		"amd":                FamilyAMF,
		"obs_qsv11":          FamilyQSV,
		"qsv":                FamilyQSV,
		"obs_x264":           FamilyX264,
		"x264":               FamilyX264,
		"":                   FamilyUnknown,
		"weird_future_enc":   FamilyUnknown,
	}
	for id, want := range cases {
		if got := FamilyFromOBSID(id); got != want {
			t.Errorf("FamilyFromOBSID(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestScanProtectsActiveOBSAndParsec(t *testing.T) {
	d := Deps{
		OBSEncoder: func() (string, string, bool, error) { return "jim_nvenc", "", true, nil },
		Processes: func() ([]Proc, error) {
			return []Proc{{Name: "obs64.exe", PID: 100}, {Name: "parsecd.exe", PID: 200}, {Name: "VRChat.exe", PID: 300}}, nil
		},
		GPU: func() ([]GPUSample, error) {
			return []GPUSample{
				{PID: 100, Adapter: "gpu0", AdapterName: "RTX 4090", EncodePct: 40},
				{PID: 200, Adapter: "gpu0", AdapterName: "RTX 4090", EncodePct: 25},
				{PID: 300, Adapter: "gpu0", AdapterName: "RTX 4090", EncodePct: 5},
			}, nil
		},
		ParsecEncoder: func() (EncoderFamily, string, bool) { return FamilyNVENC, "", true },
	}
	r := Scan(d)

	// OBS on gpu0 NVENC and Parsec on gpu0 → gpu0 is protected, NVENC family protected.
	if !r.ProtectedAdapter["gpu0"] {
		t.Fatalf("gpu0 should be protected (OBS+Parsec encoding on it): %+v", r.ProtectedAdapter)
	}
	if !r.ProtectedFamily[FamilyNVENC] {
		t.Fatalf("NVENC family should be protected: %+v", r.ProtectedFamily)
	}
	// OBS consumer must be Critical (active) with the live adapter/util attached.
	var obs *Consumer
	for i := range r.Consumers {
		if r.Consumers[i].Role == "obs" {
			obs = &r.Consumers[i]
		}
	}
	if obs == nil || !obs.Critical || obs.Adapter != "gpu0" || obs.EncPct != 40 {
		t.Fatalf("obs consumer wrong: %+v", obs)
	}
}

func TestScanFamilyOnlyWhenNoGPUUtil(t *testing.T) {
	// No GPU sampler (non-Windows / no counters) + OBS active → protect by family, not adapter.
	d := Deps{
		OBSEncoder: func() (string, string, bool, error) { return "h264_texture_amf", "", true, nil },
	}
	r := Scan(d)
	if !r.ProtectedFamily[FamilyAMF] {
		t.Fatalf("AMF family should be protected from config alone: %+v", r.ProtectedFamily)
	}
	if len(r.ProtectedAdapter) != 0 {
		t.Fatalf("no adapter should be protected without util data: %+v", r.ProtectedAdapter)
	}
}

func TestParsecEncoderFromLog(t *testing.T) {
	cases := map[string]EncoderFamily{
		"[I] video: opened NVENC H265 encoder\n":      FamilyNVENC,
		"encoder: AMD AMF VCE h264\n":                 FamilyAMF,
		"selected encoder QSV (Intel Quick Sync)\n":   FamilyQSV,
		"[I] some unrelated line\n[I] another line\n": FamilyUnknown,
	}
	for log, want := range cases {
		got, ok := parsecEncoderFromLog(log)
		if want == FamilyUnknown {
			if ok {
				t.Errorf("parsecEncoderFromLog(%q) = %q, want no match", log, got)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("parsecEncoderFromLog(%q) = %q ok=%v, want %q", log, got, ok, want)
		}
	}
	// Newest matching line wins.
	log := "encoder: NVENC\nlater re-negotiated encoder: AMD AMF\n"
	if got, ok := parsecEncoderFromLog(log); !ok || got != FamilyAMF {
		t.Errorf("newest-wins: got %q ok=%v, want amf", got, ok)
	}
}

func TestOBSEncoderFromProfile(t *testing.T) {
	simple := parseINI("[Output]\nMode=Simple\n[SimpleOutput]\nStreamEncoder=jim_nvenc\nRecEncoder=obs_x264\n")
	if s, r := obsEncoderFromProfile(simple); s != "jim_nvenc" || r != "obs_x264" {
		t.Fatalf("simple mode: got stream=%q rec=%q", s, r)
	}
	adv := parseINI("[Output]\nMode=Advanced\n[AdvOut]\nEncoder=h264_texture_amf\nRecEncoder=jim_hevc_nvenc\n[SimpleOutput]\nStreamEncoder=should_ignore\n")
	if s, r := obsEncoderFromProfile(adv); s != "h264_texture_amf" || r != "jim_hevc_nvenc" {
		t.Fatalf("advanced mode: got stream=%q rec=%q", s, r)
	}
	// Family mapping end-to-end.
	if got := FamilyFromOBSID("h264_texture_amf"); got != FamilyAMF {
		t.Fatalf("family: %q", got)
	}
}

func TestScanIdleOBSNotCritical(t *testing.T) {
	// OBS configured but NOT streaming and not encoding → recorded but not protected.
	d := Deps{
		OBSEncoder: func() (string, string, bool, error) { return "jim_nvenc", "", false, nil },
		Processes:  func() ([]Proc, error) { return []Proc{{Name: "obs64.exe", PID: 100}}, nil },
		GPU:        func() ([]GPUSample, error) { return nil, nil }, // idle: no encode util
	}
	r := Scan(d)
	if len(r.ProtectedAdapter) != 0 || r.ProtectedFamily[FamilyNVENC] {
		t.Fatalf("idle OBS must not protect anything: adapter=%+v family=%+v", r.ProtectedAdapter, r.ProtectedFamily)
	}
	if len(r.Consumers) != 1 || r.Consumers[0].Critical {
		t.Fatalf("idle OBS should be a non-critical consumer: %+v", r.Consumers)
	}
}

// TestReportListsIdleAdaptersFieldTopology is the SUS regression: an iGPU driving the display
// (busy, so PDH has GPU-Engine instances for it) plus an IDLE discrete GPU (no instances at all).
// The report used to render its adapter line from AdapterEncPct - the PDH-derived map - so the
// discrete card, i.e. THE GOOD ENCODER, never appeared at all. Verified in the field: a Radeon RX
// 7900 XTX was invisible to `ctl encoder-scan` while the Ryzen iGPU always showed.
func TestReportListsIdleAdaptersFieldTopology(t *testing.T) {
	const igpu = "0x00000000_0x00018ed4"
	const dgpu = "0x00000000_0x000163a8"
	r := Report{
		AdapterNames: map[string]string{
			igpu: "AMD Radeon(TM) Graphics",
			dgpu: "AMD Radeon RX 7900 XTX",
		},
		AdapterEncPct:    map[string]float64{igpu: 12}, // ONLY the busy iGPU was sampled
		AdapterVRAMFree:  map[string]float64{dgpu: 20000},
		ProtectedAdapter: map[string]bool{},
		ProtectedFamily:  map[EncoderFamily]bool{},
	}
	out := r.String()
	t.Logf("report:\n%s", out)
	if !strings.Contains(out, dgpu) {
		t.Fatalf("idle discrete GPU %s missing from the scan - a device policy reading this can only pick the wrong GPU:\n%s", dgpu, out)
	}
	if !strings.Contains(out, igpu) {
		t.Errorf("busy iGPU %s missing:\n%s", igpu, out)
	}
	// The idle adapter must be distinguishable from a measured 0%.
	if !strings.Contains(out, "enc=?") {
		t.Errorf("idle adapter should read enc=? (no counters), not a measured value:\n%s", out)
	}
	// Encoder capability per adapter (what each one would encode ON) is the other half of the ask.
	if !strings.Contains(out, string(FamilyAMF)) {
		t.Errorf("adapter rows do not name the encoder family:\n%s", out)
	}
	// Two same-vendor GPUs: the encoder→adapter join cannot bind, and that must be stated.
	if !strings.Contains(out, "ambiguous") {
		t.Errorf("same-vendor ambiguity not reported - device rows would silently read 'device=?':\n%s", out)
	}
}

// TestReportAdapterRowsSurviveMissingNames: PDH sampled an adapter DXGI did not enumerate. Never
// hide it - an unexplained key beats a missing GPU.
func TestReportAdapterRowsSurviveMissingNames(t *testing.T) {
	r := Report{
		AdapterNames:  map[string]string{},
		AdapterEncPct: map[string]float64{"0x0_0x1": 7},
	}
	if out := r.String(); !strings.Contains(out, "0x0_0x1") {
		t.Fatalf("sampled-but-unnamed adapter dropped:\n%s", out)
	}
}

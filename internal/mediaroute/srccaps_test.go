package mediaroute

import (
	"testing"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/medialink"
)

// The Offer's bitrate is min(global budget, per-source cap) in BOTH bitrate modes: fixed = the
// configured number capped; auto = the quality-model target (under the global ceiling) capped.
func TestOfferCarriesSourceCap(t *testing.T) {
	desc := medialink.SourceDesc{ID: "spout:4K", Name: "4K", Kind: medialink.KindVideo, Width: 3840, Height: 2160, FPS: 60}
	autoModel := medialink.AdaptiveBaseKbps(medialink.BitrateAuto, 50000, 50000, medialink.CodecH264, 3840, 2160, 60)
	if autoModel <= 12000 {
		t.Fatalf("fixture: 4K60 model target %d must exceed the 12000 cap", autoModel)
	}
	for _, tc := range []struct {
		name      string
		mode      string
		global    int
		capK      int
		want      int
		wantQMode bool
	}{
		{"fixed, cap below global", "fixed", 20000, 8000, 8000, false},
		{"fixed, cap above global", "fixed", 20000, 30000, 20000, false},
		{"fixed, no cap", "fixed", 20000, 0, 20000, false},
		{"auto, cap below model", "auto", 50000, 12000, 12000, true},
		{"auto, no cap", "auto", 50000, 0, autoModel, true},
	} {
		fr := newFakeRouter()
		fr.adverts["peerB"] = advertWith(desc, "hevc_nvenc")
		cfg := config.MediaLinkFeature{BitrateKbps: tc.global, BitrateMode: tc.mode,
			RememberedReceives: []config.RememberedReceive{{Peer: "peerB", SourceID: "spout:4K", Name: "4K", MaxKbps: tc.capK, MaxFPS: 30}}}
		m := testManager(fr, cfg, nil)
		if _, err := m.StartReceive("peerB", "spout:4K"); err != nil {
			t.Fatalf("%s: StartReceive: %v", tc.name, err)
		}
		fr.mu.Lock()
		opt := fr.offers[0]
		fr.mu.Unlock()
		if opt.BitrateKbps != tc.want || opt.QualityMode != tc.wantQMode {
			t.Errorf("%s: offer %+v, want bitrate %d qm=%v", tc.name, opt, tc.want, tc.wantQMode)
		}
		if _, f := m.SourceCaps("peerB", "spout:4K"); f != 30 {
			t.Errorf("%s: fps cap = %d, want 30 (feeds the governor's MetaRate hint)", tc.name, f)
		}
	}
}

// A live push (SetSourceCaps) wins over the config snapshot - including an explicit clear, because
// the media child's config copy is only the spawn snapshot and would otherwise resurrect old caps.
func TestSourceCapsLiveOverride(t *testing.T) {
	fr := newFakeRouter()
	cfg := config.MediaLinkFeature{RememberedReceives: []config.RememberedReceive{{Peer: "p", SourceID: "s", MaxKbps: 8000, MaxFPS: 30}}}
	m := testManager(fr, cfg, nil)
	if k, f := m.SourceCaps("p", "s"); k != 8000 || f != 30 {
		t.Fatalf("snapshot caps = %d/%d", k, f)
	}
	if err := m.SetSourceCaps("p", "s", 12000, 0); err != nil {
		t.Fatal(err)
	}
	if k, f := m.SourceCaps("p", "s"); k != 12000 || f != 0 {
		t.Fatalf("override caps = %d/%d, want 12000/0", k, f)
	}
	if err := m.SetSourceCaps("p", "s", 0, 0); err != nil {
		t.Fatal(err)
	}
	if k, f := m.SourceCaps("p", "s"); k != 0 || f != 0 {
		t.Fatalf("cleared caps = %d/%d, snapshot resurrected", k, f)
	}
	if err := m.SetSourceCaps("p", "s", 500, 0); err == nil {
		t.Fatal("out-of-range kbps accepted")
	}
}

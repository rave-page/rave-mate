package obs

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func samplePreset() Preset {
	return Preset{
		Profile:         "Live",
		SceneCollection: "Rave",
		StreamService: StreamServiceSettings{
			Type:     "rtmp_custom",
			Settings: map[string]any{"server": "rtmp://ingest.example/live", "key": "k1", "use_auth": false},
		},
		Video: VideoSettings{
			FpsNumerator: 60, FpsDenominator: 1,
			BaseWidth: 2560, BaseHeight: 1440,
			OutputWidth: 1920, OutputHeight: 1080,
		},
		OutputParams: []ProfileParameter{
			{Category: "Output", Name: "Mode", Value: "Simple"},
			{Category: "SimpleOutput", Name: "VBitrate", Value: "6000"},
		},
		CapturedAt: "2026-07-06T12:00:00Z",
	}
}

func TestPresetJSONRoundTrip(t *testing.T) {
	in := samplePreset()
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Preset
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// map values decode as float64/bool/string - compare via re-marshal.
	raw2, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	if string(raw) != string(raw2) {
		t.Fatalf("roundtrip drift:\n%s\n%s", raw, raw2)
	}
	if out.Profile != in.Profile || out.SceneCollection != in.SceneCollection ||
		out.Video != in.Video || !reflect.DeepEqual(out.OutputParams, in.OutputParams) {
		t.Fatalf("fields drifted: %+v", out)
	}
}

func TestVideoSettingsMarshalOmitsZero(t *testing.T) {
	raw, err := json.Marshal(VideoSettings{OutputWidth: 1280, OutputHeight: 720})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(raw)
	if strings.Contains(s, "fps") || strings.Contains(s, "base") {
		t.Fatalf("zero fields not omitted: %s", s)
	}
	if !strings.Contains(s, `"outputWidth":1280`) || !strings.Contains(s, `"outputHeight":720`) {
		t.Fatalf("set fields missing: %s", s)
	}
	if !(VideoSettings{}).IsZero() || (VideoSettings{BaseWidth: 1}).IsZero() {
		t.Fatal("IsZero wrong")
	}
}

func TestOutputParamKeys(t *testing.T) {
	adv := outputParamKeys("Advanced")
	for _, k := range adv {
		if k.Category != "AdvOut" {
			t.Fatalf("advanced key in %q", k.Category)
		}
	}
	if got := outputParamKeys("advanced"); !reflect.DeepEqual(got, adv) {
		t.Fatal("mode match not case-insensitive")
	}
	for _, mode := range []string{"Simple", "", "garbage"} {
		for _, k := range outputParamKeys(mode) {
			if k.Category != "SimpleOutput" {
				t.Fatalf("mode %q: key in %q", mode, k.Category)
			}
		}
	}
}

func TestPresetIsEmpty(t *testing.T) {
	if !(Preset{}).IsEmpty() {
		t.Fatal("zero preset not empty")
	}
	if (Preset{CapturedAt: "2026-07-06T12:00:00Z"}).IsEmpty() != true {
		t.Fatal("capturedAt alone should still be empty")
	}
	cases := []Preset{
		{Profile: "Live"},
		{SceneCollection: "Rave"},
		{StreamService: StreamServiceSettings{Type: "rtmp_custom"}},
		{StreamService: StreamServiceSettings{Settings: map[string]any{"server": "x"}}},
		{Video: VideoSettings{OutputWidth: 1}},
		{OutputParams: []ProfileParameter{{Category: "Output", Name: "Mode", Value: "Simple"}}},
	}
	for i, p := range cases {
		if p.IsEmpty() {
			t.Fatalf("case %d wrongly empty", i)
		}
	}
}

func TestContainsFold(t *testing.T) {
	list := []string{"Default", "Live IRL"}
	if !containsFold(list, "live irl") || containsFold(list, "Live") {
		t.Fatal("containsFold wrong")
	}
}

package obs

import (
	"encoding/json"
	"testing"
	"time"
)

// golden marshals v and asserts the exact JSON matches want (byte-exact wire shape).
func golden(t *testing.T, v any, want string) {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(raw) != want {
		t.Errorf("wire mismatch\n got: %s\nwant: %s", raw, want)
	}
}

func TestMediaRequestGoldenJSON(t *testing.T) {
	golden(t, setInputAudioSyncOffsetData{InputName: "Video", InputAudioSyncOffset: -120},
		`{"inputName":"Video","inputAudioSyncOffset":-120}`)
	golden(t, getMediaInputStatusData{InputName: "Video"},
		`{"inputName":"Video"}`)
	golden(t, setMediaInputCursorData{InputName: "Video", MediaCursor: 42000},
		`{"inputName":"Video","mediaCursor":42000}`)
	golden(t, offsetMediaInputCursorData{InputName: "Video", MediaCursorOffset: -35},
		`{"inputName":"Video","mediaCursorOffset":-35}`)
	golden(t, triggerMediaInputActionData{InputName: "Video", MediaAction: MediaActionRestart},
		`{"inputName":"Video","mediaAction":"OBS_WEBSOCKET_MEDIA_INPUT_ACTION_RESTART"}`)
	golden(t, setSourceFilterSettingsData{SourceName: "Video", FilterName: "gpu_delay",
		FilterSettings: map[string]any{"delay_ms": 120}, Overlay: true},
		`{"sourceName":"Video","filterName":"gpu_delay","filterSettings":{"delay_ms":120},"overlay":true}`)
	golden(t, createSourceFilterData{SourceName: "Video", FilterName: "rave-delay",
		FilterKind: FilterKindGPUDelay, FilterSettings: map[string]any{"delay_ms": 120}},
		`{"sourceName":"Video","filterName":"rave-delay","filterKind":"gpu_delay","filterSettings":{"delay_ms":120}}`)
	golden(t, createSourceFilterData{SourceName: "Video", FilterName: "rave-delay", FilterKind: FilterKindAsyncDelay},
		`{"sourceName":"Video","filterName":"rave-delay","filterKind":"async_delay_filter"}`)
	golden(t, setSourceFilterEnabledData{SourceName: "Video", FilterName: "rave-delay", FilterEnabled: true},
		`{"sourceName":"Video","filterName":"rave-delay","filterEnabled":true}`)
	golden(t, getSourceFilterListData{SourceName: "Video"},
		`{"sourceName":"Video"}`)
}

func TestGetSourceFilterListDecode(t *testing.T) {
	raw := `{"filters":[{"filterEnabled":true,"filterIndex":0,"filterKind":"gpu_delay","filterName":"rave-delay","filterSettings":{"delay_ms":120}}]}`
	var v struct {
		Filters []FilterInfo `json:"filters"`
	}
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Filters) != 1 {
		t.Fatalf("filters = %d, want 1", len(v.Filters))
	}
	f := v.Filters[0]
	if !f.Enabled || f.Kind != FilterKindGPUDelay || f.Name != "rave-delay" {
		t.Errorf("decoded filter = %+v", f)
	}
	if dm, _ := f.Settings["delay_ms"].(float64); dm != 120 {
		t.Errorf("delay_ms = %v, want 120", f.Settings["delay_ms"])
	}
}

func TestGetMediaInputStatusDecode(t *testing.T) {
	raw := `{"mediaState":"OBS_WEBSOCKET_MEDIA_INPUT_STATE_PLAYING","mediaDuration":300000,"mediaCursor":42500}`
	var v struct {
		State    string  `json:"mediaState"`
		Duration float64 `json:"mediaDuration"`
		Cursor   float64 `json:"mediaCursor"`
	}
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatal(err)
	}
	st := MediaInputStatus{State: v.State, Duration: msToDur(v.Duration), Cursor: msToDur(v.Cursor)}
	if !st.Playing() {
		t.Errorf("Playing() = false, want true for %q", st.State)
	}
	if st.Cursor != 42500*time.Millisecond {
		t.Errorf("Cursor = %v, want 42.5s", st.Cursor)
	}
	if st.Duration != 300*time.Second {
		t.Errorf("Duration = %v, want 300s", st.Duration)
	}
}

func TestMsToDurUnknown(t *testing.T) {
	if got := msToDur(-1); got != 0 {
		t.Errorf("msToDur(-1) = %v, want 0", got)
	}
}

func TestSeekKeyframeSnapped(t *testing.T) {
	if SeekKeyframeSnapped(KindVLCSource) {
		t.Error("vlc_source should be frame-exact (not keyframe-snapped)")
	}
	if !SeekKeyframeSnapped(KindMediaSource) {
		t.Error("ffmpeg_source (Media Source) should be keyframe-snapped")
	}
}

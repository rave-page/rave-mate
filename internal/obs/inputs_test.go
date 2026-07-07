package obs

import (
	"encoding/json"
	"testing"
)

// roundtrip marshals v then unmarshals into a generic map for shape assertions.
func roundtrip(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

func TestCreateInputPayloadShape(t *testing.T) {
	m := roundtrip(t, createInputData{
		SceneName:        "Scene",
		InputName:        "RaveMate Deck A Text",
		InputKind:        "text_gdiplus_v3",
		InputSettings:    map[string]any{"text": "hi"},
		SceneItemEnabled: true,
	})
	for _, k := range []string{"sceneName", "inputName", "inputKind", "inputSettings", "sceneItemEnabled"} {
		if _, ok := m[k]; !ok {
			t.Errorf("CreateInput payload missing key %q: %v", k, m)
		}
	}
	if m["inputKind"] != "text_gdiplus_v3" {
		t.Errorf("inputKind = %v", m["inputKind"])
	}
	settings, ok := m["inputSettings"].(map[string]any)
	if !ok || settings["text"] != "hi" {
		t.Errorf("inputSettings = %v", m["inputSettings"])
	}
}

func TestSetInputSettingsPayloadShape(t *testing.T) {
	m := roundtrip(t, setInputSettingsData{
		InputName:     "RaveMate Deck A Art",
		InputSettings: map[string]any{"file": "C:/art/a.jpg"},
		Overlay:       true,
	})
	if m["inputName"] != "RaveMate Deck A Art" {
		t.Errorf("inputName = %v", m["inputName"])
	}
	if m["overlay"] != true {
		t.Errorf("overlay = %v", m["overlay"])
	}
	settings, ok := m["inputSettings"].(map[string]any)
	if !ok || settings["file"] != "C:/art/a.jpg" {
		t.Errorf("inputSettings = %v", m["inputSettings"])
	}
}

func TestSetSceneItemTransformPayloadShape(t *testing.T) {
	m := roundtrip(t, setSceneItemTransformData{
		SceneName:          "Scene",
		SceneItemID:        7,
		SceneItemTransform: map[string]any{"positionX": 40.0, "positionY": 120.0},
	})
	if m["sceneItemId"].(float64) != 7 {
		t.Errorf("sceneItemId = %v", m["sceneItemId"])
	}
	tr, ok := m["sceneItemTransform"].(map[string]any)
	if !ok || tr["positionX"].(float64) != 40 || tr["positionY"].(float64) != 120 {
		t.Errorf("sceneItemTransform = %v", m["sceneItemTransform"])
	}
}

func TestSceneItemRequestPayloads(t *testing.T) {
	en := roundtrip(t, setSceneItemEnabledData{SceneName: "S", SceneItemID: 3, SceneItemEnabled: false})
	if en["sceneItemId"].(float64) != 3 || en["sceneItemEnabled"] != false {
		t.Errorf("SetSceneItemEnabled payload = %v", en)
	}
	gid := roundtrip(t, getSceneItemIDData{SceneName: "S", SourceName: "Src"})
	if gid["sceneName"] != "S" || gid["sourceName"] != "Src" {
		t.Errorf("GetSceneItemId payload = %v", gid)
	}
	ci := roundtrip(t, createSceneItemData{SceneName: "S", SourceName: "Src", SceneItemEnabled: true})
	if ci["sourceName"] != "Src" || ci["sceneItemEnabled"] != true {
		t.Errorf("CreateSceneItem payload = %v", ci)
	}
}

func TestInputInfoDecode(t *testing.T) {
	raw := `{"inputs":[{"inputName":"Mic","inputKind":"wasapi_input_capture","unversionedInputKind":"wasapi_input_capture"}]}`
	var v struct {
		Inputs []InputInfo `json:"inputs"`
	}
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Inputs) != 1 || v.Inputs[0].Name != "Mic" || v.Inputs[0].Kind != "wasapi_input_capture" {
		t.Errorf("decoded = %+v", v.Inputs)
	}
}

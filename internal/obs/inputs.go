package obs

// obs-websocket v5 input + scene-item request helpers used by the overlayobs renderer
// (per-deck Text/Image inputs created + positioned directly in OBS). All follow the
// existing request-correlator pattern (requestType + requestData via c.Request).

import (
	"context"
	"encoding/json"
	"fmt"
)

// ── request-data shapes (exported for payload tests; marshalled into requestData) ──

// createInputData is the GetInputKind-typed CreateInput payload.
type createInputData struct {
	SceneName        string         `json:"sceneName"`
	InputName        string         `json:"inputName"`
	InputKind        string         `json:"inputKind"`
	InputSettings    map[string]any `json:"inputSettings,omitempty"`
	SceneItemEnabled bool           `json:"sceneItemEnabled"`
}

type setInputSettingsData struct {
	InputName     string         `json:"inputName"`
	InputSettings map[string]any `json:"inputSettings"`
	Overlay       bool           `json:"overlay"`
}

type getSceneItemIDData struct {
	SceneName  string `json:"sceneName"`
	SourceName string `json:"sourceName"`
}

type createSceneItemData struct {
	SceneName        string `json:"sceneName"`
	SourceName       string `json:"sourceName"`
	SceneItemEnabled bool   `json:"sceneItemEnabled"`
}

type setSceneItemEnabledData struct {
	SceneName        string `json:"sceneName"`
	SceneItemID      int    `json:"sceneItemId"`
	SceneItemEnabled bool   `json:"sceneItemEnabled"`
}

type setSceneItemTransformData struct {
	SceneName          string         `json:"sceneName"`
	SceneItemID        int            `json:"sceneItemId"`
	SceneItemTransform map[string]any `json:"sceneItemTransform"`
}

// ── typed results ──

// InputInfo is one entry from GetInputList.
type InputInfo struct {
	Name            string `json:"inputName"`
	Kind            string `json:"inputKind"`
	UnversionedKind string `json:"unversionedInputKind"`
}

// SceneItem is one entry from GetSceneItemList.
type SceneItem struct {
	ID         int    `json:"sceneItemId"`
	SourceName string `json:"sourceName"`
	SourceKind string `json:"inputKind"`
}

// CreateInputParams describes a new input + its scene item to create.
type CreateInputParams struct {
	SceneName        string
	InputName        string
	InputKind        string
	InputSettings    map[string]any
	SceneItemEnabled bool
}

// ── requests ──

// GetCurrentProgramScene returns the active program scene name.
func (c *Client) GetCurrentProgramScene(ctx context.Context) (string, error) {
	raw, err := c.Request(ctx, "GetCurrentProgramScene", nil)
	if err != nil {
		return "", err
	}
	var v struct {
		Current string `json:"currentProgramSceneName"`
		// newer obs-websocket builds renamed this field; accept both.
		SceneName string `json:"sceneName"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", fmt.Errorf("obs GetCurrentProgramScene decode: %w", err)
	}
	if v.Current != "" {
		return v.Current, nil
	}
	return v.SceneName, nil
}

// GetInputList returns all inputs (optionally filtered by kind; "" = all).
func (c *Client) GetInputList(ctx context.Context, kind string) ([]InputInfo, error) {
	var data any
	if kind != "" {
		data = map[string]string{"inputKind": kind}
	}
	raw, err := c.Request(ctx, "GetInputList", data)
	if err != nil {
		return nil, err
	}
	var v struct {
		Inputs []InputInfo `json:"inputs"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("obs GetInputList decode: %w", err)
	}
	return v.Inputs, nil
}

// GetSceneItemList returns the scene items in sceneName.
func (c *Client) GetSceneItemList(ctx context.Context, sceneName string) ([]SceneItem, error) {
	raw, err := c.Request(ctx, "GetSceneItemList", map[string]string{"sceneName": sceneName})
	if err != nil {
		return nil, err
	}
	var v struct {
		Items []SceneItem `json:"sceneItems"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("obs GetSceneItemList decode: %w", err)
	}
	return v.Items, nil
}

// CreateInput creates an input + adds it to a scene, returning the new sceneItemId.
func (c *Client) CreateInput(ctx context.Context, p CreateInputParams) (int, error) {
	raw, err := c.Request(ctx, "CreateInput", createInputData(p))
	if err != nil {
		return 0, err
	}
	var v struct {
		SceneItemID int `json:"sceneItemId"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, fmt.Errorf("obs CreateInput decode: %w", err)
	}
	return v.SceneItemID, nil
}

// SetInputSettings updates an input's settings. overlay=true merges (keeps unspecified keys).
func (c *Client) SetInputSettings(ctx context.Context, inputName string, settings map[string]any, overlay bool) error {
	_, err := c.Request(ctx, "SetInputSettings", setInputSettingsData{
		InputName: inputName, InputSettings: settings, Overlay: overlay,
	})
	return err
}

type pressInputPropertiesButtonData struct {
	InputName    string `json:"inputName"`
	PropertyName string `json:"propertyName"`
}

// PressInputPropertiesButton clicks a button in an input's Properties dialog. For a browser_source,
// propertyName "refreshnocache" is OBS's "Refresh cache of current page" → reloads the latest HTML.
func (c *Client) PressInputPropertiesButton(ctx context.Context, inputName, propertyName string) error {
	_, err := c.Request(ctx, "PressInputPropertiesButton", pressInputPropertiesButtonData{InputName: inputName, PropertyName: propertyName})
	return err
}

// GetSceneItemID resolves the sceneItemId of sourceName within sceneName.
func (c *Client) GetSceneItemID(ctx context.Context, sceneName, sourceName string) (int, error) {
	raw, err := c.Request(ctx, "GetSceneItemId", getSceneItemIDData{SceneName: sceneName, SourceName: sourceName})
	if err != nil {
		return 0, err
	}
	var v struct {
		SceneItemID int `json:"sceneItemId"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, fmt.Errorf("obs GetSceneItemId decode: %w", err)
	}
	return v.SceneItemID, nil
}

// CreateSceneItem adds an existing source to sceneName, returning the new sceneItemId.
func (c *Client) CreateSceneItem(ctx context.Context, sceneName, sourceName string, enabled bool) (int, error) {
	raw, err := c.Request(ctx, "CreateSceneItem", createSceneItemData{
		SceneName: sceneName, SourceName: sourceName, SceneItemEnabled: enabled,
	})
	if err != nil {
		return 0, err
	}
	var v struct {
		SceneItemID int `json:"sceneItemId"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, fmt.Errorf("obs CreateSceneItem decode: %w", err)
	}
	return v.SceneItemID, nil
}

// SetSceneItemEnabled shows/hides a scene item.
func (c *Client) SetSceneItemEnabled(ctx context.Context, sceneName string, itemID int, enabled bool) error {
	_, err := c.Request(ctx, "SetSceneItemEnabled", setSceneItemEnabledData{
		SceneName: sceneName, SceneItemID: itemID, SceneItemEnabled: enabled,
	})
	return err
}

// SetSceneItemTransform sets a scene item's transform (position/scale/etc.) - keys per the
// obs-websocket SceneItemTransform object (e.g. positionX/positionY/boundsWidth).
func (c *Client) SetSceneItemTransform(ctx context.Context, sceneName string, itemID int, transform map[string]any) error {
	_, err := c.Request(ctx, "SetSceneItemTransform", setSceneItemTransformData{
		SceneName: sceneName, SceneItemID: itemID, SceneItemTransform: transform,
	})
	return err
}

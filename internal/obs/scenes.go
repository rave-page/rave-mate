package obs

// obs-websocket v5 scene + video-canvas helpers used to auto-manage the overlay browser
// source (ensure a dedicated scene + a correctly-sized browser_source input). Same
// request-correlator pattern as inputs.go.

import (
	"context"
	"encoding/json"
	"fmt"
)

// SceneInfo is one entry from GetSceneList.
type SceneInfo struct {
	Name string `json:"sceneName"`
}

// GetSceneList returns the scenes + the current program scene name.
func (c *Client) GetSceneList(ctx context.Context) ([]SceneInfo, string, error) {
	raw, err := c.Request(ctx, "GetSceneList", nil)
	if err != nil {
		return nil, "", err
	}
	var v struct {
		Scenes  []SceneInfo `json:"scenes"`
		Current string      `json:"currentProgramSceneName"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, "", fmt.Errorf("obs GetSceneList decode: %w", err)
	}
	return v.Scenes, v.Current, nil
}

// CreateScene creates a new (empty) scene. Errors if it already exists - callers check first.
func (c *Client) CreateScene(ctx context.Context, name string) error {
	_, err := c.Request(ctx, "CreateScene", map[string]string{"sceneName": name})
	return err
}

// CanvasSize returns OBS's base (canvas) width/height from GetVideoSettings.
func (c *Client) CanvasSize(ctx context.Context) (int, int, error) {
	raw, err := c.Request(ctx, "GetVideoSettings", nil)
	if err != nil {
		return 0, 0, err
	}
	var v struct {
		BaseWidth  int `json:"baseWidth"`
		BaseHeight int `json:"baseHeight"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, 0, fmt.Errorf("obs GetVideoSettings decode: %w", err)
	}
	return v.BaseWidth, v.BaseHeight, nil
}

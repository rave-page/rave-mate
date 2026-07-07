package obs

// Profile / scene-collection / stream-service / video settings surface
// (obs-websocket v5). Same request correlator as requests.go; typed structs.

import (
	"context"
	"encoding/json"
	"fmt"
)

// StreamServiceSettings mirrors Get/SetStreamServiceSettings. Settings is the
// per-service freeform object (server/key/bwtest…) - a real wire boundary.
type StreamServiceSettings struct {
	Type     string         `json:"streamServiceType"`
	Settings map[string]any `json:"streamServiceSettings"`
}

// GetStreamServiceSettings returns the current stream-service settings.
func (c *Client) GetStreamServiceSettings(ctx context.Context) (StreamServiceSettings, error) {
	raw, err := c.Request(ctx, "GetStreamServiceSettings", nil)
	if err != nil {
		return StreamServiceSettings{}, err
	}
	var v StreamServiceSettings
	if err := json.Unmarshal(raw, &v); err != nil {
		return StreamServiceSettings{}, fmt.Errorf("obs GetStreamServiceSettings decode: %w", err)
	}
	return v, nil
}

// SetStreamServiceSettings writes the stream-service settings (type + full settings object).
func (c *Client) SetStreamServiceSettings(ctx context.Context, s StreamServiceSettings) error {
	_, err := c.Request(ctx, "SetStreamServiceSettings", s)
	return err
}

// VideoSettings mirrors Get/SetVideoSettings. All fields optional on Set -
// zero values are omitted from the request (obs-websocket rejects 0).
type VideoSettings struct {
	FpsNumerator   int `json:"fpsNumerator,omitempty"`
	FpsDenominator int `json:"fpsDenominator,omitempty"`
	BaseWidth      int `json:"baseWidth,omitempty"`
	BaseHeight     int `json:"baseHeight,omitempty"`
	OutputWidth    int `json:"outputWidth,omitempty"`
	OutputHeight   int `json:"outputHeight,omitempty"`
}

// IsZero reports whether no field is set (nothing to apply).
func (v VideoSettings) IsZero() bool { return v == VideoSettings{} }

// GetVideoSettings returns the current canvas/output/fps settings.
func (c *Client) GetVideoSettings(ctx context.Context) (VideoSettings, error) {
	raw, err := c.Request(ctx, "GetVideoSettings", nil)
	if err != nil {
		return VideoSettings{}, err
	}
	var v VideoSettings
	if err := json.Unmarshal(raw, &v); err != nil {
		return VideoSettings{}, fmt.Errorf("obs GetVideoSettings decode: %w", err)
	}
	return v, nil
}

// SetVideoSettings writes video settings; zero fields are left untouched (omitted).
func (c *Client) SetVideoSettings(ctx context.Context, v VideoSettings) error {
	_, err := c.Request(ctx, "SetVideoSettings", v)
	return err
}

// GetProfileParameter reads one profile INI value (basic.ini). category e.g.
// "Output"/"SimpleOutput"/"AdvOut"; empty value ⇒ key unset.
func (c *Client) GetProfileParameter(ctx context.Context, category, name string) (string, error) {
	raw, err := c.Request(ctx, "GetProfileParameter",
		map[string]string{"parameterCategory": category, "parameterName": name})
	if err != nil {
		return "", err
	}
	var v struct {
		ParameterValue string `json:"parameterValue"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", fmt.Errorf("obs GetProfileParameter decode: %w", err)
	}
	return v.ParameterValue, nil
}

// SetProfileParameter writes one profile INI value.
func (c *Client) SetProfileParameter(ctx context.Context, category, name, value string) error {
	_, err := c.Request(ctx, "SetProfileParameter", map[string]string{
		"parameterCategory": category, "parameterName": name, "parameterValue": value,
	})
	return err
}

// GetProfileList returns the current profile name and the full list.
func (c *Client) GetProfileList(ctx context.Context) (current string, profiles []string, err error) {
	raw, err := c.Request(ctx, "GetProfileList", nil)
	if err != nil {
		return "", nil, err
	}
	var v struct {
		CurrentProfileName string   `json:"currentProfileName"`
		Profiles           []string `json:"profiles"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", nil, fmt.Errorf("obs GetProfileList decode: %w", err)
	}
	return v.CurrentProfileName, v.Profiles, nil
}

// SetCurrentProfile switches the active OBS profile.
func (c *Client) SetCurrentProfile(ctx context.Context, name string) error {
	_, err := c.Request(ctx, "SetCurrentProfile", map[string]string{"profileName": name})
	return err
}

// CreateProfile creates a new profile and switches to it.
func (c *Client) CreateProfile(ctx context.Context, name string) error {
	_, err := c.Request(ctx, "CreateProfile", map[string]string{"profileName": name})
	return err
}

// GetSceneCollectionList returns the current scene collection name and the full list.
func (c *Client) GetSceneCollectionList(ctx context.Context) (current string, collections []string, err error) {
	raw, err := c.Request(ctx, "GetSceneCollectionList", nil)
	if err != nil {
		return "", nil, err
	}
	var v struct {
		CurrentSceneCollectionName string   `json:"currentSceneCollectionName"`
		SceneCollections           []string `json:"sceneCollections"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", nil, fmt.Errorf("obs GetSceneCollectionList decode: %w", err)
	}
	return v.CurrentSceneCollectionName, v.SceneCollections, nil
}

// SetCurrentSceneCollection switches the active scene collection.
func (c *Client) SetCurrentSceneCollection(ctx context.Context, name string) error {
	_, err := c.Request(ctx, "SetCurrentSceneCollection", map[string]string{"sceneCollectionName": name})
	return err
}

// CreateSceneCollection creates a new (empty) scene collection and switches to it.
func (c *Client) CreateSceneCollection(ctx context.Context, name string) error {
	_, err := c.Request(ctx, "CreateSceneCollection", map[string]string{"sceneCollectionName": name})
	return err
}

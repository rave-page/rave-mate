package obs

// Preset capture + apply: snapshot the current OBS profile state (stream service,
// video, selected output profile parameters, scene collection) as JSON and write
// it back later - the settings half of "one-tap stream ready".

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ProfileParameter is one profile INI key captured in a Preset.
type ProfileParameter struct {
	Category string `json:"category"`
	Name     string `json:"name"`
	Value    string `json:"value"`
}

// Preset is a JSON-serializable capture of the current OBS profile state.
// Apply switches/creates the profile, switches the scene collection (must exist),
// and writes stream-service + video settings + output parameters back.
type Preset struct {
	Profile         string                `json:"profile"`
	SceneCollection string                `json:"sceneCollection,omitempty"`
	StreamService   StreamServiceSettings `json:"streamService"`
	Video           VideoSettings         `json:"video"`
	OutputParams    []ProfileParameter    `json:"outputParams,omitempty"`
	CapturedAt      string                `json:"capturedAt,omitempty"` // RFC3339 UTC
}

// IsEmpty reports whether the preset carries nothing to apply.
func (p Preset) IsEmpty() bool {
	return p.Profile == "" && p.SceneCollection == "" &&
		p.StreamService.Type == "" && len(p.StreamService.Settings) == 0 &&
		p.Video.IsZero() && len(p.OutputParams) == 0
}

// outputParamKeys returns the profile INI keys worth capturing for an output mode
// ("Simple"/"Advanced", case-insensitive). basic.ini only - advanced encoder bitrates
// live in per-output JSON files obs-websocket can't read (best effort, like
// ValidateStreamSettings).
func outputParamKeys(mode string) []ProfileParameter {
	if strings.EqualFold(mode, "Advanced") {
		return []ProfileParameter{
			{Category: "AdvOut", Name: "Encoder"},
			{Category: "AdvOut", Name: "RecEncoder"},
			{Category: "AdvOut", Name: "TrackIndex"},
		}
	}
	// Simple mode (default) - also the fallback for unknown mode strings.
	return []ProfileParameter{
		{Category: "SimpleOutput", Name: "VBitrate"},
		{Category: "SimpleOutput", Name: "ABitrate"},
		{Category: "SimpleOutput", Name: "StreamEncoder"},
		{Category: "SimpleOutput", Name: "RecEncoder"},
		{Category: "SimpleOutput", Name: "KeyframeInterval"},
	}
}

// CapturePreset snapshots the current profile state. Profile, stream service and
// video settings are required; scene collection + output params are best-effort.
func (c *Client) CapturePreset(ctx context.Context) (Preset, error) {
	var p Preset
	cur, _, err := c.GetProfileList(ctx)
	if err != nil {
		return Preset{}, fmt.Errorf("obs capture profile: %w", err)
	}
	p.Profile = cur
	if sc, _, err := c.GetSceneCollectionList(ctx); err == nil {
		p.SceneCollection = sc
	}
	if p.StreamService, err = c.GetStreamServiceSettings(ctx); err != nil {
		return Preset{}, fmt.Errorf("obs capture stream service: %w", err)
	}
	if p.Video, err = c.GetVideoSettings(ctx); err != nil {
		return Preset{}, fmt.Errorf("obs capture video: %w", err)
	}
	if mode, err := c.GetProfileParameter(ctx, "Output", "Mode"); err == nil && mode != "" {
		p.OutputParams = append(p.OutputParams, ProfileParameter{Category: "Output", Name: "Mode", Value: mode})
		for _, k := range outputParamKeys(mode) {
			v, err := c.GetProfileParameter(ctx, k.Category, k.Name)
			if err != nil || v == "" {
				continue // unset key: skip, don't fail the capture
			}
			k.Value = v
			p.OutputParams = append(p.OutputParams, k)
		}
	}
	p.CapturedAt = time.Now().UTC().Format(time.RFC3339)
	return p, nil
}

// ApplyPreset writes a preset back: switch (or create) the profile, switch the scene
// collection (must already exist - creating one would yield a blank stream), then
// stream-service + video settings + output parameters. Fails on the first error.
// OBS refuses profile/collection switches while streaming or recording - that
// surfaces as a request error here.
func (c *Client) ApplyPreset(ctx context.Context, p Preset) error {
	if p.IsEmpty() {
		return fmt.Errorf("obs apply: empty preset")
	}
	if p.Profile != "" {
		cur, profiles, err := c.GetProfileList(ctx)
		if err != nil {
			return fmt.Errorf("obs apply profile list: %w", err)
		}
		switch {
		case !containsFold(profiles, p.Profile):
			if err := c.CreateProfile(ctx, p.Profile); err != nil { // creates AND switches
				return fmt.Errorf("obs apply create profile: %w", err)
			}
		case !strings.EqualFold(cur, p.Profile):
			if err := c.SetCurrentProfile(ctx, p.Profile); err != nil {
				return fmt.Errorf("obs apply switch profile: %w", err)
			}
		}
	}
	if p.SceneCollection != "" {
		cur, list, err := c.GetSceneCollectionList(ctx)
		if err != nil {
			return fmt.Errorf("obs apply collection list: %w", err)
		}
		if !containsFold(list, p.SceneCollection) {
			return fmt.Errorf("obs apply: scene collection %q not found", p.SceneCollection)
		}
		if !strings.EqualFold(cur, p.SceneCollection) {
			if err := c.SetCurrentSceneCollection(ctx, p.SceneCollection); err != nil {
				return fmt.Errorf("obs apply switch collection: %w", err)
			}
		}
	}
	if p.StreamService.Type != "" {
		if err := c.SetStreamServiceSettings(ctx, p.StreamService); err != nil {
			return fmt.Errorf("obs apply stream service: %w", err)
		}
	}
	if !p.Video.IsZero() {
		if err := c.SetVideoSettings(ctx, p.Video); err != nil {
			return fmt.Errorf("obs apply video: %w", err)
		}
	}
	for _, pp := range p.OutputParams {
		if pp.Category == "" || pp.Name == "" {
			continue
		}
		if err := c.SetProfileParameter(ctx, pp.Category, pp.Name, pp.Value); err != nil {
			return fmt.Errorf("obs apply %s/%s: %w", pp.Category, pp.Name, err)
		}
	}
	return nil
}

// containsFold reports whether list has s (case-insensitive - OBS profile names
// are case-preserving but unique case-insensitively on Windows paths).
func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}

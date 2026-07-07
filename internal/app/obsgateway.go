package app

import (
	"context"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/featurehost"
	"rave.page/mate/internal/obs"
	"rave.page/mate/internal/obscontrol"
	"rave.page/mate/internal/studio"
)

// obsGateway adapts the OBS surfaces to studio.ObsGateway for the web Local Studio.
// Control + status ride the existing paths (featurehost child proxy, obscontrol
// manager); settings/preset ops use a lazy DIRECT obs-websocket connection - the
// child proxy doesn't carry the settings surface, and obs-websocket happily serves
// multiple clients. cfg is re-read live so settings edits apply without restart.
type obsGateway struct {
	proxy  *featurehost.ObsProxy
	ctrl   *obscontrol.Manager
	cfg    func() config.OBSFeature
	direct *obscontrol.Direct
}

func newObsGateway(proxy *featurehost.ObsProxy, ctrl *obscontrol.Manager, cfg func() config.OBSFeature) *obsGateway {
	return &obsGateway{
		proxy: proxy, ctrl: ctrl, cfg: cfg,
		direct: obscontrol.NewDirect(func() (string, int, string) {
			o := cfg()
			return o.ResolvedHost(), o.ResolvedPort(), o.Password
		}),
	}
}

func (g *obsGateway) Enabled() bool   { return g.cfg().Enabled }
func (g *obsGateway) Connected() bool { return g.proxy.Connected() }

// Statuses snapshots every OBS source known on the bus (local + peers + LAN
// remotes) with computed bitrate - obs.status shows the whole rig.
func (g *obsGateway) Statuses() []obscontrol.Instance { return g.ctrl.Statuses() }

func (g *obsGateway) ListProfiles(ctx context.Context) (string, []string, error) {
	c, err := g.direct.Client(ctx)
	if err != nil {
		return "", nil, err
	}
	return c.GetProfileList(ctx)
}

func (g *obsGateway) ListSceneCollections(ctx context.Context) (string, []string, error) {
	c, err := g.direct.Client(ctx)
	if err != nil {
		return "", nil, err
	}
	return c.GetSceneCollectionList(ctx)
}

func (g *obsGateway) GetSettings(ctx context.Context) (obs.StreamServiceSettings, obs.VideoSettings, error) {
	c, err := g.direct.Client(ctx)
	if err != nil {
		return obs.StreamServiceSettings{}, obs.VideoSettings{}, err
	}
	ss, err := c.GetStreamServiceSettings(ctx)
	if err != nil {
		return obs.StreamServiceSettings{}, obs.VideoSettings{}, err
	}
	vs, err := c.GetVideoSettings(ctx)
	if err != nil {
		return obs.StreamServiceSettings{}, obs.VideoSettings{}, err
	}
	return ss, vs, nil
}

func (g *obsGateway) CapturePreset(ctx context.Context) (obs.Preset, error) {
	c, err := g.direct.Client(ctx)
	if err != nil {
		return obs.Preset{}, err
	}
	return c.CapturePreset(ctx)
}

func (g *obsGateway) ApplyPreset(ctx context.Context, p obs.Preset) error {
	c, err := g.direct.Client(ctx)
	if err != nil {
		return err
	}
	return c.ApplyPreset(ctx, p)
}

// Stream/record control rides the child proxy - the canonical control path
// (same one obscontrol + keybinds drive).
func (g *obsGateway) StartStream(ctx context.Context) error { return g.proxy.StartStream(ctx) }
func (g *obsGateway) StopStream(ctx context.Context) error  { return g.proxy.StopStream(ctx) }
func (g *obsGateway) StartRecord(ctx context.Context) error { return g.proxy.StartRecord(ctx) }
func (g *obsGateway) StopRecord(ctx context.Context) error  { return g.proxy.StopRecord(ctx) }

var _ studio.ObsGateway = (*obsGateway)(nil)

package app

import (
	"context"
	"strings"

	"rave.page/mate/internal/api"
	"rave.page/mate/internal/config"
)

// worldliveClient adapts internal/api's worldlive surface + rave-mate's auth/config into
// vrcperm.HostedClient (the hosted WorldSync publish path). Bearer = the user access token; the
// gateway forwards the principal, so user_id defaults to the caller. Ready() self-gates on a live
// rave.page session + a configured target world id, so a hosted-mode publish surfaces a clean
// reason instead of hitting the API blind.
type worldliveClient struct {
	api   *api.Client
	token func() string
	cfg   func() *config.WorldSyncFeature
}

// Ready reports whether a hosted publish can proceed.
func (c *worldliveClient) Ready() (bool, string) {
	if c.token() == "" {
		return false, "not signed in to rave.page"
	}
	if strings.TrimSpace(c.cfg().HostedWorldID) == "" {
		return false, "no hosted world id set"
	}
	return true, ""
}

// PublishModule PUTs the raw module payload to rave.page's worldlive API (server envelopes + owns
// seq); returns the stable raw URL + gist id + seq.
func (c *worldliveClient) PublishModule(ctx context.Context, moduleKey string, payload []byte) (rawURL, gistID string, seq int64, err error) {
	out, err := c.api.PublishWorldLive(ctx, c.token(), c.cfg().HostedWorldID, moduleKey, payload)
	if err != nil {
		return "", "", 0, err
	}
	return out.RawURL, out.GistID, out.Seq, nil
}

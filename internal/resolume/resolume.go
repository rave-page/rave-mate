// Package resolume controls Resolume Arena/Avenue 7 for phrase-synced visuals: OSC send (via
// internal/osc) for tempo/resync/tempo-tap + clip triggers, and the Resolume 7 REST web API
// (net/http) for tempo/beat readback + clip triggers without standing up an OSC receiver.
//
// Resolume 7 joins an Ableton Link session natively, so it follows the Link tempo + phase this
// bridge publishes with no OSC tempo push needed - this client adds phrase-aligned clip triggers
// (fired on Link phase==0 by the caller) + coarse offset nudges on top of Link sync.
package resolume

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"rave.page/mate/internal/osc"
)

// OSC addresses (Resolume composition tempo controller + clip transport).
const (
	addrTempo    = "/composition/tempocontroller/tempo"
	addrResync   = "/composition/tempocontroller/resync"
	addrTempoTap = "/composition/tempocontroller/tempotap"
)

// Client talks to one Resolume instance. OSC is UDP send-only (lazily dialed); REST is the
// Resolume 7 web server (/api/v1). Safe for concurrent use.
type Client struct {
	host     string
	oscPort  int
	restPort int
	base     string // REST base http://host:restPort/api/v1

	hc *http.Client

	mu  sync.Mutex
	osc *osc.Client // lazily dialed on first OSC send
}

// New builds a Resolume client (does not dial). host "" = 127.0.0.1; oscPort 0 = 7000;
// restPort 0 = 8080.
func New(host string, oscPort, restPort int) *Client {
	if host == "" {
		host = "127.0.0.1"
	}
	if oscPort <= 0 {
		oscPort = 7000
	}
	if restPort <= 0 {
		restPort = 8080
	}
	return &Client{
		host:     host,
		oscPort:  oscPort,
		restPort: restPort,
		base:     fmt.Sprintf("http://%s:%d/api/v1", host, restPort),
		hc:       &http.Client{Timeout: 4 * time.Second},
	}
}

// oscClient lazily dials the OSC UDP socket (reused across sends). A failed dial is retried on
// the next send.
func (c *Client) oscClient() (*osc.Client, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.osc != nil {
		return c.osc, nil
	}
	oc, err := osc.New(fmt.Sprintf("%s:%d", c.host, c.oscPort))
	if err != nil {
		return nil, err
	}
	c.osc = oc
	return oc, nil
}

func (c *Client) sendOSC(addr string, args ...any) error {
	oc, err := c.oscClient()
	if err != nil {
		return err
	}
	if err := oc.Send(addr, args...); err != nil {
		// Drop the socket so the next send re-dials (UDP write errors are usually transient).
		c.mu.Lock()
		if c.osc == oc {
			_ = c.osc.Close()
			c.osc = nil
		}
		c.mu.Unlock()
		return err
	}
	return nil
}

// Close releases the OSC socket.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.osc != nil {
		err := c.osc.Close()
		c.osc = nil
		return err
	}
	return nil
}

// ── OSC control ──

// SetTempo sets the composition tempo (BPM) over OSC. Rarely needed when Resolume follows Link,
// but useful as a fallback when Link is unavailable.
func (c *Client) SetTempo(bpm float64) error { return c.sendOSC(addrTempo, float32(bpm)) }

// Resync realigns Resolume's beat/phase to the downbeat now - the phrase-start primitive
// (equivalent to clicking Resync in the tempo controller).
func (c *Client) Resync() error { return c.sendOSC(addrResync, int32(1)) }

// TempoTap sends one tempo tap.
func (c *Client) TempoTap() error { return c.sendOSC(addrTempoTap, int32(1)) }

// ClipConnectAddr returns the OSC connect address for a 1-based layer/clip.
func ClipConnectAddr(layer, clip int) string {
	return fmt.Sprintf("/composition/layers/%d/clips/%d/connect", layer, clip)
}

// ConnectClip triggers (connects) a clip over OSC - layer/clip are 1-based (Resolume's OSC
// index). Fire this on a Link phrase boundary (phase==0) for a phrase-aligned start.
func (c *Client) ConnectClip(layer, clip int) error {
	return c.sendOSC(ClipConnectAddr(layer, clip), int32(1))
}

// ── REST readback / triggers (Resolume 7 web server) ──

// Tempo reads the current composition tempo (BPM) over REST.
func (c *Client) Tempo(ctx context.Context) (float64, error) {
	var comp struct {
		TempoController struct {
			Tempo struct {
				Value float64 `json:"value"`
			} `json:"tempo"`
		} `json:"tempocontroller"`
	}
	if err := c.getJSON(ctx, "/composition", &comp); err != nil {
		return 0, err
	}
	return comp.TempoController.Tempo.Value, nil
}

// ConnectClipREST triggers a clip over REST - layer/clip are 1-based (matches the OSC index and
// the Resolume UI). POST .../layers/{L}/clips/{C}/connect.
func (c *Client) ConnectClipREST(ctx context.Context, layer, clip int) error {
	path := fmt.Sprintf("/composition/layers/%d/clips/%d/connect", layer, clip)
	return c.post(ctx, path, nil)
}

func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("resolume GET %s: %s: %s", path, resp.Status, body)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) post(ctx context.Context, path string, body io.Reader) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, body)
	if err != nil {
		return err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("resolume POST %s: %s: %s", path, resp.Status, b)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return nil
}

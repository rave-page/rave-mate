package obscontrol

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"rave.page/mate/internal/obs"
)

// Direct is a lazily-(re)connected direct obs-websocket client for surfaces the
// featurehost child proxy doesn't carry (profile/scene-collection/settings writes,
// preset capture+apply for the studio channel). cfg is re-read on every call so a
// settings edit applies without restart; a config change drops + redials. Same
// in-proc rationale as directOBS: plain JSON over a loopback/LAN websocket.
type Direct struct {
	cfg func() (host string, port int, password string)

	mu      sync.Mutex
	cli     *obs.Client
	key     string // host:port:pass the live cli was dialed with
	nextTry time.Time
}

// NewDirect builds a lazy direct client; nothing is dialed until Client.
func NewDirect(cfg func() (host string, port int, password string)) *Direct {
	return &Direct{cfg: cfg}
}

// Client returns a live client, (re)connecting if needed. Retries are throttled
// (3s) so a closed OBS doesn't get hammered by back-to-back RPCs.
func (d *Direct) Client(ctx context.Context) (*obs.Client, error) {
	host, port, pass := d.cfg()
	key := host + ":" + strconv.Itoa(port) + ":" + pass

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cli != nil {
		select {
		case <-d.cli.Done():
			d.cli = nil // dropped
		default:
		}
	}
	if d.cli != nil && d.key != key { // config changed: redial
		_ = d.cli.Close()
		d.cli = nil
	}
	if d.cli != nil {
		return d.cli, nil
	}
	if time.Now().Before(d.nextTry) {
		return nil, errNoLocalOBS
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	c, err := obs.Connect(cctx, host, port, pass)
	if err != nil {
		d.nextTry = time.Now().Add(3 * time.Second) // back off; OBS may be closed
		return nil, fmt.Errorf("obs connect: %w", err)
	}
	d.cli, d.key = c, key
	return c, nil
}

// Close tears down the connection (idempotent).
func (d *Direct) Close() {
	d.mu.Lock()
	c := d.cli
	d.cli = nil
	d.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
}

package obscontrol

import (
	"context"
	"sync"
	"time"

	"rave.page/mate/internal/obs"
)

// directOBS is an OBS source reached directly over the LAN (obs-websocket), implementing the OBS
// interface. It connects lazily on first use and reconnects (throttled) after a drop - so an OBS PC
// that isn't running rave-mate can still be controlled. In-proc (no featurehost crash isolation), but
// obs-websocket is plain JSON over a websocket, so the blast radius is small.
type directOBS struct {
	host string
	port int
	pass string

	mu      sync.Mutex
	cli     *obs.Client
	nextTry time.Time
}

func newDirectOBS(host string, port int, pass string) *directOBS {
	return &directOBS{host: host, port: port, pass: pass}
}

// client returns a live client, (re)connecting if needed + allowed by the retry throttle.
func (d *directOBS) client(ctx context.Context) *obs.Client {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cli != nil {
		select {
		case <-d.cli.Done():
			d.cli = nil // dropped
		default:
			return d.cli
		}
	}
	if time.Now().Before(d.nextTry) {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	c, err := obs.Connect(cctx, d.host, d.port, d.pass)
	if err != nil {
		d.nextTry = time.Now().Add(5 * time.Second) // back off; OBS may be closed
		return nil
	}
	d.cli = c
	return c
}

// Connected reports a live session without forcing a (re)connect.
func (d *directOBS) Connected() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cli == nil {
		return false
	}
	select {
	case <-d.cli.Done():
		d.cli = nil
		return false
	default:
		return true
	}
}

// close tears down the connection (on reconcile/removal).
func (d *directOBS) close() {
	d.mu.Lock()
	c := d.cli
	d.cli = nil
	d.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
}

func (d *directOBS) StartStream(ctx context.Context) error {
	if c := d.client(ctx); c != nil {
		return c.StartStream(ctx)
	}
	return errNoLocalOBS
}

func (d *directOBS) StopStream(ctx context.Context) error {
	if c := d.client(ctx); c != nil {
		return c.StopStream(ctx)
	}
	return errNoLocalOBS
}

func (d *directOBS) ToggleStream(ctx context.Context) (bool, error) {
	if c := d.client(ctx); c != nil {
		return c.ToggleStream(ctx)
	}
	return false, errNoLocalOBS
}

func (d *directOBS) StartRecord(ctx context.Context) error {
	if c := d.client(ctx); c != nil {
		return c.StartRecord(ctx)
	}
	return errNoLocalOBS
}

func (d *directOBS) StopRecord(ctx context.Context) error {
	if c := d.client(ctx); c != nil {
		return c.StopRecord(ctx)
	}
	return errNoLocalOBS
}

func (d *directOBS) ToggleRecord(ctx context.Context) (bool, error) {
	if c := d.client(ctx); c != nil {
		return c.ToggleRecord(ctx)
	}
	return false, errNoLocalOBS
}

func (d *directOBS) ToggleRecordPause(ctx context.Context) (bool, error) {
	if c := d.client(ctx); c != nil {
		return c.ToggleRecordPause(ctx)
	}
	return false, errNoLocalOBS
}

func (d *directOBS) ToggleMute(ctx context.Context, input string) (bool, error) {
	if c := d.client(ctx); c != nil {
		return c.ToggleInputMute(ctx, input)
	}
	return false, errNoLocalOBS
}

func (d *directOBS) GetStreamStatus(ctx context.Context) (obs.StreamStatus, error) {
	if c := d.client(ctx); c != nil {
		return c.GetStreamStatus(ctx)
	}
	return obs.StreamStatus{}, errNoLocalOBS
}

func (d *directOBS) GetRecordStatus(ctx context.Context) (obs.RecordStatus, error) {
	if c := d.client(ctx); c != nil {
		return c.GetRecordStatus(ctx)
	}
	return obs.RecordStatus{}, errNoLocalOBS
}

// ── media-sync surface (mediasync.MediaController) ──

func (d *directOBS) GetMediaInputStatus(ctx context.Context, inputName string) (obs.MediaInputStatus, error) {
	if c := d.client(ctx); c != nil {
		return c.GetMediaInputStatus(ctx, inputName)
	}
	return obs.MediaInputStatus{}, errNoLocalOBS
}

func (d *directOBS) SetMediaInputCursor(ctx context.Context, inputName string, cursorMs int) error {
	if c := d.client(ctx); c != nil {
		return c.SetMediaInputCursor(ctx, inputName, cursorMs)
	}
	return errNoLocalOBS
}

func (d *directOBS) OffsetMediaInputCursor(ctx context.Context, inputName string, offsetMs int) error {
	if c := d.client(ctx); c != nil {
		return c.OffsetMediaInputCursor(ctx, inputName, offsetMs)
	}
	return errNoLocalOBS
}

func (d *directOBS) TriggerMediaInputAction(ctx context.Context, inputName, action string) error {
	if c := d.client(ctx); c != nil {
		return c.TriggerMediaInputAction(ctx, inputName, action)
	}
	return errNoLocalOBS
}

package webcam

import "context"

// CamControl is the daemon/UI-facing surface of the webcam manager - satisfied by the in-proc
// *Manager and by a subprocess proxy. Frame capture/fanout + Spout output stay in-proc inside
// the media child (raw frames must never cross the pipe).
type CamControl interface {
	Start(ctx context.Context) error
	Stop()
	Instances() []Instance
	Command(cmd Cmd) error
}

var _ CamControl = (*Manager)(nil)

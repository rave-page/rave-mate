package mediaroute

import "context"

// ReceiveControl is the daemon/UI-facing surface of the video-receive manager - satisfied by the
// in-proc *Manager and by a subprocess proxy. Source/sink registration stays in-proc inside the
// media child (Register* callbacks can't cross a process boundary).
type ReceiveControl interface {
	Start(ctx context.Context)
	RemoteVideoSources() []RemoteSource
	Receives() []Receive
	StartReceive(peer, sourceID string) (string, error)
	StopReceive(session string)
	// FrameShot samples a local sender's content: the origin-side "is this picture moving" oracle.
	FrameShot(sender string, n, intervalMs, scale int, crop [4]int) (FrameShot, error)
}

var _ ReceiveControl = (*Manager)(nil)

//go:build !windows

package surfacepub

import (
	"errors"
	"image"
)

// ErrUnsupported: shared D3D11 textures are the Windows shape of this transport. macOS
// (IOSurface/CAMetalLayer) and Linux (dmabuf/Wayland subsurface) get their own presenter when there
// is a webview shell there at all - today there is not (shell_nocgo.go).
var ErrUnsupported = errors.New("surfacepub: native render surfaces are Windows-only")

// Pub is the non-Windows stub. Every method is safe on it; nothing publishes.
type Pub struct{ id string }

func Open(string) (*Pub, error)               { return nil, ErrUnsupported }
func (p *Pub) SetGeometry(int, int) error     { return ErrUnsupported }
func (p *Pub) Send(*image.NRGBA, int64) error { return ErrUnsupported }
func (p *Pub) Want() (int, int)               { return 0, 0 }
func (p *Pub) Size() (int, int)               { return 0, 0 }
func (p *Pub) Stats() Stats                   { return Stats{ID: p.id, ConsumerAgeMs: -1} }
func (p *Pub) Close()                         {}

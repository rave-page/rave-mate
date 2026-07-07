package vrslgrid

import (
	"image"
	"image/png"
	"os"

	"rave.page/mate/internal/logbus"
)

// Publisher pushes a rendered grid frame to an output backend. The concrete backend is chosen at
// build time: Spout (Windows, -tags spout) or a PNG-file fallback (any build).
type Publisher interface {
	Publish(*image.RGBA) error
	Name() string // backend label for status
	Close()
}

// NewPublisher builds the grid output for this build. spoutName is the Spout sender name (Spout
// builds); pngPath is where the fallback writes the frame. Never returns nil.
func NewPublisher(log *logbus.Bus, spoutName, pngPath string) Publisher {
	return newPublisher(log, spoutName, pngPath)
}

// pngPublisher writes each frame to a PNG file (atomic rename). The fallback when no GPU-share
// backend is compiled in / available - point an OBS Image source at the file.
type pngPublisher struct {
	path string
	log  *logbus.Bus
}

func newPNGPublisher(log *logbus.Bus, path string) *pngPublisher {
	return &pngPublisher{path: path, log: log}
}

func (p *pngPublisher) Publish(img *image.RGBA) error {
	tmp := p.path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, p.path)
}

func (p *pngPublisher) Name() string { return "PNG file: " + p.path }
func (p *pngPublisher) Close()       {}

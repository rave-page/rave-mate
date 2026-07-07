//go:build spout

package vrslgrid

import (
	"image"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/videoshare"
)

// newPublisher (spout build): publish the grid over Spout under spoutName so OBS/VRChat can pull it
// as a shared texture. Falls back to the PNG file if Spout is unavailable (no SpoutLibrary.dll / no
// GPU) so the sink still produces output.
func newPublisher(log *logbus.Bus, spoutName, pngPath string) Publisher {
	fs, err := videoshare.NewFrameSender(log, spoutName)
	if err != nil {
		log.Warn("vrslgrid", "spout unavailable; grid falls back to PNG file", map[string]any{"error": err.Error(), "png": pngPath})
		return newPNGPublisher(log, pngPath)
	}
	return &spoutPublisher{fs: fs, name: spoutName}
}

type spoutPublisher struct {
	fs   videoshare.FrameSender
	name string
}

func (p *spoutPublisher) Publish(img *image.RGBA) error {
	// VRSL cells are opaque (alpha 255) so RGBA and NRGBA share byte layout - reuse the pixel buffer.
	n := &image.NRGBA{Pix: img.Pix, Stride: img.Stride, Rect: img.Rect}
	return p.fs.Send(n)
}

func (p *spoutPublisher) Name() string { return "Spout sender: " + p.name }
func (p *spoutPublisher) Close()       { p.fs.Close() }

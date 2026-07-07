// Package visualeditor is a UI-independent layered image composition engine: a document
// model (nested groups + image/text/solid/gradient leaves with blend + transform), a
// caching compositor, placeholder/variable substitution, and template (de)serialization.
// It renders to *image.NRGBA with pure stdlib + golang.org/x/image (no Fyne dependency),
// so it is fully unit-testable headlessly and its output can feed the overlay renderers.
package visualeditor

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image/color"
)

// SchemaVersion is the on-disk document/template schema version.
const SchemaVersion = 1

// LayerKind discriminates a layer's payload.
type LayerKind string

const (
	KindGroup    LayerKind = "group"
	KindImage    LayerKind = "image"
	KindText     LayerKind = "text"
	KindSolid    LayerKind = "solid"
	KindGradient LayerKind = "gradient"
)

// Align is a text horizontal alignment.
type Align string

const (
	AlignLeft   Align = "left"
	AlignCenter Align = "center"
	AlignRight  Align = "right"
)

// RGBA is a JSON-friendly straight-alpha color (0..255 per channel).
type RGBA struct {
	R uint8 `json:"r"`
	G uint8 `json:"g"`
	B uint8 `json:"b"`
	A uint8 `json:"a"`
}

// NRGBA converts to image/color.NRGBA.
func (c RGBA) NRGBA() color.NRGBA { return color.NRGBA{R: c.R, G: c.G, B: c.B, A: c.A} }

// FromNRGBA builds an RGBA from a color.NRGBA.
func FromNRGBA(c color.NRGBA) RGBA { return RGBA{R: c.R, G: c.G, B: c.B, A: c.A} }

// Transform positions/scales/rotates a layer's content box in document space.
// Rotation is degrees clockwise about the box center.
type Transform struct {
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	ScaleX   float64 `json:"scaleX"`
	ScaleY   float64 `json:"scaleY"`
	Rotation float64 `json:"rotation"`
}

// ImageFit controls how an image fills its W×H content box.
type ImageFit string

const (
	FitCover   ImageFit = "cover"   // scale to cover, center-crop
	FitContain ImageFit = "contain" // scale to fit inside
	FitStretch ImageFit = "stretch" // fill box, ignore aspect
)

// ImageProps is an image leaf. Path is a filesystem path; LibraryRef is an optional named
// reference resolved by the host (empty = use Path).
type ImageProps struct {
	Path       string   `json:"path,omitempty"`
	LibraryRef string   `json:"libraryRef,omitempty"`
	Fit        ImageFit `json:"fit,omitempty"`
}

// TextProps is a text leaf. Content may contain {placeholder} tokens (see placeholders.go).
type TextProps struct {
	Content       string  `json:"content"`
	FontFamily    string  `json:"fontFamily"`
	FontSize      float64 `json:"fontSize"`
	Color         RGBA    `json:"color"`
	LetterSpacing float64 `json:"letterSpacing"` // extra px between glyphs
	LineHeight    float64 `json:"lineHeight"`    // multiplier of font size (0 = 1.2 default)
	Align         Align   `json:"align"`
}

// SolidProps is a solid-color rect filling the layer's W×H box.
type SolidProps struct {
	Color RGBA `json:"color"`
}

// GradientProps is a linear gradient filling the box. Angle is degrees (0 = left→right,
// 90 = top→bottom). Stops must have Pos in [0,1] ascending.
type GradientProps struct {
	Angle float64        `json:"angle"`
	Stops []GradientStop `json:"stops"`
}

// GradientStop is one color stop.
type GradientStop struct {
	Pos   float64 `json:"pos"`
	Color RGBA    `json:"color"`
}

// Layer is a node in the composition tree: a Group (Children) or a leaf (one payload set).
type Layer struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Kind      LayerKind `json:"kind"`
	Visible   bool      `json:"visible"`
	Locked    bool      `json:"locked"`
	Opacity   float64   `json:"opacity"` // 0..1
	Blend     BlendMode `json:"blend"`
	Transform Transform `json:"transform"`
	W         float64   `json:"w"` // content box width (doc px, pre-scale)
	H         float64   `json:"h"` // content box height

	Children []*Layer       `json:"children,omitempty"`
	Image    *ImageProps    `json:"image,omitempty"`
	Text     *TextProps     `json:"text,omitempty"`
	Solid    *SolidProps    `json:"solid,omitempty"`
	Gradient *GradientProps `json:"gradient,omitempty"`
}

// Document is a full composition: canvas size + a root Group whose Children are the layers.
type Document struct {
	Schema int               `json:"schema"`
	W      int               `json:"w"`
	H      int               `json:"h"`
	Root   *Layer            `json:"root"`
	Vars   map[string]string `json:"vars,omitempty"` // static placeholder values
}

// NewID returns a short random hex layer id.
func NewID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// NewDocument returns an empty document of the given size with a root group.
func NewDocument(w, h int) *Document {
	return &Document{
		Schema: SchemaVersion,
		W:      w,
		H:      h,
		Root:   NewGroup("Document"),
	}
}

// NewGroup returns an empty group layer with identity transform + full opacity.
func NewGroup(name string) *Layer {
	return &Layer{
		ID: NewID(), Name: name, Kind: KindGroup,
		Visible: true, Opacity: 1, Blend: BlendNormal,
		Transform: Transform{ScaleX: 1, ScaleY: 1},
	}
}

// newLeaf builds a leaf with common defaults filling a w×h box at (x,y).
func newLeaf(kind LayerKind, name string, x, y, w, h float64) *Layer {
	return &Layer{
		ID: NewID(), Name: name, Kind: kind,
		Visible: true, Opacity: 1, Blend: BlendNormal,
		Transform: Transform{X: x, Y: y, ScaleX: 1, ScaleY: 1},
		W:         w, H: h,
	}
}

// NewSolid builds a solid-color rect layer.
func NewSolid(name string, x, y, w, h float64, c color.NRGBA) *Layer {
	l := newLeaf(KindSolid, name, x, y, w, h)
	l.Solid = &SolidProps{Color: FromNRGBA(c)}
	return l
}

// NewText builds a text layer.
func NewText(name string, x, y, w, h float64, content, family string, size float64, c color.NRGBA) *Layer {
	l := newLeaf(KindText, name, x, y, w, h)
	l.Text = &TextProps{Content: content, FontFamily: family, FontSize: size, Color: FromNRGBA(c), Align: AlignLeft}
	return l
}

// NewImage builds an image layer.
func NewImage(name string, x, y, w, h float64, path string) *Layer {
	l := newLeaf(KindImage, name, x, y, w, h)
	l.Image = &ImageProps{Path: path, Fit: FitCover}
	return l
}

// NewGradient builds a linear-gradient rect layer.
func NewGradient(name string, x, y, w, h float64, angle float64, stops []GradientStop) *Layer {
	l := newLeaf(KindGradient, name, x, y, w, h)
	l.Gradient = &GradientProps{Angle: angle, Stops: stops}
	return l
}

// IsGroup reports whether the layer is a group.
func (l *Layer) IsGroup() bool { return l.Kind == KindGroup }

// Clone deep-copies the layer subtree, assigning fresh IDs to every node.
func (l *Layer) Clone() *Layer {
	if l == nil {
		return nil
	}
	cp := *l
	cp.ID = NewID()
	if l.Image != nil {
		ip := *l.Image
		cp.Image = &ip
	}
	if l.Text != nil {
		tp := *l.Text
		cp.Text = &tp
	}
	if l.Solid != nil {
		sp := *l.Solid
		cp.Solid = &sp
	}
	if l.Gradient != nil {
		gp := *l.Gradient
		gp.Stops = append([]GradientStop(nil), l.Gradient.Stops...)
		cp.Gradient = &gp
	}
	if l.Children != nil {
		cp.Children = make([]*Layer, len(l.Children))
		for i, c := range l.Children {
			cp.Children[i] = c.Clone()
		}
	}
	return &cp
}

// Clone deep-copies the whole document (fresh layer IDs).
func (d *Document) Clone() *Document {
	cp := *d
	cp.Root = d.Root.Clone()
	if d.Vars != nil {
		cp.Vars = make(map[string]string, len(d.Vars))
		for k, v := range d.Vars {
			cp.Vars[k] = v
		}
	}
	return &cp
}

// Find returns the layer with id and its parent (nil parent = root), or (nil,nil).
func (d *Document) Find(id string) (layer, parent *Layer) {
	return findIn(d.Root, nil, id)
}

func findIn(node, parent *Layer, id string) (*Layer, *Layer) {
	if node == nil {
		return nil, nil
	}
	if node.ID == id {
		return node, parent
	}
	for _, c := range node.Children {
		if l, p := findIn(c, node, id); l != nil {
			return l, p
		}
	}
	return nil, nil
}

// Marshal serializes the document to indented JSON (stamps the schema version).
func (d *Document) Marshal() ([]byte, error) {
	d.Schema = SchemaVersion
	return json.MarshalIndent(d, "", "  ")
}

// Unmarshal parses a document from JSON, validating + normalizing defaults.
func Unmarshal(data []byte) (*Document, error) {
	var d Document
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, err
	}
	if d.Schema > SchemaVersion {
		return nil, fmt.Errorf("visualeditor: document schema %d newer than supported %d", d.Schema, SchemaVersion)
	}
	if d.W <= 0 || d.H <= 0 {
		return nil, fmt.Errorf("visualeditor: invalid document size %dx%d", d.W, d.H)
	}
	if d.Root == nil {
		d.Root = NewGroup("Document")
	}
	normalize(d.Root)
	return &d, nil
}

// normalize fills sane defaults on a parsed subtree (idempotent).
func normalize(l *Layer) {
	if l == nil {
		return
	}
	if l.ID == "" {
		l.ID = NewID()
	}
	if l.Blend == "" || !ValidBlend(l.Blend) {
		l.Blend = BlendNormal
	}
	if l.Transform.ScaleX == 0 {
		l.Transform.ScaleX = 1
	}
	if l.Transform.ScaleY == 0 {
		l.Transform.ScaleY = 1
	}
	for _, c := range l.Children {
		normalize(c)
	}
}

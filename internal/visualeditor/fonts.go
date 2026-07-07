package visualeditor

import (
	"embed"
	"sort"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
)

//go:embed assets/fonts/*.ttf
var bundledFonts embed.FS

// bundled family name → embedded file.
var bundledFiles = map[string]string{
	"Orbitron":          "assets/fonts/Orbitron-Regular.ttf",
	"Orbitron Medium":   "assets/fonts/Orbitron-Medium.ttf",
	"Orbitron SemiBold": "assets/fonts/Orbitron-SemiBold.ttf",
	"Orbitron Bold":     "assets/fonts/Orbitron-Bold.ttf",
}

// DefaultFontFamily is used when a text layer names an unknown/empty family.
const DefaultFontFamily = "Orbitron"

// FontRegistry maps family names to parsed fonts and caches faces per size. Safe for
// concurrent use. Bundled Orbitron families are always present; the host may Register
// additional families (e.g. TTFs from the config-dir fonts/ folder).
type FontRegistry struct {
	mu    sync.Mutex
	fonts map[string]*sfnt.Font
	faces map[faceKey]font.Face // cached faces
}

type faceKey struct {
	family string
	size   int // size*100 (0.01pt resolution) so float sizes cache distinctly
}

// NewFontRegistry parses the bundled fonts and returns a ready registry.
func NewFontRegistry() *FontRegistry {
	r := &FontRegistry{fonts: map[string]*sfnt.Font{}, faces: map[faceKey]font.Face{}}
	for fam, path := range bundledFiles {
		data, err := bundledFonts.ReadFile(path)
		if err != nil {
			continue
		}
		if f, err := opentype.Parse(data); err == nil {
			r.fonts[fam] = f
		}
	}
	return r
}

// Register parses ttf/otf bytes and registers them under family. Replaces any existing
// family of the same name. Returns an error if the bytes don't parse.
func (r *FontRegistry) Register(family string, data []byte) error {
	f, err := opentype.Parse(data)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fonts[family] = f
	// drop cached faces for this family (size set may be stale)
	for k := range r.faces {
		if k.family == family {
			delete(r.faces, k)
		}
	}
	return nil
}

// Families returns the sorted list of registered family names.
func (r *FontRegistry) Families() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.fonts))
	for fam := range r.fonts {
		out = append(out, fam)
	}
	sort.Strings(out)
	return out
}

// Face returns a cached font.Face for family at size (pt). Falls back to DefaultFontFamily
// then any available font. Never returns nil once the registry has at least one font.
func (r *FontRegistry) Face(family string, size float64) font.Face {
	if size < 1 {
		size = 1
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	f := r.fonts[family]
	if f == nil {
		f = r.fonts[DefaultFontFamily]
		family = DefaultFontFamily
	}
	if f == nil { // registry empty (shouldn't happen); pick any
		for fam, af := range r.fonts {
			f, family = af, fam
			break
		}
	}
	if f == nil {
		return nil
	}
	key := faceKey{family: family, size: int(size * 100)}
	if fc, ok := r.faces[key]; ok {
		return fc
	}
	fc, err := opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return nil
	}
	r.faces[key] = fc
	return fc
}

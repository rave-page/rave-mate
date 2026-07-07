package playsync

// Embedded-artwork preparation for the backend artwork PUT: only image/jpeg|png|webp ≤256KiB
// are accepted on the wire. Small permitted types pass through untouched; everything else is
// decoded (stdlib: jpeg/png/gif) and re-encoded as JPEG, downscaled to ≤1024px. Stdlib can't
// decode webp - an oversized webp is skipped (logged), a small one passes through.

import (
	"bytes"
	"image"
	_ "image/gif" // decode support for re-encoding
	"image/jpeg"
	_ "image/png"
	"net/http"
)

const (
	maxArtworkBytes = 262144
	maxArtworkDim   = 1024
)

// passthroughTypes are the wire-permitted content types (uploadable as-is when small enough).
var passthroughTypes = map[string]bool{"image/jpeg": true, "image/png": true, "image/webp": true}

// prepareArtwork returns wire-ready bytes + content type. ok=false (with a reason) when the
// picture can't be made acceptable. Content sniffed, never trusted from tags.
func prepareArtwork(data []byte) ([]byte, string, bool, string) {
	if len(data) == 0 {
		return nil, "", false, "empty"
	}
	ct := http.DetectContentType(data)
	if passthroughTypes[ct] && len(data) <= maxArtworkBytes {
		return data, ct, true, ""
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		if ct == "image/webp" {
			return nil, "", false, "webp too large to recompress" // small webp passed through above
		}
		return nil, "", false, "undecodable (" + ct + ")"
	}
	img = scaleDown(img, maxArtworkDim)
	for _, q := range []int{85, 70, 55, 40} {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: q}); err != nil {
			return nil, "", false, "jpeg encode failed"
		}
		if buf.Len() <= maxArtworkBytes {
			return buf.Bytes(), "image/jpeg", true, ""
		}
	}
	return nil, "", false, "still too large after recompression"
}

// scaleDown area-average resizes img so max(w,h) ≤ maxDim (no-op when already small). Pure
// stdlib - quality is fine for cover thumbnails.
func scaleDown(img image.Image, maxDim int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxDim && h <= maxDim {
		return img
	}
	var ow, oh int
	if w >= h {
		ow = maxDim
		oh = max(h*maxDim/w, 1)
	} else {
		oh = maxDim
		ow = max(w*maxDim/h, 1)
	}
	dst := image.NewRGBA(image.Rect(0, 0, ow, oh))
	for y := 0; y < oh; y++ {
		sy0, sy1 := b.Min.Y+y*h/oh, b.Min.Y+(y+1)*h/oh
		if sy1 <= sy0 {
			sy1 = sy0 + 1
		}
		for x := 0; x < ow; x++ {
			sx0, sx1 := b.Min.X+x*w/ow, b.Min.X+(x+1)*w/ow
			if sx1 <= sx0 {
				sx1 = sx0 + 1
			}
			var r, g, bl, a, n uint64
			for sy := sy0; sy < sy1; sy++ {
				for sx := sx0; sx < sx1; sx++ {
					pr, pg, pb, pa := img.At(sx, sy).RGBA()
					r += uint64(pr)
					g += uint64(pg)
					bl += uint64(pb)
					a += uint64(pa)
					n++
				}
			}
			i := dst.PixOffset(x, y)
			dst.Pix[i] = uint8(r / n >> 8)
			dst.Pix[i+1] = uint8(g / n >> 8)
			dst.Pix[i+2] = uint8(bl / n >> 8)
			dst.Pix[i+3] = uint8(a / n >> 8)
		}
	}
	return dst
}

package videoshare

import (
	"image"
	"image/jpeg"
	"os"
	"strconv"
	"strings"
)

// decodeJPEG decodes a cached cover JPEG; nil on any error.
func decodeJPEG(path string) image.Image {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	img, err := jpeg.Decode(f)
	if err != nil {
		return nil
	}
	return img
}

// writeF appends a float (prec decimals) + a separator to the signature builder.
func writeF(b *strings.Builder, v float64, prec int) {
	b.WriteString(strconv.FormatFloat(v, 'f', prec, 64))
	b.WriteByte('|')
}

// writeB appends a bool + separator.
func writeB(b *strings.Builder, v bool) {
	if v {
		b.WriteByte('1')
	} else {
		b.WriteByte('0')
	}
	b.WriteByte('|')
}

package avataratlas

// srgb.go - the EXACT piecewise sRGB spec functions (IEC 61966-2-1; contract §0 colour rule +
// §11 pipeline: NOT pow-2.2). The world shader re-applies LinearToSRGB + round(x*255) on DATA
// texels after the sampler's hardware EOTF; every 8-bit channel must survive that round trip
// byte-exact (proven for all 256 values in srgb_test.go).

import "math"

// SRGBToLinear is the piecewise sRGB EOTF: encoded [0,1] -> linear [0,1].
func SRGBToLinear(s float64) float64 {
	if s <= 0.04045 {
		return s / 12.92
	}
	return math.Pow((s+0.055)/1.055, 2.4)
}

// LinearToSRGB is the piecewise sRGB OETF: linear [0,1] -> encoded [0,1].
func LinearToSRGB(l float64) float64 {
	if l <= 0.0031308 {
		return l * 12.92
	}
	return 1.055*math.Pow(l, 1/2.4) - 0.055
}

// SRGBByteToLinear decodes a stored sRGB8 byte to linear (what the GPU sampler yields).
func SRGBByteToLinear(b uint8) float64 { return SRGBToLinear(float64(b) / 255) }

// LinearToSRGBByte re-encodes linear to a stored sRGB8 byte (OETF + round, clamped).
func LinearToSRGBByte(l float64) uint8 {
	s := LinearToSRGB(l)
	if s <= 0 {
		return 0
	}
	if s >= 1 {
		return 255
	}
	return uint8(math.Round(s * 255))
}

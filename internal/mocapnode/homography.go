package mocapnode

// homography.go - exact 4-point planar homography (DLT): a direct 8x8 linear solve via
// Gaussian elimination with partial pivoting. Stdlib only; 4 correspondences give an exact fit
// (no least squares needed - contract §8b's locator supplies exactly four anchors).

import (
	"fmt"
	"math"
)

// Homography maps canonical panel coordinates to frame coordinates:
//
//	w  = g*x + h*y + 1
//	fx = (a*x + b*y + c) / w
//	fy = (d*x + e*y + f) / w
//
// stored as [a b c d e f g h] with the 3x3 matrix's bottom-right element fixed at 1.
type Homography [8]float64

// Apply maps a canonical point through the homography.
func (m Homography) Apply(x, y float64) (fx, fy float64) {
	w := m[6]*x + m[7]*y + 1
	return (m[0]*x + m[1]*y + m[2]) / w, (m[3]*x + m[4]*y + m[5]) / w
}

// SolveHomography fits src[i] -> dst[i] exactly. Each correspondence contributes two rows of
// the 8x8 system; a degenerate quad (three collinear points, repeated points) is an error.
func SolveHomography(src, dst [4][2]float64) (Homography, error) {
	var a [8][9]float64 // augmented [A|b]
	for i := 0; i < 4; i++ {
		x, y := src[i][0], src[i][1]
		X, Y := dst[i][0], dst[i][1]
		a[2*i] = [9]float64{x, y, 1, 0, 0, 0, -X * x, -X * y, X}
		a[2*i+1] = [9]float64{0, 0, 0, x, y, 1, -Y * x, -Y * y, Y}
	}
	// Gaussian elimination, partial pivoting.
	for col := 0; col < 8; col++ {
		piv := col
		for r := col + 1; r < 8; r++ {
			if math.Abs(a[r][col]) > math.Abs(a[piv][col]) {
				piv = r
			}
		}
		if math.Abs(a[piv][col]) < 1e-9 {
			return Homography{}, fmt.Errorf("mocapnode: degenerate anchor quad (singular at col %d)", col)
		}
		a[col], a[piv] = a[piv], a[col]
		for r := col + 1; r < 8; r++ {
			f := a[r][col] / a[col][col]
			if f == 0 {
				continue
			}
			for c := col; c < 9; c++ {
				a[r][c] -= f * a[col][c]
			}
		}
	}
	var m Homography
	for row := 7; row >= 0; row-- {
		v := a[row][8]
		for c := row + 1; c < 8; c++ {
			v -= a[row][c] * m[c]
		}
		m[row] = v / a[row][row]
	}
	return m, nil
}

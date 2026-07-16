package mocappanel

// quat.go - 32-bit smallest-three quaternion packing (contract §4).
// Bit layout: [31:30]=m (index of largest-|component|), [29:20]=q_a, [19:10]=q_b, [9:0]=q_c;
// the three stored components in canonical x,y,z,w order skipping m, each on a 10-bit lattice
// over [-s,+s], s=1/sqrt(2). Cells: hi16 = bits[31:16], lo16 = bits[15:0].

import "math"

const quatS = 1 / math.Sqrt2 // s: range bound of the three smallest components

// PackQuat packs a unit quaternion (x,y,z,w). The quaternion is negated so the largest
// component is >= 0 (q and -q are the same rotation); components are clamped onto the lattice.
func PackQuat(q [4]float64) uint32 {
	m := 0
	for i := 1; i < 4; i++ {
		if math.Abs(q[i]) > math.Abs(q[m]) {
			m = i
		}
	}
	if q[m] < 0 {
		for i := range q {
			q[i] = -q[i]
		}
	}
	w := uint32(m) << 30
	shift := 20
	for i := 0; i < 4; i++ {
		if i == m {
			continue
		}
		lat := math.Round((q[i] + quatS) / (2 * quatS) * 1023)
		if lat < 0 {
			lat = 0
		} else if lat > 1023 {
			lat = 1023
		}
		w |= uint32(lat) << shift
		shift -= 10
	}
	return w
}

// UnpackQuat decodes a wire word to a renormalized unit quaternion (x,y,z,w). ok=false when
// |q|^2 falls outside [0.9, 1.1] before renormalization - treat the bone absent. The
// reconstructed largest component is sqrt(1-sum), naturally >= 0. Note an all-zero word decodes
// to three components at -s (sum 1.5) and self-rejects via the norm rule.
func UnpackQuat(w uint32) (q [4]float64, ok bool) {
	m := int(w >> 30 & 3)
	sum := 0.0
	shift := 20
	for i := 0; i < 4; i++ {
		if i == m {
			continue
		}
		c := float64(w>>shift&1023)/1023*(2*quatS) - quatS
		q[i] = c
		sum += c * c
		shift -= 10
	}
	m2 := 1 - sum
	if m2 < 0 {
		m2 = 0
	}
	q[m] = math.Sqrt(m2)
	n2 := sum + m2
	if n2 < 0.9 || n2 > 1.1 {
		return [4]float64{}, false
	}
	n := math.Sqrt(n2)
	for i := range q {
		q[i] /= n
	}
	return q, true
}

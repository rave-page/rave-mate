package musiclib

// BPMRange is a target tempo band (e.g. DnB 160-190). Tracks whose analyzed/stored
// BPM landed in the wrong octave (87 for a 174 track) fold into the band by pure
// power-of-2 shifts - the only tempo correction that never moves a beat position.
type BPMRange struct {
	Min float64 `json:"min"`
	Max float64 `json:"max"`
}

// Valid reports whether the range is usable.
func (r BPMRange) Valid() bool { return r.Min > 0 && r.Max >= r.Min }

// Contains reports whether bpm already lies in the band.
func (r BPMRange) Contains(bpm float64) bool { return bpm >= r.Min && bpm <= r.Max }

// FoldBPM octave-shifts bpm into r (double while below, halve while above).
// ok=false when bpm/r is invalid, or the band is narrower than an octave and no
// power-of-2 multiple lands inside it.
func FoldBPM(bpm float64, r BPMRange) (folded float64, ok bool) {
	if bpm <= 0 || !r.Valid() {
		return bpm, false
	}
	n := bpm
	for i := 0; n < r.Min && i < 8; i++ {
		n *= 2
	}
	for i := 0; n > r.Max && i < 8; i++ {
		n /= 2
	}
	if !r.Contains(n) {
		return bpm, false
	}
	return n, true
}

// FoldTrack folds t.BPM and every beatgrid marker's BPM into r. Marker positions
// and cues stay untouched: doubling keeps every existing beat (new beats land
// between), halving keeps the anchor beat - time-based cues survive both.
// Returns the factor applied (2^k) and whether anything changed.
func FoldTrack(t *Track, r BPMRange) (factor float64, changed bool) {
	folded, ok := FoldBPM(t.BPM, r)
	if !ok || folded == t.BPM {
		return 1, false
	}
	factor = folded / t.BPM
	t.BPM = folded
	for i := range t.Beatgrid {
		t.Beatgrid[i].BPM *= factor
	}
	return factor, true
}

package mocappanel

// golden.go - the frozen GOLDEN test vector (contract §6). Both implementations must produce /
// accept these EXACT cell values; wire-level, language-neutral.

// GoldenW returns the frozen bone rotation wire word W(d,k). Component lattices stay in
// [512,671]/[353,512]/[432,591] (within +-0.32s of centre) so the reconstructed largest
// component (~0.92) always dominates and smallest-three re-encode is exactly idempotent.
func GoldenW(d, k int) uint32 {
	qa := uint32(512 + (d*100+k*13)%160)
	qb := uint32(512 - (d*50+k*7)%160)
	qc := uint32(432 + (d*20+k*29)%160)
	return uint32(k%4)<<30 | qa<<20 | qb<<10 | qc
}

// GoldenFrame returns the frozen golden header + dancers in decoded form (Rots wire words,
// Quats/Present exactly as DecodeFrame yields), so Encode(GoldenFrame()) is the golden image
// and DecodeFrame of it round-trips field-for-field.
func GoldenFrame() (Header, []Dancer) {
	h := Header{
		Version:              Version,
		Flags:                FlagGolden,
		SourceTag:            0x00C0FFEE,
		SessionNonce:         0xBEEF,
		PanelSeq:             0x00012345,
		ServerTimeMs:         1234567890123,
		NetUtcTicks:          638600000000000000,
		BpmX100:              12800,
		DownbeatServerTimeMs: 1234567890000,
		BoneSlots:            22,
		DancerCount:          2,
		FrameCounter:         42,
		StageMin:             [3]float64{-8.0, 0.0, -6.0}, // fixed-point 0xF800, 0x0000, 0xFA00
		StageSize:            [3]float64{16.0, 4.0, 12.0}, // fixed-point 0x1000, 0x0400, 0x0C00
	}
	dancers := []Dancer{
		// ~ world pos (1.25, 1.0, -0.5) under the golden bounds; q values are the contract truth.
		goldenDancer(0, 7, DancerPresent, 0x003FFFFF, [3]uint16{0x9400, 0x4000, 0x7555}, h.BoneSlots),
		// slots 0-5, 9-11, 20-21 (includes core bits).
		goldenDancer(1, 9, DancerPresent|DancerVMC, 0x00300E3F, [3]uint16{0x2000, 0x2000, 0xC000}, h.BoneSlots),
	}
	return h, dancers
}

func goldenDancer(d int, id, flags uint16, mask uint32, hips [3]uint16, s int) Dancer {
	dc := Dancer{
		LocalID: id, Flags: flags, BoneMask: mask, HipsQ: hips,
		Rots:    make([]uint32, s),
		Quats:   make([][4]float64, s),
		Present: make([]bool, s),
	}
	for k := 0; k < s; k++ {
		if mask>>k&1 == 0 {
			continue // absent slot: wire cells zero
		}
		w := GoldenW(d, k)
		q, ok := UnpackQuat(w)
		if !ok {
			panic("mocappanel: golden wire word norm-rejected") // frozen vector is always valid
		}
		dc.Rots[k] = w
		dc.Quats[k] = q
		dc.Present[k] = true
	}
	return dc
}

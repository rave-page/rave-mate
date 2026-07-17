package avataratlas

// contract.go - frozen constants of the RPA1 atlas (MOCAP PANEL CONTRACT §11). Every number
// here is contract-frozen; a change is a version bump + coordinated world-side change.

import "rave.page/mate/internal/mocappanel"

const (
	// Canvas: PNG RGBA8, width fixed, height = HeaderRows + ceil(PxPerPoint*pointCount/Width),
	// max MaxHeight (VRC hard cap).
	Width      = 2048
	MaxHeight  = 2048
	HeaderRows = 2 // row 0 header, row 1 self-test
	PxPerPoint = 3 // linear packing, row-straddling allowed

	// MaxPoints = capacity of a max-height atlas (uint24 pointCount field is wider).
	MaxPoints = (MaxHeight - HeaderRows) * Width / PxPerPoint

	// Row 0 px0 MAGIC "RPA1" (R,G,B,A).
	MagicR = 'R'
	MagicG = 'P'
	MagicB = 'A'
	MagicA = '1'

	Version = 1 // row 0 px1.R

	MaxSlotIndex = 15 // px1.B performer/dancer fixed slot 0..15

	// Row 0 box table: 3 px per bone x 32 bones (§5 slot order), first pixel at BoxTableX.
	// Boxes are BONE-LOCAL bind-space millimetres: min int16 BE, size uint16 BE.
	BoxTableX = 8

	BoneSlots = mocappanel.BoneSlotMax // 32, §5 canonical table

	// Row 1 self-test: px i (i 0..255) = (i, 255-i, (i*37)&0xFF, 255).
	SelfTestLen = 256

	// v1 point weight: MUST be 255 (rigid single-bone); <255 reserved for v2 parent blends.
	WeightV1 = 255

	// BoxPadMm: tight AABB pad per §11 scanner rules (+1mm each side).
	BoxPadMm = 1
)

// SlotName returns the §5 canonical bone name of slot ("" = reserved 22-31).
func SlotName(slot int) string {
	if slot < 0 || slot >= BoneSlots {
		return ""
	}
	return mocappanel.BoneNames[slot]
}

// slotByName maps §5 canonical bone names (== VRM humanoid names, upperChest->3) to slots.
var slotByName = func() map[string]int {
	m := make(map[string]int, BoneSlots)
	for i, n := range mocappanel.BoneNames {
		if n != "" {
			m[n] = i
		}
	}
	return m
}()

// SlotByVRMName maps a VRM humanoid bone name to its §5 slot (fingers/eyes/jaw and other
// unlisted names return ok=false - their nodes remap via the nearest mapped ancestor).
func SlotByVRMName(name string) (slot int, ok bool) {
	slot, ok = slotByName[name]
	return
}

// AtlasHeight returns the fixed canvas height for pointCount (HeaderRows + ceil rows).
func AtlasHeight(pointCount int) int {
	return HeaderRows + (PxPerPoint*pointCount+Width-1)/Width
}

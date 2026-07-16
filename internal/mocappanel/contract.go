package mocappanel

// contract.go - frozen constants and wire structs of MOCAP PANEL CONTRACT v1
// (world_building_2 repo, .devnotes/MOCAP_PANEL_CONTRACT.md). Every number here is
// contract-frozen; a change is a version bump on both sides.

// Canvas + cell geometry (image coordinates, top-left origin).
const (
	CanvasW = 1920
	CanvasH = 1080

	MetaCellPx = 32 // meta band: rows y in [0,32); meta cell c sampled at (c*32+16, 16)
	MetaCols   = 60

	DataCellPx = 16 // data region: y >= 32; cell (c,r) sampled at (c*16+8, 32+r*16+8)
	DataCols   = 120
	DataRows   = 65 // last row ends 1072; 8 px bottom margin unused
	DataY0     = MetaCellPx

	Magic0   = 0x5250 // 'R','P'
	Magic1   = 0x4D31 // 'M','1' - together "RPM1"
	MagicTol = 4      // per-byte MAGIC tolerance, post-calibration

	ParityTol = 8 // cell validity: |B - (R XOR G)| <= ParityTol post-calibration

	Version = 1 // header layout version (meta col 5)

	// Header flags (meta col 6).
	FlagGolden    = 1 << 0 // GOLDEN test pattern
	FlagLiveBones = 1 << 1

	// Dancer flags (dancer offset +1).
	DancerPresent = 1 << 0
	DancerVMC     = 1 << 1

	BoneSlotMax  = 32   // boneSlots S in 1..32
	CoreMask     = 0x23 // slots 0 (hips), 1 (spine), 5 (head) mandatory, else dancer rejected
	MaxDancers   = 10   // dancerCount D in 0..10 (v1 cap)
	MaxDataCells = 964  // world-side uniform budget: D*Stride(S) <= 964

	DancerHeaderCells = 8 // stride = 8 + 2*S

	StageFixedScale = 256 // stageMin/stageSize metres <-> 16-bit fixed-point x256
)

// v1.1 fiducials (contract §8b, FROZEN 2026-07-16): three constant corner cells drawn with
// INVERTED parity - B = (hi XOR lo) XOR 0xFF, all three land on B=0 with R != G, a colour no
// valid data/meta cell can produce (B=0 there forces R == G). TL anchor = the MAGIC pair
// itself. v1-exact decoders ignore all three by construction (meta 59 was reserved; the two
// data cells lie beyond any legal D*stride); v1.1 encoders MUST draw them.
const (
	FidTR = 0xC33C // meta col ColFidTR
	FidBL = 0xA55A // data cell (FidBLCol, FidRow)
	FidBR = 0x5AA5 // data cell (FidBRCol, FidRow)

	ColFidTR = 59 // TR fiducial meta column
	FidRow   = 64 // BL/BR fiducial data row (the last row)
	FidBLCol = 0
	FidBRCol = 119
)

// Fiducial anchor sample points (§8b): cell centres in canonical coords, top-left origin.
// TL/TR = meta cell centres of cols 0/ColFidTR; BL/BR = data cell centres of
// (FidBLCol,FidRow)/(FidBRCol,FidRow). Frozen alongside the fiducial values.
const (
	AnchorTLX, AnchorTLY = 16, 16
	AnchorTRX, AnchorTRY = 1904, 16
	AnchorBLX, AnchorBLY = 8, 1064
	AnchorBRX, AnchorBRY = 1912, 1064
)

// Meta band column map (multi-cell integers big-endian across cells: lowest col = most
// significant 16 bits). Cols 35..59 reserved = 0 (col 59 = the TR fiducial since v1.1).
const (
	ColMagic0       = 0
	ColMagic1       = 1
	ColCalBlack     = 2 // raw colour (0,0,0), not value-encoded
	ColCalMid       = 3 // raw colour (128,128,128) - sanity check only
	ColCalWhite     = 4 // raw colour (255,255,255)
	ColVersion      = 5
	ColFlags        = 6
	ColSourceTag    = 7 // 32b, 2 cells
	ColSessionNonce = 9
	ColPanelSeq     = 10 // 32b, 2 cells
	ColServerTimeMs = 12 // 64b signed, 4 cells - consumers use wrap-safe deltas only
	ColNetUtcTicks  = 16 // 64b, 4 cells - server UTC, cross-instance only
	ColBpmX100      = 20 // 0 = no beat tracker
	ColDownbeatMs   = 21 // 64b signed, 4 cells
	ColBoneSlots    = 25
	ColDancerCount  = 26
	ColFrameCounter = 27 // 32b, 2 cells; +1 per rendered data frame (liveness)
	ColStageMin     = 29 // X,Y,Z signed fixed x256, 3 cells
	ColStageSize    = 32 // X,Y,Z unsigned fixed x256, 3 cells; all three > 0
	ColReserved     = 35 // 35..59 = 0
)

// Dancer cell offsets within a stride block (dancer d occupies data cells
// [d*stride, (d+1)*stride), row-major idx = r*DataCols + c).
const (
	OffLocalID  = 0
	OffFlags    = 1
	OffBoneMask = 2 // 32b, 2 cells (bit i = bone slot i present)
	OffHips     = 4 // X,Y,Z: q = round((p - stageMin) / stageSize * 65535), clamped
	OffReserved = 7
	OffBones    = 8 // slot k at +8+2k (hi16), +9+2k (lo16); absent slots = both cells zero
)

// Stride returns a dancer's data-cell footprint for boneSlots s.
func Stride(s int) int { return DancerHeaderCells + 2*s }

// MetaSample returns the sample point of meta cell c.
func MetaSample(c int) (x, y int) { return c*MetaCellPx + MetaCellPx/2, MetaCellPx / 2 }

// DataSample returns the sample point of data cell idx (row-major).
func DataSample(idx int) (x, y int) {
	c, r := idx%DataCols, idx/DataCols
	return c*DataCellPx + DataCellPx/2, DataY0 + r*DataCellPx + DataCellPx/2
}

// BoneNames is the canonical 32-slot bone table (FROZEN); slots 22-31 reserved ("" = absent
// until a contract bump assigns them). Rotations are WORLD-space (VRCPlayerApi.GetBoneRotation);
// slot 0's rotation carries root yaw.
var BoneNames = [BoneSlotMax]string{
	"hips", "spine", "chest", "upperChest", "neck", "head",
	"leftShoulder", "leftUpperArm", "leftLowerArm", "leftHand",
	"rightShoulder", "rightUpperArm", "rightLowerArm", "rightHand",
	"leftUpperLeg", "leftLowerLeg", "leftFoot", "leftToes",
	"rightUpperLeg", "rightLowerLeg", "rightFoot", "rightToes",
}

// Header is the decoded meta band. StageMin/StageSize hold metres reconstructed from the x256
// fixed-point cells (so encode(decode(x)) is cell-exact).
type Header struct {
	Version              uint16
	Flags                uint16
	SourceTag            uint32 // event-config tag (Udon cannot read instance id)
	SessionNonce         uint16 // random per world-load
	PanelSeq             uint32 // per-panel monotonic; never a cross-node alignment key
	ServerTimeMs         int64  // GetServerTimeInSeconds*1000; wrap-safe deltas only
	NetUtcTicks          int64  // GetNetworkDateTime.Ticks
	BpmX100              uint16
	DownbeatServerTimeMs int64
	BoneSlots            int // S, 1..32; stride constant for the stream
	DancerCount          int // D, 0..10
	FrameCounter         uint32
	StageMin             [3]float64 // metres
	StageSize            [3]float64 // metres
}

// Dancer is one decoded dancer block. Rots keeps the raw 32-bit smallest-three wire words
// (0 where a slot is absent or parity-invalid); Quats the renormalized world rotations
// (x,y,z,w; zero where !Present); Present[k] = mask bit set AND cells valid AND norm accepted.
type Dancer struct {
	LocalID  uint16 // unique within SourceTag; (sourceTag, localId) is the global key
	Flags    uint16
	BoneMask uint32
	HipsQ    [3]uint16 // quantized hips world pos (see OffHips)
	Rots     []uint32
	Quats    [][4]float64
	Present  []bool
}

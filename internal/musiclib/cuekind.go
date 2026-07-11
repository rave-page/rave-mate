package musiclib

// Cross-format cue-type mapping. The only place that knows each app's integer enums; every
// importer/exporter routes through here so cue semantics survive migration.

// ── Traktor CUE_V2 TYPE ───────────────────────────────────────────────────────
// 0=cue, 1=fade-in, 2=fade-out, 3=load, 4=grid, 5=loop. A TYPE-0 cue with HOTCUE≥0 is a
// hotcue; HOTCUE=-1 is a plain marker.

func traktorCueKind(typ, hotcue int) CueKind {
	switch typ {
	case 1, 2:
		return CueFade
	case 3:
		return CueLoad
	case 4:
		return CueGrid
	case 5:
		return CueLoop
	default:
		if hotcue >= 0 {
			return CueHot
		}
		return CuePlain
	}
}

func traktorCueType(k CueKind) int {
	switch k {
	case CueFade:
		return 1
	case CueLoad:
		return 3
	case CueGrid:
		return 4
	case CueLoop:
		return 5
	default:
		return 0
	}
}

// ── Rekordbox POSITION_MARK Type ──────────────────────────────────────────────
// 0=cue, 1=fade-in, 2=fade-out, 3=load, 4=loop. Num=-1 is a memory cue (plain); Num≥0 is a
// hotcue slot.

func rekordboxCueKind(typ, num int) CueKind {
	switch typ {
	case 1, 2:
		return CueFade
	case 3:
		return CueLoad
	case 4:
		return CueLoop
	default:
		if num >= 0 {
			return CueHot
		}
		return CuePlain
	}
}

func rekordboxCueType(k CueKind) int {
	switch k {
	case CueFade:
		return 1
	case CueLoad:
		return 3
	case CueLoop:
		return 4
	default: // CuePlain, CueHot, CueGrid
		return 0
	}
}

// ── VirtualDJ POI Type (string) ───────────────────────────────────────────────
// "cue" (or absent) | "remix" | "loop" | "automix" | "beatgrid". Hot cues carry a 1-based
// Num (pad 1-8); remix points are pad-less markers - our memory-cue equivalent.

func virtualdjCueKind(typ string) CueKind {
	switch typ {
	case "loop":
		return CueLoop
	case "beatgrid":
		return CueGrid
	case "automix":
		return CueLoad
	case "remix":
		return CuePlain
	default:
		return CueHot
	}
}

func virtualdjPoiType(k CueKind) string {
	switch k {
	case CueLoop:
		return "loop"
	case CueGrid:
		return "beatgrid"
	case CueLoad:
		return "automix"
	case CuePlain:
		return "remix"
	default:
		return "cue"
	}
}

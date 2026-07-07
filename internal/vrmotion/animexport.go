package vrmotion

// Unity .anim (AnimationClip YAML) export. Each tracked transform becomes a
// position curve (meters) + an euler curve (degrees, ZXY = m_RotationOrder 4 -
// VRChat's tracker convention). Tangents are linear, matching the Player's
// linear position / nlerp rotation sampling, so a re-imported clip plays back
// the captured take faithfully. Pure data transform; no cgo, no networking.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"rave.page/mate/internal/osc"
)

// TrackerLabel is the default Unity transform path for a tracked id (0 = head,
// 1..8 = trackers). The user remaps these onto their avatar hierarchy in Unity.
func TrackerLabel(id int) string {
	if id == 0 {
		return "head"
	}
	return fmt.Sprintf("tracker%d", id)
}

// ExportAnim writes rec to path as a Unity AnimationClip (.anim), atomically.
// label maps tracker ids → transform paths; nil → TrackerLabel.
func ExportAnim(path string, rec *Recording, label func(id int) string) error {
	yaml := BuildAnim(rec, label)
	tmp, err := os.CreateTemp(filepath.Dir(path), ".vranim-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err = tmp.WriteString(yaml); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err = tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err = os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// vec3 is a Vector3 value used for curve keys, tangents, and euler angles.
type vec3 struct{ x, y, z float32 }

// BuildAnim renders rec as Unity AnimationClip YAML. Exposed for testing.
func BuildAnim(rec *Recording, label func(id int) string) string {
	if label == nil {
		label = TrackerLabel
	}
	name := "motion"
	hz := 30
	var frames []Frame
	if rec != nil {
		if rec.Name != "" {
			name = rec.Name
		}
		if rec.Hz > 0 {
			hz = rec.Hz
		}
		frames = rec.Frames
	}

	ids := trackedIDs(frames)
	var b strings.Builder
	b.WriteString("%YAML 1.1\n")
	b.WriteString("%TAG !u! tag:unity3d.com,2011:\n")
	b.WriteString("--- !u!74 &7400000\n")
	b.WriteString("AnimationClip:\n")
	b.WriteString("  m_ObjectHideFlags: 0\n")
	b.WriteString("  m_CorrespondingSourceObject: {fileID: 0}\n")
	b.WriteString("  m_PrefabInstance: {fileID: 0}\n")
	b.WriteString("  m_PrefabAsset: {fileID: 0}\n")
	b.WriteString("  m_Name: " + name + "\n")
	b.WriteString("  serializedVersion: 6\n")
	b.WriteString("  m_Legacy: 0\n")
	b.WriteString("  m_Compressed: 0\n")
	b.WriteString("  m_UseHighQualityCurve: 1\n")
	b.WriteString("  m_RotationCurves: []\n")
	b.WriteString("  m_CompressedRotationCurves: []\n")

	// Euler curves (ZXY). Angles unwrapped across frames to avoid ±180 snaps.
	b.WriteString("  m_EulerCurves:\n")
	for _, id := range ids {
		writeVec3Curve(&b, eulerSeries(frames, id), label(id))
	}
	b.WriteString("  m_PositionCurves:\n")
	for _, id := range ids {
		writeVec3Curve(&b, posSeries(frames, id), label(id))
	}
	b.WriteString("  m_ScaleCurves: []\n")
	b.WriteString("  m_FloatCurves: []\n")
	b.WriteString("  m_PPtrCurves: []\n")
	b.WriteString("  m_SampleRate: " + strconv.Itoa(hz) + "\n")
	b.WriteString("  m_WrapMode: 0\n")
	b.WriteString("  m_Bounds:\n")
	b.WriteString("    m_Center: {x: 0, y: 0, z: 0}\n")
	b.WriteString("    m_Extent: {x: 0, y: 0, z: 0}\n")
	b.WriteString("  m_ClipBindingConstant:\n")
	b.WriteString("    genericBindings: []\n")
	b.WriteString("    pptrCurveMapping: []\n")

	dur := float32(0)
	if rec != nil {
		dur = float32(rec.Duration)
	}
	b.WriteString("  m_AnimationClipSettings:\n")
	b.WriteString("    serializedVersion: 2\n")
	b.WriteString("    m_AdditiveReferencePoseClip: {fileID: 0}\n")
	b.WriteString("    m_AdditiveReferencePoseTime: 0\n")
	b.WriteString("    m_StartTime: 0\n")
	b.WriteString("    m_StopTime: " + f32(dur) + "\n")
	b.WriteString("    m_OrientationOffsetY: 0\n")
	b.WriteString("    m_Level: 0\n")
	b.WriteString("    m_CycleOffset: 0\n")
	b.WriteString("    m_HasAdditiveReferencePose: 0\n")
	b.WriteString("    m_LoopTime: 0\n")
	b.WriteString("    m_LoopBlend: 0\n")
	b.WriteString("    m_LoopBlendOrientation: 0\n")
	b.WriteString("    m_LoopBlendPositionY: 0\n")
	b.WriteString("    m_LoopBlendPositionXZ: 0\n")
	b.WriteString("    m_KeepOriginalOrientation: 0\n")
	b.WriteString("    m_KeepOriginalPositionY: 1\n")
	b.WriteString("    m_KeepOriginalPositionXZ: 0\n")
	b.WriteString("    m_HeightFromFeet: 0\n")
	b.WriteString("    m_Mirror: 0\n")
	b.WriteString("  m_EditorCurves: []\n")
	b.WriteString("  m_EulerEditorCurves: []\n")
	return b.String()
}

// trackedIDs returns sorted unique tracker ids present across frames.
func trackedIDs(frames []Frame) []int {
	seen := map[int]bool{}
	for _, fr := range frames {
		for id := range fr.Poses {
			seen[id] = true
		}
	}
	ids := make([]int, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	return ids
}

// key is one animation keyframe: time + Vector3 value.
type key struct {
	t float64
	v vec3
}

// posSeries collects a tracker's position keys (skips frames lacking the id).
func posSeries(frames []Frame, id int) []key {
	var ks []key
	for _, fr := range frames {
		if p, ok := fr.Poses[id]; ok {
			ks = append(ks, key{fr.T, vec3{p.Pos[0], p.Pos[1], p.Pos[2]}})
		}
	}
	return ks
}

// eulerSeries collects a tracker's ZXY euler keys (deg), unwrapped per-axis for
// curve continuity (raw asin/atan2 output jumps at ±180°).
func eulerSeries(frames []Frame, id int) []key {
	var ks []key
	var prev vec3
	have := false
	for _, fr := range frames {
		p, ok := fr.Poses[id]
		if !ok {
			continue
		}
		ex, ey, ez := osc.QuatToEulerZXY(p.Rot[0], p.Rot[1], p.Rot[2], p.Rot[3])
		cur := vec3{ex, ey, ez}
		if have {
			cur.x = unwrap(prev.x, cur.x)
			cur.y = unwrap(prev.y, cur.y)
			cur.z = unwrap(prev.z, cur.z)
		}
		ks = append(ks, key{fr.T, cur})
		prev, have = cur, true
	}
	return ks
}

// unwrap shifts cur by multiples of 360 to land within ±180 of prev.
func unwrap(prev, cur float32) float32 {
	for cur-prev > 180 {
		cur -= 360
	}
	for cur-prev < -180 {
		cur += 360
	}
	return cur
}

// writeVec3Curve emits one Vector3 AnimationCurve (linear tangents) at path.
func writeVec3Curve(b *strings.Builder, ks []key, path string) {
	b.WriteString("  - curve:\n")
	b.WriteString("      serializedVersion: 2\n")
	b.WriteString("      m_Curve:\n")
	for i, k := range ks {
		in := slopeAt(ks, i, true)
		out := slopeAt(ks, i, false)
		b.WriteString("      - serializedVersion: 3\n")
		b.WriteString("        time: " + f64(k.t) + "\n")
		b.WriteString("        value: " + vec3str(k.v) + "\n")
		b.WriteString("        inSlope: " + vec3str(in) + "\n")
		b.WriteString("        outSlope: " + vec3str(out) + "\n")
		b.WriteString("        tangentMode: 0\n")
		b.WriteString("        weightedMode: 0\n")
		b.WriteString("        inWeight: {x: 0.33333334, y: 0.33333334, z: 0.33333334}\n")
		b.WriteString("        outWeight: {x: 0.33333334, y: 0.33333334, z: 0.33333334}\n")
	}
	b.WriteString("      m_PreInfinity: 2\n")
	b.WriteString("      m_PostInfinity: 2\n")
	b.WriteString("      m_RotationOrder: 4\n")
	b.WriteString("    path: " + path + "\n")
}

// slopeAt returns the linear tangent for key i. in=true → slope of the segment
// into i (from i-1); else slope out of i (to i+1). Endpoints reuse the one
// neighbouring segment; isolated keys → zero slope.
func slopeAt(ks []key, i int, in bool) vec3 {
	seg := func(a, b int) vec3 {
		dt := ks[b].t - ks[a].t
		if dt <= 0 {
			return vec3{}
		}
		d := float32(dt)
		return vec3{
			(ks[b].v.x - ks[a].v.x) / d,
			(ks[b].v.y - ks[a].v.y) / d,
			(ks[b].v.z - ks[a].v.z) / d,
		}
	}
	if in {
		if i == 0 {
			if len(ks) > 1 {
				return seg(0, 1)
			}
			return vec3{}
		}
		return seg(i-1, i)
	}
	if i == len(ks)-1 {
		if i > 0 {
			return seg(i-1, i)
		}
		return vec3{}
	}
	return seg(i, i+1)
}

func vec3str(v vec3) string {
	return "{x: " + f32(v.x) + ", y: " + f32(v.y) + ", z: " + f32(v.z) + "}"
}

func f32(v float32) string { return strconv.FormatFloat(float64(v), 'g', -1, 32) }
func f64(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

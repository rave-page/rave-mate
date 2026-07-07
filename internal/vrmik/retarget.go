package vrmik

// Per-take retarget calibration: recordings store raw device poses (playspace-world metres,
// keys in device-index order), which poses the avatar wherever the user stood, at the USER's
// proportions, with limbs driven by whatever device grabbed each key. Calibrate derives from
// the whole take: an origin (recenter over the grid), a uniform user→avatar height scale
// (targets land within the avatar's reach), and a key→role map classified geometrically
// (height/spread/laterality stats), so hands/hips/feet are role-correct even for legacy takes
// recorded with index-order keys.

import (
	"math"
	"slices"
	"sort"

	"rave.page/mate/internal/vrm"
	"rave.page/mate/internal/vrmotion"
)

// Canonical role keys PoseRT consumes (sample-map keys after Normalize).
const (
	RoleHead      = 0
	RoleLeftHand  = 1
	RoleRightHand = 2
	RoleHips      = 3
	RoleLeftFoot  = 4
	RoleRightFoot = 5
)

// Retarget is a per-take calibration. Nil is valid everywhere and means passthrough.
type Retarget struct {
	Origin [3]float32  // take-space origin (median head x/z, y=0), subtracted pre-scale
	Scale  float32     // user→avatar uniform scale
	Roles  map[int]int // recording key → Role*; unmapped keys are dropped at Normalize
}

// footHR is the fraction of head height below which a tracker classifies as a foot.
const footHR = 0.35

// Calibrate derives the retarget for playing rec on m. Never nil; degenerate takes
// (no head samples) yield identity origin/scale with only the head mapped.
func Calibrate(m *vrm.Model, rec *vrmotion.Recording) *Retarget {
	rt := &Retarget{Scale: 1, Roles: map[int]int{0: RoleHead}}
	if m == nil || rec == nil || len(rec.Frames) == 0 {
		return rt
	}

	// Per-key stats over the take, all relative to the head: height, horizontal distance,
	// lateral offset in the head's yaw frame (+ = user's right).
	type stat struct{ ys, hds, lats []float32 }
	stats := map[int]*stat{}
	var headX, headY, headZ []float32
	for _, fr := range rec.Frames {
		hp, ok := fr.Poses[0]
		if !ok {
			continue
		}
		headX = append(headX, hp.Pos[0])
		headY = append(headY, hp.Pos[1])
		headZ = append(headZ, hp.Pos[2])
		fwd := qRotate(quat(hp.Rot), [3]float32{0, 0, -1}) // OpenVR forward
		fwd[1] = 0
		if length(fwd) < 0.15 { // looking straight up/down - lateral axis unreliable, skip
			continue
		}
		right := cross(normalize(fwd), [3]float32{0, 1, 0})
		for k, p := range fr.Poses {
			if k == 0 {
				continue
			}
			st := stats[k]
			if st == nil {
				st = &stat{}
				stats[k] = st
			}
			d := sub(p.Pos, hp.Pos)
			st.ys = append(st.ys, p.Pos[1])
			st.hds = append(st.hds, float32(math.Hypot(float64(d[0]), float64(d[2]))))
			st.lats = append(st.lats, dot(d, right))
		}
	}
	if len(headY) == 0 {
		return rt
	}
	mHeadY := median(headY)
	rt.Origin = [3]float32{median(headX), 0, median(headZ)}

	// Scale: avatar head rest height / user's median HMD height.
	if head := m.HumanoidNode("head"); head >= 0 && mHeadY > 0.2 {
		restW := m.RestWorld()
		if hy := restW[head][13]; hy > 0.05 {
			rt.Scale = clamp(hy/mHeadY, 0.2, 5)
		}
	}

	// Classify keys: feet = below footHR of head height (lowest two); hips = the key that
	// hugs the head's horizontal axis closest (only when ≥3 remain - with just two devices
	// they're hands); hands = the two that stray furthest. Left/right by lateral median.
	var ks []kd
	for k, st := range stats {
		if len(st.ys) == 0 {
			continue
		}
		ks = append(ks, kd{k, median(st.ys) / float32(math.Max(float64(mHeadY), 0.01)), median(st.hds), median(st.lats)})
	}
	sort.Slice(ks, func(i, j int) bool { return ks[i].hr < ks[j].hr })

	var feet, rest []kd
	for _, k := range ks {
		if k.hr < footHR && len(feet) < 2 {
			feet = append(feet, k)
		} else {
			rest = append(rest, k)
		}
	}
	assignLR(rt.Roles, feet, RoleLeftFoot, RoleRightFoot)
	if len(rest) >= 3 {
		hips := 0
		for i := 1; i < len(rest); i++ {
			if rest[i].hd < rest[hips].hd {
				hips = i
			}
		}
		rt.Roles[rest[hips].key] = RoleHips
		rest = append(rest[:hips], rest[hips+1:]...)
	}
	if len(rest) > 2 { // extra devices (elbow/knee trackers): hands stray furthest from the head axis
		sort.Slice(rest, func(i, j int) bool { return rest[i].hd > rest[j].hd })
		rest = rest[:2]
	}
	assignLR(rt.Roles, rest, RoleLeftHand, RoleRightHand)
	return rt
}

// kd is one key's take-median stats: height ratio vs head, horizontal head distance, laterality.
type kd struct {
	key         int
	hr, hd, lat float32
}

// assignLR maps one or two keys onto a left/right role pair by lateral median
// (+lat = user's right); a lone key goes by sign.
func assignLR(roles map[int]int, ks []kd, left, right int) {
	switch len(ks) {
	case 1:
		if ks[0].lat >= 0 {
			roles[ks[0].key] = right
		} else {
			roles[ks[0].key] = left
		}
	case 2:
		a, b := ks[0], ks[1]
		if a.lat > b.lat {
			a, b = b, a
		}
		roles[a.key] = left
		roles[b.key] = right
	}
}

// Normalize remaps a raw recording sample onto canonical role keys, recentered + scaled
// (still take space - PoseRT's convP does the axis conversion). Nil-safe passthrough.
func (rt *Retarget) Normalize(sample map[int]vrmotion.Pose) map[int]vrmotion.Pose {
	if rt == nil || sample == nil {
		return sample
	}
	out := make(map[int]vrmotion.Pose, len(sample))
	for k, p := range sample {
		role, ok := rt.Roles[k]
		if !ok {
			continue
		}
		p.Pos = scale(sub(p.Pos, rt.Origin), rt.Scale)
		out[role] = p
	}
	return out
}

// Conv maps a take-space point into avatar space with the calibration applied - same
// transform Normalize applies to targets, so trails/cameras align with the posed mesh.
// Nil receiver = plain axis conversion.
func (rt *Retarget) Conv(p [3]float32) [3]float32 {
	if rt == nil {
		return convP(p)
	}
	return convP(scale(sub(p, rt.Origin), rt.Scale))
}

// median returns the middle value (average-free; ties to the upper). Empty → 0.
func median(v []float32) float32 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float32(nil), v...)
	slices.Sort(s)
	return s[len(s)/2]
}

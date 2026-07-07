package vrccampaths

// Shipped pro-grade DJ-event camera (dolly) paths. These replicate common live-event camera
// moves - over-the-shoulder reveal, crowd push-in, booth orbit, crane rise, rack-focus, drift
// wide, top-down sweep - as player-relative (IsLocal) paths so they frame the player (the DJ)
// in ANY world. Each pairs with a recommended cameraosc preset (the look).
//
// CAVEAT: only the waypoint geometry + per-point look (Zoom/FocalDistance/Aperture/Exposure)
// persist in VRChat's dolly JSON. Path Type (fitted/loose), Easing, Looping, Capture and
// Streaming are camera UI state VRChat does NOT save - set those once in-game. The local-frame
// axis convention (+Z = player forward / toward crowd) is assumed; if a shot faces the wrong
// way in your build, mirror Z / add 180° yaw - these are tunable starting points.

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"

	"rave.page/mate/internal/vrcloc"
)

// BuiltinFolder is the subdir (under the camera-paths dir) the shipped paths install into.
const BuiltinFolder = "rave.page DJ Paths"

// BuiltinPath is one shipped dolly path + its recommended preset.
type BuiltinPath struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Preset      string  `json:"preset"` // matching cameraosc.BuiltinPresets name
	Points      []Point `json:"-"`
}

func v(x, y, z float64) Vec3 { return Vec3{X: x, Y: y, Z: z} }

// djPoint builds a player-relative keyframe with the common DJ-shot fields set.
func djPoint(pos, rot Vec3, zoom, focal, aperture, dur float64) Point {
	return Point{IsLocal: true, Position: pos, Rotation: rot, Zoom: zoom, FocalDistance: focal, Aperture: aperture, Duration: dur, Speed: 1}
}

// lookAtPlayerYaw returns the Y-euler (deg) that aims a camera at pos back toward the player at
// the local origin. Convention: yaw 0 faces +Z; tune sign if your build differs.
func lookAtPlayerYaw(pos Vec3) float64 {
	return math.Atan2(-pos.X, -pos.Z) * 180 / math.Pi
}

// BuiltinDJPaths returns the shipped camera-path set (geometry + recommended preset).
func BuiltinDJPaths() []BuiltinPath {
	orbit := make([]Point, 0, 5)
	for i, ang := range []float64{55, 90, 125, 160, 200} {
		rad := ang * math.Pi / 180
		const r, h = 2.2, 1.45
		pos := v(r*math.Sin(rad), h, r*math.Cos(rad))
		dur := 3.0
		if i == 0 || i == 4 {
			dur = 2.0
		}
		orbit = append(orbit, djPoint(pos, v(6, lookAtPlayerYaw(pos), 0), 45, 2.2, 16, dur))
	}

	return []BuiltinPath{
		{
			Name: "Over-the-Shoulder Reveal", Preset: "Hero - shallow DOF",
			Description: "Behind the DJ's shoulder, slow push forward revealing the decks + crowd. Shallow DOF.",
			Points: []Point{
				djPoint(v(0.55, 1.70, -1.30), v(8, 0, 0), 50, 4, 16, 4),
				djPoint(v(0.45, 1.65, -0.60), v(6, 0, 0), 48, 4, 16, 4),
				djPoint(v(0.35, 1.62, -0.10), v(5, 0, 0), 46, 5, 15, 4),
			},
		},
		{
			Name: "Crowd Push-In", Preset: "Telephoto compression",
			Description: "From beside the booth, dolly forward over the crowd, rising - telephoto compression builds energy.",
			Points: []Point{
				djPoint(v(0.80, 1.60, 0.20), v(2, 0, 0), 38, 6, 10, 3),
				djPoint(v(0.30, 1.70, 3.00), v(1, 0, 0), 32, 9, 8, 5),
				djPoint(v(0.00, 1.85, 6.50), v(0, 0, 0), 30, 12, 6, 5),
			},
		},
		{
			Name: "Booth Orbit", Preset: "Hero - shallow DOF",
			Description: "Arc ~180° around the DJ at chest height, looking inward - parallax on the booth.",
			Points:      orbit,
		},
		{
			Name: "Crane Rise", Preset: "Wide establishing",
			Description: "Low hero angle at the booth, craning up + back to a wide establishing shot.",
			Points: []Point{
				djPoint(v(0.00, 0.45, 1.60), v(-18, 0, 0), 55, 2.5, 14, 3),
				djPoint(v(0.00, 1.40, 0.40), v(-2, 0, 0), 65, 3, 8, 4),
				djPoint(v(0.00, 2.60, -2.20), v(10, 0, 0), 95, 8, 3, 5),
			},
		},
		{
			Name: "Rack-Focus Decks", Preset: "Telephoto compression",
			Description: "Locked on the decks, long lens; focus pulls from the DJ's hands to the crowd. Very shallow DOF.",
			Points: []Point{
				djPoint(v(0.30, 1.30, 0.65), v(12, 0, 0), 32, 0.6, 22, 4),
				djPoint(v(0.30, 1.30, 0.65), v(12, 0, 0), 32, 0.6, 22, 2),
				djPoint(v(0.30, 1.30, 0.65), v(8, 0, 0), 32, 8.0, 22, 5),
			},
		},
		{
			Name: "Drift Wide", Preset: "Wide establishing",
			Description: "Wide establishing shot with a gentle lateral drift across the stage.",
			Points: []Point{
				djPoint(v(-1.60, 1.70, -3.20), v(2, 0, 0), 105, 12, 2, 6),
				djPoint(v(1.60, 1.70, -3.20), v(2, 0, 0), 105, 12, 2, 6),
			},
		},
		{
			Name: "Top-Down Sweep", Preset: "Crowd - deep focus",
			Description: "Overhead God's-eye looking down, slow drift toward the crowd.",
			Points: []Point{
				djPoint(v(0.00, 4.20, 0.50), v(-78, 0, 0), 70, 9, 4, 5),
				djPoint(v(0.00, 4.40, 3.50), v(-72, 0, 0), 70, 12, 4, 6),
			},
		},
	}
}

// WritePath writes keyframes to a VRChat dolly JSON file (re-indexes points), atomically-ish.
func WritePath(file string, pts []Point) error {
	for i := range pts {
		pts[i].Index = i
	}
	data, err := json.MarshalIndent(pts, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(file, data, 0o644)
}

// InstallBuiltins writes every shipped path into <camPathsDir>/rave.page DJ Paths/. Empty dir →
// VRChat's default CameraPaths. Returns count written + the target folder.
func InstallBuiltins(camPathsDir string) (int, string, error) {
	if camPathsDir == "" {
		camPathsDir = DefaultDir()
	}
	dst := filepath.Join(camPathsDir, BuiltinFolder)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return 0, dst, err
	}
	n := 0
	for _, b := range BuiltinDJPaths() {
		f := filepath.Join(dst, vrcloc.SanitizeName(b.Name, "path")+".json")
		if err := WritePath(f, b.Points); err != nil {
			return n, dst, err
		}
		n++
	}
	return n, dst, nil
}

package osc

import (
	"fmt"
	"math"
)

// TrackerPosition builds the OSC address+args for tracker n's position (meters).
// n must be 1..8.
func TrackerPosition(n int, x, y, z float32) (addr string, args []any) {
	return fmt.Sprintf("/tracking/trackers/%d/position", n), []any{x, y, z}
}

// TrackerRotation builds the OSC address+args for tracker n's rotation (euler degrees).
// n must be 1..8.
func TrackerRotation(n int, xDeg, yDeg, zDeg float32) (addr string, args []any) {
	return fmt.Sprintf("/tracking/trackers/%d/rotation", n), []any{xDeg, yDeg, zDeg}
}

// HeadPosition builds the OSC address+args for the head tracker position (meters).
func HeadPosition(x, y, z float32) (addr string, args []any) {
	return "/tracking/trackers/head/position", []any{x, y, z}
}

// HeadRotation builds the OSC address+args for the head tracker rotation (euler degrees).
func HeadRotation(xDeg, yDeg, zDeg float32) (addr string, args []any) {
	return "/tracking/trackers/head/rotation", []any{xDeg, yDeg, zDeg}
}

// QuatToEulerZXY converts a unit quaternion to euler angles in degrees using
// the ZXY intrinsic order VRChat expects for tracker rotation. Returns the
// rotations about X, Y, Z axes respectively (degrees).
func QuatToEulerZXY(x, y, z, w float32) (xDeg, yDeg, zDeg float32) {
	// Promote to float64 for stable trig.
	qx, qy, qz, qw := float64(x), float64(y), float64(z), float64(w)

	// ZXY order: X is the middle axis, so sin(X) isolates cleanly.
	sinX := 2 * (qw*qx + qy*qz)
	sinX = math.Max(-1, math.Min(1, sinX))
	rx := math.Asin(sinX)

	var ry, rz float64
	if math.Abs(sinX) > 0.9999999 { // gimbal lock at +/-90deg pitch
		ry = math.Atan2(2*(qw*qy-qx*qz), 1-2*(qy*qy+qz*qz))
		rz = 0
	} else {
		ry = math.Atan2(2*(qw*qy-qx*qz), 1-2*(qx*qx+qy*qy))
		rz = math.Atan2(2*(qw*qz-qx*qy), 1-2*(qx*qx+qz*qz))
	}

	const deg = 180 / math.Pi
	return float32(rx * deg), float32(ry * deg), float32(rz * deg)
}

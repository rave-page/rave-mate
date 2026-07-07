// Package vmc streams motion as VMC Protocol (Virtual Motion Capture, EVMC4U) over OSC,
// so a recorded or live VR take drives a VTuber avatar in an external renderer
// (VSeeFace / Warudo / VNyan). Unlike VRChat's /tracking trackers (which only AUGMENT
// VRChat's own FBT IK and can't override the live HMD/controllers), VMC carries the full
// raw device transforms (HMD + controllers + trackers) and the receiver does the IK - a
// true "perform with my mocap" path. Reuses the stdlib OSC encoder. Default port 39539.
package vmc

import (
	"fmt"

	"rave.page/mate/internal/osc"
	"rave.page/mate/internal/vrmotion"
)

// DefaultAddr is the VMC marionette/receiver input (VSeeFace etc. listen here).
const DefaultAddr = "127.0.0.1:39539"

// DeviceKind is the VMC raw-tracker category.
type DeviceKind int

const (
	KindHMD DeviceKind = iota
	KindController
	KindTracker
)

// addr returns the VMC OSC address for this device kind's position message.
func (k DeviceKind) addr() string {
	switch k {
	case KindHMD:
		return "/VMC/Ext/Hmd/Pos"
	case KindController:
		return "/VMC/Ext/Con/Pos"
	default:
		return "/VMC/Ext/Tra/Pos"
	}
}

// Device is how a recorded pose id surfaces over VMC (kind + stable serial string).
type Device struct {
	Kind   DeviceKind
	Serial string
}

// DefaultMapping maps recorded ids → VMC devices. TrackerPoses keys 0=HMD, then devices
// in OpenVR device-index order - controllers normally precede generic trackers, so 1/2 ≈
// hands. Receivers identify devices by serial during their own calibration, so the exact
// hand split is advisory; everything past 2 is a generic tracker.
func DefaultMapping(id int) Device {
	switch id {
	case 0:
		return Device{KindHMD, "Head"}
	case 1:
		return Device{KindController, "LeftHand"}
	case 2:
		return Device{KindController, "RightHand"}
	default:
		return Device{KindTracker, fmt.Sprintf("Tracker%d", id)}
	}
}

// Sender streams VMC frames to a renderer over UDP/OSC.
type Sender struct {
	c      *osc.Client
	mapper func(int) Device
}

// New dials a VMC receiver. Empty addr → DefaultAddr.
func New(addr string) (*Sender, error) {
	if addr == "" {
		addr = DefaultAddr
	}
	c, err := osc.New(addr)
	if err != nil {
		return nil, err
	}
	return &Sender{c: c, mapper: DefaultMapping}, nil
}

// SetMapping overrides the id→device mapping (nil → DefaultMapping).
func (s *Sender) SetMapping(m func(int) Device) {
	if m == nil {
		m = DefaultMapping
	}
	s.mapper = m
}

// Close closes the socket.
func (s *Sender) Close() error { return s.c.Close() }

// SendFrame emits one VMC frame: availability (/VMC/Ext/OK 1) + relative time
// (/VMC/Ext/T t), then every mapped device pose in Unity coordinates.
func (s *Sender) SendFrame(t float64, sample map[int]vrmotion.Pose) {
	_ = s.c.Send("/VMC/Ext/OK", int32(1))
	_ = s.c.Send("/VMC/Ext/T", float32(t))
	for id, p := range sample {
		d := s.mapper(id)
		px, py, pz, qx, qy, qz, qw := ToUnity(p)
		_ = s.c.Send(d.Kind.addr(), d.Serial, px, py, pz, qx, qy, qz, qw)
	}
}

// ToUnity converts an OpenVR right-handed standing-universe pose to VMC/Unity left-handed
// space: negate Z on position; quaternion (x,y,z,w) → (-x,-y,z,w) (mirror across the Z axis).
func ToUnity(p vrmotion.Pose) (px, py, pz, qx, qy, qz, qw float32) {
	return p.Pos[0], p.Pos[1], -p.Pos[2],
		-p.Rot[0], -p.Rot[1], p.Rot[2], p.Rot[3]
}

// Package mocappanel is the REFERENCE implementation of MOCAP PANEL CONTRACT v1 - the hidden
// 1920x1080 panel a VRChat world renders on a capture node's own client to transport dancer
// skeleton data (meta-band header + per-dancer 16-bit data cells) into rave-mate via raw
// game-capture. The encoder and decoder here define the wire truth; the world-side U# encoder
// (page.rave.puppets RaveMocapPanel) must be their exact inverse. Contract source of truth:
// world_building_2 repo, .devnotes/MOCAP_PANEL_CONTRACT.md (FROZEN 2026-07-16); design rationale
// in PUPPETS_MOCAP_DESIGN.md. Changing ANY number in contract.go is a contract version bump plus
// a coordinated world-side change.
//
// v1.1 addendum (§8b): the encoder additionally draws three inverted-parity corner fiducials
// (TR/BL/BR; TL = the MAGIC pair) so a scaled/off-axis capture can be located + rectified. The
// decode here stays v1-exact and ignores them; DecodeSampled is the seam a rectifying node
// (internal/mocapnode) feeds with homography-mapped cell-centre samples.
package mocappanel

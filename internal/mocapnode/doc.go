// Package mocapnode is the capture-node side of the mocap panel transport (MOCAP PANEL
// CONTRACT v1.1 §8b; design in the world repo's PUPPETS_MOCAP_DESIGN.md §3): it ingests frames
// from a capture Source (ffmpeg desktop duplication, a DirectShow device fed by the VRChat
// Stream Camera -> Spout2 -> OBS Virtual Camera path, or a file fixture), locates the panel via
// the four corner fiducials (colour blob scan), fits an exact 4-point homography, rectifies by
// inverse-mapping ONLY the canonical cell centres (nearest-neighbour - no full-frame warp), and
// feeds the stateful mocappanel Decoder. Output is decoded packets via callback plus an optional
// JSON-lines dump, with a live Health snapshot (fps, lock state, decode success rate).
//
// A native 1920x1080 capture whose anchors land on the canonical points takes the identity
// fast-path (direct pixel reads). ffmpeg discovery/supervision mirrors internal/vrslstream
// (mediatools.Resolve, KillTree/AssignToJob, capped restart backoff). Dev harness:
// cmd/mocapnode-probe.
package mocapnode

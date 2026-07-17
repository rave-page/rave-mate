// Package avataratlas is the REFERENCE implementation of the RPA1 skinned point atlas -
// MOCAP PANEL CONTRACT v1.3 addendum §11 (world_building_2 repo,
// .devnotes/MOCAP_PANEL_CONTRACT.md, FROZEN 2026-07-17). A performer's VRM avatar is sampled
// into a bone-local point cloud and packed into a 2048-wide RGBA8 PNG that the world downloads
// via VRCImageDownloader; spillover puppets render a skinned ghost from it. The encoder and
// decoder here define the wire truth; the world-side reader (page.rave.puppets) must be their
// exact inverse. Stdlib only (image/png, image/jpeg, encoding/json).
//
// Pipeline: gltf.go parses VRM 0.x / 1.0 (glTF 2.0 / GLB, humanoid bone map), sample.go draws
// surface-area-weighted points with a DETERMINISTIC seeded PRNG (goldens reproducible - no
// Date/time anywhere in the path, a contract requirement), atlas.go quantizes into per-bone
// AABBs and encodes/decodes/verifies the PNG. golden.go carries the frozen 2-bone synthetic
// golden (seed 1, 64 points, slot 0) checked into testdata/ and the world repo Tests~/golden/.
package avataratlas

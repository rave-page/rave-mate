// Package mocapmaster is the master-side collapse of the mocap pipeline (MOCAP PANEL CONTRACT
// v1.2, world repo .devnotes/MOCAP_PANEL_CONTRACT.md §10): it consumes decoded capture-node
// packets (internal/mocapnode), keeps the latest sanity-gated pose per dancer (PoseStore,
// primary election per (sourceTag, sessionNonce) stream key), and re-renders the pristine
// COMPOSITE MOCAP REGION into the outgoing 1920x1080 VRSL composite for spillover consumers
// (seam: vrslgrid.CompositeSpec.Overlay).
package mocapmaster

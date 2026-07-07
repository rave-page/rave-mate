// Package vrstats carries VR performance + SteamVR debug telemetry across linked rave-mate instances
// over the event bus. The VR instance samples OpenVR compositor frame timing + its own overlay-app
// stats and publishes a PerfStats each second; a monitoring instance (or `rave-mate ctl vrperf`)
// renders them - so issues can be observed remotely while the headset user tests.
package vrstats

// TopicPerf is the bus topic carrying PerfStats samples.
const TopicPerf = "vr.perf"

// PerfStats is one VR instance's performance + debug snapshot (TopicPerf payload). Frame counters are
// per-sample deltas (the last ~1s), not cumulative.
type PerfStats struct {
	// SteamVR / OpenVR runtime.
	Connected    bool    `json:"connected"`          // overlay-app session active (SteamVR up)
	HMDModel     string  `json:"hmdModel,omitempty"` // headset model (Prop_ModelNumber_String)
	DisplayHz    float64 `json:"displayHz"`          // headset refresh target
	FPS          float64 `json:"fps"`                // measured app frame rate (1000/frame interval)
	FrameMs      float64 `json:"frameMs"`            // client frame interval (ms)
	GpuMs        float64 `json:"gpuMs"`              // total render GPU time (ms)
	CompositorMs float64 `json:"compositorMs"`       // compositor render CPU time (ms)
	Reprojecting bool    `json:"reprojecting"`       // last frame was reprojected / multi-presented
	Dropped      int     `json:"dropped"`            // dropped frames this sample window
	Reprojected  int     `json:"reprojected"`        // reprojected frames this sample window

	// rave-mate overlay app.
	Overlays   int  `json:"overlays"`   // active content overlays
	EditorOpen bool `json:"editorOpen"` // in-VR editor open
	TexUploads int  `json:"texUploads"` // overlay texture uploads this sample window

	// rave-mate process self-footprint on the VR host (does the process itself hog the box?).
	ProcCPUPct float64 `json:"procCpuPct"` // process CPU% (100 = one full core)
	ProcRSSMB  float64 `json:"procRssMB"`  // process working set (MiB)
	ProcHeapMB float64 `json:"procHeapMB"` // Go heap in use (MiB)
	ProcGoros  int     `json:"procGoros"`  // live goroutines
	ProcNumGC  uint32  `json:"procNumGC"`  // completed GC cycles (cumulative)
}

// Instance is a PerfStats tagged with the node that published it + whether that's the local node.
type Instance struct {
	Origin string `json:"origin"`
	Local  bool   `json:"local"`
	PerfStats
}

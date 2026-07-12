package worker

// render.pointcloud: offline motion → animated point-cloud (RMPC) export. Same process-
// isolated "render" worker + pose pipeline as render.motionvideo (C5), but instead of
// rasterizing each posed frame it samples the posed mesh into surface points and streams
// them into the compact RMPC container (internal/pointcloud) - the web/VR viewer's
// anti-extraction-friendly artifact (raw FBX never leaves). Two streaming passes keep
// memory bounded (one frame in flight): pass 1 accumulates the world AABB (quant range),
// pass 2 quantizes + writes. Reused pose buffer; deterministic replay across passes.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"rave.page/mate/internal/pointcloud"
	"rave.page/mate/internal/version"
	"rave.page/mate/internal/vrm"
	"rave.page/mate/internal/vrmdyn"
	"rave.page/mate/internal/vrmik"
	"rave.page/mate/internal/vrmotion"
)

type rpcIn struct {
	Recording string `json:"recording"` // take .json path (required)
	Avatar    string `json:"avatar"`    // VRM/GLB/FBX path (required - point cloud IS the mesh)
	FPS       int    `json:"fps"`       // default 30
	Points    int    `json:"points"`    // target points/frame (density); default 24000
	Color     *bool  `json:"color"`     // per-point albedo; nil/true = on
	Physics   *bool  `json:"physics"`   // secondary-motion sim; nil/true = on
	Out       string `json:"out"`       // .rmpc path (required)
}

// rpcPointCloud exports a recorded take as an RMPC animated point cloud.
func rpcPointCloud(params json.RawMessage, emit EmitFunc) (json.RawMessage, error) {
	var in rpcIn
	if err := json.Unmarshal(params, &in); err != nil {
		return nil, err
	}
	if in.Recording == "" || in.Avatar == "" || in.Out == "" {
		return nil, fmt.Errorf("missing recording/avatar/out")
	}
	if in.FPS <= 0 {
		in.FPS = 30
	}
	if in.Points <= 0 {
		in.Points = 24000
	}
	color := in.Color == nil || *in.Color
	physics := in.Physics == nil || *in.Physics

	rec, err := vrmotion.Load(in.Recording)
	if err != nil {
		return nil, fmt.Errorf("load recording: %w", err)
	}
	if len(rec.Frames) == 0 {
		return nil, fmt.Errorf("empty recording")
	}
	model, err := vrm.Load(in.Avatar)
	if err != nil {
		return nil, fmt.Errorf("load avatar: %w", err)
	}
	sel := pointcloud.Select(model, in.Points, color)
	if sel.Count() == 0 {
		return nil, fmt.Errorf("avatar has no mesh vertices to sample")
	}
	rt := vrmik.Calibrate(model, rec)
	var dyn *vrmdyn.State
	if physics {
		dyn = vrmdyn.NewStateFromFile(model, in.Avatar)
	}
	frameDT := 1 / float64(in.FPS)
	player := vrmotion.NewPlayer(rec)
	dur := player.Duration()
	nFrames := int(dur*float64(in.FPS)) + 1

	// pose runs the shared chain for frame i into posBuf (reused). Deterministic given a
	// Reset dyn: identical sample + dt sequence across both passes.
	var posBuf [][3]float32
	pose := func(i int) {
		t := float64(i) / float64(in.FPS)
		sample := player.Sample(t)
		local := vrmik.PoseRT(model, sample, rt)
		if dyn != nil {
			dyn.Step(model, local, frameDT)
		}
		world := model.WorldFrom(local)
		skin := model.SkinMatrices(world)
		posBuf = sel.Positions(model, world, skin, posBuf)
	}

	// progress throttled to integer-percent steps (scan 0-50, write 50-100) so a long take
	// can't flood the stdio pipe; the last frame of each phase always emits.
	lastPct := -1
	prog := func(phase string, i, total int, lo, hi float64) {
		if total <= 0 {
			return
		}
		pct := lo + (hi-lo)*float64(i)/float64(total)
		if ip := int(pct); ip != lastPct || i == total {
			lastPct = ip
			emit("progress", map[string]any{"percent": pct, "frame": i, "frames": total, "phase": phase})
		}
	}

	// pass 1: world AABB across every frame (quantization range).
	bounds := pointcloud.NewBounds()
	if dyn != nil {
		dyn.Reset()
	}
	for i := 0; i < nFrames; i++ {
		pose(i)
		for _, p := range posBuf {
			bounds.Expand(p)
		}
		prog("scan", i+1, nFrames, 0, 50)
	}
	if !bounds.Valid() {
		return nil, fmt.Errorf("degenerate point cloud (no points)")
	}
	bounds.Pad(0.01) // keep extreme points off the quant edge; non-degenerate flat axis

	// open output (atomic: tmp in the same dir, then rename).
	if err := os.MkdirAll(filepath.Dir(in.Out), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir output: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(in.Out), ".rmpc-*.tmp")
	if err != nil {
		return nil, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	bw := bufio.NewWriterSize(tmp, 1<<16)

	hdr := pointcloud.Header{
		Generator:  "rave-mate " + version.String(),
		Source:     rec.Name,
		Created:    time.Now().UTC().Format(time.RFC3339),
		FPS:        in.FPS,
		FrameCount: nFrames,
		PointCount: sel.Count(),
		HasColor:   color,
		Bounds:     bounds,
	}
	enc, err := pointcloud.NewEncoder(bw, hdr, sel.Colors)
	if err != nil {
		_ = tmp.Close()
		return nil, err
	}

	// pass 2: quantize + write.
	if dyn != nil {
		dyn.Reset()
	}
	for i := 0; i < nFrames; i++ {
		pose(i)
		if err := enc.WriteFrame(posBuf); err != nil {
			_ = tmp.Close()
			return nil, fmt.Errorf("write frame: %w", err)
		}
		prog("write", i+1, nFrames, 50, 100)
	}
	if err := bw.Flush(); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(tmpName, in.Out); err != nil {
		return nil, fmt.Errorf("finalize: %w", err)
	}
	var size int64
	if st, err := os.Stat(in.Out); err == nil {
		size = st.Size()
	}
	return json.Marshal(map[string]any{
		"out": in.Out, "frames": nFrames, "points": sel.Count(),
		"duration_s": dur, "bytes": size, "has_color": color,
	})
}

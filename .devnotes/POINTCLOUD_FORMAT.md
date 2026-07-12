# RMPC — rave-mate animated point-cloud format (task #83)

Compact, streamable animated point cloud that rave-mate exports from a motion take + posed
avatar mesh, for the rave.page **web viewer** and **VR view mode** to consume. It's the
anti-extraction artifact: per-frame surface points only — the raw VRM/FBX never leaves.

Producer: `internal/pointcloud` (encode) + `internal/worker/renderpointcloud.go`
(`render.pointcloud` worker). Reference decoder: `internal/pointcloud` (`Decoder`).

## Design

- **Point count is constant across frames.** A fixed density-strided vertex subset is chosen
  once and reused every frame (skinning moves positions, not topology). So each frame is a
  fixed-size block → **O(1) seek to frame i**.
- **Colour is frame-invariant** (albedo doesn't change with pose) → stored **once**, not per
  frame.
- **Positions are 16-bit fixed-point** within a global AABB → 6 B/point/frame (¼ of float32),
  lossy at ~1/65535 of the bbox extent per axis (sub-mm for a human-scale take).
- Byte stream is **gzip-friendly on the wire** (serve with `Content-Encoding: gzip`) while
  staying **per-frame random-accessible on disk**.

## Layout (all integers little-endian)

```
"RMPC"              4-byte magic
version             uint16                = 1
headerLen           uint32
header              headerLen bytes JSON  (see below)
colors              PointCount*3 uint8 RGB   — only if header.has_color; frame-invariant
frames              FrameCount blocks, each:
  positions         PointCount*3 uint16      — quantized (x,y,z) per point
```

Frame block size = `PointCount*6` bytes. Frame `i` starts at
`headerBytes + (has_color ? PointCount*3 : 0) + i*PointCount*6`.

### Header JSON

```json
{
  "version": 1,
  "generator": "rave-mate 1.x",
  "source": "<take name>",
  "created": "2026-07-12T00:00:00Z",
  "fps": 30,
  "frame_count": 1800,
  "point_count": 24000,
  "has_color": true,
  "bounds": { "min": [x,y,z], "max": [x,y,z] },
  "quant_bits": 16
}
```

`bounds` is the world AABB across **all** frames (padded 1 cm). Coordinate system = avatar
space from the rave-mate mesh pipeline: right-handed, **meters, +Y up, avatar faces +Z**.

## Dequantize (viewer)

```
ext_a = bounds.max[a] - bounds.min[a]
p_a   = bounds.min[a] + (q_a / 65535) * ext_a        // a ∈ {x,y,z}
```
Colour: `colors[i*3 .. i*3+3]` is the RGB (0–255) of point `i`, same for every frame.

Playback: advance frame at `fps`; interpolate positions between adjacent frames if desired
(points correspond 1:1 by index across frames).

## Web/VR viewer sketch (OUT OF SCOPE here — integrator builds)

- Parse header, read colour block once → a single `THREE.BufferAttribute` (or GPU buffer).
- Per frame, `ReadFrame` → dequantize into the position attribute; render as `THREE.Points`
  (web) / instanced quads or a compute-splat (VR). Fixed frame size ⇒ trivial scrub/seek.
- Stream frames progressively (fetch ranged) for large takes; header carries counts up front.

## Future extensions (not in the first slice)

- **Delta frames** (store Δq vs previous frame + keyframes) for another ~2–4× on disk while
  keeping seek via keyframe index.
- **Per-frame point count** (variable topology) would need a re-added per-frame count +
  offset index — deliberately omitted (constant count is the win here).
- Optional **normals** block (for lit splatting / VR) — same frame-invariant-vs-per-frame
  split as colour; normals rotate with pose so they'd be per-frame (add a `has_normals` +
  a quantized-octahedral normals block per frame).
- Higher-fidelity sampling: surface-area-weighted / Poisson-disk instead of vertex-stride;
  triangle-interior samples for dense meshes.

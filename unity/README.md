# rave.page Unity exporter

Editor script exporting a VRChat avatar for rave-mate's Motion studio: the FBX + a
`<name>.physbones.json` sidecar with the avatar's real VRCPhysBone / DynamicBone
parameters (rave-mate simulates hair/tail/accessory physics from it; without a
sidecar it falls back to bone-name heuristics).

## Install

Copy `Editor/RavePagePhysboneExporter.cs` into your Unity project's `Assets/Editor/`.
No dependencies required; if present these are used automatically:
- **FBX Exporter** package (`com.unity.formats.fbx`) - exports the avatar as configured
  in the scene. Without it the mesh's source `.fbx` asset is copied instead.
- **VRC SDK** (VRCPhysBone) / **DynamicBone** - components read via reflection.

## Use

1. Select the avatar root in the Hierarchy.
2. `rave.page ▸ Export Avatar for rave-mate…`, pick an output folder.
3. Import both files in rave-mate: Motion ▸ Motion studio ▸ **Import avatar…** (pick the
   `.fbx`; keep the `.physbones.json` next to it - rave-mate loads it by matching basename).

VRCPhysBone maps 1:1 (pull/spring/stiffness/gravity/gravityFalloff/immobile/
endpointPosition/radius + ignore list). DynamicBone maps: elasticity→pull,
1−damping→spring, stiffness→stiffness, −gravity.y→gravity, endOffset→endpointPosition.
Colliders are not simulated yet (radius stored for later).

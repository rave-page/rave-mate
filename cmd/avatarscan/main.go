// Command avatarscan converts a VRM/glTF/binary-FBX avatar into an RPA1 skinned point atlas
// PNG (MOCAP PANEL CONTRACT §11 + v1.3.1 scanner-input amendment; reference codec in
// internal/avataratlas). DEV/operator CLI - the rave-mate GUI stays the single shipped binary.
//
//	avatarscan -in model.vrm|model.fbx -slot 0 -points 20000 [-seed 1] [-out atlas.png]
//	           [-bonemap map.json] [-report]
//	avatarscan -golden [-outdir DIR]
//
// Inputs without VRM humanoid metadata (FBX, plain glTF/GLB) map bones via deterministic name
// heuristics; -bonemap ({"nodeName": "slotName"}) overrides/extends them.
// -report prints a JSON report (input kind, mapping table, counts, per-bone histogram, boxes)
// to stdout.
// -golden emits the FROZEN synthetic golden (2-bone rig, seed 1, 64 points, slot 0) as
// golden_atlas_slot0.png + golden_atlas_slot0.json - the conformance fixture for the world
// reader (checked into page.rave.puppets Tests~/golden/).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"rave.page/mate/internal/avataratlas"
)

func main() {
	var (
		in      = flag.String("in", "", "input model (.vrm/.glb GLB container, .gltf with external resources, or binary .fbx)")
		slot    = flag.Int("slot", 0, "performer/dancer fixed slot 0..15 (atlas identity, px1.B)")
		points  = flag.Int("points", 20000, "surface points to sample")
		seed    = flag.Int64("seed", 1, "deterministic PRNG seed (same file+seed+points = identical atlas)")
		out     = flag.String("out", "atlas.png", "output atlas PNG path")
		bonemap = flag.String("bonemap", "", "JSON file {\"nodeName\": \"slotName\"} overriding/extending humanoid name heuristics (slotName \"\" force-unmaps)")
		report  = flag.Bool("report", false, "print JSON report (input kind, bone mapping, counts, per-bone histogram, boxes) to stdout")
		golden  = flag.Bool("golden", false, "emit the frozen golden fixture instead of scanning")
		outdir  = flag.String("outdir", ".", "with -golden: output directory")
	)
	flag.Parse()

	if *golden {
		if err := avataratlas.WriteGolden(*outdir); err != nil {
			fatal(err)
		}
		fmt.Fprintf(os.Stderr, "golden: wrote %s + %s to %s\n",
			avataratlas.GoldenPNG, avataratlas.GoldenJSON, *outdir)
		return
	}
	if *in == "" {
		flag.Usage()
		os.Exit(2)
	}

	doc, err := avataratlas.Load(*in)
	if err != nil {
		fatal(err)
	}
	if doc.HumanoidDupNodes > 0 {
		fmt.Fprintf(os.Stderr, "avatarscan: warning: humanoid map references %d node(s) from multiple bones (spec violation; resolved deterministically)\n", doc.HumanoidDupNodes)
	}
	for _, w := range doc.Warnings {
		fmt.Fprintf(os.Stderr, "avatarscan: warning: %s\n", w)
	}

	var overrides map[string]string
	if *bonemap != "" {
		raw, err := os.ReadFile(*bonemap)
		if err != nil {
			fatal(err)
		}
		if err := json.Unmarshal(raw, &overrides); err != nil {
			fatal(fmt.Errorf("bonemap %s: %w", *bonemap, err))
		}
	}
	mapping, err := avataratlas.MapHumanoid(doc, overrides)
	if err != nil {
		fatal(err)
	}
	if mapping.Source != "vrm0" && mapping.Source != "vrm1" {
		fmt.Fprintf(os.Stderr, "avatarscan: bone mapping (%s):\n", mapping.Source)
		for _, p := range mapping.Pairs {
			fmt.Fprintf(os.Stderr, "  %-13s <- %q (node %d)\n", p.SlotName, p.Name, p.Node)
		}
		if len(mapping.Unmapped) > 0 {
			fmt.Fprintf(os.Stderr, "  unmapped joints (%d, ancestor-walk): %v\n", len(mapping.Unmapped), mapping.Unmapped)
		}
	}

	res, err := avataratlas.Sample(doc, *points, *seed)
	if err != nil {
		fatal(err)
	}
	atlas, err := avataratlas.BuildAtlas(res.Points, *slot)
	if err != nil {
		fatal(err)
	}
	if dir := filepath.Dir(*out); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fatal(err)
		}
	}
	f, err := os.Create(*out)
	if err != nil {
		fatal(err)
	}
	if err := atlas.EncodePNG(f); err != nil {
		f.Close()
		fatal(err)
	}
	if err := f.Close(); err != nil {
		fatal(err)
	}

	if *report {
		if err := json.NewEncoder(os.Stdout).Encode(buildReport(*in, *out, *seed, doc, res, atlas, mapping)); err != nil {
			fatal(err)
		}
	}
	fmt.Fprintf(os.Stderr, "avatarscan: %s -> %s: %d points (%d requested, %d dropped, %d prims skipped), %d bones, %s/%s\n",
		*in, *out, len(atlas.Points), res.Requested, res.Dropped, res.SkippedPrims, atlas.BoneCount, doc.InputKind, mapping.Source)
}

type reportBox struct {
	Slot   int    `json:"slot"`
	Name   string `json:"name"`
	Points int    `json:"points"`
	MinMm  [3]int `json:"minMm"`
	SizeMm [3]int `json:"sizeMm"`
}

type reportOut struct {
	Input           string                    `json:"input"`
	Output          string                    `json:"output"`
	InputKind       string                    `json:"inputKind"` // glb / gltf / fbx
	VRMVersion      string                    `json:"vrmVersion"`
	UnitScaleFactor float64                   `json:"unitScaleFactor,omitempty"` // FBX GlobalSettings (cm per raw unit)
	MapSource       string                    `json:"mapSource"`                 // vrm0/vrm1/heuristic(+override)
	Mapping         []avataratlas.BoneMapPair `json:"mapping"`
	UnmappedJoints  []string                  `json:"unmappedJoints"`
	MapConflicts    int                       `json:"mapConflicts"`
	Warnings        []string                  `json:"warnings,omitempty"`
	Seed            int64                     `json:"seed"`
	Requested       int                       `json:"requested"`
	Emitted         int                       `json:"emitted"`
	Dropped         int                       `json:"dropped"`
	SkippedPrims    int                       `json:"skippedPrimitives"`
	HumanoidDups    int                       `json:"humanoidDupNodes"` // spec-violating duplicate node refs in the VRM humanoid map
	SlotIndex       int                       `json:"slotIndex"`
	BoneCount       int                       `json:"boneCount"`
	Width           int                       `json:"width"`
	Height          int                       `json:"height"`
	ModelMinM       [3]float64                `json:"modelMinM"` // model-space bounds of emitted points (metres)
	ModelMaxM       [3]float64                `json:"modelMaxM"`
	PerBone         map[string]int            `json:"perBone"` // histogram, §5 slot names
	Boxes           []reportBox               `json:"boxes"`
}

func buildReport(in, out string, seed int64, doc *avataratlas.Document, res *avataratlas.SampleResult, atlas *avataratlas.Atlas, mapping *avataratlas.BoneMapping) reportOut {
	r := reportOut{
		Input: in, Output: out, InputKind: doc.InputKind, VRMVersion: doc.VRMVersion,
		UnitScaleFactor: doc.FBXUnitScaleFactor, Seed: seed,
		MapSource: mapping.Source, Mapping: mapping.Pairs, UnmappedJoints: mapping.Unmapped,
		MapConflicts: mapping.Conflicts, Warnings: doc.Warnings,
		Requested: res.Requested, Emitted: len(atlas.Points), Dropped: res.Dropped,
		SkippedPrims: res.SkippedPrims, HumanoidDups: doc.HumanoidDupNodes,
		SlotIndex: atlas.SlotIndex, BoneCount: atlas.BoneCount,
		Width: avataratlas.Width, Height: avataratlas.AtlasHeight(len(atlas.Points)),
		ModelMinM: res.ModelMin, ModelMaxM: res.ModelMax,
		PerBone: map[string]int{},
	}
	for slot := 0; slot < avataratlas.BoneSlots; slot++ {
		if res.PerSlot[slot] == 0 && !atlas.Boxes[slot].Used() {
			continue
		}
		name := avataratlas.SlotName(slot)
		if name == "" {
			name = fmt.Sprintf("slot%d", slot)
		}
		r.PerBone[name] = res.PerSlot[slot]
		box := reportBox{Slot: slot, Name: name, Points: res.PerSlot[slot]}
		for ax := 0; ax < 3; ax++ {
			box.MinMm[ax] = int(atlas.Boxes[slot].Min[ax])
			box.SizeMm[ax] = int(atlas.Boxes[slot].Size[ax])
		}
		r.Boxes = append(r.Boxes, box)
	}
	return r
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "avatarscan:", err)
	os.Exit(1)
}

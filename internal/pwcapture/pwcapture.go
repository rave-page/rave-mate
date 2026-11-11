// Package pwcapture captures a PipeWire video node (e.g. Spout2PW's
// spout2pw.VRChat.VRCSender1, media.class Stream/Output/Video) and exposes it as a
// medialink.Source so a paired peer can request a route by node name - the Linux sender analog of
// the Windows Spout receiver.
//
// Node DISCOVERY is pure Go over the `pw-dump` CLI (no cgo, robust, no extra dep). The CAPTURE
// itself is a cgo shim that dlopens libpipewire-0.3 at runtime (design .devnotes/MEDIALINK_DESIGN.md
// §D8: no go.mod dep, GStreamer + pure-Go rejected), built only under `-tags pipewire` on Linux;
// every other build gets the no-op Manager (capture_noop.go) so a machine without PipeWire - or any
// non-Linux target - degrades cleanly instead of failing to launch.
package pwcapture

import (
	"context"
	"encoding/json"
	"os/exec"
	"sort"
	"strings"
	"time"

	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/sysexec"
)

const source = "pwcapture"

// captureClasses are the PipeWire media.class values we treat as routable video producers: an app
// output stream (Spout2PW, OBS, gst pipewiresink mode=provide) and a hardware/virtual camera.
var captureClasses = map[string]bool{
	"Stream/Output/Video": true,
	"Video/Source":        true,
}

// captureFormats are the SPA raw formats the capture shim offers (SHM + DMA-BUF). RGBA-family
// only in v1: an RGBA/BGRA/BGRx/RGBx producer (Spout2PW via libfunnel) negotiates directly; a
// YUY2/NV12 camera does not and is listed but not registered (no colour-convert path yet).
var captureFormats = map[string]bool{"BGRA": true, "RGBA": true, "BGRx": true, "RGBx": true}

// Node is a discovered PipeWire video node.
type Node struct {
	ID         uint32 // PipeWire global object id
	Name       string // node.name - the stable id a peer routes by (e.g. "spout2pw.VRChat.VRCSender1")
	Desc       string // node.description / media.name - human label
	MediaClass string
	Width      int // from EnumFormat (0 = not advertised)
	Height     int
	FPS        float64
	Capturable bool // EnumFormat offers an RGBA-family format the shim can negotiate
}

// Label returns the human label (description, else the node name).
func (n Node) Label() string {
	if n.Desc != "" {
		return n.Desc
	}
	return n.Name
}

// Router is the medialink seam the Manager registers sources on (subset of *medialink.RouteManager,
// mirrors webcam.Router).
type Router interface {
	RegisterSource(medialink.SourceDesc, medialink.SourceOpen)
	UnregisterSource(id string)
}

// sourceID is the stable medialink source id for a PipeWire node name.
func sourceID(nodeName string) string { return "pw:" + nodeName }

// DiscoverNodes lists PipeWire video producer nodes via `pw-dump`. ok=false when pw-dump is
// unavailable or errors (no PipeWire session) - the caller treats that as "no nodes".
func DiscoverNodes(ctx context.Context) ([]Node, bool) {
	pw, err := exec.LookPath("pw-dump")
	if err != nil {
		return nil, false
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, pw, "-N") // -N: no monitoring, one snapshot then exit
	sysexec.Hide(cmd)
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	return parsePwDump(out), true
}

// parsePwDump extracts video producer nodes from a pw-dump JSON array. Tolerant of unknown fields
// and shapes (pw-dump is a debug surface); anything it can't read is skipped, never fatal.
func parsePwDump(data []byte) []Node {
	var objs []pwObject
	if err := json.Unmarshal(data, &objs); err != nil {
		return nil
	}
	var nodes []Node
	for _, o := range objs {
		if !strings.HasSuffix(o.Type, "Node") {
			continue
		}
		p := o.Info.Props
		mc := p["media.class"]
		if !captureClasses[mc] {
			continue
		}
		name := p["node.name"]
		if name == "" {
			continue
		}
		w, h, fps, capturable := bestFormat(o.Info.Params)
		nodes = append(nodes, Node{
			ID: o.ID, Name: name, Desc: firstNonEmpty(p["node.description"], p["media.name"]),
			MediaClass: mc, Width: w, Height: h, FPS: fps, Capturable: capturable,
		})
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Name < nodes[j].Name })
	return nodes
}

// pwObject is the slice of a pw-dump entry we read (props are stringly-typed; sizes are read from
// params when present).
type pwObject struct {
	ID   uint32 `json:"id"`
	Type string `json:"type"`
	Info struct {
		Props  map[string]string          `json:"props"`
		Params map[string]json.RawMessage `json:"params"`
	} `json:"info"`
}

// fmtEntry is one EnumFormat/Format entry (pw-dump shape).
type fmtEntry struct {
	Format    string                      `json:"format"`
	Size      struct{ Width, Height int } `json:"size"`
	Framerate json.RawMessage             `json:"framerate"`
}

// bestFormat reads the preferred (w,h,fps) and whether the node offers an RGBA-family format the
// shim can capture. The negotiated Format wins over EnumFormat; within a list, a capturable entry
// wins over a non-capturable one. Returns zeros when nothing parses.
func bestFormat(params map[string]json.RawMessage) (w, h int, fps float64, capturable bool) {
	for _, key := range []string{"Format", "EnumFormat"} {
		raw, ok := params[key]
		if !ok {
			continue
		}
		var entries []fmtEntry
		if json.Unmarshal(raw, &entries) != nil {
			continue
		}
		for _, e := range entries {
			if e.Size.Width <= 0 || e.Size.Height <= 0 {
				continue
			}
			cap := captureFormats[e.Format]
			if w == 0 || (cap && !capturable) { // first valid, or upgrade to a capturable one
				w, h, fps, capturable = e.Size.Width, e.Size.Height, parseFPS(e.Framerate), cap
			}
			if capturable {
				return
			}
		}
	}
	return
}

// parseFPS handles both a fixed framerate {num,denom} and a ranged one {default:{num,denom},...}.
func parseFPS(raw json.RawMessage) float64 {
	if len(raw) == 0 {
		return 0
	}
	type frac struct{ Num, Denom float64 }
	var fr frac
	if json.Unmarshal(raw, &fr) == nil && fr.Num > 0 && fr.Denom > 0 {
		return fr.Num / fr.Denom
	}
	var ranged struct{ Default frac }
	if json.Unmarshal(raw, &ranged) == nil && ranged.Default.Num > 0 && ranged.Default.Denom > 0 {
		return ranged.Default.Num / ranged.Default.Denom
	}
	return 0
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

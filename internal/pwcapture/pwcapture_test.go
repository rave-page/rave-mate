package pwcapture

import "testing"

// Real pw-dump shapes (captured on sus): a BGRx producer (Spout2PW-class, capturable), a YUY2
// camera (Video/Source, not capturable - no RGBA-family format), a ranged-framerate camera, an
// audio node + a non-node object (both skipped).
const samplePwDump = `[
 {"id":124,"type":"PipeWire:Interface:Node","info":{"props":{"media.class":"Stream/Output/Video","node.name":"spout2pw.VRChat.VRCSender1","node.description":"VRChat Sender"},
   "params":{"Format":[],"EnumFormat":[{"mediaType":"video","mediaSubtype":"raw","format":"BGRx","size":{"width":1920,"height":1080},"framerate":{"num":60,"denom":1}}]}}},
 {"id":99,"type":"PipeWire:Interface:Node","info":{"props":{"media.class":"Video/Source","node.name":"v4l2_cam","node.description":"HD Pro Webcam C920 (V4L2)"},
   "params":{"EnumFormat":[{"mediaType":"video","mediaSubtype":"raw","format":"YUY2","size":{"width":640,"height":480},"framerate":{"default":{"num":30,"denom":1},"alt1":{"num":24,"denom":1}}}]}}},
 {"id":77,"type":"PipeWire:Interface:Node","info":{"props":{"media.class":"Audio/Sink","node.name":"speaker"}}},
 {"id":5,"type":"PipeWire:Interface:Port","info":{"props":{"media.class":"Stream/Output/Video","node.name":"not-a-node"}}}
]`

func TestParsePwDump(t *testing.T) {
	nodes := parsePwDump([]byte(samplePwDump))
	if len(nodes) != 2 {
		t.Fatalf("got %d video nodes, want 2: %+v", len(nodes), nodes)
	}
	// Sorted by name: spout2pw... < v4l2_cam
	spout := nodes[0]
	if spout.Name != "spout2pw.VRChat.VRCSender1" {
		t.Fatalf("node[0] = %q", spout.Name)
	}
	if !spout.Capturable {
		t.Error("BGRx producer should be capturable")
	}
	if spout.Width != 1920 || spout.Height != 1080 || spout.FPS != 60 {
		t.Errorf("producer dims = %dx%d@%v, want 1920x1080@60", spout.Width, spout.Height, spout.FPS)
	}
	if spout.Label() != "VRChat Sender" {
		t.Errorf("label = %q, want description", spout.Label())
	}
	cam := nodes[1]
	if cam.Capturable {
		t.Error("YUY2 camera must NOT be capturable (no RGBA-family format)")
	}
	if cam.FPS != 30 { // ranged framerate: default 30
		t.Errorf("camera fps = %v, want 30 (ranged default)", cam.FPS)
	}
}

func TestSourceID(t *testing.T) {
	if got := sourceID("spout2pw.VRChat.VRCSender1"); got != "pw:spout2pw.VRChat.VRCSender1" {
		t.Fatalf("sourceID = %q", got)
	}
}

func TestParsePwDumpGarbage(t *testing.T) {
	if n := parsePwDump([]byte("not json")); n != nil {
		t.Fatalf("garbage should yield nil, got %+v", n)
	}
	if n := parsePwDump([]byte("[]")); len(n) != 0 {
		t.Fatalf("empty array should yield no nodes, got %+v", n)
	}
}

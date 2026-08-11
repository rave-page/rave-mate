package videoedit

import (
	"strings"
	"testing"
)

func TestCropSizeAxes(t *testing.T) {
	// 1920×1080 → 9:16: full height, pan x, width 607→606 (even)
	cw, ch, ax := CropSize(1920, 1080, AspectByKey("9x16"))
	if cw != 606 || ch != 1080 || ax != "x" {
		t.Fatalf("9x16: %d×%d %s", cw, ch, ax)
	}
	// 1080×1920 → 16:9: full width, pan y
	cw, ch, ax = CropSize(1080, 1920, AspectByKey("16x9"))
	if cw != 1080 || ch != 606 || ax != "y" {
		t.Fatalf("16x9 tall: %d×%d %s", cw, ch, ax)
	}
	// square source to square target: no free axis
	_, _, ax = CropSize(1080, 1080, AspectByKey("1x1"))
	if ax != "" {
		t.Fatalf("match: axis %q", ax)
	}
	if cw, _, _ := CropSize(0, 100, AspectByKey("1x1")); cw != 0 {
		t.Fatal("bad input must yield zero")
	}
}

func TestCropFilterStatic(t *testing.T) {
	p := Project{Aspect: "9x16", Pan: 0.5}
	p.Normalize()
	got := p.CropFilter(1920, 1080, 0)
	// maxOff = 1920-606 = 1314; centered = 657
	if got != "crop=606:1080:657:0" {
		t.Fatalf("static: %s", got)
	}
	// orig aspect = no filter
	p.Aspect = "orig"
	if f := p.CropFilter(1920, 1080, 0); f != "" {
		t.Fatalf("orig: %q", f)
	}
}

func TestCropFilterKeyframesLerpAndTrimShift(t *testing.T) {
	p := Project{Aspect: "9x16", PanKF: []PanKey{{T: 10, X: 0}, {T: 20, X: 1}}}
	p.Normalize()
	got := p.CropFilter(1920, 1080, 10) // trim starts at the first key
	// keys shift to τ=0 and τ=10; maxOff 1314
	want := "crop=606:1080:'if(lt(t,0),0,if(lt(t,10),0+(1314-0)*(t-0)/10,1314))':0"
	if got != want {
		t.Fatalf("kf:\n got %s\nwant %s", got, want)
	}
	if !strings.Contains(got, "'") {
		t.Fatal("expression must be quoted against -vf comma splitting")
	}
}

func TestCropFilterSingleKeyIsStatic(t *testing.T) {
	p := Project{Aspect: "9x16", PanKF: []PanKey{{T: 5, X: 1}}}
	p.Normalize()
	if got := p.CropFilter(1920, 1080, 0); got != "crop=606:1080:1314:0" {
		t.Fatalf("single key: %s", got)
	}
}

func TestNormalizeSortsAndClamps(t *testing.T) {
	p := Project{Pan: 7, PanKF: []PanKey{{T: 9, X: 2}, {T: 3, X: -1}}}
	p.Normalize()
	if p.Pan != 0.5 || p.PanKF[0].T != 3 || p.PanKF[0].X != 0 || p.PanKF[1].X != 1 {
		t.Fatalf("normalize: %+v", p)
	}
	if p.Aspect != "orig" || p.PresetKey != "reel" {
		t.Fatalf("defaults: %+v", p)
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	p := Project{Source: `C:\sets\a.mp4`, Aspect: "4x5", Pan: 0.25,
		Effects: []EffectInst{{Kind: "frei0r", Ref: "glow", Params: map[string]float64{"blur": 0.5}}}}
	p.Normalize()
	data, err := p.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	q, err := Unmarshal(data)
	if err != nil || q.Source != p.Source || q.Aspect != "4x5" || q.Pan != 0.25 ||
		len(q.Effects) != 1 || q.Effects[0].Params["blur"] != 0.5 {
		t.Fatalf("roundtrip: %+v err=%v", q, err)
	}
}

func TestExportPreset(t *testing.T) {
	e := ExportPresetByKey("reel")
	pr := e.Preset()
	if pr.Width != 1080 || pr.Height != 1920 || pr.VideoCodec != "h264" || pr.Container != "mp4" {
		t.Fatalf("reel preset: %+v", pr)
	}
	if ExportPresetByKey("nope").Key != "reel" {
		t.Fatal("fallback must be reel")
	}
}

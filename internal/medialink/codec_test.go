package medialink

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// TestNegotiateCodecMatrix walks the §3.2 tier matrix.
func TestNegotiateCodecMatrix(t *testing.T) {
	allDec := []string{DecodeAV1, DecodeHEVC, DecodeH264, DecodeJPEG}
	cases := []struct {
		name     string
		enc, dec []string
		want     CodecChoice
		ok       bool
	}{
		{"tier1 av1 hw", []string{"hevc_nvenc", "av1_nvenc"}, allDec,
			CodecChoice{CodecAV1, "av1_nvenc", 1, false}, true},
		{"tier2 hevc default", []string{"hevc_nvenc", "h264_nvenc", "mjpeg"}, allDec,
			CodecChoice{CodecHEVC, "hevc_nvenc", 2, false}, true},
		{"tier2 amf variant", []string{"hevc_amf", "h264_amf"}, allDec,
			CodecChoice{CodecHEVC, "hevc_amf", 2, false}, true},
		{"tier3 requester decodes h264 only", []string{"av1_nvenc", "hevc_nvenc", "h264_qsv"}, []string{DecodeH264},
			CodecChoice{CodecH264, "h264_qsv", 3, false}, true},
		{"tier4 sw x264", []string{"libx264", "mjpeg"}, allDec,
			CodecChoice{CodecH264, "libx264", 4, true}, true},
		{"tier4 sw svt-av1 when only av1 decodable", []string{"libx264", "libsvtav1"}, []string{DecodeAV1},
			CodecChoice{CodecAV1, "libsvtav1", 4, true}, true},
		{"tier5 mjpeg floor", []string{"mjpeg"}, allDec,
			CodecChoice{CodecJPEG, "mjpeg", 5, false}, true},
		{"no overlap", []string{"hevc_nvenc"}, []string{DecodeJPEG}, CodecChoice{}, false},
		{"empty encoders", nil, allDec, CodecChoice{}, false},
		{"empty decoders", []string{"hevc_nvenc"}, nil, CodecChoice{}, false},
	}
	for _, c := range cases {
		got, ok := NegotiateCodec(c.enc, c.dec)
		if ok != c.ok || got != c.want {
			t.Errorf("%s: NegotiateCodec = %+v ok=%v, want %+v ok=%v", c.name, got, ok, c.want, c.ok)
		}
	}
	// Software tiers carry the §3.2 CPU warning; hardware tiers don't.
	sw, _ := NegotiateCodec([]string{"libx264"}, allDec)
	if sw.Warning() == "" {
		t.Fatal("software tier must warn")
	}
	hw, _ := NegotiateCodec([]string{"hevc_nvenc"}, allDec)
	if hw.Warning() != "" {
		t.Fatalf("hardware tier must not warn: %q", hw.Warning())
	}
}

// TestCapsCodecJSON: enc/dec ride Caps as omit-when-empty extensions.
func TestCapsCodecJSON(t *testing.T) {
	raw, err := json.Marshal(Caps{Report: true})
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"enc", "dec"} {
		if _, present := keys[k]; present {
			t.Fatalf("empty %q must be omitted: %s", k, raw)
		}
	}
	full := Caps{Sync: true, Encoders: []string{"hevc_nvenc"}, Decoders: []string{DecodeHEVC}}
	raw, _ = json.Marshal(full)
	var got Caps
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Sync || len(got.Encoders) != 1 || got.Encoders[0] != "hevc_nvenc" || got.Decoders[0] != DecodeHEVC {
		t.Fatalf("round-trip: %+v", got)
	}
}

// TestVideoCodecNegotiatedOnRoute: a video offer between capability-advertising nodes lands on
// the highest common tier; the Answer records codec + chosen encoder; adverts carry the sets.
func TestVideoCodecNegotiatedOnRoute(t *testing.T) {
	h := &hub{}
	secrets := fakeSecrets{key: testKey()}
	rmB := New(Options{Self: "B", Bus: &busView{h, "B"}, Secrets: secrets, Ports: []int{0}, AdvertHost: "127.0.0.1",
		Encoders: []string{"hevc_nvenc", "h264_nvenc", "mjpeg"}})
	rmA := New(Options{Self: "A", Bus: &busView{h, "A"}, Secrets: secrets, Ports: []int{0}, AdvertHost: "127.0.0.1",
		Decoders: []string{DecodeHEVC, DecodeH264, DecodeJPEG}}) // no AV1 decode

	rmB.RegisterSource(SourceDesc{ID: "obs", Name: "OBS Spout", Kind: KindVideo, Codec: CodecBGRA,
		Width: 1920, Height: 1080, FPS: 60},
		func(context.Context, Offer) (Source, error) { return &sliceSource{}, nil })
	rmA.RegisterSink(SinkDesc{ID: "out", Kind: KindVideo},
		func(context.Context, Answer) (Sink, error) { return &collectSink{done: make(chan struct{})}, nil })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := rmB.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer rmB.Stop()
	if err := rmA.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer rmA.Stop()

	// Adverts advertise the capability sets (§3.2 "capability advertisement").
	rmB.Advertise()
	waitFor(t, time.Second, func() bool {
		ad, ok := rmA.RemoteAdverts()["B"]
		return ok && ad.Caps != nil && len(ad.Caps.Encoders) == 3
	})

	ansCh := make(chan Answer, 1)
	unsub := (&busView{h, "observer"}).Subscribe(TopicAnswer, func(ev Event) {
		var a Answer
		if json.Unmarshal(ev.Data, &a) == nil {
			select {
			case ansCh <- a:
			default:
			}
		}
	})
	defer unsub()
	if _, err := rmA.Offer("B", "obs", "out", CodecNone); err != nil {
		t.Fatal(err)
	}
	select {
	case a := <-ansCh:
		if !a.Accept || a.Codec != CodecHEVC {
			t.Fatalf("want HEVC (tier 2, no AV1 decode on A): %+v", a)
		}
		if a.Caps == nil || len(a.Caps.Encoders) != 1 || a.Caps.Encoders[0] != "hevc_nvenc" {
			t.Fatalf("chosen encoder not recorded: %+v", a.Caps)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no answer")
	}
}

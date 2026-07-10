package train

import "testing"

func TestParseTrainEvent(t *testing.T) {
	ev, ok := parseTrainEvent([]byte(`{"ev":"start","tracks":12,"device":"cuda"}`))
	if !ok || ev.Kind != "start" || ev.Tracks != 12 || ev.Device != "cuda" {
		t.Fatalf("start = %+v ok=%v", ev, ok)
	}

	ev, ok = parseTrainEvent([]byte(`{"ev":"epoch","n":3,"loss":0.041,"valFBeat":0.93,"valFDown":0.81}`))
	if !ok || ev.Kind != "epoch" || ev.Epoch != 3 || ev.Loss != 0.041 ||
		ev.ValFBeat != 0.93 || ev.ValFDown != 0.81 {
		t.Fatalf("epoch = %+v ok=%v", ev, ok)
	}

	ev, ok = parseTrainEvent([]byte(`{"ev":"done","checkpoint":"C:\\m\\finetuned-20260710-1200.ckpt","report":{"beforeFBeat":0.88,"afterFBeat":0.94,"improved":true}}`))
	if !ok || ev.Kind != "done" || ev.Checkpoint != `C:\m\finetuned-20260710-1200.ckpt` ||
		ev.BeforeF != 0.88 || ev.AfterF != 0.94 || !ev.Improved {
		t.Fatalf("done = %+v ok=%v", ev, ok)
	}

	ev, ok = parseTrainEvent([]byte(`{"ev":"error","msg":"RuntimeError: boom"}`))
	if !ok || ev.Kind != "error" || ev.Msg != "RuntimeError: boom" {
		t.Fatalf("error = %+v ok=%v", ev, ok)
	}

	for _, bad := range []string{"", "not json", `{"no":"ev"}`, `[1,2]`} {
		if _, ok := parseTrainEvent([]byte(bad)); ok {
			t.Fatalf("parsed non-event %q", bad)
		}
	}
}

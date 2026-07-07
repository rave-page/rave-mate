package fingerprint

import (
	"context"
	"encoding/json"
	"testing"
)

// ── fake doubles ──────────────────────────────────────────────────────────────

type fakeWorkerRunner struct {
	calledTyp    string
	calledMethod string
	calledParams any
	result       json.RawMessage
	err          error
}

func (f *fakeWorkerRunner) Run(_ context.Context, typ, method string, params any) (json.RawMessage, error) {
	f.calledTyp = typ
	f.calledMethod = method
	f.calledParams = params
	return f.result, f.err
}

type fakeSubmitter struct {
	called bool
	got    SubmitIn
}

func (f *fakeSubmitter) SubmitFingerprint(_ context.Context, in SubmitIn) error {
	f.called = true
	f.got = in
	return nil
}

// ── tests ─────────────────────────────────────────────────────────────────────

func TestRunnerProcess(t *testing.T) {
	const (
		testPath = "/music/track.flac"
		testFP   = "AQAAZ0mkUUmY5lGSRIk"
		testDur  = 183.5
	)
	cannedResult, _ := json.Marshal(map[string]any{
		"fingerprint":     testFP,
		"durationSeconds": testDur,
	})

	fw := &fakeWorkerRunner{result: cannedResult}
	fs := &fakeSubmitter{}
	r := &Runner{Workers: fw, Submit: fs}

	out, err := r.Process(context.Background(), testPath)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}

	// Worker called with right type + method.
	if fw.calledTyp != "fingerprint" {
		t.Errorf("worker type: got %q want %q", fw.calledTyp, "fingerprint")
	}
	if fw.calledMethod != "fingerprint.compute" {
		t.Errorf("worker method: got %q want %q", fw.calledMethod, "fingerprint.compute")
	}
	// Path forwarded correctly.
	p, ok := fw.calledParams.(workerComputeParams)
	if !ok {
		t.Fatalf("params type: got %T", fw.calledParams)
	}
	if p.Path != testPath {
		t.Errorf("params.Path: got %q want %q", p.Path, testPath)
	}

	// Parsed result correct.
	if out.Fingerprint != testFP {
		t.Errorf("Fingerprint: got %q want %q", out.Fingerprint, testFP)
	}
	if out.DurationSeconds != testDur {
		t.Errorf("DurationSeconds: got %v want %v", out.DurationSeconds, testDur)
	}
	if out.Path != testPath {
		t.Errorf("Path: got %q want %q", out.Path, testPath)
	}

	// Submitter received the parsed values.
	if !fs.called {
		t.Fatal("Submitter.SubmitFingerprint not called")
	}
	if fs.got.Fingerprint != testFP || fs.got.DurationSeconds != testDur || fs.got.Path != testPath {
		t.Errorf("Submitter received wrong SubmitIn: %+v", fs.got)
	}
}

func TestRunnerProcessNilSubmitter(t *testing.T) {
	cannedResult, _ := json.Marshal(map[string]any{
		"fingerprint":     "AQAAZ0mkUUmY5lGSRIk",
		"durationSeconds": 60.0,
	})
	fw := &fakeWorkerRunner{result: cannedResult}
	r := &Runner{Workers: fw, Submit: nil}
	out, err := r.Process(context.Background(), "/tmp/x.mp3")
	if err != nil {
		t.Fatalf("Process (nil Submit): %v", err)
	}
	if out.Fingerprint == "" {
		t.Error("expected non-empty fingerprint")
	}
}

func TestRunnerProcessEmptyFingerprint(t *testing.T) {
	cannedResult, _ := json.Marshal(map[string]any{
		"fingerprint":     "",
		"durationSeconds": 10.0,
	})
	fw := &fakeWorkerRunner{result: cannedResult}
	r := &Runner{Workers: fw}
	_, err := r.Process(context.Background(), "/tmp/x.mp3")
	if err == nil {
		t.Fatal("expected error for empty fingerprint, got nil")
	}
}

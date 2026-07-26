//go:build windows && cgo

package mediapipe

import (
	"context"
	"io"
	"os"
	"testing"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/mediatools"
	"rave.page/mate/internal/mfenc"
)

// TestAbsentChildExeGateRefusesNative (field #166, exact conditions): HW present, child
// exe truly ABSENT (no env, no embed, no sidecar, no repo tree) - the advertisement gate
// must refuse AND a native-spec route open must still land on ffmpeg, never fatal.
func TestAbsentChildExeGateRefusesNative(t *testing.T) {
	ffmpeg, ok := mediatools.Resolve("ffmpeg")
	if !ok {
		t.Skip("no ffmpeg")
	}
	if !mfenc.Available() {
		t.Skip("no hardware MF encoder")
	}
	if mfenc.HasEmbeddedChild() {
		t.Skip("encembed build: the child cannot be absent - which is the fix under test")
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// cwd → temp dir: encExePath's repo-tree walk finds nothing; test binary dir has no
	// sidecar; untagged test build has no embed = the self-updated-install state.
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.Chdir(wd)
		mfenc.RefreshChildAvailable() // restore the gate verdict for later tests
	}()
	if mfenc.RefreshChildAvailable() {
		t.Fatal("gate advertised native with the child exe absent")
	}

	probeMu.Lock()
	saved := probeCached
	probeCached = map[string]Caps{ffmpeg: {Encoders: []string{"libx264"}, Validated: true}}
	probeMu.Unlock()
	defer func() { probeMu.Lock(); probeCached = saved; probeMu.Unlock() }()
	log := logbus.New(64)
	encF, _ := Factories(log)
	spec := medialink.EncodeSpec{Encoder: medialink.EncoderMFNative, Codec: medialink.CodecH264,
		Width: 128, Height: 96, FPS: 30, BitrateKbps: 300}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	src, err := encF(ctx, spec, &rawSrc{w: 128, h: 96, n: 30})
	if err != nil {
		t.Fatalf("route must degrade with child absent, got error: %v", err)
	}
	defer func() { _ = src.Close() }()
	f, err := src.Next(ctx)
	if err != nil || f.Codec != medialink.CodecH264 {
		t.Fatalf("frame err=%v codec=%v", err, f.Codec)
	}
}

// TestMissingChildExeDegradesToFfmpeg (field crash #166): the encoder child exe is ABSENT
// on self-updated installs. A negotiated h264_mf_native route open must then degrade to
// the probed ffmpeg H.264 encoder - never fatal the media child.
func TestMissingChildExeDegradesToFfmpeg(t *testing.T) {
	ffmpeg, ok := mediatools.Resolve("ffmpeg")
	if !ok {
		t.Skip("no ffmpeg")
	}
	if !mfenc.Available() {
		t.Skip("no hardware MF encoder")
	}
	t.Setenv("RAVE_MATE_ENC_EXE", `C:\definitely\not\here\rave-mate-enc.exe`)
	probeMu.Lock()
	saved := probeCached
	probeCached = map[string]Caps{ffmpeg: {Encoders: []string{"libx264"}, Validated: true}}
	probeMu.Unlock()
	defer func() { probeMu.Lock(); probeCached = saved; probeMu.Unlock() }()

	log := logbus.New(64)
	encF, _ := Factories(log)
	spec := medialink.EncodeSpec{Encoder: medialink.EncoderMFNative, Codec: medialink.CodecH264,
		Width: 128, Height: 96, FPS: 30, BitrateKbps: 300}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	src, err := encF(ctx, spec, &rawSrc{w: 128, h: 96, n: 60})
	if err != nil {
		t.Fatalf("factory must degrade on missing child exe, got error: %v", err)
	}
	defer func() { _ = src.Close() }()
	for got := 0; got < 3; got++ {
		f, err := src.Next(ctx)
		if err == io.EOF {
			t.Fatalf("EOF after %d frames", got)
		}
		if err != nil {
			t.Fatalf("Next after %d frames: %v", got, err)
		}
		if f.Codec != medialink.CodecH264 {
			t.Fatalf("codec=%v want H.264", f.Codec)
		}
	}
}

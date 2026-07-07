package worker

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rave.page/mate/internal/mp4meta"
)

func rvCall(t *testing.T, params string) error {
	t.Helper()
	_, err := rvMotionVideo(json.RawMessage(params), func(string, any) {})
	return err
}

func TestRenderMotionVideoValidation(t *testing.T) {
	if err := rvCall(t, `{}`); err == nil || !strings.Contains(err.Error(), "recording/out") {
		t.Fatalf("missing params: %v", err)
	}
	if err := rvCall(t, `{"recording":"x.json","out":"x.mp4","mode":"cinematic"}`); err == nil || !strings.Contains(err.Error(), "unknown mode") {
		t.Fatalf("bad mode: %v", err)
	}
	if err := rvCall(t, `{"recording":"Z:/nope/take.json","out":"x.mp4"}`); err == nil || !strings.Contains(err.Error(), "load recording") {
		t.Fatalf("missing recording file: %v", err)
	}
}

func TestRenderWorkerRegistered(t *testing.T) {
	if !KnownType("render") {
		t.Fatal("render worker type not registered")
	}
}

// rvFakeMP4 builds a minimal ftyp+moov(trak/mdia/hdlr 'vide')+mdat file.
func rvFakeMP4(t *testing.T) string {
	t.Helper()
	mk := func(typ string, payload ...[]byte) []byte {
		n := 8
		for _, p := range payload {
			n += len(p)
		}
		b := make([]byte, 8, n)
		binary.BigEndian.PutUint32(b[:4], uint32(n))
		copy(b[4:], typ)
		for _, p := range payload {
			b = append(b, p...)
		}
		return b
	}
	hdlr := make([]byte, 24)
	copy(hdlr[8:12], "vide")
	f := append(mk("ftyp", []byte("isom\x00\x00\x00\x00")),
		mk("moov", mk("trak", mk("mdia", mk("hdlr", hdlr))))...)
	f = append(f, mk("mdat", []byte("xx"))...)
	p := filepath.Join(t.TempDir(), "r.mp4")
	if err := os.WriteFile(p, f, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFinalizeOrbitUntouched(t *testing.T) {
	p := rvFakeMP4(t)
	before, _ := os.ReadFile(p)
	spherical, err := rvFinalize("orbit", p)
	if err != nil || spherical {
		t.Fatalf("orbit finalize: spherical=%v err=%v", spherical, err)
	}
	after, _ := os.ReadFile(p)
	if !bytes.Equal(before, after) {
		t.Fatal("flat render modified by finalize")
	}
}

func TestFinalizeEquirectInjects(t *testing.T) {
	p := rvFakeMP4(t)
	before, _ := os.ReadFile(p)
	spherical, err := rvFinalize("equirect", p)
	if err != nil || !spherical {
		t.Fatalf("equirect finalize: spherical=%v err=%v", spherical, err)
	}
	after, _ := os.ReadFile(p)
	if len(after) <= len(before) {
		t.Fatal("no bytes injected")
	}
	if !bytes.Contains(after, mp4meta.SphericalUUID[:]) || !bytes.Contains(after, []byte("equirectangular")) {
		t.Fatal("spherical uuid/xml missing")
	}
}

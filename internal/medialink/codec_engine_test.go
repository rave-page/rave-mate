package medialink

import "testing"

// WP-1: the pipe-free native engine must WIN the negotiation whenever it is advertised and the peer
// decodes H.264 - the pipe-fed AV1/HEVC tiers push multi-GB/s of raw RGBA through an ffmpeg stdin
// pipe on the sender, which is the melt. Without it, the old tier order must be untouched.
func TestNegotiatePipeFreePreemption(t *testing.T) {
	allDec := []string{DecodeAV1, DecodeHEVC, DecodeH264, DecodeJPEG}
	px1080p60 := float64(1920 * 1080 * 60)

	// mfenc available (EncoderMFNative advertised) → H.264 native beats hevc_nvenc AND av1_nvenc.
	enc := []string{"av1_nvenc", "hevc_nvenc", "h264_nvenc", EncoderMFNative, "libx264", "mjpeg"}
	got, ok := NegotiateCodecFor(enc, allDec, px1080p60)
	if !ok || got.Encoder != EncoderMFNative || got.Codec != CodecH264 || got.Tier != 3 || got.Software {
		t.Fatalf("mfenc rig: got %+v ok=%v, want %s h264 tier3 hw", got, ok, EncoderMFNative)
	}
	// Same rig WITHOUT the native engine → the classic tier walk (AV1 first).
	noMF := []string{"av1_nvenc", "hevc_nvenc", "h264_nvenc", "libx264", "mjpeg"}
	got, ok = NegotiateCodecFor(noMF, allDec, px1080p60)
	if !ok || got.Encoder != "av1_nvenc" || got.Tier != 1 {
		t.Fatalf("no-mfenc rig: got %+v ok=%v, want av1_nvenc tier1", got, ok)
	}
	// Peer cannot decode H.264 → no preemption, HEVC still wins.
	got, ok = NegotiateCodecFor(enc, []string{DecodeHEVC, DecodeJPEG}, px1080p60)
	if !ok || got.Encoder != "hevc_nvenc" {
		t.Fatalf("hevc-only peer: got %+v ok=%v, want hevc_nvenc", got, ok)
	}
	// Native engine is a HARDWARE tier-3 choice - a 4K60 source must not be pushed off it.
	got, ok = NegotiateCodecFor([]string{EncoderMFNative, "libx264", "mjpeg"}, allDec, float64(3840*2160*60))
	if !ok || got.Encoder != EncoderMFNative {
		t.Fatalf("4K60 mfenc rig: got %+v ok=%v, want %s", got, ok, EncoderMFNative)
	}
	// SWOnly-style advertisement (no native engine, no hw) keeps the software tier.
	got, ok = NegotiateCodecFor([]string{"libx264", "mjpeg"}, allDec, float64(1280*720*60))
	if !ok || got.Encoder != "libx264" || !got.Software {
		t.Fatalf("sw-only rig: got %+v ok=%v, want libx264 software", got, ok)
	}
	// EncoderTier must resolve the new name (the receiving side derives its stats from it).
	if tier, sw, ok := EncoderTier(EncoderMFNative); !ok || tier != 3 || sw {
		t.Fatalf("EncoderTier(%s) = %d,%v,%v; want 3,false,true", EncoderMFNative, tier, sw, ok)
	}
}

// The SENDER's own codec preference (MediaLink.PreferCodec mirrored onto the send side) outranks both
// the tier order and the pipe-free preemption, but only when it is satisfiable.
func TestNegotiateSenderPreference(t *testing.T) {
	allDec := []string{DecodeAV1, DecodeHEVC, DecodeH264, DecodeJPEG}
	enc := []string{"av1_nvenc", "hevc_nvenc", "h264_nvenc", EncoderMFNative, "libx264", "mjpeg"}
	px := float64(1920 * 1080 * 60)

	got, ok := Negotiate(enc, allDec, NegotiateOpts{PixelRate: px, Prefer: "hevc"})
	if !ok || got.Encoder != "hevc_nvenc" {
		t.Fatalf("prefer hevc: got %+v ok=%v", got, ok)
	}
	got, ok = Negotiate(enc, allDec, NegotiateOpts{PixelRate: px, Prefer: "mjpeg"})
	if !ok || got.Encoder != "mjpeg" || got.Tier != 5 {
		t.Fatalf("prefer mjpeg: got %+v ok=%v", got, ok)
	}
	// h264 preference lands on the pipe-free engine (first in the tier).
	got, ok = Negotiate(enc, allDec, NegotiateOpts{PixelRate: px, Prefer: "h264"})
	if !ok || got.Encoder != EncoderMFNative {
		t.Fatalf("prefer h264: got %+v ok=%v", got, ok)
	}
	// Unsatisfiable preference (peer can't decode it) must NOT refuse the route - fall through.
	got, ok = Negotiate(enc, []string{DecodeH264}, NegotiateOpts{PixelRate: px, Prefer: "hevc"})
	if !ok || got.Encoder != EncoderMFNative {
		t.Fatalf("prefer hevc vs h264-only peer: got %+v ok=%v, want fallthrough", got, ok)
	}
	// Preference we hold no encoder for: same fallthrough.
	got, ok = Negotiate([]string{"hevc_nvenc"}, allDec, NegotiateOpts{PixelRate: px, Prefer: "h264"})
	if !ok || got.Encoder != "hevc_nvenc" {
		t.Fatalf("prefer h264 without an h264 encoder: got %+v ok=%v", got, ok)
	}
	// A preferred SOFTWARE tier stays gated by the pixel-rate rule.
	got, ok = Negotiate([]string{"libx264", "mjpeg"}, allDec,
		NegotiateOpts{PixelRate: float64(3840 * 2160 * 60), Prefer: "h264"})
	if !ok || got.Encoder != "mjpeg" {
		t.Fatalf("prefer h264 at 4K60 sw-only: got %+v ok=%v, want mjpeg", got, ok)
	}
}

// A hard encoder pin (MediaLink.Encoder) outranks everything it can legally serve.
func TestNegotiateEncoderPin(t *testing.T) {
	allDec := []string{DecodeAV1, DecodeHEVC, DecodeH264, DecodeJPEG}
	enc := []string{"av1_nvenc", "hevc_nvenc", "h264_nvenc", EncoderMFNative, "libx264", "mjpeg"}
	px := float64(1280 * 720 * 60)

	got, ok := Negotiate(enc, allDec, NegotiateOpts{PixelRate: px, PinEncoder: "libx264"})
	if !ok || got.Encoder != "libx264" || !got.Software || got.Tier != 4 {
		t.Fatalf("pin libx264: got %+v ok=%v", got, ok)
	}
	got, ok = Negotiate(enc, allDec, NegotiateOpts{PixelRate: px, PinEncoder: "h264_nvenc"})
	if !ok || got.Encoder != "h264_nvenc" {
		t.Fatalf("pin h264_nvenc: got %+v ok=%v", got, ok)
	}
	// Pin we don't hold → fall through to the normal precedence (pipe-free preemption).
	got, ok = Negotiate(enc, allDec, NegotiateOpts{PixelRate: px, PinEncoder: "hevc_qsv"})
	if !ok || got.Encoder != EncoderMFNative {
		t.Fatalf("pin absent encoder: got %+v ok=%v", got, ok)
	}
	// Pin the peer cannot decode → fall through, never refuse.
	got, ok = Negotiate(enc, []string{DecodeH264}, NegotiateOpts{PixelRate: px, PinEncoder: "hevc_nvenc"})
	if !ok || got.Encoder != EncoderMFNative {
		t.Fatalf("pin undecodable codec: got %+v ok=%v", got, ok)
	}
	// Pin + pixel rate: libx264 at 4K60 is refused by the software gate, so we fall through.
	got, ok = Negotiate(enc, allDec, NegotiateOpts{PixelRate: float64(3840 * 2160 * 60), PinEncoder: "libx264"})
	if !ok || got.Encoder != EncoderMFNative {
		t.Fatalf("pin libx264 at 4K60: got %+v ok=%v, want fallthrough", got, ok)
	}
	// A pin beats an opposing preference (the pin is the more specific instruction).
	got, ok = Negotiate(enc, allDec, NegotiateOpts{PixelRate: px, Prefer: "mjpeg", PinEncoder: "hevc_nvenc"})
	if !ok || got.Encoder != "hevc_nvenc" {
		t.Fatalf("pin vs prefer: got %+v ok=%v", got, ok)
	}
}

// EncodeSpec.Device: only a resolved (LUID, index>=0) pair is a device - the zero value must read as
// "engine default" so every existing spec builder keeps emitting no device flags.
func TestEncodeSpecDevice(t *testing.T) {
	if _, _, ok := (EncodeSpec{}).Device(); ok {
		t.Fatal("zero spec must not report a device")
	}
	if _, _, ok := (EncodeSpec{DeviceLUID: "0x00000000_0x0000c34f", DeviceIndex: -1}).Device(); ok {
		t.Fatal("LUID with an unresolved index must not report a device")
	}
	if _, _, ok := (EncodeSpec{DeviceIndex: 1}).Device(); ok {
		t.Fatal("index without a LUID must not report a device")
	}
	luid, idx, ok := EncodeSpec{DeviceLUID: "0x00000000_0x0000c34f", DeviceIndex: 0}.Device()
	if !ok || idx != 0 || luid != "0x00000000_0x0000c34f" {
		t.Fatalf("adapter 0 pin: %q %d %v", luid, idx, ok)
	}
}

package mediapipe

import (
	"fmt"
	"strconv"
	"strings"

	"rave.page/mate/internal/medialink"
)

// args.go - pure ffmpeg argv builders for the encode/decode children (§3.2 LL settings: no
// B-frames, zerolatency-class tuning, ~2 s GOP, capped VBV).

// defaultBitrateKbps scales the §3.1 1080p60-HEVC-class budget (20 Mbps) by pixel rate,
// clamped to [2, 80] Mbps.
func defaultBitrateKbps(w, h int, fps float64) int {
	if fps <= 0 {
		fps = 30
	}
	ref := 1920.0 * 1080 * 60
	kbps := int(20_000 * (float64(w) * float64(h) * fps) / ref)
	if kbps < 2_000 {
		kbps = 2_000
	}
	if kbps > 80_000 {
		kbps = 80_000
	}
	return kbps
}

// gopFrames is the §3.2 ~2 s GOP.
func gopFrames(fps float64) int {
	if fps <= 0 {
		fps = 30
	}
	g := int(2 * fps)
	if g < 2 {
		g = 2
	}
	return g
}

// hwDeviceD3D11 is the label the d3d11va hardware context is registered under when a pinned adapter
// is the ONLY reason the child needs one (AMF / Media Foundation: no per-encoder device option).
const hwDeviceD3D11 = "rvd3d"

// scaleFilter builds the downscale chain for an explicit MaxHeight (outH = target height).
//
// Hardware encoder families with a stable GPU scaler get one: the CPU then only packs RGBA→NV12
// and the resize runs on the silicon that is about to encode the frame anyway, instead of swscale
// resizing 33 MB of RGBA per 4K frame on the cores OBS wants. scale_cuda (NVENC) and scale_qsv
// (QSV) have been in ffmpeg since 4.x; AMF/D3D11 keep swscale because scale_amf / scale_d3d11 are
// ffmpeg-7.1-era filters we cannot assume on a user's PATH ffmpeg. swscale is also the fallback
// after a hw-filter failure (forceSW - see encoder.run's early-fail demotion).
//
// The returned init flags claim the child's ONE -init_hw_device / -filter_hw_device pair. A pinned
// encode device (WP-3) is folded INTO these same strings by planEncodeDevice - there is never a
// second pair, and planEncodeDevice is the only place that decides which pair a child gets.
func scaleFilter(encoder string, outH int, forceSW bool) (init []string, vf string) {
	sw := fmt.Sprintf("scale=-2:%d", outH)
	if forceSW {
		return nil, sw
	}
	switch {
	case strings.HasSuffix(encoder, "_nvenc"):
		return []string{"-init_hw_device", "cuda=rvcu", "-filter_hw_device", "rvcu"},
			fmt.Sprintf("format=nv12,hwupload_cuda,scale_cuda=-2:%d", outH)
	case strings.HasSuffix(encoder, "_qsv"):
		return []string{"-init_hw_device", "qsv=rvqsv", "-filter_hw_device", "rvqsv"},
			fmt.Sprintf("format=nv12,hwupload=extra_hw_frames=16,scale_qsv=-2:%d", outH)
	}
	return nil, sw
}

// hwScaleFamily reports whether scaleFilter would use a GPU scaler for this encoder.
func hwScaleFamily(encoder string) bool {
	init, _ := scaleFilter(encoder, 1080, false)
	return len(init) > 0
}

// encodeDevicePlan is the resolved hardware-context decision for ONE child: at most one
// -init_hw_device/-filter_hw_device pair (ffmpeg allows several, but two devices for one frame path
// is how you get a filter graph feeding an encoder on the wrong GPU), the -vf chain, and the
// per-encoder device selector.
type encodeDevicePlan struct {
	init []string // -init_hw_device + -filter_hw_device pair (global: before -i)
	vf   string   // -vf chain ("" = none)
	sel  []string // per-encoder device option (after -c:v)
}

// planEncodeDevice composes the MaxHeight downscale chain (capture path, WP-5) with the pinned
// encode device (WP-3). Precedence, so exactly one hardware context is ever created:
//
//  1. A GPU scaler is in use (nvenc/qsv, not demoted to swscale) → the pinned adapter is folded into
//     THAT device spec ("cuda=rvcu:1", "qsv=rvqsv,child_device=1"). No per-encoder selector then: the
//     encoder inherits the device from the hardware frames the filter hands it, and a second
//     selector would fight the frames context.
//  2. No GPU scaler, adapter pinned → the per-encoder option (-gpu for NVENC, -qsv_device for QSV),
//     or a d3d11va context for AMF / *_mf, which expose no device option at all in ffmpeg.
//  3. Nothing pinned → byte-identical argv to the auto path (no device flags anywhere), so a build
//     without d3d11va/cuda can only ever be affected by an explicit pin.
//
// Caveat, deliberate: NVENC's -gpu and cuda= take a CUDA ordinal, which usually but not always
// matches the DXGI ordinal we resolved. The native MF engine binds by LUID and has no such ambiguity
// - it is the accurate path, and the default one for H.264.
func planEncodeDevice(spec medialink.EncodeSpec, scaled, forceSW bool, outH int) encodeDevicePlan {
	var p encodeDevicePlan
	if scaled {
		p.init, p.vf = scaleFilter(spec.Encoder, outH, forceSW)
	}
	_, idx, pinned := spec.Device()
	if !pinned {
		return p
	}
	if len(p.init) > 0 {
		p.init = foldAdapter(p.init, idx)
		return p
	}
	switch {
	case strings.HasSuffix(spec.Encoder, "_nvenc"):
		p.sel = []string{"-gpu", strconv.Itoa(idx)}
	case strings.HasSuffix(spec.Encoder, "_qsv"):
		p.sel = []string{"-qsv_device", strconv.Itoa(idx)}
	case strings.HasSuffix(spec.Encoder, "_amf"), strings.HasSuffix(spec.Encoder, "_mf"):
		p.init = []string{"-init_hw_device", fmt.Sprintf("d3d11va=%s:%d", hwDeviceD3D11, idx),
			"-filter_hw_device", hwDeviceD3D11}
	}
	return p
}

// foldAdapter rewrites a scaler's device spec to name the pinned adapter (cuda takes ":<ordinal>",
// qsv a "child_device=<ordinal>" option). Unknown device types are left alone.
func foldAdapter(init []string, idx int) []string {
	out := append([]string(nil), init...)
	for i := 0; i+1 < len(out); i++ {
		if out[i] != "-init_hw_device" {
			continue
		}
		switch {
		case strings.HasPrefix(out[i+1], "cuda="):
			out[i+1] += ":" + strconv.Itoa(idx)
		case strings.HasPrefix(out[i+1], "qsv="):
			out[i+1] += ",child_device=" + strconv.Itoa(idx)
		}
	}
	return out
}

// encodeArgs builds the raw-RGBA-stdin → bitstream-stdout child argv. forceSW pins the MaxHeight
// downscale to CPU swscale (used after a GPU-scaler failure).
func encodeArgs(spec medialink.EncodeSpec, forceSW bool) []string {
	fps := spec.FPS
	if fps <= 0 {
		fps = 30
	}
	// Downscale ceiling: encode a 4K source at (default) 1080p - the single biggest CPU
	// lever on software tiers. Bitrate defaults follow the OUTPUT pixel rate.
	outW, outH := spec.Width, spec.Height
	scaled := spec.MaxHeight > 0 && spec.Height > spec.MaxHeight
	if scaled {
		outH = spec.MaxHeight
		outW = spec.Width * outH / spec.Height // approximation for bitrate math; ffmpeg computes the even width
	}
	kbps := spec.BitrateKbps
	if kbps <= 0 {
		kbps = defaultBitrateKbps(outW, outH, fps)
	}
	dev := planEncodeDevice(spec, scaled, forceSW, outH)
	args := []string{"-hide_banner", "-loglevel", "error", "-fflags", "nobuffer"}
	args = append(args, dev.init...) // the child's ONE hardware context - global, before the input
	args = append(args,
		"-f", "rawvideo", "-pix_fmt", "rgba",
		"-video_size", fmt.Sprintf("%dx%d", spec.Width, spec.Height),
		"-framerate", trimFloat(fps),
		"-i", "-", "-an",
	)
	if dev.vf != "" {
		args = append(args, "-vf", dev.vf)
	}
	g := strconv.Itoa(gopFrames(fps))
	br := strconv.Itoa(kbps) + "k"
	vbv := strconv.Itoa(kbps/2) + "k"
	rc := []string{"-b:v", br, "-maxrate", br, "-bufsize", vbv, "-g", g, "-bf", "0"}
	switch spec.Encoder {
	case medialink.EncoderMFNative:
		// Defensive: the native engine has no ffmpeg counterpart name. Reached only when the
		// engine keying in Factories was bypassed; h264_mf is the ffmpeg Media Foundation wrapper.
		args = append(args, "-c:v", "h264_mf")
		args = append(args, rc...)
	case "libx264":
		args = append(args, "-c:v", "libx264", "-preset", "superfast", "-tune", "zerolatency")
		args = append(args, rc...)
	case "h264_nvenc", "hevc_nvenc":
		args = append(args, "-c:v", spec.Encoder, "-preset", "p1", "-tune", "ull", "-delay", "0")
		args = append(args, rc...)
	case "h264_qsv", "hevc_qsv":
		args = append(args, "-c:v", spec.Encoder, "-preset", "veryfast", "-async_depth", "1")
		args = append(args, rc...)
	case "h264_amf":
		// AMF omits SPS/PPS in-band by default → the bitstream filter/decoder starves. header_spacing
		// repeats them every GOP; forced_idr makes the -g keyframes true IDRs.
		args = append(args, "-c:v", spec.Encoder, "-usage", "ultralowlatency",
			"-forced_idr", "1", "-header_spacing", g)
		args = append(args, rc...)
	case "hevc_amf":
		// AMF omits VPS/SPS/PPS by default (default header_insertion_mode -1) - the "VPS id 0 not
		// available" encode-child crash. Insert parameter sets at every IDR so the stream is
		// self-contained and the metadata bsf can parse it.
		args = append(args, "-c:v", spec.Encoder, "-usage", "ultralowlatency",
			"-forced_idr", "1", "-header_insertion_mode", "idr")
		args = append(args, rc...)
	case "mjpeg":
		args = append(args, "-c:v", "mjpeg", "-q:v", "6", "-pix_fmt", "yuvj422p")
	default: // unknown encoder: pass through with the generic rc knobs
		args = append(args, "-c:v", spec.Encoder)
		args = append(args, rc...)
	}
	args = append(args, dev.sel...)
	// Output framing: parameter sets repeated on every keyframe (dump_extra) so a decoder can
	// (re)join mid-stream. Non-AMF also gets the {codec}_metadata filter for AUD insertion. AMF is
	// EXCLUDED from that filter: its encoder emits an imperfect elementary stream (parameter sets
	// only via -header_insertion_mode, no clean in-band VPS), and the metadata filter's strict CBS
	// parser hard-crashes the child on it ("VPS id 0 not available" → zero packets). dump_extra
	// alone just prepends extradata (no parse), so AMF frames flow. MJPEG is self-framing (SOI/EOI).
	amf := strings.HasSuffix(spec.Encoder, "_amf")
	switch spec.Codec {
	case medialink.CodecH264:
		if amf {
			args = append(args, "-bsf:v", "dump_extra=freq=keyframe", "-f", "h264")
		} else {
			args = append(args, "-bsf:v", "dump_extra=freq=keyframe,h264_metadata=aud=insert", "-f", "h264")
		}
	case medialink.CodecHEVC:
		if amf {
			args = append(args, "-bsf:v", "dump_extra=freq=keyframe", "-f", "hevc")
		} else {
			args = append(args, "-bsf:v", "dump_extra=freq=keyframe,hevc_metadata=aud=insert", "-f", "hevc")
		}
	case medialink.CodecJPEG:
		args = append(args, "-f", "mjpeg")
	}
	return append(args, "-flush_packets", "1", "-")
}

// decodeArgs builds the bitstream-stdin → raw-RGBA-stdout child argv. hwaccel "" = software.
// The explicit output size pins the rawvideo pipe framing (advert dims == coded dims → no-op).
func decodeArgs(spec medialink.DecodeSpec, hwaccel string) []string {
	// probesize 32 (the minimum) + analyzeduration 0: the format is forced (-f), so probing
	// must not stall waiting for pipe bytes - stream params come from the in-band SPS/PPS.
	args := []string{
		"-hide_banner", "-loglevel", "error",
		"-fflags", "nobuffer", "-flags", "low_delay",
		"-probesize", "32", "-analyzeduration", "0",
	}
	if hwaccel != "" {
		args = append(args, "-hwaccel", hwaccel)
	}
	switch spec.Codec {
	case medialink.CodecH264:
		args = append(args, "-f", "h264")
	case medialink.CodecHEVC:
		args = append(args, "-f", "hevc")
	case medialink.CodecJPEG:
		args = append(args, "-f", "mjpeg")
	}
	args = append(args, "-i", "-", "-an",
		"-pix_fmt", "rgba", "-s", fmt.Sprintf("%dx%d", spec.Width, spec.Height),
		"-f", "rawvideo", "-flush_packets", "1", "-")
	return args
}

// trimFloat renders fps without trailing zeros ("60", "29.97").
func trimFloat(f float64) string {
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}

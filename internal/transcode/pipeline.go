package transcode

import "fmt"

// Split decode→fx→encode pipeline halves for the vfx worker: ffmpeg decodes the
// trimmed+cropped clip to rawvideo RGBA on stdout, rave-mate-vfx filters it, a second
// ffmpeg consumes the filtered stream on stdin and encodes per preset (audio mapped
// from the source with the same trim).

// DecodeRawArgs builds the decode half: VF (crop) → scale to w×h, constant fps,
// RGBA rawvideo on stdout, audio dropped.
func (j Job) DecodeRawArgs(w, h int, fps float64) []string {
	return j.decodeRawArgs(w, h, fps, "", false)
}

// DecodeRawArgsPost is DecodeRawArgs with a post-scale filter suffix (e.g. the
// fit layout's background blur - after the downscale, where it is cheap).
func (j Job) DecodeRawArgsPost(w, h int, fps float64, post string) []string {
	return j.decodeRawArgs(w, h, fps, post, false)
}

// DecodeRawArgsRT is DecodeRawArgsPost paced at native frame rate (-re): the
// realtime preview pipeline stays ~1× realtime instead of racing through the
// whole remainder of the source at full speed.
func (j Job) DecodeRawArgsRT(w, h int, fps float64, post string) []string {
	return j.decodeRawArgs(w, h, fps, post, true)
}

func (j Job) decodeRawArgs(w, h int, fps float64, post string, realtime bool) []string {
	a := []string{"-hide_banner", "-nostats", "-y"}
	if realtime {
		a = append(a, "-re")
	}
	if j.TrimStart > 0 {
		a = append(a, "-ss", ftoa(j.TrimStart))
	}
	a = append(a, "-i", j.Input)
	if j.TrimEnd > j.TrimStart {
		a = append(a, "-t", ftoa(j.TrimEnd-j.TrimStart))
	}
	vf := fmt.Sprintf("scale=%d:%d", w, h)
	if j.VF != "" {
		vf = j.VF + "," + vf
	}
	if post != "" {
		vf += "," + post
	}
	return append(a, "-vf", vf, "-r", ftoa(fps), "-an", "-f", "rawvideo", "-pix_fmt", "rgba", "-")
}

// EncodeRawArgs builds the encode half: rawvideo RGBA w×h@fps on stdin (pipe:0) +
// audio from j.Input (same trim window), preset video/audio args (scaling and VF are
// decode-side, so the preset's geometry is suppressed here).
func (j Job) EncodeRawArgs(w, h int, fps float64) []string {
	return j.encodeRawArgs(w, h, fps, false)
}

// EncodeRawOverlayArgs is the fit-layout encode half: the piped raw stream is the
// styled BACKGROUND; the untouched source (input 1, same trim) is scaled to fit
// inside w×h and overlaid centered - the foreground never passes through the
// effect chain.
func (j Job) EncodeRawOverlayArgs(w, h int, fps float64) []string {
	return j.encodeRawArgs(w, h, fps, true)
}

// StreamMime is the MSE codec string EncodeStreamArgs produces (H.264 High@4.2 + AAC-LC).
const StreamMime = `video/mp4; codecs="avc1.64002a,mp4a.40.2"`

// EncodeStreamArgs is the realtime-preview encode half: piped raw video + audio
// from j.Input (same trim), x264 ultrafast/zerolatency + AAC into a GROWING
// MSE-appendable fragmented MP4 (empty_moov + default_base_moof, 0.5 s GOP
// fragments). overlay = fit layout (piped raw is the styled background).
func (j Job) EncodeStreamArgs(w, h int, fps float64, overlay bool) []string {
	a := []string{"-hide_banner", "-nostats", "-y",
		"-f", "rawvideo", "-pix_fmt", "rgba",
		"-video_size", fmt.Sprintf("%dx%d", w, h),
		"-framerate", ftoa(fps), "-i", "pipe:0"}
	if j.TrimStart > 0 {
		a = append(a, "-ss", ftoa(j.TrimStart))
	}
	a = append(a, "-i", j.Input)
	if j.TrimEnd > j.TrimStart {
		a = append(a, "-t", ftoa(j.TrimEnd-j.TrimStart))
	}
	if overlay {
		fc := fmt.Sprintf("[1:v]fps=%s,scale=%d:%d:force_original_aspect_ratio=decrease[fg];"+
			"[0:v][fg]overlay=(W-w)/2:(H-h)/2[vout]", ftoa(fps), w, h)
		a = append(a, "-filter_complex", fc, "-map", "[vout]", "-map", "1:a:0?")
	} else {
		a = append(a, "-map", "0:v:0", "-map", "1:a:0?")
	}
	g := int(fps/2 + 0.5)
	if g < 1 {
		g = 1
	}
	return append(a,
		"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency",
		"-profile:v", "high", "-level:v", "4.2", "-pix_fmt", "yuv420p",
		"-g", fmt.Sprintf("%d", g), "-crf", "26",
		"-c:a", "aac", "-b:a", "128k", "-ar", "48000",
		"-f", "mp4", "-movflags", "+frag_keyframe+empty_moov+default_base_moof",
		j.Output)
}

// StreamAudioMime is the MSE codec string EncodeAudioStreamArgs produces (AAC-LC, no video track).
const StreamAudioMime = `audio/mp4; codecs="mp4a.40.2"`

// EncodeAudioStreamArgs is the SURFACE-path clock leg: audio only, same trim window, same growing
// fragmented MP4 the /ms/ tail + __mst runtime already feed. The picture never reaches an encoder
// on that path (it goes decode → fx → shared texture), so this stream carries nothing but the
// clock the surface presents against. silent = the source has no audio track: mux a silent one
// instead, because a fragmented MP4 with zero streams is not a clock, it is a parse error.
func (j Job) EncodeAudioStreamArgs(silent bool) []string {
	a := []string{"-hide_banner", "-nostats", "-y", "-re"}
	if silent {
		a = append(a, "-f", "lavfi", "-i", "anullsrc=r=48000:cl=stereo")
		if d := j.TrimEnd - j.TrimStart; d > 0 {
			a = append(a, "-t", ftoa(d))
		}
	} else {
		if j.TrimStart > 0 {
			a = append(a, "-ss", ftoa(j.TrimStart))
		}
		a = append(a, "-i", j.Input)
		if j.TrimEnd > j.TrimStart {
			a = append(a, "-t", ftoa(j.TrimEnd-j.TrimStart))
		}
		a = append(a, "-map", "0:a:0")
	}
	// frag_duration, NOT frag_keyframe: with no video track there are no keyframes to cut on, so
	// frag_keyframe buffers the whole stream and the growing file stays a 28-byte ftyp until EOF.
	// Found by execution - the element sat at 0:01 forever waiting for an init segment.
	return append(a, "-vn", "-c:a", "aac", "-b:a", "128k", "-ar", "48000",
		"-f", "mp4", "-movflags", "+empty_moov+default_base_moof", "-frag_duration", "500000", j.Output)
}

// PresentRawArgs is the surface path's last video stage: the fx'd RGBA stream (cw×ch, the
// render-quality cap's size) on stdin, scaled to the surface's own rect (ow×oh) and - for the fit
// layout - the CLEAN source overlaid centred at full rect size, because the foreground must never
// pass through the effect chain. Same composition EncodeRawOverlayArgs does, minus the encoder.
//
// The scale is what keeps the PREVIEW QUALITY selector meaningful on a surface: the chain runs at
// its capped size (measured: 540p renders 1.85x faster than the 734p element rect, which is the
// difference between the picture keeping up with the audio clock and lagging it by 25 s), while the
// present stays a 1:1 copy of a picture that already matches the surface.
func (j Job) PresentRawArgs(cw, ch, ow, oh int, fps float64, fit bool) []string {
	a := []string{"-hide_banner", "-nostats", "-y",
		"-f", "rawvideo", "-pix_fmt", "rgba",
		"-video_size", fmt.Sprintf("%dx%d", cw, ch),
		"-framerate", ftoa(fps), "-i", "pipe:0"}
	if !fit {
		return append(a, "-vf", fmt.Sprintf("scale=%d:%d", ow, oh), "-an",
			"-f", "rawvideo", "-pix_fmt", "rgba", "pipe:1")
	}
	if j.TrimStart > 0 {
		a = append(a, "-ss", ftoa(j.TrimStart))
	}
	a = append(a, "-i", j.Input)
	if j.TrimEnd > j.TrimStart {
		a = append(a, "-t", ftoa(j.TrimEnd-j.TrimStart))
	}
	fc := fmt.Sprintf("[0:v]scale=%d:%d[bg];[1:v]fps=%s,scale=%d:%d:force_original_aspect_ratio=decrease[fg];"+
		"[bg][fg]overlay=(W-w)/2:(H-h)/2[vout]", ow, oh, ftoa(fps), ow, oh)
	return append(a, "-filter_complex", fc, "-map", "[vout]", "-an",
		"-f", "rawvideo", "-pix_fmt", "rgba", "pipe:1")
}

func (j Job) encodeRawArgs(w, h int, fps float64, overlay bool) []string {
	a := []string{"-hide_banner", "-nostats", "-y",
		"-f", "rawvideo", "-pix_fmt", "rgba",
		"-video_size", fmt.Sprintf("%dx%d", w, h),
		"-framerate", ftoa(fps), "-i", "pipe:0"}
	if j.TrimStart > 0 {
		a = append(a, "-ss", ftoa(j.TrimStart))
	}
	a = append(a, "-i", j.Input)
	if j.TrimEnd > j.TrimStart {
		a = append(a, "-t", ftoa(j.TrimEnd-j.TrimStart))
	}
	if overlay {
		fc := fmt.Sprintf("[1:v]fps=%s,scale=%d:%d:force_original_aspect_ratio=decrease[fg];"+
			"[0:v][fg]overlay=(W-w)/2:(H-h)/2[vout]", ftoa(fps), w, h)
		a = append(a, "-filter_complex", fc, "-map", "[vout]", "-map", "1:a:0?")
	} else {
		a = append(a, "-map", "0:v:0", "-map", "1:a:0?")
	}

	jv := j
	jv.VF = ""
	jv.Preset.Width, jv.Preset.Height, jv.Preset.Deinterlace = 0, 0, false
	a = append(a, jv.videoArgs()...)
	a = append(a, j.audioArgs()...)
	if c := j.Preset.Container; c == "mp4" || c == "m4a" || c == "aac" {
		a = append(a, "-movflags", "+faststart")
	}
	return append(a, j.Output)
}

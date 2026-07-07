// Package delaycomp computes and applies per-source delay compensation so every visual/audio
// source lands on the same beat. Reference clock = Link beat time; a source's measured latency
// has up to three components (control-plane RTT, render latency, manual trim). Sources are
// aligned to the SLOWEST (you can only add delay, never remove it), so every computed
// compensation is ≥0: delay the faster sources to match the slowest.
//
// Compensation is stored per source as mediasync.SourceConfig.StaticOffsetMs and pushed to OBS
// (audio → SetInputAudioSyncOffset, video → a delay filter via CreateSourceFilter +
// SetSourceFilterSettings) or Resolume. Measurement inputs: RTT probes (ProbeRTT, reusing
// medialink.OffsetEstimator's min-RTT filter), setalign beat-transient cross-correlation (true
// end-to-end render latency), and a manual nudge (ground-truth fallback).
package delaycomp

import (
	"context"
	"errors"
	"math"
	"time"

	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/obs"
)

// Latency is one source's measured latency components (ms). Total drives the compensation plan.
type Latency struct {
	RTTms    float64 `json:"rttMs"`    // control/network one-way delay (min-RTT/2)
	RenderMs float64 `json:"renderMs"` // measured render latency (setalign beat-transient align)
	ManualMs float64 `json:"manualMs"` // manual nudge (trim / ground-truth fallback; may be ±)
}

// Total is the source's end-to-end latency (ms). Negative-only manual trims can pull it below 0;
// callers clamp where a physical delay is required.
func (l Latency) Total() float64 { return l.RTTms + l.RenderMs + l.ManualMs }

// Plan computes per-source compensation (ms, rounded, ≥0) to align every source to the slowest
// (reference = max Total). comp[src] = maxTotal − Total(src). Empty input → empty plan.
func Plan(lat map[string]Latency) map[string]int {
	out := make(map[string]int, len(lat))
	if len(lat) == 0 {
		return out
	}
	maxTotal := math.Inf(-1)
	for _, l := range lat {
		if t := l.Total(); t > maxTotal {
			maxTotal = t
		}
	}
	for k, l := range lat {
		c := maxTotal - l.Total()
		if c < 0 {
			c = 0
		}
		out[k] = int(math.Round(c))
	}
	return out
}

// ── measurement ──

// ProbeRTT runs n round-trip probes via doOnce (each performs one control request and returns its
// RTT) and returns the min-filtered one-way delay in ms (Cristian: one-way ≈ min-RTT/2). Reuses
// medialink.OffsetEstimator's min-RTT + 2×-min-disqualification filter. n<1 → one probe.
func ProbeRTT(n int, doOnce func() (time.Duration, error)) (float64, error) {
	if n < 1 {
		n = 1
	}
	var est medialink.OffsetEstimator
	var lastErr error
	got := 0
	for i := 0; i < n; i++ {
		rtt, err := doOnce()
		if err != nil {
			lastErr = err
			continue
		}
		est.Add(0, rtt.Nanoseconds(), time.Now())
		got++
	}
	if got == 0 {
		if lastErr != nil {
			return 0, lastErr
		}
		return 0, errors.New("delaycomp: no successful RTT probes")
	}
	e, ok := est.Estimate(time.Now())
	if !ok {
		return 0, errors.New("delaycomp: no usable RTT sample")
	}
	return float64(e.RTTNs) / 2 / float64(time.Millisecond), nil
}

// ── OBS application ──

// DelayFilterName is the name of the delay filter rave-mate manages on a video source.
const DelayFilterName = "rave-mate delay"

// gpuDelayMaxMs is OBS's Render Delay (gpu_delay) ceiling; above it we use async_delay_filter.
const gpuDelayMaxMs = 500

// OBSController is the subset of the OBS client/proxy delaycomp drives.
type OBSController interface {
	SetInputAudioSyncOffset(ctx context.Context, input string, offsetMs int) error
	CreateSourceFilter(ctx context.Context, source, filter, kind string, settings map[string]any) error
	SetSourceFilterSettings(ctx context.Context, source, filter string, settings map[string]any, overlay bool) error
	GetSourceFilterList(ctx context.Context, source string) ([]obs.FilterInfo, error)
}

// VideoDelayKind picks the OBS delay filter kind for delayMs: gpu_delay (≤500ms, any source) or
// async_delay_filter (>500ms, ≤20s; not valid on Media Sources).
func VideoDelayKind(delayMs int) string {
	if delayMs <= gpuDelayMaxMs {
		return obs.FilterKindGPUDelay
	}
	return obs.FilterKindAsyncDelay
}

// ApplyOBSAudioOffset sets an input's audio sync offset (ms) - positive delays the audio.
func ApplyOBSAudioOffset(ctx context.Context, ctl OBSController, input string, offsetMs int) error {
	return ctl.SetInputAudioSyncOffset(ctx, input, offsetMs)
}

// ApplyOBSVideoDelay ensures the managed delay filter exists on source and sets it to delayMs.
// Idempotent: creates the filter once (of the kind matching delayMs), then updates its delay_ms.
// A pre-existing filter of a different kind (delay crossed the gpu/async boundary) is left in
// place and just updated - recreating it live would flicker the source mid-set.
func ApplyOBSVideoDelay(ctx context.Context, ctl OBSController, source string, delayMs int) error {
	if delayMs < 0 {
		delayMs = 0
	}
	exists := false
	if filters, err := ctl.GetSourceFilterList(ctx, source); err == nil {
		for _, f := range filters {
			if f.Name == DelayFilterName {
				exists = true
				break
			}
		}
	}
	settings := map[string]any{"delay_ms": delayMs}
	if !exists {
		if err := ctl.CreateSourceFilter(ctx, source, DelayFilterName, VideoDelayKind(delayMs), settings); err != nil {
			return err
		}
		return nil
	}
	return ctl.SetSourceFilterSettings(ctx, source, DelayFilterName, settings, true)
}

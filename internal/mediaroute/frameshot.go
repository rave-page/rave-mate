package mediaroute

import (
	"fmt"
	"image"
	"time"

	"rave.page/mate/internal/framedebug"
)

// Bounds on a frame shot. It runs where the live routes run, so a readback storm would compete with
// them for GPU bandwidth: the cost is capped here rather than trusted to the caller.
const (
	fsMaxGrabs      = 30
	fsMaxIntervalMs = 2000
	fsDefaultGrabs  = 8
	fsDefaultIntMs  = 250
)

// FrameShot is the verdict of sampling one sender's content, plus the last frame as a PNG.
type FrameShot struct {
	Sender  string   `json:"sender"`
	W       int      `json:"w,omitempty"`
	H       int      `json:"h,omitempty"`
	Grabs   int      `json:"grabs"`
	Changes int      `json:"changes"` // consecutive grabs whose content hash differed
	Hashes  []uint64 `json:"hashes"`  // per-grab, so WHICH grabs differed survives the wire
	PNG     []byte   `json:"png,omitempty"`
	Senders []string `json:"senders,omitempty"` // candidates, when the name was empty/unknown
	Err     string   `json:"err,omitempty"`
}

// Frozen reports whether the sampled content never changed across a usable number of grabs. Two
// grabs cannot distinguish a freeze from bad luck, so it demands at least three.
func (f FrameShot) Frozen() bool { return f.Grabs >= 3 && f.Changes == 0 }

// Verdict is the one-line human answer.
func (f FrameShot) Verdict() string {
	switch {
	case f.Err != "" && f.Grabs == 0:
		return "no reading: " + f.Err
	case f.Grabs == 0:
		return "no frames grabbed"
	case f.Frozen():
		return fmt.Sprintf("FROZEN at the source - %d grabs, 0 changed", f.Grabs)
	case f.Changes == 0:
		return fmt.Sprintf("inconclusive - only %d grab(s), none changed", f.Grabs)
	}
	return fmt.Sprintf("LIVE - %d of %d grabs changed", f.Changes, f.Grabs)
}

// FrameShot samples a local video-share sender's CURRENT content and reports whether it changed.
//
// This is the ORIGIN-side oracle, and it is the one that was missing. It reads the sender's own
// texture through GrabSenderFrame (which deliberately ignores Spout's IsFrameNew hint: senders
// written by our encoder child never bump the frame counter, and videoshare/frame.go documents that
// counter as unusable on this SDK pairing), so its verdict depends on NO encode, network, decode or
// republish stage. "8 grabs, 0 changed" is a dead sender, full stop - and every downstream counter
// can read perfectly healthy while that is true, which is exactly how a frozen 4K route cost hours.
func (m *Manager) FrameShot(sender string, n, intervalMs, scale int, crop [4]int) (FrameShot, error) {
	out := FrameShot{Sender: sender}
	if sender == "" {
		out.Senders = m.listSenders()
		out.Err = "no sender named"
		return out, nil
	}
	w, h, ok := m.senderSize(sender)
	if !ok || w <= 0 || h <= 0 {
		out.Senders = m.listSenders()
		out.Err = fmt.Sprintf("sender %q not found", sender)
		return out, nil
	}
	out.W, out.H = w, h

	if n <= 0 {
		n = fsDefaultGrabs
	}
	n = min(n, fsMaxGrabs)
	if intervalMs <= 0 {
		intervalMs = fsDefaultIntMs
	}
	intervalMs = min(intervalMs, fsMaxIntervalMs)

	rec := framedebug.For("src:" + sender)
	var last *image.NRGBA
	var prevHash uint64
	for i := range n {
		if i > 0 {
			time.Sleep(time.Duration(intervalMs) * time.Millisecond)
		}
		img, err := m.grabFrame(sender, w, h)
		if err != nil || img == nil {
			if out.Err == "" && err != nil {
				out.Err = err.Error() // keep going: a transient miss must not void the whole sample
			}
			continue
		}
		hash := framedebug.Hash(img.Pix)
		out.Hashes = append(out.Hashes, hash)
		if out.Grabs > 0 && hash != prevHash {
			out.Changes++
		}
		prevHash = hash
		out.Grabs++
		rec.Frame(img) // feeds the same stall clock the route panel reads
		last = img
	}
	if last != nil {
		png, err := framedebug.EncodePNG(last, scale, cropRect(crop))
		if err != nil {
			out.Err = err.Error()
		} else {
			out.PNG = png
		}
	}
	return out, nil
}

// cropRect turns the wire's x,y,w,h into a Rectangle. A zero-area crop means "whole frame".
func cropRect(c [4]int) image.Rectangle {
	if c[2] <= 0 || c[3] <= 0 {
		return image.Rectangle{}
	}
	return image.Rect(c[0], c[1], c[0]+c[2], c[1]+c[3])
}

// FrameShotBudget is the worst-case wall time a FrameShot can take, for callers that must set a
// timeout across a process boundary. A diagnostic that times out mid-sample answers nothing.
func FrameShotBudget() time.Duration {
	return time.Duration(fsMaxGrabs*fsMaxIntervalMs)*time.Millisecond + 15*time.Second
}

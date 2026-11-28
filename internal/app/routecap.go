package app

import (
	"fmt"
	"strconv"
	"strings"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/mediaroute"
)

// capArg is one parsed `--kbps|--fps N|none` flag: set=false = leave that axis unchanged.
type capArg struct {
	set bool
	v   int // 0 = none (clear)
}

// cutCapFlags strips `--kbps N|none` / `--fps N|none` (also `--kbps=N`) from a route-cap source arg.
func cutCapFlags(s string) (rest string, kbps, fps capArg, err error) {
	f := strings.Fields(s)
	keep := f[:0]
	parse := func(name, v string) (capArg, error) {
		if strings.EqualFold(v, "none") || v == "0" {
			return capArg{set: true}, nil
		}
		n, e := strconv.Atoi(v)
		if e != nil || n < 0 {
			return capArg{}, fmt.Errorf("--%s wants a positive number or none, got %q", name, v)
		}
		return capArg{set: true, v: n}, nil
	}
	for i := 0; i < len(f); i++ {
		name, val, hasEq := strings.Cut(strings.TrimPrefix(f[i], "--"), "=")
		if !strings.HasPrefix(f[i], "--") || (name != "kbps" && name != "fps") {
			keep = append(keep, f[i])
			continue
		}
		if !hasEq {
			if i+1 >= len(f) {
				return "", kbps, fps, fmt.Errorf("--%s needs a value", name)
			}
			i++
			val = f[i]
		}
		a, e := parse(name, val)
		if e != nil {
			return "", kbps, fps, e
		}
		if name == "kbps" {
			kbps = a
		} else {
			fps = a
		}
	}
	return strings.Join(keep, " "), kbps, fps, nil
}

// resolveRemembered resolves (peer token, source token) among the RECEIVED (remembered) sources:
// source matches an exact id, a case-insensitive name, else an unambiguous name substring.
func resolveRemembered(list []config.RememberedReceive, peerTok, srcTok string) (config.RememberedReceive, error) {
	cands := map[string]nodeCand{}
	for _, r := range list {
		cands[r.Peer] = nodeCand{ID: r.Peer, Name: r.Peer}
	}
	nc := make([]nodeCand, 0, len(cands))
	for _, v := range cands {
		nc = append(nc, v)
	}
	pid, ok := matchNodeID(nc, peerTok)
	if !ok {
		return config.RememberedReceive{}, fmt.Errorf("no received source from a peer matching %q (run `ctl routes`)", peerTok)
	}
	srcTok = strings.TrimSpace(srcTok)
	var hits []config.RememberedReceive
	for _, r := range list {
		if r.Peer == pid && (r.SourceID == srcTok || strings.EqualFold(r.Name, srcTok)) {
			hits = append(hits, r)
		}
	}
	if len(hits) == 0 {
		lt := strings.ToLower(srcTok)
		for _, r := range list {
			if r.Peer == pid && strings.Contains(strings.ToLower(r.Name), lt) {
				hits = append(hits, r)
			}
		}
	}
	switch {
	case len(hits) == 0:
		return config.RememberedReceive{}, fmt.Errorf("%s: no received source %q", peerShort(pid), srcTok)
	case len(hits) > 1:
		return config.RememberedReceive{}, fmt.Errorf("source %q is ambiguous on %s (%d matches) - use the exact source id", srcTok, peerShort(pid), len(hits))
	}
	return hits[0], nil
}

// fmtCap renders one cap axis for ctl ("none" for 0).
func fmtCap(v int, unit string) string {
	if v <= 0 {
		return "none"
	}
	return strconv.Itoa(v) + unit
}

// RouteCap shows or sets one received source's per-source caps (ctl route-cap). With no flags it
// prints the stored caps; each flag sets its axis (none = clear) and leaves the other unchanged.
func (c *appControl) RouteCap(peer, arg string) string {
	if c.mediaRoutes == nil || c.cfg == nil {
		return "media plane unavailable"
	}
	source, kbps, fps, err := cutCapFlags(arg)
	if err != nil {
		return "error: " + err.Error()
	}
	if source == "" {
		return "usage: route-cap <peer> <source> [--kbps N|none] [--fps N|none]"
	}
	r, err := resolveRemembered(c.cfg.Features.MediaLink.RememberedReceives, peer, source)
	if err != nil {
		return "error: " + err.Error()
	}
	label := fmt.Sprintf("%s %s", peerShort(r.Peer), r.SourceID)
	if !kbps.set && !fps.set {
		return fmt.Sprintf("caps %s: maxKbps=%s maxFps=%s", label, fmtCap(r.MaxKbps, ""), fmtCap(r.MaxFPS, ""))
	}
	k, f := r.MaxKbps, r.MaxFPS
	if kbps.set {
		k = kbps.v
	}
	if fps.set {
		f = fps.v
	}
	sc, ok := c.mediaRoutes.(mediaroute.SourceCapper)
	if !ok {
		return "error: per-source caps unsupported by this media plane"
	}
	if err := sc.SetSourceCaps(r.Peer, r.SourceID, k, f); err != nil {
		return "error: " + err.Error()
	}
	return fmt.Sprintf("caps %s: maxKbps=%s maxFps=%s (live on a running route within ~1 s; new offers carry them)",
		label, fmtCap(k, ""), fmtCap(f, ""))
}

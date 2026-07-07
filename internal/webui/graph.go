package webui

import (
	"fmt"
	"math"
	"strings"
)

// sparkSeries is one trace for an SVG sparkline (vals oldest→newest; NaN = gap; fill = area under).
type sparkSeries struct {
	vals  []float64
	color string
	fill  bool
}

// Brand trace colours (match the Fyne graphs).
const (
	sparkMint   = "#08F79B"
	sparkHot    = "#FF3E8A"
	sparkViolet = "#7C3AED"
	sparkAmber  = "#FFB547"
	sparkInfo   = "#A78BFA"
	sparkMuted  = "rgba(255,255,255,.35)"
)

// sparklineSVG renders multi-series traces as an inline SVG (autoscaled to the hottest sample; a
// flat-zero stays on the floor). Go generates the whole graph - the webview only paints it.
func sparklineSVG(series []sparkSeries, w, h int) string {
	maxV := 1.0
	for _, s := range series {
		for _, v := range s.vals {
			if !math.IsNaN(v) && v > maxV {
				maxV = v
			}
		}
	}
	usable := float64(h) * 0.9
	var b strings.Builder
	fmt.Fprintf(&b, `<svg viewBox="0 0 %d %d" preserveAspectRatio="none" class="spark">`, w, h)
	// attribute values before /> MUST be quoted: an unquoted value eats the "/" (class becomes
	// "spark-grid/", no self-close), the unclosed <line> swallows every following polyline/polygon
	// as children, and SVG never paints graphics elements nested in a <line> → blank graph.
	fmt.Fprintf(&b, `<line x1="0" y1="%d" x2="%d" y2="%d" class="spark-grid"/><line x1="0" y1="%d" x2="%d" y2="%d" class="spark-grid"/>`,
		h/3, w, h/3, 2*h/3, w, 2*h/3)
	for _, s := range series {
		n := len(s.vals)
		if n < 2 {
			continue
		}
		xat := func(i int) float64 { return float64(w) * float64(i) / float64(n-1) }
		yat := func(v float64) float64 {
			y := float64(h) - 1 - (v / maxV * usable)
			if y < 0 {
				y = 0
			}
			return y
		}
		// split into segments on NaN gaps
		var segs [][]string
		var cur []string
		for i, v := range s.vals {
			if math.IsNaN(v) {
				if len(cur) > 0 {
					segs = append(segs, cur)
					cur = nil
				}
				continue
			}
			cur = append(cur, fmt.Sprintf("%.1f,%.1f", xat(i), yat(v)))
		}
		if len(cur) > 0 {
			segs = append(segs, cur)
		}
		if s.fill {
			for _, seg := range segs {
				if len(seg) < 2 {
					continue
				}
				x0 := strings.SplitN(seg[0], ",", 2)[0]
				x1 := strings.SplitN(seg[len(seg)-1], ",", 2)[0]
				fmt.Fprintf(&b, `<polygon points="%s,%d %s %s,%d" fill="%s" fill-opacity="0.16" stroke="none"/>`,
					x0, h, strings.Join(seg, " "), x1, h, s.color)
			}
		}
		for _, seg := range segs {
			fmt.Fprintf(&b, `<polyline points="%s" fill="none" stroke="%s" stroke-width="1.5"/>`, strings.Join(seg, " "), s.color)
		}
	}
	b.WriteString(`</svg>`)
	return b.String()
}

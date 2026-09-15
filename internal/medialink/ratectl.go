package medialink

// RateControlSource is an optional Source extension (the §2.5 sibling of KeyframeSource): the route
// caps the encoder's output when the receiver sends a MetaRate hint - the DJ-PC VRAM governor's
// backpressure. Encoder-backed video sources implement it; a source without it ignores rate hints,
// so an older sender degrades cleanly. Bitrate is a live lever on every encoder; height/fps are
// best-effort.
type RateControlSource interface {
	Source
	SetRateHint(RateHint)
}

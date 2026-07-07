package traktormap

import (
	"context"
	_ "embed"
)

// ravePageDEVI is the pre-extracted "Generic MIDI" device frame of the rave-mate-authored
// RavePage controller mapping (4 decks x EQ high/mid/low + fader/filter/play/cue sent over a
// Generic MIDI device that session/sources/midisrc decodes into deck/mixer state). Shipped (we
// author it), so unlike Denon there is no download.
//
//go:embed assets/RavePage.devi
var ravePageDEVI []byte

// ravePageDevice is the DEVI name Traktor stores. Traktor names generic-MIDI mappings
// "Generic MIDI"; activate/deactivate match by this name, so a user who already runs their own
// unrelated Generic MIDI device should review before deactivating.
const ravePageDevice = "Generic MIDI"

// RavePage is the one-click rave-mate EQ/fader MIDI-out mapping.
var RavePage = Mapping{
	Key:     "ravepage",
	Display: "RavePage EQ/fader out (Generic MIDI)",
	Device:  ravePageDevice,
	Fetch:   func(context.Context) ([]byte, error) { return ravePageDEVI, nil },
}

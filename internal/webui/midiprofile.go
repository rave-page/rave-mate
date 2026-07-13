package webui

// Per-device UI-mapping profile mutations (copy/clear), pure over config so they unit-test
// without a UI. A profile is derived from each bind's captured port via
// config.MIDIFeature.BindProfileKey - see render_midictl_uimap.go for the rendering side.

import (
	"rave.page/mate/internal/config"
	"rave.page/mate/internal/vrbind"
)

// umProfilePort returns the MIDIKey.Port value a bind stored under profile key gets:
// the any-device profile stores "", every other profile stores its key (a controller Port
// substring or a raw orphan port - both match the device at dispatch).
func umProfilePort(key string) string {
	if key == config.BindProfileAny {
		return ""
	}
	return key
}

// umCopyProfile duplicates every ui.* bind of the src profile onto dst, remapping only the
// port (mode/sensitivity/reverse/target survive). Binds the dst profile already holds (same
// action+target+status+data1+mode) are skipped. Returns the number of binds added.
func umCopyProfile(m config.MIDIFeature, f *config.VROverlayFeature, srcKey, dstKey string) int {
	if srcKey == dstKey {
		return 0
	}
	dstPort := umProfilePort(dstKey)
	exists := func(nb vrbind.Bind) bool {
		for _, b := range f.Binds {
			if b.MIDI != nil && b.Action == nb.Action && b.Target == nb.Target &&
				b.MIDI.Status == nb.MIDI.Status && b.MIDI.Data1 == nb.MIDI.Data1 &&
				b.MIDI.Mode == nb.MIDI.Mode && m.BindProfileKey(b.MIDI.Port) == dstKey {
				return true
			}
		}
		return false
	}
	added := 0
	src := f.Binds // fixed-length view: appends below must not extend the iteration
	for i := range src {
		b := src[i]
		if !umIsUIBind(b) || m.BindProfileKey(b.MIDI.Port) != srcKey {
			continue
		}
		k := *b.MIDI
		k.Port = dstPort
		nb := vrbind.Bind{Action: b.Action, Target: b.Target, MIDI: &k}
		if exists(nb) {
			continue
		}
		f.Binds = append(f.Binds, nb)
		added++
	}
	return added
}

// umClearProfile removes every ui.* bind owned by the profile. Returns the number removed.
func umClearProfile(m config.MIDIFeature, f *config.VROverlayFeature, key string) int {
	kept := f.Binds[:0]
	removed := 0
	for _, b := range f.Binds {
		if umIsUIBind(b) && m.BindProfileKey(b.MIDI.Port) == key {
			removed++
			continue
		}
		kept = append(kept, b)
	}
	f.Binds = kept
	return removed
}

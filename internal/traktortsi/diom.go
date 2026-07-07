package traktortsi

import "errors"

// Device is one controller mapping inside the DIOM blob.
type Device struct {
	Name    string // DEVI name, e.g. "Traktor.Kontrol S4 MK3" or "Generic MIDI"
	Comment string // DDIC - the human mapping name shown in Controller Manager
	InPort  string // DDPT in-port (MIDI device rave-mate/Traktor reads)
	OutPort string // DDPT out-port (MIDI device Traktor writes state to)
	raw     []byte // the complete DEVI frame (tag+len+payload), for add/remove fidelity
}

// ParseDevices lists the controller mappings in a DIOM blob (starts with the "DIOM" tag).
func ParseDevices(blob []byte) ([]Device, error) {
	devs, err := devsPayload(blob)
	if err != nil {
		return nil, err
	}
	deviFrames, err := readFrames(devs[4:]) // skip the uint32 device count
	if err != nil {
		return nil, err
	}
	var out []Device
	for _, df := range deviFrames {
		if df.tag != "DEVI" {
			continue
		}
		d := Device{raw: reframe("DEVI", df.payload)}
		name, rest, err := readString(df.payload)
		if err != nil {
			return nil, err
		}
		d.Name = name
		if inner, err := readFrames(rest); err == nil {
			if ddat, ok := find(inner, "DDAT"); ok {
				if parts, err := readFrames(ddat.payload); err == nil {
					if ddic, ok := find(parts, "DDIC"); ok {
						d.Comment, _, _ = readString(ddic.payload)
					}
					if ddpt, ok := find(parts, "DDPT"); ok {
						ip, r, e := readString(ddpt.payload)
						if e == nil {
							d.InPort = ip
							d.OutPort, _, _ = readString(r)
						}
					}
				}
			}
		}
		out = append(out, d)
	}
	return out, nil
}

// devsPayload returns the DEVS frame payload (count + DEVI frames) from a DIOM blob.
func devsPayload(blob []byte) ([]byte, error) {
	roots, err := readFrames(blob)
	if err != nil {
		return nil, err
	}
	diom, ok := find(roots, "DIOM")
	if !ok {
		return nil, errors.New("traktortsi: no DIOM root")
	}
	inner, err := readFrames(diom.payload)
	if err != nil {
		return nil, err
	}
	devs, ok := find(inner, "DEVS")
	if !ok {
		return nil, errors.New("traktortsi: no DEVS container")
	}
	if len(devs.payload) < 4 {
		return nil, errTrunc
	}
	return devs.payload, nil
}

// reframe rebuilds a full TAG+LEN+payload frame (used to retain a DEVI verbatim).
func reframe(tag string, payload []byte) []byte {
	return putFrame(make([]byte, 0, len(payload)+8), tag, payload)
}

package traktortsi

import "encoding/binary"

// DeviceNames lists the controller-mapping names in a DIOM blob.
func DeviceNames(blob []byte) ([]string, error) {
	devs, err := ParseDevices(blob)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(devs))
	for i, d := range devs {
		names[i] = d.Name
	}
	return names, nil
}

// HasDevice reports whether a mapping with the given name is installed.
func HasDevice(blob []byte, name string) (bool, error) {
	names, err := DeviceNames(blob)
	if err != nil {
		return false, err
	}
	for _, n := range names {
		if n == name {
			return true, nil
		}
	}
	return false, nil
}

// MakeDevice builds a minimal DEVI frame (name + comment + in/out ports, no mappings) - a
// skeleton controller device. Used to seed the RavePage map (out-mappings added on top) and
// by tests.
func MakeDevice(name, comment, inPort, outPort string) []byte {
	ddpt := putString(putString(nil, inPort), outPort)
	ddat := putFrame(putFrame(nil, "DDIC", putString(nil, comment)), "DDPT", ddpt)
	payload := putFrame(putString(nil, name), "DDAT", ddat)
	return putFrame(nil, "DEVI", payload)
}

// NewDIOM builds a DIOM controller blob containing the given full DEVI frames.
func NewDIOM(devis ...[]byte) []byte {
	devsPayload := binary.BigEndian.AppendUint32(nil, uint32(len(devis)))
	for _, d := range devis {
		devsPayload = append(devsPayload, d...)
	}
	inner := putFrame(putFrame(nil, "DIOI", []byte{0, 0, 0, 1}), "DEVS", devsPayload)
	return putFrame(nil, "DIOM", inner)
}

// DeviceRaw returns the complete DEVI frame bytes for the named mapping - used to extract a
// template from a downloaded controller .tsi. ok=false if the device isn't present.
func DeviceRaw(blob []byte, name string) ([]byte, bool, error) {
	devs, err := ParseDevices(blob)
	if err != nil {
		return nil, false, err
	}
	for _, d := range devs {
		if d.Name == name {
			return d.raw, true, nil
		}
	}
	return nil, false, nil
}

// AddDevice appends a complete DEVI frame to the DEVS container (rebuilding the parent
// lengths + child count). deviRaw is a full "DEVI"+len+payload frame. A device with the same
// name is replaced (so install is idempotent).
func AddDevice(blob, deviRaw []byte) ([]byte, error) {
	name, err := deviName(deviRaw)
	if err != nil {
		return nil, err
	}
	devs, err := ParseDevices(blob)
	if err != nil {
		return nil, err
	}
	var devis [][]byte
	for _, d := range devs {
		if d.Name != name { // drop an existing same-named mapping (replace)
			devis = append(devis, d.raw)
		}
	}
	devis = append(devis, deviRaw)
	return writeDevices(blob, devis)
}

// RemoveDevice deletes the named mapping from the DEVS container. A no-op (returns the blob
// unchanged) if it isn't present.
func RemoveDevice(blob []byte, name string) ([]byte, error) {
	devs, err := ParseDevices(blob)
	if err != nil {
		return nil, err
	}
	var devis [][]byte
	removed := false
	for _, d := range devs {
		if d.Name == name {
			removed = true
			continue
		}
		devis = append(devis, d.raw)
	}
	if !removed {
		return blob, nil
	}
	return writeDevices(blob, devis)
}

// writeDevices rebuilds the blob with a replacement DEVI list, preserving any other top-level
// DIOM frames (e.g. DIOI) and fixing every length + the DEVS count.
func writeDevices(blob []byte, devis [][]byte) ([]byte, error) {
	roots, err := readFrames(blob)
	if err != nil {
		return nil, err
	}
	diom, ok := find(roots, "DIOM")
	if !ok {
		return nil, errTrunc
	}
	inner, err := readFrames(diom.payload)
	if err != nil {
		return nil, err
	}
	devsPayload := binary.BigEndian.AppendUint32(nil, uint32(len(devis)))
	for _, d := range devis {
		devsPayload = append(devsPayload, d...) // each d is a full DEVI frame
	}
	var dp []byte
	for _, f := range inner {
		if f.tag == "DEVS" {
			dp = putFrame(dp, "DEVS", devsPayload)
		} else {
			dp = putFrame(dp, f.tag, f.payload)
		}
	}
	return putFrame(nil, "DIOM", dp), nil
}

// deviName extracts the name string from a full DEVI frame.
func deviName(deviRaw []byte) (string, error) {
	fs, err := readFrames(deviRaw)
	if err != nil || len(fs) == 0 || fs[0].tag != "DEVI" {
		return "", errTrunc
	}
	name, _, err := readString(fs[0].payload)
	return name, err
}

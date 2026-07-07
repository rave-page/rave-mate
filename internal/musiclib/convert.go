package musiclib

import (
	"fmt"
	"io"
)

// Format identifies a DJ-library interchange format for Import/Export dispatch.
type Format string

const (
	FormatTraktor   Format = "traktor"   // collection.nml
	FormatRekordbox Format = "rekordbox" // exported DJ_PLAYLISTS XML
	FormatVirtualDJ Format = "virtualdj" // database.xml
	FormatM3U       Format = "m3u"       // playlist (export only)
	FormatCSV       Format = "csv"       // metadata backup (export only)
)

// Import reads a library in the given format into the normalized model. The conversion
// pipeline is import(A) → Library → Export(B); this is the read half.
func Import(format Format, r io.Reader) (Library, error) {
	lib := Library{Source: Source{App: string(format)}}
	collect := func(t Track) { lib.Tracks = append(lib.Tracks, t) }
	var err error
	switch format {
	case FormatTraktor:
		_, err = ParseCollection(r, collect)
	case FormatRekordbox:
		lib.Tracks, lib.Playlists, err = ParseRekordboxLibrary(r)
		return lib, err
	case FormatVirtualDJ:
		_, err = ParseVirtualDJ(r, collect)
	default:
		return lib, fmt.Errorf("import: unsupported format %q", format)
	}
	return lib, err
}

// Export writes lib to w in the given format (the write half of the conversion pipeline).
func Export(format Format, lib Library, w io.Writer) error {
	switch format {
	case FormatTraktor:
		return ExportTraktorNML(lib, w)
	case FormatRekordbox:
		return ExportRekordboxXML(lib.Tracks, w)
	case FormatVirtualDJ:
		return ExportVirtualDJ(lib.Tracks, w)
	case FormatM3U:
		return ExportM3U(lib.Tracks, w)
	case FormatCSV:
		return ExportCSV(lib.Tracks, w)
	default:
		return fmt.Errorf("export: unsupported format %q", format)
	}
}

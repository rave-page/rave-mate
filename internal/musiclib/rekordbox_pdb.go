package musiclib

import (
	"encoding/binary"
	"fmt"
	"path/filepath"
	"sort"
	"unicode/utf16"
)

// Rekordbox device export (export.pdb) reader - the DeviceSQL store Rekordbox writes to USB
// (PIONEER/rekordbox/export.pdb). Pure-Go, zero deps. Format per Deep-Symmetry/crate-digger
// (rekordbox_pdb.ksy). Gives library tracks + playlists + history sets (play ORDER; the .pdb
// carries no per-track timestamps). All local - the user's own exported library.

// PDB page-type table ids we read.
const (
	pdbTracks           = 0
	pdbGenres           = 1
	pdbArtists          = 2
	pdbAlbums           = 3
	pdbLabels           = 4
	pdbKeys             = 5
	pdbPlaylistTree     = 7
	pdbPlaylistEntries  = 8
	pdbHistoryPlaylists = 11
	pdbHistoryEntries   = 12
)

const pdbHeapStart = 0x28 // page-relative offset where the row heap begins

type pdbTable struct{ first, last uint32 }

type pdbReader struct {
	d       []byte
	lenPage uint32
	tables  map[int]pdbTable
}

// ParseRekordboxPDB parses an export.pdb byte image into the normalized model. deviceRoot is
// the mount root of the USB (e.g. "E:\\") used to absolutize the device-relative file paths;
// pass "" to keep paths as stored.
func ParseRekordboxPDB(data []byte, deviceRoot string) (Library, error) {
	if len(data) < 0x1C {
		return Library{}, fmt.Errorf("pdb: too small")
	}
	r := &pdbReader{d: data, tables: map[int]pdbTable{}}
	r.lenPage = binary.LittleEndian.Uint32(data[0x04:])
	if r.lenPage < pdbHeapStart || r.lenPage > 1<<20 {
		return Library{}, fmt.Errorf("pdb: bad page size %d", r.lenPage)
	}
	numTables := binary.LittleEndian.Uint32(data[0x08:])
	if numTables > 64 {
		numTables = 64
	}
	for i := uint32(0); i < numTables; i++ {
		off := 0x1C + int(i)*16
		if off+16 > len(data) {
			break
		}
		typ := int(binary.LittleEndian.Uint32(data[off:]))
		first := binary.LittleEndian.Uint32(data[off+8:])
		last := binary.LittleEndian.Uint32(data[off+12:])
		if _, dup := r.tables[typ]; !dup {
			r.tables[typ] = pdbTable{first: first, last: last}
		}
	}

	// Name lookups first (tracks reference them by id).
	artists := r.nameTable(pdbArtists, artistName)
	albums := r.nameTable(pdbAlbums, albumName)
	genres := r.nameTable(pdbGenres, fixedName(4)) // id u4, name at +4
	labels := r.nameTable(pdbLabels, fixedName(4)) // id u4, name at +4
	keys := r.nameTable(pdbKeys, fixedName(8))     // id u4, id2 u4, name at +8

	var lib Library
	lib.Source = Source{App: "rekordbox"}
	trackPath := map[uint32]string{} // track id → resolved path (for playlists/history)
	r.eachRow(pdbTracks, func(o int) {
		t, id, ok := r.track(o, deviceRoot, artists, albums, genres, labels, keys)
		if !ok {
			return
		}
		lib.Tracks = append(lib.Tracks, t)
		trackPath[id] = t.Path
	})

	lib.Playlists = r.playlists(trackPath)
	lib.Sessions = r.histories(trackPath)
	return lib, nil
}

// u16/u32/u8 are bounds-checked little-endian reads (0 on OOB).
func (r *pdbReader) u16(o int) uint16 {
	if o < 0 || o+2 > len(r.d) {
		return 0
	}
	return binary.LittleEndian.Uint16(r.d[o:])
}

func (r *pdbReader) u32(o int) uint32 {
	if o < 0 || o+4 > len(r.d) {
		return 0
	}
	return binary.LittleEndian.Uint32(r.d[o:])
}

func (r *pdbReader) u8(o int) byte {
	if o < 0 || o >= len(r.d) {
		return 0
	}
	return r.d[o]
}

// eachRow walks the page chain of the given table type and invokes fn with the absolute file
// offset of every present row. Guards against cycles + out-of-range page links.
func (r *pdbReader) eachRow(tableType int, fn func(rowOff int)) {
	tbl, ok := r.tables[tableType]
	if !ok {
		return
	}
	seen := map[uint32]bool{}
	page := tbl.first
	for {
		if seen[page] {
			break
		}
		seen[page] = true
		base := int(page) * int(r.lenPage)
		if base < 0 || base+pdbHeapStart > len(r.d) {
			break
		}
		pageType := int(r.u32(base + 0x08))
		flags := r.u8(base + 0x1B)
		isData := flags&0x40 == 0
		if pageType == tableType && isData {
			r.eachRowInPage(base, fn)
		}
		if page == tbl.last {
			break
		}
		next := r.u32(base + 0x0C)
		if next == page || int(next)*int(r.lenPage) >= len(r.d) {
			break
		}
		page = next
	}
}

// eachRowInPage emits the present rows of one data page. The row index is built backwards from
// the page end in groups of 16: group g starts at len_page-g*0x24; present_flags sit at base-4,
// row offset i at base-(6+2i); the row body lives at heap_start+ofs_row.
func (r *pdbReader) eachRowInPage(pageBase int, fn func(rowOff int)) {
	flags := r.u8(pageBase + 0x1B)
	if flags&0x40 != 0 {
		return
	}
	hdr := uint32(r.u8(pageBase+0x18))<<16 | uint32(r.u8(pageBase+0x19))<<8 | uint32(r.u8(pageBase+0x1A))
	numRowOffsets := int(hdr >> 11) // top 13 bits = offsets ever allocated
	if numRowOffsets <= 0 {
		return
	}
	numGroups := (numRowOffsets-1)/16 + 1
	for g := 0; g < numGroups; g++ {
		groupBase := int(r.lenPage) - g*0x24
		present := r.u16(pageBase + groupBase - 4)
		for i := 0; i < 16; i++ {
			if present&(1<<uint(i)) == 0 {
				continue
			}
			ofs := int(r.u16(pageBase + groupBase - (6 + 2*i)))
			rowOff := pageBase + pdbHeapStart + ofs
			if rowOff < pageBase || rowOff >= pageBase+int(r.lenPage) {
				continue
			}
			fn(rowOff)
		}
	}
}

// devStr decodes a DeviceSQL string at absolute offset o. Short ASCII: odd flag byte, length
// = flag>>1 (incl. flag), text = length-1 bytes. Long ASCII (0x40) / UTF-16LE (0x90): u2 length
// (incl. 4-byte header) + 1 pad byte, then text.
func (r *pdbReader) devStr(o int) string {
	if o < 0 || o >= len(r.d) {
		return ""
	}
	kind := r.d[o]
	switch kind {
	case 0x40, 0x90:
		length := int(r.u16(o + 1))
		n := length - 4
		start := o + 4
		if n <= 0 || start+n > len(r.d) {
			return ""
		}
		if kind == 0x40 {
			return decodeLatin1(r.d[start : start+n])
		}
		u := make([]uint16, n/2)
		for i := range u {
			u[i] = binary.LittleEndian.Uint16(r.d[start+2*i:])
		}
		return string(utf16.Decode(u))
	default:
		if kind&1 == 0 {
			return "" // even non-flag byte → not a short string
		}
		n := int(kind>>1) - 1
		start := o + 1
		if n <= 0 || start+n > len(r.d) {
			return ""
		}
		return decodeLatin1(r.d[start : start+n])
	}
}

// decodeLatin1 maps bytes 1:1 to runes (DeviceSQL "ASCII" is really latin-1).
func decodeLatin1(b []byte) string {
	rs := make([]rune, len(b))
	for i, c := range b {
		rs[i] = rune(c)
	}
	return string(rs)
}

// nameTable builds an id→name map from a simple name table using the given row decoder.
func (r *pdbReader) nameTable(tableType int, dec func(r *pdbReader, o int) (uint32, string)) map[uint32]string {
	m := map[uint32]string{}
	r.eachRow(tableType, func(o int) {
		id, name := dec(r, o)
		if id != 0 && name != "" {
			m[id] = name
		}
	})
	return m
}

// fixedName decodes id u4 at row start + a device string at a fixed offset (genres/labels/keys).
func fixedName(nameOff int) func(r *pdbReader, o int) (uint32, string) {
	return func(r *pdbReader, o int) (uint32, string) {
		return r.u32(o), r.devStr(o + nameOff)
	}
}

// artistName decodes an artist row: subtype u2 picks near (u1 @0x09) vs far (u2 @0x0a) name offset.
func artistName(r *pdbReader, o int) (uint32, string) {
	id := r.u32(o + 0x04)
	sub := r.u16(o)
	var nameOfs int
	if sub&0x04 == 0x04 {
		nameOfs = int(r.u16(o + 0x0a))
	} else {
		nameOfs = int(r.u8(o + 0x09))
	}
	return id, r.devStr(o + nameOfs)
}

// albumName decodes an album row: subtype u2 picks near (u1 @0x15) vs far (u2 @0x16) name offset.
func albumName(r *pdbReader, o int) (uint32, string) {
	id := r.u32(o + 0x0C)
	sub := r.u16(o)
	var nameOfs int
	if sub&0x04 == 0x04 {
		nameOfs = int(r.u16(o + 0x16))
	} else {
		nameOfs = int(r.u8(o + 0x15))
	}
	return id, r.devStr(o + nameOfs)
}

// track decodes a track row into a Track + its id. FK ids resolve via the name maps.
func (r *pdbReader) track(o int, deviceRoot string, artists, albums, genres, labels, keys map[uint32]string) (Track, uint32, bool) {
	id := r.u32(o + 0x48)
	if id == 0 {
		return Track{}, 0, false
	}
	str := func(i int) string { return r.devStr(o + int(r.u16(o+0x5E+2*i))) }
	path := str(20) // file_path (device-relative)
	full := path
	if deviceRoot != "" && path != "" {
		full = filepath.Join(deviceRoot, filepath.FromSlash(path))
	}
	t := Track{
		Path:        full,
		Title:       str(17),
		Artist:      artists[r.u32(o+0x44)],
		Album:       albums[r.u32(o+0x40)],
		Genre:       genres[r.u32(o+0x3C)],
		Label:       labels[r.u32(o+0x28)],
		Key:         keys[r.u32(o+0x20)],
		Comment:     str(16),
		BPM:         float64(r.u32(o+0x38)) / 100,
		DurationSec: float64(r.u16(o + 0x54)),
		BitrateBps:  int(r.u32(o+0x30)) * 1000,
		FileSizeKB:  int(r.u32(o+0x10) / 1024),
		PlayCount:   int(r.u16(o + 0x4E)),
		Rating:      int(r.u8(o + 0x59)),
		ImportDate:  str(10), // date_added
		ReleaseDate: str(11),
	}
	if t.Path == "" && t.Title == "" {
		return Track{}, 0, false
	}
	return t, id, true
}

// playlists builds flat playlists (with folder paths) from the tree + entry tables.
func (r *pdbReader) playlists(trackPath map[uint32]string) []Playlist {
	type node struct {
		parent   uint32
		name     string
		isFolder bool
	}
	nodes := map[uint32]node{}
	r.eachRow(pdbPlaylistTree, func(o int) {
		id := r.u32(o + 0x0C)
		if id == 0 {
			return
		}
		nodes[id] = node{
			parent:   r.u32(o + 0x00),
			name:     r.devStr(o + 0x14),
			isFolder: r.u32(o+0x10) != 0,
		}
	})
	// entries grouped by playlist id, ordered by entry_index.
	type pe struct {
		idx     uint32
		trackID uint32
	}
	ent := map[uint32][]pe{}
	r.eachRow(pdbPlaylistEntries, func(o int) {
		pid := r.u32(o + 0x08)
		ent[pid] = append(ent[pid], pe{idx: r.u32(o + 0x00), trackID: r.u32(o + 0x04)})
	})
	folderPath := func(id uint32) string {
		var segs []string
		for p := nodes[id].parent; p != 0; {
			n, ok := nodes[p]
			if !ok {
				break
			}
			segs = append([]string{n.name}, segs...)
			p = n.parent
		}
		return joinFolder(segs)
	}
	var out []Playlist
	// deterministic order by playlist id
	ids := make([]uint32, 0, len(nodes))
	for id := range nodes {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		n := nodes[id]
		if n.isFolder {
			continue
		}
		es := ent[id]
		sort.Slice(es, func(i, j int) bool { return es[i].idx < es[j].idx })
		paths := make([]string, 0, len(es))
		for _, e := range es {
			if p := trackPath[e.trackID]; p != "" {
				paths = append(paths, p)
			}
		}
		out = append(out, Playlist{Name: n.name, Folder: folderPath(id), Paths: paths})
	}
	return out
}

// histories builds a Session per history playlist (play order; no per-track timestamps in PDB).
func (r *pdbReader) histories(trackPath map[uint32]string) []Session {
	names := map[uint32]string{}
	r.eachRow(pdbHistoryPlaylists, func(o int) {
		names[r.u32(o)] = r.devStr(o + 4)
	})
	type he struct {
		idx     uint32
		trackID uint32
	}
	ent := map[uint32][]he{}
	r.eachRow(pdbHistoryEntries, func(o int) {
		pid := r.u32(o + 0x04)
		ent[pid] = append(ent[pid], he{idx: r.u32(o + 0x08), trackID: r.u32(o + 0x00)})
	})
	ids := make([]uint32, 0, len(names))
	for id := range names {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var out []Session
	for _, id := range ids {
		es := ent[id]
		sort.Slice(es, func(i, j int) bool { return es[i].idx < es[j].idx })
		played := make([]PlayedTrack, 0, len(es))
		for _, e := range es {
			if p := trackPath[e.trackID]; p != "" {
				played = append(played, PlayedTrack{Path: p})
			}
		}
		if len(played) > 0 {
			out = append(out, Session{Name: names[id], Played: played})
		}
	}
	return out
}

func joinFolder(segs []string) string {
	out := ""
	for _, s := range segs {
		if s == "" {
			continue
		}
		if out == "" {
			out = s
		} else {
			out += "/" + s
		}
	}
	return out
}

// DiscoverRekordboxPDB scans likely mount points for PIONEER/rekordbox/export.pdb and returns
// (deviceRoot, pdbPath) pairs. Windows: drive letters; Unix: /Volumes, /media/<user>, /mnt.
func DiscoverRekordboxPDB() (roots, pdbs []string) {
	for _, root := range pdbMountRoots() {
		p := filepath.Join(root, "PIONEER", "rekordbox", "export.pdb")
		if fileExists(p) {
			roots = append(roots, root)
			pdbs = append(pdbs, p)
		}
	}
	return roots, pdbs
}

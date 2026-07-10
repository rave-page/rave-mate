package musiclib

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"strconv"
)

// NML playlist write-back for the beatgrid fixer's manual-prep list: find-or-create a playlist
// NODE and union/prune track keys, porting fix_grids.upsert_playlist/prune_playlist (lines
// 158-204). Keys use Traktor's PRIMARYKEY format = VOLUME+DIR+FILE of the COLLECTION ENTRY
// LOCATION (fix_grids.entry_key, lines 37-40), so they're derived from the collection - a path
// with no COLLECTION ENTRY is skipped. Streaming + atomic temp+rename via rewriteNMLFile;
// unknown playlists/folders pass through untouched.

// nmlPlaylistScan is the pass-1 snapshot needed to rewrite one playlist.
type nmlPlaylistScan struct {
	keyByPath    map[string]string // resolved COLLECTION path → raw PRIMARYKEY
	exists       bool              // target playlist NODE present
	existingKeys map[string]bool   // raw keys already in the target playlist
	entryCount   int               // ENTRY count in the target playlist
	matchCount   int               // target entries whose key resolves to a wanted path
	rootOK       bool              // $ROOT folder SUBNODES present (creation target)
	rootSubCount int               // direct NODE children of $ROOT SUBNODES
}

// UpsertNMLPlaylist finds-or-creates the flat playlist named name (created under the $ROOT
// playlists folder) and unions in the given tracks' primary keys, updating the ENTRIES count.
// Returns how many keys were newly added. No-op (no write) when nothing new. Back the file up
// BEFORE calling.
func UpsertNMLPlaylist(path, name string, entryPaths []string) (added int, err error) {
	if path == "" || name == "" {
		return 0, errors.New("musiclib: empty nml path or playlist name")
	}
	scan, err := scanNMLForPlaylist(path, name, pathSet(entryPaths))
	if err != nil {
		return 0, err
	}
	// Ordered dedup of new keys (input order; skips dupes, already-present, not-in-collection).
	seen := map[string]bool{}
	var addKeys []string
	for _, p := range entryPaths {
		k := scan.keyByPath[p]
		if k == "" || seen[k] || scan.existingKeys[k] {
			continue
		}
		seen[k] = true
		addKeys = append(addKeys, k)
	}
	if scan.exists && len(addKeys) == 0 {
		return 0, nil
	}
	if !scan.exists && !scan.rootOK {
		return 0, errors.New("musiclib: playlists $ROOT folder not found")
	}
	err = rewriteNMLFile(path, func(src io.Reader, dst io.Writer) error {
		if scan.exists {
			return appendPlaylistKeys(src, dst, name, addKeys, scan.entryCount+len(addKeys))
		}
		return insertPlaylistNode(src, dst, name, addKeys, scan.rootSubCount+1)
	})
	if err != nil {
		return 0, err
	}
	return len(addKeys), nil
}

// RemoveFromNMLPlaylist prunes the given tracks (matched by resolved PRIMARYKEY path) from the
// playlist named name, updating its ENTRIES count. Returns how many entries were removed.
// No-op (no write) when nothing matches.
func RemoveFromNMLPlaylist(path, name string, entryPaths []string) (removed int, err error) {
	if path == "" || name == "" {
		return 0, errors.New("musiclib: empty nml path or playlist name")
	}
	want := pathSet(entryPaths)
	scan, err := scanNMLForPlaylist(path, name, want)
	if err != nil {
		return 0, err
	}
	if !scan.exists || scan.matchCount == 0 {
		return 0, nil
	}
	err = rewriteNMLFile(path, func(src io.Reader, dst io.Writer) error {
		return removePlaylistEntries(src, dst, name, want, scan.entryCount-scan.matchCount)
	})
	if err != nil {
		return 0, err
	}
	return scan.matchCount, nil
}

// pathSet builds a lookup of non-empty paths.
func pathSet(paths []string) map[string]bool {
	out := make(map[string]bool, len(paths))
	for _, p := range paths {
		if p != "" {
			out[p] = true
		}
	}
	return out
}

// scanNMLForPlaylist streams the file once, collecting the COLLECTION keys of wanted paths, the
// target playlist's existing keys/counts, and the $ROOT SUBNODES creation target.
func scanNMLForPlaylist(path, name string, wantPaths map[string]bool) (nmlPlaylistScan, error) {
	f, err := os.Open(path)
	if err != nil {
		return nmlPlaylistScan{}, err
	}
	defer func() { _ = f.Close() }() // read-only
	scan := nmlPlaylistScan{keyByPath: map[string]string{}, existingKeys: map[string]bool{}}
	dec := xml.NewDecoder(bufio.NewReaderSize(f, 1<<20))
	depth := 0
	inCollection, inColEntry := false, false
	inTarget, targetDone := false, false
	targetDepth := -1
	playlistsDepth, rootDepth, subDepth := -1, -1, -1
	inPlaylists, inRoot, inSub := false, false, false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return scan, nil
		}
		if err != nil {
			return scan, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "COLLECTION":
				inCollection = true
			case "ENTRY":
				if inCollection {
					inColEntry = true
				}
				if inTarget {
					scan.entryCount++
				}
			case "LOCATION":
				if inColEntry {
					if p := locPath(t.Attr); p != "" && wantPaths[p] {
						scan.keyByPath[p] = rawLocKey(t.Attr)
					}
				}
			case "PRIMARYKEY":
				if inTarget {
					k := attrVal(t.Attr, "KEY")
					scan.existingKeys[k] = true
					if wantPaths[resolveKey(k)] {
						scan.matchCount++
					}
				}
			case "PLAYLISTS":
				inPlaylists, playlistsDepth = true, depth
			case "NODE":
				if !targetDone && !inTarget && attrVal(t.Attr, "TYPE") == "PLAYLIST" && attrVal(t.Attr, "NAME") == name {
					scan.exists, inTarget, targetDepth = true, true, depth
				}
				if inPlaylists && !inRoot && rootDepth < 0 && depth == playlistsDepth+1 &&
					attrVal(t.Attr, "TYPE") == "FOLDER" && attrVal(t.Attr, "NAME") == "$ROOT" {
					inRoot, rootDepth = true, depth
				}
				if inSub && depth == subDepth+1 {
					scan.rootSubCount++
				}
			case "SUBNODES":
				if inRoot && !scan.rootOK && depth == rootDepth+1 {
					scan.rootOK, inSub, subDepth = true, true, depth
				}
			}
			depth++
		case xml.EndElement:
			depth--
			switch t.Name.Local {
			case "COLLECTION":
				inCollection = false
			case "ENTRY":
				inColEntry = false
			case "PLAYLISTS":
				inPlaylists = false
			case "NODE":
				if inTarget && depth == targetDepth {
					inTarget, targetDone = false, true
				}
				if inRoot && depth == rootDepth {
					inRoot = false
				}
			case "SUBNODES":
				if inSub && depth == subDepth {
					inSub = false
				}
			}
		}
	}
}

// rawLocKey builds the Traktor primary key from LOCATION attrs: VOLUME+DIR+FILE.
func rawLocKey(attr []xml.Attr) string {
	return attrVal(attr, "VOLUME") + attrVal(attr, "DIR") + attrVal(attr, "FILE")
}

// appendPlaylistKeys streams src→dst, rewriting the target playlist's ENTRIES count and
// appending one ENTRY/PRIMARYKEY per key before its </PLAYLIST>. Everything else verbatim.
func appendPlaylistKeys(src io.Reader, dst io.Writer, name string, keys []string, newCount int) error {
	dec := xml.NewDecoder(src)
	enc := xml.NewEncoder(dst)
	depth, targetDepth := 0, -1
	inTarget, done, injected := false, false, false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if !done && !inTarget && t.Name.Local == "NODE" &&
				attrVal(t.Attr, "TYPE") == "PLAYLIST" && attrVal(t.Attr, "NAME") == name {
				inTarget, targetDepth = true, depth
			}
			if inTarget && t.Name.Local == "PLAYLIST" {
				t.Attr = setAttr(t.Attr, "ENTRIES", strconv.Itoa(newCount))
				tok = t
			}
			depth++
		case xml.EndElement:
			depth--
			if inTarget && t.Name.Local == "PLAYLIST" && !injected {
				if err := emitPlaylistEntries(enc, keys); err != nil {
					return err
				}
				injected = true
			}
			if inTarget && t.Name.Local == "NODE" && depth == targetDepth {
				inTarget, done = false, true
			}
		}
		if err := enc.EncodeToken(tok); err != nil {
			return err
		}
	}
	if !injected {
		return errors.New("musiclib: playlist node vanished during rewrite")
	}
	return enc.Flush()
}

// insertPlaylistNode streams src→dst, bumping the $ROOT SUBNODES COUNT and injecting a fresh
// PLAYLIST NODE (fix_grids.upsert_playlist creation branch) before its </SUBNODES>.
func insertPlaylistNode(src io.Reader, dst io.Writer, name string, keys []string, newSubCount int) error {
	dec := xml.NewDecoder(src)
	enc := xml.NewEncoder(dst)
	depth := 0
	playlistsDepth, rootDepth, subDepth := -1, -1, -1
	inPlaylists, inRoot := false, false
	injected := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "PLAYLISTS":
				inPlaylists, playlistsDepth = true, depth
			case "NODE":
				if inPlaylists && !inRoot && rootDepth < 0 && depth == playlistsDepth+1 &&
					attrVal(t.Attr, "TYPE") == "FOLDER" && attrVal(t.Attr, "NAME") == "$ROOT" {
					inRoot, rootDepth = true, depth
				}
			case "SUBNODES":
				if inRoot && subDepth < 0 && depth == rootDepth+1 {
					subDepth = depth
					t.Attr = setAttr(t.Attr, "COUNT", strconv.Itoa(newSubCount))
					tok = t
				}
			}
			depth++
		case xml.EndElement:
			depth--
			if t.Name.Local == "SUBNODES" && depth == subDepth && !injected {
				if err := emitNewPlaylistNode(enc, name, keys); err != nil {
					return err
				}
				injected = true
			}
			if t.Name.Local == "NODE" && inRoot && depth == rootDepth {
				inRoot = false
			}
			if t.Name.Local == "PLAYLISTS" {
				inPlaylists = false
			}
		}
		if err := enc.EncodeToken(tok); err != nil {
			return err
		}
	}
	if !injected {
		return errors.New("musiclib: playlists $ROOT folder not found")
	}
	return enc.Flush()
}

// removePlaylistEntries streams src→dst, dropping target-playlist ENTRYs whose PRIMARYKEY
// resolves to a wanted path and rewriting the ENTRIES count. Everything else verbatim.
func removePlaylistEntries(src io.Reader, dst io.Writer, name string, want map[string]bool, newCount int) error {
	dec := xml.NewDecoder(src)
	enc := xml.NewEncoder(dst)
	depth, targetDepth := 0, -1
	inTarget, done := false, false
	var buf []xml.Token // ENTRY subtree being buffered inside the target playlist
	drop := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if buf != nil {
			if se, ok := tok.(xml.StartElement); ok {
				depth++
				if se.Name.Local == "PRIMARYKEY" && want[resolveKey(attrVal(se.Attr, "KEY"))] {
					drop = true
				}
			}
			if ee, ok := tok.(xml.EndElement); ok {
				depth--
				if ee.Name.Local == "ENTRY" {
					buf = append(buf, xml.CopyToken(tok))
					if !drop {
						for _, bt := range buf {
							if err := enc.EncodeToken(bt); err != nil {
								return err
							}
						}
					}
					buf = nil
					continue
				}
			}
			buf = append(buf, xml.CopyToken(tok))
			continue
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if !done && !inTarget && t.Name.Local == "NODE" &&
				attrVal(t.Attr, "TYPE") == "PLAYLIST" && attrVal(t.Attr, "NAME") == name {
				inTarget, targetDepth = true, depth
			}
			if inTarget && t.Name.Local == "PLAYLIST" {
				t.Attr = setAttr(t.Attr, "ENTRIES", strconv.Itoa(newCount))
				tok = t
			}
			depth++
			if inTarget && t.Name.Local == "ENTRY" {
				buf = []xml.Token{xml.CopyToken(tok)}
				drop = false
				continue
			}
		case xml.EndElement:
			depth--
			if inTarget && t.Name.Local == "NODE" && depth == targetDepth {
				inTarget, done = false, true
			}
		}
		if err := enc.EncodeToken(tok); err != nil {
			return err
		}
	}
	return enc.Flush()
}

// emitNewPlaylistNode writes <NODE TYPE="PLAYLIST"><PLAYLIST ENTRIES TYPE="LIST" UUID> + entries.
func emitNewPlaylistNode(enc *xml.Encoder, name string, keys []string) error {
	node := startElem("NODE", [][2]string{{"TYPE", "PLAYLIST"}, {"NAME", name}})
	if err := enc.EncodeToken(node); err != nil {
		return err
	}
	pl := startElem("PLAYLIST", [][2]string{
		{"ENTRIES", strconv.Itoa(len(keys))}, {"TYPE", "LIST"}, {"UUID", newUUIDHex()},
	})
	if err := enc.EncodeToken(pl); err != nil {
		return err
	}
	if err := emitPlaylistEntries(enc, keys); err != nil {
		return err
	}
	if err := enc.EncodeToken(pl.End()); err != nil {
		return err
	}
	return enc.EncodeToken(node.End())
}

// emitPlaylistEntries writes one <ENTRY><PRIMARYKEY TYPE="TRACK" KEY=k/></ENTRY> per key.
func emitPlaylistEntries(enc *xml.Encoder, keys []string) error {
	for _, k := range keys {
		e := xml.StartElement{Name: xml.Name{Local: "ENTRY"}}
		if err := enc.EncodeToken(e); err != nil {
			return err
		}
		if err := emitElem(enc, "PRIMARYKEY", [][2]string{{"TYPE", "TRACK"}, {"KEY", k}}); err != nil {
			return err
		}
		if err := enc.EncodeToken(e.End()); err != nil {
			return err
		}
	}
	return nil
}

// newUUIDHex returns a v4 UUID as 32 hex chars (matches Python uuid4().hex).
func newUUIDHex() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never fails (panics on broken entropy)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return hex.EncodeToString(b[:])
}

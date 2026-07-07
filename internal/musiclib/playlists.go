package musiclib

import (
	"encoding/xml"
	"io"
	"path"
)

// nmlNode is one PLAYLISTS tree node (FOLDER with SUBNODES, or PLAYLIST with entries).
type nmlNode struct {
	Type     string `xml:"TYPE,attr"` // FOLDER|PLAYLIST|SMARTLIST
	Name     string `xml:"NAME,attr"`
	Subnodes struct {
		Nodes []nmlNode `xml:"NODE"`
	} `xml:"SUBNODES"`
	Playlist struct {
		Entries []struct {
			Key struct {
				Type string `xml:"TYPE,attr"`
				Key  string `xml:"KEY,attr"`
			} `xml:"PRIMARYKEY"`
		} `xml:"ENTRY"`
	} `xml:"PLAYLIST"`
}

// ParseNMLPlaylists streams an NML until the PLAYLISTS element and walks its folder tree
// into flat playlists (Folder = "Foo/Sub", "" at root; "$ROOT" elided). Traktor SMARTLIST
// nodes are skipped - rave-mate has its own smart playlists. The PLAYLISTS section is tiny
// next to COLLECTION, so decoding it whole is fine.
func ParseNMLPlaylists(r io.Reader) ([]Playlist, error) {
	dec := xml.NewDecoder(r)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil, nil // no PLAYLISTS section
		}
		if err != nil {
			return nil, err
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "PLAYLISTS" {
			continue
		}
		var root struct {
			Nodes []nmlNode `xml:"NODE"`
		}
		if err := dec.DecodeElement(&root, &se); err != nil {
			return nil, err
		}
		var out []Playlist
		for _, n := range root.Nodes {
			walkNMLNode(n, "", &out)
		}
		return out, nil
	}
}

// walkNMLNode flattens one node: folders recurse (path-accumulating), playlists emit.
func walkNMLNode(n nmlNode, folder string, out *[]Playlist) {
	switch n.Type {
	case "FOLDER":
		f := folder
		if n.Name != "" && n.Name != "$ROOT" {
			f = path.Join(folder, n.Name)
		}
		for _, c := range n.Subnodes.Nodes {
			walkNMLNode(c, f, out)
		}
	case "PLAYLIST":
		pl := Playlist{Name: n.Name, Folder: folder}
		for _, e := range n.Playlist.Entries {
			if e.Key.Type == "TRACK" && e.Key.Key != "" {
				pl.Paths = append(pl.Paths, resolveKey(e.Key.Key))
			}
		}
		*out = append(*out, pl)
	}
}

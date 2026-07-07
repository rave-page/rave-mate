package musiclib

import (
	"strings"
	"testing"
)

// rbLibSample: 2 collection tracks + a PLAYLISTS tree with a root folder containing one
// folder ("House") that holds a playlist ("Set A") referencing both tracks by TrackID, and a
// top-level playlist ("Faves") referencing one. Exercises TrackID resolution + folder paths.
const rbLibSample = `<?xml version="1.0" encoding="UTF-8"?>
<DJ_PLAYLISTS Version="1.0.0">
<PRODUCT Name="rekordbox" Version="6.0" Company="Pioneer"/>
<COLLECTION Entries="2">
<TRACK TrackID="1" Name="Drop It" Artist="DJ Test" AverageBpm="128.00"
 Location="file://localhost/C:/Music/Drop%20It.mp3"></TRACK>
<TRACK TrackID="2" Name="Lift Off" Artist="DJ Two" AverageBpm="124.00"
 Location="file://localhost/C:/Music/Lift%20Off.mp3"></TRACK>
</COLLECTION>
<PLAYLISTS>
<NODE Type="0" Name="ROOT" Count="2">
<NODE Type="0" Name="House" Count="1">
<NODE Type="1" Name="Set A" KeyType="0" Entries="2">
<TRACK Key="1"/>
<TRACK Key="2"/>
</NODE>
</NODE>
<NODE Type="1" Name="Faves" KeyType="0" Entries="1">
<TRACK Key="2"/>
</NODE>
</NODE>
</PLAYLISTS>
</DJ_PLAYLISTS>`

func TestParseRekordboxLibrary(t *testing.T) {
	tracks, pls, err := ParseRekordboxLibrary(strings.NewReader(rbLibSample))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(tracks) != 2 {
		t.Fatalf("tracks=%d, want 2", len(tracks))
	}
	if len(pls) != 2 {
		t.Fatalf("playlists=%d, want 2: %+v", len(pls), pls)
	}
	// Flatten order is depth-first: "Set A" (in folder House) then "Faves" (root).
	setA := pls[0]
	if setA.Name != "Set A" || setA.Folder != "House" {
		t.Errorf("Set A: name=%q folder=%q", setA.Name, setA.Folder)
	}
	if len(setA.Paths) != 2 ||
		!strings.HasSuffix(setA.Paths[0], "Drop It.mp3") ||
		!strings.HasSuffix(setA.Paths[1], "Lift Off.mp3") {
		t.Errorf("Set A paths: %v", setA.Paths)
	}
	if strings.Contains(setA.Paths[0], "%20") {
		t.Errorf("path not URL-decoded: %q", setA.Paths[0])
	}
	faves := pls[1]
	if faves.Name != "Faves" || faves.Folder != "" {
		t.Errorf("Faves: name=%q folder=%q", faves.Name, faves.Folder)
	}
	if len(faves.Paths) != 1 || !strings.HasSuffix(faves.Paths[0], "Lift Off.mp3") {
		t.Errorf("Faves paths: %v", faves.Paths)
	}
}

// TestParseRekordboxKeyTypeLocation: KeyType=1 playlists reference tracks by Location URL,
// not TrackID - resolve those directly.
func TestParseRekordboxKeyTypeLocation(t *testing.T) {
	const xml = `<DJ_PLAYLISTS><COLLECTION Entries="0"></COLLECTION>
<PLAYLISTS><NODE Type="0" Name="ROOT" Count="1">
<NODE Type="1" Name="ByPath" KeyType="1" Entries="1">
<TRACK Key="file://localhost/C:/Music/A%20B.flac"/>
</NODE></NODE></PLAYLISTS></DJ_PLAYLISTS>`
	_, pls, err := ParseRekordboxLibrary(strings.NewReader(xml))
	if err != nil {
		t.Fatal(err)
	}
	if len(pls) != 1 || len(pls[0].Paths) != 1 || !strings.HasSuffix(pls[0].Paths[0], "A B.flac") {
		t.Fatalf("keytype=1 resolve: %+v", pls)
	}
}

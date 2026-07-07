package nmlsrc

import "testing"

const sampleCollection = `<?xml version="1.0" encoding="UTF-8"?>
<NML VERSION="19">
  <COLLECTION ENTRIES="2">
    <ENTRY MODIFIED_DATE="2024/1/1" TITLE="Strobe" ARTIST="deadmau5">
      <LOCATION DIR="/:Users/:dj/:Music/:" FILE="strobe.mp3" VOLUME="C:"/>
      <ALBUM TITLE="For Lack of a Better Name"/>
      <INFO GENRE="Progressive House" LABEL="mau5trap" KEY="9d" PLAYTIME="637"/>
      <TEMPO BPM="128.000000"/>
    </ENTRY>
    <ENTRY TITLE="Ghosts 'n' Stuff" ARTIST="deadmau5">
      <ALBUM TITLE="For Lack of a Better Name"/>
      <INFO GENRE="Electro House" KEY="5a"/>
      <TEMPO BPM="128.000000"/>
    </ENTRY>
  </COLLECTION>
</NML>`

func TestIndexCollection(t *testing.T) {
	idx, err := IndexBytes([]byte(sampleCollection))
	if err != nil {
		t.Fatal(err)
	}
	if len(idx) != 2 {
		t.Fatalf("want 2 entries, got %d", len(idx))
	}
	m, ok := idx[Key("Strobe", "deadmau5")]
	if !ok {
		t.Fatal("Strobe not indexed")
	}
	if m.Album != "For Lack of a Better Name" || m.Genre != "Progressive House" || m.Label != "mau5trap" || m.Key != "9d" || m.BPM != 128.0 {
		t.Fatalf("meta = %+v", m)
	}
	if m.Path != "/Users/dj/Music/strobe.mp3" {
		t.Fatalf("path = %q", m.Path)
	}
}

func TestKeyCaseInsensitive(t *testing.T) {
	if Key(" Strobe ", "DeadMau5") != Key("strobe", "deadmau5") {
		t.Fatal("Key should normalize case + whitespace")
	}
}

func TestIndexSkipsTitleless(t *testing.T) {
	idx, err := IndexBytes([]byte(`<NML><COLLECTION><ENTRY ARTIST="x"/></COLLECTION></NML>`))
	if err != nil {
		t.Fatal(err)
	}
	if len(idx) != 0 {
		t.Fatalf("titleless entry should be skipped, got %d", len(idx))
	}
}

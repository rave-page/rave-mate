package idmark

import (
	"path/filepath"
	"testing"
)

func TestMatchFileMark(t *testing.T) {
	s := Load("")
	s.Set(`D:\Music\Promos\secret.mp3`, Mark{})
	if _, ok := s.Match(`D:\Music\Promos\secret.mp3`); !ok {
		t.Fatal("exact file must match")
	}
	if _, ok := s.Match(`D:\Music\Promos\other.mp3`); ok {
		t.Fatal("sibling must not match")
	}
	// non-marked passthrough
	if _, ok := s.Match(`D:\Music\released.mp3`); ok {
		t.Fatal("unmarked must not match")
	}
}

func TestMatchDirRecursive(t *testing.T) {
	s := Load("")
	s.Set(`D:\Music\Promos`, Mark{ShowArtist: true})
	cases := []string{
		`D:\Music\Promos\a.mp3`,
		`D:\Music\Promos\nested\deep\b.flac`,
		`D:\Music\Promos`,
	}
	for _, c := range cases {
		m, ok := s.Match(c)
		if !ok || !m.ShowArtist {
			t.Fatalf("%s: want dir mark, got ok=%v m=%+v", c, ok, m)
		}
	}
	// prefix must respect the separator boundary
	if _, ok := s.Match(`D:\Music\PromosOld\a.mp3`); ok {
		t.Fatal("PromosOld must not match Promos")
	}
}

func TestMatchLongestPrefixFileOverridesDir(t *testing.T) {
	s := Load("")
	s.Set(`D:\Music\Promos`, Mark{})                          // dir: hide all
	s.Set(`D:\Music\Promos\mine.mp3`, Mark{ShowArtist: true}) // file override
	s.Set(`D:\Music\Promos\labelok`, Mark{ShowLabel: true})   // nested dir override
	if m, ok := s.Match(`D:\Music\Promos\mine.mp3`); !ok || !m.ShowArtist {
		t.Fatalf("file override lost: %+v", m)
	}
	if m, ok := s.Match(`D:\Music\Promos\labelok\x.mp3`); !ok || !m.ShowLabel || m.ShowArtist {
		t.Fatalf("nested dir override lost: %+v", m)
	}
	if m, ok := s.Match(`D:\Music\Promos\other.mp3`); !ok || m.ShowArtist || m.ShowLabel {
		t.Fatalf("dir mark must govern the rest: %+v", m)
	}
}

func TestMatchCaseAndSeparatorInsensitive(t *testing.T) {
	s := Load("")
	s.Set(`D:\Music\Promos`, Mark{})
	for _, c := range []string{
		`d:\music\promos\A.MP3`,
		`D:/Music/Promos/a.mp3`,
		`D:\MUSIC\PROMOS\nested\a.mp3`,
	} {
		if _, ok := s.Match(c); !ok {
			t.Fatalf("%s: want case/separator-insensitive match", c)
		}
	}
}

func TestSetDedupAndRemove(t *testing.T) {
	s := Load("")
	s.Set(`D:\Music\Promos`, Mark{})
	s.Set(`d:/music/promos/`, Mark{ShowArtist: true}) // same path, different spelling → update
	if got := len(s.List()); got != 1 {
		t.Fatalf("want 1 entry, got %d", got)
	}
	if m, _ := s.Match(`D:\Music\Promos\a.mp3`); !m.ShowArtist {
		t.Fatal("update lost")
	}
	if !s.IsMarked(`D:/Music/Promos`) {
		t.Fatal("IsMarked must fold case/separators")
	}
	s.Remove(`D:\MUSIC\PROMOS`)
	if len(s.List()) != 0 {
		t.Fatal("remove failed")
	}
	if _, ok := s.Match(`D:\Music\Promos\a.mp3`); ok {
		t.Fatal("match after remove")
	}
}

func TestPersistRoundTrip(t *testing.T) {
	f := filepath.Join(t.TempDir(), "idmarks.json")
	s := Load(f)
	s.Set(`D:\Music\Promos`, Mark{ShowLabel: true})
	s2 := Load(f)
	if m, ok := s2.Match(`D:\Music\Promos\x.mp3`); !ok || !m.ShowLabel {
		t.Fatalf("persisted mark lost: ok=%v m=%+v", ok, m)
	}
}

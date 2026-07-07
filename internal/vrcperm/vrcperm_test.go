package vrcperm

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/github"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/vrchat"
)

// fakeGists records create/update calls.
type fakeGists struct {
	creates, updates int
	lastFiles        map[string]string
	failUpdate404    bool
}

func (f *fakeGists) Create(_ context.Context, _ string, files map[string]string, _ bool) (*github.Gist, error) {
	f.creates++
	f.lastFiles = files
	return &github.Gist{ID: fmt.Sprintf("g%d", f.creates), HTMLURL: "https://gist.github.com/o/g"}, nil
}

func (f *fakeGists) Update(_ context.Context, id string, _ string, files map[string]string) (*github.Gist, error) {
	if f.failUpdate404 {
		return nil, fmt.Errorf("github: PATCH /gists/%s HTTP 404: Not Found", id)
	}
	f.updates++
	f.lastFiles = files
	return &github.Gist{ID: id, HTMLURL: "https://gist.github.com/o/" + id}, nil
}

// fakeMembers serves group members with optional per-group failure.
type fakeMembers struct {
	pages map[string][]vrchat.GroupMember // groupID|roleID → all members
	fail  map[string]bool
	calls int
}

func member(name string) vrchat.GroupMember {
	var m vrchat.GroupMember
	m.User.DisplayName = name
	return m
}

func (f *fakeMembers) GroupMembers(_ context.Context, groupID, roleID string, offset, n int) ([]vrchat.GroupMember, error) {
	f.calls++
	key := groupID + "|" + roleID
	if f.fail[key] {
		return nil, fmt.Errorf("HTTP 403")
	}
	all := f.pages[key]
	if offset >= len(all) {
		return nil, nil
	}
	end := min(offset+n, len(all))
	return all[offset:end], nil
}

func newTestService(f *config.WorldSyncFeature, gists GistStore, members MemberSource) (*Service, *int) {
	saves := 0
	s := New(Deps{
		Log:  logbus.New(16),
		Cfg:  func() *config.WorldSyncFeature { return f },
		Save: func() { saves++ },
		Gists: func() GistStore {
			if gists == nil {
				return nil
			}
			return gists
		},
		Owner:   func() string { return "octo" },
		Members: func() MemberSource { return members },
	})
	s.pagePause = 0
	return s, &saves
}

func TestFormatNames(t *testing.T) {
	got := FormatNames([]string{"b", "a", "b", " ", "a"})
	if got != "a\nb\n" {
		t.Fatalf("FormatNames = %q", got)
	}
	if FormatNames(nil) != "\n" {
		t.Fatal("empty list must render a non-empty gist file")
	}
}

func TestFormatJSON(t *testing.T) {
	got := FormatJSON("VIPs", []string{"Zed", "Amy"})
	for _, want := range []string{`"list": "VIPs"`, `"Amy"`, `"Zed"`, `"users"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("FormatJSON missing %s in %s", want, got)
		}
	}
	if FormatJSON("x", nil) == "" || !strings.Contains(FormatJSON("x", nil), "[]") {
		t.Fatal("empty users must be []")
	}
}

func TestImageHostAllowed(t *testing.T) {
	cases := map[string]bool{
		"https://i.imgur.com/x.png":                    true,
		"https://dymattic.github.io/img/poster.png":    true,
		"https://stream.vrcdn.cloud/x.png":             true,
		"http://i.imgur.com/x.png":                     false, // https only
		"https://gist.githubusercontent.com/o/g/raw/f": false, // string-allowlisted, NOT image
		"https://rave.page/media/x.png":                false,
		"https://evil.com/i.imgur.com/x.png":           false,
		"https://notgithub.io.evil.com/x.png":          false,
	}
	for u, want := range cases {
		if got := ImageHostAllowed(u); got != want {
			t.Errorf("ImageHostAllowed(%q) = %v, want %v", u, got, want)
		}
	}
}

func TestExpandListUsersAndRoles(t *testing.T) {
	members := &fakeMembers{pages: map[string][]vrchat.GroupMember{
		"grp_1|rol_1": {member("Role1A"), member("Role1B")},
	}}
	f := &config.WorldSyncFeature{Enabled: true}
	s, _ := newTestService(f, &fakeGists{}, members)
	l := &config.PermList{ID: "l1", Name: "L", Entries: []config.PermEntry{
		{Kind: config.PermEntryUser, UserID: "usr_1", Display: "Direct"},
		{Kind: config.PermEntryGroupRole, GroupID: "grp_1", RoleID: "rol_1", GroupName: "G", RoleName: "R"},
	}}
	names, err := s.ExpandList(context.Background(), l)
	if err != nil {
		t.Fatalf("ExpandList: %v", err)
	}
	if got := FormatNames(names); got != "Direct\nRole1A\nRole1B\n" {
		t.Fatalf("expanded = %q", got)
	}
}

func TestExpandPagination(t *testing.T) {
	var all []vrchat.GroupMember
	for i := 0; i < 250; i++ {
		all = append(all, member(fmt.Sprintf("m%03d", i)))
	}
	members := &fakeMembers{pages: map[string][]vrchat.GroupMember{"grp_1|": all}}
	f := &config.WorldSyncFeature{Enabled: true}
	s, _ := newTestService(f, &fakeGists{}, members)
	e := &config.PermEntry{Kind: config.PermEntryGroupRole, GroupID: "grp_1"}
	names, err := s.expandGroupRole(context.Background(), e)
	if err != nil || len(names) != 250 {
		t.Fatalf("got %d names, err %v", len(names), err)
	}
	if members.calls != 3 { // 100+100+50
		t.Fatalf("calls = %d", members.calls)
	}
}

func TestExpandFallsBackToCache(t *testing.T) {
	members := &fakeMembers{
		pages: map[string][]vrchat.GroupMember{"grp_1|rol_1": {member("Kept")}},
		fail:  map[string]bool{},
	}
	f := &config.WorldSyncFeature{Enabled: true}
	s, _ := newTestService(f, &fakeGists{}, members)
	e := &config.PermEntry{Kind: config.PermEntryGroupRole, GroupID: "grp_1", RoleID: "rol_1"}
	if _, err := s.expandGroupRole(context.Background(), e); err != nil {
		t.Fatalf("seed: %v", err)
	}
	members.fail["grp_1|rol_1"] = true // group went private
	names, err := s.expandGroupRole(context.Background(), e)
	if err == nil {
		t.Fatal("want error after visibility loss")
	}
	if len(names) != 1 || names[0] != "Kept" {
		t.Fatalf("cache fallback = %v", names)
	}
}

func TestPublishListDiffOnly(t *testing.T) {
	gists := &fakeGists{}
	f := &config.WorldSyncFeature{Enabled: true}
	s, saves := newTestService(f, gists, &fakeMembers{})
	l := &config.PermList{ID: "l1", Name: "L", Entries: []config.PermEntry{
		{Kind: config.PermEntryUser, Display: "A"},
	}}

	s.PublishList(context.Background(), l)
	if gists.creates != 1 || l.GistID != "g1" || *saves != 1 {
		t.Fatalf("first publish: creates=%d gist=%q saves=%d", gists.creates, l.GistID, *saves)
	}
	st := s.Status("list:l1")
	if st.URL != "https://gist.githubusercontent.com/octo/g1/raw/allow.txt" || st.Err != "" {
		t.Fatalf("status = %+v", st)
	}

	s.PublishList(context.Background(), l) // unchanged → no write
	if gists.updates != 0 || gists.creates != 1 {
		t.Fatalf("diff-only violated: updates=%d creates=%d", gists.updates, gists.creates)
	}
	if !s.Status("list:l1").Skipped {
		t.Fatal("want Skipped status")
	}

	l.Entries = append(l.Entries, config.PermEntry{Kind: config.PermEntryUser, Display: "B"})
	s.PublishList(context.Background(), l) // changed → update
	if gists.updates != 1 {
		t.Fatalf("updates = %d", gists.updates)
	}
	if got := gists.lastFiles[FileNames]; got != "A\nB\n" {
		t.Fatalf("published names = %q", got)
	}
}

func TestPublishSelfHealsOn404(t *testing.T) {
	gists := &fakeGists{failUpdate404: true}
	f := &config.WorldSyncFeature{Enabled: true}
	s, _ := newTestService(f, gists, &fakeMembers{})
	l := &config.PermList{ID: "l1", Name: "L", GistID: "stale",
		Entries: []config.PermEntry{{Kind: config.PermEntryUser, Display: "A"}}}
	s.PublishList(context.Background(), l)
	if gists.creates != 1 || l.GistID != "g1" {
		t.Fatalf("no self-heal: creates=%d gist=%q err=%q", gists.creates, l.GistID, s.Status("list:l1").Err)
	}
}

func TestPublishChannels(t *testing.T) {
	gists := &fakeGists{}
	f := &config.WorldSyncFeature{
		Enabled: true, PostersOn: true, EventsOn: true, NowPlayingOn: true,
		Posters:        []config.WorldPoster{{Img: "https://i.imgur.com/a.png", Caption: "c", Link: "https://rave.page/x"}},
		NowPlayingLink: "https://rave.page/dym",
	}
	s, _ := newTestService(f, gists, &fakeMembers{})
	s.events = func(context.Context) []Event { return []Event{{Title: "Rave", Date: "Fri Jul 3"}} }
	s.nowPlay = func() NowPlaying { return NowPlaying{Live: true, DJ: "dym", Track: "T"} }

	s.RefreshAll(context.Background())
	if gists.creates != 3 {
		t.Fatalf("creates = %d", gists.creates)
	}
	if f.PostersGistID == "" || f.EventsGistID == "" || f.NowPlayingGistID == "" {
		t.Fatalf("gist ids not persisted: %+v", f)
	}
	np := gists.lastFiles[FileNowPlaying]
	if !strings.Contains(np, `"link": "https://rave.page/dym"`) {
		t.Fatalf("nowplaying link default not applied: %s", np)
	}
}

func TestNotReadyWithoutGitHub(t *testing.T) {
	f := &config.WorldSyncFeature{Enabled: true}
	s, _ := newTestService(f, nil, &fakeMembers{})
	if s.Ready() {
		t.Fatal("Ready without GitHub link")
	}
	l := &config.PermList{ID: "l1", Name: "L", Entries: []config.PermEntry{{Kind: config.PermEntryUser, Display: "A"}}}
	s.PublishList(context.Background(), l)
	if s.Status("list:l1").Err == "" {
		t.Fatal("want error status when unlinked")
	}
}

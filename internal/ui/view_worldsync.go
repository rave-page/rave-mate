package ui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/unityproj"
	"rave.page/mate/internal/vrchat"
	"rave.page/mate/internal/vrcperm"
)

// buildWorldSync is the Worlds tab: permission lists (users + group roles → gist
// allowlists for VideoTXL etc.) and world-display channels (posters / events /
// now-playing). Everything publishes to gists that worlds poll - see
// WORLD_INTEGRATIONS_RESEARCH.md.
func (u *UI) buildWorldSync() fyne.CanvasObject {
	ws := u.svc.WorldSync
	if ws == nil {
		return container.NewCenter(mutedLabel("World Sync unavailable."))
	}
	// Status lines refresh on a shared ticker (torn down with the tab via closers) -
	// no per-widget service subscriptions to leak across tab rebuilds.
	var refs []func()
	intro := widget.NewLabelWithStyle("Worlds", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	body := container.NewVBox(
		intro,
		u.wsLinkHint(&refs),
		u.wsListsCard(&refs),
		u.wsPostersCard(&refs),
		u.wsEventsCard(&refs),
		u.wsNowPlayingCard(&refs),
		u.wsUnityCard(),
	)
	stop := make(chan struct{})
	u.closers = append(u.closers, func() { close(stop) })
	goUI("worldsync-status", func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				fyne.Do(func() {
					for _, fn := range refs {
						fn()
					}
				})
			}
		}
	})
	return container.NewVScroll(body)
}

// wsLinkHint shows what is still missing (GitHub / VRChat link) for full function.
func (u *UI) wsLinkHint(refs *[]func()) fyne.CanvasObject {
	lbl := mutedLabel("")
	refresh := func() {
		var missing []string
		if u.svc.GitHub == nil || !u.svc.GitHub.SignedIn() {
			missing = append(missing, "GitHub (Settings ▸ Integrations ▸ World Sync)")
		}
		if u.svc.Vrchat == nil || !u.svc.Vrchat.State().LoggedIn {
			missing = append(missing, "VRChat (Settings ▸ Integrations ▸ VRChat) - needed for friends browser + group-role expansion")
		}
		if len(missing) == 0 {
			lbl.SetText("")
			lbl.Hide()
			return
		}
		lbl.Show()
		lbl.SetText("Link missing: " + strings.Join(missing, " · "))
	}
	refresh()
	*refs = append(*refs, refresh)
	return lbl
}

// wsStatusLine renders a target's last publish outcome + raw URL actions.
func (u *UI) wsStatusLine(refs *[]func(), key string, gistID func() string, mainFile string) fyne.CanvasObject {
	ws := u.svc.WorldSync
	lbl := mutedLabel("")
	lbl.Wrapping = fyne.TextWrapWord
	copyBtn := widget.NewButtonWithIcon("Copy world URL", theme.ContentCopyIcon(), func() {
		if url := ws.RawURLFor(gistID(), mainFile); url != "" {
			u.app.Clipboard().SetContent(url)
			u.Notify("World Sync", "URL copied - paste it into the world component")
		}
	})
	openBtn := widget.NewButtonWithIcon("Open gist", theme.ComputerIcon(), func() {
		if st := ws.Status(key); st.HTMLURL != "" {
			if uri := mustURL(st.HTMLURL); uri != nil {
				_ = u.app.OpenURL(uri)
			}
		}
	})
	refresh := func() {
		st := ws.Status(key)
		url := ws.RawURLFor(gistID(), mainFile)
		switch {
		case st.Err != "":
			lbl.SetText("Last publish: " + st.Err)
		case url == "":
			lbl.SetText("Not published yet.")
		case !st.When.IsZero():
			lbl.SetText(fmt.Sprintf("Published %s\n%s", st.When.Format("15:04:05"), url))
		default:
			lbl.SetText(url)
		}
		has := url != ""
		setVisible(copyBtn, has)
		setVisible(openBtn, has && ws.Status(key).HTMLURL != "")
	}
	refresh()
	*refs = append(*refs, refresh)
	return container.NewVBox(lbl, container.NewHBox(copyBtn, openBtn))
}

// setVisible shows/hides w.
func setVisible(w fyne.CanvasObject, on bool) {
	if on {
		w.Show()
	} else {
		w.Hide()
	}
}

// wsPublish runs one publish action off the UI thread.
func (u *UI) wsPublish(name string, fn func(ctx context.Context)) {
	goUI("worldsync-publish", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		fn(ctx)
		fyne.Do(func() { u.Notify("World Sync", name+" published") })
	})
}

// ── Permission lists ──────────────────────────────────────────────────────────

// wsListsCard manages the permission lists (one gist per list).
func (u *UI) wsListsCard(refs *[]func()) fyne.CanvasObject {
	f := &u.svc.Cfg.Features.WorldSync
	ws := u.svc.WorldSync

	rows := container.NewVBox()
	var rebuild func()
	rebuild = func() {
		rows.RemoveAll()
		for i := range f.Lists {
			l := &f.Lists[i]
			key := "list:" + l.ID
			edit := widget.NewButtonWithIcon("", theme.DocumentCreateIcon(), func() {
				u.wsEditListDialog(l, rebuild)
			})
			pub := widget.NewButtonWithIcon("", theme.UploadIcon(), func() {
				u.wsPublish(l.Name, func(ctx context.Context) { ws.PublishList(ctx, l) })
			})
			del := widget.NewButtonWithIcon("", theme.DeleteIcon(), func() {
				dialog.ShowConfirm("Delete list", fmt.Sprintf("Delete %q? The gist stays on GitHub (delete it there if needed).", l.Name), func(ok bool) {
					if !ok {
						return
					}
					f.Lists = append(f.Lists[:i], f.Lists[i+1:]...)
					u.saveCfg()
					rebuild()
				}, u.win)
			})
			head := container.NewBorder(nil, nil, nil, container.NewHBox(edit, pub, del),
				widget.NewLabel(fmt.Sprintf("%s - %d entries", l.Name, len(l.Entries))))
			rows.Add(container.NewVBox(head, u.wsStatusLine(refs, key, func() string { return l.GistID }, vrcperm.FileNames)))
		}
		if len(f.Lists) == 0 {
			rows.Add(mutedLabel("No permission lists yet. Add one below."))
		}
		rows.Refresh()
	}
	rebuild()

	nameEntry := newEntry()
	nameEntry.SetPlaceHolder("list name (e.g. VIP video control)")
	addBtn := widget.NewButtonWithIcon("Add list", theme.ContentAddIcon(), func() {
		name := strings.TrimSpace(nameEntry.Text)
		if name == "" {
			u.Notify("World Sync", "Name the list first")
			return
		}
		f.Lists = append(f.Lists, config.PermList{ID: fmt.Sprintf("list-%d", time.Now().UnixNano()), Name: name})
		u.saveCfg()
		nameEntry.SetText("")
		rebuild()
		u.wsEditListDialog(&f.Lists[len(f.Lists)-1], rebuild)
	})

	return cardWithHelp("Permission lists", "Whitelists worlds poll from a gist - users + VRChat group roles.",
		"Each list publishes one gist with two files:\n"+
			"allow.txt - display names, one per line (VideoTXL Remote Whitelist newline mode, generic loaders)\n"+
			"allow.json - {\"users\":[…]} (VideoTXL JSON mode, array path 'users')\n\n"+
			"Group-role entries expand to CURRENT members while rave-mate runs - members of that role become publicly listed in the gist. Udon has no runtime group API, so this server-side expansion is the only way to grant by role.",
		rows, container.NewBorder(nil, nil, nil, addBtn, nameEntry))
}

// wsEditListDialog edits one list's entries (friends browser + group-role browser).
func (u *UI) wsEditListDialog(l *config.PermList, onChange func()) {
	entries := container.NewVBox()
	var rebuild func()
	rebuild = func() {
		entries.RemoveAll()
		for i := range l.Entries {
			e := l.Entries[i]
			var label string
			if e.Kind == config.PermEntryUser {
				label = "User: " + e.Display
			} else {
				role := e.RoleName
				if role == "" {
					role = "all members"
				}
				label = fmt.Sprintf("Group role: %s - %s", e.GroupName, role)
			}
			del := widget.NewButtonWithIcon("", theme.DeleteIcon(), func() {
				l.Entries = append(l.Entries[:i], l.Entries[i+1:]...)
				u.saveCfg()
				rebuild()
			})
			entries.Add(container.NewBorder(nil, nil, nil, del, widget.NewLabel(label)))
		}
		if len(l.Entries) == 0 {
			entries.Add(mutedLabel("Empty list - add friends or group roles."))
		}
		entries.Refresh()
	}
	rebuild()

	addFriend := widget.NewButtonWithIcon("Add friend…", theme.AccountIcon(), func() {
		u.wsFriendPicker(func(id, display string) {
			l.Entries = append(l.Entries, config.PermEntry{Kind: config.PermEntryUser, UserID: id, Display: display})
			u.saveCfg()
			rebuild()
		})
	})
	addName := widget.NewButtonWithIcon("Add name…", theme.ContentAddIcon(), func() {
		e := newEntry()
		e.SetPlaceHolder("exact VRChat display name")
		dialog.ShowCustomConfirm("Add display name", "Add", "Cancel", e, func(ok bool) {
			n := strings.TrimSpace(e.Text)
			if !ok || n == "" {
				return
			}
			l.Entries = append(l.Entries, config.PermEntry{Kind: config.PermEntryUser, Display: n})
			u.saveCfg()
			rebuild()
		}, u.win)
	})
	addRole := widget.NewButtonWithIcon("Add group role…", theme.HomeIcon(), func() {
		u.wsGroupRolePicker(func(e config.PermEntry) {
			l.Entries = append(l.Entries, e)
			u.saveCfg()
			rebuild()
		})
	})

	note := mutedLabel("Role grants publish that role's member names to the gist (unlisted but public URL). Only whole-group/role member names are listed - never user ids.")
	note.Wrapping = fyne.TextWrapWord
	content := container.NewBorder(nil,
		container.NewVBox(note, container.NewHBox(addFriend, addName, addRole)),
		nil, nil, container.NewVScroll(entries))
	d := dialog.NewCustom("Edit list: "+l.Name, "Done", content, u.win)
	d.SetOnClosed(onChange)
	d.Resize(fyne.NewSize(560, 480))
	d.Show()
}

// ── Friend picker ─────────────────────────────────────────────────────────────

// wsFriendPicker browses/searches VRChat friends (online + offline pages).
func (u *UI) wsFriendPicker(onPick func(id, display string)) {
	mgr := u.svc.Vrchat
	if mgr == nil || !mgr.State().LoggedIn {
		u.Notify("World Sync", "Link VRChat first (Settings ▸ Integrations)")
		return
	}
	var all []vrchat.Friend
	list := container.NewVBox()
	status := mutedLabel("Loading friends…")

	var search *kitSearchField
	rebuild := func() {
		list.RemoveAll()
		q := strings.ToLower(strings.TrimSpace(search.Text()))
		shown := 0
		for _, fr := range all {
			if q != "" && !strings.Contains(strings.ToLower(fr.DisplayName), q) {
				continue
			}
			if shown >= 60 {
				list.Add(mutedLabel("… refine the filter to see more"))
				break
			}
			shown++
			fr := fr
			add := widget.NewButtonWithIcon("Add", theme.ContentAddIcon(), func() { onPick(fr.ID, fr.DisplayName) })
			list.Add(container.NewBorder(nil, nil, nil, add, widget.NewLabel(fr.DisplayName)))
		}
		if shown == 0 && len(all) > 0 {
			list.Add(mutedLabel("No match."))
		}
		list.Refresh()
	}
	search = newKitSearchField("filter friends…", func(string) { rebuild() })

	goUI("worldsync-friends", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		var got []vrchat.Friend
		for _, offline := range []bool{false, true} {
			for offset := 0; offset < 500; offset += 100 {
				page, err := mgr.Client().Friends(ctx, offset, 100, offline)
				if err != nil || len(page) == 0 {
					break
				}
				got = append(got, page...)
				if len(page) < 100 {
					break
				}
			}
		}
		sort.Slice(got, func(i, j int) bool {
			return strings.ToLower(got[i].DisplayName) < strings.ToLower(got[j].DisplayName)
		})
		fyne.Do(func() {
			all = got
			status.SetText(fmt.Sprintf("%d friends", len(all)))
			rebuild()
		})
	})

	content := container.NewBorder(container.NewVBox(search.Object(), status), nil, nil, nil,
		container.NewVScroll(list))
	d := dialog.NewCustom("Add friend", "Done", content, u.win)
	d.Resize(fyne.NewSize(480, 520))
	d.Show()
}

// ── Group / role picker ───────────────────────────────────────────────────────

// wsGroupRolePicker browses favorites + own groups and searches all groups, then
// picks a role (or all members) to grant.
func (u *UI) wsGroupRolePicker(onPick func(config.PermEntry)) {
	mgr := u.svc.Vrchat
	if mgr == nil || !mgr.State().LoggedIn {
		u.Notify("World Sync", "Link VRChat first (Settings ▸ Integrations)")
		return
	}
	f := &u.svc.Cfg.Features.WorldSync
	list := container.NewVBox()
	status := mutedLabel("Loading your groups…")
	var mine, results []vrchat.Group

	isFav := func(id string) bool {
		for _, g := range f.FavoriteGroups {
			if g.ID == id {
				return true
			}
		}
		return false
	}
	toggleFav := func(id, name string) {
		for i, g := range f.FavoriteGroups {
			if g.ID == id {
				f.FavoriteGroups = append(f.FavoriteGroups[:i], f.FavoriteGroups[i+1:]...)
				u.saveCfg()
				return
			}
		}
		f.FavoriteGroups = append(f.FavoriteGroups, config.FavoriteGroup{ID: id, Name: name})
		u.saveCfg()
	}

	var rebuild func()
	row := func(id, name string, members int) fyne.CanvasObject {
		favIcon := theme.RadioButtonIcon()
		if isFav(id) {
			favIcon = theme.RadioButtonCheckedIcon()
		}
		fav := widget.NewButtonWithIcon("", favIcon, func() {
			toggleFav(id, name)
			rebuild()
		})
		roles := widget.NewButtonWithIcon("Roles…", theme.NavigateNextIcon(), func() {
			u.wsRolePicker(id, name, onPick)
		})
		lbl := name
		if members > 0 {
			lbl = fmt.Sprintf("%s (%d members)", name, members)
		}
		return container.NewBorder(nil, nil, fav, roles, widget.NewLabel(lbl))
	}
	rebuild = func() {
		list.RemoveAll()
		if len(f.FavoriteGroups) > 0 {
			list.Add(smallCaps("Favorites"))
			for _, g := range f.FavoriteGroups {
				list.Add(row(g.ID, g.Name, 0))
			}
		}
		if len(mine) > 0 {
			list.Add(smallCaps("Your groups"))
			for _, g := range mine {
				if !isFav(g.EffectiveID()) {
					list.Add(row(g.EffectiveID(), g.Name, g.MemberCount))
				}
			}
		}
		if len(results) > 0 {
			list.Add(smallCaps("Search results"))
			for _, g := range results {
				if !isFav(g.EffectiveID()) {
					list.Add(row(g.EffectiveID(), g.Name, g.MemberCount))
				}
			}
		}
		list.Refresh()
	}
	rebuild()

	search := newKitSearchField("search all groups…", nil) // submit via button (server-side search)
	searchBtn := widget.NewButtonWithIcon("Search", theme.SearchIcon(), func() {
		q := strings.TrimSpace(search.Text())
		if q == "" {
			return
		}
		status.SetText("Searching…")
		goUI("worldsync-groupsearch", func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			got, err := mgr.Client().SearchGroups(ctx, q, 0, 30)
			fyne.Do(func() {
				if err != nil {
					status.SetText("Search failed: " + err.Error())
					return
				}
				results = got
				status.SetText(fmt.Sprintf("%d results", len(got)))
				rebuild()
			})
		})
	})

	goUI("worldsync-mygroups", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		got, err := mgr.Client().UserGroups(ctx, mgr.CurrentUserID())
		fyne.Do(func() {
			if err != nil {
				status.SetText("Could not load your groups: " + err.Error())
			} else {
				mine = got
				status.SetText(fmt.Sprintf("%d groups", len(got)))
			}
			rebuild()
		})
	})

	note := mutedLabel("You can grant roles of groups you're not in - but member expansion only works where the member list is visible (public groups). Private groups keep their last good expansion.")
	note.Wrapping = fyne.TextWrapWord
	content := container.NewBorder(
		container.NewVBox(container.NewBorder(nil, nil, nil, searchBtn, search.Object()), status),
		note, nil, nil, container.NewVScroll(list))
	d := dialog.NewCustom("Add group role", "Done", content, u.win)
	d.Resize(fyne.NewSize(540, 560))
	d.Show()
}

// wsRolePicker lists a group's roles (+ "All members") for granting.
func (u *UI) wsRolePicker(groupID, groupName string, onPick func(config.PermEntry)) {
	mgr := u.svc.Vrchat
	list := container.NewVBox()
	status := mutedLabel("Loading roles…")
	var d *dialog.CustomDialog

	addRow := func(roleID, roleName, label string) {
		pick := widget.NewButtonWithIcon("Grant", theme.ConfirmIcon(), func() {
			onPick(config.PermEntry{
				Kind: config.PermEntryGroupRole, GroupID: groupID, GroupName: groupName,
				RoleID: roleID, RoleName: roleName,
			})
			d.Hide()
		})
		list.Add(container.NewBorder(nil, nil, nil, pick, widget.NewLabel(label)))
	}
	addRow("", "", "All members")

	goUI("worldsync-roles", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		roles, err := mgr.Client().GroupRoles(ctx, groupID)
		fyne.Do(func() {
			if err != nil {
				status.SetText("Could not load roles: " + err.Error())
				return
			}
			status.SetText(fmt.Sprintf("%d roles", len(roles)))
			for _, r := range roles {
				lbl := r.Name
				if r.IsManagementRole {
					lbl += " (management)"
				}
				addRow(r.ID, r.Name, lbl)
			}
			list.Refresh()
		})
	})

	content := container.NewBorder(status, nil, nil, nil, container.NewVScroll(list))
	d = dialog.NewCustom("Roles of "+groupName, "Cancel", content, u.win)
	d.Resize(fyne.NewSize(460, 420))
	d.Show()
}

// ── Display channels ──────────────────────────────────────────────────────────

// wsPostersCard manages the poster-billboard channel.
func (u *UI) wsPostersCard(refs *[]func()) fyne.CanvasObject {
	f := &u.svc.Cfg.Features.WorldSync
	ws := u.svc.WorldSync

	rows := container.NewVBox()
	var rebuild func()
	rebuild = func() {
		rows.RemoveAll()
		for i := range f.Posters {
			p := f.Posters[i]
			warn := ""
			if p.Img != "" && !vrcperm.ImageHostAllowed(p.Img) {
				warn = " ⚠ image host not VRC-allowlisted"
			}
			edit := widget.NewButtonWithIcon("", theme.DocumentCreateIcon(), func() {
				u.wsPosterDialog(&f.Posters[i], rebuild)
			})
			del := widget.NewButtonWithIcon("", theme.DeleteIcon(), func() {
				f.Posters = append(f.Posters[:i], f.Posters[i+1:]...)
				u.saveCfg()
				rebuild()
			})
			cap := p.Caption
			if cap == "" {
				cap = p.Img
			}
			rows.Add(container.NewBorder(nil, nil, nil, container.NewHBox(edit, del),
				widget.NewLabel(fmt.Sprintf("%d. %s%s", i+1, cap, warn))))
		}
		if len(f.Posters) == 0 {
			rows.Add(mutedLabel("No posters yet."))
		}
		rows.Refresh()
	}
	rebuild()

	addBtn := widget.NewButtonWithIcon("Add poster", theme.ContentAddIcon(), func() {
		f.Posters = append(f.Posters, config.WorldPoster{})
		u.saveCfg()
		u.wsPosterDialog(&f.Posters[len(f.Posters)-1], rebuild)
	})
	toggle := widget.NewCheck("Publish", func(on bool) {
		f.PostersOn = on
		u.saveCfg()
	})
	toggle.SetChecked(f.PostersOn)
	pubBtn := widget.NewButtonWithIcon("Publish now", theme.UploadIcon(), func() {
		u.wsPublish("Posters", func(ctx context.Context) { ws.PublishPosters(ctx) })
	})

	return cardWithHelp("Poster billboards", "Gist-fed billboard content for the world poster prefab.",
		"The gist carries JSON (image URL + caption + link). VRChat images load through a SEPARATE image allowlist - the image URL must be on an allowlisted host (i.imgur.com, *.github.io, i.ibb.co, …). gist/rave.page URLs can't be textures. Text-only posters work everywhere.",
		rows, container.NewHBox(addBtn, toggle, pubBtn),
		u.wsStatusLine(refs, "posters", func() string { return f.PostersGistID }, vrcperm.FilePosters))
}

// wsPosterDialog edits one poster slot.
func (u *UI) wsPosterDialog(p *config.WorldPoster, onSave func()) {
	img := newEntry()
	img.SetText(p.Img)
	img.SetPlaceHolder("https://i.imgur.com/… (VRC image-allowlisted host)")
	warn := mutedLabel("")
	warn.Wrapping = fyne.TextWrapWord
	img.OnChanged = func(s string) {
		if strings.TrimSpace(s) != "" && !vrcperm.ImageHostAllowed(s) {
			warn.SetText("⚠ Host not on VRChat's image allowlist - the prefab will show text only. Allowed: i.imgur.com, i.ibb.co, *.github.io, pbs.twimg.com, …")
		} else {
			warn.SetText("")
		}
	}
	caption := newEntry()
	caption.SetText(p.Caption)
	caption.SetPlaceHolder("caption")
	link := newEntry()
	link.SetText(p.Link)
	link.SetPlaceHolder("https://rave.page/… (shown as text/QR by the prefab)")
	content := container.NewVBox(formGrid(
		fieldLabel("Image"), img,
		fieldLabel("Caption"), caption,
		fieldLabel("Link"), link,
	), warn)
	d := dialog.NewCustomConfirm("Edit poster", "Save", "Cancel", content, func(ok bool) {
		if !ok {
			return
		}
		p.Img, p.Caption, p.Link = strings.TrimSpace(img.Text), caption.Text, strings.TrimSpace(link.Text)
		u.saveCfg()
		onSave()
	}, u.win)
	d.Resize(fyne.NewSize(560, 320))
	d.Show()
}

// wsEventsCard controls the upcoming-events channel.
func (u *UI) wsEventsCard(refs *[]func()) fyne.CanvasObject {
	f := &u.svc.Cfg.Features.WorldSync
	ws := u.svc.WorldSync
	toggle := widget.NewCheck("Publish", func(on bool) {
		f.EventsOn = on
		u.saveCfg()
	})
	toggle.SetChecked(f.EventsOn)
	pubBtn := widget.NewButtonWithIcon("Publish now", theme.UploadIcon(), func() {
		u.wsPublish("Events", func(ctx context.Context) { ws.PublishEvents(ctx) })
	})
	return cardWithHelp("Upcoming events", "Publishes your upcoming rave.page events for the events-board prefab.",
		"Feeds title + date of upcoming rave.page events into a gist the events prefab polls. Refreshes with the World Sync interval; worlds see changes within interval + ~5 min gist CDN cache.",
		container.NewHBox(toggle, pubBtn),
		u.wsStatusLine(refs, "events", func() string { return f.EventsGistID }, vrcperm.FileEvents))
}

// wsNowPlayingCard controls the live now-playing channel.
func (u *UI) wsNowPlayingCard(refs *[]func()) fyne.CanvasObject {
	f := &u.svc.Cfg.Features.WorldSync
	ws := u.svc.WorldSync
	toggle := widget.NewCheck("Publish while live", func(on bool) {
		f.NowPlayingOn = on
		u.saveCfg()
	})
	toggle.SetChecked(f.NowPlayingOn)
	link := newEntry()
	link.SetText(f.NowPlayingLink)
	link.SetPlaceHolder("https://rave.page/yourname")
	link.OnChanged = func(s string) {
		f.NowPlayingLink = strings.TrimSpace(s)
		u.saveCfg()
	}
	img := newEntry()
	img.SetText(f.NowPlayingImg)
	img.SetPlaceHolder("card image (VRC image-allowlisted host, optional)")
	imgWarn := mutedLabel("")
	img.OnChanged = func(s string) {
		f.NowPlayingImg = strings.TrimSpace(s)
		u.saveCfg()
		if f.NowPlayingImg != "" && !vrcperm.ImageHostAllowed(f.NowPlayingImg) {
			imgWarn.SetText("⚠ Host not on VRChat's image allowlist")
		} else {
			imgWarn.SetText("")
		}
	}
	pubBtn := widget.NewButtonWithIcon("Publish now", theme.UploadIcon(), func() {
		u.wsPublish("Now playing", func(ctx context.Context) { ws.PublishNowPlaying(ctx) })
	})
	return cardWithHelp("Now playing", "Live DJ card for worlds: artist/track + your rave.page link.",
		"While a session is live, publishes the currently audible track (artist/title from the session hub's derived output) at most once a minute - plus the gist CDN's ~5 min cache, worlds lag 1–6 min. Track info goes through the session layer output (ID-redaction applies once available).",
		formGrid(fieldLabel("Link"), link, fieldLabel("Image"), img), imgWarn,
		container.NewHBox(toggle, pubBtn),
		u.wsStatusLine(refs, "nowplaying", func() string { return f.NowPlayingGistID }, vrcperm.FileNowPlaying))
}

// wsUnityCard writes the source URLs into configured Unity projects (file-based
// handoff - the editor plugin's World Sync window reads sources.json).
func (u *UI) wsUnityCard() fyne.CanvasObject {
	ws := u.svc.WorldSync
	rows := container.NewVBox()
	rebuild := func() {
		rows.RemoveAll()
		projects := u.svc.Cfg.Features.Unity.Projects
		for _, dir := range projects {
			info := unityproj.Inspect(dir)
			if !info.Valid {
				continue
			}
			dir := dir
			write := widget.NewButtonWithIcon("Write source URLs", theme.UploadIcon(), func() {
				if err := unityproj.WriteWorldSyncSources(dir, ws.SourcesJSON()); err != nil {
					u.Notify("World Sync", "Write failed: "+err.Error())
					return
				}
				u.Notify("World Sync", "sources.json written → "+info.Name)
			})
			rows.Add(container.NewBorder(nil, nil, nil, write, widget.NewLabel(info.Name)))
		}
		if len(rows.Objects) == 0 {
			rows.Add(mutedLabel("No Unity projects configured (Settings ▸ Integrations ▸ Unity)."))
		}
	}
	rebuild()
	return cardWithHelp("Unity projects", "Hand published URLs to the world project.",
		"Writes Assets/rave.page/WorldSync/sources.json into the project. In Unity: Tools → rave.page → World Sync lists the feeds, wires a VideoTXL Remote Whitelist, or copies URLs for any prefab. Re-write after publishing a new list.",
		rows)
}

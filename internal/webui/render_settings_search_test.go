package webui

import (
	"sort"
	"strings"
	"testing"
	"time"

	"rave.page/mate/internal/i18n"
)

// B4d differential gate: the STRUCTURED matcher (render_settings_search.go) must return the same
// result as the pre-B4d HTML-derived matcher for EVERY query, over the whole settings fixture
// corpus. Written before the swap and run against both implementations.
//
// Two complementary proofs:
//  1. TestSettingsSearchHaystacksEquivalent - EXHAUSTIVE. Query terms are whitespace-free
//     (strings.Fields), so a term matches a haystack iff it is a substring of one of its
//     whitespace-free runs. Asserting mutual run containment therefore decides identity for every
//     term that could ever be typed, not for a sample of them.
//  2. TestSettingsSearchDifferential - the PANE level: mechanically derived queries (every distinct
//     word in the corpus + substring boundary variants + multi-term + the fixture queries) must
//     select the identical ordered card-id set per section, through the production path.

// setCardSearchHayLegacy is the pre-B4d haystack, kept verbatim as the differential reference
// (loudnessFieldsLegacy / liveTickLegacy precedent): render the card, strip to text, fold.
func setCardSearchHayLegacy(c setCardSt) string {
	return foldSearch(stripTags(setCardHTML(c)))
}

// setCardSearchHayNew is what setCardMatches folds.
func setCardSearchHayNew(c setCardSt) string {
	var b strings.Builder
	appendSetCardText(&b, c)
	return foldSearch(b.String())
}

// setCorpusCard is one card of the corpus with the fixture + locale it came from.
type setCorpusCard struct {
	name string // fixture/locale/section/card
	card setCardSt
}

// setSearchCorpus builds every card of every fixture, in every installed locale. The tooltip prose
// is locale-dependent, so a locale sweep widens the text corpus by an order of magnitude. only!=nil
// restricts the fixtures (the enumerated-query gate is O(queries x cards) and the exhaustive
// haystack gate already covers every card).
func setSearchCorpus(t testing.TB, locales []string, only ...string) []setCorpusCard {
	t.Helper()
	t.Cleanup(func() { i18n.SetLocale("en") })
	var out []setCorpusCard
	fxs := setFixtures()
	names := make([]string, 0, len(fxs))
	if len(only) > 0 {
		for _, n := range only {
			if _, ok := fxs[n]; !ok {
				t.Fatalf("fixture %q missing from the corpus", n)
			}
		}
		names = append(names, only...)
	} else {
		for n := range fxs {
			names = append(names, n)
		}
	}
	sort.Strings(names) // deterministic order: the fixture map's iteration order is random
	for _, loc := range locales {
		i18n.SetLocale(loc)
		for _, n := range names {
			fx := fxs[n]
			if fx.u.svc.Cfg == nil {
				continue // the unavailable view renders no cards
			}
			stats := fx.u.settingsStatus()
			for _, s := range settingsSections() {
				for _, id := range s.cards {
					out = append(out, setCorpusCard{
						name: n + "/" + loc + "/" + s.id + "/" + id,
						card: fx.u.settingsCardState(id, stats[id]),
					})
				}
			}
		}
	}
	i18n.SetLocale("en")
	return out
}

// runs returns the distinct whitespace-free runs of a folded haystack (every possible query term is
// a substring of exactly these).
func runs(hay string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, r := range strings.Fields(hay) {
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	return out
}

// containsAllRuns reports the first run of a not found inside any run of b.
func containsAllRuns(a, b []string) (string, bool) {
	joined := "\n" + strings.Join(b, "\n") + "\n" // runs are whitespace-free: Contains == "inside one run"
	for _, r := range a {
		if !strings.Contains(joined, r) {
			return r, false
		}
	}
	return "", true
}

// TestSettingsSearchHaystacksEquivalent: for every card in the corpus the two haystacks admit
// EXACTLY the same set of whitespace-free substrings - i.e. every conceivable query term matches
// both or neither.
func TestSettingsSearchHaystacksEquivalent(t *testing.T) {
	corpus := setSearchCorpus(t, localesForSearch())
	var terms, cards int
	for _, cc := range corpus {
		lr, nr := runs(setCardSearchHayLegacy(cc.card)), runs(setCardSearchHayNew(cc.card))
		if r, ok := containsAllRuns(lr, nr); !ok {
			t.Errorf("%s: text run %q reachable in the OLD haystack only (search would lose a match)", cc.name, r)
		}
		if r, ok := containsAllRuns(nr, lr); !ok {
			t.Errorf("%s: text run %q reachable in the NEW haystack only (search would invent a match)", cc.name, r)
		}
		cards++
		for _, r := range lr {
			n := len([]rune(r))
			terms += n * (n + 1) / 2
		}
	}
	if cards == 0 {
		t.Fatal("empty corpus")
	}
	t.Logf("corpus: %d cards, substring-set equality covers %d single-term queries (upper bound, with repeats)", cards, terms)
}

// localesForSearch returns the locales the corpus sweeps (all installed).
func localesForSearch() []string {
	out := []string{}
	for _, l := range i18n.Available() {
		out = append(out, l.Code)
	}
	if len(out) == 0 {
		out = []string{"en"}
	}
	return out
}

// setSearchQueries derives the query corpus mechanically from the cards' own text: every distinct
// word, substring boundary variants of a deterministic slice of them, two-term AND queries, the
// queries the fixtures ship, and whitespace/no-match edges.
func setSearchQueries(corpus []setCorpusCard) []string {
	words := map[string]bool{}
	var ordered []string
	for _, cc := range corpus {
		for _, w := range runs(setCardSearchHayLegacy(cc.card)) {
			if !words[w] {
				words[w] = true
				ordered = append(ordered, w)
			}
		}
	}
	sort.Strings(ordered)
	qs := append([]string{}, ordered...)
	for i, w := range ordered {
		if i%7 != 0 { // boundary variants for a deterministic slice - the full cross product adds no coverage
			continue
		}
		r := []rune(w)
		if len(r) > 1 {
			qs = append(qs, string(r[:1]), string(r[len(r)-1:]), string(r[1:]), string(r[:len(r)-1]))
		}
		if len(r) > 2 {
			qs = append(qs, string(r[:2]), string(r[len(r)-2:]), string(r[1:len(r)-1]))
		}
		qs = append(qs, w+"zz", "zz"+w, strings.ToUpper(w))
	}
	for i := 0; i+1 < len(ordered); i += 97 { // multi-term AND across the corpus
		qs = append(qs, ordered[i]+" "+ordered[i+1], ordered[i]+"  "+ordered[i], "  "+ordered[i]+" ")
	}
	qs = append(qs, "port", `a&b<"c">`, "zzz-no-such-setting", "", " ", "\t\n", "MIDI", "Résolume", "midi port")
	return qs
}

// TestSettingsSearchDifferential: over the derived query corpus, the card-id set the production
// matcher selects per section must equal the pre-B4d matcher's, for every fixture.
func TestSettingsSearchDifferential(t *testing.T) {
	// the query corpus is derived from EVERY card (all fixtures), the per-query sweep runs over the
	// state-shape-distinct ones - every block kind, escaping, unicode, long, gated, both queries
	// (one key per DISTINCT fixture UI: account=empty cfg, djsources=populated, then the edges)
	corpus := setSearchCorpus(t, []string{"en"}, "account", "djsources", "gated", "escaping", "long",
		"unicode", "selectOpen")
	queries := setSearchQueries(setSearchCorpus(t, []string{"en"}))

	// pre-fold both haystacks once per card: the differential is over the MATCH decision, and
	// re-rendering 40 cards per query would make this gate minutes long.
	type hay struct {
		name    string
		old     string
		card    setCardSt
		section string
	}
	hays := make([]hay, 0, len(corpus))
	for _, cc := range corpus {
		parts := strings.Split(cc.name, "/")
		hays = append(hays, hay{name: cc.name, old: setCardSearchHayLegacy(cc.card), card: cc.card, section: parts[2]})
	}

	var checks, hits int
	for _, q := range queries {
		terms := strings.Fields(foldSearch(q))
		var oldSel, newSel []string
		for _, h := range hays {
			wantMatch := matchAllTerms(h.old, terms)
			gotMatch := setCardMatches(h.card, terms)
			checks++
			if wantMatch {
				hits++
				oldSel = append(oldSel, h.section+"/"+h.card.ID)
			}
			if gotMatch {
				newSel = append(newSel, h.section+"/"+h.card.ID)
			}
			if wantMatch != gotMatch {
				t.Fatalf("query %q card %s: old match=%v new match=%v", q, h.name, wantMatch, gotMatch)
			}
		}
		if strings.Join(oldSel, ",") != strings.Join(newSel, ",") {
			t.Fatalf("query %q: selection diverges\n old: %v\n new: %v", q, oldSel, newSel)
		}
	}
	t.Logf("differential: %d queries x %d cards = %d match decisions (%d matches), zero divergence",
		len(queries), len(hays), checks, hits)
}

// TestSettingsSearchPaneMatchesLegacy drives the PRODUCTION path (settingsContentState) and compares
// the pane it builds - sections, order, card ids - against the pre-B4d matcher's verdict computed
// from the same card states. This is the pin that production is wired to the equivalent matcher;
// the differential above only compares the two functions.
func TestSettingsSearchPaneMatchesLegacy(t *testing.T) {
	t.Cleanup(func() { i18n.SetLocale("en") })
	i18n.SetLocale("en")
	fxs := setFixtures()
	for _, name := range []string{"djsources", "account", "escaping", "unicode"} {
		u := fxs[name].u
		// Pin the async gridfix env probe settled: its gate note ("...then enable") landing
		// between the legacy and pane measurements diverges them (raced on linux CI).
		u.gfProbe.mu.Lock()
		u.gfProbe.ready, u.gfProbe.at = true, time.Now()
		u.gfProbe.mu.Unlock()
		stats := u.settingsStatus()
		for _, q := range []string{"port", "midi", "path", "enable", "e", "&", `a&b<"c">`, "midi port",
			"port midi", "zzz-no-such-setting", "ffmpeg", "obs", "vrchat", "Résolume", "SPOUT", "0.0.0.0"} {
			terms := strings.Fields(foldSearch(q))
			var want []string
			for _, s := range settingsSections() {
				for _, id := range s.cards {
					if matchAllTerms(setCardSearchHayLegacy(u.settingsCardState(id, stats[id])), terms) {
						want = append(want, s.id+"/"+id)
					}
				}
			}
			u.setMu.Lock()
			u.setSec, u.setQuery = "account", q
			u.setMu.Unlock()
			c := u.settingsContentState()
			var got []string
			for _, s := range c.Secs {
				for _, card := range s.Cards {
					got = append(got, s.ID+"/"+card.ID)
				}
			}
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("%s query %q: pane diverges from the legacy matcher\n want: %v\n got:  %v", name, q, want, got)
			}
			if c.Searching != (strings.TrimSpace(q) != "") {
				t.Fatalf("%s query %q: Searching=%v", name, q, c.Searching)
			}
			vis, searching := u.settingsVisible()
			if searching != c.Searching || len(vis) != len(got) {
				t.Fatalf("%s query %q: view state %d visible vs %d rendered", name, q, len(vis), len(got))
			}
		}
		u.setMu.Lock()
		u.setQuery = ""
		u.setMu.Unlock()
	}
}

// TestSettingsSearchDifferentialIsNotVacuous: the gate must fail when the structured walk drops a
// text surface. Mutating a card's blocks so a term lives ONLY in the rendered HTML has to be caught
// (a fixture whose mutation reaches no haystack is worse than no fixture).
func TestSettingsSearchDifferentialIsNotVacuous(t *testing.T) {
	c := setCardSt{ID: "x", Title: "Title", St: setStatusSt{V: "ok", T: "state-line"},
		Blocks: []setBlock{{K: "kv", KV: &uiKV{Label: "Kee", DL: "kee", Value: "vaLue"}}}}
	for _, term := range []string{"title", "state-line", "kee", "value"} {
		if !setCardMatches(c, []string{term}) {
			t.Errorf("term %q unreachable in the structured walk", term)
		}
		if !matchAllTerms(setCardSearchHayLegacy(c), []string{term}) {
			t.Errorf("term %q unreachable in the legacy haystack", term)
		}
	}
	// a field's VALUE is an attribute in both paths - it must stay unsearchable
	f := uiField{Label: "Label", DL: "label", Act: "set:a", Value: "attr-only", Type: "text", PH: "ph-only"}
	c2 := setCardSt{ID: "y", Title: "T", Blocks: []setBlock{{K: "field", Fld: &f}}}
	for _, term := range []string{"attr-only", "ph-only"} {
		if setCardMatches(c2, []string{term}) {
			t.Errorf("attribute-only text %q became searchable", term)
		}
		if matchAllTerms(setCardSearchHayLegacy(c2), []string{term}) {
			t.Errorf("attribute-only text %q matched the legacy haystack - the invariant is wrong", term)
		}
	}
	// the run-containment proof must reject a walk that skips a surface
	full := setCardSt{ID: "z", Title: "Alpha", Desc: "Bravo", St: setStatusSt{V: "ok", T: "Charlie"}}
	if _, ok := containsAllRuns(runs(setCardSearchHayLegacy(full)), runs(foldSearch("alpha bravo"))); ok {
		t.Fatal("containsAllRuns accepted a haystack missing a run")
	}
}

// TestSettingsSearchStructuredCoverage drives every block/kid kind + tooltip shape through both
// matchers - the fixture corpus does not carry every combination (e.g. an OPEN select with a
// filtered row set, or the legacy raw-tooltip bridge).
func TestSettingsSearchStructuredCoverage(t *testing.T) {
	t.Cleanup(func() { i18n.SetLocale("en") })
	i18n.SetLocale("en")
	fld := func(l string) *uiField { f := newField(l, "set:a", "val-"+l, "text"); return &f }
	tgl := func(l string) *uiToggle { x := newToggle(l, "set:b", true); return &x }
	sel := func(label, cur string, open bool, rows []selRow) *selState {
		return &selState{ID: "s1", Label: label, CurLabel: cur, Open: open, Filter: "flt", Rows: rows}
	}
	rows := []selRow{{Val: "v1", Label: "Row One", Sub: "sub one", Badge: "B1", Cur: true}, {Val: "v2", Label: "Row Two"}}
	multi := tipTopicSt("account-bridge")
	grid := tipTopicSt("cue-edit")

	cards := []setCardSt{
		{ID: "a", Title: "Head tip grid", TipS: grid, Desc: "card desc", St: setStatusSt{V: "ok", T: "status text"},
			Tgl: &setSwitchSt{Label: "Switch label", On: true},
			Blocks: []setBlock{
				{K: "note", Text: "note text"}, {K: "hint", Tone: "warn", Text: "hint text"},
				{K: "empty", Text: "empty text"}, {K: "installNote", Text: "install note"},
				{K: "noteRaw", HTML: `<b>raw note</b> &amp; more`},
				{K: "field", Fld: fld("Field A"), TipS: multi},
				{K: "field", Fld: fld("Field B"), Tip: tipTopic("icecast")}, // legacy raw bridge
				{K: "toggle", Tgl: tgl("Toggle A"), TipS: multi},
				{K: "toggle", Tgl: tgl("Toggle gated"), Gate: "install ffmpeg", TipS: multi},
				{K: "select", Sel: sel("Sel label", "Sel current", false, rows)},
				{K: "select", Sel: sel("Sel label 2", "Cur 2", true, rows), SelLblS: &ssLabelSt{Text: "Ss label", Tip: multi}},
				{K: "select", Sel: sel("", "Cur 3", true, nil), SelLbl: `<span class=ss-label>Legacy ss</span>`},
				{K: "amenu", Sel: sel("Amenu label", "Amenu cur", false, rows)},
				{K: "kv", KV: &uiKV{Label: "KV key", DL: "kv key", Value: "KV value"}},
			}},
		{ID: "b", Title: "Composites", St: setStatusSt{V: "warn", T: "warn line"},
			Blocks: []setBlock{
				{K: "fpair", Kids: []setKid{
					{K: "field", Fld: fld("Kid field"), TipS: multi},
					{K: "select", Sel: sel("Kid sel", "Kid cur", true, rows)},
					{K: "amenu", Sel: sel("Kid amenu", "Kid amenu cur", false, nil)},
					{K: "btn", Btn: &uiBtn{Label: "Kid btn", Variant: "primary", Act: "x"}},
				}},
				{K: "btnrow", Kids: []setKid{{K: "btn", Btn: &uiBtn{Label: "Row btn"}}}},
				{K: "pathrow", Fld: fld("Path field"), Btn: &uiBtn{Label: "Browse"}},
				{K: "itemrow", Title: "Item title", Sub: "Item sub", Kids: []setKid{{K: "btn", Btn: &uiBtn{Label: "Item btn"}}}},
				{K: "install", ID: "ffmpeg", Text: "install text", Kids: []setKid{{K: "btn", Btn: &uiBtn{Label: "Install now"}}}},
				{K: "region", ID: "reg", HTML: `<div class=x>region text</div>`},
				{K: "raw", HTML: `<p>raw block text</p>`},
				{K: "form", ID: "act-x", Inputs: []setInput{{Type: "text", Name: "n", PH: "placeholder only"}},
					Kids: []setKid{{K: "btn", Btn: &uiBtn{Label: "Form btn"}}}, Submit: "Submit label", SubVar: "primary"},
				{K: "unknown-kind", Text: "never rendered"},
			}},
	}
	for i, c := range cards {
		lr, nr := runs(setCardSearchHayLegacy(c)), runs(setCardSearchHayNew(c))
		if r, ok := containsAllRuns(lr, nr); !ok {
			t.Errorf("card %d: run %q in the OLD haystack only", i, r)
		}
		if r, ok := containsAllRuns(nr, lr); !ok {
			t.Errorf("card %d: run %q in the NEW haystack only", i, r)
		}
	}
	// the surfaces above must really be reachable (an inert fixture proves nothing)
	must := []string{"head tip grid", "card desc", "status text", "note text", "hint text", "empty text",
		"install note", "raw note", "field a", "field b", "toggle a", "toggle gated", "install ffmpeg",
		"sel label", "sel current", "row one", "sub one", "b1", "no matches", "legacy ss", "ss label",
		"amenu label", "kv key", "kv value"}
	for _, m := range must {
		if !setCardMatches(cards[0], strings.Fields(m)) {
			t.Errorf("card 0: %q not searchable", m)
		}
	}
	for _, m := range []string{"kid field", "kid btn", "row btn", "path field", "browse", "item title",
		"item sub", "item btn", "install text", "install now", "region text", "raw block text",
		"form btn", "submit label"} {
		if !setCardMatches(cards[1], strings.Fields(m)) {
			t.Errorf("card 1: %q not searchable", m)
		}
	}
	for _, m := range []string{"switch label", "placeholder only", "never rendered", "val-field a", "flt"} {
		if setCardMatches(cards[0], strings.Fields(m)) || setCardMatches(cards[1], strings.Fields(m)) {
			t.Errorf("%q must not be searchable (attribute / unrendered)", m)
		}
	}
}

// ── numbers: search latency over the fixture corpus (old = render+strip, new = structured walk) ──

func benchSearchCorpus(b *testing.B) []setCardSt {
	b.Helper()
	cc := setSearchCorpus(b, []string{"en"})
	out := make([]setCardSt, 0, len(cc))
	for _, c := range cc {
		out = append(out, c.card)
	}
	return out
}

// BenchmarkSettingsSearchLegacy: one query over the whole corpus, pre-B4d (render every card).
func BenchmarkSettingsSearchLegacy(b *testing.B) {
	cards := benchSearchCorpus(b)
	terms := strings.Fields(foldSearch("port"))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		n := 0
		for _, c := range cards {
			if matchAllTerms(setCardSearchHayLegacy(c), terms) {
				n++
			}
		}
		if n == 0 {
			b.Fatal("no matches - query is inert")
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(len(cards)), "cards")
}

// BenchmarkSettingsSearchStructured: the same query through the structured walk.
func BenchmarkSettingsSearchStructured(b *testing.B) {
	cards := benchSearchCorpus(b)
	terms := strings.Fields(foldSearch("port"))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		n := 0
		for _, c := range cards {
			if setCardMatches(c, terms) {
				n++
			}
		}
		if n == 0 {
			b.Fatal("no matches - query is inert")
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(len(cards)), "cards")
}

// BenchmarkSettingsPaneQuery: the REAL per-keystroke handler-lane cost - build every card's state,
// match, keep the hits. This is what the search box pays on the actWorker.
func BenchmarkSettingsPaneQuery(b *testing.B) {
	u := setFixtureUI(true)
	u.setMu.Lock()
	u.setSec, u.setQuery = "account", "port"
	u.setMu.Unlock()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c := u.settingsContentState()
		if !c.Searching || len(c.Secs) == 0 {
			b.Fatal("query matched nothing")
		}
	}
}

// BenchmarkSettingsPaneQueryLegacy: the same pane through the pre-B4d matcher.
func BenchmarkSettingsPaneQueryLegacy(b *testing.B) {
	u := setFixtureUI(true)
	terms := strings.Fields(foldSearch("port"))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stats := u.settingsStatus()
		hits := 0
		for _, s := range settingsSections() {
			for _, id := range s.cards {
				if matchAllTerms(setCardSearchHayLegacy(u.settingsCardState(id, stats[id])), terms) {
					hits++
				}
			}
		}
		if hits == 0 {
			b.Fatal("query matched nothing")
		}
	}
}

// BenchmarkSettingsSearchHaystackSize reports the bytes each path folds per card.
func BenchmarkSettingsSearchHaystackSize(b *testing.B) {
	cards := benchSearchCorpus(b)
	var oldB, newB int
	for _, c := range cards {
		oldB += len(setCardSearchHayLegacy(c))
		newB += len(setCardSearchHayNew(c))
	}
	b.ReportMetric(float64(oldB)/float64(len(cards)), "old_B/card")
	b.ReportMetric(float64(newB)/float64(len(cards)), "new_B/card")
}

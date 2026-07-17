package webui

import (
	"strings"
	"testing"

	"rave.page/mate/internal/i18n"
)

// TestTooltipsFollowALanguageSwitch: helpTopics held i18n.T's RESULT, resolved when the package var
// was initialized - so every tooltip in the app was frozen to the locale active at process start
// and a language switch never reached one. The prose now resolves in tipTopic, on the render path.
func TestTooltipsFollowALanguageSwitch(t *testing.T) {
	t.Cleanup(func() { i18n.SetLocale("en") })

	i18n.SetLocale("en")
	en := tipTopic("icecast")
	if !strings.Contains(en, "Set capture via Icecast") {
		t.Fatalf("the en tooltip does not carry its own title:\n%s", en)
	}
	if got := i18n.SetLocale("de"); got != "de" {
		t.Skipf("no de catalog to switch to (got %q)", got)
	}
	de := tipTopic("icecast")
	if de == en {
		t.Fatal("the tooltip did not follow the language switch - its text is frozen to the init-time locale")
	}
	if !strings.Contains(de, "Set-Mitschnitt") {
		t.Fatalf("the tooltip switched to something that is not the de catalog:\n%s", de)
	}
	// ...and it is not one-way sticky either.
	i18n.SetLocale("en")
	if back := tipTopic("icecast"); back != en {
		t.Fatal("switching back to en did not restore the English tooltip")
	}
}

// TestEveryHelpTopicHasText: the registry now keys prose off the topic id (help.<id>.title/.body)
// rather than carrying it, so an id with no catalog entry renders the raw KEY at the user. en is
// the source of truth, so a miss here is a bug on every surface that points at the topic.
func TestEveryHelpTopicHasText(t *testing.T) {
	t.Cleanup(func() { i18n.SetLocale("en") })
	i18n.SetLocale("en")
	for id := range helpTopics {
		title, body := "help."+id+".title", "help."+id+".body"
		if got := i18n.T(title); got == title || strings.TrimSpace(got) == "" {
			t.Errorf("topic %q has no title in en.json - its tooltip shows the raw key", id)
		}
		if got := i18n.T(body); got == body || strings.TrimSpace(got) == "" {
			t.Errorf("topic %q has no body in en.json", id)
		}
	}
}

// TestTipTopicKeepsGridAndLinks: resolving the prose at render time must not drop what the registry
// entry actually holds - the keybind grid and the authoritative-source links.
func TestTipTopicKeepsGridAndLinks(t *testing.T) {
	h := tipTopic("cue-edit") // the one topic with a full keybind grid
	if !strings.Contains(h, "tt-kb-keys") {
		t.Fatal("the cue-edit tooltip lost its keybind grid")
	}
	if l := tipTopic("auto-match-pattern"); !strings.Contains(l, "https://github.com/google/re2/wiki/Syntax") {
		t.Fatal("the topic lost its authoritative-source link")
	}
	if tipTopic("no-such-topic") != "" {
		t.Fatal("an unknown topic id rendered something")
	}
}

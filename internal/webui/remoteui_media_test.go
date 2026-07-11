package webui

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/ui"
)

func testUIBare() *UI {
	return &UI{svc: ui.Services{Cfg: &config.Config{}}, active: "live", stop: make(chan struct{})}
}

func TestFetchLocalTokenGuarded(t *testing.T) {
	u := testUIBare()
	h := newRuiHub(u)
	payload := []byte("\xff\xd8jpeg-ish-payload-1234567890")
	url := u.imgBytesURL(payload)
	if url == "" {
		t.Skip("loopback media server unavailable")
	}
	path := url[strings.Index(url, "/img/"):]

	res := h.fetchLocal(ruiMsg{Path: path, Off: 0, Len: 1024})
	if res.Status != http.StatusOK {
		t.Fatalf("status = %d", res.Status)
	}
	got, _ := base64.StdEncoding.DecodeString(res.Data)
	if string(got) != string(payload) {
		t.Fatalf("bytes mismatch: %q", got)
	}
	// offset read (endpoint ignores Range → host skips manually)
	res = h.fetchLocal(ruiMsg{Path: path, Off: 2, Len: 4})
	got, _ = base64.StdEncoding.DecodeString(res.Data)
	if string(got) != string(payload[2:6]) {
		t.Fatalf("offset read mismatch: %q", got)
	}
	// only /m/ + /img/ are reachable
	for _, bad := range []string{"/etc/passwd", "/rmt/x/img/y", "/img/../../secret", "/mfoo"} {
		if r := h.fetchLocal(ruiMsg{Path: bad}); r.Status == http.StatusOK && !strings.HasPrefix(bad, "/img/") {
			t.Fatalf("path %q must not be fetchable", bad)
		}
	}
	if r := h.fetchLocal(ruiMsg{Path: "/img/nonexistent"}); r.Status == http.StatusOK {
		t.Fatal("unknown token must 404")
	}
}

func TestMediaRewriteRoundTrip(t *testing.T) {
	u := testUIBare()
	h := newRuiHub(u)
	url := u.imgBytesURL([]byte("x"))
	if url == "" {
		t.Skip("loopback media server unavailable")
	}
	page := `<img src="` + url + `">`
	wire := h.rewriteMediaOut(page)
	if !strings.Contains(wire, ruiMediaPlaceholder) || strings.Contains(wire, "127.0.0.1") {
		t.Fatalf("host rewrite failed: %s", wire)
	}
	st := u.mirror()
	st.mu.Lock()
	st.sid = "sidX"
	st.mu.Unlock()
	local := u.mirrorRewriteMediaIn(wire)
	if !strings.Contains(local, "/rmt/sidX/img/") {
		t.Fatalf("controller rewrite failed: %s", local)
	}
	releaseUIState(u)
}

func TestProxyServeImgAndMedia(t *testing.T) {
	u := testUIBare()
	port := u.mpProxyPort()
	if port == 0 {
		t.Skip("loopback media server unavailable")
	}
	content := []byte(strings.Repeat("abcdefghij", 100))
	fetch := func(path string, off int64, ln int) (ruiMsg, error) {
		if path != "/img/tok1" && path != "/m/tok2" {
			return ruiMsg{Status: 404}, nil
		}
		if off >= int64(len(content)) {
			return ruiMsg{Status: 200, Total: int64(len(content))}, nil
		}
		end := off + int64(ln)
		if end > int64(len(content)) {
			end = int64(len(content))
		}
		return ruiMsg{Status: 200, CT: "application/test", Total: int64(len(content)),
			Off: off, Data: base64.StdEncoding.EncodeToString(content[off:end])}, nil
	}
	registerRuiProxy("sidT", &ruiProxy{fetch: fetch, cache: map[string][]byte{}, cacheCT: map[string]string{}})
	defer unregisterRuiProxy("sidT")

	get := func(url, rng string) (*http.Response, []byte) {
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		if rng != "" {
			req.Header.Set("Range", rng)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", url, err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return resp, b
	}

	resp, b := get(fmt.Sprintf("http://127.0.0.1:%d/rmt/sidT/img/tok1", port), "")
	if resp.StatusCode != 200 || string(b) != string(content) {
		t.Fatalf("img proxy: status=%d len=%d", resp.StatusCode, len(b))
	}
	resp, b = get(fmt.Sprintf("http://127.0.0.1:%d/rmt/sidT/m/tok2", port), "")
	if resp.StatusCode != 200 || string(b) != string(content) {
		t.Fatalf("media stream proxy: status=%d len=%d", resp.StatusCode, len(b))
	}
	resp, b = get(fmt.Sprintf("http://127.0.0.1:%d/rmt/sidT/m/tok2", port), "bytes=10-")
	if resp.StatusCode != http.StatusPartialContent || string(b) != string(content[10:]) {
		t.Fatalf("ranged media proxy: status=%d len=%d", resp.StatusCode, len(b))
	}
	resp, _ = get(fmt.Sprintf("http://127.0.0.1:%d/rmt/unknown/img/tok1", port), "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown sid must 404, got %d", resp.StatusCode)
	}
}

func TestParseRangeStart(t *testing.T) {
	cases := []struct {
		h    string
		want int64
		ok   bool
	}{
		{"bytes=0-", 0, true}, {"bytes=100-200", 100, true}, {"bytes=100-200, 300-", 100, true},
		{"", 0, false}, {"bytes=-500", 0, false}, {"items=3-4", 0, false},
	}
	for _, c := range cases {
		got, ok := parseRangeStart(c.h)
		if got != c.want || ok != c.ok {
			t.Fatalf("parseRangeStart(%q) = %d,%v", c.h, got, ok)
		}
	}
}

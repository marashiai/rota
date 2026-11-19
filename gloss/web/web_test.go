package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"rota/gloss/review"
)

func TestRoutes(t *testing.T) {
	h := New(review.NewClient("x"), Options{})
	for _, c := range []struct {
		method, path, header string
		code                 int
		has                  string
	}{
		{"GET", "/", "", 200, `name="pr"`},
		{"GET", "/?pr=o/r%2312", "", 302, ""},
		{"GET", "/o/r/12", "", 302, ""},
		{"GET", "/o/r/12/", "", 200, `data-pr="o/r#12"`},
		{"GET", "/o/r/12/app.js", "", 200, "gloss"},
		{"GET", "/o/r/12/app.css", "", 200, ":root"},
		{"POST", "/o/r/12/api/send", "", 403, ""},
		{"GET", "/o/r/12/api/nothing", "", 404, ""},
		{"GET", "/nothing", "", 404, ""},
	} {
		req := httptest.NewRequest(c.method, c.path, strings.NewReader("{}"))
		if c.header != "" {
			req.Header.Set("X-Gloss", c.header)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != c.code || !strings.Contains(w.Body.String(), c.has) {
			t.Errorf("%s %s: %d %q", c.method, c.path, w.Code, w.Body.String()[:min(80, w.Body.Len())])
		}
	}
	if loc := func() string {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/?pr=o/r%2312", nil))
		return w.Header().Get("Location")
	}(); loc != "o/r/12/" {
		t.Errorf("index redirect: %q", loc)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/o/r/12", nil))
	if loc := w.Header().Get("Location"); loc != "12/" {
		t.Errorf("slash redirect: %q", loc)
	}
	_ = http.StatusOK
}

// The page has no back button or swipe of its own, as a browser has its own back.
func TestBackIsTheBrowsers(t *testing.T) {
	w := httptest.NewRecorder()
	New(review.NewClient("x"), Options{}).ServeHTTP(w, httptest.NewRequest("GET", "/o/r/12/", nil))
	if body := w.Body.String(); w.Code != 200 || !strings.Contains(body, `<body data-pr="o/r#12">`) || strings.Contains(body, `id="back"`) || strings.Contains(body, "data-back") {
		t.Errorf("%d %s", w.Code, body[:min(400, len(body))])
	}
	if js, err := assets.ReadFile("app.js"); err != nil || strings.Contains(string(js), "dataset.back") || strings.Contains(string(js), "backing") {
		t.Errorf("app.js still swipes back (%v)", err)
	}
}

// A change goes on when the page that asked for it is left: stopped halfway, an approval would
// leave an empty pending review and approve nothing.
func TestAChangeGoesOnWhenThePageIsLeft(t *testing.T) {
	var asked atomic.Int32
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer gh.Close()
	c := review.NewClient("x")
	c.API = gh.URL
	h := New(c, Options{})
	for _, c := range []struct {
		method, path string
		goesOn       bool
	}{
		{"POST", "/o/r/12/api/approve", true},
		{"GET", "/o/r/12/api/review", false}, // reading stops with the page
	} {
		asked.Store(0)
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // the reviewer has left the page
		req := httptest.NewRequestWithContext(ctx, c.method, c.path, strings.NewReader(`{"head":"abc"}`))
		req.Header.Set("X-Gloss", "1")
		h.ServeHTTP(httptest.NewRecorder(), req)
		if got := asked.Load() > 0; got != c.goesOn {
			t.Errorf("%s %s after the page was left: GitHub asked %d times", c.method, c.path, asked.Load())
		}
	}
}

func TestADiffComesHighlighted(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/commits/abcdef1" {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,1 +1,2 @@\n package a\n+func New() {}\n")
	}))
	defer gh.Close()
	c := review.NewClient("x")
	c.API = gh.URL
	w := httptest.NewRecorder()
	New(c, Options{}).ServeHTTP(w, httptest.NewRequest("GET", "/o/r/12/api/diff/abcdef1", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"text":"func New() {}","hl":[{"k":"k","s":0,"n":4},{"k":"nf","s":5,"n":3}]`) {
		t.Errorf("%d %s", w.Code, w.Body.String())
	}
}

// What the reviewer has read comes with the review, and the page sends what it showed: the
// handler keeps it on this machine, not on GitHub.
func TestThePageKeepsWhatWasRead(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/graphql" {
			t.Errorf("GitHub asked for %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, `{"data":{"viewer":{"login":"me"},"repository":{"pullRequest":{"id":"P","title":"T","state":"OPEN","author":{"login":"me"},
			"comments":{"nodes":[{"id":"C1","body":"**Triage verdict:** unchanged","createdAt":"2026-11-06T10:00:00Z","author":{"login":"me"}}]}}}}}`)
	}))
	defer gh.Close()
	c := review.NewClient("x")
	c.API = gh.URL
	reads := review.NewReads(filepath.Join(t.TempDir(), "read.json"))
	h := New(c, Options{Reads: reads})

	post := func(body, header string) int {
		req := httptest.NewRequest("POST", "/o/r/12/api/read", strings.NewReader(body))
		if header != "" {
			req.Header.Set("X-Gloss", header)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code
	}
	if code := post(`{"threads":{"C1":"2026-11-06T10:00:00Z"}}`, ""); code != 403 {
		t.Errorf("a mark without the page's header: %d", code)
	}
	if code := post(`{"threads":`, "1"); code != 400 {
		t.Errorf("a broken mark: %d", code)
	}
	if r, _ := reads.Get("o/r#12"); len(r.Threads) != 0 {
		t.Fatalf("a refused mark was kept: %+v", r)
	}
	if code := post(`{"threads":{"C1":"2026-11-06T10:00:00Z"}}`, "1"); code != 200 {
		t.Fatalf("a mark: %d", code)
	}
	if r, _ := reads.Get("o/r#12"); !r.Threads["C1"].Equal(time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("the mark was not kept: %+v", r)
	}

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/o/r/12/api/review", nil))
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, `"read":{"threads":{"C1":"2026-11-06T10:00:00Z"}`) || !strings.Contains(body, `"pr_comment":true`) {
		t.Errorf("the review lacks what was read, or which notes are PR comments: %d %s", w.Code, body)
	}
}

// The page shows the chat of the app gloss is part of, framed, with a button that opens it, when
// that app gives one for the pull request; gloss alone shows none.
func TestThePageShowsTheAppsChat(t *testing.T) {
	page := func(o Options) string {
		w := httptest.NewRecorder()
		New(review.NewClient("x"), o).ServeHTTP(w, httptest.NewRequest("GET", "/o/r/12/", nil))
		return w.Body.String()
	}
	if body := page(Options{}); strings.Contains(body, `id="chatbtn"`) || strings.Contains(body, "<iframe") {
		t.Fatal("gloss alone has no chat")
	}
	asked := ""
	body := page(Options{Chat: func(ref review.Ref) string { asked = ref.String(); return "/?embed=chat&agent=alpha" }})
	if asked != "o/r#12" || !strings.Contains(body, `<button id="chatbtn"`) || !strings.Contains(body, `<iframe id="chatframe" src="/?embed=chat&amp;agent=alpha" title="Chat">`) {
		t.Fatalf("the app's chat, for this pull request: %q\n%s", asked, body)
	}
	if js, _ := assets.ReadFile("app.js"); !strings.Contains(string(js), "type: 'rota-context'") || !strings.Contains(string(js), "e.origin !== location.origin") {
		t.Fatal("the page tells the chat what the reviewer looks at, and hears only its own")
	}
}

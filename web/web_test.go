package web

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"rota/core"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// team is a team folder with one finished shift, and the pages served on it.
func team(t *testing.T) (root string, srv *httptest.Server) {
	t.Helper()
	root = t.TempDir()
	stub := filepath.Join(root, "claude")
	write(t, stub, "#!/bin/bash\nhead -n1 >/dev/null\n"+
		`echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}'`+"\n"+
		`printf '{"type":"result","subtype":"success","is_error":false,"result":"ran in %s","num_turns":3,"session_id":"s1","usage":{}}' "$PWD"`+"\n")
	os.Chmod(stub, 0o755)
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\ncommit: false\nsync_repos: false\nclaude_bin: "+stub+"\n")
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: true\nweight: 3\n---\nAlpha fixes **bugs**.\n")
	write(t, filepath.Join(root, "inbox", "gone.json"), `[{"id":"g1","at":"2026-10-29T10:00:00Z","from":"human","text":"a note for a removed agent"}]`)
	write(t, filepath.Join(root, "hosts.json"), `{"linux1":{"hash":"x"}}`)
	write(t, filepath.Join(root, "journal", "2026-10-27.md"), "# Weekly\n\n| agent | weight |\n|---|---|\n| alpha | 3 |\n\n- keep `alpha`\n")
	tm, err := core.Open(root, "rota.conf")
	if err != nil {
		t.Fatal(err)
	}
	null, _ := os.Open(os.DevNull)
	stdout := os.Stdout
	os.Stdout = null
	err = tm.RunShift(context.Background(), "alpha", time.Now(), true, false)
	os.Stdout = stdout
	if err != nil {
		t.Fatal(err)
	}
	srv = httptest.NewUnstartedServer(nil)
	port, _ := strconv.Atoi(strings.Split(srv.Listener.Addr().String(), ":")[1])
	srv.Config.Handler = Handler(root, "rota.conf", port) // the forms check the port they are served on
	srv.Start()
	t.Cleanup(srv.Close)
	return root, srv
}

// notesOf is an agent's notes as it reads them.
func notesOf(tm *core.Team, agent string) string { return tm.NotesText(tm.Notes(agent)) }

func get(t *testing.T, srv *httptest.Server, path string) string {
	t.Helper()
	resp, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("%s: HTTP %d", path, resp.StatusCode)
	}
	return string(b)
}

// Today's report updates in place, and the app reads the mail again, when the team changes: /events
// says when the team folder changed. Nothing reloads a page.
func TestLivePagesUpdateInPlaceWhenTheTeamChanges(t *testing.T) {
	liveCheck = 50 * time.Millisecond
	root, srv := team(t)
	write(t, filepath.Join(root, "reports", time.Now().Format("2006-01-02")+".md"), "# Today\n\nAll quiet.\n")
	write(t, filepath.Join(root, "reports", "2026-01-02.md"), "# Long ago\n")
	if body := get(t, srv, "/reports/"); !strings.Contains(body, "<main data-live>") || !strings.Contains(body, `new EventSource("/events")`) || strings.Contains(body, "location.reload") {
		t.Fatalf("today's report is live and never reloads:\n%s", body)
	}
	if body := get(t, srv, "/reports/2026-01-02"); strings.Contains(body, "data-live") || strings.Contains(body, "EventSource") {
		t.Fatal("an old report is not live")
	}
	if body, js := get(t, srv, "/"), get(t, srv, "/app.js"); !strings.Contains(body, `<script src="/app.js?v=`) || !strings.Contains(js, "new EventSource('/events')") {
		t.Fatal("the mail, the main page, reads itself again when the team changes")
	}
	if js := get(t, srv, "/app.js"); !strings.Contains(js, "onGitHub(t.id, 'Approving'") || !strings.Contains(js, "onGitHub(t.id, 'Sending'") || !strings.Contains(js, `aria-busy="true"`) {
		t.Fatal("what GitHub takes a few seconds to do, an approval or a note, shows a spinner until it is done")
	}
	if js := get(t, srv, "/app.js"); !strings.Contains(js, "draw(document.getElementById('root'), html, !samePlace(drawnAt, at))") || !strings.Contains(js, "morph(el, drawT.content)") || strings.Contains(js, "getElementById('root').innerHTML = (desk()") || !strings.Contains(js, "r.swipes = true") {
		t.Fatal("the app draws into the page as it stands, so that what the human selects stays selected while the team changes, and binds a row's swipe once")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("server-sent events: %q", resp.Header.Get("Content-Type"))
	}
	lines := make(chan string)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	if l := <-lines; l != "retry: 3000" {
		t.Fatalf("the stream starts with how soon to reconnect: %q", l)
	}
	time.Sleep(200 * time.Millisecond)
	write(t, filepath.Join(root, "inbox", "alpha.json"), `[{"id":"a1","at":"2026-10-29T10:00:00Z","from":"human","text":"a new note"}]`)
	timeout := time.After(5 * time.Second)
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatal("the stream ended")
			}
			if l == "data: changed" {
				return
			}
		case <-timeout:
			t.Fatal("a change in the team folder is announced")
		}
	}
}

// The app offers notifications for this device: a service worker, a manifest for a phone's home
// screen, the team's key, what the worker shows (an open question here), the open questions a worker
// from before shows, and posts that need the page's token.
func TestThePagesOfferNotificationsForQuestions(t *testing.T) {
	root, srv := team(t)
	write(t, filepath.Join(root, "questions", "20261029-1700-alpha-ab12.json"), `{"agent":"alpha","asked":"2026-10-29T17:00:00Z","text":"Is production on gpt-5?"}`)
	if body := get(t, srv, "/api/push"); !strings.Contains(body, `"key":"B`) {
		t.Fatalf("the team's public key: %s", body)
	}
	if body := get(t, srv, "/"); !strings.Contains(body, `rel="manifest"`) {
		t.Fatal("the app links the manifest")
	}
	resp, _ := http.Get(srv.URL + "/sw.js")
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.Header.Get("Content-Type") != "application/javascript" || !strings.Contains(string(b), "showNotification") || !strings.Contains(string(b), "/notify.json") {
		t.Fatalf("the service worker: %s", resp.Header.Get("Content-Type"))
	}
	if body := get(t, srv, "/notify.json"); !strings.Contains(body, `"title":"alpha asks you"`) || !strings.Contains(body, `"body":"Is production on gpt-5?"`) || !strings.Contains(body, `"url":"/#questions"`) {
		t.Fatalf("what the worker shows: %s", body)
	}
	if body := get(t, srv, "/manifest.webmanifest"); !strings.Contains(body, `"display": "standalone"`) || !strings.Contains(body, "/mark-512.png") {
		t.Fatal("a manifest a phone can install from")
	}
	// an icon is asked for again each time, and is the same until a new rota draws another
	resp, _ = http.Get(srv.URL + "/mark-192.png")
	resp.Body.Close()
	tag := resp.Header.Get("ETag")
	if resp.Header.Get("Content-Type") != "image/png" || tag == "" || resp.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("the icon: %v", resp.Header)
	}
	req, _ := http.NewRequest("GET", srv.URL+"/mark-192.png", nil)
	req.Header.Set("If-None-Match", tag)
	if again, err := http.DefaultClient.Do(req); err != nil || again.StatusCode != http.StatusNotModified {
		t.Fatalf("the same icon again: %v %v", again, err)
	}
	if body := get(t, srv, "/questions.json"); !strings.Contains(body, `"agent":"alpha"`) || !strings.Contains(body, "Is production on gpt-5?") {
		t.Fatalf("the open questions for the worker: %s", body)
	}
	token := regexp.MustCompile(`data-token="([0-9a-f]+)"`).FindStringSubmatch(get(t, srv, "/"))[1]
	post := func(path string, form url.Values) int {
		resp, err := http.PostForm(srv.URL+path, form)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := post("/push/subscribe", url.Values{"endpoint": {"https://fcm.googleapis.com/fcm/send/test1"}}); code != 403 {
		t.Fatalf("no token, no subscription: %d", code)
	}
	if code := post("/push/subscribe", url.Values{"endpoint": {"https://fcm.googleapis.com/fcm/send/test1"}, "token": {token}}); code != 204 {
		t.Fatalf("a subscription with the token: %d", code)
	}
	tm, _ := core.Open(root, "rota.conf")
	if subs := tm.PushSubscriptions(); len(subs) != 1 || subs[0].Endpoint != "https://fcm.googleapis.com/fcm/send/test1" {
		t.Fatalf("the device is registered: %+v", subs)
	}
	post("/push/unsubscribe", url.Values{"endpoint": {"https://fcm.googleapis.com/fcm/send/test1"}, "token": {token}})
	if len(tm.PushSubscriptions()) != 0 {
		t.Fatal("and unregistered")
	}
}

// The pages are the app at /. An address of the pages before the app leads to the app's page for
// it; nothing else is served; Markdown is escaped.
func TestThePagesRenderTheTeamFolder(t *testing.T) {
	root, srv := team(t)
	body := get(t, srv, "/")
	for _, want := range []string{`<body data-token="`, `<link rel="stylesheet" href="/app.css?v=`, `<script src="/app.js?v=`, `<link rel="manifest"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("the app should contain %q:\n%s", want, body)
		}
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for path, want := range map[string]string{
		"/status/": "/#team", "/agents/": "/#team", "/agents/alpha": "/#agent:alpha", "/settings/": "/#settings", "/spend/": "/#shares",
		"/shares/": "/#shares", "/journal/2026-10-27": "/#team", "/transcripts/": "/#team", "/transcripts/2026-11-08/0900-alpha": "/#run:2026-11-08/0900-alpha",
		"/inbox/": "/#inbox", "/inbox/alpha": "/#agent:alpha", "/reviews/": "/#reviews", "/issues/": "/#issues", "/agents/a%22b": "/#team",
	} {
		resp, err := client.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != want {
			t.Errorf("%s leads to %q, not %q (%d)", path, resp.Header.Get("Location"), want, resp.StatusCode)
		}
	}
	for _, bad := range []string{"/nothing", "/do/pause", "/reports/../../etc/passwd"} {
		resp, err := http.Get(srv.URL + bad)
		if err != nil {
			t.Fatalf("%s: %v", bad, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %d, not 404", bad, resp.StatusCode)
		}
	}
	// a link that chose what narrows Reviews or Issues chooses it still, for the folder and the console
	tm, _ := core.Open(root, "rota.conf")
	for _, step := range []struct {
		path string
		want core.ListFilter
	}{
		{"/reviews/?repo=o/web&mark=" + url.QueryEscape("💬"), core.ListFilter{Repo: "o/web", Mark: "💬"}},
		{"/reviews/?all", core.ListFilter{}},
		{"/issues/?order=oldest", core.ListFilter{Order: "oldest"}},
		{"/issues/?order=bogus", core.ListFilter{Order: "oldest"}},
	} {
		resp, err := client.Get(srv.URL + step.path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		tab := core.ReviewsTab
		if strings.HasPrefix(step.path, "/issues/") {
			tab = core.IssuesTab
		}
		if f := tm.ListFilter(tab); f.Repo != step.want.Repo || f.Mark != step.want.Mark || f.Order != step.want.Order || resp.StatusCode != http.StatusFound {
			t.Errorf("%s: %+v, not %+v (%d)", step.path, f, step.want, resp.StatusCode)
		}
	}
	for in, want := range map[string]string{ // what ends the sentence is not part of the link
		"Screenshot: https://x.y/a/b-2-won.png. The review runs.": `Screenshot: <a href="https://x.y/a/b-2-won.png">https://x.y/a/b-2-won.png</a>. The review runs.`,
		"see https://x.y/z, then https://x.y/q?a=1&b=2: done":     `see <a href="https://x.y/z">https://x.y/z</a>, then <a href="https://x.y/q?a=1&amp;b=2">https://x.y/q?a=1&amp;b=2</a>: done`,
		`is it "https://x.y/z"?`:                                  `is it &#34;<a href="https://x.y/z">https://x.y/z</a>&#34;?`,
		"(at https://x.y/z) and https://x.y/v1.2/file.txt!":       `(at <a href="https://x.y/z">https://x.y/z</a>) and <a href="https://x.y/v1.2/file.txt">https://x.y/v1.2/file.txt</a>!`,
		"https://x.y/?a=1& and https://x.y/?b=2&.":                `<a href="https://x.y/?a=1&amp;">https://x.y/?a=1&amp;</a> and <a href="https://x.y/?b=2&amp;">https://x.y/?b=2&amp;</a>.`,
	} {
		if got := inlineHTML(in); got != want {
			t.Errorf("%q:\n got %s\nwant %s", in, got, want)
		}
	}
	if got := string(markdown("<script>x</script> *em* and **b** at https://x.y/z")); !strings.Contains(got, "&lt;script&gt;") || !strings.Contains(got, "<i>em</i>") || !strings.Contains(got, `<a href="https://x.y/z">`) {
		t.Fatalf("markdown: %s", got)
	}
}

// The app saves an agent's settings only from its own page: another origin or a wrong token is
// refused. A save changes only what it sends, and an empty value takes the team default, unwritten.
func TestTheAgentEditorSavesSafely(t *testing.T) {
	root, srv := team(t)
	token := regexp.MustCompile(`data-token="([0-9a-f]+)"`).FindStringSubmatch(get(t, srv, "/"))[1]
	post := func(origin, token, set string) int {
		form := url.Values{"token": {token}, "action": {"agent-set"}, "arg": {"alpha"}, "text": {set}}
		req, _ := http.NewRequest("POST", srv.URL+"/api/act", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := post("http://evil.example", token, `{"model":"opus"}`); code != 403 {
		t.Fatalf("a foreign origin is refused, got %d", code)
	}
	if code := post("", "wrong", `{"model":"opus"}`); code != 403 {
		t.Fatalf("a wrong token is refused, got %d", code)
	}
	if code := post(srv.URL, token, `{"model":"sonnet","effort":"low","run_minutes":"50"}`); code != 200 {
		t.Fatalf("a genuine save is taken, got %d", code)
	}
	if code := post(srv.URL, token, `{"weight":"4","run_minutes":""}`); code != 200 {
		t.Fatalf("a partial save, got %d", code)
	}
	tm, _ := core.Open(root, "rota.conf")
	if a := tm.Agent("alpha"); a.Weight != 4 || !a.Enabled || a.Model != "sonnet" || a.Effort != "low" || a.RunMinutes != tm.Def.RunMinutes {
		t.Fatalf("a save changes only what it sends; empty takes the default: %+v", a)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "agents", "alpha.md")); strings.Contains(string(b), "run_minutes:") {
		t.Fatalf("an empty value is not written:\n%s", b)
	}
}

// A website that points its own name at this machine (DNS rebinding) is refused on every
// page, so it can neither read the team nor learn the form token.
func TestOnlyThisMachineIsServed(t *testing.T) {
	_, srv := team(t)
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	for _, path := range []string{"/", "/journal/", "/transcripts/", "/agents/alpha", "/mark.png"} {
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		req.Host = "evil.example:" + strconv.Itoa(port)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden || strings.Contains(string(b), "token") {
			t.Fatalf("%s for another host: HTTP %d", path, resp.StatusCode)
		}
	}
	for _, host := range []string{"127.0.0.1:" + strconv.Itoa(port), "localhost:" + strconv.Itoa(port)} {
		req, _ := http.NewRequest("GET", srv.URL+"/", nil)
		req.Host = host
		if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 200 {
			t.Fatalf("%s must be served: %v", host, err)
		}
	}
}

// On the Tailscale address, a request names the address it came in on, or this machine's
// Tailscale name; a ts.net name that reaches localhost is someone else's.
func TestTheTailscaleAddressIsServed(t *testing.T) {
	s := &site{port: 7900}
	ask := func(in, host string) bool {
		a, _ := net.ResolveTCPAddr("tcp", in)
		r := httptest.NewRequest("GET", "/", nil).WithContext(context.WithValue(context.Background(), http.LocalAddrContextKey, a))
		r.Host = host
		return s.thisMachine(r)
	}
	if !ask("100.101.102.103:7900", "100.101.102.103:7900") || ask("100.101.102.103:7900", "evil.example:7900") || ask("127.0.0.1:7900", "mac.tail1.ts.net:7900") {
		t.Fatal("the address a request came in on is this machine, another name is not")
	}
	if ip := core.TailscaleIP(); ip != "" && !ask(ip+":7900", "mac.tail1.ts.net:7900") {
		t.Fatal("this machine's Tailscale name is served on its Tailscale address")
	}
}

func TestServeStopsWithItsContext(t *testing.T) {
	root, _ := team(t)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, root, "rota.conf", port, false) }()
	time.Sleep(200 * time.Millisecond)
	if resp, err := http.Get("http://" + addr(port) + "/"); err != nil || resp.StatusCode != 200 {
		t.Fatalf("served: %v", err)
	}
	conn, err := net.Dial("tcp", addr(port)) // OPTIONS * from another host is refused too
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(conn, "OPTIONS * HTTP/1.1\r\nHost: evil.example:%d\r\nConnection: close\r\n\r\n", port)
	reply, _ := io.ReadAll(conn)
	conn.Close()
	if !strings.HasPrefix(string(reply), "HTTP/1.1 403") {
		t.Fatalf("OPTIONS * for another host: %q", strings.SplitN(string(reply), "\r\n", 2)[0])
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not stop")
	}
}

// The phone is told of an agent's answer the human has not seen: the worker shows the newest of
// what the human hears of, which the pages say (notify.json), and a tap opens the review; a worker
// from before reads the answers themselves.
func TestThePhoneHearsOfAnswers(t *testing.T) {
	root, srv := team(t)
	write(t, filepath.Join(root, ".open.json"), `{"time":"`+time.Now().Format(time.RFC3339)+`","repos":{"o/app":{"1":{"title":"one","pr":true,`+
		`"talk":{"read":"2026-11-05T12:00:00Z","answered":"2026-11-05T12:00:00Z","by":"dev","answer":"Renamed it."}}}}}`)
	if body := get(t, srv, "/answers.json"); !strings.Contains(body, `"agent":"dev"`) || !strings.Contains(body, `"text":"Renamed it."`) ||
		!strings.Contains(body, `"name":"app#1"`) || !strings.Contains(body, `"page":"/review/o/app/1/"`) {
		t.Fatalf("the answers the human has not seen: %s", body)
	}
	if body := get(t, srv, "/sw.js"); !strings.Contains(body, `fetch("/notify.json"`) {
		t.Fatalf("the worker shows what the pages say: %s", body)
	}
	if body := get(t, srv, "/notify.json"); !strings.Contains(body, `"title":"dev answered you on app#1"`) || !strings.Contains(body, `"url":"/review/o/app/1/"`) {
		t.Fatalf("the newest of what the human hears of: %s", body)
	}
	tm, _ := core.Open(root, "rota.conf")
	tm.SeenReview("o/app", 1, time.Date(2026, 11, 5, 12, 0, 1, 0, time.UTC))
	if body := get(t, srv, "/answers.json"); strings.TrimSpace(body) != "[]" {
		t.Fatalf("once the review was opened, none: %s", body)
	}
}

// What a review page reads its notes with marks them seen; nothing else it asks for does.
func TestReadingTheNotesIsSeeingThem(t *testing.T) {
	for _, tc := range []struct {
		method, path, repo string
		number             int
	}{
		{"GET", "o/app/12/api/review", "o/app", 12},
		{"POST", "o/app/12/api/review", "", 0},
		{"GET", "o/app/12/", "", 0},
		{"GET", "o/app/12/api/diff/abc1234", "", 0},
		{"GET", "o/app/x/api/review", "", 0},
	} {
		repo, n, ok := readsNotes(tc.method, tc.path)
		if ok != (tc.repo != "") || repo != tc.repo || n != tc.number {
			t.Errorf("%s %s: %q %d %v", tc.method, tc.path, repo, n, ok)
		}
	}
}

// A result does not end the organizer's shift: a background job that ends after the agent's
// answer wakes claude again. Its run shows running while the organizer runs it.
func TestATranscriptStillRunsAfterAResult(t *testing.T) {
	root, srv := team(t)
	tm, _ := core.Open(root, "rota.conf")
	start := time.Now().In(tm.Zone.Location)
	path := filepath.Join(root, "logs", start.Format("2006-01-02"), start.Format("1504")+"-beta.jsonl")
	write(t, path, `{"type":"result","subtype":"success","is_error":false,"result":"first report","num_turns":161}`+"\n")
	id := filepath.Base(filepath.Dir(path)) + "/" + strings.TrimSuffix(filepath.Base(path), ".jsonl")
	var run struct {
		Status  string
		Running bool
	}
	json.Unmarshal([]byte(get(t, srv, "/api/run?id="+url.QueryEscape(id))), &run)
	if run.Running || run.Status != "unfinished" {
		t.Fatalf("no organizer runs it, so its result ended it: %+v", run)
	}
	b, _ := json.Marshal(core.Status{Time: time.Now(), Pid: os.Getpid(), StartedAt: time.Now(), Running: &core.Running{Agent: "beta", Host: "local", Since: start}})
	write(t, filepath.Join(root, ".state.json"), string(b))
	json.Unmarshal([]byte(get(t, srv, "/api/run?id="+url.QueryEscape(id))), &run)
	if !run.Running || run.Status != "running" {
		t.Fatalf("the organizer still runs it after its result: %+v", run)
	}
}

// While the organizer is on hold, the app says so: the team's page, and the top of the mail.
func TestEveryPageSaysTheTeamIsOnHold(t *testing.T) {
	root, srv := team(t)
	conf, _ := os.ReadFile(filepath.Join(root, "rota.conf"))
	write(t, filepath.Join(root, "rota.conf"), strings.Replace(string(conf), "usage: off", "usage: on", 1))
	b, _ := json.Marshal(core.Status{Time: time.Now(), Pid: os.Getpid(), StartedAt: time.Now(), UsageError: "usage endpoint: HTTP 401: token expired"})
	write(t, filepath.Join(root, ".state.json"), string(b))
	if body := get(t, srv, "/api/team"); !strings.Contains(body, `"title":"The team is on hold"`) || !strings.Contains(body, `"state":"held"`) {
		t.Fatalf("the team's page says it is on hold: %s", body)
	}
	if body := get(t, srv, "/api/mail"); !strings.Contains(body, `"State":"On hold"`) {
		t.Fatalf("the mail's top says it: %s", body)
	}
}

// The app at / is the team's mail: it runs only its own script, reads /api/mail and acts through
// /api/act, which needs the page's token. The mail has the agents' questions and the pull requests
// that wait for the human, in the order the triage gave, with what the human did to them.
func TestTheAppReadsAndActsOnTheMail(t *testing.T) {
	root, srv := team(t)
	conf, _ := os.ReadFile(filepath.Join(root, "rota.conf"))
	write(t, filepath.Join(root, "rota.conf"), string(conf)+"review_label: rota:needs-review\n")
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: true\nweight: 3\nrepos: o/app\n---\nAlpha fixes **bugs**.\n")
	write(t, filepath.Join(root, "reports", time.Now().Format("2006-01-02")+".md"), "# Today\n\nNothing reached users <today>.\n")
	write(t, filepath.Join(root, "questions", "20261108-0800-alpha-ab12.json"), `{"agent":"alpha","asked":"2026-11-08T08:00:00Z","text":"Is the QA stack's .env done?"}`)
	write(t, filepath.Join(root, "questions", "20261108-0900-alpha-cd34.json"), `{"agent":"alpha","asked":"2026-11-08T09:00:00Z","text":"Roll it out.","subject":"Finish the rollout",`+
		`"steps":["Add <b>STUDIO_PLAY_HOST</b> to `+"`.env`"+`","Restart"],"options":["Done","Not yet"],"recommend":"Done"}`)
	asks := `"labels":["rota:needs-review"]`
	write(t, filepath.Join(root, ".open.json"), `{"time":"`+time.Now().Add(time.Hour).Format(time.RFC3339)+`","repos":{"o/app":{`+
		`"1":{"title":"asks for review","pr":true,"head":"alpha/one","add":5,"del":1,"files":2,`+asks+`},`+
		`"3":{"title":"not asked yet","pr":true,"labels":["UI"],"issues":["o/app#7"]},`+
		`"7":{"title":"an issue it is for","labels":["priority:high"]},`+
		`"5":{"title":"<b>second</b> in the order","pr":true,`+asks+`},`+
		`"13":{"title":"you wait on its agent","pr":true,"labels":["rota:needs-review"],"talk":{"waiting":1}}}}}`)
	write(t, filepath.Join(root, ".queue.json"), `{"by":"alpha","time":"2026-11-08T07:30:00Z","prs":[{"ref":"o/app#5","why":"it unblocks the release"},{"ref":"o/app#1","why":"small"}]}`)

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("the app runs only its own script: %q", csp)
	}
	var m struct {
		Threads []struct {
			ID, Kind, Box, Subject, Label, Review, URL, QID string
			Rank                                            int
			Unread                                          bool
			PR                                              *struct {
				State, Author, Why string
				Add                int
				Labels             []string
				Marks              []struct{ Emoji string }
			}
			Msgs []struct {
				From, HTML, Plain, Recommend string
				Steps, Options               []string
			}
		}
		Members []struct{ Name, State string }
		Top     struct{ State string }
	}
	if err := json.Unmarshal([]byte(get(t, srv, "/api/mail")), &m); err != nil {
		t.Fatal(err)
	}
	byID := map[string]int{}
	for i, th := range m.Threads {
		byID[th.ID] = i
	}
	q, one, five, waits := m.Threads[byID["q:20261108-0800-alpha-ab12"]], m.Threads[byID["pr:o/app#1"]], m.Threads[byID["pr:o/app#5"]], m.Threads[byID["pr:o/app#13"]]
	if q.Box != "inbox" || q.QID != "20261108-0800-alpha-ab12" || q.Msgs[0].From != "alpha" || !strings.Contains(q.Msgs[0].HTML, "Is the QA stack&#39;s .env done?") {
		t.Fatalf("the question: %+v", q)
	}
	todo := m.Threads[byID["q:20261108-0900-alpha-cd34"]]
	if todo.Kind != "do" || todo.Subject != "Finish the rollout" || len(todo.Msgs) != 1 || todo.Msgs[0].Recommend != "Done" || len(todo.Msgs[0].Options) != 2 ||
		todo.Msgs[0].Steps[0] != "Add &lt;b&gt;STUDIO_PLAY_HOST&lt;/b&gt; to <code>.env</code>" {
		t.Fatalf("a to-do, its steps escaped: %+v", todo)
	}
	if one.Box != "inbox" || one.PR.State != "review" || one.PR.Author != "alpha" || one.PR.Add != 5 || one.Review != "/review/o/app/1/" || one.URL != "https://github.com/o/app/pull/1" || one.Label != "app#1" {
		t.Fatalf("a pull request ready for review: %+v %+v", one, one.PR)
	}
	if five.Subject != "<b>second</b> in the order" || five.PR.Why != "it unblocks the release" || len(five.Msgs) != 1 || five.Msgs[0].From == "triage" || !(q.Rank < five.Rank && five.Rank < one.Rank) {
		t.Fatalf("the question first, then the pull requests in the triage's order: %+v", five)
	}
	if three := m.Threads[byID["pr:o/app#3"]].PR; !slices.Equal(three.Labels, []string{"UI", "priority:high"}) || len(waits.PR.Marks) != 1 || waits.PR.Marks[0].Emoji != core.WaitsMark {
		t.Fatalf("what narrows Reviews: its own labels, those of the issue it is for, and its marks: %+v %+v", three, waits.PR)
	}
	if waits.Box != "waiting" || m.Threads[byID["pr:o/app#3"]].Box != "open" {
		t.Fatalf("one waits on its agent, one is not asked yet: %s %s", waits.Box, m.Threads[byID["pr:o/app#3"]].Box)
	}
	report := m.Threads[byID["d:"+time.Now().Format("2006-01-02")]]
	if report.Box != "updates" || !strings.Contains(report.Msgs[0].HTML, "Nothing reached users &lt;today&gt;.") {
		t.Fatalf("today's report is an update, its Markdown escaped: %+v", report)
	}
	if push := get(t, srv, "/api/push"); !strings.Contains(push, `"key":"`) {
		t.Fatalf("the key a device needs to hear of the team: %s", push)
	}
	if len(m.Members) != 1 || m.Members[0].Name != "alpha" || m.Members[0].State != "needs" || m.Top.State != "Not running" {
		t.Fatalf("the team: %+v %+v", m.Members, m.Top)
	}

	resp, err = http.Get(srv.URL + "/api/mail") // the same mail again is not sent again
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/api/mail", nil)
	req.Header.Set("If-None-Match", resp.Header.Get("ETag"))
	again, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	again.Body.Close()
	if again.StatusCode != http.StatusNotModified {
		t.Fatalf("the same mail is sent again: %d", again.StatusCode)
	}
	token := regexp.MustCompile(`data-token="([0-9a-f]+)"`).FindStringSubmatch(get(t, srv, "/"))[1]
	act := func(form url.Values) (int, map[string]string) {
		resp, err := http.PostForm(srv.URL+"/api/act", form)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]string
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	if code, _ := act(url.Values{"action": {"star"}, "id": {"pr:o/app#1"}}); code != http.StatusForbidden {
		t.Fatalf("without the page's token: %d", code)
	}
	if code, out := act(url.Values{"token": {token}, "action": {"star"}, "id": {"pr:o/app#1"}}); code != 200 {
		t.Fatalf("star: %d %v", code, out)
	}
	if code, out := act(url.Values{"token": {token}, "action": {"answer"}, "id": {"q:20261108-0800-alpha-ab12"}, "text": {"Yes, done."}}); code != 200 || !strings.Contains(out["said"], "alpha") {
		t.Fatalf("answer: %d %v", code, out)
	}
	if code, out := act(url.Values{"token": {token}, "action": {"note"}, "arg": {"alpha"}, "about": {"#1"}, "text": {"Rebase it, please."}}); code != 200 || !strings.Contains(out["said"], "alpha") {
		t.Fatalf("a note: %d %v", code, out)
	}
	if code, out := act(url.Values{"token": {token}, "action": {"explode"}, "id": {"pr:o/app#1"}}); code != 400 || out["error"] == "" {
		t.Fatalf("nonsense: %d %v", code, out)
	}
	if code, _ := act(url.Values{"token": {token}, "action": {"prefs"}, "arg": {`{"split":false,"focus":3,"after":"list","notify":"all","chat":"off"}`}}); code != 200 {
		t.Fatalf("prefs: %d", code)
	}
	if code, _ := act(url.Values{"token": {token}, "action": {"prefs"}, "arg": {`{"split":false,"focus":3,"after":"list","notify":"all","chat":"loud"}`}}); code != 400 {
		t.Fatal("the chat's notifications are all or off")
	}
	for _, change := range []string{`{"unread":true,"from":"beta"}`, `{"from":"alpha"}`} { // only what changes: unread stays
		if code, out := act(url.Values{"token": {token}, "action": {"filters"}, "id": {"starred"}, "arg": {change}}); code != 200 {
			t.Fatalf("what narrows Starred: %d %v", code, out)
		}
	}
	for _, bad := range [][2]string{{"reviews", `{"unread":true}`}, {"starred", `null`}, {"starred", `{"unread":"yes"}`}} {
		if code, _ := act(url.Values{"token": {token}, "action": {"filters"}, "id": {bad[0]}, "arg": {bad[1]}}); code != 400 {
			t.Fatalf("%s %s is kept", bad[0], bad[1])
		}
	}
	var mf struct {
		Filters map[string]core.MailFilter `json:"filters"`
	}
	if err := json.Unmarshal([]byte(get(t, srv, "/api/mail")), &mf); err != nil || mf.Filters["starred"] != (core.MailFilter{Unread: true, From: "alpha"}) {
		t.Fatalf("the mail does not bring Starred's filters back: %v %+v", err, mf.Filters)
	}
	if code, out := act(url.Values{"token": {token}, "action": {"list"}, "id": {"issues"}, "repo": {"o/app"}, "order": {"oldest"}}); code != 200 {
		t.Fatalf("what narrows Issues: %d %v", code, out)
	}
	if code, _ := act(url.Values{"token": {token}, "action": {"list"}, "id": {"issues"}, "order": {"loudest"}}); code != 400 {
		t.Fatal("an order that is not one is kept")
	}
	if code, _ := act(url.Values{"token": {token}, "action": {"list"}, "id": {"reviews"}, "order": {"oldest"}}); code != 400 {
		t.Fatal("Reviews has an order of its own")
	}
	// Reviews: each change is only what it names, so what the console chose meanwhile stays
	tm0, _ := core.Open(root, "rota.conf")
	tm0.SetListFilter(core.ReviewsTab, core.ListFilter{Repo: "o/app", Mark: core.WaitsMark})
	for _, step := range []struct {
		form url.Values
		want core.ListFilter
	}{
		{url.Values{"add": {"priority:high"}}, core.ListFilter{Repo: "o/app", Mark: core.WaitsMark, Labels: []string{"priority:high"}}},
		{url.Values{"add": {" UI "}}, core.ListFilter{Repo: "o/app", Mark: core.WaitsMark, Labels: []string{"priority:high", "UI"}}},
		{url.Values{"add": {"ui"}}, core.ListFilter{Repo: "o/app", Mark: core.WaitsMark, Labels: []string{"priority:high", "UI"}}},
		{url.Values{"drop": {"PRIORITY:HIGH"}}, core.ListFilter{Repo: "o/app", Mark: core.WaitsMark, Labels: []string{"UI"}}},
		{url.Values{"mark": {""}}, core.ListFilter{Repo: "o/app", Labels: []string{"UI"}}},
		{url.Values{"repo": {"o/web"}}, core.ListFilter{Repo: "o/web", Labels: []string{"UI"}}},
		{url.Values{"clear": {"1"}}, core.ListFilter{}},
	} {
		step.form.Set("token", token)
		step.form.Set("action", "list")
		step.form.Set("id", "reviews")
		if code, out := act(step.form); code != 200 {
			t.Fatalf("%v: %d %v", step.form, code, out)
		}
		if f := tm0.ListFilter(core.ReviewsTab); !reflect.DeepEqual(f, step.want) {
			t.Fatalf("%v: %+v, not %+v", step.form, f, step.want)
		}
	}
	if code, out := act(url.Values{"token": {token}, "action": {"approve"}, "id": {"q:20261108-0800-alpha-ab12"}}); code != 400 || !strings.Contains(out["error"], "not a pull request") {
		t.Fatalf("approving what is not a pull request: %d %v", code, out)
	}
	tm, _ := core.Open(root, "rota.conf")
	if n := tm.Notes("alpha"); len(n) != 2 || n[1].About != "o/app#1" || !strings.Contains(n[0].Text, "Yes, done.") {
		t.Fatalf("the answer and the note reach alpha: %+v", n)
	}
	if f := tm.ListFilter(core.IssuesTab); f.Repo != "o/app" || f.Order != "oldest" {
		t.Fatalf("Issues keeps what narrows it: %+v", f)
	}
	var lists struct{ Lists map[string]core.ListFilter }
	json.Unmarshal([]byte(get(t, srv, "/api/mail")), &lists)
	if lists.Lists["issues"].Repo != "o/app" {
		t.Fatalf("the app reads what narrows its lists: %+v", lists)
	}
	if body := get(t, srv, "/api/issues"); !strings.HasPrefix(body, "[") {
		t.Fatalf("the open issues: %s", body)
	}
	mail := tm.Mail(time.Now())
	for _, th := range mail.Threads {
		if th.ID == "pr:o/app#1" && !th.Star || th.ID == "q:20261108-0800-alpha-ab12" && th.Box != "done" {
			t.Fatalf("what the human did is kept: %+v", th)
		}
	}
	if mail.Prefs.Focus != 3 || mail.Prefs.Split {
		t.Fatalf("prefs: %+v", mail.Prefs)
	}
}

// The app controls the team through /api/act with the page's token: a pause and a resume, run now
// (which says why it cannot while no organizer runs), and the team's settings, of which a bad value
// saves nothing. No other site may frame the pages.
func TestThePagesControlTheTeam(t *testing.T) {
	root, srv := team(t)
	token := regexp.MustCompile(`data-token="([0-9a-f]+)"`).FindStringSubmatch(get(t, srv, "/"))[1]
	act := func(form url.Values) (int, string) {
		form.Set("token", token)
		resp, err := http.PostForm(srv.URL+"/api/act", form)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]string
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out["said"] + out["error"]
	}
	tm, _ := core.Open(root, "rota.conf")
	write(t, filepath.Join(root, "questions", "20261029-1800-alpha-cd34.json"), `{"agent":"alpha","asked":"2026-10-29T18:00:00Z","text":"Keep the old banner?"}`)
	if code, said := act(url.Values{"action": {"dismiss"}, "id": {"q:20261029-1800-alpha-cd34"}}); code != 200 || !strings.Contains(said, "alpha's question dismissed") {
		t.Fatalf("a question is dismissed: %d %s", code, said)
	}
	if len(tm.Questions()) != 0 || !strings.Contains(notesOf(tm, "alpha"), `dismissed your question "Keep the old banner?"`) {
		t.Fatal("the dismissed question is closed, and alpha's inbox says so")
	}
	if code, said := act(url.Values{"action": {"pause"}}); code != 200 || !tm.Paused() || !strings.HasPrefix(said, "team paused") {
		t.Fatalf("the team is paused: %d %s", code, said)
	}
	if code, _ := act(url.Values{"action": {"resume"}}); code != 200 || tm.Paused() {
		t.Fatal("and resumed")
	}
	if _, said := act(url.Values{"action": {"run"}, "arg": {"alpha"}}); !strings.Contains(said, "organizer is not running") {
		t.Fatalf("run now says why it cannot: %q", said)
	}
	if code, said := act(url.Values{"action": {"team-set"}, "text": {`{"team_pct":"15"}`}}); code != 200 || !strings.Contains(said, "within seconds") {
		t.Fatalf("a team setting: %d %s", code, said)
	}
	if code, _ := act(url.Values{"action": {"team-set"}, "text": {`{"team_pct":"10","tick_minutes":"zero"}`}}); code != 400 {
		t.Fatalf("a bad value is refused: %d", code)
	}
	if tm, _ := core.Open(root, "rota.conf"); tm.TeamPct != 15 {
		t.Fatalf("and saves nothing: %v", tm.TeamPct)
	}
	resp, _ := http.PostForm(srv.URL+"/api/act", url.Values{"token": {"wrong"}, "action": {"pause"}})
	if resp.Body.Close(); resp.StatusCode != 403 || tm.Paused() {
		t.Fatal("a post without the token is refused")
	}
	if resp, _ := http.Get(srv.URL + "/"); resp.Header.Get("X-Frame-Options") != "DENY" || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatal("no other site may frame the pages")
	}
}

// Settings › Team › Repositories edits the team's repositories, default_repos: one is added as GitHub
// names it once GitHub says it exists, where Undo puts it back, and one is removed; one mistyped, one
// GitHub does not have and one there already are refused, and save nothing. The page names the agents
// with a list of their own, which the team's does not reach.
func TestTheTeamsRepositoriesAreEditedInSettings(t *testing.T) {
	root, srv := team(t)
	conf := filepath.Join(root, "rota.conf")
	write(t, conf, "usage: off\nlisten: off\ncommit: false\ndefault_repos: o/app, o/web       # owner/name, comma separated\n")
	write(t, filepath.Join(root, "agents", "beta.md"), "---\nrepos: o/own\n---\nBeta.\n")
	bin, log := t.TempDir(), filepath.Join(root, "gh.log")
	write(t, filepath.Join(bin, "gh"), "#!/bin/bash\necho \"$*\" >> "+log+"\n"+`case "$3" in
o/new) echo '{"nameWithOwner":"o/New"}' ;;
o/app) echo '{"nameWithOwner":"o/app"}' ;;
*) echo "GraphQL: Could not resolve to a Repository with the name '$3'. (repository)" >&2; exit 1 ;;
esac`)
	os.Chmod(filepath.Join(bin, "gh"), 0o755)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	token := regexp.MustCompile(`data-token="([0-9a-f]+)"`).FindStringSubmatch(get(t, srv, "/"))[1]
	act := func(action, arg, text string) (int, string) {
		resp, err := http.PostForm(srv.URL+"/api/act", url.Values{"token": {token}, "action": {action}, "arg": {arg}, "text": {text}})
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]string
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out["said"] + out["error"]
	}
	type own struct {
		Name  string
		On    bool
		Repos []string
	}
	settings := func() (repos []string, owns []own) {
		var s struct {
			Repos []string
			Own   []own
		}
		json.Unmarshal([]byte(get(t, srv, "/api/settings")), &s)
		return s.Repos, s.Own
	}
	repos, owns := settings()
	if strings.Join(repos, " ") != "o/app o/web" || len(owns) != 1 || owns[0].Name != "beta" || owns[0].On || strings.Join(owns[0].Repos, " ") != "o/own" {
		t.Fatalf("the team's repositories, and beta with its own: %v %+v", repos, owns)
	}
	if code, said := act("repo-add", "https://github.com/o/new.git", ""); code != 200 || said != "added o/New: every agent but beta works on it from its next run" {
		t.Fatalf("a link adds the repository, as GitHub names it: %d %s", code, said)
	}
	for _, bad := range []struct{ repo, says string }{{"o/NEW", "already"}, {"o/nope", "GitHub has no repository o/nope"}, {"not a repo", "not a repository"}, {"--web", "not a repository"}, {"o/..", "not a repository"}} {
		if code, said := act("repo-add", bad.repo, ""); code != 400 || !strings.Contains(said, bad.says) {
			t.Fatalf("%s is refused: %d %s", bad.repo, code, said)
		}
	}
	if b, _ := os.ReadFile(log); strings.Count(string(b), "repo view ") != 2 {
		t.Fatalf("GitHub is asked only about a repository not there yet:\n%s", b)
	}
	if code, said := act("repo-remove", "O/APP", ""); code != 200 || said != "removed o/app" {
		t.Fatalf("a repository is removed: %d %s", code, said)
	}
	if code, _ := act("repo-remove", "o/gone", ""); code != 400 {
		t.Fatal("one not there is not removed")
	}
	if code, _ := act("repo-add", "o/app", "0"); code != 200 {
		t.Fatal("Undo adds it back")
	}
	if b, _ := os.ReadFile(conf); !strings.Contains(string(b), "default_repos: o/app, o/web, o/New # owner/name, comma separated\n") {
		t.Fatalf("where it was, the line's comment kept:\n%s", b)
	}
	if code, _ := act("agent-set", "beta", `{"repos":""}`); code != 200 {
		t.Fatal("beta takes the team's")
	}
	if tm, _ := core.Open(root, "rota.conf"); strings.Join(tm.Agent("beta").Repos, " ") != "o/app o/web o/New" {
		t.Fatalf("beta works on the team's: %v", tm.Agent("beta").Repos)
	}
	if _, owns := settings(); len(owns) != 0 {
		t.Fatalf("no agent has its own: %+v", owns)
	}
}

// What an agent writes for the human in reports/<agent>/ is listed on its page, latest changed
// first, and opens rendered, with who wrote it; nothing else is served from there.
func TestAnAgentsReportsAreListedOnItsPage(t *testing.T) {
	root, srv := team(t)
	write(t, filepath.Join(root, "reports", "alpha", "old.md"), "# Old\n")
	write(t, filepath.Join(root, "reports", "alpha", "rollouts.md"), "# Rollouts\n\n- backend#69: run step 1\n")
	write(t, filepath.Join(root, "reports", "alpha", ".draft.md"), "not yet")
	write(t, filepath.Join(root, "reports", "alpha", "release notes é.md"), "# Notes\n")
	old := time.Now().Add(-time.Hour)
	os.Chtimes(filepath.Join(root, "reports", "alpha", "old.md"), old, old)
	var page struct{ Reports []struct{ Name, URL string } }
	json.Unmarshal([]byte(get(t, srv, "/api/agent?name=alpha")), &page)
	var names []string
	for _, r := range page.Reports {
		names = append(names, r.Name)
	}
	if len(names) != 3 || names[len(names)-1] != "old.md" || slices.Contains(names, ".draft.md") || page.Reports[0].URL == "" {
		t.Fatalf("its page lists its reports, latest changed first, and no hidden file: %+v", page.Reports)
	}
	if body := get(t, srv, "/reports/alpha/rollouts.md"); !strings.Contains(body, "<h1>Rollouts</h1>") || !strings.Contains(body, `href="/#agent:alpha"`) {
		t.Fatalf("a report opens rendered, with who wrote it:\n%s", body)
	}
	if body := get(t, srv, "/reports/alpha/release%20notes%20%C3%A9.md"); !strings.Contains(body, "<h1>Notes</h1>") {
		t.Fatal("a name with a space and an accent opens too")
	}
	if body := get(t, srv, "/reports/"); strings.Contains(body, "Rollouts") {
		t.Fatal("the human's daily reports stay apart")
	}
	for _, path := range []string{"/reports/alpha/.draft.md", "/reports/alpha/../rota.conf", "/reports/nobody/rollouts.md", "/reports/alpha/missing.md"} {
		resp, _ := http.Get(srv.URL + path)
		if resp.Body.Close(); resp.StatusCode == 200 {
			t.Fatalf("%s is not served", path)
		}
	}
}

// The pages review only the team's repositories' pull requests, though gh's login reaches more.
func TestReviewsAreOnlyTheTeamsRepositories(t *testing.T) {
	t.Setenv("GH_TOKEN", "test") // no gh call, and nothing reaches GitHub here
	root, srv := team(t)
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: true\nrepos: o/app\n---\nAlpha fixes **bugs**.\n")
	if !strings.Contains(get(t, srv, "/review/app.js"), "function") {
		t.Fatal("gloss's script is served")
	}
	if page := get(t, srv, "/review/o/app/1/"); !strings.Contains(page, `<script src="/voice.js?v=`) || !strings.Contains(page, ` data-token="`) { // the team's: gloss's page, which loads the pull request itself
		t.Fatalf("a review page reads aloud and takes dictation as the app does:\n%s", page)
	}
	get(t, srv, "/review/O/App/1/")
	for _, path := range []string{"/review/other/secret/1/", "/review/other/secret/1/api/review", "/review/o/", "/review/o/apps/1/"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s is not the team's: HTTP %d", path, resp.StatusCode)
		}
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(srv.URL + "/review/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); resp.StatusCode != http.StatusFound || loc != "/#reviews" {
		t.Fatalf("/review/ is the Reviews folder: %d %s", resp.StatusCode, loc)
	}
}

func TestReportsAndEvidence(t *testing.T) {
	root, srv := team(t)
	write(t, filepath.Join(root, "reports", "2026-10-29.md"), "# What changed\n\n- backend#63, merged by the triage\n")
	write(t, filepath.Join(root, "evidence", "app", "developer", "1-fix", "after.png"), "\x89PNG")
	write(t, filepath.Join(root, "evidence", "app", "developer", "1-fix", "page.html"), "<script>steal()</script>")
	write(t, filepath.Join(root, "evidence", "app", "developer", "1-fix", "review.md"), "# Review\n\nnothing open")
	if body := get(t, srv, "/reports/"); !strings.Contains(body, "merged by the triage") {
		t.Fatalf("the report is shown:\n%s", body)
	}
	resp, _ := http.Get(srv.URL + "/evidence/app/developer/1-fix/after.png")
	if resp.Body.Close(); resp.Header.Get("Content-Type") != "image/png" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("an image is served as one: %v", resp.Header)
	}
	if body := get(t, srv, "/evidence/app/developer/1-fix/page.html"); strings.Contains(body, "<script>steal") || !strings.Contains(body, "&lt;script&gt;steal") {
		t.Fatal("an html file is only text")
	}
	if body := get(t, srv, "/evidence/app/developer/1-fix"); !strings.Contains(body, `href="/evidence/app/developer/1-fix/review.md"`) {
		t.Fatalf("a folder lists its files:\n%s", body)
	}
	if body := get(t, srv, "/evidence/app/developer/1-fix/review.md"); !strings.Contains(body, "<h1>Review</h1>") {
		t.Fatal("markdown is rendered")
	}
	resp, _ = http.Get(srv.URL + "/evidence/../secrets")
	if resp.Body.Close(); resp.StatusCode == 200 {
		t.Fatal("nothing outside evidence/")
	}
}

// The team's page leads to a proposal of new weights from how each agent used the pool; the human
// changes any of them and applies them, with the page's token.
func TestTheSharesAreRebalancedFromSpend(t *testing.T) {
	root, srv := team(t)
	write(t, filepath.Join(root, "agents", "beta.md"), "---\nenabled: true\nweight: 1\n---\nBeta.\n")
	var p struct {
		Agents []struct {
			Name     string
			Proposed float64
			Why      string
		}
	}
	json.Unmarshal([]byte(get(t, srv, "/api/shares")), &p)
	if len(p.Agents) != 2 || p.Agents[0].Name != "alpha" || p.Agents[0].Proposed != 75 || p.Agents[1].Proposed != 25 || !strings.Contains(p.Agents[0].Why+p.Agents[1].Why, "too few to judge") {
		t.Fatalf("the proposal: %+v", p)
	}
	token := regexp.MustCompile(`data-token="([0-9a-f]+)"`).FindStringSubmatch(get(t, srv, "/"))[1]
	act := func(tok, weights string) int {
		resp, err := http.PostForm(srv.URL+"/api/act", url.Values{"token": {tok}, "action": {"shares-apply"}, "text": {weights}})
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := act("wrong", `{"alpha":1}`); code != 403 {
		t.Fatalf("a post without the token is refused: %d", code)
	}
	for _, bad := range []string{`{"alpha":"many","beta":2}`, `{"alpha":null,"beta":25}`, `{"alpha":-1,"beta":25}`} {
		if code := act(token, bad); code != 400 {
			t.Fatalf("%s saves nothing: %d", bad, code)
		}
	}
	if code := act(token, `{"alpha":60,"beta":40}`); code != 200 {
		t.Fatalf("applied: %d", code)
	}
	if tm, _ := core.Open(root, "rota.conf"); tm.Agent("alpha").Weight != 60 || tm.Agent("beta").Weight != 40 {
		t.Fatalf("the weights are written: %v %v", tm.Agent("alpha").Weight, tm.Agent("beta").Weight)
	}
}

// The Issues folder lists the open issues, newest first, each with the pull requests for it; the
// order chosen is kept until it is changed, and one it does not know is refused.
func TestTheIssuesPageListsAndOrdersTheOpenIssues(t *testing.T) {
	root, srv := team(t)
	write(t, filepath.Join(root, ".open.json"), `{"time":"`+time.Now().Format(time.RFC3339)+`","repos":{`+
		`"o/app":{"1":{"title":"the oldest","labels":["P1"],"created":"2026-11-01T09:00:00Z"},"2":{"title":"the newest","created":"2026-11-05T09:00:00Z"},`+
		`"3":{"title":"a fix","pr":true,"issues":["o/app#1"]}}}}`)
	read := func() []struct {
		Number int
		URL    string
		PRs    []string
	} {
		var out []struct {
			Number int
			URL    string
			PRs    []string
		}
		json.Unmarshal([]byte(get(t, srv, "/api/issues")), &out)
		return out
	}
	is := read()
	if len(is) != 2 || is[0].Number != 2 || is[1].Number != 1 || is[1].URL != "https://github.com/o/app/issues/1" || len(is[1].PRs) != 1 || is[1].PRs[0] != "pr:o/app#3" {
		t.Fatalf("issues only, newest first, each with its pull requests: %+v", is)
	}
	token := regexp.MustCompile(`data-token="([0-9a-f]+)"`).FindStringSubmatch(get(t, srv, "/"))[1]
	order := func(o string) int {
		resp, err := http.PostForm(srv.URL+"/api/act", url.Values{"token": {token}, "action": {"list"}, "id": {"issues"}, "order": {o}})
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := order("oldest"); code != 200 || read()[0].Number != 1 {
		t.Fatalf("oldest first: %d", code)
	}
	if code := order("bogus"); code != 400 {
		t.Fatalf("an order it does not know: %d", code)
	}
	tm, _ := core.Open(root, "rota.conf")
	if f := tm.ListFilter(core.IssuesTab); f.Order != "oldest" {
		t.Fatalf("the order chosen is kept: %+v", f)
	}
}

// An issue opens in the reading pane as GitHub has it now: what it says and its comments, drawn as
// the agents' Markdown is, everything escaped, GitHub's HTML comments left out and a picture a link
// to it. Only the team's repositories are asked for; GitHub not answering says so.
func TestAnIssueReadsAsGitHubHasIt(t *testing.T) {
	root, srv := team(t)
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: true\nrepos: o/app\n---\nAlpha fixes **bugs**.\n")
	bin, log := t.TempDir(), filepath.Join(root, "gh.log")
	write(t, filepath.Join(bin, "gh"), "#!/bin/bash\necho \"$*\" >> "+log+"\n"+`case "$3 $5" in
"7 o/app") cat <<'EOF'
{"title":"Dead records <b>stay</b>","state":"CLOSED","stateReason":"NOT_PLANNED","author":{"login":"someone"},"createdAt":"2026-11-05T15:00:26Z",
 "body":"## Why\n\nIt **stays**. <script>alert(1)</script>\n\n<img width=\"10\" alt=\"x\" src=\"https://github.com/user-attachments/assets/a.png\" />",
 "labels":[{"name":"priority:high"}],
 "comments":[{"author":{"login":"someone"},"createdAt":"2026-11-06T10:00:00Z","body":"<!-- gloss {\"author\":\"developer\"} -->\n**developer**\n\nA third one."}]}
EOF
;;
"8 o/app") echo '{"title":"open","state":"OPEN","stateReason":"REOPENED","author":{"login":"x"},"createdAt":"2026-11-05T15:00:26Z","body":"","labels":[],"comments":[]}' ;;
*) echo "GraphQL: Could not resolve to an issue" >&2; exit 1 ;;
esac`)
	os.Chmod(filepath.Join(bin, "gh"), 0o755)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	var is struct {
		Title, State, Reason, Author, HTML string
		Labels                             []string
		Comments                           []struct{ Author, HTML string }
	}
	if err := json.Unmarshal([]byte(get(t, srv, "/api/issue?repo=o/app&n=7")), &is); err != nil {
		t.Fatal(err)
	}
	if is.Title != "Dead records <b>stay</b>" || is.State != "CLOSED" || is.Reason != "NOT_PLANNED" || is.Author != "someone" || !slices.Equal(is.Labels, []string{"priority:high"}) {
		t.Fatalf("the issue as GitHub has it: %+v", is)
	}
	if !strings.Contains(is.HTML, "<h2>Why</h2>") || !strings.Contains(is.HTML, "<b>stays</b>") || !strings.Contains(is.HTML, "&lt;script&gt;alert(1)&lt;/script&gt;") ||
		!strings.Contains(is.HTML, `<a href="https://github.com/user-attachments/assets/a.png">picture</a>`) || strings.Contains(is.HTML, "<img") || strings.Contains(is.HTML, "<script") {
		t.Fatalf("what it says, drawn, everything escaped, its picture a link: %s", is.HTML)
	}
	if len(is.Comments) != 1 || strings.Contains(is.Comments[0].HTML, "gloss") || !strings.Contains(is.Comments[0].HTML, "<b>developer</b>") {
		t.Fatalf("its comment, without GitHub's hidden marker: %+v", is.Comments)
	}
	var open struct{ State, Reason string }
	json.Unmarshal([]byte(get(t, srv, "/api/issue?repo=O/App&n=8")), &open) // GitHub's names ignore case
	if open.State != "OPEN" || open.Reason != "" {
		t.Fatalf("an open issue has no reason it was closed: %+v", open)
	}
	status := func(path string) int {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	asked, _ := os.ReadFile(log)
	for _, path := range []string{"/api/issue?repo=someone/else&n=7", "/api/issue?repo=o/app&n=x", "/api/issue?repo=o/app&n=-1", "/api/issue?n=7"} {
		if code := status(path); code != 404 {
			t.Fatalf("%s: %d, not 404", path, code)
		}
	}
	if after, _ := os.ReadFile(log); len(after) != len(asked) {
		t.Fatalf("GitHub is asked only about the team's repositories:\n%s", after)
	}
	if code := status("/api/issue?repo=o/app&n=9"); code != 502 {
		t.Fatalf("GitHub did not answer: %d", code)
	}
}

// The app's team pages: the team with its agents; an agent with its runs, its role and every
// setting; a run with its report and transcript. The human changes an agent's settings and role,
// pauses and resumes it, from the page, with its token; what is not a setting, an agent or a run
// is refused.
func TestTheTeamsPagesInTheApp(t *testing.T) {
	root, srv := team(t) // alpha ran a shift
	var tm struct {
		Title  string
		Agents []struct {
			Name, Schedule string
			Paused         bool
		}
	}
	json.Unmarshal([]byte(get(t, srv, "/api/team")), &tm)
	if tm.Title != "The team is not running" || len(tm.Agents) != 1 || tm.Agents[0].Name != "alpha" || tm.Agents[0].Schedule != "Every 20h" {
		t.Fatalf("the team: %+v", tm)
	}
	type agentPage struct {
		Name, Role, RoleHTML string
		Paused               bool
		RunList              []struct {
			ID, Status, Title string
			Kept              bool
		}
		Fields []struct{ Key, Kind, Value, Effective string }
	}
	agent := func() agentPage {
		var a agentPage
		if err := json.Unmarshal([]byte(get(t, srv, "/api/agent?name=alpha")), &a); err != nil {
			t.Fatal(err)
		}
		return a
	}
	a := agent()
	if len(a.RunList) != 1 || a.RunList[0].Status != "ok" || !a.RunList[0].Kept || !strings.Contains(a.RoleHTML, "<b>bugs</b>") || len(a.Fields) != len(core.AgentFields) {
		t.Fatalf("alpha's page: %+v", a)
	}
	var run struct{ ID, Agent, Report, Events string }
	json.Unmarshal([]byte(get(t, srv, "/api/run?id="+url.QueryEscape(a.RunList[0].ID))), &run)
	if run.Agent != "alpha" || !strings.Contains(run.Report, "ran in") || !strings.Contains(run.Events, "<b>Bash</b> <code>ls</code>") {
		t.Fatalf("its run: %+v", run)
	}
	for _, path := range []string{"/api/run?id=..%2F..%2Fetc%2Fpasswd", "/api/run?id=2026-01-01%2F0000-alpha", "/api/agent?name=..%2Falpha", "/api/agent?name=nobody"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %d", path, resp.StatusCode)
		}
	}
	token := regexp.MustCompile(`data-token="([0-9a-f]+)"`).FindStringSubmatch(get(t, srv, "/"))[1]
	act := func(form url.Values) (int, string) {
		form.Set("token", token)
		resp, err := http.PostForm(srv.URL+"/api/act", form)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]string
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out["said"] + out["error"]
	}
	if code, said := act(url.Values{"action": {"agent-set"}, "arg": {"alpha"}, "text": {`{"model":"sonnet","run_minutes":"50"}`}}); code != 200 || !strings.Contains(said, "Saved") {
		t.Fatalf("settings: %d %s", code, said)
	}
	for _, f := range agent().Fields {
		if f.Key == "model" && f.Value != "sonnet" || f.Key == "run_minutes" && f.Value != "50" {
			t.Fatalf("not kept: %+v", f)
		}
	}
	for _, bad := range []url.Values{
		{"action": {"agent-set"}, "arg": {"alpha"}, "text": {`{"no_such_setting":"1"}`}},
		{"action": {"agent-set"}, "arg": {"alpha"}, "text": {`{}`}},
		{"action": {"agent-set"}, "arg": {"nobody"}, "text": {`{"model":"opus"}`}},
		{"action": {"role"}, "arg": {"alpha"}, "text": {"  "}},
		{"action": {"cancel"}, "arg": {"alpha"}},
	} {
		if code, said := act(bad); code != 400 {
			t.Errorf("%v: %d %s", bad, code, said)
		}
	}
	if code, said := act(url.Values{"action": {"role"}, "arg": {"alpha"}, "text": {"Alpha fixes bugs, and tests them."}}); code != 200 || !strings.Contains(said, "Saved") {
		t.Fatalf("role: %d %s", code, said)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "agents", "alpha.md")); !strings.Contains(string(b), "model: sonnet") || !strings.HasSuffix(strings.TrimSpace(string(b)), "Alpha fixes bugs, and tests them.") {
		t.Fatalf("the agent's file: %s", b)
	}
	if code, said := act(url.Values{"action": {"pause"}, "arg": {"alpha"}}); code != 200 || !strings.HasPrefix(said, "alpha paused") || !agent().Paused {
		t.Fatalf("pause: %d %s", code, said)
	}
	if code, said := act(url.Values{"action": {"resume"}, "arg": {"alpha"}}); code != 200 || agent().Paused {
		t.Fatalf("resume: %d %s", code, said)
	}
	// an agent added while the organizer runs, which it has not looked at yet: its page, no runs
	write(t, filepath.Join(root, ".state.json"), fmt.Sprintf(`{"time":%q,"pid":%d,"agents":[{"name":"alpha","enabled":true,"state":"ready"}]}`, time.Now().Format(time.RFC3339), os.Getpid()))
	write(t, filepath.Join(root, "agents", "beta.md"), "---\nenabled: true\n---\nBeta.\n")
	if body := get(t, srv, "/api/agent?name=beta"); !strings.Contains(body, `"name":"beta"`) || !strings.Contains(body, `"runList":[]`) {
		t.Fatalf("a new agent's page: %s", body)
	}
	if body := get(t, srv, "/api/team"); !strings.Contains(body, `"name":"beta"`) {
		t.Fatalf("a new agent on the team's page: %s", body)
	}
}

// The pages follow their settings within seconds: to another port when web_port changes, nowhere
// when web is off; rota web serves them on this machine while web is off. They stop with ctx.
func TestThePagesFollowTheirSettings(t *testing.T) {
	prev := settingsCheck
	t.Cleanup(func() { settingsCheck = prev })
	settingsCheck = 50 * time.Millisecond
	root := t.TempDir()
	free := func() int {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		return ln.Addr().(*net.TCPAddr).Port
	}
	serves := func(port int) bool {
		c := http.Client{Timeout: time.Second}
		resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
		if err == nil {
			resp.Body.Close()
		}
		return err == nil && resp.StatusCode == 200
	}
	conf := func(webMode string, port int) {
		write(t, filepath.Join(root, "rota.conf"), fmt.Sprintf("usage: off\nlisten: off\nweb: %s\nweb_port: %d\n", webMode, port))
		time.Sleep(20 * time.Millisecond) // the next change has another modification time
	}
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: true\n---\nalpha\n")
	until := func(what string, cond func() bool) {
		t.Helper()
		for end := time.Now().Add(5 * time.Second); !cond(); time.Sleep(20 * time.Millisecond) {
			if time.Now().After(end) {
				t.Fatal("timed out: " + what)
			}
		}
	}
	a, b := free(), free()
	conf("local", a)
	var mu sync.Mutex
	var said []string
	say := func(s string) { mu.Lock(); said = append(said, s); mu.Unlock() }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { Follow(ctx, root, "rota.conf", false, say); close(done) }()
	until("the pages on the first port", func() bool { return serves(a) })
	conf("local", b)
	until("on the second port, and not the first", func() bool { return serves(b) && !serves(a) })
	conf("off", b)
	until("nowhere once web is off", func() bool { return !serves(b) && !serves(a) })
	saying := func() []string { mu.Lock(); defer mu.Unlock(); return slices.Clone(said) }
	until("it says so", func() bool { return len(saying()) == 3 })
	if got := saying(); !strings.HasPrefix(got[0], fmt.Sprintf("pages at http://127.0.0.1:%d/", a)) || got[2] != "pages off: web is off" {
		cancel()
		t.Fatalf("it says where the pages are: %q", got)
	}
	cancel()
	<-done
	ctx, cancel = context.WithCancel(context.Background())
	done = make(chan struct{})
	go func() { Follow(ctx, root, "rota.conf", true, say); close(done) }() // rota web
	until("rota web serves them while web is off", func() bool { return serves(b) })
	cancel()
	<-done
	if serves(b) {
		t.Fatal("the pages stop with their context")
	}
}

// Pages that are local leave the tailnet, also when an earlier setting or rota left Tailscale
// serving them: Tailscale is told to stop at once, and again until it confirms.
func TestLocalPagesLeaveTheTailnet(t *testing.T) {
	prevCheck, prevRetry := settingsCheck, offTailnetRetry
	t.Cleanup(func() { settingsCheck, offTailnetRetry = prevCheck, prevRetry })
	settingsCheck, offTailnetRetry = 50*time.Millisecond, 100*time.Millisecond
	root, bin := t.TempDir(), t.TempDir()
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	calls, state, fails := filepath.Join(bin, "calls"), filepath.Join(bin, "served"), filepath.Join(bin, "fail-once")
	write(t, state, fmt.Sprintf(`{"TCP":{"443":{"HTTPS":true}},"Web":{"mac.tail1.ts.net:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:%d"}}}}}`, port)) // as Tailscale answers
	write(t, filepath.Join(bin, "tailscale"), `#!/bin/sh
case "$1" in
status) echo '{"Self":{"DNSName":"mac.tail1.ts.net."},"CertDomains":["mac.tail1.ts.net"]}' ;;
serve) if [ "$2" = status ]; then read -r l < `+state+`; echo "$l"
  else echo "$@" >> `+calls+`; if [ ! -f `+fails+` ]; then : > `+fails+`; echo "busy" >&2; exit 1; fi; echo '{}' > `+state+`; fi ;;
esac
`)
	os.Chmod(filepath.Join(bin, "tailscale"), 0o755)
	t.Setenv("PATH", bin)
	write(t, filepath.Join(root, "rota.conf"), fmt.Sprintf("usage: off\nlisten: off\nweb: local\nweb_port: %d\n", port))
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: true\n---\nalpha\n")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { Follow(ctx, root, "rota.conf", false, func(string) {}); close(done) }()
	defer func() { cancel(); <-done }()
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if b, _ := os.ReadFile(state); !strings.Contains(string(b), "Proxy") {
			break
		}
		if time.Now().After(end) {
			b, _ := os.ReadFile(calls)
			t.Fatalf("Tailscale is told to stop until it does: %q", b)
		}
	}
	if b, _ := os.ReadFile(calls); strings.Count(string(b), "serve --https=443 off") != 2 {
		t.Fatalf("refused once, then told again: %q", b)
	}
}

// Local pages that Tailscale still serves beside something else, and so will not stop serving,
// are not served at all: on this machine they would be on the tailnet too. Once Tailscale serves
// them no more, they are.
func TestPagesTailscaleWillNotStopServingAreNotServed(t *testing.T) {
	prevCheck, prevRetry := settingsCheck, offTailnetRetry
	t.Cleanup(func() { settingsCheck, offTailnetRetry = prevCheck, prevRetry })
	settingsCheck, offTailnetRetry = 50*time.Millisecond, 100*time.Millisecond
	root, bin := t.TempDir(), t.TempDir()
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	state := filepath.Join(bin, "served")
	write(t, state, fmt.Sprintf(`{"TCP":{"443":{"HTTPS":true}},"Web":{"mac.tail1.ts.net:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:%d"},"/grafana":{"Proxy":"http://127.0.0.1:3000"}}}}}`, port))
	write(t, filepath.Join(bin, "tailscale"), `#!/bin/sh
case "$1" in
status) echo '{"Self":{"DNSName":"mac.tail1.ts.net."},"CertDomains":["mac.tail1.ts.net"]}' ;;
serve) read -r l < `+state+`; echo "$l" ;;
esac
`)
	os.Chmod(filepath.Join(bin, "tailscale"), 0o755)
	t.Setenv("PATH", bin)
	write(t, filepath.Join(root, "rota.conf"), fmt.Sprintf("usage: off\nlisten: off\nweb: local\nweb_port: %d\n", port))
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: true\n---\nalpha\n")
	var mu sync.Mutex
	var said []string
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Follow(ctx, root, "rota.conf", false, func(s string) { mu.Lock(); said = append(said, s); mu.Unlock() })
		close(done)
	}()
	defer func() { cancel(); <-done }()
	serves := func() bool {
		c := http.Client{Timeout: time.Second}
		resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
		if err == nil {
			resp.Body.Close()
		}
		return err == nil
	}
	time.Sleep(400 * time.Millisecond)
	mu.Lock()
	held := len(said) == 1 && strings.HasPrefix(said[0], "pages off until Tailscale stops serving them")
	mu.Unlock()
	if serves() || !held {
		t.Fatalf("pages Tailscale will not stop serving are served: %q", said)
	}
	write(t, state, `{}`) // the human turned it off in Tailscale
	for end := time.Now().Add(5 * time.Second); !serves(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("the pages are served once Tailscale serves them no more")
		}
	}
}

// Pages served while Tailscale did not answer stop when it answers that it still serves them and
// will not stop: on this machine they would be on the tailnet too.
func TestPagesStopWhenTailscaleComesBackServingThem(t *testing.T) {
	prevCheck, prevRetry := settingsCheck, offTailnetRetry
	t.Cleanup(func() { settingsCheck, offTailnetRetry = prevCheck, prevRetry })
	settingsCheck, offTailnetRetry = 50*time.Millisecond, 100*time.Millisecond
	root, bin := t.TempDir(), t.TempDir()
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	state, up := filepath.Join(bin, "served"), filepath.Join(bin, "up")
	write(t, state, fmt.Sprintf(`{"TCP":{"443":{"HTTPS":true}},"Web":{"mac.tail1.ts.net:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:%d"},"/grafana":{"Proxy":"http://127.0.0.1:3000"}}}}}`, port))
	write(t, filepath.Join(bin, "tailscale"), `#!/bin/sh
[ -f `+up+` ] || { echo "failed to connect to local tailscaled" >&2; exit 1; }
case "$1" in
status) echo '{"Self":{"DNSName":"mac.tail1.ts.net."},"CertDomains":["mac.tail1.ts.net"]}' ;;
serve) read -r l < `+state+`; echo "$l" ;;
esac
`)
	os.Chmod(filepath.Join(bin, "tailscale"), 0o755)
	t.Setenv("PATH", bin)
	write(t, filepath.Join(root, "rota.conf"), fmt.Sprintf("usage: off\nlisten: off\nweb: local\nweb_port: %d\n", port))
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: true\n---\nalpha\n")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { Follow(ctx, root, "rota.conf", false, func(string) {}); close(done) }()
	defer func() { cancel(); <-done }()
	serves := func() bool {
		c := http.Client{Timeout: time.Second}
		resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
		if err == nil {
			resp.Body.Close()
		}
		return err == nil
	}
	until := func(what string, cond func() bool) {
		t.Helper()
		for end := time.Now().Add(5 * time.Second); !cond(); time.Sleep(20 * time.Millisecond) {
			if time.Now().After(end) {
				t.Fatal("timed out: " + what)
			}
		}
	}
	until("served while Tailscale is off: it serves nothing", serves)
	write(t, up, "") // Tailscale is back, with the old proxy beside another
	until("no longer served", func() bool { return !serves() })
	write(t, state, `{}`)
	until("served again once Tailscale serves them no more", serves)
}

// The team chat in the app: its messages as HTML, a change applied and undone from the page, what
// the human writes, all with the page's token; the same chat is not sent again.
func TestTheTeamChatInTheApp(t *testing.T) {
	root, srv := team(t)
	conf, _ := os.ReadFile(filepath.Join(root, "rota.conf"))
	write(t, filepath.Join(root, "rota.conf"), string(conf)+"chat_model: off\n") // no Team: nothing to wake
	tm, _ := core.Open(root, "rota.conf")
	id, err := tm.ChatPropose(core.ChatChange{Agent: "alpha", Set: map[string]string{"run_minutes": "90"}, Why: "slow <b>stack</b>"})
	if err != nil {
		t.Fatal(err)
	}
	type chat struct {
		Msgs []apiChatMsg
		Team bool
	}
	read := func() chat {
		var c chat
		json.Unmarshal([]byte(get(t, srv, "/api/chat")), &c)
		return c
	}
	if c := read(); c.Team || len(c.Msgs) != 1 || c.Msgs[0].Change == nil || c.Msgs[0].Change.Why != "slow <b>stack</b>" {
		t.Fatalf("the chat: %+v", c)
	}
	if raw := get(t, srv, "/api/chat"); !strings.Contains(raw, `"busy":[]`) { // the page shows the Team typing until it hears it is not
		t.Fatalf("an empty busy is not said: %s", raw)
	}
	token := regexp.MustCompile(`data-token="([0-9a-f]+)"`).FindStringSubmatch(get(t, srv, "/"))[1]
	act := func(form url.Values) (int, string) {
		resp, err := http.PostForm(srv.URL+"/api/act", form)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]string
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out["said"] + out["error"]
	}
	if code, _ := act(url.Values{"action": {"chat-apply"}, "arg": {id}}); code != http.StatusForbidden {
		t.Fatalf("without the page's token: %d", code)
	}
	if code, said := act(url.Values{"token": {token}, "action": {"chat-apply"}, "arg": {id}}); code != 200 || !strings.Contains(said, "run_minutes default → 90") {
		t.Fatalf("apply: %d %s", code, said)
	}
	if code, said := act(url.Values{"token": {token}, "action": {"chat-undo"}, "arg": {id}}); code != 200 || !strings.HasPrefix(said, "Undone") {
		t.Fatalf("undo: %d %s", code, said)
	}
	// what an agent asked, answered from the chat: the question closes, as from the mail
	qid, err := tm.AskWith(core.Question{Agent: "alpha", Text: "Which way?", Options: []string{"A: tabs", "B: accept the risk"}})
	if err != nil {
		t.Fatal(err)
	}
	answer, err := tm.ChatPropose(core.ChatChange{Do: "answer", Thread: "q:" + qid, Text: "B", Why: "you said so"})
	if err != nil {
		t.Fatal(err)
	}
	if code, said := act(url.Values{"token": {token}, "action": {"chat-apply"}, "arg": {answer}}); code != 200 || said != "Answered alpha." {
		t.Fatalf("the answer: %d %s", code, said)
	}
	if tm, _ := core.Open(root, "rota.conf"); len(tm.Questions()) != 0 || !strings.Contains(tm.NotesText(tm.Notes("alpha")), "B: accept the risk") {
		t.Fatalf("answered: %+v", tm.Questions())
	}
	if code, said := act(url.Values{"token": {token}, "action": {"chat-undo"}, "arg": {answer}}); code != 400 || !strings.Contains(said, "not taken back") {
		t.Fatalf("an answer is taken back: %d %s", code, said)
	}
	if code, _ := act(url.Values{"token": {token}, "action": {"chat"}, "text": {"Hello <script>x</script>"}}); code != 200 {
		t.Fatalf("send: %d", code)
	}
	c := read()
	var mine string
	for _, m := range c.Msgs {
		if m.From == "you" {
			mine = m.HTML
		}
	}
	if !strings.Contains(mine, "&lt;script&gt;") || strings.Contains(mine, "<script>") {
		t.Fatalf("what the human wrote is text: %q", mine)
	}
	if code, said := act(url.Values{"token": {token}, "action": {"chat"}, "text": {"@nobody hi"}}); code != 400 || !strings.Contains(said, "no agent is called nobody") {
		t.Fatalf("to no agent: %d %s", code, said)
	}
	if code, _ := act(url.Values{"token": {token}, "action": {"chat"}, "arg": {"alpha"}, "text": {"Rebase it, please."}}); code != 200 { // in alpha's chat: no @ needed
		t.Fatalf("in alpha's chat: %d", code)
	}
	c = read()
	for i, want := range []string{"team you", "team ", "alpha you", "alpha "} { // the Team's chat, with rota's line that the Team is off; alpha's, with rota's line that alpha is not at work
		if m := c.Msgs[len(c.Msgs)-4+i]; m.With+" "+m.From != want {
			t.Fatalf("message %d is %q in %q's chat, want %q: %+v", i, m.From, m.With, want, c.Msgs)
		}
	}
	if ths := tm.MessageThreads(); len(ths) != 0 || !strings.Contains(tm.NotesText(tm.Notes("alpha")), "in the team chat: Rebase it, please.\n") {
		t.Fatalf("alpha, not at work, gets it from the chat at its next shift, not in its mail: %+v", ths)
	}
	if code, _ := act(url.Values{"token": {token}, "action": {"chat"}, "arg": {"alpha"}, "text": {"And this one?"}, "about": {"Pull request o/app#1 «one», by alpha"}}); code != 200 {
		t.Fatalf("with what is on the screen: %d", code)
	}
	if raw := get(t, srv, "/api/chat"); !strings.Contains(raw, `"about":"Pull request o/app#1 «one», by alpha"`) || !strings.Contains(tm.NotesText(tm.Notes("alpha")), "(on their screen: Pull request o/app#1 «one», by alpha)") {
		t.Fatalf("the chat and alpha have what was on the screen: %s", raw)
	}
	c = read()
	at := c.Msgs[len(c.Msgs)-1].At // the page says the human saw alpha's chat up to there, as the server has the time
	if code, _ := act(url.Values{"token": {token}, "action": {"chat-seen"}, "arg": {"alpha"}, "text": {at.Format(time.RFC3339Nano)}}); code != 200 || !tm.ChatSeenAt("alpha").Equal(at) {
		t.Fatalf("seen: %d %v", code, tm.ChatSeenAt("alpha"))
	}
	if code, _ := act(url.Values{"token": {token}, "action": {"chat-seen"}, "arg": {"alpha"}, "text": {"yesterday"}}); code != 400 {
		t.Fatal("seen up to a time")
	}
	// the human edits their words alpha has not answered, and stops it: what it never had leaves its inbox
	last := c.Msgs[len(c.Msgs)-1]
	if code, _ := act(url.Values{"token": {token}, "action": {"chat-edit"}, "arg": {last.ID}, "text": {"And this <one>?"}}); code != 200 {
		t.Fatalf("edit: %d", code)
	}
	c = read()
	edited := c.Msgs[len(c.Msgs)-1]
	if !edited.Edited || edited.Text != "And this <one>?" || !strings.Contains(edited.HTML, "&lt;one&gt;") || edited.About != last.About || slices.ContainsFunc(c.Msgs, func(m apiChatMsg) bool { return m.ID == last.ID }) {
		t.Fatalf("the new words, at the chat's end, in place of the old: %+v", c.Msgs)
	}
	if in := tm.NotesText(tm.Notes("alpha")); !strings.Contains(in, "And this <one>?") || strings.Contains(in, "And this one?") {
		t.Fatalf("alpha, not at work, gets the new words in place of the old: %s", in)
	}
	if code, said := act(url.Values{"token": {token}, "action": {"chat-stop"}, "arg": {"alpha"}}); code != 200 || said != "You stopped alpha." {
		t.Fatalf("stop: %d %s", code, said)
	}
	if in := tm.NotesText(tm.Notes("alpha")); strings.Contains(in, "team chat") {
		t.Fatalf("what alpha never had leaves its inbox: %s", in)
	}
	if code, said := act(url.Values{"token": {token}, "action": {"chat-stop"}, "arg": {"alpha"}}); code != 400 || !strings.Contains(said, "not answering you") {
		t.Fatalf("an agent that answers nothing is stopped: %d %s", code, said)
	}
	// the human deletes their words alpha has not answered: they leave the chat and alpha's inbox
	if code, _ := act(url.Values{"token": {token}, "action": {"chat"}, "arg": {"alpha"}, "text": {"Never mind the rebase."}}); code != 200 {
		t.Fatalf("chat: %d", code)
	}
	c = read()
	gone := c.Msgs[slices.IndexFunc(c.Msgs, func(m apiChatMsg) bool { return m.From == "you" && m.Text == "Never mind the rebase." })] // rota says after it that alpha is not at work
	if code, said := act(url.Values{"token": {token}, "action": {"chat-delete"}, "arg": {gone.ID}}); code != 200 {
		t.Fatalf("delete: %d %s", code, said)
	}
	if c = read(); slices.ContainsFunc(c.Msgs, func(m apiChatMsg) bool { return m.ID == gone.ID || strings.Contains(m.Text, "Never mind") }) {
		t.Fatalf("the deleted words leave the chat: %+v", c.Msgs)
	}
	if in := tm.NotesText(tm.Notes("alpha")); strings.Contains(in, "Never mind") {
		t.Fatalf("alpha, not at work, never gets them: %s", in)
	}
	if code, _ := act(url.Values{"token": {token}, "action": {"chat-delete"}, "arg": {gone.ID}}); code != 400 {
		t.Fatal("words deleted already are deleted again")
	}
	if js := get(t, srv, "/app.js"); !strings.Contains(js, "action: 'chat-stop'") || !strings.Contains(js, "action: 'chat-edit'") || !strings.Contains(js, "data-cstop") || !strings.Contains(js, "data-cedit") || !strings.Contains(js, "action: 'chat-delete'") || !strings.Contains(js, "data-cdel") {
		t.Fatal("the chat has Stop while an answer comes, and Edit and Delete on the human's words not yet answered")
	}
	for path, want := range map[string][2]string{"/": {"frame-ancestors 'none'", "DENY"}, "/?embed=chat": {"frame-ancestors 'self'", "SAMEORIGIN"}} { // only the chat alone, for a review page of these pages
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, want[0]) || resp.Header.Get("X-Frame-Options") != want[1] {
			t.Fatalf("%s may be framed by: %q %q", path, csp, resp.Header.Get("X-Frame-Options"))
		}
	}
	resp, _ := http.Get(srv.URL + "/api/chat")
	tag := resp.Header.Get("ETag")
	resp.Body.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/api/chat", nil)
	req.Header.Set("If-None-Match", tag)
	again, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	again.Body.Close()
	if again.StatusCode != http.StatusNotModified {
		t.Fatalf("the same chat is sent again: %d", again.StatusCode)
	}
}

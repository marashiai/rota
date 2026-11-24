// Package web serves the team's pages on this machine: reports, status, reviews, journal,
// transcripts, spend, inbox, agents and settings, with the console's controls: the team's,
// each agent's, notes, and answers to the agents' questions. Every request reads the team
// folder through the core.
package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"rota/core"
	"rota/icon"
	"rota/md"

	"rota/gloss/review"
	glossweb "rota/gloss/web"
)

// Version is this build, shown on the Settings page.
var Version = "dev"

func addr(port int) string { return net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) }

// tailnetCheck is how often the pages look for this machine's Tailscale address.
const tailnetCheck = 30 * time.Second

// liveCheck is how often a live page's connection looks for a change; tests shorten it.
var liveCheck = time.Second

// Serve serves the pages on this machine, on port, until ctx ends; with tailnet, also at its
// Tailscale address, for the human's phone and other devices.
func Serve(ctx context.Context, root, conf string, port int, tailnet bool) error {
	srv := &http.Server{Addr: addr(port), Handler: Handler(root, conf, port), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 60 * time.Second,
		DisableGeneralOptionsHandler: true} // OPTIONS * goes through the host check too
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { <-ctx.Done(); srv.Close() }()
	go func() { // while the pages are served, the phones hear of what is said in the chat
		for ctx.Err() == nil {
			if t, err := core.Open(root, conf); err == nil {
				t.WatchChat(ctx)
				return
			}
			select {
			case <-ctx.Done():
			case <-time.After(30 * time.Second): // no team folder yet
			}
		}
	}()
	tailnetDone := make(chan struct{})
	if tailnet {
		go func() { defer close(tailnetDone); serveTailnet(ctx, srv, port) }()
	} else {
		close(tailnetDone)
	}
	err = srv.Serve(ln)
	cancel()
	<-tailnetDone // it set nothing up on the tailnet that outlives the pages: what follows may take them off it
	if err != http.ErrServerClosed && ctx.Err() == nil {
		return err
	}
	return nil
}

// serveTailnet serves srv at this machine's Tailscale address too, and follows the address
// as Tailscale comes up, goes down or changes it. Closing srv closes its listener.
func serveTailnet(ctx context.Context, srv *http.Server, port int) {
	var ln net.Listener
	bound, warned, said := "", "", ""
	for ctx.Err() == nil {
		if err := core.ServeTailscaleHTTPS(ctx, port); err != nil && ctx.Err() == nil { // a phone allows notifications only over HTTPS
			if err.Error() != said { // say it once; it is retried
				said = err.Error()
				fmt.Fprintln(os.Stderr, "warn: pages over HTTPS:", err)
			}
		} else if u := core.TailscaleHTTPS(port); u != "" && u != said {
			said = u
			fmt.Printf("%s pages over HTTPS at %s/\n", time.Now().Format("15:04"), u)
		}
		if ip := core.TailscaleIP(); ip != bound {
			if ln != nil {
				ln.Close()
				ln, bound = nil, ""
			}
			if ip != "" { // never every interface
				if l, err := net.Listen("tcp", net.JoinHostPort(ip, strconv.Itoa(port))); err == nil {
					ln, bound, warned = l, ip, ""
					go srv.Serve(l)
				} else if err.Error() != warned { // say it once; it is retried
					warned = err.Error()
					fmt.Fprintln(os.Stderr, "warn: pages at the Tailscale address:", err)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(tailnetCheck):
		}
	}
}

type site struct {
	root, conf string
	port       int
	token      string // in every form; a page from another origin cannot know it
}

// safeName is what a page name may be: a file's name, or <date>/<name> for a transcript.
var safeName = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]*(/[A-Za-z0-9_-][A-Za-z0-9._-]*)?$`)

// serveIcon serves an icon, drawn the first time it is asked for: drawing them all first would
// hold up the pages' start. A browser asks again each time and hears it is the same until a new
// rota draws another, which it then shows at once.
func serveIcon(size int, style string) http.HandlerFunc {
	drawn := sync.OnceValues(func() ([]byte, string) {
		b := icon.PNG(size, style)
		sum := sha256.Sum256(b)
		return b, `"` + hex.EncodeToString(sum[:8]) + `"`
	})
	return func(w http.ResponseWriter, r *http.Request) {
		b, tag := drawn()
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("ETag", tag)
		if r.Header.Get("If-None-Match") == tag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Write(b)
	}
}

// Handler is the site; port is the one it is served on, which a form's origin must match.
func Handler(root, conf string, port int) http.Handler {
	b := make([]byte, 16)
	rand.Read(b)
	s := &site{root, conf, port, hex.EncodeToString(b)}
	mux := http.NewServeMux()
	// doc serves a document page made from the path under it: a report, or evidence
	doc := func(path string, f func(*core.Team, string) (string, bool, template.HTML)) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			t, err := core.Open(s.root, s.conf)
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			rest := strings.Trim(strings.TrimPrefix(r.URL.Path, path), "/")
			if path == "/evidence/" { // a path of any depth under evidence/: its files are images, served as they are
				if file := t.Evidence(rest); file != "" && image(file) != "" {
					w.Header().Set("Content-Type", image(file))
					w.Header().Set("X-Content-Type-Options", "nosniff")
					http.ServeFile(w, r, file)
					return
				}
			} else if path == "/reports/" && strings.Count(rest, "/") == 1 { // an agent's report: only a file it lists is served
			} else if rest != "" && (!safeName.MatchString(rest) || strings.Contains(rest, "..")) {
				http.NotFound(w, r)
				return
			}
			title, live, body := f(t, rest)
			if title == "" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			layout.Execute(w, map[string]any{"Title": title, "Live": live, "Body": body, "Voice": s.voiceTag()})
		})
	}
	reports := func(t *core.Team, name string) (string, bool, template.HTML) { // live while it shows today's
		if agent, file, ok := strings.Cut(name, "/"); ok {
			return agentReport(t, agent, file)
		}
		title, _, body := docList("Reports", t.Reports(), "/reports/", name, "Nothing reported yet. When a shift ends, what you should know from it appears here, newest first.")
		all := t.Reports()
		return title, name == "" || len(all) > 0 && strings.TrimSuffix(filepath.Base(all[0]), ".md") == name, body
	}
	// moved sends an address of the pages before the app to the app's page for it
	moved := func(path string, to func(rest string) string) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			rest := strings.Trim(strings.TrimPrefix(r.URL.Path, path), "/")
			if rest != "" && (!safeName.MatchString(rest) || strings.Contains(rest, "..")) {
				rest = ""
			}
			http.Redirect(w, r, "/#"+to(rest), http.StatusFound)
		})
	}
	// listMoved is moved for Reviews and Issues, whose links chose what narrows the list, kept
	// for the app's folder and the console: such a link chooses it still, then opens the folder
	listMoved := func(path, tab, folder string) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			if slices.ContainsFunc([]string{"repo", "mark", "label", "all", "order"}, q.Has) {
				if t, err := core.Open(s.root, s.conf); err == nil {
					f := core.ListFilter{Repo: q.Get("repo"), Mark: q.Get("mark"), Labels: q["label"], Order: t.ListFilter(tab).Order}
					if q.Has("all") {
						f = core.ListFilter{Order: f.Order}
					}
					if o := q.Get("order"); tab == core.IssuesTab && slices.ContainsFunc(core.IssueOrders, func(x core.Filter) bool { return x.Value == o }) {
						f.Order = o
					}
					t.SetListFilter(tab, f)
				}
			}
			http.Redirect(w, r, "/#"+folder, http.StatusFound)
		})
	}
	always := func(page string) func(string) string { return func(string) string { return page } }
	moved("/status/", always("team"))
	moved("/spend/", always("shares"))
	moved("/shares/", always("shares"))
	moved("/settings/", always("settings"))
	moved("/journal/", always("team"))
	listMoved("/reviews/", core.ReviewsTab, "reviews")
	listMoved("/issues/", core.IssuesTab, "issues")
	moved("/agents/", func(name string) string {
		return or(map[bool]string{true: "agent:" + name}[name != "" && !strings.Contains(name, "/")], "team")
	})
	moved("/inbox/", func(name string) string {
		return or(map[bool]string{true: "agent:" + name}[name != "" && !strings.Contains(name, "/")], "inbox")
	})
	moved("/transcripts/", func(id string) string {
		return or(map[bool]string{true: "run:" + id}[strings.Contains(id, "/")], "team")
	})
	mux.HandleFunc("/", s.app) // the mail client for the team
	mux.HandleFunc("/api/mail", s.mail)
	mux.HandleFunc("/api/act", s.apiAct)
	mux.HandleFunc("/api/issues", s.issuesJSON)
	mux.HandleFunc("/api/issue", s.issueJSON)
	mux.HandleFunc("/api/push", s.pushJSON)
	mux.HandleFunc("/api/team", s.teamJSON)
	mux.HandleFunc("/api/agent", s.agentJSON)
	mux.HandleFunc("/api/run", s.runJSON)
	mux.HandleFunc("/api/settings", s.settingsJSON)
	mux.HandleFunc("/api/chat", s.chatJSON)
	mux.HandleFunc("/api/shares", s.sharesJSON)
	mux.HandleFunc("/api/voice", s.voiceJSON)
	mux.HandleFunc("/api/voice/say", s.voiceSay)
	mux.HandleFunc("/api/voice/listen", s.voiceListen)
	mux.Handle("/review/", http.StripPrefix("/review", s.reviewPages()))
	doc("/reports/", reports)
	doc("/evidence/", s.evidence)
	mux.HandleFunc("/events", s.events)
	mux.HandleFunc("/mark.png", serveIcon(64, "web")) // the tile to its edges: a tab shows it small, the pages beside their name
	for _, size := range []int{192, 512} {            // what a phone's home screen shows, to its edges: the phone rounds it
		mux.HandleFunc(fmt.Sprintf("/mark-%d.png", size), serveIcon(size, "full"))
	}
	mux.HandleFunc("/manifest.webmanifest", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/manifest+json")
		fmt.Fprint(w, manifest)
	})
	mux.HandleFunc("/sw.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.Header().Set("Cache-Control", "no-cache")
		fmt.Fprint(w, serviceWorker)
	})
	mux.HandleFunc("/notify.json", s.notifyJSON)
	mux.HandleFunc("/questions.json", s.questionsJSON)
	mux.HandleFunc("/answers.json", s.answersJSON)
	mux.HandleFunc("/push/", s.push)
	return s.local(mux)
}

// local refuses every request that names a host other than this machine. A website that
// points its own name at this address (DNS rebinding) is same-origin with itself in the
// browser, so without this it could read the pages.
func (s *site) local(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.thisMachine(r) {
			http.Error(w, "refused: not this machine", http.StatusForbidden)
			return
		}
		// no other site may show the pages in a frame and trick a click on a control
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

// thisMachine says whether a request names this machine: localhost, the address it came in
// on, or, on the Tailscale address, this machine's Tailscale name (only Tailscale can point
// a ts.net name anywhere).
func (s *site) thisMachine(r *http.Request) bool {
	port := strconv.Itoa(s.port)
	if r.Host == addr(s.port) || r.Host == "localhost:"+port {
		return true
	}
	if name := core.TailscaleName(); name != "" && (r.Host == name || strings.HasPrefix(r.Host, name+":")) {
		return true // Tailscale serves the pages over HTTPS at this machine's own name; only Tailscale can point it here
	}
	in, _ := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if in == nil {
		return false
	}
	if r.Host == in.String() {
		return true
	}
	host, p, _ := net.SplitHostPort(r.Host)
	ip, _, _ := net.SplitHostPort(in.String())
	return p == port && strings.HasSuffix(strings.ToLower(host), ".ts.net") && ip == core.TailscaleIP()
}

var layout = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Title}} · rota</title><link rel="icon" type="image/png" href="/mark.png">
<link rel="manifest" href="/manifest.webmanifest"><link rel="apple-touch-icon" href="/mark-192.png"><meta name="apple-mobile-web-app-capable" content="yes">
<style>
:root{--bg:#fff;--fg:#1b1b1f;--mut:#5c5c66;--line:#e4e4ea;--pill:#f1f1f5;--ok:#1a7f37;--warn:#9a4f00;--bad:#b3261e;--link:#0b57d0}
@media(prefers-color-scheme:dark){:root{--bg:#121216;--fg:#e8e8ec;--mut:#a3a3ae;--line:#2a2a33;--pill:#1e1e26;--ok:#4caf6a;--warn:#e0a040;--bad:#ef7a70;--link:#8ab4f8}}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--fg);font:15px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}
nav{display:flex;align-items:center;gap:8px;padding:10px 16px;border-bottom:1px solid var(--line);position:sticky;top:0;z-index:1;background:var(--bg)}
nav a{display:flex;align-items:center;gap:8px;font-weight:700;color:var(--fg);text-decoration:none}nav a img{border-radius:5px}
main{max-width:980px;margin:0 auto;padding:16px}h1{font-size:22px;margin:8px 0 16px}h2{font-size:17px;margin:24px 0 8px}h3{font-size:15px;margin:16px 0 6px}
table{border-collapse:collapse;width:100%;font-size:14px}th,td{text-align:left;padding:6px 8px;border-bottom:1px solid var(--line);vertical-align:top}th{color:var(--mut);font-weight:600}
code,pre{font:13px/1.45 ui-monospace,SFMono-Regular,Menlo,monospace}code{background:var(--pill);padding:1px 5px;border-radius:4px}
pre{background:var(--pill);padding:10px 12px;border-radius:8px;overflow:auto;white-space:pre-wrap}pre code{background:none;padding:0}
.mut{color:var(--mut)}.ok{color:var(--ok)}.warn{color:var(--warn)}.bad{color:var(--bad)}a{color:var(--link)}
.pill{display:inline-block;background:var(--pill);border-radius:999px;padding:1px 10px;margin:2px;font-size:13px;text-decoration:none;color:var(--fg)}.pill.on{outline:2px solid var(--link)}
blockquote{margin:8px 0;padding:2px 12px;border-left:3px solid var(--line);color:var(--mut)}ul,ol{padding-left:22px}li{margin:2px 0}details summary{cursor:pointer;color:var(--mut)}
img{max-width:100%;height:auto}p,li,blockquote,summary,h1,h2,h3{overflow-wrap:anywhere}
@media(max-width:640px){main{padding:12px}h1{font-size:20px;margin:12px 0}table{display:block;overflow-x:auto}table.doc th,table.doc td{min-width:9em}}
</style></head><body>
<nav><a href="/"><img src="/mark.png" alt="" width="22" height="22">rota</a></nav>
<main{{if .Live}} data-live{{end}}><h1>{{.Title}}</h1>{{.Body}}</main>
{{if .Live}}<script>(function () { // when the team changes, the page updates in place: no reload
  var main = document.querySelector("main"), due = false, busy = false;
  function edited(f) {
    if (f.type == "hidden" || f.type == "submit") return false;
    if (f.type == "checkbox" || f.type == "radio") return f.checked != f.defaultChecked;
    if (f.tagName == "SELECT") {
      var o = [].slice.call(f.options), d = o.filter(function (x) { return x.defaultSelected; });
      return d.length ? o.some(function (x) { return x.selected != x.defaultSelected; }) : f.selectedIndex > 0;
    }
    return f.value != f.defaultValue;
  }
  function inUse() { // something typed, focused or selected stays as it is
    var a = document.activeElement, s = getSelection();
    return a && a != document.body && main.contains(a) && ["INPUT", "TEXTAREA", "SELECT"].indexOf(a.tagName) >= 0 ||
      [].some.call(main.querySelectorAll("input, textarea, select"), edited) ||
      s && !s.isCollapsed && main.contains(s.anchorNode);
  }
  function update() {
    if (busy || document.hidden || inUse()) { due = true; return; }
    due = false; busy = true;
    fetch(location.href, {cache: "no-store"}).then(function (r) {
      if (!r.ok) throw r.status;
      if (r.redirected) history.replaceState(null, "", r.url); // a choice the address made is kept: asked again, it would undo a later one
      return r.text();
    }).then(function (html) {
      busy = false;
      var doc = new DOMParser().parseFromString(html, "text/html"), next = doc.querySelector("main");
      var nav = document.querySelector("nav"), top = doc.querySelector("nav"); // its count, the team's state and the budget
      if (nav && top && top.innerHTML != nav.innerHTML) { var x = nav.scrollLeft; nav.innerHTML = top.innerHTML; nav.scrollLeft = x; }
      if (!next) return;
      if (!next.hasAttribute("data-live")) events.close(); // a finished shift's transcript no longer changes
      if (next.innerHTML == main.innerHTML) return;
      if (inUse()) { due = true; return; }
      var open = {}, end = main.querySelector("[data-follow]") && innerHeight + scrollY >= document.documentElement.scrollHeight - 40;
      function key(d, i) { var s = d.querySelector("summary"); return i + " " + (s ? s.textContent : ""); }
      main.querySelectorAll("details").forEach(function (d, i) { open[key(d, i)] = d.open; });
      main.innerHTML = next.innerHTML;
      main.querySelectorAll("details").forEach(function (d, i) { var k = key(d, i); if (k in open) d.open = open[k]; });
      document.title = doc.title;
      if (end) scrollTo(0, document.documentElement.scrollHeight); // following the end of a page that grows there: a running transcript
    }).catch(function () { busy = false; due = true; });
  }
  var events = new EventSource("/events");
  events.onmessage = update;
  events.onopen = update; // what changed since the page was made, or while the organizer restarted
  setInterval(function () { if (due) update(); }, 2000); // what waited for you to finish
  document.addEventListener("visibilitychange", function () { if (!document.hidden && due) update(); });
})();</script>{{end}}{{.Voice}}</body></html>`))

// events tells a live page each time the team folder changes (core's Stamp), as server-sent
// events: the page then fetches itself and updates in place.
func (s *site) events(w http.ResponseWriter, r *http.Request) {
	t, err := core.Open(s.root, s.conf)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	rc := http.NewResponseController(w)
	rc.SetReadDeadline(time.Time{}) // it stays open as long as the page; each write has its own deadline
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	send := func(msg string) bool {
		rc.SetWriteDeadline(time.Now().Add(10 * time.Second)) // a reader that stopped reading is let go
		_, err := fmt.Fprint(w, msg)
		return err == nil && rc.Flush() == nil
	}
	if !send("retry: 3000\n\n") {
		return
	}
	last, quiet := t.Stamp(), 0
	for {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(liveCheck):
		}
		msg := ": still here\n\n" // now and then, so nothing between closes a quiet connection
		if now := t.Stamp(); now != last {
			last, msg, quiet = now, "data: changed\n\n", 0
		} else if quiet++; quiet < 25 {
			continue
		}
		quiet = 0
		if !send(msg) {
			return
		}
	}
}

// header is what the top of every page says: how the team is, what of the budget is used,
// and how many things wait for the human.
type header struct {
	State, Dot, Title string // the team in a few words, its dot's class and the longer text
	Budget            string // "28% used this week"; "" when the budget is not enforced
	Fill              int    // how full the budget's bar is, in percent
	Waits             int    // questions and pull requests that wait for the human: the Inbox's count
	held              string // why the team is on hold, or ""
}

func headerOf(t *core.Team) header {
	var h header
	h.Waits = len(t.Questions()) + len(t.ReviewQueue().ForYou(t.ReviewLabel))
	st := t.Live()
	switch {
	case st == nil:
		h.State, h.Dot, h.Title = "Not running", "off", "The organizer is not running: no run starts"
		return h
	case st.Stale:
		h.State, h.Dot = "Silent", "held"
	case st.Running != nil:
		h.State, h.Dot = st.Running.Agent+" is working", "work"
	case st.Held() != "":
		h.State, h.Dot = "On hold", "held"
	case st.Paused:
		h.State, h.Dot = "Paused", "paused"
	default:
		h.State, h.Dot = "On", "on"
	}
	h.Title, h.held = "The organizer: "+st.Organizer(), st.Held()
	if p := st.Pool; p.Enforced {
		used := p.Of(p.Used)
		h.Budget, h.Fill = fmt.Sprintf("%.0f%% used %s", used, p.Period), int(min(max(used, 0), 100))
	}
	return h
}

var esc = html.EscapeString

// image is the media type of an image a page may show, or "": nothing else is served as it
// is, so a file an agent wrote can never run in these pages.
func image(path string) string {
	return map[string]string{".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp"}[strings.ToLower(filepath.Ext(path))]
}

// evidence shows what a pull request or a report points to under evidence/: a folder as a
// list, markdown rendered, any other text as text, and images inline.
func (s *site) evidence(t *core.Team, rest string) (string, bool, template.HTML) {
	file := t.Evidence(rest)
	st, err := os.Stat(file)
	if file == "" || err != nil {
		return "", false, ""
	}
	var b strings.Builder
	if st.IsDir() {
		entries, _ := os.ReadDir(file)
		b.WriteString(`<ul>`)
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			p := strings.Trim(rest+"/"+e.Name(), "/")
			name := e.Name()
			if e.IsDir() {
				name += "/"
			}
			fmt.Fprintf(&b, `<li><a href="/evidence/%s">%s</a></li>`, esc(p), esc(name))
		}
		b.WriteString(`</ul>`)
		if len(entries) == 0 {
			b.WriteString(`<p class="mut">Nothing here yet.</p>`)
		}
		return "Evidence: " + or(rest, "all"), false, template.HTML(b.String())
	}
	text := readFile(file)
	if strings.HasSuffix(strings.ToLower(file), ".md") {
		b.WriteString(string(markdown(text)))
	} else {
		fmt.Fprintf(&b, `<pre>%s</pre>`, esc(text))
	}
	return "Evidence: " + rest, false, template.HTML(b.String())
}

// agentReport shows a report an agent wrote for the human, from reports/<agent>/.
func agentReport(t *core.Team, agent, name string) (string, bool, template.HTML) {
	file := t.AgentReport(agent, name)
	if file == "" {
		return "", false, ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<p class="mut">Written by <a href="/#agent:%s">%s</a>.</p>`, esc(agent), esc(agent))
	if text := readFile(file); strings.HasSuffix(strings.ToLower(file), ".md") {
		b.WriteString(string(markdown(text)))
	} else {
		fmt.Fprintf(&b, `<pre>%s</pre>`, esc(text))
	}
	return agent + ": " + name, false, template.HTML(b.String())
}

// docList links a folder's markdown files, newest first, and shows the chosen one; a name
// that is not there is not found.
func docList(title string, files []string, base, name, empty string) (string, bool, template.HTML) {
	if len(files) == 0 && name == "" {
		return title, false, template.HTML(`<p class="mut">` + esc(empty) + `</p>`)
	}
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(files[0]), ".md")
	}
	var b strings.Builder
	b.WriteString(`<p>`)
	shown := ""
	for _, f := range files {
		n := strings.TrimSuffix(filepath.Base(f), ".md")
		on := ""
		if n == name {
			on, shown = " on", f
		}
		fmt.Fprintf(&b, `<a class="pill%s" href="%s%s">%s</a>`, on, base, esc(n), esc(n))
	}
	b.WriteString(`</p>`)
	if shown == "" {
		return "", false, ""
	}
	b.WriteString(string(markdown(readFile(shown))))
	return title, false, template.HTML(b.String())
}

// id names a transcript in a URL: <date>/<HHMM>-<agent>.
func id(path string) string {
	return filepath.Base(filepath.Dir(path)) + "/" + strings.TrimSuffix(filepath.Base(path), ".jsonl")
}

func eventHTML(e core.Event) string {
	switch e.Kind {
	case "start":
		return fmt.Sprintf(`<p class="mut">claude started · %s · %d tools</p>`, esc(or(e.Model, "default model")), e.Tools)
	case "text":
		return `<div class="ev">` + string(markdown(e.Text)) + `</div>`
	case "tool":
		return fmt.Sprintf(`<div class="ev tool"><b>%s</b> <code>%s</code></div>`, esc(e.Tool), esc(e.Arg))
	case "output":
		cls := "mut"
		if e.Error {
			cls = "bad"
		}
		return fmt.Sprintf(`<details><summary class="%s">%s</summary><pre>%s</pre></details>`, cls, esc(core.FirstLine(e.Text, 120)), esc(e.Text))
	case "end":
		cls, how := "ok", "finished"
		if e.Error {
			cls, how = "bad", "ended: "+e.Subtype
		}
		return fmt.Sprintf(`<div class="ev end"><p class="%s">%s · %d turns</p>%s</div>`, cls, esc(how), e.Turns, markdown(e.Text))
	case "inbox":
		return `<div class="ev"><p class="mut">took a note from its inbox:</p>` + string(markdown(e.Text)) + `</div>`
	case "note":
		return `<p class="bad">` + esc(e.Text) + `</p>`
	}
	return ""
}

// reviewPages serves gloss's review pages under /review/, as gh's login on this machine, for
// the team's repositories only; after the human sends notes, the agent that wrote the pull
// request is told.
func (s *site) reviewPages() http.Handler {
	var mu sync.Mutex
	var h http.Handler
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch p := strings.Trim(r.URL.Path, "/"); p {
		case "": // the list is the Reviews page
			http.Redirect(w, r, "/#reviews", http.StatusFound)
			return
		case "app.js", "app.css":
		default:
			t, err := core.Open(s.root, s.conf)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			parts := strings.SplitN(p, "/", 3)
			if len(parts) < 2 || !t.Reviewable(parts[0]+"/"+parts[1]) {
				http.NotFound(w, r)
				return
			}
			if repo, number, ok := readsNotes(r.Method, p); ok {
				at, rec := time.Now(), &statusRecorder{ResponseWriter: w, status: http.StatusOK}
				defer func() {
					if rec.status == http.StatusOK { // what the page read, the human has seen
						t.SeenReview(repo, number, at)
					}
				}()
				w = rec
			}
		}
		mu.Lock()
		if h == nil {
			c, err := core.ReviewClient()
			if err != nil {
				mu.Unlock()
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
			h = glossweb.New(c, glossweb.Options{Title: "rota", Foot: s.voiceTag(), OnSend: func(ctx context.Context, rv *review.Review, sent int) error {
				t, err := core.Open(s.root, s.conf)
				if err != nil {
					return err
				}
				approved, err := core.Approved(ctx, c, rv)
				return errors.Join(err, t.ReviewSent(ctx, rv.Ref.Repo(), rv.Ref.Number, sent, approved))
			}, Chat: func(ref review.Ref) string { // the team chat over the review, on the chat of the agent that wrote it first
				u := "/?embed=chat"
				if t, err := core.Open(s.root, s.conf); err == nil {
					if a := t.PRAgent(ref.Repo(), ref.Number); a != "" {
						u += "&agent=" + url.QueryEscape(a)
					}
				}
				return u
			}})
		}
		handler := h
		mu.Unlock()
		handler.ServeHTTP(w, r)
	})
}

// readsNotes tells whether a request under /review/ (p, without it) is a review page reading a
// pull request's notes: it does when it opens, and every 30 seconds while it shows.
func readsNotes(method, p string) (repo string, number int, ok bool) {
	parts := strings.Split(p, "/")
	if method != http.MethodGet || len(parts) != 5 || parts[3] != "api" || parts[4] != "review" {
		return "", 0, false
	}
	if n, err := strconv.Atoi(parts[2]); err == nil && n > 0 {
		return parts[0] + "/" + parts[1], n, true
	}
	return "", 0, false
}

// statusRecorder notes the status a handler answered with.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) { r.status = code; r.ResponseWriter.WriteHeader(code) }

// control does what a control asks and says what happened.
func (s *site) control(t *core.Team, action, agent, qid, about, text string) (string, error) {
	st := t.Status(time.Now())
	if agent != "" && action != "forget" && t.Agent(agent) == nil {
		return "", fmt.Errorf("%s is not an agent", agent)
	}
	exe, _ := os.Executable()
	switch action {
	case "answer":
		for _, q := range t.Questions() {
			if q.ID == qid {
				if strings.TrimSpace(text) == "" {
					return "", fmt.Errorf("the answer is empty")
				}
				return "answer left in " + q.Agent + "'s inbox for its next shift", t.Answer(qid, text)
			}
		}
		return "", fmt.Errorf("that question is no longer open")
	case "dismiss":
		for _, q := range t.Questions() {
			if q.ID == qid {
				return q.Agent + "'s question dismissed; its inbox says so", t.Dismiss(qid)
			}
		}
		return "", fmt.Errorf("that question is no longer open")
	case "pause":
		if agent != "" {
			return agent + " paused: its running shift finishes, no new one starts; Run now still runs it", t.PauseAgent(agent)
		}
		return "team paused: the running shift finishes, no new one starts", t.Pause()
	case "resume":
		if agent != "" {
			return agent + " resumed", t.ResumeAgent(agent)
		}
		return "team resumed", t.Unpause()
	case "run":
		return t.AskToRun(agent, st)
	case "cancel":
		if st.Running == nil {
			return "", fmt.Errorf("no shift is running")
		}
		return "stopping " + st.Running.Agent + "'s shift", t.CancelShift(agent)
	case "start":
		if st.Pid != 0 {
			return "", fmt.Errorf("the organizer is already running")
		}
		_, err := t.StartOrganizer(exe, true)
		return "organizer started; its output is in rota.log", err
	case "stop":
		if st.Pid == os.Getpid() { // these pages are its own: answer first, then stop
			go func() { time.Sleep(time.Second); t.StopOrganizer() }()
			return "stopping the organizer; it serves these pages, so they stop too (rota web, the console or the menu bar serves them again)", nil
		}
		return "organizer stopped", t.StopOrganizer()
	case "note":
		if strings.TrimSpace(text) == "" {
			return "", fmt.Errorf("the note is empty")
		}
		to := t.AddressOf(agent, about)
		if err := t.LeaveNote(agent, about, "human", text); err != nil {
			return "", err
		}
		if t.Steerable(agent) {
			return "note for " + to.String() + ": its running shift reads it after its current step", nil
		}
		return "note for " + to.String() + ": read at its next shift", nil
	case "forget":
		return "note deleted", t.DeleteNote(agent, qid)
	}
	return "", fmt.Errorf("unknown control %q", action)
}

// sameOrigin refuses a post that did not come from these pages, so a page visited elsewhere
// cannot change the team; local has already refused other hosts.
func (s *site) sameOrigin(r *http.Request) bool {
	return fromHere(r) && r.FormValue("token") == s.token
}

// fromHere says a request names no other site as where it came from.
func fromHere(r *http.Request) bool {
	host := r.Host
	for _, h := range []string{r.Header.Get("Origin"), r.Header.Get("Referer")} {
		if h != "" && h != "http://"+host && !strings.HasPrefix(h, "http://"+host+"/") && h != "https://"+host && !strings.HasPrefix(h, "https://"+host+"/") {
			return false
		}
	}
	return true
}

// questionsJSON are the open questions, for the service worker that shows a push.
func (s *site) questionsJSON(w http.ResponseWriter, r *http.Request) {
	t, err := core.Open(s.root, s.conf)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type q struct {
		Agent string    `json:"agent"`
		Text  string    `json:"text"`
		Asked time.Time `json:"asked"`
	}
	out := []q{}
	for _, x := range t.Questions() {
		if t.Notifies(x.Importance) { // what the human chose to hear of: the phone shows nothing else
			out = append(out, q{x.Agent, or(x.Subject, x.Text), x.Asked})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(out)
}

// notifyJSON is what a device shows when it is told something, for the service worker: the
// newest of what the human chose to hear of, in the chat or in their mail.
func (s *site) notifyJSON(w http.ResponseWriter, r *http.Request) {
	t, err := core.Open(s.root, s.conf)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(t.Notice(time.Now()))
}

// answersJSON is the agents' answers the human has not seen, for a service worker from before
// notify.json.
func (s *site) answersJSON(w http.ResponseWriter, r *http.Request) {
	t, err := core.Open(s.root, s.conf)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type a struct {
		core.Answer
		Page string `json:"page"`
	}
	out := []a{}
	for _, x := range t.Answers() {
		out = append(out, a{x, x.Page()})
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(out)
}

// push turns notifications on or off for a device, or sends a test: posts from the status
// page, with its token.
func (s *site) push(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "post only", http.StatusMethodNotAllowed)
		return
	}
	r.ParseForm()
	if !s.sameOrigin(r) {
		http.Error(w, "refused", http.StatusForbidden)
		return
	}
	t, err := core.Open(s.root, s.conf)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	switch strings.TrimPrefix(r.URL.Path, "/push/") {
	case "subscribe":
		err = t.Subscribe(r.FormValue("endpoint"), r.FormValue("device"))
	case "unsubscribe":
		err = t.Unsubscribe(r.FormValue("endpoint"))
	case "test":
		n, perr := t.PushAll(r.Context())
		if perr != nil {
			http.Error(w, perr.Error(), http.StatusBadGateway)
			return
		}
		fmt.Fprintf(w, "sent to %d %s", n, map[bool]string{true: "device", false: "devices"}[n == 1])
		return
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

const manifest = `{"name": "rota", "short_name": "rota", "start_url": "/", "scope": "/", "display": "standalone",
"background_color": "#121216", "theme_color": "#121216",
"icons": [{"src": "/mark-192.png", "sizes": "192x192", "type": "image/png"}, {"src": "/mark-512.png", "sizes": "512x512", "type": "image/png"}]}
`

// serviceWorker shows a push: it carries no content, so the worker fetches the open questions
// and the agents' answers the human has not seen from the pages, over the tailnet, and shows the
// newest; off the tailnet it says only that an agent needs them.
const serviceWorker = `self.addEventListener("install", function () { self.skipWaiting(); });
self.addEventListener("activate", function (e) { e.waitUntil(self.clients.claim()); });
self.addEventListener("push", function (e) { // the push says nothing: what to show comes from the pages
  function note(n) {
    return self.registration.showNotification(n.title, {body: n.body, tag: n.tag || "rota", renotify: true, icon: "/mark-192.png", data: {url: n.url || "/"}});
  }
  e.waitUntil(fetch("/notify.json", {cache: "no-store"}).then(function (r) { return r.json(); }).then(note)
    .catch(function () { return note({title: "rota", body: "An agent asks or tells you something."}); }));
});
self.addEventListener("notificationclick", function (e) {
  e.notification.close();
  var url = e.notification.data.url;
  e.waitUntil(self.clients.matchAll({type: "window", includeUncontrolled: true}).then(function (ws) {
    for (var i = 0; i < ws.length; i++) if ("focus" in ws[i]) { ws[i].navigate(url); return ws[i].focus(); }
    return self.clients.openWindow(url);
  }));
});
`

// ---- markdown ----

// markdown renders what the agents write; everything is escaped, so a shift's text can only
// ever be text.
func markdown(text string) template.HTML {
	var b strings.Builder
	blocksHTML(&b, md.Parse(text))
	return template.HTML(b.String())
}

func blocksHTML(b *strings.Builder, blocks []md.Block) {
	list := ""
	for _, bl := range blocks {
		want := ""
		if bl.Kind == "item" {
			want = "ul"
			if bl.Marker != "-" {
				want = "ol"
			}
		}
		if want != list {
			if list != "" {
				b.WriteString("</" + list + ">")
			}
			if want != "" {
				b.WriteString("<" + want + ">")
			}
			list = want
		}
		switch bl.Kind {
		case "heading":
			fmt.Fprintf(b, "<h%d>%s</h%d>", bl.Level, inlineHTML(bl.Text), bl.Level)
		case "para":
			b.WriteString("<p>" + inlineHTML(bl.Text) + "</p>")
		case "item":
			b.WriteString("<li>" + inlineHTML(bl.Text) + "</li>")
		case "code":
			b.WriteString("<pre><code>" + esc(strings.Join(bl.Lines, "\n")) + "</code></pre>")
		case "rule":
			b.WriteString("<hr>")
		case "quote":
			b.WriteString("<blockquote>")
			blocksHTML(b, bl.Blocks)
			b.WriteString("</blockquote>")
		case "table":
			b.WriteString(`<table class="doc">`)
			for i, row := range bl.Rows {
				cell := "td"
				if i == 0 {
					cell = "th"
				}
				b.WriteString("<tr>")
				for _, c := range row {
					b.WriteString("<" + cell + ">" + inlineHTML(c) + "</" + cell + ">")
				}
				b.WriteString("</tr>")
			}
			b.WriteString("</table>")
		}
	}
	if list != "" {
		b.WriteString("</" + list + ">")
	}
}

// autolink finds an address in text escaped for HTML. What ends the sentence after it is left out
// of the link, as GitHub does: a full stop, a comma, a colon, a semicolon, ! or ?, and a quote mark,
// escaped; the semicolon that ends an escaped character, as &amp; does, stays.
var (
	autolink  = regexp.MustCompile(`https?://[^\s<)]+`)
	endEntity = regexp.MustCompile(`&(?:[A-Za-z]+|#[0-9]+);$`)
)

// linked is escaped text with its addresses as links.
func linked(t string) string {
	return autolink.ReplaceAllStringFunc(t, func(u string) string {
		end := ""
		for {
			if q := strings.TrimSuffix(strings.TrimSuffix(u, "&#34;"), "&#39;"); q != u {
				end, u = u[len(q):]+end, q
			} else if n := len(u); n > 0 && strings.IndexByte(".,:;!?", u[n-1]) >= 0 && !endEntity.MatchString(u) {
				end, u = u[n-1:]+end, u[:n-1]
			} else {
				break
			}
		}
		return `<a href="` + u + `">` + u + `</a>` + end
	})
}

func inlineHTML(s string) string {
	var b strings.Builder
	for _, sp := range md.Inline(s) {
		t := esc(sp.Text)
		switch {
		case sp.Code:
			t = "<code>" + t + "</code>"
		case sp.Link != "" && (strings.HasPrefix(sp.Link, "http://") || strings.HasPrefix(sp.Link, "https://")):
			t = `<a href="` + esc(sp.Link) + `">` + t + `</a>`
		default:
			t = linked(t)
		}
		if sp.Bold {
			t = "<b>" + t + "</b>"
		}
		if sp.Italic {
			t = "<i>" + t + "</i>"
		}
		b.WriteString(t)
	}
	return b.String()
}

func or(s, alt string) string {
	if s == "" {
		return alt
	}
	return s
}

func readFile(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

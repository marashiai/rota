package web

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"rota/core"
	"rota/md"
)

// The pages are a mail client for the team: the app in app/ reads the human's mail from
// /api/mail and does what they do through /api/act. Every change to the team folder reaches it
// through /events, and it reads the mail again.

//go:embed app
var appFiles embed.FS

// assets are the app's files with a hash of each, which names it in the page so a browser keeps
// a file only until it changes.
var assets = func() map[string]asset {
	out := map[string]asset{}
	fs.WalkDir(appFiles, "app", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, _ := appFiles.ReadFile(path)
		sum := sha256.Sum256(b)
		out[strings.TrimPrefix(path, "app/")] = asset{b, hex.EncodeToString(sum[:6])}
		return nil
	})
	return out
}()

type asset struct {
	body []byte
	hash string
}

var appPage = template.Must(template.New("app").Parse(string(assets["index.html"].body)))

// appCSP lets the app run only its own script: whatever an agent writes is only ever text.
const appCSP = "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; " +
	"frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

// app serves the app at / and its files; anything else under / is not found.
func (s *site) app(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/")
	if name == "" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Content-Security-Policy", appCSP)
		if r.URL.Query().Get("embed") == "chat" { // only the chat, which a review page of these pages shows over itself: a frame of their own
			w.Header().Set("Content-Security-Policy", strings.Replace(appCSP, "frame-ancestors 'none'", "frame-ancestors 'self'", 1))
			w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		}
		appPage.Execute(w, map[string]string{"Token": s.token, "JS": assets["app.js"].hash, "CSS": assets["app.css"].hash, "Voice": assets["voice.js"].hash})
		return
	}
	a, ok := assets[name]
	if !ok || name == "index.html" {
		http.NotFound(w, r)
		return
	}
	types := map[string]string{".js": "text/javascript; charset=utf-8", ".css": "text/css; charset=utf-8"}
	w.Header().Set("Content-Type", types[name[strings.LastIndex(name, "."):]])
	if r.URL.Query().Get("v") == a.hash {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	w.Write(a.body)
}

// apiThread is a thread as the app reads it: its messages as HTML, with a plain line of each
// for the list and for search.
type apiThread struct {
	core.Thread
	Msgs   []apiMessage `json:"msgs"`
	Review string       `json:"review,omitempty"` // a pull request's review page
	URL    string       `json:"url,omitempty"`    // on GitHub
}

type apiMessage struct {
	From      string     `json:"from"`
	To        string     `json:"to,omitempty"`
	At        time.Time  `json:"at"`
	HTML      string     `json:"html"`
	Plain     string     `json:"plain"`
	Run       string     `json:"run,omitempty"` // the transcript of the shift that said it
	Options   []string   `json:"options,omitempty"`
	Recommend string     `json:"recommend,omitempty"`
	Steps     []string   `json:"steps,omitempty"` // a to-do's steps as HTML, escaped like the rest
	Quotes    []apiQuote `json:"quotes,omitempty"`
}

// apiQuote is a review note a message quotes, its texts as HTML.
type apiQuote struct {
	Where  string `json:"where,omitempty"`
	Note   string `json:"note"`
	Answer string `json:"answer,omitempty"`
}

type apiTop struct {
	State, Dot, Title, Budget string
	Fill                      int
}

type apiMail struct {
	Lists   map[string]core.ListFilter `json:"lists"`   // what narrows Reviews and Issues, kept until the human changes it
	Filters map[string]core.MailFilter `json:"filters"` // what narrows each folder of the mail, kept until the human changes it
	Threads []apiThread                `json:"threads"`
	Members []core.Member              `json:"members"`
	Repos   []core.RepoLabel           `json:"repos"`
	Prefs   core.MailPrefs             `json:"prefs"`
	Top     apiTop                     `json:"top"`
	Now     time.Time                  `json:"now,omitzero"`
}

// pushJSON is what a device needs to hear of the team: the team's VAPID key, and the HTTPS
// address of the pages (a phone allows notifications only over HTTPS); or why there is no key.
func (s *site) pushJSON(w http.ResponseWriter, r *http.Request) {
	t, err := core.Open(s.root, s.conf)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := map[string]string{"https": core.TailscaleHTTPS(t.WebPort)}
	if key, err := t.VAPIDPublicKey(); err != nil {
		out["error"] = err.Error()
	} else {
		out["key"] = key
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(out)
}

// mail is the human's mail, for the app.
func (s *site) mail(w http.ResponseWriter, r *http.Request) {
	t, err := core.Open(s.root, s.conf)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	now := time.Now()
	m := t.Mail(now)
	h := headerOf(t)
	out := apiMail{Members: m.Members, Repos: m.Repos, Prefs: m.Prefs, Threads: []apiThread{}, Filters: m.Filters,
		Lists: map[string]core.ListFilter{"reviews": t.ListFilter(core.ReviewsTab), "issues": t.ListFilter(core.IssuesTab)},
		Top:   apiTop{h.State, h.Dot, h.Title, h.Budget, h.Fill}}
	for _, th := range m.Threads {
		a := apiThread{Thread: th}
		for _, msg := range th.Msgs {
			x := apiMessage{From: msg.From, To: msg.To, At: msg.At, HTML: string(markdown(msg.Text)), Plain: plain(msg.Text), Options: msg.Options, Recommend: msg.Recommend}
			for _, step := range msg.Steps {
				x.Steps = append(x.Steps, inlineHTML(step))
			}
			for _, q := range msg.Quotes {
				a := apiQuote{Where: q.Where, Note: string(markdown(q.Note))}
				if q.Answer != "" {
					a.Answer = string(markdown(q.Answer))
				}
				x.Quotes = append(x.Quotes, a)
			}
			if msg.Log != "" {
				x.Run = "/#run:" + msg.Log
			}
			a.Msgs = append(a.Msgs, x)
		}
		if repo, n, ok := strings.Cut(th.Ref, "#"); ok {
			if th.PR != nil {
				a.Review, a.URL = core.ReviewPage(repo, th.PR.Number), "https://github.com/"+repo+"/pull/"+n
			} else {
				a.URL = "https://github.com/" + repo + "/issues/" + n // GitHub leads a pull request's number there too
			}
		}
		out.Threads = append(out.Threads, a)
	}
	out.Now = time.Time{} // what the mail says, not when it was read: the same mail has the same tag
	body, _ := json.Marshal(out)
	sum := sha256.Sum256(body)
	tag := `"` + hex.EncodeToString(sum[:12]) + `"`
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", tag)
	w.Header().Set("Vary", "Accept-Encoding")
	if r.Header.Get("If-None-Match") == tag { // a running shift changes the team folder every second; the mail, seldom
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Write(body)
		return
	}
	w.Header().Set("Content-Encoding", "gzip") // the reports make it hundreds of kilobytes
	gz := gzip.NewWriter(w)
	gz.Write(body)
	gz.Close()
}

// plain is Markdown as one line of plain text.
func plain(text string) string {
	var parts []string
	var walk func(bs []md.Block)
	walk = func(bs []md.Block) {
		for _, b := range bs {
			switch b.Kind {
			case "code":
				parts = append(parts, strings.Join(b.Lines, " "))
			case "quote":
				walk(b.Blocks)
			case "table":
				for _, row := range b.Rows {
					parts = append(parts, strings.Join(row, " "))
				}
			default:
				var s strings.Builder
				for _, sp := range md.Inline(b.Text) {
					s.WriteString(sp.Text)
				}
				parts = append(parts, s.String())
			}
		}
	}
	walk(md.Parse(text))
	return strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
}

// prOf is the repository and number of a pull request's thread, "pr:owner/name#n".
func prOf(id string) (string, int, error) {
	repo, num, ok := strings.Cut(strings.TrimPrefix(id, "pr:"), "#")
	n, err := strconv.Atoi(num)
	if !strings.HasPrefix(id, "pr:") || !ok || err != nil || n <= 0 || !strings.Contains(repo, "/") {
		return "", 0, fmt.Errorf("that is not a pull request")
	}
	return repo, n, nil
}

// apiAct does what the human did in the app and says what happened, as JSON: {"said": ...}, or
// {"error": ...} with a failing status.
func (s *site) apiAct(w http.ResponseWriter, r *http.Request) {
	reply := func(status int, key, text string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]string{key: text})
	}
	if r.Method != http.MethodPost {
		reply(http.StatusMethodNotAllowed, "error", "post only")
		return
	}
	r.ParseForm()
	if !s.sameOrigin(r) {
		reply(http.StatusForbidden, "error", "refused")
		return
	}
	t, err := core.Open(s.root, s.conf)
	if err != nil {
		reply(http.StatusInternalServerError, "error", err.Error())
		return
	}
	said, err := s.mailAct(t, r.Form)
	if err != nil {
		reply(http.StatusBadRequest, "error", err.Error())
		return
	}
	reply(http.StatusOK, "said", said)
}

// mailAct does one thing the human did: answer or dismiss a question, leave an agent a note,
// keep how they like the inbox, or mark a thread (read, star, archive, snooze, move...).
func (s *site) mailAct(t *core.Team, f map[string][]string) (string, error) {
	get := func(k string) string {
		if v := f[k]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	action, id, arg, text, now := get("action"), get("id"), get("arg"), get("text"), time.Now()
	if said, ok, err := s.teamAct(t, action, arg, text, get("about")); ok {
		return said, err
	}
	switch action {
	case "answer", "dismiss":
		qid, ok := strings.CutPrefix(id, "q:")
		if !ok {
			return "", fmt.Errorf("that is not a question")
		}
		said, err := s.control(t, action, "", qid, "", text)
		if err == nil {
			t.MarkMail(id, "read", "", now)
		}
		return said, err
	case "note": // a message to the agent arg: about a pull request or issue, if any; in a thread, after the others
		thread, _ := strings.CutPrefix(get("thread"), "m:")
		ref := ""
		if thread == "" {
			var err error
			if ref, err = t.NoteRef(arg, get("about")); err != nil {
				return "", err
			}
		}
		if _, err := t.SendMessage(arg, ref, text, thread); err != nil {
			return "", err
		}
		return "sent to " + arg, nil
	case "unsend":
		thread, ok := strings.CutPrefix(id, "m:")
		if !ok {
			return "", fmt.Errorf("that is not a message")
		}
		return "taken back", t.Unsend(thread)
	case "prefs":
		var p core.MailPrefs
		if err := json.Unmarshal([]byte(arg), &p); err != nil {
			return "", fmt.Errorf("prefs: %v", err)
		}
		return "saved", t.SetMailPrefs(p)
	case "filters": // id: the folder; arg: only what changes, as JSON, so what another device chose
		// meanwhile stays (core.ChangeMailFilter says what it may name)
		var change map[string]any
		if err := json.Unmarshal([]byte(arg), &change); err != nil {
			return "", fmt.Errorf("filters: %v", err)
		}
		return "kept", t.ChangeMailFilter(id, change)
	case "approve", "review-note": // as the human on GitHub, the way gloss does
		repo, n, err := prOf(id)
		if err != nil {
			return "", err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second) // within the pages' write deadline
		defer cancel()
		said, warn := "sent", ""
		if action == "approve" {
			said = "approved"
			warn, err = t.ApprovePR(ctx, repo, n)
		} else {
			warn, err = t.NotePR(ctx, repo, n, text)
		}
		if err != nil {
			return "", err
		}
		if warn != "" { // GitHub has it; telling the agents did not work
			said += ", but " + warn
		}
		return said, nil
	case "list": // id: reviews or issues. Only what the form names changes, so what the console or
		// another device chose meanwhile stays: repo and mark set those ("" for all), add and drop
		// a label, clear takes the repository, the mark and every label off, order sets the issues'
		tab := map[string]string{"reviews": core.ReviewsTab, "issues": core.IssuesTab}[id]
		if tab == "" {
			return "", fmt.Errorf("which list?")
		}
		lf := t.ListFilter(tab)
		if _, ok := f["repo"]; ok {
			lf.Repo = get("repo")
		}
		if _, ok := f["mark"]; ok {
			lf.Mark = get("mark")
		}
		if add := get("add"); add != "" {
			lf.Labels = append(lf.Labels, add) // kept once, whatever its case
		}
		if drop := strings.TrimSpace(get("drop")); drop != "" {
			lf.Labels = slices.DeleteFunc(lf.Labels, func(l string) bool { return strings.EqualFold(l, drop) })
		}
		if _, ok := f["clear"]; ok {
			lf = core.ListFilter{Order: lf.Order}
		}
		if _, ok := f["order"]; ok {
			order := get("order")
			if tab != core.IssuesTab || !slices.ContainsFunc(core.IssueOrders, func(o core.Filter) bool { return o.Value == order }) {
				return "", fmt.Errorf("no such order")
			}
			lf.Order = order
		}
		return "saved", t.SetListFilter(tab, lf)
	}
	if action == "read" { // a pull request's thread shows its notes: reading it is seeing them, as opening its review is
		if repo, n, err := prOf(id); err == nil {
			t.SeenReview(repo, n, now)
		}
	}
	return "", t.MarkMail(id, action, arg, now)
}

// issuesJSON are the team's open issues, as GitHub last listed them, in the order the human chose
// for the Issues folder.
func (s *site) issuesJSON(w http.ResponseWriter, r *http.Request) {
	t, err := core.Open(s.root, s.conf)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type issue struct {
		Repo    string      `json:"repo"`
		Number  int         `json:"number"`
		Title   string      `json:"title"`
		URL     string      `json:"url"`
		Created time.Time   `json:"created,omitzero"`
		Marks   []core.Mark `json:"marks,omitempty"`
		Labels  []string    `json:"labels,omitempty"`
		PRs     []string    `json:"prs,omitempty"`  // the threads of the pull requests open for it
		Rank    int         `json:"rank,omitempty"` // its place in the plan, from 1
		Out     bool        `json:"out,omitempty"`  // the plan leaves it out for now
		Why     string      `json:"why,omitempty"`  // why the plan puts it there, or leaves it out
	}
	out := []issue{}
	for _, i := range t.Issues(t.ListFilter(core.IssuesTab).Order) {
		x := issue{Repo: i.Repo, Number: i.Number, Title: i.Title, URL: i.URL(), Created: i.Created, Marks: i.Marks, Labels: i.Labels, Rank: i.Rank, Out: i.Out, Why: i.Why}
		for _, ref := range i.PRs {
			x.PRs = append(x.PRs, "pr:"+ref)
		}
		out = append(out, x)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(out)
}

// issueJSON is one issue of the team's repositories as GitHub has it now (?repo=owner/name&n=7),
// for the reading pane: what it says and every comment on it, oldest first, each drawn as the
// agents' Markdown is.
func (s *site) issueJSON(w http.ResponseWriter, r *http.Request) {
	t, err := core.Open(s.root, s.conf)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	repo := r.URL.Query().Get("repo")
	n, err := strconv.Atoi(r.URL.Query().Get("n"))
	if err != nil || n <= 0 || !t.Reviewable(repo) {
		http.Error(w, "no such issue of the team's repositories", http.StatusNotFound)
		return
	}
	is, err := t.ReadIssue(r.Context(), repo, n)
	if err != nil {
		http.Error(w, "GitHub did not answer: "+err.Error(), http.StatusBadGateway)
		return
	}
	type comment struct {
		Author string    `json:"author"`
		At     time.Time `json:"at"`
		HTML   string    `json:"html"`
	}
	out := struct {
		Title    string    `json:"title"`
		State    string    `json:"state"`
		Reason   string    `json:"reason,omitempty"`
		Author   string    `json:"author"`
		Created  time.Time `json:"created"`
		HTML     string    `json:"html"`
		Labels   []string  `json:"labels,omitempty"`
		Comments []comment `json:"comments"`
	}{Title: is.Title, State: is.State, Reason: is.Reason, Author: is.Author, Created: is.Created, HTML: issueHTML(is.Body), Labels: is.Labels, Comments: []comment{}}
	for _, c := range is.Comments {
		out.Comments = append(out.Comments, comment{c.Author, c.At, issueHTML(c.Body)})
	}
	writeJSON(w, r, out)
}

var (
	htmlComment = regexp.MustCompile(`(?s)<!--.*?-->`)
	htmlImage   = regexp.MustCompile(`(?i)<img\b[^>]*?\bsrc="(https?://[^"\s]+)"[^>]*>`)
)

// issueHTML draws what an issue or a comment says as the agents' Markdown is drawn, everything
// escaped: GitHub's HTML comments (a review note's marker) are left out, as GitHub leaves them
// out, and a picture is a link to it.
func issueHTML(text string) string {
	text = htmlComment.ReplaceAllString(text, "")
	text = htmlImage.ReplaceAllString(text, "[picture]($1)")
	return string(markdown(text))
}

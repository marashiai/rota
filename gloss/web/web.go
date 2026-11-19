// Package web serves gloss's review page: a pull request's whole change, and each commit, with
// notes beside the code, vim-like keys, drafts saved as they are typed, and a Send button.
//
// Mount the handler where it suits; it uses relative paths only:
//
//	mux.Handle("/review/", http.StripPrefix("/review", web.New(client, web.Options{})))
//
// A pull request's page is then /review/<owner>/<name>/<number>/.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"rota/gloss/review"
	"rota/gloss/syntax"
)

//go:embed page.html app.js app.css
var assets embed.FS

var page = template.Must(template.ParseFS(assets, "page.html"))

// Options adjust the page to the app it is part of.
type Options struct {
	// OnSend runs after the reviewer sends drafts or approves (rv.Approved() tells): the place to
	// wake the pull request's author, or whoever merges. Its error is shown to the reviewer; the
	// review is sent either way.
	OnSend func(ctx context.Context, rv *review.Review, sent int) error
	// Title names the app in the page's title ("gloss" when empty).
	Title string
	// Reads keeps what the reviewer has read, shared with the console; review.DefaultReads when
	// nil.
	Reads *review.Reads
	// Chat, when set, is the address of a chat the review page of ref shows over itself, framed,
	// with a button that opens it; the page tells it what the reviewer looks at. The app gloss is
	// part of sets it (rota: its team chat); gloss alone has none.
	Chat func(ref review.Ref) string
}

// Handler serves review pages.
type Handler struct {
	c     *review.Client
	opts  Options
	mu    sync.Mutex
	prs   map[string]*sync.Mutex // one change at a time per pull request
	cache map[string]loaded      // the last review read of each pull request
}

type loaded struct {
	rv *review.Review
	at time.Time
}

// recent is how long a change may start from the review last read, instead of reading it again.
const recent = 20 * time.Second

// load reads a pull request's review, or takes the one read within recent when fresh is false.
func (h *Handler) load(ctx context.Context, ref review.Ref, fresh bool) (*review.Review, bool, error) {
	if !fresh {
		h.mu.Lock()
		l, ok := h.cache[ref.String()]
		h.mu.Unlock()
		if ok && time.Since(l.at) < recent {
			return l.rv, true, nil
		}
	}
	rv, err := h.c.Load(ctx, ref)
	if err == nil {
		h.mu.Lock()
		h.cache[ref.String()] = loaded{rv, time.Now()}
		h.mu.Unlock()
	}
	return rv, false, err
}

// New returns a handler that reads and writes GitHub as c.
func New(c *review.Client, opts Options) *Handler {
	if opts.Title == "" {
		opts.Title = "gloss"
	}
	if opts.Reads == nil {
		opts.Reads = review.DefaultReads()
	}
	return &Handler{c: c, opts: opts, prs: map[string]*sync.Mutex{}, cache: map[string]loaded{}}
}

var route = regexp.MustCompile(`^/([\w.-]+)/([\w.-]+)/(\d+)(/.*)?$`)

var sha = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch path {
	case "", "/":
		h.index(w, r)
		return
	case "/app.js", "/app.css":
		h.asset(w, path[1:])
		return
	}
	m := route.FindStringSubmatch(path)
	if m == nil {
		http.NotFound(w, r)
		return
	}
	n, _ := strconv.Atoi(m[3])
	ref := review.Ref{Owner: m[1], Name: m[2], Number: n}
	rest := m[4]
	switch {
	case rest == "":
		relative(w, m[3]+"/")
	case rest == "/":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		chat := ""
		if h.opts.Chat != nil {
			chat = h.opts.Chat(ref)
		}
		page.Execute(w, map[string]any{"PR": ref.String(), "Title": h.opts.Title, "Chat": chat})
	case rest == "/app.js" || rest == "/app.css":
		h.asset(w, rest[1:])
	case strings.HasPrefix(rest, "/api/"):
		h.api(w, r, ref, strings.TrimPrefix(rest, "/api/"))
	default:
		http.NotFound(w, r)
	}
}

// relative redirects to a location relative to the page, which http.Redirect would make absolute
// and so wrong when the handler is mounted under a prefix.
func relative(w http.ResponseWriter, loc string) {
	w.Header().Set("Location", loc)
	w.WriteHeader(http.StatusFound)
}

func (h *Handler) asset(w http.ResponseWriter, name string) {
	b, err := assets.ReadFile(name)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if strings.HasSuffix(name, ".js") {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	} else {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(b)
}

func (h *Handler) index(w http.ResponseWriter, r *http.Request) {
	if pr := r.URL.Query().Get("pr"); pr != "" {
		ref, err := review.ParseRef(pr, "")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		relative(w, fmt.Sprintf("%s/%s/%d/", ref.Owner, ref.Name, ref.Number))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>%s</title><link rel="stylesheet" href="app.css"><body class="index"><form>
<label>Pull request <input name="pr" placeholder="owner/name#123 or a pull request URL" autofocus size="40"></label>
<button>Open</button></form>`, template.HTMLEscapeString(h.opts.Title))
}

func (h *Handler) lock(ref review.Ref) func() {
	h.mu.Lock()
	m := h.prs[ref.String()]
	if m == nil {
		m = &sync.Mutex{}
		h.prs[ref.String()] = m
	}
	h.mu.Unlock()
	m.Lock()
	return m.Unlock
}

type request struct {
	Note string            `json:"note"` // the note a reply answers, or the draft to edit or delete
	Text string            `json:"text"`
	At   *review.NoteInput `json:"at"` // where a new note goes
}

func (h *Handler) api(w http.ResponseWriter, r *http.Request, ref review.Ref, what string) {
	ctx := r.Context()
	if r.Method != http.MethodGet {
		// a change runs to its end even when the reviewer leaves the page meanwhile: stopped
		// halfway, an approval leaves an empty pending review behind and approves nothing
		ctx = context.WithoutCancel(ctx)
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	fail := func(err error) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
	}
	if r.Method == http.MethodGet {
		switch {
		case what == "review":
			rv, _, err := h.load(ctx, ref, true)
			if err != nil {
				fail(err)
				return
			}
			read, _ := h.opts.Reads.Get(ref.String()) // without it, every note is new
			reply(w, map[string]any{"review": rv, "read": read})
		case strings.HasPrefix(what, "diff/"):
			// a commit's own diff, or the whole change's: diff/<base>...<head>
			key := strings.TrimPrefix(what, "diff/")
			rv := &review.Review{Ref: ref}
			var files []review.File
			var err error
			if base, head, whole := strings.Cut(key, "..."); whole {
				if !sha.MatchString(base) || !sha.MatchString(head) {
					http.NotFound(w, r)
					return
				}
				rv.Base, rv.Head = base, head
				files, err = h.c.Whole(ctx, rv)
			} else {
				if !sha.MatchString(key) {
					http.NotFound(w, r)
					return
				}
				files, err = h.c.Diff(ctx, rv, key)
			}
			if err != nil {
				fail(err)
				return
			}
			w.Header().Set("Cache-Control", "private, max-age=86400") // the diff between two commits never changes
			reply(w, map[string]any{"files": syntax.Highlight(files)})
		default:
			http.NotFound(w, r)
		}
		return
	}
	// Changes come only from the page itself: a custom header cannot be sent cross-site without
	// a preflight this handler never answers.
	if r.Method != http.MethodPost || r.Header.Get("X-Gloss") != "1" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if what == "read" { // notes the reviewer has read: kept on this machine, not on GitHub
		var read review.Read
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&read); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if err := h.opts.Reads.Mark(ref.String(), read); err != nil {
			fail(err)
			return
		}
		reply(w, map[string]any{})
		return
	}
	var req request
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	switch what {
	case "draft", "reply", "edit", "delete", "send", "approve":
	default:
		http.NotFound(w, r)
		return
	}
	defer h.lock(ref)()
	rv, cached, err := h.load(ctx, ref, false)
	if err != nil {
		fail(err)
		return
	}
	out, err := h.change(ctx, rv, what, req)
	var refused *review.APIError
	if cached && errors.As(err, &refused) {
		// GitHub refused, so nothing changed: the review read earlier may be out of date
		if rv, _, err = h.load(ctx, ref, true); err == nil {
			out, err = h.change(ctx, rv, what, req)
		}
	}
	if err != nil {
		fail(err)
		return
	}
	if fresh, _, err := h.load(ctx, ref, true); err == nil {
		rv = fresh
	} // else the change went through: say so, with the review as it was changed
	out["review"] = rv
	reply(w, out)
}

// change makes one change to the review and says what came of it.
func (h *Handler) change(ctx context.Context, rv *review.Review, what string, req request) (map[string]any, error) {
	out := map[string]any{}
	var err error
	switch what {
	case "draft":
		if req.At == nil {
			return nil, errors.New("where does the note go?")
		}
		in := *req.At
		in.Text = req.Text
		out["id"], err = h.c.AddDraft(ctx, rv, in)
	case "reply":
		out["id"], err = h.c.DraftReply(ctx, rv, req.Note, req.Text)
	case "edit":
		err = h.c.EditDraft(ctx, rv, req.Note, req.Text)
	case "delete":
		err = h.c.DeleteDraft(ctx, rv, req.Note)
	case "send", "approve":
		var sent int
		if what == "approve" {
			sent, err = h.c.Approve(ctx, rv)
		} else {
			sent, err = h.c.Send(ctx, rv)
		}
		out["sent"] = sent
		if err == nil && (sent > 0 || what == "approve") && h.opts.OnSend != nil {
			if herr := h.opts.OnSend(ctx, rv, sent); herr != nil {
				out["hook_error"] = herr.Error()
			}
		}
	}
	return out, err
}

func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

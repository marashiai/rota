//go:build darwin || freebsd || netbsd || openbsd || dragonfly || linux

// Package tui is gloss's terminal console: a pull request's whole change, and each commit, with
// notes beside the code, the same vim-like keys as the review page, drafts saved as they are
// typed, and Send.
package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"rota/gloss/review"
	"rota/gloss/syntax"
)

// Options adjust the console to the app it is part of.
type Options struct {
	// OnSend runs after the reviewer sends drafts or approves (rv.Approved() tells): the place to
	// wake the pull request's author, or whoever merges.
	OnSend func(ctx context.Context, rv *review.Review, sent int) error
	// Reads keeps what the reviewer has read, shared with the pages; review.DefaultReads when nil.
	Reads *review.Reads
}

// Run shows the pull request in the terminal until the reviewer quits. It takes the terminal
// over and gives it back as it found it, with nothing left reading from it.
func Run(ctx context.Context, c *review.Client, ref review.Ref, opts Options) error {
	if !IsTerminal(os.Stdin) || !IsTerminal(os.Stdout) {
		return errors.New("the console needs a terminal; gloss web shows the same in a browser")
	}
	t, err := openTerminal(os.Stdin, os.Stdout)
	if err != nil {
		return err
	}
	defer t.close()
	ctx, cancel := context.WithCancel(ctx)
	m := &model{c: c, ref: ref, opts: opts, files: map[string][]review.File{}, loading: map[string]bool{},
		events: make(chan any, 64), jobs: make(chan job, 64), status: "reading " + ref.String() + " from GitHub…",
		reads: opts.Reads, unfold: map[string]bool{}, showRead: map[int]bool{}}
	if m.reads == nil {
		m.reads = review.DefaultReads()
	}
	m.read, _ = m.reads.Get(ref.String()) // without it, every note is new
	m.saved = m.read.Clone()
	defer m.flush()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); m.worker(ctx) }()
	go func() { defer wg.Done(); readKeys(ctx, t.in, m.events) }()
	defer func() { cancel(); close(m.jobs); wg.Wait() }()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGWINCH)
	defer signal.Stop(sig)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	m.w, m.h = t.size()
	m.enqueue(job{kind: "refresh"})
	for {
		m.draw(t.out)
		if m.quit && m.inflight == 0 {
			return nil
		}
		if time.Since(m.flushed) > 5*time.Second {
			m.flush()
		}
		select {
		case ev := <-m.events:
			m.handle(ev)
		case <-sig:
			m.w, m.h = t.size()
			m.layout(true)
		case <-ticker.C:
			if m.ed == nil && m.rv != nil && m.inflight == 0 {
				m.enqueue(job{kind: "refresh", quiet: true})
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// readKeys reads the terminal until ctx ends. Reads time out (VMIN 0, VTIME 1), so the reader
// stops when the console closes instead of taking the next key from the app underneath.
func readKeys(ctx context.Context, in *os.File, events chan<- any) {
	buf := make([]byte, 4096)
	var rest []byte
	for ctx.Err() == nil {
		n, err := in.Read(buf)
		if n > 0 {
			var keys []key
			keys, rest = parseKeys(append(rest, buf[:n]...))
			for _, k := range keys {
				select {
				case events <- k:
				case <-ctx.Done():
					return
				}
			}
		}
		if err != nil && err != io.EOF {
			return
		}
	}
}

// model is the console's state, owned by Run's loop; the worker has its own copy of the review.
type model struct {
	c    *review.Client
	ref  review.Ref
	opts Options

	rv      *review.Review
	view    int                      // 0: the whole change; n: commit n
	files   map[string][]review.File // by diffKey
	loading map[string]bool
	rows    []row
	items   []item
	cur     int // the item under the cursor
	top     int // the first row shown
	w, h    int

	ed        *editor
	focus     string // the note the cursor goes to once it is drawn
	status    string
	statusErr bool
	prev      string // the first key of gg or dd
	ask       *question
	pick      *chooser
	help      bool
	quit      bool
	inflight  int
	first     bool // the review was read once

	reads    *review.Reads
	read     review.Read     // what the reviewer had read when the review opened, and what they folded since: what shows folded
	saved    review.Read     // what the reads keep
	seen     review.Read     // what was on the screen since, for the reads to keep
	flushed  time.Time       // when seen was last kept
	unfold   map[string]bool // threads the reviewer opened again
	showRead map[int]bool    // views whose read notes show one by one

	events chan any
	jobs   chan job
}

type question struct {
	text string
	yes  func()
}

// chooser is a list laid over the view to pick from: the whole change or a commit, or a file.
type chooser struct {
	title string
	items []choice
	cur   int
	top   int
}

type choice struct {
	segs []seg
	run  func()
}

func (m *model) say(msg string, err bool) { m.status, m.statusErr = msg, err }

// ---- the worker: every GitHub call that changes something, one at a time, in order

type job struct {
	kind   string // refresh, draft, reply, edit, delete, send, approve
	at     review.NoteInput
	note   string
	text   string
	ed     *editor
	quiet  bool
	silent bool
}

type done struct {
	job
	rv      *review.Review
	id      string
	sent    int
	hookErr error
	err     error
}

type filesRead struct {
	key   string
	files []review.File
	err   error
}

type saveDue struct{ ed *editor }

func (m *model) enqueue(j job) {
	m.inflight++
	m.jobs <- j
}

func (m *model) worker(ctx context.Context) {
	var rv *review.Review // the worker's own; the loop gets clones
	var read time.Time
	load := func() error {
		r, err := m.c.Load(ctx, m.ref)
		if err == nil {
			rv, read = r, time.Now()
		}
		return err
	}
	for j := range m.jobs {
		d := done{job: j}
		cached := rv != nil && j.kind != "refresh" && time.Since(read) < 20*time.Second
		if !cached {
			d.err = load()
		}
		if d.err == nil && j.kind != "refresh" {
			d.id, d.sent, d.hookErr, d.err = m.change(ctx, rv, j)
			var refused *review.APIError
			if cached && errors.As(d.err, &refused) { // nothing changed: retry on a fresh review
				if d.err = load(); d.err == nil {
					d.id, d.sent, d.hookErr, d.err = m.change(ctx, rv, j)
				}
			}
			load() // when this fails, the change still went through: the review as it was changed is shown
		}
		if rv != nil {
			d.rv = rv.Clone()
		}
		select {
		case m.events <- d:
		case <-ctx.Done():
			return
		}
	}
}

func (m *model) change(ctx context.Context, rv *review.Review, j job) (id string, sent int, hookErr, err error) {
	switch j.kind {
	case "draft":
		in := j.at
		in.Text = j.text
		id, err = m.c.AddDraft(ctx, rv, in)
	case "reply":
		id, err = m.c.DraftReply(ctx, rv, j.note, j.text)
	case "edit":
		err = m.c.EditDraft(ctx, rv, j.note, j.text)
		id = j.note
	case "delete":
		err = m.c.DeleteDraft(ctx, rv, j.note)
	case "send":
		sent, err = m.c.Send(ctx, rv)
		if err == nil && sent > 0 && m.opts.OnSend != nil {
			hookErr = m.opts.OnSend(ctx, rv, sent)
		}
	case "approve":
		sent, err = m.c.Approve(ctx, rv)
		if err == nil && m.opts.OnSend != nil {
			hookErr = m.opts.OnSend(ctx, rv, sent)
		}
	}
	return
}

// diffKey names view v's diff: the whole change's, from the base to the head, or a commit's own.
func (m *model) diffKey(v int) string {
	if v == 0 {
		return m.rv.Base + "..." + m.rv.Head
	}
	return m.rv.Commits[v-1].SHA
}

// loadDiff reads view v's diff when it is not here yet.
func (m *model) loadDiff(v int) {
	key := m.diffKey(v)
	if m.loading[key] || m.files[key] != nil {
		return
	}
	m.loading[key] = true
	rv := &review.Review{Ref: m.ref, Base: m.rv.Base, Head: m.rv.Head}
	go func() {
		var files []review.File
		var err error
		if v == 0 {
			files, err = m.c.Whole(context.Background(), rv)
		} else {
			files, err = m.c.Diff(context.Background(), rv, key)
		}
		if err == nil {
			files = syntax.Highlight(files)
		}
		m.events <- filesRead{key, files, err}
	}()
}

// ---- events

func (m *model) handle(ev any) {
	switch e := ev.(type) {
	case key:
		m.key(e)
	case filesRead:
		m.loading[e.key] = false
		if e.err != nil {
			m.say(e.err.Error(), true)
			return
		}
		m.files[e.key] = e.files
		m.layout(true)
	case saveDue:
		if m.ed == e.ed {
			m.save(false)
		}
	case done:
		m.inflight--
		if e.rv != nil {
			m.rv = e.rv
			if m.view > len(m.rv.Commits) { // a force-push took commits away
				m.go_(0)
			}
		}
		m.finished(e)
		m.layout(true)
	}
}

func (m *model) finished(d done) {
	switch d.kind {
	case "refresh":
		switch {
		case d.err != nil:
			m.say(d.err.Error(), true)
		case !m.first:
			m.first = true
			m.say("", false)
			if n := m.read.New(m.rv, m.rv.Threads); n > 0 {
				m.say(fmt.Sprintf("%d new note%s · } goes to the next", n, plural(n)), false)
			}
		case !d.quiet:
			m.say("reloaded", false)
		}
	case "draft", "reply", "edit":
		ed := d.ed
		if ed == nil {
			return
		}
		ed.saving--
		if d.err != nil {
			ed.failed = true
			ed.state = "not saved: " + d.err.Error()
			if ed.closing {
				ed.closing, ed.warned = false, true
				ed.state += " · Esc again discards it"
			}
			return
		}
		if d.id != "" {
			ed.id = d.id
		}
		ed.saved = d.text
		ed.failed = false
		ed.state = "saved as a draft: only you see it until you send"
		m.settle(ed)
	case "delete":
		if d.err != nil {
			m.say("not deleted: "+d.err.Error(), true)
		} else if !d.silent {
			m.say("draft deleted", false)
		}
	case "approve":
		notes := ""
		if d.sent > 0 {
			notes = fmt.Sprintf(", with %d note%s", d.sent, plural(d.sent))
		}
		switch {
		case d.err != nil:
			m.say("not approved: "+d.err.Error(), true)
		case d.hookErr != nil:
			m.say(fmt.Sprintf("approved%s · but telling the team failed: %v", notes, d.hookErr), true)
		default:
			m.say(fmt.Sprintf("approved%s: the team may merge it", notes), false)
		}
	case "send":
		switch {
		case d.err != nil:
			m.say("not sent: "+d.err.Error(), true)
		case d.sent == 0:
			m.say("no drafts to send", false)
		case d.hookErr != nil:
			m.say(fmt.Sprintf("sent %d notes · but telling the author failed: %v", d.sent, d.hookErr), true)
		default:
			m.say(fmt.Sprintf("sent %d note%s", d.sent, plural(d.sent)), false)
		}
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// ---- keys

func (m *model) key(k key) {
	if m.ask != nil {
		q := m.ask
		m.ask = nil
		if k.Rune == 'y' || k.Name == "enter" {
			q.yes()
		} else {
			m.say("", false)
		}
		return
	}
	if m.help {
		m.help = false
		return
	}
	if m.pick != nil {
		m.pickKey(k)
		return
	}
	if m.ed != nil {
		m.edit(k)
		return
	}
	if m.rv == nil {
		if k.Rune == 'q' || k.Name == "ctrl+c" {
			m.quit = true
		}
		return
	}
	prev := m.prev
	m.prev = ""
	switch {
	case k.Rune == 'j' || k.Name == "down":
		m.move(1)
	case k.Rune == 'k' || k.Name == "up":
		m.move(-1)
	case k.Name == "wheeldown":
		m.move(3)
	case k.Name == "wheelup":
		m.move(-3)
	case k.Rune == 'g':
		if prev == "g" {
			m.setCur(0)
		} else {
			m.prev = "g"
		}
	case k.Rune == 'G':
		m.setCur(len(m.items) - 1)
	case k.Name == "ctrl+d" || k.Name == "pgdn":
		m.page(1)
	case k.Name == "ctrl+u" || k.Name == "pgup":
		m.page(-1)
	case k.Rune == ']':
		m.jump(func(it item) bool { return it.kind == "hunk" }, 1)
	case k.Rune == '[':
		m.jump(func(it item) bool { return it.kind == "hunk" }, -1)
	case k.Rune == '}':
		m.jump(m.noteStop, 1)
	case k.Rune == '{':
		m.jump(m.noteStop, -1)
	case k.Rune == 'z':
		m.fold()
	case k.Name == "enter":
		if it, ok := m.item(); ok && (it.kind == "fold" || it.kind == "folds") {
			m.fold()
		}
	case k.Rune == 'Z':
		m.readAll()
	case k.Rune == '.':
		m.jump(func(it item) bool { return it.kind == "file" }, 1)
	case k.Rune == ',':
		m.jump(func(it item) bool { return it.kind == "file" }, -1)
	case k.Rune == 'J':
		m.go_(m.view + 1)
	case k.Rune == 'K':
		m.go_(m.view - 1)
	case k.Rune == 'o':
		m.go_(0)
	case k.Rune == 'v':
		m.pickView()
	case k.Rune == 'f':
		m.pickFile()
	case k.Rune == 'c':
		m.compose()
	case k.Rune == 'C':
		m.composeWhole()
	case k.Rune == 'r':
		m.reply()
	case k.Rune == 'e':
		m.editDraft()
	case k.Rune == 'x':
		m.deleteDraft()
	case k.Rune == 'd':
		if prev == "d" {
			m.deleteDraft()
		} else {
			m.prev = "d"
		}
	case k.Rune == 'S':
		m.send()
	case k.Rune == 'A':
		m.approve()
	case k.Rune == 'R':
		m.enqueue(job{kind: "refresh"})
		m.say("reading from GitHub…", false)
	case k.Rune == '?':
		m.help = true
	case k.Rune == 'q' || k.Name == "ctrl+c":
		m.quit = true
	}
}

// noteStop is where } and { stop: at each open thread, and at each new note in it.
func (m *model) noteStop(it item) bool {
	return it.kind == "note" && (it.thread.Notes[0] == it.note || !m.read.Has(m.rv, it.thread, it.note))
}

// fold folds the thread under the cursor, which marks it read, or opens a folded one, or shows
// the read notes of the view one by one.
func (m *model) fold() {
	it, ok := m.item()
	if !ok {
		return
	}
	switch {
	case it.kind == "folds": // the cursor goes onto the first of them
		at := m.cur
		m.showRead[m.view] = true
		m.layout(true)
		for i := at; i < len(m.items); i++ {
			if m.items[i].kind == "fold" {
				m.setCur(i)
				break
			}
		}
	case it.kind == "fold":
		m.unfold[it.thread.ID] = true
		m.focus = it.note.ID
		m.layout(true)
	case it.kind == "note":
		t := it.thread
		for _, n := range t.Notes {
			if n.Draft {
				m.say("a thread with your draft stays open", false)
				return
			}
		}
		first, last := m.cur, m.cur
		for first > 0 && m.items[first-1].thread == t && m.items[first-1].kind == "note" {
			first--
		}
		for last+1 < len(m.items) && m.items[last+1].thread == t && m.items[last+1].kind == "note" {
			last++
		}
		next := ""
		if last+1 < len(m.items) {
			next = m.items[last+1].key()
		}
		delete(m.unfold, t.ID)
		at := review.Latest(t)
		m.read.Mark(t, at)
		m.seen.Mark(t, at)
		m.flush()
		m.focus = t.Notes[0].ID
		m.layout(true)
		if m.focus != "" { // it went into the line of read ones: the cursor goes on to what came next
			m.focus = ""
			m.setCur(first)
			for i, x := range m.items {
				if next != "" && x.key() == next {
					m.setCur(i)
					break
				}
			}
		}
	case it.thread != nil: // a line with notes under it
		m.say("z folds a note: move onto it first", false)
	}
}

// readAll marks every note read, and folds them.
func (m *model) readAll() {
	m.read.MarkAll(m.rv)
	m.seen.MarkAll(m.rv)
	clear(m.unfold)
	m.flush()
	m.layout(true)
	m.say("you have read every note: they show folded", false)
}

// flush has the reads keep what was on the screen.
func (m *model) flush() {
	m.flushed = time.Now()
	if m.seen.All.IsZero() && len(m.seen.Threads) == 0 {
		return
	}
	if err := m.reads.Mark(m.ref.String(), m.seen); err != nil {
		m.say("what you read is not kept: "+err.Error(), true)
		return
	}
	m.saved.Add(m.seen)
	m.seen = review.Read{}
}

func (m *model) item() (item, bool) {
	if m.cur < 0 || m.cur >= len(m.items) {
		return item{}, false
	}
	return m.items[m.cur], true
}

func (m *model) setCur(i int) {
	if len(m.items) == 0 {
		return
	}
	m.cur = max(0, min(len(m.items)-1, i))
	m.scroll()
}

func (m *model) move(d int) { m.setCur(m.cur + d) }

func (m *model) jump(pred func(item) bool, dir int) {
	for i := m.cur + dir; i >= 0 && i < len(m.items); i += dir {
		if pred(m.items[i]) {
			m.setCur(i)
			return
		}
	}
}

// page moves half a screen: the view scrolls and the cursor goes with it.
func (m *model) page(dir int) {
	half := max(1, m.contentHeight()/2)
	m.top = max(0, min(len(m.rows)-1, m.top+dir*half))
	it, ok := m.item()
	if !ok {
		return
	}
	target := it.first + dir*half
	i := m.cur
	for i+dir >= 0 && i+dir < len(m.items) && (dir > 0 && m.items[i].first < target || dir < 0 && m.items[i].first > target) {
		i += dir
	}
	m.setCur(i)
}

func (m *model) go_(v int) {
	if m.rv == nil {
		return
	}
	v = max(0, min(len(m.rv.Commits), v))
	m.view, m.cur, m.top = v, 0, 0
	m.layout(false)
}

// ---- the choosers

func (m *model) pickKey(k key) {
	p := m.pick
	switch {
	case k.Rune == 'j' || k.Name == "down" || k.Name == "wheeldown":
		p.cur = min(len(p.items)-1, p.cur+1)
	case k.Rune == 'k' || k.Name == "up" || k.Name == "wheelup":
		p.cur = max(0, p.cur-1)
	case k.Rune == 'g':
		p.cur = 0
	case k.Rune == 'G':
		p.cur = len(p.items) - 1
	case k.Name == "enter" || k.Rune == 'l':
		m.pick = nil
		if p.cur >= 0 && p.cur < len(p.items) {
			p.items[p.cur].run()
		}
	case k.Name == "esc" || k.Rune == 'q' || k.Rune == 'v' || k.Rune == 'f' || k.Name == "ctrl+c":
		m.pick = nil
	}
}

// pickView lists the whole change and the commits, with their notes.
func (m *model) pickView() {
	p := &chooser{title: "The whole change, or a commit · Enter opens · Esc closes", cur: m.view}
	on := func(sha string) (ts []*review.Thread) {
		for _, t := range m.rv.Threads {
			if t.Commit == sha {
				ts = append(ts, t)
			}
		}
		return ts
	}
	add := func(v int, segs []seg, ts []*review.Thread) {
		if n := len(ts); n > 0 {
			segs = append(segs, seg{sDim, fmt.Sprintf("  %d note%s", n, plural(n))})
		}
		if n := m.read.New(m.rv, ts); n > 0 {
			segs = append(segs, seg{sNew, fmt.Sprintf(" · %d new", n)})
		}
		p.items = append(p.items, choice{segs, func() { m.go_(v) }})
	}
	add(0, []seg{{sBold, "    the whole change"}}, on(""))
	for i, c := range m.rv.Commits {
		add(i+1, []seg{{"", fmt.Sprintf(" %2d ", i+1)}, {sDim, short(c.SHA) + " "}, {"", c.Title}}, on(c.SHA))
	}
	m.pick = p
}

// pickFile lists the view's files, with their notes, to jump to one.
func (m *model) pickFile() {
	files, ok := m.files[m.diffKey(m.view)]
	if !ok {
		m.say("the diff is still being read", false)
		return
	}
	if len(files) == 0 {
		m.say("no files in this diff", false)
		return
	}
	p := &chooser{title: "Files · Enter jumps to one · Esc closes"}
	placed := m.placed(files)
	here := ""
	if it, ok := m.item(); ok {
		here = it.path
	}
	for _, f := range files {
		path := f.Path
		segs := []seg{{"", " " + path + " "}, {sDim, fmt.Sprintf("%s +%d −%d", f.Status, f.Added(), f.Deleted())}}
		if n := placed.OnFile(path); n > 0 {
			segs = append(segs, seg{sDim, fmt.Sprintf("  %d note%s", n, plural(n))})
		}
		if path == here {
			p.cur = len(p.items)
		}
		p.items = append(p.items, choice{segs, func() {
			for i, it := range m.items {
				if it.kind == "file" && it.path == path {
					m.setCur(i)
					m.top = it.first
					return
				}
			}
		}})
	}
	m.pick = p
}

// placed is where the view's threads show in files, its diff.
func (m *model) placed(files []review.File) review.Placed {
	if m.view == 0 {
		return m.rv.PlaceWhole(files)
	}
	return m.rv.Place(m.view-1, files)
}

func (m *model) commitSHA() string {
	if m.view == 0 {
		return ""
	}
	return m.rv.Commits[m.view-1].SHA
}

func short(sha string) string { return sha[:min(7, len(sha))] }

func (m *model) compose() {
	it, ok := m.item()
	if !ok {
		return
	}
	sha := m.commitSHA()
	switch it.kind {
	case "line":
		side := "+"
		if it.old {
			side = "−"
		}
		m.open(&editor{mode: "new", at: review.NoteInput{Commit: sha, Path: it.path, Line: it.line, Old: it.old}, label: fmt.Sprintf("%s:%s%d", it.path, side, it.line)})
	case "hunk":
		m.open(&editor{mode: "new", at: review.NoteInput{Commit: sha, Path: it.path, Hunk: it.hunk}, label: fmt.Sprintf("hunk %d of %s", it.hunk, it.path)})
	case "file":
		m.open(&editor{mode: "new", at: review.NoteInput{Commit: sha, Path: it.path}, label: it.path})
	case "commit":
		m.open(&editor{mode: "new", at: review.NoteInput{Commit: sha}, label: "commit " + short(sha)})
	case "note", "fold":
		m.reply()
	case "folds":
		if sha != "" {
			m.open(&editor{mode: "new", at: review.NoteInput{Commit: sha}, label: "commit " + short(sha)})
		} else {
			m.composeWhole()
		}
	default:
		m.composeWhole()
	}
}

func (m *model) composeWhole() {
	m.open(&editor{mode: "new", label: "the whole change"})
}

func (m *model) reply() {
	it, ok := m.item()
	if !ok || it.thread == nil {
		m.say("no note here to reply to", false)
		return
	}
	if it.kind == "fold" { // open it, so its notes show above the reply
		m.unfold[it.thread.ID] = true
		m.focus = it.thread.Notes[len(it.thread.Notes)-1].ID
		m.layout(true)
	}
	m.open(&editor{mode: "reply", note: it.thread.ID, label: "a reply to " + it.thread.Notes[0].Author})
}

func (m *model) editDraft() {
	it, ok := m.item()
	if !ok || it.kind != "note" || !it.note.Draft {
		m.say("only your drafts can be edited", false)
		return
	}
	ed := &editor{mode: "edit", note: it.note.ID, id: it.note.ID, label: "your draft", text: []rune(it.note.Text), saved: it.note.Text}
	ed.pos = len(ed.text)
	m.open(ed)
}

func (m *model) deleteDraft() {
	it, ok := m.item()
	if !ok || it.kind != "note" || !it.note.Draft {
		m.say("only your drafts can be deleted", false)
		return
	}
	id := it.note.ID
	m.ask = &question{"Delete this draft? y/n", func() {
		m.enqueue(job{kind: "delete", note: id})
		m.say("deleting…", false)
	}}
}

func (m *model) approve() {
	switch {
	case m.rv == nil:
		return
	case m.rv.State != "OPEN":
		m.say("it is "+strings.ToLower(m.rv.State)+": nothing to approve", false)
		return
	case m.rv.Approved():
		m.say("already approved", false)
		return
	}
	q := "Approve this pull request?"
	if n := m.rv.Drafts(); n > 0 {
		q = fmt.Sprintf("Send your %d draft%s and approve this pull request?", n, plural(n))
	}
	m.ask = &question{q + " The team may merge it once nothing holds it back. y/n", func() {
		m.enqueue(job{kind: "approve"})
		m.say("approving…", false)
	}}
}

func (m *model) send() {
	n := m.rv.Drafts()
	if n == 0 {
		m.say("no drafts to send", false)
		return
	}
	m.ask = &question{fmt.Sprintf("Send your %d draft%s as one review? The author is told. y/n", n, plural(n)), func() {
		m.enqueue(job{kind: "send"})
		m.say("sending…", false)
	}}
}

package tui

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"rota/core"
)

// ---- a scrollable document ----

type pane struct {
	key    string // what lines were built from; a new key builds them again
	lines  []string
	marks  []int
	top, h int
	live   bool // it grows (a running shift): at the end, it stays at the end
	follow bool
	query  string
	hits   []int
}

type doc struct {
	key   string
	build func() ([]string, []int)
	empty string
	live  bool
}

func (p *pane) maxTop() int { return max(0, len(p.lines)-p.h) }

func (p *pane) clamp() {
	if p.follow {
		p.top = p.maxTop()
	}
	p.top = max(0, min(p.top, p.maxTop()))
}

func (p *pane) scroll(d int) {
	p.top, p.follow = p.top+d, false
	p.clamp()
	p.follow = p.live && d > 0 && p.top >= p.maxTop()
}

func (p *pane) jump(dir int) {
	for i := range p.marks {
		m := p.marks[i]
		if dir < 0 {
			m = p.marks[len(p.marks)-1-i]
		}
		if dir > 0 && m > p.top || dir < 0 && m < p.top {
			p.top, p.follow = m, false
			p.clamp()
			return
		}
	}
	if dir < 0 {
		p.top = 0
	}
}

// find marks the lines that hold the query and moves to the next one; dir 0 starts at the top line.
func (p *pane) find(dir int) bool {
	p.hits = nil
	q := strings.ToLower(p.query)
	for i, l := range p.lines {
		if q != "" && strings.Contains(strings.ToLower(plain(l)), q) {
			p.hits = append(p.hits, i)
		}
	}
	if len(p.hits) == 0 {
		return false
	}
	at, from := -1, p.top+2
	for i := range p.hits {
		h := p.hits[i]
		if dir < 0 {
			h = p.hits[len(p.hits)-1-i]
		}
		if dir > 0 && h > from || dir == 0 && h >= p.top || dir < 0 && h < from {
			at = h
			break
		}
	}
	if at < 0 { // around the end
		at = p.hits[0]
		if dir < 0 {
			at = p.hits[len(p.hits)-1]
		}
	}
	p.top, p.follow = at-2, false
	p.clamp()
	return true
}

// drawPane draws a document in r: a gutter marking search matches, the text, and a
// scrollbar on the right edge when it does not fit.
func (c *console) drawPane(f *frame, r rect, p *pane, d doc) {
	if p.key != d.key {
		p.key = d.key
		p.lines, p.marks = d.build()
		if p.query != "" {
			top := p.top
			p.find(0)
			p.top = top
		}
	}
	p.h, p.live = r.h, d.live
	p.clamp()
	if len(p.lines) == 0 {
		f.put(r.x+1, r.y, sty(c.pal.Muted(), d.empty), r.w-3)
		return
	}
	match := map[int]bool{}
	for _, h := range p.hits {
		match[h] = true
	}
	for i := 0; i < r.h && p.top+i < len(p.lines); i++ {
		if match[p.top+i] {
			f.put(r.x, r.y+i, sty(c.pal.Warn(), "▸"), 1)
		}
		f.put(r.x+1, r.y+i, p.lines[p.top+i], r.w-3)
	}
	c.scrollbar(f, r.x+r.w-1, r.y, r.h, p.top, len(p.lines))
	c.hits = append(c.hits, hit{r: r, wheel: p.scroll})
}

func (c *console) scrollbar(f *frame, x, y, h, top, n int) {
	if n <= h || h < 2 {
		return
	}
	thumb := max(1, h*h/n)
	pos := (h - thumb) * top / max(1, n-h)
	for i := 0; i < h; i++ {
		switch {
		case i >= pos && i < pos+thumb && c.pal.name == "mono":
			f.put(x, y+i, "█", 1)
		case i >= pos && i < pos+thumb:
			f.put(x, y+i, sty(c.pal.Accent(), "┃"), 1)
		default:
			f.put(x, y+i, sty(c.pal.Rule(), "│"), 1)
		}
	}
}

func (c *console) paneKey(p *pane, k key) bool {
	page := max(1, p.h-2)
	switch {
	case k.name == "up" || k.r == 'k':
		p.scroll(-1)
	case k.name == "down" || k.r == 'j':
		p.scroll(1)
	case k.name == "pgup" || k.r == 'b' || k.name == "ctrl+b":
		p.scroll(-page)
	case k.name == "pgdn" || k.r == ' ' || k.name == "ctrl+f":
		p.scroll(page)
	case k.name == "ctrl+u" || k.name == "ctrl+d":
		p.scroll(map[bool]int{true: -page / 2, false: page / 2}[k.name == "ctrl+u"])
	case k.name == "home" || k.r == 'g':
		p.top, p.follow = 0, false
	case k.name == "end" || k.r == 'G':
		p.top, p.follow = p.maxTop(), p.live
	case k.r == ']' || k.r == '[':
		p.jump(map[bool]int{true: 1, false: -1}[k.r == ']'])
	case k.r == '/':
		c.prompt("find: ", p.query, func(q string) {
			p.query = q
			switch {
			case q == "":
				p.hits = nil
			case p.find(0):
				c.say(fmt.Sprintf("%d lines match; n next, N previous", len(p.hits)), false)
			default:
				c.say("no match for "+q, true)
			}
		})
	case k.r == 'n' || k.r == 'N':
		if !p.find(map[bool]int{true: 1, false: -1}[k.r == 'n']) {
			c.say("nothing to find; / searches", true)
		}
	default:
		return false
	}
	return true
}

// ---- a list on the left, the selected item on the right ----

type item struct{ id, label string }

type browser struct {
	name, empty string
	items       func(c *console) []item
	title       func(c *console, id string) string
	doc         func(c *console, id string, w int) doc
	agent       func(c *console, id string) string // the agent an item is about, for the agent keys
	own         func(c *console, b *browser, k key) bool
	ownHints    []hint

	list   []item
	loaded time.Time
	sel    int
	top    int
	focus  bool // the reader has the keys
	want   string
	view   pane
	viewID string
}

func (b *browser) open(id string) { b.want, b.focus, b.loaded = id, true, time.Time{} }

// ensure reloads the items when they are two seconds old, or one is wanted.
func (b *browser) ensure(c *console) {
	if time.Since(b.loaded) < 2*time.Second && b.want == "" {
		return
	}
	cur := b.want
	if cur == "" && b.sel < len(b.list) {
		cur = b.list[b.sel].id
	}
	b.list, b.loaded, b.sel, b.want = b.items(c), time.Now(), 0, ""
	for i, it := range b.list {
		if it.id == cur {
			b.sel = i
		}
	}
}

func keepVisible(top, sel, h, n int) int {
	top = min(top, sel)
	if sel >= top+h {
		top = sel - h + 1
	}
	return max(0, min(top, n-h))
}

func (b *browser) draw(c *console, f *frame, a rect) {
	p := c.pal
	b.ensure(c)
	if len(b.list) == 0 {
		f.put(margin, a.y+a.h/3, sty(p.Muted(), b.empty), a.w-2*margin)
		return
	}
	lw := max(26, min(44, a.w/3))
	gap := 2 - btoi(a.h < 16)
	listSt, viewSt := p.Head(), bold
	if b.focus {
		listSt, viewSt = bold, p.Head()
	}
	f.put(margin, a.y, sty(listSt, b.name)+sty(p.Muted(), fmt.Sprintf("  %d", len(b.list))), lw-margin-1)
	body := rect{0, a.y + gap, lw, a.h - gap}
	b.top = keepVisible(b.top, b.sel, body.h, len(b.list))
	for i := 0; i < body.h && b.top+i < len(b.list); i++ {
		f.put(margin, body.y+i, b.list[b.top+i].label, lw-margin-1)
		if b.top+i == b.sel {
			f.band(p, 0, body.y+i, lw, !b.focus)
		}
	}
	for y := a.y; y < a.y+a.h; y++ {
		f.put(lw, y, sty(p.Rule(), "│"), 1)
	}
	c.hits = append(c.hits, hit{r: body, click: func(_, y int) {
		if n := b.top + y; n < len(b.list) {
			b.focus, b.sel = n == b.sel, n // a second click on the same item reads it
		}
	}, wheel: func(d int) { b.sel = max(0, min(len(b.list)-1, b.sel+d/3)) }})
	id := b.list[b.sel].id
	f.put(lw+3, a.y, sty(viewSt, b.title(c, id)), a.w-lw-3-margin)
	r := rect{lw + 2, a.y + gap, a.w - lw - 3, a.h - gap}
	d := b.doc(c, id, r.w-3)
	if b.viewID != id {
		b.viewID, b.view = id, pane{follow: d.live}
	}
	c.drawPane(f, r, &b.view, d)
	c.hits = append(c.hits, hit{r: rect{lw + 1, a.y, a.w - lw - 1, a.h}, click: func(int, int) { b.focus = true }, wheel: b.view.scroll})
}

func (b *browser) key(c *console, k key) bool {
	b.ensure(c)
	id := ""
	if b.sel < len(b.list) {
		id = b.list[b.sel].id
	}
	if b.own != nil && b.own(c, b, k) {
		return true
	}
	if b.focus {
		if k.name == "left" || k.r == 'h' || k.name == "esc" || k.name == "backspace" {
			b.focus = false
			return true
		}
		if c.paneKey(&b.view, k) {
			return true
		}
	} else {
		switch {
		case k.name == "up" || k.r == 'k':
			b.sel = max(0, b.sel-1)
			return true
		case k.name == "down" || k.r == 'j':
			b.sel = min(len(b.list)-1, b.sel+1)
			return true
		case k.name == "home" || k.r == 'g':
			b.sel = 0
			return true
		case k.name == "end" || k.r == 'G':
			b.sel = max(0, len(b.list)-1)
			return true
		case k.name == "enter" || k.name == "right" || k.r == 'l':
			b.focus = len(b.list) > 0
			return true
		case k.name == "pgdn" || k.r == ' ' || k.name == "pgup" || k.r == 'b' || strings.ContainsRune("/[]nN", k.r) && k.r != 0:
			b.focus = strings.ContainsRune("/[]nN", k.r) && k.r != 0
			return c.paneKey(&b.view, k)
		}
	}
	if b.agent != nil && id != "" {
		return c.agentKey(b.agent(c, id), k)
	}
	return false
}

func (b *browser) hints(c *console) []hint {
	if b.focus {
		return append([]hint{{"↑↓", "scroll"}, {"[ ]", "entry"}, {"/", "find"}, {"←", "back"}}, b.ownHints...)
	}
	return append([]hint{{"↑↓", "select"}, {"⏎", "read"}, {"/", "find"}}, b.ownHints...)
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func fileKey(path string, extra ...any) string {
	st, err := os.Stat(path)
	if err != nil {
		return path
	}
	return fmt.Sprint(path, st.Size(), st.ModTime().UnixNano(), extra)
}

func readFile(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

func (c *console) markdownDoc(path string, w int, empty string) doc {
	return doc{key: fileKey(path, w, c.pal.name), empty: empty, build: func() ([]string, []int) { return c.pal.markdown(readFile(path), w) }}
}

var entryHead = regexp.MustCompile(`(?m)^## \d\d:\d\d `)

func journalPage() *browser {
	return &browser{name: "Days", empty: "The journal is empty. Each shift adds its report here.",
		items: func(c *console) []item {
			var out []item
			for _, p := range c.t.JournalDays() {
				day := strings.TrimSuffix(filepath.Base(p), ".md")
				label := day
				if t, err := time.Parse("2006-01-02", day); err == nil {
					label = t.Format("Mon 02 Jan")
				}
				if n := len(entryHead.FindAllString(readFile(p), -1)); n > 0 {
					label += sty(c.pal.Muted(), fmt.Sprintf("   %d shift%s", n, map[bool]string{true: "", false: "s"}[n == 1]))
				}
				out = append(out, item{p, label})
			}
			return out
		},
		title: func(c *console, id string) string {
			day := strings.TrimSuffix(filepath.Base(id), ".md")
			if t, err := time.Parse("2006-01-02", day); err == nil {
				return t.Format("Monday 2 January 2006")
			}
			return day
		},
		doc: func(c *console, id string, w int) doc { return c.markdownDoc(id, w, "empty") },
	}
}

func reportsPage() *browser {
	return &browser{name: "Reports", empty: "Nothing reported yet. When a shift ends, what you should know from it appears here, newest first.",
		items: func(c *console) []item {
			var out []item
			for _, p := range c.t.Reports() {
				out = append(out, item{p, strings.TrimSuffix(filepath.Base(p), ".md")})
			}
			for _, a := range c.t.Agents { // and what each agent wrote for the human, after the days
				for _, p := range c.t.AgentReports(a.Name) {
					out = append(out, item{p, a.Name + ": " + filepath.Base(p)})
				}
			}
			return out
		},
		title: func(c *console, id string) string {
			if agent := filepath.Base(filepath.Dir(id)); agent != filepath.Base(c.t.Path("reports")) {
				return agent + ": " + filepath.Base(id)
			}
			return "Report of " + strings.TrimSuffix(filepath.Base(id), ".md")
		},
		doc: func(c *console, id string, w int) doc { return c.markdownDoc(id, w, "empty") },
	}
}

func inboxPage() *browser {
	return &browser{name: "Agents", empty: "No agents.",
		items: func(c *console) []item {
			names, nameW := c.t.Inboxes(), 0 // with those of agents that no longer exist
			for _, n := range names {
				nameW = max(nameW, width(n))
			}
			var out []item
			for _, name := range names {
				label := sty(c.pal.Muted(), name)
				if n := len(c.t.Notes(name)); n > 0 {
					label = fit(name, nameW) + " " + sty(join(c.pal.Accent(), bold), rjust(strconv.Itoa(n), 3))
				}
				out = append(out, item{name, label})
			}
			return out
		},
		title: func(c *console, id string) string {
			return "Notes for " + id + sty(c.pal.Muted(), "  as it reads them · i adds one · x deletes one")
		},
		doc: func(c *console, id string, w int) doc {
			path := c.t.Inbox(id)
			return doc{key: fileKey(path, w, c.pal.name), empty: "no notes waiting for " + id + " · i leaves one",
				build: func() ([]string, []int) { return c.pal.markdown(c.t.NotesText(c.t.Notes(id)), w) }}
		},
		agent: func(c *console, id string) string { return id },
		own: func(c *console, b *browser, k key) bool {
			if k.r == 'x' && b.sel < len(b.list) {
				c.forget(b.list[b.sel].id)
				return true
			}
			return false
		},
		ownHints: []hint{{"i", "leave a note"}, {"x", "delete a note"}},
	}
}

func transcriptsPage() *browser {
	outputs := false
	find := func(c *console, id string) core.Transcript {
		for _, tr := range c.t.Transcripts("") {
			if tr.Path == id {
				return tr
			}
		}
		return core.Transcript{Path: id}
	}
	return &browser{name: "Shifts", empty: "No shifts yet. Each shift's transcript appears here as it runs.",
		items: func(c *console) []item {
			var out []item
			for _, tr := range c.t.Transcripts("") {
				label := sty(c.pal.Muted(), tr.Start.Format("Mon 02 15:04")) + "  " + fit(tr.Agent, 10)
				switch {
				case tr.Run != nil:
					g, st := c.pal.outcome(tr.Run.Status)
					label += " " + sty(st, g+" "+shortStatus(tr.Run.Status))
				case tr.Running(c.st):
					label += " " + sty(c.pal.Head(), "● live")
				}
				out = append(out, item{tr.Path, label})
			}
			return out
		},
		title: func(c *console, id string) string {
			tr := find(c, id)
			title := tr.Agent + sty(c.pal.Muted(), "  "+tr.Start.Format("Mon 02 Jan 15:04"))
			switch r := tr.Run; {
			case r != nil:
				g, st := c.pal.outcome(r.Status)
				title += "  " + sty(st, g+" "+shortStatus(r.Status)) + sty(c.pal.Muted(), fmt.Sprintf(" · %d turns · %d min · %s of the pool", r.Turns, r.Seconds/60, r.PctText(c.t.Config)))
			case tr.Running(c.st):
				title += "  " + sty(c.pal.Head(), "● live")
			}
			return title
		},
		doc: func(c *console, id string, w int) doc {
			live := find(c, id).Running(c.st)
			return doc{key: fileKey(id, w, outputs, live, c.pal.name), live: live, empty: "empty", build: func() ([]string, []int) {
				events, _ := core.ReadEvents(id) // a result does not end it: a background job can wake claude again
				lines, marks := c.pal.transcript(events, w, outputs)
				if live {
					lines = append(lines, "", sty(c.pal.Head(), "● the shift is still running"))
				}
				return lines, marks
			}}
		},
		agent: func(c *console, id string) string { return find(c, id).Agent },
		own: func(c *console, b *browser, k key) bool {
			switch {
			case k.r == 'x':
				outputs = !outputs
				c.say(map[bool]string{true: "tool output shown", false: "tool output folded"}[outputs], false)
			case k.r == 'f' && b.focus:
				b.view.follow = !b.view.follow
				b.view.clamp()
			default:
				return false
			}
			return true
		},
		ownHints: []hint{{"x", "tool output"}, {"f", "follow"}},
	}
}

// ---- status ----

// statusPage keeps the selected agent by name: the running agent leads the list, so a shift that
// starts or ends reorders it, and a key acts on the agent the band is on.
type statusPage struct {
	sel  int
	name string
}

// selected is the selected agent's place in the list as it is now, by name; the first, when
// that agent is gone or none was chosen.
func (s *statusPage) selected(c *console) int {
	if i := slices.IndexFunc(c.st.Agents, func(a core.AgentStatus) bool { return a.Name == s.name }); i >= 0 {
		return i
	}
	return max(0, min(s.sel, len(c.st.Agents)-1))
}

// choose selects the agent at i.
func (s *statusPage) choose(c *console, i int) {
	s.sel = i
	if i >= 0 && i < len(c.st.Agents) {
		s.name = c.st.Agents[i].Name
	}
}

func (s *statusPage) draw(c *console, f *frame, a rect) {
	p := c.pal
	if len(c.st.Agents) == 0 && c.loadErr == "" && len(c.t.Agents) > 0 {
		f.put(margin, a.y+a.h/3, sty(p.Muted(), "Reading the team and Claude's usage…"), a.w)
		return
	}
	w, y := a.w-2*margin, a.y
	put := func(l string) {
		if y < a.y+a.h {
			f.put(margin, y, l, w)
		}
		y++
	}
	for _, l := range p.statusHead(c.st, c.t.Paused(), w, true) {
		put(l)
	}
	if c.loadErr != "" {
		put(sty(p.Bad(), "✗ "+c.loadErr))
	}
	for _, l := range c.t.Warnings {
		put(sty(p.Warn(), "▲ "+l))
	}
	if len(c.questions) > 0 {
		y++
		put(p.section("Questions for you", "a answers · D dismisses", w))
		for _, q := range c.questions {
			first := sty(join(p.Warn(), bold), "? ") + sty(bold, q.Agent) + sty(p.Muted(), " "+q.Asked.Local().Format("Mon 15:04")) + "  "
			for _, l := range wrap([]span{{q.Text, ""}}, w, first, strings.Repeat(" ", width(first))) {
				put(l)
			}
		}
	}
	y++
	table := p.agentTable(c.st, w)
	// A running shift's latest steps come before the table, in what the table leaves: at most
	// liveLines, none when fewer than 3 fit. The head's Shift line says what it does meanwhile.
	if rest := a.y + a.h - y - len(table) - 2; c.st.Running != nil && rest >= 3 {
		for _, l := range p.activity(c.t, c.st, w, min(rest, liveLines)) {
			put(l)
		}
		y++
	}
	enabled := 0
	for _, a := range c.st.Agents {
		enabled += btoi(a.Enabled)
	}
	put(p.section("Agents", fmt.Sprintf("%d enabled of %d", enabled, len(c.st.Agents)), w))
	put(table[0])
	s.choose(c, s.selected(c))
	for i, l := range table[1:] {
		row, rowY := i, y
		put(l)
		if i == s.sel {
			f.band(p, 0, rowY, a.w, true)
		}
		c.hits = append(c.hits, hit{r: rect{0, rowY, a.w, 1}, click: func(int, int) {
			if s.sel == row {
				c.openAgent(c.st.Agents[row].Name)
			}
			s.choose(c, row)
		}})
	}
	y++
	if rest := a.y + a.h - y; rest >= 3 && c.st.Running == nil {
		for _, l := range p.activity(c.t, c.st, w, rest) {
			put(l)
		}
	}
}

// liveLines is the most a running shift takes of the status page, its heading included.
const liveLines = 8

func (s *statusPage) key(c *console, k key) bool {
	n, i := len(c.st.Agents), s.selected(c)
	switch {
	case k.name == "up" || k.r == 'k':
		s.choose(c, max(0, i-1))
	case k.name == "down" || k.r == 'j':
		s.choose(c, min(n-1, i+1))
	case k.name == "home" || k.r == 'g':
		s.choose(c, 0)
	case k.name == "end" || k.r == 'G':
		s.choose(c, n-1)
	case (k.name == "enter" || k.name == "right" || k.r == 'l') && i < n:
		c.openAgent(c.st.Agents[i].Name)
	case i < n:
		return c.agentKey(c.st.Agents[i].Name, k)
	default:
		return false
	}
	return true
}

func (s *statusPage) hints(c *console) []hint {
	return []hint{{"↑↓", "agent"}, {"⏎", "settings"}, {"e", "enable"}, {"P", "pause"}, {"r", "run"}, {"i", "note"}, {"o", "transcript"}}
}

func (c *console) openAgent(name string) {
	ap := c.pages[tabAgents].(*agentsPage)
	for i, a := range c.t.Agents {
		if a.Name == name {
			ap.sel, ap.focus = i, true
		}
	}
	c.tab = tabAgents
}

// statusHead is the top of the status page: the organizer, the running shift, Claude's
// windows, the team pool, hosts and who is next. rota status prints it too.
func (p palette) statusHead(st core.Status, paused bool, w int, console bool) []string {
	label := func(s string) string { return sty(p.Muted(), fmt.Sprintf("%-11s", s)) }
	org := sty(p.OK(), "● running") + sty(p.Muted(), " since "+st.StartedAt.Local().Format("Mon 15:04"))
	switch {
	case st.Pid == 0 && console:
		org = sty(p.Muted(), "○ not running · s starts it")
	case st.Pid == 0:
		org = sty(p.Muted(), "○ not running · rota start, the menu bar, or rota (the console)")
	case st.Stale:
		org = sty(p.Warn(), "◌ "+st.Organizer())
	case paused:
		org = sty(p.Warn(), "‖ paused") + sty(p.Muted(), ": the running shift finishes, no new one starts")
	case st.Held() != "":
		org = sty(p.Warn(), "▲ on hold") + sty(p.Muted(), ": "+st.Held()+" · next look "+st.NextTick.Local().Format("15:04"))
	case st.Running == nil && !st.NextTick.IsZero():
		org += sty(p.Muted(), " · next look "+st.NextTick.Local().Format("15:04"))
	}
	out := []string{label("Organizer") + org}
	if r := st.Running; r != nil {
		out = append(out, label("Shift")+sty(p.Head(), "▸ "+r.Agent)+sty(p.Muted(), fmt.Sprintf(" on %s · %d min · ", r.Host, int(time.Since(r.Since).Minutes())))+r.Live)
	}
	switch u := st.Usage; {
	case u != nil:
		for _, bw := range []int{12, 6} {
			gauge := func(name string, pct float64) string {
				return name + "  " + p.bar(pct, 100, bw) + fmt.Sprintf(" %3.0f%%", pct)
			}
			parts := []string{gauge("session", u.Session), gauge("weekly", u.Weekly)}
			if u.ScopedName != "" {
				parts = append(parts, gauge(u.ScopedName, u.Scoped))
			}
			line := label("Claude") + strings.Join(parts, "    ")
			if !u.ResetsAt.IsZero() {
				line += sty(p.Muted(), "    resets "+u.ResetsAt.Local().Format("Mon 15:04"))
			}
			if width(line) <= w || bw == 6 {
				out = append(out, line)
				break
			}
		}
	case st.UsageError != "":
		out = append(out, label("Claude")+sty(p.Warn(), "▲ usage cannot be read: "+st.UsageError))
	default:
		out = append(out, label("Claude")+sty(p.Muted(), "usage data off"))
	}
	if st.Pool.Enforced {
		pct := 0.0
		if st.Pool.Quota > 0 {
			pct = st.Pool.Used / st.Pool.Quota * 100
		}
		out = append(out, label("Team pool")+p.bar(pct, 100, 12)+fmt.Sprintf(" %.1f%% used", st.Pool.Of(st.Pool.Used))+
			sty(p.Muted(), fmt.Sprintf(" %s · team_pct %g%% of the weekly window, %s · every percent here is of the pool", st.Pool.Period, st.Pool.TeamPct, st.Pool.Pacing)))
		if f := st.Pool.FreeText(); f != "" {
			out = append(out, label("Free")+f)
		}
	} else {
		out = append(out, label("Team pool")+sty(p.Muted(), "not enforced without Claude usage data"))
	}
	var hosts []string
	for _, h := range st.Hosts {
		hosts = append(hosts, h.String())
	}
	out = append(out, label("Hosts")+or(strings.Join(hosts, " · "), sty(p.Muted(), "none joined")))
	next := sty(p.Muted(), "nobody is ready")
	if st.Next != "" {
		next = sty(bold, st.Next) + sty(p.Muted(), " is ready")
	}
	if !st.WeekSince.IsZero() {
		next += sty(p.Muted(), " · budget week since "+st.WeekSince.Local().Format("Mon 02 Jan 15:04"))
	}
	out = append(out, label("Next")+next)
	for i := range out {
		out[i] = truncate(out[i], w)
	}
	return out
}

// stateGlyph gives each kind of state a shape, so it reads without color.
func (p palette) state(s string) string {
	switch {
	case strings.HasPrefix(s, "ready"):
		return sty(p.OK(), "● "+s)
	case s == "disabled":
		return sty(p.Muted(), "○ "+s)
	case strings.HasPrefix(s, "failed"):
		return sty(p.Bad(), "✗ "+s)
	case strings.HasPrefix(s, "due"), strings.HasPrefix(s, "outside hours"):
		return sty(p.Muted(), "◷ ") + s
	}
	return sty(p.Warn(), "▲ "+s)
}

// agentTable is the agents' table: a header and one line per agent, the columns that matter
// least left out when the width runs short.
func (p palette) agentTable(st core.Status, w int) []string {
	type column struct {
		title string
		right bool
		cells []string
		w     int
	}
	cols := []*column{{title: "agent"}, {title: "state"}, {title: "next run"}, {title: "last run"},
		{title: "runs", right: true}, {title: "share " + strings.TrimPrefix(st.Pool.Period, "this ")}, {title: "disk", right: true}, {title: "model"},
		{title: "prio", right: true}, {title: "weight", right: true}, {title: "week %", right: true}}
	for _, a := range st.Agents {
		name, state := sty(p.OK(), "●")+" "+a.Name, p.state(a.State)
		if !a.Enabled {
			name = sty(p.Muted(), "○ "+a.Name)
		} else if a.Paused {
			name = sty(p.Warn(), "‖") + " " + a.Name
		}
		if r := st.Running; r != nil && r.Agent == a.Name {
			name, state = sty(p.Head(), "▸")+" "+sty(bold, a.Name), sty(p.Head(), fmt.Sprintf("▸ running %d min", int(time.Since(r.Since).Minutes())))
		}
		last := sty(p.Muted(), "never")
		if !a.LastRun.IsZero() {
			g, s := p.outcome(a.LastStatus)
			last = a.LastRun.Local().Format("Mon 15:04") + " " + sty(s, g)
		}
		share := sty(p.Muted(), "–")
		if a.QuotaPct > 0 {
			share = p.bar(a.PeriodPct/a.QuotaPct*100, 100, 5) + fmt.Sprintf(" %.1f/%.1f", st.Pool.Of(a.PeriodPct), st.Pool.Of(a.QuotaPct))
		}
		disk := a.DiskText()
		if a.OverCap() {
			disk = sty(p.Warn(), disk+" over")
		}
		vals := []string{name, state, a.NextText(st.NextTick), last, strconv.Itoa(a.Runs), share, disk, a.ModelEffort(),
			strconv.Itoa(a.Priority), strconv.FormatFloat(a.Weight, 'f', -1, 64), fmt.Sprintf("%.1f", st.Pool.OfWeek(a.WeekPct))}
		for i, v := range vals {
			cols[i].cells = append(cols[i].cells, v)
		}
	}
	total := func() int {
		t := 2 * (len(cols) - 1)
		for _, c := range cols {
			c.w = width(c.title)
			for _, v := range c.cells {
				c.w = max(c.w, width(v))
			}
			t += c.w
		}
		return t
	}
	for total() > w && len(cols) > 2 { // the columns are in order of importance
		cols = cols[:len(cols)-1]
	}
	if over := total() - w; over > 0 {
		cols[1].w = max(8, cols[1].w-over)
	}
	cell := func(c *column, s string) string {
		if c.right {
			return rjust(truncate(s, c.w), c.w)
		}
		return fit(s, c.w)
	}
	var head []string
	for _, c := range cols {
		head = append(head, sty(p.Muted(), cell(c, c.title)))
	}
	out := []string{strings.Join(head, "  ")}
	for i := range st.Agents {
		var row []string
		for _, c := range cols {
			row = append(row, cell(c, c.cells[i]))
		}
		out = append(out, strings.Join(row, "  "))
	}
	return out
}

// activity is the bottom of the status page: the running shift as it happens, or the
// latest shifts.
func (p palette) activity(t *core.Team, st core.Status, w, h int) []string {
	if r := st.Running; r != nil {
		out := []string{p.section("Live", r.Agent+" on "+r.Host+" · o opens the transcript", w)}
		if trs := t.Transcripts(r.Agent); len(trs) > 0 {
			events, _ := core.ReadEvents(trs[0].Path)
			lines, _ := p.transcript(events[max(0, len(events)-40):], w, false)
			var kept []string
			for _, l := range lines {
				if l != "" {
					kept = append(kept, l)
				}
			}
			out = append(out, kept[max(0, len(kept)-(h-1)):]...)
		}
		return out
	}
	out := []string{p.section("Recent shifts", "", w)}
	for i := len(t.Runs) - 1; i >= 0 && len(out) < h; i-- {
		r := t.Runs[i]
		g, s := p.outcome(r.Status)
		line := sty(p.Muted(), r.Time.Format("Mon 02 15:04")) + "  " + fit(r.Agent, 10) + " " + sty(s, g+" "+fit(shortStatus(r.Status), 11)) +
			sty(p.Muted(), fmt.Sprintf(" %4d turns  %3d min  %s of the pool", r.Turns, r.Seconds/60, r.PctText(t.Config)))
		if r.Reason != "" {
			line += sty(p.Muted(), "  "+r.Reason)
		}
		out = append(out, truncate(line, w))
	}
	if len(t.Runs) == 0 {
		out = append(out, sty(p.Muted(), "no shifts yet"))
	}
	return out
}

// PrintStatus is rota status on a terminal: the console's status page, printed once.
func PrintStatus(out io.Writer, t *core.Team, st core.Status) {
	p := statusPalette(t.Root)
	w := termWidth(os.Stdout) - 1
	lines := p.statusHead(st, st.Paused, w, false)
	for _, l := range t.Warnings {
		lines = append(lines, sty(p.Warn(), "▲ "+l))
	}
	if st.Running != nil { // what the team does now, before the table
		lines = append(append(lines, ""), p.activity(t, st, w, liveLines)...)
	}
	lines = append(append(lines, ""), p.agentTable(st, w)...)
	if st.Running == nil {
		lines = append(append(lines, ""), p.activity(t, st, w, 7)...)
	}
	lines = append(lines, "", sty(p.Muted(), "rota on its own opens the console: this page live, every other page, and every control"))
	for _, l := range lines {
		fmt.Fprintln(out, l+"\x1b[0m")
	}
}

// ---- spend ----

// spendPage is this budget week per agent, then every shift, newest first: one is selected,
// Enter opens its transcript and the agent keys act on its agent.
type spendPage struct {
	sel, top int
	balance  *core.Proposal // new shares being changed, until applied or dropped
	bform    form
}

func (s *spendPage) draw(c *console, f *frame, a rect) {
	if s.balance != nil {
		s.drawBalance(c, f, a)
		return
	}
	p, n := c.pal, len(c.t.Runs)
	if n == 0 {
		f.put(margin, a.y+a.h/3, sty(p.Muted(), "No shifts yet."), a.w-2*margin)
		return
	}
	w := a.w - 2*margin - 1
	head := append(p.spendWeek(c.t, c.st, w), sty(p.Muted(), "B proposes new shares from this use, to change and apply"), "", p.section("Every shift", "newest first · ⏎ opens its transcript", w))
	if a.h-len(head) < 4 { // a short terminal: the shifts only
		head = head[len(head)-1:]
	}
	for i, l := range head {
		f.put(margin, a.y+i, l, w)
	}
	s.sel = max(0, min(s.sel, n-1))
	var detail []string // the selected shift's whole row of spend.csv
	if r := s.run(c); r != nil && a.h-len(head) >= 8 {
		detail = append([]string{""}, p.shiftDetail(c.t.Config, *r, w)...)
		detail = detail[:min(len(detail), (a.h-len(head))/2)] // the list keeps half the room
	}
	list := rect{0, a.y + len(head), a.w, a.h - len(head) - len(detail)}
	for i, l := range detail {
		f.put(margin, list.y+list.h+i, l, w)
	}
	s.top = keepVisible(s.top, s.sel, list.h, n)
	for i := 0; i < list.h && s.top+i < n; i++ {
		f.put(margin, list.y+i, p.shiftRow(c.t.Config, c.t.Runs[n-1-s.top-i], w), w)
		if s.top+i == s.sel {
			f.band(p, 0, list.y+i, a.w-1, true)
		}
	}
	c.scrollbar(f, a.w-1, list.y, list.h, s.top, n)
	c.hits = append(c.hits, hit{r: list, click: func(_, y int) {
		if row := s.top + y; row < n {
			if row == s.sel {
				s.open(c)
			}
			s.sel = row
		}
	}, wheel: func(d int) { s.sel = max(0, min(n-1, s.sel+d/3)) }})
}

// run is the selected shift.
func (s *spendPage) run(c *console) *core.Run {
	if n := len(c.t.Runs); s.sel < n {
		return &c.t.Runs[n-1-s.sel]
	}
	return nil
}

func (s *spendPage) open(c *console) {
	r := s.run(c)
	if r == nil {
		return
	}
	for _, tr := range c.t.Transcripts(r.Agent) {
		if tr.Run == r {
			c.tab = tabTranscripts
			c.pages[tabTranscripts].(*browser).open(tr.Path)
			return
		}
	}
	c.say("this shift's transcript is no longer kept", true)
}

func (s *spendPage) key(c *console, k key) bool {
	if s.balance != nil {
		return s.balanceKey(c, k)
	}
	n := len(c.t.Runs)
	switch {
	case k.r == 'B':
		s.openBalance(c)
	case k.name == "up" || k.r == 'k':
		s.sel = max(0, s.sel-1)
	case k.name == "down" || k.r == 'j':
		s.sel = min(n-1, s.sel+1)
	case k.name == "pgup" || k.r == 'b':
		s.sel = max(0, s.sel-10)
	case k.name == "pgdn" || k.r == ' ':
		s.sel = min(n-1, s.sel+10)
	case k.name == "home" || k.r == 'g':
		s.sel = 0
	case k.name == "end" || k.r == 'G':
		s.sel = max(0, n-1)
	case k.name == "enter" || k.name == "right" || k.r == 'l':
		s.open(c)
	default:
		if r := s.run(c); r != nil {
			return c.agentKey(r.Agent, k)
		}
		return false
	}
	return true
}

func (s *spendPage) hints(c *console) []hint {
	if s.balance != nil {
		return []hint{{"↑↓", "agent"}, {"⏎", "change its weight"}, {"A", "apply them all"}, {"B", "start over"}, {"Esc", "drop"}}
	}
	return []hint{{"↑↓", "shift"}, {"⏎", "transcript"}, {"o", "latest"}, {"r", "run now"}, {"B", "rebalance shares"}}
}

// spendWeek is this budget week per agent, with the team's total.
func (p palette) spendWeek(t *core.Team, st core.Status, w int) []string {
	week := st.WeekSince
	out := []string{p.section("This budget week", "since "+week.Local().Format("Mon 02 Jan 15:04"), w),
		sty(p.Muted(), fmt.Sprintf("%-12s %6s %7s %8s %14s  %s", "agent", "shifts", "failed", "% of pool", "tokens out", "last"))}
	var tn, tf, to int
	var tp float64
	for _, a := range t.Agents {
		n, failed, outTok, pct, last := 0, 0, 0, 0.0, ""
		for _, r := range t.Runs {
			if r.Agent == a.Name && !r.Time.Before(week) {
				n, outTok, pct = n+1, outTok+r.Out, pct+r.PctUsed
				failed += btoi(!r.IsRun())
				g, s := p.outcome(r.Status)
				last = r.Time.Format("Mon 15:04") + " " + sty(s, g)
			}
		}
		fail := sty(p.Muted(), "0")
		if failed > 0 {
			fail = sty(p.Bad(), strconv.Itoa(failed))
		}
		out = append(out, fit(a.Name, 12)+fmt.Sprintf(" %6d ", n)+rjust(fail, 7)+fmt.Sprintf(" %8.1f ", t.OfWeekPool(pct))+rjust(thousands(outTok), 14)+"  "+or(last, sty(p.Muted(), "–")))
		tn, tf, to, tp = tn+n, tf+failed, to+outTok, tp+pct
	}
	return append(out, sty(p.Rule(), strings.Repeat("─", min(w, 60))),
		sty(bold, fit("team", 12)+fmt.Sprintf(" %6d %7d %8.1f ", tn, tf, t.OfWeekPool(tp))+rjust(thousands(to), 14)))
}

// shiftDetail is every column spend.csv keeps for a shift, as the pages' Spend table shows them.
func (p palette) shiftDetail(c *core.Config, r core.Run, w int) []string {
	var spans []span
	for i, v := range r.Row() {
		if i < 2 { // the time and the agent begin the line
			continue
		}
		if v == "" {
			v = "–"
		}
		spans = append(spans, span{core.SpendHeading(core.SpendColumns[i]) + ": ", p.Muted()}, span{r.Cell(c, i, v) + "  ", ""})
	}
	return wrap(spans, w, sty(p.Muted(), r.Time.Format("Mon 02 Jan 15:04"))+"  "+r.Agent+"  ", "  ")
}

// shiftRow is one shift on a line: when, who, how it ended, what it took.
func (p palette) shiftRow(c *core.Config, r core.Run, w int) string {
	g, s := p.outcome(r.Status)
	line := sty(p.Muted(), r.Time.Format("Mon 02 Jan 15:04")) + "  " + fit(r.Agent, 10) + " " + sty(s, g+" "+fit(shortStatus(r.Status), 11)) +
		fmt.Sprintf(" %4d turns  %3d min  %6s", r.Turns, r.Seconds/60, r.PctText(c))
	if r.Reason != "" {
		line += sty(p.Muted(), "  "+r.Reason)
	}
	return truncate(line, w)
}

func thousands(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

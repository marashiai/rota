//go:build darwin || freebsd || netbsd || openbsd || dragonfly || linux

package tui

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"rota/gloss/review"
)

// Styles use the terminal's own palette, so they follow its theme.
const (
	sReset  = "\x1b[0m"
	sBold   = "\x1b[1m"
	sDim    = "\x1b[2m"
	sAdd    = "\x1b[32m"
	sDel    = "\x1b[31m"
	sStr    = "\x1b[33m" // code: strings yellow, numbers cyan; keywords bold and comments dim, in the line's colour
	sNum    = "\x1b[36m"
	sHunk   = "\x1b[36m"
	sFile   = "\x1b[1;34m"
	sAgent  = "\x1b[35m"
	sDraft  = "\x1b[33m"
	sMark   = "\x1b[1;33m"
	sBar    = "\x1b[7m"
	sError  = "\x1b[31m"
	sAsk    = "\x1b[1;33m"
	sNoteBy = "\x1b[1m"
	sNew    = "\x1b[1;32m"
)

type seg struct{ style, text string }

type row struct {
	segs []seg
	item int // -1: no item
}

// item is something the cursor stops on: a line, a hunk, a file, a commit, a note.
type item struct {
	kind   string // change, goto, commit, file, hunk, line, note, fold (a folded thread), folds (the read ones of a view)
	path   string
	line   int
	old    bool
	hunk   int
	view   int
	note   *review.Note
	thread *review.Thread // a note's thread, or the first thread under a line
	first  int            // its rows
	last   int
}

func (it item) key() string {
	id := ""
	if it.note != nil {
		id = it.note.ID
	}
	return fmt.Sprintf("%s|%s|%s|%d|%v|%d|%d", it.kind, id, it.path, it.line, it.old, it.hunk, it.view)
}

type builder struct {
	rows  []row
	items []item
	w     int
}

func (b *builder) begin(it item) int {
	it.first, it.last = len(b.rows), len(b.rows)
	b.items = append(b.items, it)
	return len(b.items) - 1
}

func (b *builder) add(i int, segs ...seg) {
	b.rows = append(b.rows, row{segs, i})
	if i >= 0 {
		b.items[i].last = len(b.rows) - 1
	}
}

func (b *builder) blank() { b.add(-1) }

func sp(n int) seg { return seg{"", strings.Repeat(" ", n)} }

func expand(s string) string { return strings.ReplaceAll(s, "\t", "    ") }

// wrap breaks text into lines of at most width runes, at spaces where it can.
func wrap(text string, width int) []string {
	width = max(10, width)
	var out []string
	for _, para := range strings.Split(expand(text), "\n") {
		line := []rune(para)
		for len(line) > width {
			cut := width
			for i := width; i > width/2; i-- {
				if line[i] == ' ' {
					cut = i
					break
				}
			}
			out = append(out, string(line[:cut]))
			line = []rune(strings.TrimLeft(string(line[cut:]), " "))
		}
		out = append(out, string(line))
	}
	return out
}

// layout lays the current view out as rows; keep holds the cursor on the same item.
func (m *model) layout(keep bool) {
	key := ""
	if it, ok := m.item(); ok && keep {
		key = it.key()
	}
	b := &builder{w: max(20, m.w-1)}
	switch {
	case m.rv == nil:
	case m.view == 0:
		m.whole(b)
	default:
		m.commit(b, m.view)
	}
	m.rows, m.items = b.rows, b.items
	idx := -1
	for i, it := range m.items {
		if m.focus != "" && it.note != nil && it.note.ID == m.focus || m.focus == "" && key != "" && it.key() == key {
			idx = i
			break
		}
	}
	if idx >= 0 {
		m.focus = ""
		m.cur = idx
	} else if !keep {
		m.cur = 0
	}
	m.cur = max(0, min(len(m.items)-1, m.cur))
	m.scroll()
}

// whole lays the whole change out: its title and description, the notes on it, and its diff
// from the base to the head with every note GitHub can place in it.
func (m *model) whole(b *builder) {
	rv := m.rv
	it := b.begin(item{kind: "change"})
	b.add(it, seg{sBold, " " + rv.Title})
	approval := ""
	if rv.Approved() {
		approval = " · approved by " + rv.Approval.By
	}
	b.add(it, seg{sDim, fmt.Sprintf(" %s · by %s · %s · %d commits%s", rv.PR, rv.Author, strings.ToLower(rv.State), len(rv.Commits), approval)})
	if strings.TrimSpace(rv.Body) != "" {
		b.blank()
		for _, l := range wrap(rv.Body, b.w-2) {
			b.add(-1, seg{sDim, " " + l})
		}
	}
	files, ok := m.files[m.diffKey(0)]
	if !ok {
		m.loadDiff(0)
		m.notes(b, "NOTES ON THE WHOLE CHANGE · C adds one", rv.Overall(), true, false)
		b.blank()
		b.add(-1, seg{sDim, " reading the diff…"})
		return
	}
	placed := rv.PlaceWhole(files)
	m.notes(b, "NOTES ON THE WHOLE CHANGE · C adds one", placed.Whole, true, true)
	m.diff(b, files, placed, true)
	if len(files) == 0 {
		b.blank()
		b.add(-1, seg{sDim, " This change changes no files."})
	}
	if rv.Truncated {
		b.blank()
		b.add(-1, seg{sError, " GitHub gave only the first 100 commits, threads or comments: some are missing."})
	}
}

// commit lays commit n out the same way: its title and message, the notes on it, and its own diff.
func (m *model) commit(b *builder, n int) {
	c := m.rv.Commits[n-1]
	it := b.begin(item{kind: "commit"})
	b.add(it, seg{sBold, " " + c.Title})
	b.add(it, seg{sDim, fmt.Sprintf(" commit %d of %d · %s · %s · %s", n, len(m.rv.Commits), short(c.SHA), c.Author, c.Date.Local().Format("Jan 2 15:04"))})
	if strings.TrimSpace(c.Message) != "" {
		b.blank()
		for _, l := range wrap(c.Message, b.w-2) {
			b.add(-1, seg{sDim, " " + l})
		}
	}
	files, ok := m.files[c.SHA]
	if !ok {
		m.loadDiff(n)
		var top []*review.Thread
		for _, t := range m.rv.Threads {
			if t.Commit == c.SHA && t.Path == "" {
				top = append(top, t)
			}
		}
		m.notes(b, "NOTES ON THIS COMMIT · c on the title adds one", top, false, false)
		b.blank()
		b.add(-1, seg{sDim, " reading the diff…"})
		return
	}
	placed := m.rv.Place(n-1, files)
	m.notes(b, "NOTES ON THIS COMMIT · c on the title adds one", placed.Whole, false, true)
	m.diff(b, files, placed, false)
	if len(files) == 0 {
		b.blank()
		b.add(-1, seg{sDim, " This commit changes no files."})
	}
}

// notes lays the view's own notes out under a heading: on the whole change or the whole commit,
// and the ones its diff cannot show.
func (m *model) notes(b *builder, heading string, ts []*review.Thread, whole, loose bool) {
	b.blank()
	b.add(-1, seg{sDim, " " + heading})
	if len(ts) == 0 {
		b.add(-1, seg{sDim, "   none yet"})
	}
	var open []*review.Thread
	read := 0
	for _, t := range ts {
		if m.folded(t) {
			read += len(t.Notes)
		} else {
			open = append(open, t)
		}
	}
	if len(ts)-len(open) >= 2 && !m.showRead[m.view] { // the read ones fold into one line
		it := b.begin(item{kind: "folds", view: m.view})
		b.add(it, sp(2), seg{sDim, fmt.Sprintf("▸ %d notes you have read · z shows them", read)})
		ts = open
	}
	for _, t := range ts {
		m.thread(b, t, 2, m.where(t, whole, loose))
	}
}

// diff lays files out with the threads placed in them.
func (m *model) diff(b *builder, files []review.File, placed review.Placed, whole bool) {
	for _, f := range files {
		b.blank()
		name := f.Path
		if f.OldPath != "" {
			name = f.OldPath + " → " + f.Path
		}
		it := b.begin(item{kind: "file", path: f.Path})
		b.add(it, seg{sFile, "── " + name + " "}, seg{sDim, fmt.Sprintf("%s +%d −%d", f.Status, f.Added(), f.Deleted())})
		for _, t := range placed.Files[f.Path] {
			m.thread(b, t, 2, m.where(t, whole, true))
		}
		if f.Binary {
			b.add(-1, seg{sDim, "   binary file"})
		}
		for hi, h := range f.Hunks {
			it := b.begin(item{kind: "hunk", path: f.Path, hunk: hi + 1})
			b.add(it, seg{sHunk, fmt.Sprintf("%13s@@ -%d,%d +%d,%d @@ %s", "", h.OldStart, h.OldCount, h.NewStart, h.NewCount, h.Section)})
			for _, l := range h.Lines {
				old := l.Kind == '-'
				ln := l.New
				if old {
					ln = l.Old
				}
				style := ""
				switch l.Kind {
				case '+':
					style = sAdd
				case '-':
					style = sDel
				}
				it := b.begin(item{kind: "line", path: f.Path, line: ln, old: old})
				b.add(it, append([]seg{{sDim, fmt.Sprintf("%5s %5s ", num(l.Old), num(l.New))}, {style, string(l.Kind) + " "}}, code(style, l.Text, l.Spans)...)...)
				keys := []review.LineKey{{Path: f.Path, Line: ln, Old: old}}
				if l.Kind == ' ' {
					keys = append(keys, review.LineKey{Path: f.Path, Line: l.Old, Old: true})
				}
				for _, k := range keys {
					for _, t := range placed.Lines[k] {
						if b.items[it].thread == nil {
							b.items[it].thread = t
						}
						m.thread(b, t, 14, m.where(t, whole, false))
					}
				}
			}
		}
	}
}

// where says what a thread's place does not show by itself: in the whole change, the commit it
// was written on; when it is loose, the line or file this diff does not show.
func (m *model) where(t *review.Thread, whole, loose bool) string {
	var parts []string
	if whole && t.Commit != "" {
		if n := m.rv.CommitNumber(t.Commit); n > 0 {
			parts = append(parts, fmt.Sprintf("in commit %d", n))
		} else {
			parts = append(parts, "in a commit no longer in the pull request")
		}
	}
	if loose && t.Path != "" && !t.Outdated {
		if t.Line != 0 {
			side := ""
			if t.Old {
				side = " (old side)"
			}
			parts = append(parts, fmt.Sprintf("line %d%s of %s, which this diff does not show", t.Line, side, t.Path))
		} else {
			parts = append(parts, "on "+t.Path)
		}
	}
	return strings.Join(parts, " · ")
}

func num(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprint(n)
}

// code lays a line's text out: the plain text in the line's style, each token in that and its
// kind's, tabs expanded. Keywords, types, tags and headings are bold, comments dim, strings
// yellow and numbers cyan; the rest keeps the line's colour, so an added or a deleted line stays
// green or red.
func code(style, text string, spans []review.Span) []seg {
	if len(spans) == 0 {
		return []seg{{style, expand(text)}}
	}
	runes := []rune(text)
	var segs []seg
	at := 0
	to := func(end int, st string) {
		if end = min(end, len(runes)); end > at {
			segs = append(segs, seg{st, expand(string(runes[at:end]))})
			at = end
		}
	}
	for _, s := range spans {
		to(s.Start, style)
		to(s.Start+s.Len, style+tokenStyle(s.Kind))
	}
	to(len(runes), style)
	return segs
}

func tokenStyle(kind string) string {
	switch kind {
	case "k", "kt", "cp", "nt", "gh", "gu", "gs":
		return sBold
	case "c":
		return sDim
	case "s":
		return sStr
	case "m":
		return sNum
	}
	return ""
}

func (m *model) thread(b *builder, t *review.Thread, indent int, where string) {
	if m.folded(t) {
		m.folds(b, t, indent)
		return
	}
	var flags []string
	if where != "" {
		flags = append(flags, where)
	}
	if t.Outdated {
		flags = append(flags, "changed by a later commit")
	}
	if t.Resolved {
		flags = append(flags, "resolved")
	}
	b.add(-1, sp(indent), seg{sDim, "┌ " + strings.Join(flags, " · ")})
	for _, n := range t.Notes {
		i := b.begin(item{kind: "note", note: n, thread: t})
		bar := sDim
		who := []seg{{sNoteBy, n.Author}}
		switch {
		case n.Named:
			bar = sAgent
			who = append(who, seg{sAgent, " agent"})
		case m.rv.Mine(n) && !n.PRComment: // a PR comment from the account may be an agent's
			who = []seg{{sNoteBy, "you"}}
		}
		if n.Draft {
			bar = sDraft
			who = append(who, seg{sDraft, " draft"})
		}
		if !m.read.Has(m.rv, t, n) {
			who = append(who, seg{sNew, " new"})
		}
		head := append([]seg{sp(indent), {bar, "│ "}}, who...)
		head = append(head, seg{sDim, " · " + n.Created.Local().Format("Jan 2 15:04")})
		b.add(i, head...)
		for _, l := range wrap(n.Text, b.w-indent-3) {
			b.add(i, sp(indent), seg{bar, "│ "}, seg{"", l})
		}
	}
}

// folded says whether t shows folded: the reviewer has read it, and has not opened it again.
func (m *model) folded(t *review.Thread) bool {
	return !m.unfold[t.ID] && m.read.Folded(m.rv, t)
}

// folds lays a folded thread out on one line: who wrote it, when, and how it starts.
func (m *model) folds(b *builder, t *review.Thread, indent int) {
	n := t.Notes[0]
	i := b.begin(item{kind: "fold", note: n, thread: t})
	who := n.Author
	if m.rv.Mine(n) && !n.PRComment {
		who = "you"
	}
	head := fmt.Sprintf("▸ %s · %s · ", who, n.Created.Local().Format("Jan 2 15:04"))
	tail := ""
	if r := len(t.Notes) - 1; r > 0 {
		tail = fmt.Sprintf(" · %d repl%s", r, map[bool]string{true: "y", false: "ies"}[r == 1])
	}
	room := b.w - indent - utf8.RuneCountInString(head) - utf8.RuneCountInString(tail) - 1
	b.add(i, sp(indent), seg{sDim, head + clip(gist(n.Text), room) + tail})
}

// gist is how a note starts: its first line with text, without Markdown's marks.
func gist(text string) string {
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(strings.NewReplacer("**", "", "__", "", "`", "").Replace(l))
		l = strings.TrimSpace(strings.TrimLeft(l, "#>-* "))
		if l != "" {
			return strings.Join(strings.Fields(l), " ")
		}
	}
	return ""
}

// clip cuts s to n runes, with an ellipsis when it cut.
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:max(0, n)])
	}
	return string(r[:n-1]) + "…"
}

// look marks the notes on the screen as read; the review shows them folded the next time it opens.
func (m *model) look() {
	if m.rv == nil || m.help || m.pick != nil {
		return
	}
	for r := m.top; r < min(len(m.rows), m.top+m.contentHeight()); r++ {
		i := m.rows[r].item
		if i < 0 || i >= len(m.items) || m.items[i].kind != "note" {
			continue
		}
		it := m.items[i]
		if !m.saved.Has(m.rv, it.thread, it.note) && !m.seen.Has(m.rv, it.thread, it.note) {
			m.seen.Mark(it.thread, it.note.Created)
		}
	}
}

// ---- the screen

func (m *model) editorHeight() int {
	if m.ed == nil {
		return 0
	}
	lines := len(m.ed.lines(m.editWidth()))
	return max(5, min(lines+2, m.h/2))
}

func (m *model) contentHeight() int { return max(1, m.h-2-m.editorHeight()) }

// scroll keeps the cursor's item on the screen.
func (m *model) scroll() {
	it, ok := m.item()
	if !ok {
		m.top = 0
		return
	}
	ch := m.contentHeight()
	switch {
	case it.first < m.top:
		m.top = it.first
	case it.last >= m.top+ch:
		m.top = min(it.first, it.last-ch+1)
	}
	m.top = max(0, m.top)
}

// line writes one screen line: segs on base, cut or padded to width.
func line(sb *strings.Builder, y, width int, base string, segs ...seg) {
	fmt.Fprintf(sb, "\x1b[%d;1H%s%s", y, sReset, base)
	left := width
	for _, s := range segs {
		if left <= 0 {
			break
		}
		text := printable(s.text)
		if n := utf8.RuneCountInString(text); n > left {
			text = string([]rune(text)[:left])
		}
		left -= utf8.RuneCountInString(text)
		sb.WriteString(sReset + base + s.style + text)
	}
	sb.WriteString(sReset + base + strings.Repeat(" ", max(0, left)) + sReset)
}

// printable replaces control characters, so text from a pull request cannot drive the terminal.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r >= 0x80 && r < 0xa0 {
			return '·'
		}
		return r
	}, s)
}

func (m *model) draw(out io.Writer) {
	var sb strings.Builder
	sb.WriteString("\x1b[?25l")
	m.scroll()
	// the header
	where := "the whole change"
	if m.rv != nil && m.view > 0 {
		c := m.rv.Commits[m.view-1]
		where = fmt.Sprintf("commit %d/%d %s %s", m.view, len(m.rv.Commits), short(c.SHA), c.Title)
	}
	right := "? keys "
	if m.rv != nil {
		switch n := m.rv.Drafts(); {
		case n > 0:
			right = fmt.Sprintf("%d draft%s · S sends · A approves · ? keys ", n, plural(n))
		case m.rv.Approved():
			right = "approved · ? keys "
		case m.rv.State == "OPEN":
			right = "A approves · ? keys "
		}
	}
	left := fmt.Sprintf(" gloss · %s · %s", m.ref, where)
	pad := m.w - utf8.RuneCountInString(left) - utf8.RuneCountInString(right)
	if pad < 1 {
		left = string([]rune(left)[:max(0, m.w-utf8.RuneCountInString(right)-1)])
		pad = 1
	}
	line(&sb, 1, m.w, sBar, seg{"", left + strings.Repeat(" ", pad) + right})

	ch := m.contentHeight()
	switch {
	case m.help:
		for i := 0; i < ch; i++ {
			text := ""
			if i < len(helpText) {
				text = helpText[i]
			}
			line(&sb, 2+i, m.w, "", seg{"", text})
		}
	case m.pick != nil:
		m.drawChooser(&sb, ch)
	default:
		for i := 0; i < ch; i++ {
			r := m.top + i
			if r >= len(m.rows) {
				line(&sb, 2+i, m.w, "")
				continue
			}
			rw := m.rows[r]
			mark := seg{"", " "}
			if rw.item >= 0 && rw.item == m.cur {
				mark = seg{sMark, "▌"}
			}
			line(&sb, 2+i, m.w, "", append([]seg{mark}, rw.segs...)...)
		}
		m.look()
	}

	// the editor
	cx, cy := 0, 0
	if ed := m.ed; ed != nil {
		y := 2 + ch
		eh := m.editorHeight()
		title := " New note on " + ed.label
		switch ed.mode {
		case "reply":
			title = " Writing " + ed.label
		case "edit":
			title = " Editing your draft"
		}
		line(&sb, y, m.w, sBar, seg{"", title + "  ·  Esc closes"})
		width := m.editWidth()
		starts := ed.lines(width)
		row, col := ed.cursor(starts)
		rows := eh - 2
		if row < ed.top {
			ed.top = row
		}
		if row >= ed.top+rows {
			ed.top = row - rows + 1
		}
		for i := 0; i < rows; i++ {
			text := ""
			if ed.top+i < len(starts) {
				text = ed.lineText(starts, ed.top+i)
			}
			line(&sb, y+1+i, m.w, "", seg{"", " " + text})
		}
		state := sDim
		if ed.failed || ed.warned {
			state = sError
		}
		line(&sb, y+eh-1, m.w, "", seg{state, " " + ed.state})
		cx, cy = 2+col, y+1+row-ed.top
	}

	// the status line
	switch {
	case m.ask != nil:
		line(&sb, m.h, m.w, "", seg{sAsk, " " + m.ask.text})
	case m.status != "":
		st := sDim
		if m.statusErr {
			st = sError
		}
		line(&sb, m.h, m.w, "", seg{st, " " + m.status})
	default:
		line(&sb, m.h, m.w, "", seg{sDim, " j/k move · ]/[ hunk · }/{ note · z fold · J/K commit · v views · f files · c note · r reply · S send · ? keys · q quits"})
	}
	if m.ed != nil {
		fmt.Fprintf(&sb, "\x1b[%d;%dH\x1b[?25h", cy, cx)
	}
	io.WriteString(out, sb.String())
}

// drawChooser draws the open chooser over the view: its title, then its items, the chosen one
// marked.
func (m *model) drawChooser(sb *strings.Builder, ch int) {
	p := m.pick
	line(sb, 2, m.w, "", seg{sBold, " " + p.title})
	rows := ch - 2
	if p.cur < p.top {
		p.top = p.cur
	}
	if p.cur >= p.top+rows {
		p.top = p.cur - rows + 1
	}
	for i := 0; i < rows; i++ {
		j := p.top + i
		if j >= len(p.items) {
			line(sb, 4+i, m.w, "")
			continue
		}
		mark := seg{"", " "}
		if j == p.cur {
			mark = seg{sMark, "▌"}
		}
		line(sb, 4+i, m.w, "", append([]seg{mark}, p.items[j].segs...)...)
	}
	line(sb, 3, m.w, "")
}

var helpText = []string{
	"",
	"  Keys",
	"",
	"  j k           next, previous line or note       c       note here: on the line, hunk, file or commit",
	"  ] [           next, previous hunk               C       note on the whole change",
	"  } {           next, previous note or new one    r       reply to the note here",
	"  . ,           next, previous file               e       edit your draft",
	"  J K           next, previous commit             x  dd   delete your draft",
	"  o             the whole change                  S       send your drafts as one review",
	"  v             pick the whole change or a commit A       approve: the team may merge it",
	"  f             pick a file to jump to            R       read the notes from GitHub again",
	"  gg G          top, bottom                       q       quit: your drafts stay saved",
	"  Ctrl+D Ctrl+U half a page down, up",
	"  z             fold the note here, or open a folded one (Enter opens one too)",
	"  Z             fold every note: you have read them all",
	"",
	"  A note you have read shows folded, on one line, the next time you open the review; the",
	"  read notes on the whole change or a commit fold into a single line. You have read a note",
	"  once it was on the screen, or once you fold it. New ones say new.",
	"",
	"  In the editor: Esc or Ctrl+S closes. A note is saved to your pending review on GitHub as",
	"  you type; only you see it until you send. Ctrl+A/E line start/end, Ctrl+U/K/W delete.",
	"",
	"  Any key closes this.",
}

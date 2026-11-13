package tui

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"rota/core"
	"rota/md"
)

// A palette is the console's colors, from the 256-color set, each checked (draw_test.go) for
// WCAG AA contrast: every text color at least 4.5:1 against the usual backgrounds of its kind
// of terminal, and on the selection band. Meaning never rests on color alone: states carry
// a glyph (✓ ✗ ■ ● ○ ◷ ▸ ▲), the selection a marker, numbers their value. mono has no color.
type palette struct {
	name                                     string
	muted, accent, ok, warn, bad, code, rule int // 256-color indices; -1: none
	selBg, selFg, selMuted                   int // the selection band, and text on it
}

var palettes = []palette{
	{"dark", 248, 75, 78, 214, 210, 116, 240, 236, 255, 250},
	{"light", 241, 25, 22, 94, 124, 23, 250, 255, 232, 238},
	{"mono", -1, -1, -1, -1, -1, -1, -1, -1, -1, -1},
}

func paletteNamed(name string) palette {
	for _, p := range palettes {
		if p.name == name {
			return p
		}
	}
	return palettes[0]
}

func fg(n int) string {
	if n < 0 {
		return ""
	}
	return fmt.Sprintf("38;5;%d", n)
}

// The styles of a palette, as SGR parameters for sty.
func (p palette) Muted() string  { return fg(p.muted) }
func (p palette) Accent() string { return fg(p.accent) }
func (p palette) OK() string     { return fg(p.ok) }
func (p palette) Warn() string   { return fg(p.warn) }
func (p palette) Bad() string    { return fg(p.bad) }
func (p palette) Code() string   { return fg(p.code) }
func (p palette) Rule() string   { return fg(p.rule) }
func (p palette) Head() string {
	if p.name == "mono" {
		return "1;4"
	}
	return join(fg(p.accent), "1")
}

// bars: thin lines in color, solid blocks without.
func (p palette) barGlyphs() (full, half, track string) {
	if p.name == "mono" {
		return "█", "▌", "░"
	}
	return "━", "╸", "─"
}

const bold = "1"

func sty(st, s string) string {
	if st == "" || s == "" {
		return s
	}
	return "\x1b[" + st + "m" + s + "\x1b[0m"
}

func join(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + ";" + b
}

// onBand is a cell's style on the selection band: the band's background, text lifted to stay
// readable on it; mono reverses instead.
func (p palette) onBand(st string) string {
	if p.name == "mono" {
		return join(st, "7")
	}
	st = strings.ReplaceAll(strings.ReplaceAll(st, fg(p.muted), fg(p.selMuted)), fg(p.rule), fg(p.selMuted))
	if !strings.Contains(st, "38;5;") {
		st = join(st, fg(p.selFg))
	}
	return join(st, fmt.Sprintf("48;5;%d", p.selBg))
}

// ---- widths ----

func runeWidth(r rune) int {
	switch {
	case r < 0x20 || r == 0x7f:
		return 0
	case r < 0x300:
		return 1
	case unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf) || r >= 0xfe00 && r <= 0xfe0f:
		return 0
	case r >= 0x1100 && r <= 0x115f, r >= 0x2e80 && r <= 0xa4cf && r != 0x303f, r >= 0xac00 && r <= 0xd7a3,
		r >= 0xf900 && r <= 0xfaff, r >= 0xfe30 && r <= 0xfe4f, r >= 0xff00 && r <= 0xff60, r >= 0xffe0 && r <= 0xffe6,
		r >= 0x1f300 && r <= 0x1f64f, r >= 0x1f680 && r <= 0x1f6ff, r >= 0x1f900 && r <= 0x1f9ff, r >= 0x1fa70 && r <= 0x1faff,
		r >= 0x20000 && r <= 0x3fffd, strings.ContainsRune("⌚⌛⏩⏪⏫⏬⏰⏳◽◾☔☕⚡⚪⚫⚽⚾⛄⛅⛔⛪⛲⛳⛵⛺⛽✅✊✋✨❌❎❓❔❕❗➕➖➗➰➿⬛⬜⭐⭕", r):
		return 2
	}
	return 1
}

// escLen is the length of the escape sequence s starts with, or 0.
func escLen(s string) int {
	if len(s) < 2 || s[0] != 0x1b || s[1] != '[' {
		return 0
	}
	j := 2
	for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
		j++
	}
	return min(j+1, len(s))
}

// width is how many columns s takes, escape sequences not counted.
func width(s string) int {
	w := 0
	for i := 0; i < len(s); {
		if n := escLen(s[i:]); n > 0 {
			i += n
			continue
		}
		r, n := utf8.DecodeRuneInString(s[i:])
		w += runeWidth(r)
		i += n
	}
	return w
}

func plain(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if n := escLen(s[i:]); n > 0 {
			i += n
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// truncate cuts s to w columns, ending in … when something was cut.
func truncate(s string, w int) string {
	if width(s) <= w {
		return s
	}
	var b strings.Builder
	col := 0
	for i := 0; i < len(s) && w > 0; {
		if n := escLen(s[i:]); n > 0 {
			b.WriteString(s[i : i+n])
			i += n
			continue
		}
		r, n := utf8.DecodeRuneInString(s[i:])
		if col+runeWidth(r) > w-1 {
			break
		}
		b.WriteRune(r)
		col += runeWidth(r)
		i += n
	}
	return b.String() + "\x1b[0m…"
}

// fit is s cut or padded to exactly w columns.
func fit(s string, w int) string {
	s = truncate(s, w)
	return s + strings.Repeat(" ", max(0, w-width(s)))
}

func rjust(s string, w int) string { return strings.Repeat(" ", max(0, w-width(s))) + s }

// cutWidth splits plain s after at most w columns, at least one rune.
func cutWidth(s string, w int) (string, string) {
	col := 0
	for i, r := range s {
		if col+runeWidth(r) > w && i > 0 {
			return s[:i], s[i:]
		}
		col += runeWidth(r)
	}
	return s, ""
}

// ---- the screen as a grid of cells ----

type cell struct{ ch, st string } // ch "" is the right half of a wide rune

type frame struct {
	w, h   int
	cells  [][]cell
	cx, cy int // where to show the cursor; -1: hidden
}

func newFrame(w, h int) *frame {
	f := &frame{w: w, h: h, cx: -1, cy: -1, cells: make([][]cell, h)}
	for y := range f.cells {
		f.cells[y] = make([]cell, w)
		for x := range f.cells[y] {
			f.cells[y][x] = cell{" ", ""}
		}
	}
	return f
}

// put writes styled text at x, y within maxw columns (-1: to the edge); it returns the
// columns used.
func (f *frame) put(x, y int, s string, maxw int) int {
	if y < 0 || y >= f.h || x < 0 || x >= f.w {
		return 0
	}
	if maxw < 0 || x+maxw > f.w {
		maxw = f.w - x
	}
	cur, col := "", 0
	for i := 0; i < len(s); {
		if n := escLen(s[i:]); n > 0 {
			if s[i+n-1] == 'm' {
				if p := s[i+2 : i+n-1]; p == "" || p == "0" {
					cur = ""
				} else {
					cur = join(cur, p)
				}
			}
			i += n
			continue
		}
		r, n := utf8.DecodeRuneInString(s[i:])
		i += n
		if r == '\t' {
			r = ' '
		}
		rw := runeWidth(r)
		if rw == 0 {
			if r >= 0x20 && col > 0 && f.cells[y][x+col-1].ch != "" {
				f.cells[y][x+col-1].ch += string(r)
			}
			continue
		}
		if col+rw > maxw {
			break
		}
		f.cells[y][x+col] = cell{string(r), cur}
		if rw == 2 {
			f.cells[y][x+col+1] = cell{"", cur}
		}
		col += rw
	}
	return col
}

// band marks row y from x for w columns as the selection: with the keys (focused) the row
// gets the band; either way column x gets the marker, so selection never rests on color.
func (f *frame) band(p palette, x, y, w int, focused bool) {
	if y < 0 || y >= f.h || x < 0 {
		return
	}
	mark := cell{"▌", p.Accent()}
	if focused {
		for xx := x; xx < x+w && xx < f.w; xx++ {
			f.cells[y][xx].st = p.onBand(f.cells[y][xx].st)
		}
		mark.st = p.onBand(mark.st)
	}
	if p.name == "mono" && !focused {
		mark.st = bold
	}
	f.cells[y][x] = mark
}

// box draws a rounded frame titled title, its inside cleared.
func (f *frame) box(p palette, x, y, w, h int, title string) {
	for yy := y; yy < y+h && yy < f.h; yy++ {
		for xx := x; xx < x+w && xx < f.w; xx++ {
			f.cells[yy][xx] = cell{" ", ""}
		}
	}
	f.put(x, y, sty(p.Muted(), "╭"+strings.Repeat("─", w-2)+"╮"), w)
	for yy := y + 1; yy < y+h-1; yy++ {
		f.put(x, yy, sty(p.Muted(), "│"), 1)
		f.put(x+w-1, yy, sty(p.Muted(), "│"), 1)
	}
	f.put(x, y+h-1, sty(p.Muted(), "╰"+strings.Repeat("─", w-2)+"╯"), w)
	f.put(x+2, y, " "+sty(bold, title)+" ", w-4)
}

// lines renders the grid, one string per row.
func (f *frame) lines() []string {
	out := make([]string, f.h)
	for y := range f.cells {
		var b strings.Builder
		cur := ""
		for _, c := range f.cells[y] {
			if c.ch == "" {
				continue
			}
			if c.st != cur {
				b.WriteString("\x1b[0m")
				if c.st != "" {
					b.WriteString("\x1b[" + c.st + "m")
				}
				cur = c.st
			}
			b.WriteString(c.ch)
		}
		out[y] = b.String() + "\x1b[0m"
	}
	return out
}

// ---- text ----

type span struct{ text, st string }

// wrap lays spans out in lines of at most width columns; the first line starts with first,
// the others with rest: a hanging indent.
func wrap(spans []span, w int, first, rest string) []string {
	type word []span
	var words []word
	var cur word
	for _, sp := range spans {
		for i, part := range strings.Split(strings.ReplaceAll(sp.text, "\n", " "), " ") {
			if i > 0 && len(cur) > 0 {
				words, cur = append(words, cur), nil
			}
			if part != "" {
				cur = append(cur, span{part, sp.st})
			}
		}
	}
	if len(cur) > 0 {
		words = append(words, cur)
	}
	var lines []string
	var line strings.Builder
	line.WriteString(first)
	lw, empty := width(first), true
	newLine := func() {
		lines = append(lines, line.String())
		line.Reset()
		line.WriteString(rest)
		lw, empty = width(rest), true
	}
	for _, wd := range words {
		ww := 0
		for _, a := range wd {
			ww += width(a.text)
		}
		if !empty && lw+1+ww > w {
			newLine()
		}
		if !empty {
			line.WriteByte(' ')
			lw++
		}
		for _, a := range wd {
			for text := a.text; text != ""; {
				if lw >= w && !empty {
					newLine()
				}
				var part string
				part, text = cutWidth(text, max(1, w-lw))
				line.WriteString(sty(a.st, part))
				lw, empty = lw+width(part), false
			}
		}
	}
	return append(lines, line.String())
}

// section is a titled rule: the title, a muted note, a thin line to the width.
func (p palette) section(title, note string, w int) string {
	s := sty(bold, title)
	if note != "" {
		s += "  " + sty(p.Muted(), note)
	}
	if rest := w - width(s) - 1; rest > 0 {
		s += " " + sty(p.Rule(), strings.Repeat("─", rest))
	}
	return truncate(s, w)
}

// bar draws pct as a bar of w cells in half cells; its color says how close it is to the
// limit, and the number printed beside it says the same without color.
func (p palette) bar(pct, limit float64, w int) string {
	full, half, track := p.barGlyphs()
	halves := int(max(0, min(1, pct/100))*float64(w)*2 + 0.5)
	if pct > 0 && halves == 0 {
		halves = 1
	}
	st := p.OK()
	if pct >= limit {
		st = p.Bad()
	} else if pct >= limit*0.8 {
		st = p.Warn()
	}
	s := strings.Repeat(full, halves/2)
	if halves%2 == 1 {
		s += half
	}
	return sty(st, s) + sty(p.Rule(), strings.Repeat(track, w-(halves+1)/2))
}

// outcome colors a shift's status and gives it its glyph.
func (p palette) outcome(status string) (glyph, st string) {
	switch {
	case status == "ok":
		return "✓", p.OK()
	case status == "pool" || status == "hours" || status == "stopped" || status == "interrupted" || status == "running":
		return "■", p.Warn()
	case status == "":
		return " ", p.Muted()
	}
	return "✗", p.Bad()
}

// shortStatus is a shift's outcome in a word or two.
func shortStatus(s string) string {
	switch {
	case s == "error:error_max_turns":
		return "turn cap"
	case strings.HasPrefix(s, "error:"):
		return "error"
	case s == "no-json":
		return "no result"
	case s == "pool":
		return "pool used"
	case s == "hours":
		return "hours ended"
	}
	return s
}

// ---- markdown and transcripts ----

var journalHead = regexp.MustCompile(`^(\d\d:\d\d) (\S+) on (\S+) \| (.*)$`)

func (p palette) spans(text, base string) []span {
	var out []span
	for _, s := range md.Inline(text) {
		st := base
		switch {
		case s.Code:
			st = join(st, p.Code())
		case s.Link != "":
			st = join(st, join("4", p.Accent()))
		}
		if s.Bold {
			st = join(st, bold)
		}
		if s.Italic {
			st = join(st, "3")
		}
		out = append(out, span{s.Text, st})
	}
	return out
}

// markdown renders what the agents write for a terminal w columns wide. marks are where
// sections start: journal entries when there are any, else the top headings.
func (p palette) markdown(text string, w int) (lines []string, marks []int) {
	return p.blocks(md.Parse(text), w)
}

func (p palette) blocks(blocks []md.Block, w int) (lines []string, marks []int) {
	w = max(w, 10)
	var entries, heads []int
	blank := func() {
		if len(lines) > 0 && lines[len(lines)-1] != "" {
			lines = append(lines, "")
		}
	}
	list := false
	for _, b := range blocks {
		if list && b.Kind != "item" || !list && b.Kind == "item" {
			blank()
		}
		list = b.Kind == "item"
		switch b.Kind {
		case "heading":
			blank()
			if m := journalHead.FindStringSubmatch(b.Text); m != nil && b.Level == 2 {
				entries = append(entries, len(lines))
				lines = append(lines, p.entry(m, w)...)
				continue
			}
			if b.Level <= 2 {
				heads = append(heads, len(lines))
			}
			st := p.Head()
			if b.Level >= 3 {
				st = bold
			}
			lines = append(lines, wrap(p.spans(b.Text, st), w, "", "")...)
		case "para":
			lines = append(lines, wrap(p.spans(b.Text, ""), w, "", "")...)
			blank()
		case "item":
			bullet := []string{"•", "◦", "▪", "▫"}[b.Level]
			if b.Marker != "-" {
				bullet = b.Marker
			}
			pad := strings.Repeat("  ", b.Level)
			lines = append(lines, wrap(p.spans(b.Text, ""), w, pad+sty(p.Accent(), bullet)+" ", pad+strings.Repeat(" ", width(bullet)+1))...)
		case "code":
			for _, l := range b.Lines {
				for first := true; first || l != ""; first = false {
					var part string
					part, l = cutWidth(l, w-2)
					lines = append(lines, "  "+sty(p.Code(), part))
				}
			}
			blank()
		case "rule":
			lines = append(lines, sty(p.Rule(), strings.Repeat("─", w)))
		case "quote":
			inner, _ := p.blocks(b.Blocks, w-2)
			for _, l := range inner {
				lines = append(lines, sty(p.Rule(), "│ ")+p.mute(l))
			}
			blank()
		case "table":
			lines = append(lines, p.table(b.Rows, w)...)
			blank()
		}
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(entries) > 0 {
		return lines, entries
	}
	return lines, heads
}

// mute dims a styled line: everything in it muted.
func (p palette) mute(l string) string {
	if p.Muted() == "" {
		return l
	}
	on := "\x1b[" + p.Muted() + "m"
	return on + strings.ReplaceAll(l, "\x1b[0m", "\x1b[0m"+on) + "\x1b[0m"
}

// entry is a journal entry's heading: "05:00 errors on local | 0.0% of the team pool | ...".
func (p palette) entry(m []string, w int) []string {
	sp := []span{{m[1] + " ", p.Head()}, {m[2], bold}, {" on " + m[3], p.Muted()}}
	for _, part := range strings.Split(m[4], " | ") {
		if part = strings.TrimSpace(part); strings.HasPrefix(part, "log ") {
			continue
		}
		st := p.Muted()
		if _, s := p.outcome(part); part == "ok" || strings.HasPrefix(part, "error") || contains(statuses, part) {
			st = join(s, bold)
		}
		sp = append(sp, span{" · ", p.Rule()}, span{part, st})
	}
	return wrap(sp, w, sty(p.Accent(), "▌ "), sty(p.Accent(), "▌ "))
}

var statuses = []string{"timeout", "stopped", "interrupted", "failed", "lost", "busy", "no-json", "pool", "hours"}

func (p palette) table(rows [][]string, w int) []string {
	n := 0
	for _, r := range rows {
		n = max(n, len(r))
	}
	cells := make([][]string, len(rows))
	widths := make([]int, n)
	for i, r := range rows {
		cells[i] = make([]string, n)
		for j := 0; j < n && j < len(r); j++ {
			st := ""
			if i == 0 {
				st = bold
			}
			for _, s := range p.spans(r[j], st) {
				cells[i][j] += sty(s.st, s.text)
			}
			widths[j] = max(widths[j], width(cells[i][j]))
		}
	}
	for total := func() int {
		t := 3 * (n - 1)
		for _, x := range widths {
			t += x
		}
		return t
	}; total() > w; {
		big := 0
		for j := range widths {
			if widths[j] > widths[big] {
				big = j
			}
		}
		if widths[big] <= 4 {
			break
		}
		widths[big]--
	}
	var out []string
	for i, r := range cells {
		var parts []string
		for j := range r {
			parts = append(parts, fit(r[j], widths[j]))
		}
		out = append(out, strings.Join(parts, sty(p.Rule(), " │ ")))
		if i == 0 {
			var rule []string
			for _, x := range widths {
				rule = append(rule, strings.Repeat("─", x))
			}
			out = append(out, sty(p.Rule(), strings.Join(rule, "─┼─")))
		}
	}
	return out
}

// transcript renders a shift's events. With outputs, what each tool printed is shown (a
// dozen lines) instead of its first line. marks are the tool calls and the end.
func (p palette) transcript(events []core.Event, w int, outputs bool) (lines []string, marks []int) {
	w = max(w, 20)
	for _, e := range events {
		switch e.Kind {
		case "start":
			lines = append(lines, sty(p.Muted(), fmt.Sprintf("── claude started · %s · %d tools", or(e.Model, "default model"), e.Tools)), "")
		case "text":
			if n := len(lines); n > 0 && lines[n-1] != "" {
				lines = append(lines, "")
			}
			md, _ := p.markdown(e.Text, w)
			lines = append(append(lines, md...), "")
		case "tool":
			marks = append(marks, len(lines))
			lines = append(lines, truncate(sty(p.Head(), "▸ "+e.Tool)+" "+e.Arg, w))
		case "output":
			st := p.Muted()
			if e.Error {
				st = p.Bad()
			}
			all := strings.Split(e.Text, "\n")
			if !outputs {
				more := ""
				if len(all) > 1 {
					more = fmt.Sprintf("  (%d lines)", len(all))
				}
				lines = append(lines, sty(st, truncate("  ↳ "+strings.TrimSpace(all[0]), w-width(more)))+sty(p.Muted(), more))
				continue
			}
			for i, l := range all {
				if i == 12 {
					lines = append(lines, sty(p.Rule(), "  │ ")+sty(p.Muted(), fmt.Sprintf("… %d more lines", len(all)-12)))
					break
				}
				lines = append(lines, sty(p.Rule(), "  │ ")+sty(st, truncate(strings.ReplaceAll(l, "\t", "    "), w-4)))
			}
		case "end":
			marks = append(marks, len(lines))
			how, st := "finished", p.OK()
			if e.Error {
				how, st = "ended: "+e.Subtype, p.Bad()
			}
			lines = append(lines, "", sty(join(st, bold), fmt.Sprintf("━━ %s · %d turns", how, e.Turns)), "")
			md, _ := p.markdown(e.Text, w)
			lines = append(lines, md...)
		case "inbox":
			if n := len(lines); n > 0 && lines[n-1] != "" {
				lines = append(lines, "")
			}
			md, _ := p.markdown(e.Text, w-2)
			lines = append(lines, sty(p.Head(), "✉ took a note from its inbox"))
			for _, l := range md {
				lines = append(lines, "  "+l)
			}
			lines = append(lines, "")
		case "note":
			lines = append(lines, wrap([]span{{e.Text, p.Bad()}}, w, "", "")...)
		}
	}
	return lines, marks
}

func or(s, alt string) string {
	if s == "" {
		return alt
	}
	return s
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

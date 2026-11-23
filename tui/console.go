// Package tui is the team console (rota, or rota ui): every page of the web dashboard and
// every control of the menu bar, in the terminal. It reads and changes the team only
// through the core.
package tui

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"rota/core"
)

type rect struct{ x, y, w, h int }

type hit struct {
	r     rect
	click func(x, y int)
	wheel func(d int)
}

type hint struct{ key, what string }

// page is one tab.
type page interface {
	draw(c *console, f *frame, a rect)
	key(c *console, k key) bool
	hints(c *console) []hint
}

type console struct {
	ctx        context.Context
	root, conf string
	exe        string // this program, to start the organizer

	t         *core.Team // as last read
	st        core.Status
	questions []core.Question // open, oldest first
	asked     bool            // the questions were read once: a new one after that rings the bell
	loadErr   string
	loading   bool
	loadedAt  time.Time
	results   chan loaded

	pal       palette
	themeFrom string
	bg        float64
	haveBg    bool
	bgAskedAt time.Time

	w, h, tab int
	shown     int // the tab drawn last
	pages     []page
	names     [][2]string // each tab's name and short name

	input   *lineInput
	picker  *picker
	confirm *confirmBox
	help    bool
	msg     string
	msgBad  bool
	msgAt   time.Time

	hits    []hit
	suspend func(func() error) error
	ask11   func() // asks the terminal for its background; the answer arrives as a key
	bell    func()
	repaint bool
	quit    bool
}

// Run opens the console on the team folder; it returns when it is quit or ctx ends.
func Run(ctx context.Context, root, conf string) error {
	if !IsTerminal(os.Stdin) || !IsTerminal(os.Stdout) {
		return fmt.Errorf("the console needs a terminal; rota status prints the same as text")
	}
	t, err := core.Open(root, conf)
	if err != nil {
		return err
	}
	c := newConsole(ctx, t)
	term, err := newTerminal()
	if err != nil {
		return err
	}
	if err := term.raw(); err != nil {
		return err
	}
	c.bg, c.haveBg = term.background(300 * time.Millisecond) // before the first frame, so it has the right colors
	c.applyTheme()
	c.bgAskedAt = time.Now()
	if err := term.enter(); err != nil {
		term.restore()
		return err
	}
	defer term.leave()
	defer func() {
		if r := recover(); r != nil {
			term.leave()
			panic(r)
		}
	}()
	return c.loop(term)
}

func newConsole(ctx context.Context, t *core.Team) *console {
	exe, _ := os.Executable()
	c := &console{ctx: ctx, root: t.Root, conf: t.File, exe: exe, t: t, results: make(chan loaded, 1), w: 100, h: 30, pal: palettes[0]}
	c.st = core.Status{Agents: nil}
	c.pages = []page{reportsPage(), &statusPage{}, reviewsPage(), issuesPage(), journalPage(), transcriptsPage(), inboxPage(), &spendPage{}, &agentsPage{}, &settingsPage{}}
	c.names = [][2]string{{"Reports", "Rpts"}, {"Status", "Stat"}, {"Reviews", "Revs"}, {"Issues", "Isss"}, {"Journal", "Jrnl"}, {"Transcripts", "Logs"}, {"Inbox", "Inbx"}, {"Spend", "Spnd"}, {"Agents", "Agts"}, {"Settings", "Sets"}}
	return c
}

// The console's pages, in tab order: the report first, where the console opens, then the status.
const (
	tabReports = iota
	tabStatus
	tabReviews
	tabIssues
	tabJournal
	tabTranscripts
	tabInbox
	tabSpend
	tabAgents
	tabSettings
)

func (c *console) loop(term *terminal) error {
	keys := make(chan key, 256)
	var stop, done chan struct{}
	startReader := func() {
		stop, done = make(chan struct{}), make(chan struct{})
		go func(stop, done chan struct{}) { term.readKeys(stop, keys); close(done) }(stop, done)
	}
	startReader()
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	var prev []string
	c.ask11 = func() { term.write("\x1b]11;?\x1b\\") }
	c.bell = func() { term.write("\a") }
	c.suspend = func(fn func() error) error {
		close(stop)
		<-done
		term.leave()
		err := fn()
		term.enter()
		startReader()
		prev = nil
		return err
	}
	c.refresh()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for !c.quit {
		c.w, c.h = term.size()
		if c.repaint {
			prev, c.repaint = nil, false
		}
		prev = c.draw(term, prev)
		select {
		case <-c.ctx.Done():
			return nil
		case k := <-keys:
			c.handle(k)
			for more := true; more && !c.quit; { // a burst of keys (a held arrow, the wheel) before drawing
				select {
				case k := <-keys:
					c.handle(k)
				default:
					more = false
				}
			}
		case <-winch:
			prev = nil
		case l := <-c.results:
			c.apply(l)
		case <-tick.C:
			c.tick()
		}
	}
	return nil
}

// draw writes the lines that changed since the previous frame.
func (c *console) draw(term *terminal, prev []string) []string {
	f := c.frame()
	lines := f.lines()
	var b strings.Builder
	b.WriteString("\x1b[?2026h")
	if len(prev) != len(lines) {
		b.WriteString("\x1b[0m\x1b[2J")
		prev = nil
	}
	for y, l := range lines {
		if y >= len(prev) || prev[y] != l {
			fmt.Fprintf(&b, "\x1b[%d;1H%s", y+1, l)
		}
	}
	if f.cx >= 0 { // a bar cursor while typing
		fmt.Fprintf(&b, "\x1b[%d;%dH\x1b[5 q\x1b[?25h", f.cy+1, f.cx+1)
	} else {
		b.WriteString("\x1b[?25l\x1b[0 q")
	}
	term.write(b.String() + "\x1b[?2026l")
	return lines
}

// ---- the team, reread in the background ----

type loaded struct {
	t         *core.Team
	st        core.Status
	questions []core.Question
	err       error
}

func (c *console) refresh() {
	if c.loading {
		return
	}
	c.loading = true
	go func(root, conf string) {
		t, err := core.Open(root, conf)
		l := loaded{t: t, err: err}
		if err == nil {
			l.st, l.questions = t.Status(time.Now()), t.Questions()
		}
		c.results <- l
	}(c.root, c.conf)
}

func (c *console) apply(l loaded) {
	c.loading, c.loadedAt, c.loadErr = false, time.Now(), ""
	if l.err != nil {
		c.loadErr = l.err.Error()
		return
	}
	c.t, c.st = l.t, l.st
	c.setQuestions(l.questions)
}

// setQuestions takes the open questions; one that is new since the last reading rings the
// terminal's bell and says who asks.
func (c *console) setQuestions(qs []core.Question) {
	known := map[string]bool{}
	for _, q := range c.questions {
		known[q.ID] = true
	}
	for _, q := range qs {
		if c.asked && !known[q.ID] {
			c.say(q.Agent+" asks you a question · a answers", false)
			if c.bell != nil {
				c.bell()
			}
		}
	}
	c.questions, c.asked = qs, true
}

// reread reads the team folder again at once, after the console changed it; the status
// follows in the background.
func (c *console) reread() {
	if t, err := core.Open(c.root, c.conf); err == nil {
		c.t, c.loadErr = t, ""
	} else {
		c.loadErr = err.Error()
	}
	c.loadedAt = time.Time{}
}

func (c *console) tick() {
	every := 5 * time.Second
	if c.st.Pid != 0 {
		every = 2 * time.Second
	}
	if time.Since(c.loadedAt) >= every {
		c.refresh()
	}
	if c.msg != "" && time.Since(c.msgAt) > 8*time.Second {
		c.msg = ""
	}
	// a terminal may switch between light and dark with the system's appearance
	if name, _ := themeSetting(c.root); name == "auto" && time.Since(c.bgAskedAt) > 30*time.Second && c.ask11 != nil {
		c.bgAskedAt = time.Now()
		c.ask11()
	}
}

func (c *console) applyTheme() {
	p, from := resolveTheme(c.root, c.bg, c.haveBg)
	if p.name != c.pal.name {
		c.repaint = true
	}
	c.pal, c.themeFrom = p, from
	for _, pg := range c.pages {
		if b, ok := pg.(*browser); ok {
			b.view.key, b.loaded = "", time.Time{} // drawn again in the new colors
		}
	}
}

// ---- the frame ----

const margin = 2

func (c *console) frame() *frame {
	f := newFrame(c.w, c.h)
	c.hits = c.hits[:0]
	if c.w < 50 || c.h < 12 {
		msg := "rota needs 50 × 12 or more"
		f.put(max(0, (c.w-len(msg))/2), c.h/2, msg, c.w)
		return f
	}
	if c.tab != c.shown { // a filter is for the visit to Settings
		if sp, ok := c.pages[c.shown].(*settingsPage); ok {
			sp.filter = ""
		}
		c.shown = c.tab
	}
	c.drawTabs(f)
	top, bottom := 3, 2
	if c.h < 22 {
		top, bottom = 2, 1
	}
	c.pages[c.tab].draw(c, f, rect{0, top, c.w, c.h - top - bottom})
	c.drawBottom(f)
	if c.help {
		c.drawHelp(f)
	}
	if c.picker != nil {
		c.drawPicker(f)
	}
	return f
}

func (c *console) organizer(short bool) string {
	p, st := c.pal, c.st
	switch {
	case st.Pid == 0 && short:
		return sty(p.Muted(), "○ off")
	case st.Pid == 0:
		return sty(p.Muted(), "○ organizer off")
	case st.Stale:
		return sty(p.Warn(), "◌ silent")
	case st.Running != nil:
		return sty(p.Head(), fmt.Sprintf("▸ %s %dm", st.Running.Agent, int(time.Since(st.Running.Since).Minutes())))
	case c.t.Paused():
		return sty(p.Warn(), "‖ paused")
	case st.Held() != "":
		return sty(p.Warn(), "▲ on hold")
	}
	return sty(p.OK(), "● idle")
}

// drawTabs draws the tabs, the active one marked by color, weight and the heavy rule under
// it, and the organizer and the clock on the right; a narrow terminal gets what fits.
func (c *console) drawTabs(f *frame) {
	p := c.pal
	for form := 0; form < 5; form++ {
		names, gap, brand, clock := form/2, 3-min(form, 1), "◆ rota beta", form < 3 // the pages are rota's; the console is beta
		if form >= 3 {
			brand = "◆ rota"
		}
		if form == 4 {
			brand = "◆"
		}
		right := c.organizer(form > 0)
		badge := ""
		if n := len(c.questions); n > 0 {
			badge = fmt.Sprintf("? %d", n)
			if form == 0 {
				badge += map[bool]string{true: " question", false: " questions"}[n == 1]
			}
			badge = sty(join(p.Warn(), bold), badge)
			right = badge + "  " + right
		}
		if clock {
			right += sty(p.Muted(), "  "+time.Now().Format("15:04"))
		}
		labels, total := make([]string, len(c.names)), 0
		for i, n := range c.names {
			labels[i] = strings.TrimSpace(fmt.Sprintf("%d %s", (i+1)%10, []string{n[0], n[1], ""}[min(names, 2)])) // the tenth is 0
			total += width(labels[i]) + gap
		}
		x := margin + width(brand) + gap + 1
		if x+total+width(right)+margin > c.w && form < 4 {
			continue
		}
		f.put(margin, 0, sty(p.Head(), brand), -1)
		f.put(0, 1, sty(p.Rule(), strings.Repeat("─", c.w)), -1)
		for i, l := range labels {
			w := width(l)
			if i == c.tab {
				f.put(x, 0, sty(p.Head(), l), w)
				f.put(x, 1, sty(p.Head(), strings.Repeat("━", w)), w)
			} else {
				num, name, _ := strings.Cut(l, " ")
				f.put(x, 0, sty(p.Muted(), num)+" "+name, w)
			}
			tab := i
			c.hits = append(c.hits, hit{r: rect{x - 1, 0, w + 2, 2}, click: func(int, int) { c.tab = tab }})
			x += w + gap
		}
		f.put(c.w-width(right)-margin, 0, right, -1)
		if badge != "" {
			c.hits = append(c.hits, hit{r: rect{c.w - width(right) - margin, 0, width(badge), 1}, click: func(int, int) { c.answerQuestion() }})
		}
		return
	}
}

// drawBottom is the line of keys, or what is being typed, or a question; the last action's
// message shows on the right with ✓ or ✗.
func (c *console) drawBottom(f *frame) {
	p, y, x := c.pal, c.h-1, margin
	key := func(k, what string) string { return sty(join(p.Accent(), bold), k) + " " + sty(p.Muted(), what) }
	switch {
	case c.input != nil:
		in := c.input
		prompt := sty(p.Head(), in.prompt)
		keys := key("⏎", "save") + "   " + key("esc", "cancel")
		room := c.w - x - width(prompt) - width(keys) - 2*margin
		if room < 20 {
			keys, room = "", c.w-x-width(prompt)-margin
		}
		start := 0 // scroll so the cursor stays in view
		for width(string(in.text[start:in.pos])) > room-1 {
			start++
		}
		f.put(x, y, prompt, -1)
		f.put(x+width(prompt), y, string(in.text[start:]), room)
		f.put(c.w-width(keys)-margin, y, keys, -1)
		f.cx, f.cy = x+width(prompt)+width(string(in.text[start:in.pos])), y
		return
	case c.confirm != nil:
		f.put(x, y, sty(join(p.Warn(), bold), "▲ "+c.confirm.text)+"   "+key("y", "yes")+"   "+key("n", "no"), c.w-2*margin)
		return
	}
	msg := ""
	if c.msg != "" {
		icon, st := "✓ ", p.OK()
		if c.msgBad {
			icon, st = "✗ ", p.Bad()
		}
		msg = truncate(sty(st, icon+c.msg), c.w*3/5)
	}
	help := key("?", "keys")
	room := c.w - 2*margin - width(msg) - width(help) - 6
	team := []hint{{"p", "pause"}, {"R", "run next"}, {"s", "organizer"}, {"q", "quit"}}
	if len(c.questions) > 0 {
		team = append([]hint{{"a", "answer"}, {"D", "dismiss"}}, team...)
	}
	for _, h := range append(c.pages[c.tab].hints(c), team...) {
		part := key(h.key, h.what)
		if width(part) > room {
			break // whole hints only: ? lists them all
		}
		x += f.put(x, y, part, -1) + 3
		room -= width(part) + 3
	}
	f.put(x, y, help, -1)
	if msg != "" {
		f.put(c.w-width(msg)-margin, y, msg, -1)
	}
}

var helpGroups = []struct {
	title string
	keys  [][2]string
}{
	{"Moving around", [][2]string{{"1–9 0  Tab  ⇧Tab", "switch tabs"}, {"↑ ↓   j k", "move, scroll"}, {"⏎  →  l", "open, change"},
		{"←  h  Esc", "back"}, {"PgUp PgDn  ␣  b", "page"}, {"g  G", "top, bottom"}, {"[  ]", "previous, next entry"},
		{"/  n  N", "find, next, previous"}, {"mouse", "wheel scrolls; click selects"}, {"", "a second click opens"}}},
	{"Anywhere", [][2]string{{"?", "this sheet"}, {"q", "quit"}, {"Ctrl-Z", "suspend"}, {"Ctrl-L", "redraw"}}},
	{"The team", [][2]string{{"a", "answer an agent's question"}, {"D", "dismiss one without an answer"}, {"p", "pause or resume"}, {"R", "run the next shift now"}, {"c", "stop the running shift"},
		{"s", "start or stop the organizer"}, {"w", "open the web pages"}, {"B", "on Spend or Agents: rebalance the shares"}}},
	{"The selected agent", [][2]string{{"e", "enable or disable"}, {"P", "pause or resume it"}, {"r", "run it now"},
		{"i", "leave a note: to it, or about a pull request or issue it works on"}, {"o", "its latest transcript"}, {"E", "edit its file"}}},
	{"Forms and transcripts", [][2]string{{"⏎  ␣", "change the field"}, {"d", "back to the default"},
		{"x", "show tool output"}, {"f", "follow a running shift"}}},
}

// drawHelp is the sheet of keys: two columns when there is room, then a line about colors.
func (c *console) drawHelp(f *frame) {
	p := c.pal
	column := func(from, to int) (out []string, w int) {
		for i, g := range helpGroups[from:to] {
			if i > 0 {
				out = append(out, "")
			}
			out = append(out, sty(bold, g.title))
			for _, k := range g.keys {
				out = append(out, sty(join(p.Accent(), bold), fit(k[0], 15))+"  "+k[1])
			}
		}
		for _, l := range out {
			w = max(w, width(l))
		}
		return out, w
	}
	left, lw := column(0, 2)
	right, rw := column(2, len(helpGroups))
	var lines []string
	if lw+rw+16 <= c.w {
		for i := 0; i < max(len(left), len(right)); i++ {
			l, r := "", ""
			if i < len(left) {
				l = left[i]
			}
			if i < len(right) {
				r = right[i]
			}
			lines = append(lines, fit(l, lw)+"      "+r)
		}
	} else {
		lines = append(append(left, ""), right...)
	}
	w := 0
	for _, l := range lines {
		w = max(w, width(l))
	}
	w = min(w+8, c.w-2)
	note := fmt.Sprintf("Colors: %s, from %s. Settings › Console › theme changes them; NO_COLOR turns them off.", c.pal.name, c.themeFrom)
	lines = append(append(lines, ""), wrap([]span{{note, p.Muted()}}, w-8, "", "")...)
	h := min(len(lines)+4, c.h-2)
	x, y := (c.w-w)/2, max(0, min((c.h-h)/2, c.h-1-h))
	f.box(p, x, y, w, h, "Keys")
	for i := 0; i < h-4 && i < len(lines); i++ {
		f.put(x+4, y+2+i, lines[i], w-8)
	}
	c.hits = append(c.hits, hit{r: rect{0, 0, c.w, c.h}, click: func(int, int) { c.help = false }})
}

// ---- overlays ----

type lineInput struct {
	prompt string
	text   []rune
	pos    int
	done   func(string)
}

type picker struct {
	title          string
	values, labels []string
	sel, cur       int // where the arrows are; the value in force (-1: none)
	done           func(string)
	// Of a picker of any number of values: those ticked, and what ⏎ does with them.
	on       []bool
	doneSome func([]string)
}

type confirmBox struct {
	text string
	yes  func()
}

func (c *console) say(msg string, bad bool) { c.msg, c.msgBad, c.msgAt = msg, bad, time.Now() }

func (c *console) sayErr(err error) bool {
	if err != nil {
		c.say(err.Error(), true)
	}
	return err != nil
}

func (c *console) prompt(p, initial string, done func(string)) {
	c.input = &lineInput{prompt: p, text: []rune(initial), pos: len([]rune(initial)), done: done}
}

func (c *console) ask(text string, yes func()) { c.confirm = &confirmBox{text, yes} }

func (c *console) pick(title string, values, labels []string, cur string, done func(string)) {
	p := &picker{title: title, values: values, labels: labels, done: done, cur: -1}
	for i, v := range values {
		if v == cur {
			p.sel, p.cur = i, i
		}
	}
	c.picker = p
}

// pickSome asks for any number of values, ticked as given: space ticks or unticks the one the
// arrows are on, ⏎ hands done those ticked.
func (c *console) pickSome(title string, values, labels, ticked []string, done func([]string)) {
	p := &picker{title: title, values: values, labels: labels, cur: -1, on: make([]bool, len(values)), doneSome: done}
	for i, v := range values {
		p.on[i] = slices.Contains(ticked, v)
	}
	c.picker = p
}

// toggle ticks or unticks value n of a picker of several; of one, it chooses it.
func (c *console) toggle(p *picker, n int) {
	if p.doneSome != nil {
		p.on[n] = !p.on[n]
		return
	}
	c.picker = nil
	p.done(p.values[n])
}

func (c *console) inputKey(k key) {
	in := c.input
	insert := func(rs []rune) {
		in.text = append(append(append([]rune{}, in.text[:in.pos]...), rs...), in.text[in.pos:]...)
		in.pos += len(rs)
	}
	switch k.name {
	case "enter":
		c.input = nil
		in.done(string(in.text))
	case "esc", "ctrl+c", "ctrl+g":
		c.input = nil
	case "backspace":
		if in.pos > 0 {
			in.text, in.pos = append(in.text[:in.pos-1], in.text[in.pos:]...), in.pos-1
		}
	case "delete", "ctrl+d":
		if in.pos < len(in.text) {
			in.text = append(in.text[:in.pos], in.text[in.pos+1:]...)
		}
	case "left", "ctrl+b":
		in.pos = max(0, in.pos-1)
	case "right", "ctrl+f":
		in.pos = min(len(in.text), in.pos+1)
	case "home", "ctrl+a":
		in.pos = 0
	case "end", "ctrl+e":
		in.pos = len(in.text)
	case "ctrl+u":
		in.text, in.pos = in.text[in.pos:], 0
	case "ctrl+k":
		in.text = in.text[:in.pos]
	case "ctrl+w":
		j := in.pos
		for j > 0 && in.text[j-1] == ' ' {
			j--
		}
		for j > 0 && in.text[j-1] != ' ' {
			j--
		}
		in.text, in.pos = append(in.text[:j], in.text[in.pos:]...), j
	case "paste":
		insert([]rune(strings.Join(strings.Fields(k.paste), " ")))
	case "":
		if k.r >= 0x20 {
			insert([]rune{k.r})
		}
	}
}

func (c *console) pickerKey(k key) {
	p := c.picker
	switch {
	case k.name == "up" || k.r == 'k':
		p.sel = (p.sel + len(p.values) - 1) % len(p.values)
	case k.name == "down" || k.r == 'j' || k.name == "tab":
		p.sel = (p.sel + 1) % len(p.values)
	case k.name == "enter" && p.doneSome != nil:
		c.picker = nil
		var ticked []string
		for i, v := range p.values {
			if p.on[i] {
				ticked = append(ticked, v)
			}
		}
		p.doneSome(ticked)
	case k.name == "enter" || k.r == ' ':
		c.toggle(p, p.sel)
	case k.name == "esc" || k.r == 'q' || k.name == "ctrl+c":
		c.picker = nil
	}
}

// drawPicker shows the choices in a box: ● marks the value in force, ☑ those ticked, the band
// the one the arrows are on.
func (c *console) drawPicker(f *frame) {
	p, pk := c.pal, c.picker
	foot := sty(join(p.Accent(), bold), "⏎") + sty(p.Muted(), " choose   ") + sty(join(p.Accent(), bold), "esc") + sty(p.Muted(), " cancel")
	if pk.doneSome != nil {
		foot = sty(join(p.Accent(), bold), "space") + sty(p.Muted(), " tick   ") + sty(join(p.Accent(), bold), "⏎") + sty(p.Muted(), " apply   ") +
			sty(join(p.Accent(), bold), "esc") + sty(p.Muted(), " cancel")
	}
	w := max(width(pk.title)+8, width(foot)+6)
	for _, l := range pk.labels {
		w = max(w, width(l)+10)
	}
	w = min(w, c.w-4)
	rows := min(len(pk.labels), c.h-8)
	h := rows + 5
	x, y := (c.w-w)/2, (c.h-h)/2
	f.box(p, x, y, w, h, pk.title)
	top := max(0, pk.sel-rows+1)
	for i := 0; i < rows && top+i < len(pk.labels); i++ {
		n := top + i
		radio := sty(p.Muted(), "○")
		switch {
		case pk.doneSome != nil && pk.on[n]:
			radio = sty(p.Accent(), "☑")
		case pk.doneSome != nil:
			radio = sty(p.Muted(), "☐")
		case pk.cur == n:
			radio = sty(p.Accent(), "●")
		}
		f.put(x+3, y+2+i, radio+" "+pk.labels[n], w-6)
		if n == pk.sel {
			f.band(p, x+1, y+2+i, w-2, true)
		}
		c.hits = append(c.hits, hit{r: rect{x + 1, y + 2 + i, w - 2, 1}, click: func(int, int) {
			pk.sel = n
			c.toggle(pk, n)
		}})
	}
	f.put(x+3, y+h-2, foot, w-6)
}

// ---- keys ----

func (c *console) handle(k key) {
	switch {
	case k.name == "background":
		if lum, ok := parseBackground([]byte(k.paste)); ok {
			c.bg, c.haveBg = lum, true
			c.applyTheme()
		}
		return
	case k.name == "click" || k.name == "wheelup" || k.name == "wheeldown":
		if c.help && k.name == "click" {
			c.help = false
		} else if c.input == nil && c.confirm == nil && !c.help && (c.picker == nil || k.name == "click") {
			c.mouse(k)
		}
		return
	case c.confirm != nil:
		cf := c.confirm
		c.confirm = nil
		if k.r == 'y' || k.r == 'Y' || k.name == "enter" {
			cf.yes()
		}
		return
	case c.input != nil:
		c.inputKey(k)
		return
	case c.picker != nil:
		c.pickerKey(k)
		return
	case c.help:
		c.help = false
		return
	}
	switch {
	case k.name == "ctrl+c" || k.r == 'q':
		c.quit = true
	case k.name == "ctrl+z":
		c.suspend(func() error { return syscall.Kill(0, syscall.SIGTSTP) })
	case k.name == "ctrl+l":
		c.repaint = true
	case k.r == '?':
		c.help = true
	case k.r >= '1' && int(k.r-'1') < len(c.pages):
		c.tab = int(k.r - '1')
	case k.r == '0' && len(c.pages) >= 10: // the tenth tab
		c.tab = 9
	case k.name == "tab":
		c.tab = (c.tab + 1) % len(c.pages)
	case k.name == "backtab":
		c.tab = (c.tab + len(c.pages) - 1) % len(c.pages)
	case k.r == 'a':
		c.answerQuestion()
	case k.r == 'D':
		c.dismissQuestion()
	case k.r == 'p':
		c.togglePause()
	case k.r == 'R':
		c.runNow("")
	case k.r == 'c':
		c.cancelShift()
	case k.r == 's':
		c.toggleOrganizer()
	case k.r == 'w':
		c.openWeb()
	default:
		c.pages[c.tab].key(c, k)
	}
}

func (c *console) mouse(k key) {
	for i := len(c.hits) - 1; i >= 0; i-- {
		h := c.hits[i]
		if k.x < h.r.x || k.x >= h.r.x+h.r.w || k.y < h.r.y || k.y >= h.r.y+h.r.h {
			continue
		}
		switch {
		case k.name == "click" && h.click != nil:
			h.click(k.x-h.r.x, k.y-h.r.y)
			return
		case k.name == "wheelup" && h.wheel != nil:
			h.wheel(-3)
			return
		case k.name == "wheeldown" && h.wheel != nil:
			h.wheel(3)
			return
		}
	}
}

// agentKey handles the keys that act on one agent, wherever one is selected.
func (c *console) agentKey(name string, k key) bool {
	a := c.t.Agent(name)
	if a == nil {
		return false
	}
	switch k.r {
	case 'e':
		c.setAgent(name, "enabled", fmt.Sprint(!a.Enabled))
	case 'P':
		if c.t.PausedAgents()[name] {
			if !c.sayErr(c.t.ResumeAgent(name)) {
				c.say(name+" resumed", false)
			}
		} else if !c.sayErr(c.t.PauseAgent(name)) {
			c.say(name+" paused: its running shift finishes, no new one starts; r still runs it", false)
		}
	case 'r':
		c.runNow(name)
	case 'i':
		c.note(name)
	case 'o':
		if trs := c.t.Transcripts(name); len(trs) > 0 {
			c.tab = tabTranscripts
			c.pages[tabTranscripts].(*browser).open(trs[0].Path)
		} else {
			c.say(name+" has no transcript yet", true)
		}
	case 'E':
		c.edit(c.t.AgentFile(name))
	default:
		return false
	}
	return true
}

// ---- actions ----

// pickQuestion calls do with the only open question or the one picked.
func (c *console) pickQuestion(title string, do func(core.Question)) {
	qs := c.questions
	switch len(qs) {
	case 0:
		c.say("no agent has a question for you", false)
	case 1:
		do(qs[0])
	default:
		var ids, labels []string
		for _, q := range qs {
			ids, labels = append(ids, q.ID), append(labels, q.Agent+": "+q.Line())
		}
		c.pick(title, ids, labels, "", func(id string) {
			for _, q := range qs {
				if q.ID == id {
					do(q)
				}
			}
		})
	}
}

// closed drops a question just closed here, ahead of the next reading.
func (c *console) closed(id string) {
	for i := range c.questions {
		if c.questions[i].ID == id {
			c.questions = append(c.questions[:i:i], c.questions[i+1:]...)
			break
		}
	}
	c.loadedAt = time.Time{}
}

// dismissQuestion closes an agent's open question without an answer, once confirmed; the agent
// is told in its inbox.
func (c *console) dismissQuestion() {
	c.pickQuestion("Dismiss which question?", func(q core.Question) {
		c.tab = tabStatus
		c.ask("Dismiss "+q.Agent+"'s question without an answer?", func() {
			if !c.sayErr(c.t.Dismiss(q.ID)) {
				c.say(q.Agent+"'s question dismissed; its inbox says so", false)
				c.closed(q.ID)
			}
		})
	})
}

// answerQuestion answers an agent's open question, the only one or the one picked; the answer
// goes to the agent's inbox for its next shift.
func (c *console) answerQuestion() {
	c.pickQuestion("Answer which question?", func(q core.Question) {
		c.tab = tabStatus // the status page shows the question while the answer is typed
		c.prompt("answer for "+q.Agent+": ", "", func(text string) {
			if c.sayErr(c.t.Answer(q.ID, text)) || strings.TrimSpace(text) == "" {
				return
			}
			c.say("answer left in "+q.Agent+"'s inbox", false)
			c.closed(q.ID)
		})
	})
}

func (c *console) togglePause() {
	if c.t.Paused() {
		if !c.sayErr(c.t.Unpause()) {
			c.say("team resumed", false)
		}
	} else if !c.sayErr(c.t.Pause()) {
		c.say("team paused: the running shift finishes, no new one starts", false)
	}
	c.loadedAt = time.Time{}
}

// runNow asks for a shift now: the agent's, or the next eligible one.
func (c *console) runNow(name string) {
	if c.st.Pid == 0 {
		c.say("the organizer is not running; s starts it", true)
		return
	}
	for _, a := range c.st.Agents {
		if a.Name == name && a.State == "disabled" {
			c.say(name+" is disabled; e enables it", true)
			return
		}
	}
	if said, err := c.t.AskToRun(name, c.st); !c.sayErr(err) {
		c.say(said, false)
	}
}

func (c *console) cancelShift() {
	if c.st.Running == nil {
		c.say("no shift is running", true)
		return
	}
	agent := c.st.Running.Agent
	c.ask("Stop "+agent+"'s shift? It is killed and recorded as stopped.", func() {
		if !c.sayErr(c.t.CancelShift("")) {
			c.say("stopping "+agent+"'s shift", false)
		}
	})
}

func (c *console) toggleOrganizer() {
	if c.st.Pid != 0 {
		q := "Stop the organizer? No shift starts until it runs again."
		if c.st.Running != nil {
			q = "Stop the organizer? " + c.st.Running.Agent + "'s running shift is stopped."
		}
		c.ask(q, func() {
			if !c.sayErr(c.t.StopOrganizer()) {
				c.say("organizer stopped", false)
			}
			c.loadedAt = time.Time{}
		})
		return
	}
	if _, err := c.t.StartOrganizer(c.exe, true); !c.sayErr(err) {
		c.say("organizer started in the background; output in rota.log; s stops it", false)
	}
	c.loadedAt = time.Time{}
}

// note leaves the agent a note: first, when it works on pull requests or issues, where to,
// each as agents address it; then the note.
func (c *console) note(name string) {
	addrs := c.t.Addresses(name)
	write := func(to core.Address) {
		when := "read at its next shift"
		if c.t.Steerable(name) {
			when = "its running shift reads it after its current step"
		}
		c.prompt("note for "+to.String()+" ("+when+"): ", "", func(text string) {
			if !c.sayErr(c.t.LeaveNote(name, to.Ref, "human", text)) && strings.TrimSpace(text) != "" {
				c.say("note for "+to.String()+": "+when, false)
			}
		})
	}
	if len(addrs) < 2 {
		write(core.Address{Agent: name})
		return
	}
	values, labels := make([]string, len(addrs)), make([]string, len(addrs))
	for i, a := range addrs {
		values[i], labels[i] = a.Ref, a.Label()
	}
	c.pick("a note to", values, labels, "", func(ref string) {
		for _, a := range addrs {
			if a.Ref == ref {
				write(a)
			}
		}
	})
}

// forget deletes one of an agent's notes, chosen from them, after asking.
func (c *console) forget(agent string) {
	notes := c.t.Notes(agent)
	if len(notes) == 0 {
		c.say("no notes waiting for "+agent, true)
		return
	}
	values, labels := make([]string, len(notes)), make([]string, len(notes))
	for i, n := range notes {
		about := ""
		if n.About != "" {
			about = " · " + c.t.AddressOf(agent, n.About).String()
		}
		values[i], labels[i] = n.ID, c.t.NoteHead(n)+about+": "+core.FirstLine(n.Text, 60)
	}
	c.pick("delete a note for "+agent, values, labels, "", func(id string) {
		c.ask("Delete this note for "+agent+"?", func() {
			if !c.sayErr(c.t.DeleteNote(agent, id)) {
				c.say("note deleted", false)
			}
		})
	})
}

func editor() []string {
	for _, v := range []string{"VISUAL", "EDITOR"} {
		if words := strings.Fields(os.Getenv(v)); len(words) > 0 {
			return words
		}
	}
	return []string{"vi"}
}

// edit opens a file in $VISUAL or $EDITOR with the console out of the way.
func (c *console) edit(path string) error {
	ed := editor()
	err := c.suspend(func() error {
		cmd := exec.Command(ed[0], append(ed[1:], path)...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return cmd.Run()
	})
	if err != nil {
		c.say(ed[0]+": "+err.Error(), true)
	}
	c.reread()
	return err
}

func (c *console) openWeb() {
	if c.t.Web == "off" {
		c.say("the pages are off (web: off in rota.conf)", true)
		return
	}
	conn, err := net.DialTimeout("tcp", c.t.WebAddr(), 300*time.Millisecond)
	if err != nil {
		c.say("nothing serves the pages: they come with the organizer (s), or rota web", true)
		return
	}
	conn.Close()
	openURL(c, c.t.WebURL("/"))
}

// setAgent writes one setting into an agent's file; empty means the team default.
func (c *console) setAgent(name, key, value string) {
	value = strings.TrimSpace(value)
	if c.sayErr(c.t.SetAgent(name, map[string]string{key: value})) {
		return
	}
	c.reread()
	switch {
	case key == "enabled" && value == "true":
		c.say(name+" enabled", false)
	case key == "enabled":
		c.say(name+" disabled", false)
	case value == "":
		c.say(name+": "+key+" back to the team default", false)
	default:
		c.say(name+": "+key+" = "+value+" · applies at the next shift", false)
	}
}

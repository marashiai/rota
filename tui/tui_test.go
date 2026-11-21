package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

func screen(c *console) string {
	var b strings.Builder
	for _, l := range c.frame().lines() {
		b.WriteString(strings.TrimRight(plain(l), " ") + "\n")
	}
	return b.String()
}

// notesOf is an agent's notes as it reads them.
func notesOf(t *core.Team, agent string) string { return t.NotesText(t.Notes(agent)) }

func typeText(c *console, s string) {
	for _, r := range s {
		c.handle(key{r: r})
	}
}

func press(c *console, names ...string) {
	for _, n := range names {
		c.handle(key{name: n})
	}
}

// console is a console on a team folder with one finished shift.
func newTestConsole(t *testing.T) (*console, string) {
	t.Helper()
	root := t.TempDir()
	stub := filepath.Join(root, "claude")
	write(t, stub, "#!/bin/bash\nhead -n1 >/dev/null\n"+
		`echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}'`+"\n"+
		`printf '{"type":"result","subtype":"success","is_error":false,"result":"ran in %s","num_turns":3,"session_id":"s1","usage":{}}' "$PWD"`+"\n")
	os.Chmod(stub, 0o755)
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\ncommit: false\nsync_repos: false\nweb: off\nclaude_bin: "+stub+"\ntick_minutes: 60            # how often\n")
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: true\nweight: 3\n---\nAlpha fixes **bugs**.\n")
	write(t, filepath.Join(root, "agents", "beta.md"), "---\nenabled: false\npriority: 5\n---\nBeta.\n")
	write(t, filepath.Join(root, "reports", "2026-10-29.md"), "# What changed\n\n## Merged\n\n- backend#63\n")
	write(t, filepath.Join(root, "inbox", "gone.json"), `[{"id":"g1","at":"2026-10-29T10:00:00Z","from":"human","text":"for an agent that was removed"}]`)
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
	tm, _ = core.Open(root, "rota.conf")
	startBrowser = func(url string) error { return fmt.Errorf("a test opens no browser: %s", url) }
	c := newConsole(context.Background(), tm)
	c.apply(loaded{t: tm, st: tm.Status(time.Now())})
	c.w, c.h = 120, 40
	c.suspend = func(func() error) error { t.Fatal("nothing here should leave the console"); return nil }
	return c, root
}

func TestTheConsoleShowsEveryPage(t *testing.T) {
	c, _ := newTestConsole(t)
	for _, tc := range []struct {
		tab   rune
		wants []string
	}{
		{'1', []string{"◆ rota", "1 R", "2 Stat", "4 Iss", "0 Set", "Report of 2026-10-29", "Merged"}},
		{'2', []string{"○ off", "○ not running · s starts it", "Agents  1 enabled of 2", "● alpha", "○ beta", "Recent shifts", "✓ ok", "disk", "0.0 / 100 GB", "– / 100 GB"}},
		{'3', []string{"No open pull requests"}},
		{'4', []string{"No open issues"}},
		{'5', []string{"Days  1", "1 shift", "▌", "alpha on local"}},
		{'6', []string{"Shifts  1", "alpha", "✓ ok · 3 turns", "▸ Bash ls", "finished · 3 turns"}},
		{'7', []string{"Agents  3", "gone", "Notes for beta", "no notes waiting for beta"}},
		{'8', []string{"This budget week", "team", "Every shift", "alpha", "session: s1", "output tokens: 0", "retry: –"}},
		{'9', []string{"Agents  2", "● alpha", "○ beta", "Schedule", "Share of the team pool", "Enabled", "Claude Code's default", "· default", "Disk cap", " · disk "}},
		{'0', []string{"rota.conf", "Console", "theme", "auto", "Claude", "claude_bin", "· default"}},
	} {
		c.handle(key{r: tc.tab})
		got := screen(c)
		for _, want := range tc.wants {
			if !strings.Contains(got, want) {
				t.Errorf("tab %c should show %q:\n%s", tc.tab, want, got)
			}
		}
		if strings.Contains(got, "$") || strings.Contains(strings.ToLower(got), "usd") {
			t.Errorf("tab %c shows a dollar value:\n%s", tc.tab, got)
		}
	}
	if c.handle(key{r: '0'}); c.tab != tabSettings {
		t.Errorf("0 is the last tab, Settings, got tab %d", c.tab)
	}
	// every tab, focused or not, at every size, draws lines exactly as wide as the screen
	for _, size := range [][2]int{{50, 12}, {80, 24}, {233, 70}} {
		c.w, c.h = size[0], size[1]
		for tab := range c.pages {
			c.tab = tab
			for _, focus := range []bool{false, true} {
				for _, p := range c.pages {
					switch pg := p.(type) {
					case *browser:
						pg.focus = focus
					case *agentsPage:
						pg.focus = focus
					}
				}
				for _, l := range c.frame().lines() {
					if w := width(l); w != c.w {
						t.Fatalf("%dx%d tab %d: a line is %d wide", c.w, c.h, tab, w)
					}
				}
			}
		}
	}
	c.w, c.h = 40, 10
	if !strings.Contains(screen(c), "rota needs 50 × 12") {
		t.Fatal("a tiny terminal says so")
	}
}

// r and m narrow the Reviews tab to a repository and to a state, each in turn, then back to all.
func TestTheConsoleReviewsNarrow(t *testing.T) {
	c, root := newTestConsole(t)
	write(t, filepath.Join(root, ".open.json"), `{"time":"`+time.Now().Format(time.RFC3339)+`","repos":{`+
		`"o/app":{"1":{"title":"app one","pr":true,"labels":["rota:approved"]},"2":{"title":"app two","pr":true,"labels":["rota:leave-alone"]}},`+
		`"o/web":{"3":{"title":"web three","pr":true,"labels":["rota:approved"]}}}}`)
	c.handle(key{r: '3'})
	shows := func(keys string, wants, not []string) {
		t.Helper()
		typeText(c, keys)
		got := screen(c)
		for _, w := range wants {
			if !strings.Contains(got, w) {
				t.Fatalf("after %q it should show %q:\n%s", keys, w, got)
			}
		}
		for _, w := range not {
			if strings.Contains(got, w) {
				t.Fatalf("after %q it should not show %q:\n%s", keys, w, got)
			}
		}
	}
	shows("", []string{"Pull requests  3", "app one", "web three", "r repository", "m state"}, nil)
	shows("r", []string{"Pull requests · app  2", "app one", "app two"}, []string{"web three"})
	shows("m", []string{"Pull requests · app · ✅  1", "app one"}, []string{"app two", "web three"})
	shows("r", []string{"Pull requests · web · ✅  1", "web three"}, []string{"app one"})
	shows("m", []string{"No open pull request matches web and ✋; r, m and L"}, []string{"web three"})
	shows("m", []string{"Pull requests · web  1", "web three"}, nil)
	if f := c.t.ListFilter(core.ReviewsTab); f.Repo != "o/web" || f.Mark != "" {
		t.Fatalf("the choice is not kept for the pages and the next console: %+v", f)
	}
	shows("r", []string{"Pull requests  3", "app one", "app two", "web three"}, nil)

	// a choice made in the pages, or kept from an earlier console, narrows the list
	if err := c.t.SetListFilter(core.ReviewsTab, core.ListFilter{Repo: "o/app", Mark: "✅"}); err != nil {
		t.Fatal(err)
	}
	c.pages[2].(*browser).loaded = time.Time{} // as when it reads the list again, every 2 seconds
	shows("", []string{"Pull requests · app · ✅  1", "app one"}, []string{"app two", "web three"})
	if err := c.t.SetListFilter(core.ReviewsTab, core.ListFilter{Repo: "o/web"}); err != nil { // chosen in the pages, the list not read again yet
		t.Fatal(err)
	}
	shows("m", []string{"Pull requests · web · ✅  1", "web three"}, []string{"app one"})
	if f := c.t.ListFilter(core.ReviewsTab); f.Repo != "o/web" || f.Mark != "✅" {
		t.Fatalf("m changed the state of a list the pages narrowed since: %+v", f)
	}
	if err := c.t.SetListFilter(core.ReviewsTab, core.ListFilter{Repo: "o/app", Mark: "✅"}); err != nil {
		t.Fatal(err)
	}
	tm, _ := core.Open(root, "rota.conf")
	c = newConsole(context.Background(), tm) // a console opened later
	c.apply(loaded{t: tm, st: tm.Status(time.Now())})
	c.w, c.h = 120, 40
	c.handle(key{r: '3'})
	shows("", []string{"Pull requests · app · ✅  1", "app one"}, []string{"app two", "web three"})
}

// L ticks labels: the Reviews tab keeps the pull requests that carry every one, on themselves or
// on an open issue they are for.
func TestTheConsoleReviewsNarrowByLabels(t *testing.T) {
	c, root := newTestConsole(t)
	write(t, filepath.Join(root, ".open.json"), `{"time":"`+time.Now().Format(time.RFC3339)+`","repos":{`+
		`"o/app":{"1":{"title":"app one","pr":true,"issues":["o/app#10"]},"2":{"title":"app two","pr":true,"labels":["P1"]},`+
		`"10":{"title":"ten","labels":["P1","security"]}}}}`)
	c.handle(key{r: '3'})
	shows := func(wants, not []string) {
		t.Helper()
		got := screen(c)
		for _, w := range wants {
			if !strings.Contains(got, w) {
				t.Fatalf("it should show %q:\n%s", w, got)
			}
		}
		for _, w := range not {
			if strings.Contains(got, w) {
				t.Fatalf("it should not show %q:\n%s", w, got)
			}
		}
	}
	shows([]string{"L labels"}, nil)
	typeText(c, "L")
	shows([]string{"Show those with every label ticked", "☐ P1 (2)", "☐ security (1)", "space tick"}, nil)
	typeText(c, " ")
	shows([]string{"☑ P1 (2)", "☐ security (1)"}, nil)
	press(c, "enter")
	shows([]string{"Pull requests · P1  2", "app one", "app two"}, []string{"Show those with every label"})
	typeText(c, "L")
	shows([]string{"☑ P1 (2)", "☐ security (1)"}, nil)
	typeText(c, "j ")
	press(c, "enter")
	shows([]string{"Pull requests · P1 · security  1", "app one"}, []string{"app two"})
	typeText(c, "L ")
	press(c, "esc")
	shows([]string{"Pull requests · P1 · security  1"}, []string{"app two"}) // esc changes nothing
	// space unticks P1, then j and space untick security
	typeText(c, "L j ")
	press(c, "enter")
	shows([]string{"Pull requests  2", "app one", "app two"}, nil)

	// A label is one, whatever its case: P1 ticked is the p1 of a repository that spells it so.
	write(t, filepath.Join(root, ".open.json"), `{"time":"`+time.Now().Format(time.RFC3339)+`","repos":{`+
		`"o/app":{"2":{"title":"app two","pr":true,"labels":["P1"]}},"o/web":{"3":{"title":"web three","pr":true,"labels":["p1"]}}}}`)
	typeText(c, "L ")
	press(c, "enter")
	typeText(c, "rr")
	shows([]string{"Pull requests · web · P1  1", "web three"}, []string{"app two"})
	typeText(c, "L")
	shows([]string{"☑ p1 (1)"}, []string{"(0)"})
	typeText(c, " ")
	press(c, "enter")
	shows([]string{"Pull requests · web  1", "web three"}, nil)
}

func TestTheConsoleChangesTheTeam(t *testing.T) {
	c, root := newTestConsole(t)
	agentFile := func() string { b, _ := os.ReadFile(filepath.Join(root, "agents", "alpha.md")); return string(b) }
	ap := c.pages[tabAgents].(*agentsPage)
	field := func(k string) {
		for i, f := range core.AgentFields {
			if f.Key == k {
				ap.form.sel = i
				return
			}
		}
		t.Fatalf("no field %s", k)
	}
	// Agents: a choice through the picker, a number through the line editor, back to the default
	c.handle(key{r: '9'})
	for c.t.Agents[ap.sel].Name != "alpha" {
		press(c, "down")
	}
	press(c, "enter")
	field("model")
	press(c, "enter")
	if c.picker == nil {
		t.Fatal("a choice opens a picker")
	}
	for c.picker.labels[c.picker.sel] != "opus" {
		press(c, "down")
	}
	press(c, "enter")
	field("run_minutes")
	press(c, "enter")
	if c.input == nil || string(c.input.text) != "45" {
		t.Fatalf("the editor starts from the value in force: %+v", c.input)
	}
	press(c, "ctrl+u")
	typeText(c, "20")
	press(c, "enter")
	if f := agentFile(); !strings.Contains(f, "model: opus") || !strings.Contains(f, "run_minutes: 20") || !strings.Contains(f, "Alpha fixes **bugs**.") {
		t.Fatalf("settings not written:\n%s", f)
	}
	if !strings.Contains(screen(c), "Timeout") || c.t.Agent("alpha").RunMinutes != 20 {
		t.Fatal("the console rereads what it wrote")
	}
	press(c, "backspace")
	if strings.Contains(agentFile(), "run_minutes") {
		t.Fatal("backspace puts a field back to the team default")
	}
	clickRow := func(label string) { // a second click on a field changes it
		c.frame()
		lines := strings.Split(screen(c), "\n")
		for y, l := range lines {
			if x := strings.Index(l, label); x >= 0 && strings.Contains(l, "  ") {
				c.handle(key{name: "click", x: len([]rune(l[:x])), y: y})
				return
			}
		}
		t.Fatalf("no row %q", label)
	}
	clickRow("Weight")
	clickRow("Weight")
	if c.input == nil || !strings.HasPrefix(c.input.prompt, "Weight") {
		t.Fatal("a second click on a field opens its editor")
	}
	press(c, "esc")
	field("weight")
	press(c, "enter")
	press(c, "ctrl+u")
	typeText(c, "x")
	press(c, "enter")
	if !c.msgBad || !strings.Contains(c.msg, "weight") {
		t.Fatalf("a bad value is refused with a message: %q", c.msg)
	}
	field("enabled")
	press(c, "enter")
	if !strings.Contains(agentFile(), "enabled: false") {
		t.Fatal("a switch toggles")
	}
	typeText(c, "e")
	if !strings.Contains(agentFile(), "enabled: true") {
		t.Fatal("e enables the agent again")
	}

	// Settings: a number with its comment kept, then a switch
	c.handle(key{r: '0'})
	typeText(c, "/tick")
	press(c, "enter", "enter", "ctrl+u")
	typeText(c, "15")
	press(c, "enter")
	typeText(c, "/")
	press(c, "ctrl+u")
	typeText(c, "tray_autostart")
	press(c, "enter", "enter")
	conf, _ := os.ReadFile(filepath.Join(root, "rota.conf"))
	if !strings.Contains(string(conf), "tick_minutes: 15            # how often") || !strings.Contains(string(conf), "tray_autostart_organizer: true") {
		t.Fatalf("rota.conf:\n%s", conf)
	}
	if c.t.TickMinutes != 15 || !c.t.TrayAutostart {
		t.Fatal("the console rereads the settings it wrote")
	}

	// Inbox: a note for the agent's next shift
	c.handle(key{r: '7'})
	inbox := c.pages[tabInbox].(*browser)
	for inbox.ensure(c); inbox.list[inbox.sel].id != "alpha"; {
		press(c, "down")
	}
	typeText(c, "i")
	typeText(c, "look at #12")
	press(c, "enter")
	if b := notesOf(c.t, "alpha"); !strings.Contains(b, "from the human: look at #12") || !strings.Contains(screen(c), "from the human: look at #12") {
		t.Fatalf("inbox: %q\n%s", b, screen(c))
	}
	// with pull requests or issues it works on, first where to, as agents address them
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: true\nweight: 3\nrepos: o/backend\n---\nAlpha.\n")
	write(t, filepath.Join(root, "journal", time.Now().Format("2006-01-02")+".md"), "# x\n\n## 09:00 alpha on local | 1% | 1 turns | 1 min | ok | log x\n\nOpened backend#5.\n")
	write(t, filepath.Join(root, ".open.json"), `{"time":"`+time.Now().Format(time.RFC3339)+`","repos":{"o/backend":{"5":{"title":"five","pr":true}}}}`)
	c.reread()
	typeText(c, "i")
	if s := screen(c); !strings.Contains(s, "@alpha backend#5 · five") || !strings.Contains(s, "@alpha ") {
		t.Fatalf("i offers the agent and what it works on:\n%s", s)
	}
	press(c, "down", "enter")
	if s := screen(c); !strings.Contains(s, "note for @alpha backend#5 (read at its next shift):") {
		t.Fatalf("then asks for the note:\n%s", s)
	}
	typeText(c, "rebase it")
	press(c, "enter")
	if b := notesOf(c.t, "alpha"); !strings.Contains(b, "## o/backend#5\n- "+time.Now().Format("2006-01-02")+" from the human: rebase it") {
		t.Fatalf("filed under the pull request: %q", b)
	}
	// x deletes a note, chosen from them, after asking
	typeText(c, "x")
	if s := screen(c); !strings.Contains(s, "delete a note for alpha") || !strings.Contains(s, "· @alpha backend#5: rebase it") {
		t.Fatalf("x offers the notes:\n%s", s)
	}
	press(c, "down", "enter")
	typeText(c, "y")
	if b := notesOf(c.t, "alpha"); strings.Contains(b, "rebase it") || !strings.Contains(b, "look at #12") {
		t.Fatalf("the chosen note is deleted: %q", b)
	}

	// The team: pause and resume; run now needs the organizer
	typeText(c, "p")
	if !c.t.Paused() {
		t.Fatal("p pauses the team")
	}
	typeText(c, "p")
	if c.t.Paused() {
		t.Fatal("p resumes the team")
	}
	c.handle(key{r: '2'})
	typeText(c, "r")
	if !strings.Contains(c.msg, "organizer is not running") {
		t.Fatalf("got %q", c.msg)
	}

	// Spend: Enter opens the selected shift's transcript
	c.handle(key{r: '8'})
	press(c, "enter")
	if tr := c.pages[tabTranscripts].(*browser); c.tab != tabTranscripts || !strings.Contains(screen(c), "▸ Bash ls") || tr.list[tr.sel].id != c.t.Transcripts("alpha")[0].Path {
		t.Fatalf("Enter on a shift opens its transcript (tab %d):\n%s", c.tab, screen(c))
	}

	// the key sheet, and the mouse on the tabs
	typeText(c, "?")
	if !strings.Contains(screen(c), "Moving around") {
		t.Fatal("? shows the keys")
	}
	typeText(c, "x")
	first := strings.Split(screen(c), "\n")[0]
	x := strings.Index(first, "5 J") // the tab's long or short name, as the width allows
	c.handle(key{name: "click", x: len([]rune(first[:x])), y: 0})
	if c.tab != tabJournal {
		t.Fatalf("a click on a tab opens it (tab %d)", c.tab)
	}
	typeText(c, "q")
	if !c.quit {
		t.Fatal("q quits")
	}
}

func TestKeysAreDecoded(t *testing.T) {
	in := "\x1b[A\x1b[B\x1bOCq\r\x7f\x1b[5~\x1b[Z\x1b[<64;10;5M\x1b[<0;3;1M\x1b[<0;3;1m\x1b[200~hi\nthere\x1b[201~é\x03\x1bx"
	ks, rest := parseKeys([]byte(in), false)
	var got []string
	for _, k := range ks {
		switch {
		case k.name == "paste":
			got = append(got, "paste:"+k.paste)
		case k.name == "click" || strings.HasPrefix(k.name, "wheel"):
			got = append(got, fmt.Sprintf("%s:%d,%d", k.name, k.x, k.y))
		case k.name != "":
			got = append(got, k.name)
		default:
			got = append(got, string(k.r))
		}
	}
	if want := "up down right q enter backspace pgup backtab wheelup:9,4 click:2,0 paste:hi\nthere é ctrl+c x"; strings.Join(got, " ") != want || len(rest) != 0 {
		t.Fatalf("got  %q\nwant %q", strings.Join(got, " "), want)
	}
	if ks, rest := parseKeys([]byte("\x1b"), false); len(ks) != 0 || len(rest) != 1 {
		t.Fatal("a lone escape waits for what follows")
	}
	if ks, _ := parseKeys([]byte("\x1b"), true); len(ks) != 1 || ks[0].name != "esc" {
		t.Fatal("a lone escape with nothing after it is Esc")
	}
	if ks, rest := parseKeys([]byte{0xc3}, false); len(ks) != 0 || len(rest) != 1 {
		t.Fatal("half a UTF-8 rune waits for the rest")
	}
	// the terminal's answers never become keystrokes: 1 would switch tabs, r would run an agent
	ks, rest = parseKeys([]byte("\x1b]11;rgb:1e1e/1e1e/1e1e\x1b\\\x1b[?62;22c\x1b]10;rgb:ffff/ffff/ffff\x07q"), false)
	if len(ks) != 2 || ks[0].name != "background" || ks[1].r != 'q' || len(rest) != 0 {
		t.Fatalf("answers are swallowed: %+v %q", ks, rest)
	}
}

func TestTextLayout(t *testing.T) {
	p := paletteNamed("dark")
	f := newFrame(10, 2)
	f.put(0, 0, "a✅b"+sty(p.Bad(), "cdefghij"), -1)
	f.put(1, 1, sty(p.OK(), "ok")+" "+sty(p.Muted(), "plain"), -1)
	f.band(p, 0, 1, 10, true)
	lines := f.lines()
	if got := plain(lines[0]); got != "a✅bcdefgh" {
		t.Fatalf("a wide rune and clipping: %q", got)
	}
	if got := plain(lines[1]); !strings.HasPrefix(got, "▌ok plain") || !strings.Contains(lines[1], p.OK()+";48;5;236") || !strings.Contains(lines[1], fg(p.selMuted)) {
		t.Fatalf("on the band, colors keep their meaning and muted text is lifted: %q", lines[1])
	}
	if got := plain(truncate("hello world", 6)); got != "hello…" {
		t.Fatalf("truncate: %q", got)
	}
	if got := wrap([]span{{"one two three four", ""}}, 9, "• ", "  "); strings.Join(got, "|") != "• one two|  three|  four" {
		t.Fatalf("wrap: %q", got)
	}
	if got := wrap([]span{{"abcdefghijkl", ""}}, 5, "", ""); strings.Join(got, "|") != "abcde|fghij|kl" {
		t.Fatalf("a long word breaks at the edge: %q", got)
	}
}

func TestMarkdownAndTranscriptsForTheTerminal(t *testing.T) {
	p := paletteNamed("dark")
	md := "# 2026-10-29\n\n## 05:00 errors on local | 0.0% of weekly window | 9 turns | 0 min | ok | log logs/x.jsonl\n\n" +
		"> Inbox read this shift:\n> - note one\n\n## Summary\nSome **bold** and `code` text that wraps around the line.\n\n" +
		"- item one\n  continued\n- item two\n  1. nested\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n_Attempt 1 of 3._\n\n## 06:00 qa on local | 1 turns | error:error_max_turns | log y\n"
	lines, marks := p.markdown(md, 40)
	text := plain(strings.Join(lines, "\n"))
	for _, want := range []string{"▌ 05:00 errors on local · 0.0% of weekly", "│ Inbox read this shift:", "│ • note one", "• item one continued", "  1. nested", "a │ b", "──┼──", "Attempt 1 of 3.", "▌ 06:00 qa on local"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "log logs") || strings.Contains(text, "**") || strings.Contains(text, "`") || len(marks) != 2 {
		t.Errorf("markup leaked, or the entries are not the sections (%v):\n%s", marks, text)
	}
	for _, l := range lines {
		if width(l) > 40 {
			t.Errorf("wider than 40: %q", plain(l))
		}
	}
	var events []core.Event
	for _, l := range []string{`{"type":"system","subtype":"init","model":"claude-opus-5-5","tools":["Bash","Read"]}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Looking at **the logs**."},{"type":"tool_use","name":"Bash","input":{"command":"ls -la\nmore"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","content":"file1\nfile2"}]}}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"## Summary\nDone.","num_turns":3}`} {
		events = append(events, core.ParseLine(l)...)
	}
	lines, marks = p.transcript(events, 60, false)
	text = plain(strings.Join(lines, "\n"))
	for _, want := range []string{"claude started · claude-opus-5-5 · 2 tools", "Looking at the logs.", "▸ Bash ls -la …", "↳ file1  (2 lines)", "━━ finished · 3 turns", "Done."} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if len(marks) != 2 {
		t.Errorf("marks: %v", marks)
	}
	if lines, _ = p.transcript(events, 60, true); !strings.Contains(plain(strings.Join(lines, "\n")), "│ file1\n  │ file2") {
		t.Error("x shows the tool output")
	}
}

func xtermRGB(n int) (float64, float64, float64) {
	base := [16][3]int{{0, 0, 0}, {205, 0, 0}, {0, 205, 0}, {205, 205, 0}, {0, 0, 238}, {205, 0, 205}, {0, 205, 205}, {229, 229, 229},
		{127, 127, 127}, {255, 0, 0}, {0, 255, 0}, {255, 255, 0}, {92, 92, 255}, {255, 0, 255}, {0, 255, 255}, {255, 255, 255}}
	var c [3]int
	switch {
	case n < 16:
		c = base[n]
	case n < 232:
		step := func(v int) int {
			if v == 0 {
				return 0
			}
			return 55 + 40*v
		}
		n -= 16
		c = [3]int{step(n / 36), step(n / 6 % 6), step(n % 6)}
	default:
		v := 8 + 10*(n-232)
		c = [3]int{v, v, v}
	}
	return float64(c[0]) / 255, float64(c[1]) / 255, float64(c[2]) / 255
}

// Every palette reads: each text color at least 4.5:1 (WCAG AA) against the usual
// backgrounds of its kind of terminal, and on its own selection band.
func TestPalettesMeetWCAGContrast(t *testing.T) {
	lum := func(n int) float64 { return luminance(xtermRGB(n)) }
	hexLum := func(h string) float64 {
		var r, g, b int
		fmt.Sscanf(h, "#%02x%02x%02x", &r, &g, &b)
		return luminance(float64(r)/255, float64(g)/255, float64(b)/255)
	}
	ratio := func(a, b float64) float64 { return (max(a, b) + 0.05) / (min(a, b) + 0.05) }
	for name, bgs := range map[string][]string{"dark": {"#000000", "#1e1e1e", "#282c34", "#2e3440"}, "light": {"#ffffff", "#fafafa", "#fdf6e3", "#eeeeee"}} {
		p := paletteNamed(name)
		text := map[string]int{"muted": p.muted, "accent": p.accent, "ok": p.ok, "warn": p.warn, "bad": p.bad, "code": p.code}
		for what, c := range text {
			for _, bg := range bgs {
				if r := ratio(lum(c), hexLum(bg)); r < 4.5 {
					t.Errorf("%s: %s (%d) on %s is %.2f:1", name, what, c, bg, r)
				}
			}
		}
		text["selected text"], text["muted on the band"] = p.selFg, p.selMuted
		delete(text, "muted")
		for what, c := range text {
			if r := ratio(lum(c), lum(p.selBg)); r < 4.5 {
				t.Errorf("%s: %s (%d) on the band (%d) is %.2f:1", name, what, c, p.selBg, r)
			}
		}
	}
}

func TestTheThemeIsChosenInTheDocumentedOrder(t *testing.T) {
	root := t.TempDir()
	for _, v := range []string{"ROTA_THEME", "NO_COLOR", "COLORFGBG"} {
		t.Setenv(v, "")
	}
	pick := func(bg float64, have bool) string { p, _ := resolveTheme(root, bg, have); return p.name }
	if pick(0.9, true) != "light" || pick(0.01, true) != "dark" || pick(0, false) != "dark" {
		t.Fatal("auto follows the terminal's background, else dark")
	}
	t.Setenv("COLORFGBG", "0;15")
	if pick(0, false) != "light" {
		t.Fatal("COLORFGBG with a light background")
	}
	t.Setenv("NO_COLOR", "1")
	if pick(0.9, true) != "mono" {
		t.Fatal("NO_COLOR asks for no color")
	}
	saveTheme(root, "light")
	if pick(0.01, true) != "light" {
		t.Fatal("a choice made in the console beats NO_COLOR and the terminal")
	}
	t.Setenv("ROTA_THEME", "mono")
	if pick(0.9, true) != "mono" {
		t.Fatal("ROTA_THEME beats everything")
	}
	saveTheme(root, "auto")
	if _, err := os.Stat(themeFile(root)); !os.IsNotExist(err) {
		t.Fatal("auto is the absence of a choice")
	}
	for reply, want := range map[string]float64{"\x1b]11;rgb:ffff/ffff/ffff\x07": 1, "\x1b]11;rgb:00/00/00\x1b\\": 0, "\x1b]11;rgb:1e1e/1e1e/1e1e\x1b\\": 0.0123} {
		if got, ok := parseBackground([]byte(reply)); !ok || got < want-0.001 || got > want+0.001 {
			t.Errorf("%q: %v %v", reply, got, ok)
		}
	}
}

func TestMonoUsesNoColorAndMarksTheSelection(t *testing.T) {
	c, _ := newTestConsole(t)
	c.pal = paletteNamed("mono")
	for tab := range c.pages {
		c.tab = tab
		reverse := false
		for _, l := range c.frame().lines() {
			if strings.Contains(l, "38;5;") || strings.Contains(l, "48;5;") {
				t.Fatalf("tab %d uses color in mono: %q", tab, l)
			}
			reverse = reverse || strings.Contains(l, ";7m") || strings.Contains(l, "[7m")
		}
		if tab == tabStatus && !reverse {
			t.Fatal("the selection shows in reverse video")
		}
	}
}

func TestTheConsoleOnAHostAndWithManyAgents(t *testing.T) {
	c, root := newTestConsole(t)
	write(t, filepath.Join(root, "agents", "far.md"), "---\nenabled: true\nhost: linux1\n---\nfar\n")
	for i := 0; i < 40; i++ {
		write(t, filepath.Join(root, "agents", fmt.Sprintf("z%02d.md", i)), "---\npriority: 200\n---\nz\n")
	}
	c.reread()
	c.handle(key{r: '9'})
	press(c, "end")
	if !strings.Contains(screen(c), "▌ ○ z39") {
		t.Fatalf("the selected agent stays in view:\n%s", screen(c))
	}
}

// While a shift runs, the status tab shows its latest steps before the table and its agent first;
// the band stays on the agent chosen when the list reorders, and keys act on that one.
func TestTheConsoleStatusLeadsWithTheRunningShift(t *testing.T) {
	c, root := newTestConsole(t)
	c.handle(key{r: '2'})
	first, second := c.st.Agents[0].Name, c.st.Agents[1].Name
	press(c, "down") // the second agent, by name
	if got := screen(c); strings.Contains(got, "Live") || !strings.Contains(got, "Recent shifts") {
		t.Fatalf("with none running, the latest shifts:\n%s", got)
	}
	b, _ := json.Marshal(core.Status{Time: time.Now(), Pid: os.Getpid(), StartedAt: time.Now(), Running: &core.Running{Agent: second, Host: "local", Since: time.Now().Add(-2 * time.Minute), Live: "Bash ls"}})
	write(t, filepath.Join(root, ".state.json"), string(b))
	c.apply(loaded{t: c.t, st: c.t.Status(time.Now())})
	got := screen(c)
	live, agents := strings.Index(got, "Live"), strings.Index(got, "Agents")
	if live < 0 || agents < live || strings.Contains(got, "Recent shifts") {
		t.Fatalf("the running shift before the table, the latest shifts not at all:\n%s", got)
	}
	rows := strings.Split(got[agents:], "\n")
	if !strings.Contains(rows[2], "▸ "+second) || !strings.Contains(rows[2], "▸ running 2 min") || !strings.Contains(rows[3], first) {
		t.Fatalf("its agent first, marked, then the others:\n%s", strings.Join(rows[:4], "\n"))
	}
	press(c, "enter") // opens the agent the band is on: the one chosen, now first
	if a := c.pages[tabAgents].(*agentsPage).agent(c); c.tab != tabAgents || a == nil || a.Name != second {
		t.Fatalf("the key acts on %s, the agent chosen before the list reordered: %+v", second, a)
	}
}

// While the organizer is on hold, every tab says so, and the status says why and what to do.
func TestTheConsoleSaysTheTeamIsOnHold(t *testing.T) {
	c, _ := newTestConsole(t)
	st := c.t.Status(time.Now())
	st.Pid, st.StartedAt, st.UsageError = os.Getpid(), time.Now(), "usage endpoint: HTTP 401: token expired"
	c.apply(loaded{t: c.t, st: st})
	c.handle(key{r: '2'})
	got := screen(c)
	for _, want := range []string{"▲ on hold: no shift starts until Claude's usage can be read; wait for it, or set usage to off", "▲ usage cannot be read: usage endpoint: HTTP 401"} {
		if !strings.Contains(got, want) {
			t.Errorf("the status should show %q:\n%s", want, got)
		}
	}
	c.handle(key{r: '3'})
	if got := screen(c); !strings.Contains(strings.SplitN(got, "\n", 2)[0], "▲ on hold") {
		t.Errorf("every tab shows the hold:\n%s", got)
	}
}

// A new question rings the bell and says who asks; every tab shows how many are open, the
// status page shows them, and a answers one into the agent's inbox.
func TestTheConsoleShowsAndAnswersQuestions(t *testing.T) {
	c, root := newTestConsole(t)
	rang := 0
	c.bell = func() { rang++ }
	write(t, filepath.Join(root, "questions", "20261029-1700-alpha-ab12.json"), `{"agent":"alpha","asked":"2026-10-29T17:00:00Z","text":"Is production on gpt-5?"}`)
	c.apply(loaded{t: c.t, st: c.st, questions: c.t.Questions()})
	c.w = 160
	if top := strings.SplitN(screen(c), "\n", 2)[0]; !strings.Contains(top, "1 Reports") || !strings.Contains(top, "2 Status") || !strings.Contains(top, "? 1") {
		t.Errorf("every tab shows the open questions, and the tabs keep their names:\n%s", top)
	}
	c.w = 200
	if top := strings.SplitN(screen(c), "\n", 2)[0]; !strings.Contains(top, "? 1 question  ○ organizer off") {
		t.Errorf("with room, the count says what it counts:\n%s", top)
	}
	c.w = 120
	c.handle(key{r: '2'})
	got := screen(c)
	for _, want := range []string{"Questions for you", "? alpha", "Is production on gpt-5?", "a answer"} {
		if !strings.Contains(got, want) {
			t.Errorf("the console should show %q:\n%s", want, got)
		}
	}
	if rang != 1 || !strings.Contains(c.msg, "alpha asks you a question") {
		t.Errorf("a new question rings the bell and says so: %d %q", rang, c.msg)
	}
	c.handle(key{r: '3'})
	c.handle(key{r: 'a'})
	if c.input == nil || c.tab != tabStatus {
		t.Fatal("a opens the answer on the status page")
	}
	typeText(c, "no, luna only")
	press(c, "enter")
	if b := notesOf(c.t, "alpha"); !strings.Contains(b, `answering your question "Is production on gpt-5?": no, luna only`) {
		t.Fatalf("the answer is in alpha's inbox:\n%s", b)
	}
	if len(c.questions) != 0 || len(c.t.Questions()) != 0 || strings.Contains(screen(c), "Questions for you") {
		t.Fatal("the answered question is gone")
	}
	write(t, filepath.Join(root, "questions", "20261029-1800-alpha-cd34.json"), `{"agent":"alpha","asked":"2026-10-29T18:00:00Z","text":"Keep the old banner?"}`)
	c.apply(loaded{t: c.t, st: c.st, questions: c.t.Questions()})
	c.handle(key{r: 'D'})
	if !strings.Contains(screen(c), "Dismiss alpha's question without an answer?") {
		t.Fatalf("D asks before dismissing:\n%s", screen(c))
	}
	c.handle(key{r: 'y'})
	if len(c.questions) != 0 || len(c.t.Questions()) != 0 || !strings.Contains(notesOf(c.t, "alpha"), `dismissed your question "Keep the old banner?"`) {
		t.Fatal("the dismissed question is gone, and alpha's inbox says so")
	}
}

// B on the Spend tab, or on the Agents tab, opens a proposal of new shares on Spend; Enter
// changes a weight, A applies them after a yes, and Esc drops the proposal without changing
// anything. Elsewhere B does nothing.
func TestTheConsoleRebalancesTheShares(t *testing.T) {
	c, root := newTestConsole(t)
	write(t, filepath.Join(root, "agents", "gamma.md"), "---\nenabled: true\nweight: 1\n---\nGamma.\n")
	c.reread()
	c.tab = tabStatus
	typeText(c, "B")
	if c.tab != tabStatus || strings.Contains(screen(c), "Rebalance the shares") {
		t.Fatal("B is no key on Status")
	}
	c.tab = tabSpend
	if !strings.Contains(screen(c), "B proposes new shares") {
		t.Fatalf("Spend says B proposes new shares:\n%s", screen(c))
	}
	typeText(c, "B")
	s := screen(c)
	for _, want := range []string{"Rebalance the shares", "alpha", "gamma", "too few to judge", "75.0% of the pool"} {
		if c.tab != tabSpend || !strings.Contains(s, want) {
			t.Fatalf("the proposal should show %q:\n%s", want, s)
		}
	}
	press(c, "esc")
	if strings.Contains(screen(c), "Rebalance the shares") {
		t.Fatal("Esc drops the proposal")
	}
	c.tab = tabAgents
	typeText(c, "B")
	if c.tab != tabSpend || !strings.Contains(screen(c), "Rebalance the shares") {
		t.Fatal("B on the Agents tab opens the proposal on Spend")
	}
	press(c, "down", "enter", "ctrl+u")
	typeText(c, "3")
	press(c, "enter")
	if !strings.Contains(screen(c), "→   3.8% of the pool") {
		t.Fatalf("a changed weight changes the shares at once:\n%s", screen(c))
	}
	typeText(c, "An")
	if tm, _ := core.Open(root, "rota.conf"); tm.Agent("gamma").Weight != 1 {
		t.Fatal("no applies nothing")
	}
	typeText(c, "Ay")
	if tm, _ := core.Open(root, "rota.conf"); tm.Agent("alpha").Weight != 75 || tm.Agent("gamma").Weight != 3 || !strings.Contains(c.msg, "weights applied") {
		t.Fatalf("yes applies the weights: alpha %v gamma %v, %q", tm.Agent("alpha").Weight, tm.Agent("gamma").Weight, c.msg)
	}
}

// The Issues tab lists the open issues as planned, those the plan leaves out newest first after
// them; o changes the order, r, m and L narrow it, what is chosen is kept for the pages, and ⏎
// opens the issue on GitHub.
func TestTheConsoleListsTheIssues(t *testing.T) {
	c, root := newTestConsole(t)
	write(t, filepath.Join(root, ".open.json"), `{"time":"`+time.Now().Format(time.RFC3339)+`","repos":{`+
		`"o/app":{"1":{"title":"the oldest","labels":["P1"],"created":"2026-11-01T09:00:00Z"},"2":{"title":"the newest","created":"2026-11-05T09:00:00Z"},`+
		`"3":{"title":"a fix","pr":true,"issues":["o/app#1"]}}}}`)
	write(t, filepath.Join(root, ".plan.json"), `{"by":"planner","time":"`+time.Now().Format(time.RFC3339)+`","issues":[{"ref":"o/app#1","why":"users lose their work"}]}`)
	var opened []string
	startBrowser = func(url string) error { opened = append(opened, url); return nil }
	c.handle(key{r: '4'})
	got := screen(c)
	if !strings.Contains(got, "Issues  2") || strings.Contains(got, "a fix") || strings.Index(got, "the oldest") > strings.Index(got, "the newest") || !strings.Contains(got, "1. 🔧") ||
		!strings.Contains(got, "No. 1 in the plan: users lose their work") {
		t.Fatalf("the open issues as planned, with its place and why, then the rest:\n%s", got)
	}
	typeText(c, "o")
	if got = screen(c); !strings.Contains(got, "Issues · newest first") || strings.Index(got, "the newest") > strings.Index(got, "the oldest") {
		t.Fatalf("o: newest first:\n%s", got)
	}
	typeText(c, "o")
	got = screen(c)
	if !strings.Contains(got, "Issues · oldest first") || strings.Index(got, "the oldest") > strings.Index(got, "the newest") {
		t.Fatalf("o: oldest first:\n%s", got)
	}
	if f := c.t.ListFilter(core.IssuesTab); f.Order != core.OrderOldest {
		t.Fatalf("the order is not kept for the pages: %+v", f)
	}
	typeText(c, "m")
	if got = screen(c); !strings.Contains(got, "🔧") || strings.Contains(got, "the newest") {
		t.Fatalf("m narrows to the issues with a pull request:\n%s", got)
	}
	c.handle(key{name: "enter"})
	if len(opened) != 1 || opened[0] != "https://github.com/o/app/issues/1" {
		t.Fatalf("⏎ opens the issue on GitHub: %q", opened)
	}
	typeText(c, "m")
	typeText(c, "r")
	if got = screen(c); !strings.Contains(got, "Issues · oldest first · app") || !strings.Contains(got, "the newest") {
		t.Fatalf("r narrows to a repository:\n%s", got)
	}
	typeText(c, "r")
	if err := c.t.SetListFilter(core.IssuesTab, core.ListFilter{Labels: []string{"P1"}, Order: core.OrderPriority}); err != nil { // as the pages choose
		t.Fatal(err)
	}
	c.pages[tabIssues].(*browser).loaded = time.Time{}
	if got = screen(c); !strings.Contains(got, "Issues · by priority · P1") || strings.Contains(got, "the newest") {
		t.Fatalf("a choice made in the pages narrows the console:\n%s", got)
	}
}

//go:build darwin

// Package tray is rota in the macOS menu bar: the organizer's state at a glance, every agent
// with its settings and controls, and the team's controls. It reads the organizer's snapshot
// and acts through the core, like every other front end.
package tray

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"fyne.io/systray"
	"golang.org/x/sys/unix"

	"rota/core"
	"rota/icon"
)

// active is the running menu, for the notifications' answers.
var active *tray

type tray struct {
	mu         sync.Mutex // menu actions and refreshes, one at a time
	ctx        context.Context
	root, conf string
	exe        string
	t          *core.Team
	problem    string           // why the team folder could not be read, shown in the menu
	organizer  *os.Process      // an organizer the tray started, or the menu it replaced: stopped when it quits
	self       *core.SelfUpdate // another rota installed in this one's place
	web        *exec.Cmd        // an rota web the tray started: stopped when it quits
	names      string           // the agents the menu was built for
	agents     map[string]*agentMenu
	header     *systray.MenuItem
	claude     *systray.MenuItem
	hosts      *systray.MenuItem
	pause      *systray.MenuItem
	cancel     *systray.MenuItem
	kick       *systray.MenuItem
	startStop  *systray.MenuItem
	questions  []core.Question      // open, as last read
	qids       string               // the questions the menu was built for
	notified   map[string]bool      // questions with a notification up
	answered   map[string]time.Time // agents' answers with a notification up, by its id: answerPrefix and the review's page
	off        bool                 // macOS has rota's notifications turned off
}

type agentMenu struct {
	item, enabled   *systray.MenuItem
	paused          *systray.MenuItem
	models, efforts map[string]*systray.MenuItem
}

// Run puts rota in the menu bar until it is quit or ctx ends. exe is this program, which
// the menu runs for the organizer and the pages; inBundle says it runs as rota.app.
func Run(ctx context.Context, root, conf, exe string, inBundle bool) (err error) {
	tr := &tray{ctx: ctx, root: root, conf: conf, exe: exe, notified: map[string]bool{}, answered: map[string]time.Time{},
		self: core.WatchSelfUpdate()}
	active = tr
	if pid, err := strconv.Atoi(os.Getenv(organizerEnv)); err == nil { // the menu this one replaced started it
		os.Unsetenv(organizerEnv)
		if p, err := os.FindProcess(pid); err == nil && p.Signal(syscall.Signal(0)) == nil {
			tr.organizer = p
			go p.Wait() // still this process's child: reaped when it ends
		}
	}
	if abs, err := filepath.Abs(root); err == nil {
		tr.root = abs
	}
	if tr.t, err = core.Open(root, conf); err != nil { // a new machine: the menu still runs, and says what to do
		tr.t, tr.problem = &core.Team{Config: core.DefaultConfig(tr.root)}, err.Error()
	}
	systray.Run(func() { tr.ready(inBundle) }, func() {})
	return nil
}

func (tr *tray) ready(inBundle bool) {
	glyph := icon.PNG(32, "template") // the menu bar shows it at 16 points: 32 pixels on a Retina screen
	systray.SetTemplateIcon(glyph, glyph)
	if inBundle {
		InstallCommand(tr.exe) // the app is the install: the terminal command is the app
		setupNotifications()
	}
	tr.mu.Lock()
	tr.build()
	if tr.t.TrayAutostart && tr.t.Live() == nil {
		tr.toggleOrganizer()
	}
	tr.mu.Unlock()
	go func() {
		for {
			tr.mu.Lock()
			tr.update()
			tr.refresh()
			tr.mu.Unlock()
			select {
			case <-tr.ctx.Done():
				tr.mu.Lock()
				tr.quit()
				tr.mu.Unlock()
				return
			case <-time.After(2 * time.Second):
			}
		}
	}()
}

func options(key string) []string {
	for _, f := range core.AgentFields {
		if f.Key == key {
			return append([]string{""}, f.Options()...)
		}
	}
	return nil
}

// build lays the menu out for the current agents.
func (tr *tray) build() {
	systray.ResetMenu()
	tr.header = systray.AddMenuItem("rota", "")
	tr.header.Disable()
	tr.claude = systray.AddMenuItem("claude", "")
	tr.claude.Disable()
	systray.AddSeparator()
	if len(tr.questions) > 0 {
		for _, q := range tr.questions {
			q := q
			tr.handle(systray.AddMenuItem("? "+q.Agent+" asks: "+core.FirstLine(q.Text, 70), q.Text), func() { tr.answer(q) })
		}
		if tr.off {
			off := systray.AddMenuItem("Notifications are off: allow rota in System Settings, Notifications", "")
			off.Disable()
		}
		systray.AddSeparator()
	}
	tr.agents, tr.names = map[string]*agentMenu{}, ""
	for _, ag := range tr.t.Agents {
		name := ag.Name
		tr.names += name + ","
		m := &agentMenu{item: systray.AddMenuItem(name, ""), models: map[string]*systray.MenuItem{}, efforts: map[string]*systray.MenuItem{}}
		m.enabled = m.item.AddSubMenuItemCheckbox("Enabled", "", ag.Enabled)
		tr.handle(m.enabled, func() {
			if a := tr.t.Agent(name); a != nil { // it may have been removed since the menu was built
				tr.set(name, "enabled", fmt.Sprint(!a.Enabled))
			}
		})
		m.paused = m.item.AddSubMenuItemCheckbox("Paused", "its running shift finishes, no new one starts", tr.t.PausedAgents()[name])
		tr.handle(m.paused, func() {
			if tr.t.PausedAgents()[name] {
				tr.t.ResumeAgent(name)
			} else {
				tr.t.PauseAgent(name)
			}
		})
		for _, kind := range []struct {
			key, title string
			items      map[string]*systray.MenuItem
		}{{"model", "Model", m.models}, {"effort", "Effort", m.efforts}} {
			sub := m.item.AddSubMenuItem(kind.title, "")
			for _, v := range options(kind.key) {
				if kind.items[v] != nil {
					continue
				}
				key, v := kind.key, v
				kind.items[v] = sub.AddSubMenuItemCheckbox(or(v, "default"), "", false)
				tr.handle(kind.items[v], func() { tr.set(name, key, v) })
			}
		}
		tr.handle(m.item.AddSubMenuItem("Run now", "ahead of every agent you did not ask for: its interval and hours skipped; the team pool still applies"), func() { tr.t.RunNow(name) })
		tr.handle(m.item.AddSubMenuItem("Latest run", "its report and transcript"), func() {
			if trs := tr.t.Transcripts(name); len(trs) > 0 {
				tr.openPage("/#run:" + filepath.Base(filepath.Dir(trs[0].Path)) + "/" + strings.TrimSuffix(filepath.Base(trs[0].Path), ".jsonl"))
			} else {
				tr.openPage("/#agent:" + name)
			}
		})
		tr.handle(m.item.AddSubMenuItem("Leave a note…", "to it, or about a pull request or issue it works on; its running shift gets it after its current step"), func() { tr.note(name) })
		tr.handle(m.item.AddSubMenuItem("Open work folder", ""), func() { open(tr.t.Path(ag.WorkDir)) })
		tr.agents[name] = m
	}
	systray.AddSeparator()
	tr.hosts = systray.AddMenuItem("hosts: none", "")
	tr.hosts.Disable()
	systray.AddSeparator()
	tr.pause = systray.AddMenuItem("Pause team", "the running shift finishes; no new shift starts")
	tr.cancel = systray.AddMenuItem("Stop current shift", "")
	tr.kick = systray.AddMenuItem("Run next shift now", "")
	tr.handle(tr.pause, func() {
		if tr.t.Paused() {
			tr.t.Unpause()
		} else {
			tr.t.Pause()
		}
	})
	tr.handle(tr.cancel, func() { tr.t.CancelShift("") })
	tr.handle(tr.kick, func() { tr.t.RunNow("") })
	systray.AddSeparator()
	tr.startStop = systray.AddMenuItem("Start organizer", "")
	tr.handle(tr.startStop, tr.toggleOrganizer)
	tr.handle(systray.AddMenuItem("Inbox", "what waits for you, and what the team did"), func() { tr.openPage("/") })
	tr.handle(systray.AddMenuItem("Team", "the organizer, the agents and their runs"), func() { tr.openPage("/#team") })
	tr.handle(systray.AddMenuItem("Today's report", ""), func() { tr.openPage("/reports/") })
	tr.handle(systray.AddMenuItem("Open team folder", ""), func() { open(tr.root) })
	tr.handle(systray.AddMenuItem("Install terminal command", "links rota and gloss in ~/.local/bin to this app"), func() {
		if link, err := InstallCommand(tr.exe); err == nil {
			tr.header.SetTitle("terminal command installed: " + link)
		} else {
			tr.header.SetTitle(err.Error())
		}
	})
	systray.AddSeparator()
	tr.handle(systray.AddMenuItem("Quit", "also stops an organizer the menu started"), tr.quit)
}

func (tr *tray) handle(item *systray.MenuItem, f func()) {
	go func() {
		for range item.ClickedCh {
			tr.mu.Lock()
			f()
			tr.refresh()
			tr.mu.Unlock()
		}
	}()
}

// refresh repaints from the team folder and the organizer's snapshot; a changed set of
// agents rebuilds the menu.
func (tr *tray) refresh() {
	if t, err := core.Open(tr.root, tr.conf); err == nil {
		tr.t, tr.problem = t, ""
	} else {
		tr.problem = err.Error()
	}
	names := ""
	for _, a := range tr.t.Agents {
		names += a.Name + ","
	}
	tr.questions = tr.t.Questions()
	ids, open := "", map[string]bool{}
	for _, q := range tr.questions {
		ids, open[q.ID] = ids+q.ID+",", true
		if !tr.notified[q.ID] {
			tr.notified[q.ID] = notifyQuestion(q)
		}
	}
	for id := range tr.notified {
		if !open[id] { // answered, here or elsewhere
			withdraw(id)
			delete(tr.notified, id)
		}
	}
	unseen, answers := map[string]bool{}, tr.t.Answers()
	for _, a := range answers { // a newer answer on the same pull request takes the last one's place
		id := answerPrefix + a.Page()
		unseen[id] = true
		if !tr.answered[id].Equal(a.At) && notifyAnswer(id, a) {
			tr.answered[id] = a.At
		}
	}
	for id := range tr.answered {
		if !unseen[id] { // its review was opened
			withdraw(id)
			delete(tr.answered, id)
		}
	}
	off := len(tr.questions)+len(answers) > 0 && notificationsOff() // also checks again whether they are allowed
	if names != tr.names || ids != tr.qids || off != tr.off {
		tr.qids, tr.off = ids, off
		tr.build()
	}
	asks := ""
	if n := len(tr.questions); n > 0 {
		asks = fmt.Sprintf(" ?%d", n)
	}
	setTitle := func(s string) { systray.SetTitle(asks + s) }
	live := tr.t.Live()
	if live != nil && !live.Stale {
		tr.stopWeb() // the organizer serves the pages itself
	}
	if live == nil {
		setTitle(" off")
		tr.header.SetTitle(or(tr.problem, "Organizer not running"))
		tr.claude.SetTitle("claude: unknown")
		tr.startStop.SetTitle("Start organizer")
		tr.pause.Disable()
		tr.cancel.Disable()
		tr.kick.Disable()
	} else {
		tr.startStop.SetTitle("Stop organizer")
		tr.pause.Enable()
		tr.kick.Enable()
		tr.pause.SetTitle(map[bool]string{true: "Resume team", false: "Pause team"}[live.Paused])
		header := "Organizer " + live.Organizer() + " · " + fmt.Sprintf("pool %.0f%% used %s", live.Pool.Of(live.Pool.Used), live.Pool.Period)
		if !live.Pool.Enforced {
			header = "Organizer " + live.Organizer() + " · pool not enforced without usage data"
		}
		switch {
		case live.Running != nil:
			setTitle(fmt.Sprintf(" %s %dm", live.Running.Agent, int(time.Since(live.Running.Since).Minutes())))
			tr.cancel.Enable()
		case live.Held() != "":
			setTitle(" ▲ on hold")
			header = "▲ On hold: " + live.Held()
			tr.cancel.Disable()
		case live.Paused:
			setTitle(" paused")
			tr.cancel.Disable()
		default:
			setTitle(fmt.Sprintf(" idle %.0f%%", live.Pool.Of(live.Pool.Used)))
			tr.cancel.Disable()
		}
		tr.header.SetTitle(header)
		switch {
		case live.Usage != nil:
			tr.claude.SetTitle("claude: " + live.Usage.String())
		case live.UsageError != "":
			tr.claude.SetTitle("claude: usage cannot be read: " + core.FirstLine(live.UsageError, 80))
		default:
			tr.claude.SetTitle("claude: usage data off")
		}
		var hosts []string
		for _, h := range live.Hosts {
			hosts = append(hosts, h.String())
		}
		tr.hosts.SetTitle("hosts: " + or(strings.Join(hosts, " · "), "none"))
	}
	for _, ag := range tr.t.Agents {
		m := tr.agents[ag.Name]
		if m == nil {
			continue
		}
		check(m.enabled, ag.Enabled)
		check(m.paused, tr.t.PausedAgents()[ag.Name])
		for v, item := range m.models {
			check(item, v == ag.Model)
		}
		for v, item := range m.efforts {
			check(item, v == ag.Effort)
		}
		title := ag.Name
		for _, as := range liveAgents(live) {
			if as.Name != ag.Name {
				continue
			}
			title = fmt.Sprintf("%s    %s · next %s", ag.Name, as.State, as.NextText(live.NextTick))
			if as.QuotaPct > 0 {
				title += fmt.Sprintf(" · %.1f/%.1f%% of the pool", live.Pool.Of(as.PeriodPct), live.Pool.Of(as.QuotaPct))
			}
			if r := live.Running; r != nil && r.Agent == ag.Name {
				title = fmt.Sprintf("%s    running %dm on %s · %s", ag.Name, int(time.Since(r.Since).Minutes()), r.Host, core.FirstLine(r.Live, 60))
			}
		}
		m.item.SetTitle(title)
	}
}

// answer asks for the answer to q in a dialog and sends it to the agent that asked, or
// dismisses q without an answer.
func (tr *tray) answer(q core.Question) {
	go func() {
		out, err := exec.Command("osascript", "-e", "on run argv", "-e", "activate", "-e",
			`set r to display dialog (item 1 of argv) with title (item 2 of argv) default answer "" buttons {"Cancel", "Dismiss", "Answer"} default button "Answer" cancel button "Cancel"`,
			"-e", "return (button returned of r) & linefeed & (text returned of r)", "-e", "end run", q.Text, q.Agent+" asks you").Output()
		if err != nil {
			return // cancelled
		}
		pressed, text, _ := strings.Cut(string(out), "\n")
		tr.mu.Lock()
		defer tr.mu.Unlock()
		if pressed == "Dismiss" {
			tr.t.Dismiss(q.ID)
		} else {
			tr.t.Answer(q.ID, text)
		}
		tr.refresh()
	}()
}

// note asks where a note to the agent goes, when it works on pull requests or issues, each as
// agents address it, then for the note, in dialogs, and leaves it.
func (tr *tray) note(agent string) {
	go func() {
		tr.mu.Lock()
		addrs := tr.t.Addresses(agent)
		tr.mu.Unlock()
		if len(addrs) == 0 {
			return
		}
		to := addrs[0]
		if len(addrs) > 1 {
			labels := make([]string, len(addrs))
			for i, a := range addrs {
				labels[i] = a.Label()
			}
			out, err := exec.Command("osascript", append([]string{"-e", "on run argv", "-e", "activate", "-e",
				`set picked to choose from list (items 2 thru -1 of argv) with title "Leave a note" with prompt (item 1 of argv) default items {item 2 of argv}`,
				"-e", `if picked is false then error number -128`, "-e", "return item 1 of picked", "-e", "end run", "A note to"}, labels...)...).Output()
			if err != nil {
				return // cancelled
			}
			picked := strings.TrimSpace(string(out))
			for _, a := range addrs {
				if a.Label() == picked {
					to = a
				}
			}
		}
		when := "read at its next shift"
		if tr.t.Steerable(agent) {
			when = "its running shift reads it after its current step"
		}
		out, err := exec.Command("osascript", "-e", "on run argv", "-e", "activate", "-e",
			`text returned of (display dialog (item 1 of argv) with title (item 2 of argv) default answer "" buttons {"Cancel", "Leave note"} default button "Leave note")`,
			"-e", "end run", "A note for "+to.String()+", "+when+".", "Leave a note").Output()
		if err != nil {
			return
		}
		tr.mu.Lock()
		defer tr.mu.Unlock()
		tr.t.LeaveNote(agent, to.Ref, "human", string(out))
		tr.refresh()
	}()
}

// answerID is answer for the question with that id, if it is still open.
func (tr *tray) answerID(id string) {
	tr.mu.Lock()
	qs := tr.t.Questions()
	tr.mu.Unlock()
	for _, q := range qs {
		if q.ID == id {
			tr.answer(q)
		}
	}
}

func liveAgents(live *core.Status) []core.AgentStatus {
	if live == nil {
		return nil
	}
	return live.Agents
}

func check(item *systray.MenuItem, on bool) {
	if on {
		item.Check()
	} else {
		item.Uncheck()
	}
}

func (tr *tray) set(name, key, value string) { tr.t.SetAgent(name, map[string]string{key: value}) }

func (tr *tray) toggleOrganizer() {
	if tr.t.Live() != nil {
		tr.t.StopOrganizer()
		return
	}
	tr.stopWeb() // the organizer serves the pages itself
	if cmd, err := tr.t.StartOrganizer(tr.exe, false); err == nil {
		tr.organizer = cmd.Process // stopped when the menu quits
	}
}

func (tr *tray) quit() {
	if p := tr.organizer; p != nil && p.Signal(syscall.SIGTERM) == nil {
		for end := time.Now().Add(15 * time.Second); time.Now().Before(end) && p.Signal(syscall.Signal(0)) == nil; { // reaped once it ends
			time.Sleep(100 * time.Millisecond)
		}
	}
	tr.stopWeb()
	systray.Quit()
}

func (tr *tray) stopWeb() {
	if tr.web != nil && tr.web.Process != nil {
		tr.web.Process.Signal(syscall.SIGTERM)
	}
	tr.web = nil
}

// organizerEnv hands an organizer the menu started to the menu that takes its place.
const organizerEnv = "ROTA_TRAY_ORGANIZER"

// update runs a newly installed rota in the menu's place, as the organizer does between
// shifts: an organizer it started goes on and is handed over, an rota web it started is stopped,
// since the new menu runs its own when a page is opened.
func (tr *tray) update() {
	if !tr.self.Ready() {
		return
	}
	if web := tr.web; web != nil && web.Process != nil { // stopped and reaped first: the new menu could not
		p := web.Process
		tr.stopWeb()
		for end := time.Now().Add(3 * time.Second); time.Now().Before(end) && p.Signal(syscall.Signal(0)) == nil; {
			time.Sleep(50 * time.Millisecond)
		}
	}
	var env []string
	if p := tr.organizer; p != nil && p.Signal(syscall.Signal(0)) == nil {
		env = append(env, organizerEnv+"="+strconv.Itoa(p.Pid))
	}
	tr.self.Restart(env...) // returns only when the new one would not start: this one goes on
}

// openPage opens a page; when nothing serves the pages, the menu runs rota web itself.
func (tr *tray) openPage(path string) {
	if tr.t.Web == "off" {
		return
	}
	if conn, err := net.DialTimeout("tcp", tr.t.WebAddr(), 300*time.Millisecond); err == nil {
		conn.Close()
	} else if tr.web == nil {
		tr.web = exec.Command(tr.exe, "web", "-root", tr.root, "-config", tr.conf)
		if tr.web.Start() == nil {
			go tr.web.Wait()
		}
		time.Sleep(400 * time.Millisecond)
	}
	open(tr.t.WebURL(path))
}

func open(path string) { exec.Command("open", path).Start() }

func or(s, alt string) string {
	if s == "" {
		return alt
	}
	return s
}

// InstallCommand links ~/.local/bin/rota to exe, so the terminal runs the app's rota, and
// ~/.local/bin/gloss to the gloss beside it, which the agents write review notes with. A gloss
// there that is not a rota.app's is someone's own: it stays. A copy of the app that runs from a
// read-only place, its disk image or where macOS moved it to check it, links nothing: it goes away.
func InstallCommand(exe string) (string, error) {
	var fs unix.Statfs_t
	if unix.Statfs(exe, &fs) == nil && fs.Flags&unix.MNT_RDONLY != 0 {
		return "", fmt.Errorf("rota runs from its disk image: move it to Applications first")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if gloss := filepath.Join(filepath.Dir(exe), "gloss"); isFile(gloss) {
		link := filepath.Join(dir, "gloss")
		cur, err := os.Readlink(link)
		if _, gone := os.Lstat(link); os.IsNotExist(gone) || err == nil && strings.HasSuffix(cur, "/rota.app/Contents/MacOS/gloss") {
			relink(link, gloss)
		}
	}
	link := filepath.Join(dir, "rota")
	return link, relink(link, exe)
}

// relink points link at target, unless it already does.
func relink(link, target string) error {
	if cur, err := os.Readlink(link); err == nil && cur == target {
		return nil
	}
	os.Remove(link)
	return os.Symlink(target, link)
}

// isFile says whether path is a regular file.
func isFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}

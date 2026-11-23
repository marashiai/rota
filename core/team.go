// Package core is rota without a face: the team folder, the schedule, shifts, hosts and
// Claude's usage. The command line, the console, the web pages and the menu bar are built
// on it and on nothing else.
package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Team is a team folder as read at one moment: its settings, agents and shift records.
type Team struct {
	*Config
	Agents   []*Agent // by priority, then name
	Runs     []Run    // spend.csv, in the order written
	Warnings []string // settings that were ignored, and why
}

// Open reads the team folder root with its config file conf.
func Open(root, conf string) (*Team, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(root); err != nil {
		return nil, fmt.Errorf("no team folder at %s: run rota init (or set -root / $ROTA_ROOT)", root)
	}
	c, err := LoadConfig(root, conf)
	if err != nil {
		return nil, err
	}
	t := &Team{Config: c, Warnings: c.Warnings}
	files, _ := filepath.Glob(filepath.Join(c.Path(agentsDir), "*.md"))
	if len(files) == 0 {
		t.Warnings = append(t.Warnings, fmt.Sprintf("no agents in %s: add one as agents/<name>.md", c.Path(agentsDir)))
	}
	for _, f := range files {
		a, err := loadAgent(c, f)
		if err != nil {
			return nil, err
		}
		t.Agents = append(t.Agents, a)
		t.Warnings = append(t.Warnings, a.Warnings...)
	}
	for _, l := range [][2]string{{"hold_label", c.HoldLabel}, {"review_label", c.ReviewLabel}} {
		if l[1] != "" && findLabel(t.Labels(), l[1]) == nil {
			t.Warnings = append(t.Warnings, fmt.Sprintf("%s: %s %s is not one of the team's labels", conf, l[0], l[1]))
		}
	}
	sort.Slice(t.Agents, func(i, j int) bool {
		x, y := t.Agents[i], t.Agents[j]
		if x.Priority != y.Priority {
			return x.Priority < y.Priority
		}
		return x.Name < y.Name
	})
	var owners []*Agent // an hour has one owner: the agent first in the team's order keeps it
	for _, a := range t.Agents {
		if b, when := ownedOverlap(a, owners); b != nil {
			a.OwnHours = false
			w := fmt.Sprintf("%s: own_hours ignored: on %s its hours overlap those %s owns", c.Rel(t.AgentFile(a.Name)), when, b.Name)
			a.Warnings = append(a.Warnings, w)
			t.Warnings = append(t.Warnings, w)
		} else if a.Enabled && a.OwnHours {
			owners = append(owners, a)
		}
	}
	t.Runs, err = loadRuns(c)
	return t, err
}

// Agent is the named agent, or nil.
func (t *Team) Agent(name string) *Agent {
	for _, a := range t.Agents {
		if a.Name == name {
			return a
		}
	}
	return nil
}

func (t *Team) agent(name string) (*Agent, error) {
	if a := t.Agent(name); a != nil {
		return a, nil
	}
	return nil, fmt.Errorf("no agent %q", name)
}

// ---- changing the team ----

// AgentFile is the agent's file: its settings and its role.
func (t *Team) AgentFile(name string) string { return filepath.Join(t.Path(agentsDir), name+".md") }

// AgentSettings are the settings the agent's file sets itself, as written; the rest are the
// team's defaults.
func (t *Team) AgentSettings(name string) map[string]string {
	head, _, _ := splitFront(readFile(t.AgentFile(name)))
	kv, _ := parseKV(head)
	if kv == nil {
		kv = map[string]string{}
	}
	return kv
}

// Settings are the settings rota.conf sets, as written; the rest are the defaults.
func (t *Team) Settings() map[string]string {
	kv, _ := parseKV(readFile(t.Path(t.File)))
	if kv == nil {
		kv = map[string]string{}
	}
	return kv
}

// SetAgent writes settings into an agent's file; an empty value means the team default. It
// refuses settings that would have the agent own hours another agent owns.
func (t *Team) SetAgent(name string, set map[string]string) error {
	if _, err := t.agent(name); err != nil {
		return err
	}
	path := t.AgentFile(name)
	return changeLocked(path, func() error {
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		s, err := withFrontMatter(string(b), set)
		if err != nil {
			return err
		}
		was, err1 := parseAgent(t.Config, path, b)
		now, err2 := parseAgent(t.Config, path, []byte(s))
		if err1 == nil && err2 == nil {
			if other, when := ownedOverlap(now, t.Agents); other != nil {
				if before, _ := ownedOverlap(was, t.Agents); before == nil { // one it overlapped already keeps its warning
					return fmt.Errorf("own_hours: on %s its hours would overlap those %s owns", when, other.Name)
				}
			}
		}
		return writeWhole(path, []byte(s))
	})
}

// swapAgent writes settings into an agent's file as SetAgent does, and says what they replaced:
// each key's value as written, or that the file had none.
func (t *Team) swapAgent(name string, set map[string]string) (was map[string]string, absent []string, err error) {
	if _, err := t.agent(name); err != nil {
		return nil, nil, err
	}
	path := t.AgentFile(name)
	was = map[string]string{}
	err = changeLocked(path, func() error {
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		head, _, err := splitFront(string(b))
		if err != nil {
			return err
		}
		kv, err := parseKV(head)
		if err != nil {
			return err
		}
		for _, k := range sortedKeys(set) {
			if v, ok := kv[k]; ok {
				was[k] = v
			} else {
				absent = append(absent, k)
			}
		}
		s, err := withFrontMatter(string(b), set)
		if err != nil {
			return err
		}
		before, err1 := parseAgent(t.Config, path, b)
		after, err2 := parseAgent(t.Config, path, []byte(s))
		if err1 == nil && err2 == nil { // as SetAgent: no owned hours over another's
			if other, when := ownedOverlap(after, t.Agents); other != nil {
				if already, _ := ownedOverlap(before, t.Agents); already == nil {
					return fmt.Errorf("own_hours: on %s its hours would overlap those %s owns", when, other.Name)
				}
			}
		}
		return writeWhole(path, []byte(s))
	})
	return was, absent, err
}

// restoreAgent writes back what swapAgent replaced, exactly: an empty value stays one; unless one
// of them changed since from what was set then (expect).
func (t *Team) restoreAgent(name string, was map[string]string, absent []string, expect map[string]string) error {
	path := t.AgentFile(name)
	return changeLocked(path, func() error {
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		head, _, err := splitFront(string(b))
		if err != nil {
			return err
		}
		kv, _ := parseKV(head)
		for k, v := range expect { // as the swap left it: an empty value removed the key
			now, there := kv[k]
			if v = strings.TrimSpace(v); there == (v == "") || now != v {
				return fmt.Errorf("%s's %s has changed since: it is %q now; undo would overwrite that", name, k, now)
			}
		}
		s, err := frontMatter(string(b), was, absent, true)
		if err != nil {
			return err
		}
		return writeWhole(path, []byte(s))
	})
}

// SetRole replaces an agent's role, the system prompt it works under.
func (t *Team) SetRole(name, role string) error {
	if _, err := t.agent(name); err != nil {
		return err
	}
	return setRole(t.AgentFile(name), role)
}

// SetSetting writes one setting into rota.conf; empty means the default.
func (t *Team) SetSetting(key, value string) error { return setConfKey(t.Path(t.File), key, value) }

// SetSettings writes several settings in one write, all or none.
func (t *Team) SetSettings(set map[string]string) error { return setConfKeys(t.Path(t.File), set) }

// ---- what the team has written ----

// Inboxes names every inbox: the agents', then those of agents that no longer exist.
func (t *Team) Inboxes() []string {
	var names []string
	for _, a := range t.Agents {
		names = append(names, a.Name)
	}
	files, _ := filepath.Glob(filepath.Join(t.Path(inboxDir), "*.json"))
	for _, f := range files {
		if n := strings.TrimSuffix(filepath.Base(f), ".json"); t.Agent(n) == nil {
			names = append(names, n)
		}
	}
	return names
}

// Options are the values an editor offers for a setting: its choices or suggestions, and
// for a host the names of the hosts that have joined.
func (t *Team) Options(f Field) []string {
	out := f.Options()
	if strings.TrimPrefix(f.Key, "default_") == "host" {
		for _, h := range knownHosts(t.Config) {
			out = append(out, h.Name)
		}
	}
	return out
}

// JournalDays are the journal's files, newest first.
func (t *Team) JournalDays() []string {
	return newestFirst(filepath.Join(t.Path(journalDir), "*.md"))
}

// Reports are the human's reports, one a day, newest first.
func (t *Team) Reports() []string { return newestFirst(filepath.Join(t.Path(reportsDir), "*.md")) }

// AgentReports are the files the agent wrote for the human in reports/<agent>/, the latest
// changed first.
func (t *Team) AgentReports(agent string) []string {
	dir := t.agentReportsDir(agent)
	if dir == "" {
		return nil
	}
	entries, _ := os.ReadDir(dir)
	type file struct {
		path string
		at   time.Time
	}
	var files []file
	for _, e := range entries {
		if info, err := e.Info(); err == nil && info.Mode().IsRegular() && !strings.HasPrefix(e.Name(), ".") {
			files = append(files, file{filepath.Join(dir, e.Name()), info.ModTime()})
		}
	}
	sort.SliceStable(files, func(i, j int) bool { return files[i].at.After(files[j].at) })
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.path
	}
	return out
}

// AgentReport is the agent's report of that name, or "" when it has none such, or it now leads
// out of the agent's folder.
func (t *Team) AgentReport(agent, name string) string {
	for _, p := range t.AgentReports(agent) {
		if filepath.Base(p) != name {
			continue
		}
		if real, err := filepath.EvalSymlinks(p); err == nil && real == filepath.Join(mustReal(filepath.Dir(p)), name) {
			return p
		}
	}
	return ""
}

// agentReportsDir is reports/<agent>/ of an agent of the team, or "".
func (t *Team) agentReportsDir(agent string) string {
	if t.Agent(agent) == nil {
		return ""
	}
	for _, dir := range []string{t.Path(reportsDir), filepath.Join(t.Path(reportsDir), agent)} {
		if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() { // a link put in its place leads elsewhere
			return ""
		}
	}
	return filepath.Join(t.Path(reportsDir), agent)
}

// Evidence is a file a pull request or a report points to, under evidence/ (the folder itself
// for ""); "" when the path leads anywhere else.
func (t *Team) Evidence(rel string) string {
	base := t.Path(evidenceDir)
	if fi, err := os.Lstat(base); err != nil || !fi.IsDir() { // a link put in its place leads elsewhere
		return ""
	}
	if rel == "" {
		return base
	}
	p := filepath.Join(base, filepath.FromSlash(rel))
	if !strings.HasPrefix(p, base+string(filepath.Separator)) {
		return ""
	}
	if real, err := filepath.EvalSymlinks(p); err != nil || !strings.HasPrefix(real, mustReal(base)+string(filepath.Separator)) {
		return ""
	}
	return p
}

func mustReal(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// Transcript is one shift's event stream, logs/<date>/<HHMM>-<agent>.jsonl.
type Transcript struct {
	Path  string
	Agent string
	Start time.Time
	Run   *Run // its record in spend.csv; nil while it runs, or when it never finished
}

// Transcripts are the kept shift logs, newest first; agent "" means every agent's.
func (t *Team) Transcripts(agent string) []Transcript {
	if agent == "" {
		agent = "*"
	}
	byStart := map[string]*Run{}
	for i := range t.Runs {
		r := &t.Runs[i]
		byStart[r.Agent+"|"+r.Time.In(t.Zone.Location).Format("2006-01-02 1504")] = r
	}
	var out []Transcript
	for _, p := range newestFirst(filepath.Join(t.Path(logDir), "*", "*-"+agent+".jsonl")) {
		hhmm, name, _ := strings.Cut(strings.TrimSuffix(filepath.Base(p), ".jsonl"), "-")
		key := filepath.Base(filepath.Dir(p)) + " " + hhmm
		start, _ := time.ParseInLocation("2006-01-02 1504", key, t.Zone.Location)
		out = append(out, Transcript{Path: p, Agent: name, Start: start, Run: byStart[name+"|"+key]})
	}
	return out
}

// Running says whether this is the shift the organizer is running now.
func (tr Transcript) Running(st Status) bool {
	r := st.Running
	return tr.Run == nil && r != nil && r.Agent == tr.Agent && r.Since.Sub(tr.Start).Abs() < 2*time.Minute
}

func newestFirst(glob string) []string {
	files, _ := filepath.Glob(glob)
	sort.Sort(sort.Reverse(sort.StringSlice(files)))
	return files
}

// ---- controlling the organizer ----
//
// The organizer (rota start) watches a few files in the team folder; every front end acts
// through them, so they work the same from any process and whether the organizer runs or not.

func (c *Config) pausedFile() string { return c.Path(".paused") }
func (c *Config) kickFile() string   { return c.Path(".kick") }
func (c *Config) cancelFile() string { return c.Path(".cancel") }
func (c *Config) askedDir() string   { return c.Path(".run-now") }

// Paused says whether the team is paused: no new shift starts, except ones asked for.
func (t *Team) Paused() bool { _, err := os.Stat(t.pausedFile()); return err == nil }

// Pause stops new shifts from starting; the running one finishes.
func (t *Team) Pause() error { return os.WriteFile(t.pausedFile(), nil, 0o644) }

// Unpause undoes Pause; an interrupted shift that waited for it resumes.
func (t *Team) Unpause() error {
	if err := os.Remove(t.pausedFile()); err != nil && !os.IsNotExist(err) {
		return err
	}
	t.askToResume()
	return t.kick() // what waited for it starts now, not at the next tick
}

// PauseAgent stops the agent's new shifts until ResumeAgent: its running shift finishes, and
// it keeps what asks for it, its turn and its share of the pool. The human's Run now still
// runs it.
func (t *Team) PauseAgent(agent string) error {
	if _, err := t.agent(agent); err != nil {
		return err
	}
	if err := os.MkdirAll(t.Path(heldDir), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(t.Path(heldDir), agent), nil, 0o644)
}

// ResumeAgent undoes PauseAgent; the agent starts at once when it is due or asked for, unless
// the team is paused: a wake-up then would be the human's Run now.
func (t *Team) ResumeAgent(agent string) error {
	if _, err := t.agent(agent); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(t.Path(heldDir), agent)); err != nil && !os.IsNotExist(err) {
		return err
	}
	if t.Paused() {
		return nil
	}
	return t.kick()
}

// PausedAgents are the agents the human paused.
func (c *Config) PausedAgents() map[string]bool {
	paused := map[string]bool{}
	entries, _ := os.ReadDir(c.Path(heldDir))
	for _, e := range entries {
		paused[e.Name()] = true
	}
	return paused
}

// RunNow is the human asking for a shift now. For an agent it marks the agent due, its
// interval, hours and retry wait skipped, until its shift starts, and puts it next, ahead of
// every agent waiting longer: every request is served, the human's before the team's, within
// the team pool. Without an agent it asks for the next eligible shift. Either way the
// organizer wakes up to look.
func (t *Team) RunNow(agent string) error {
	if agent != "" {
		if _, err := t.agent(agent); err != nil {
			return err
		}
		if err := t.askByHand(agent); err != nil {
			return err
		}
	}
	return t.kick()
}

// kick wakes the organizer up to look.
func (c *Config) kick() error { return os.WriteFile(c.kickFile(), nil, 0o644) }

// AskToRun is RunNow as the console and the pages offer it: it refuses what cannot work and
// says what happens next, by the given status.
func (t *Team) AskToRun(name string, st Status) (string, error) {
	if st.Pid == 0 {
		return "", fmt.Errorf("the organizer is not running; start it first")
	}
	if a := t.Agent(name); a != nil && !a.Enabled {
		return "", fmt.Errorf("%s is disabled; enable it first", name)
	}
	r := t.RunWait(name, st)
	if err := t.RunNow(name); err != nil {
		return "", err
	}
	switch {
	case name == "":
		return "asked the organizer for the next shift now", nil
	case r.Why != "":
		return name + " is asked for, but cannot start now: " + r.Why + "; its next run: " + r.Next, nil
	case r.After != "":
		return name + " is asked for: it starts after " + r.After + "'s shift, if it still may then", nil
	}
	return name + " asked to run now: it starts ahead of every agent you did not ask for", nil
}

// byHand is what a request to run now holds when the human made it.
const byHand = "human"

// askFor asks for the agent's shift on the team's behalf: it skips the agent's interval, and
// it waits its turn from now. Asking again keeps its turn, and a request the human made stays
// theirs.
func (t *Team) askFor(agent string) error {
	if err := os.MkdirAll(t.askedDir(), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(t.askedDir(), agent), os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	return f.Close()
}

// askByHand is the human's request for the agent's shift: it goes ahead of priority. It
// replaces a request on the team's behalf in one step, so it never reads as one. Asked again,
// the agent keeps its place.
func (t *Team) askByHand(agent string) error {
	if b, err := os.ReadFile(filepath.Join(t.askedDir(), agent)); err == nil && string(b) == byHand {
		return nil
	}
	if err := os.MkdirAll(t.askedDir(), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(t.Path(""), ".run-now-*")
	if err != nil {
		return err
	}
	_, err = f.WriteString(byHand)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), filepath.Join(t.askedDir(), agent))
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

// Asked are the agents asked to run now whose shift has not started yet.
func (c *Config) Asked() map[string]bool {
	asked := map[string]bool{}
	for name := range c.askedAt() {
		asked[name] = true
	}
	return asked
}

// askedAt is when each agent of Asked was asked for: its request's time, which asking again
// does not change.
func (c *Config) askedAt() map[string]time.Time {
	at := map[string]time.Time{}
	entries, _ := os.ReadDir(c.askedDir())
	for _, e := range entries {
		if info, err := e.Info(); err == nil {
			at[e.Name()] = info.ModTime()
		}
	}
	return at
}

// askedByHand are the agents of Asked that the human asked for.
func (c *Config) askedByHand() map[string]bool {
	by := map[string]bool{}
	for name := range c.Asked() {
		if b, _ := os.ReadFile(filepath.Join(c.askedDir(), name)); string(b) == byHand {
			by[name] = true
		}
	}
	return by
}

func (c *Config) answered(agent string) { os.Remove(filepath.Join(c.askedDir(), agent)) }

// CancelShift stops the running shift, agent's only when agent is not ""; the organizer goes on.
func (t *Team) CancelShift(agent string) error {
	st := t.Live()
	switch {
	case st == nil || st.Running == nil:
		return errors.New("no shift is running")
	case agent != "" && st.Running.Agent != agent:
		return fmt.Errorf("%s is not working", agent)
	}
	return writeWhole(t.cancelFile(), []byte(agent)) // named from the start: never read as any shift's
}

// Live is the running organizer's last snapshot, or nil when none runs. A live process that
// has not written for over two minutes is marked Stale: the machine was probably asleep.
func (c *Config) Live() *Status {
	var st Status
	b, err := os.ReadFile(c.Path(stateFile))
	if err != nil || json.Unmarshal(b, &st) != nil || st.Pid == 0 || syscall.Kill(st.Pid, 0) != nil {
		return nil
	}
	st.Stale = time.Since(st.Time) > 2*time.Minute
	if c.Usage == "off" {
		st.UsageError = "" // set off since the organizer's last look: nothing waits for it now
	}
	return &st
}

// Stamp changes whenever what the pages show may have changed: the organizer's snapshot or
// its process, a transcript, the journal, a report, a question, a note, spend.csv,
// the paired hosts, an agent's file, the settings, what narrows the Reviews tab, a pause or a request to run; and every minute, for what the
// clock changes (an agent coming due). The pages update in place when it does.
func (c *Config) Stamp() string {
	h := fnv.New64a()
	add := func(p string) {
		if fi, err := os.Stat(p); err == nil {
			fmt.Fprintf(h, "%s %d %d\n", p, fi.Size(), fi.ModTime().UnixNano())
		}
	}
	add(c.Path(c.File))
	for _, f := range []string{stateFile, spendFile, hostsFile, ".paused", ".run-now", logDir, openFile, queueFile, planFile, seenFile, mailFile, filterFile(ReviewsTab), filterFile(IssuesTab), steerDir, talkDir, chatFile, ".chat-busy"} {
		add(c.Path(f))
	}
	for _, dir := range []string{journalDir, reportsDir, questionsDir, inboxDir, agentsDir, sentDir} {
		add(c.Path(dir))
		entries, _ := os.ReadDir(c.Path(dir))
		for _, e := range entries {
			add(c.Path(filepath.Join(dir, e.Name())))
		}
	}
	if days := newestFirst(filepath.Join(c.Path(logDir), "*")); len(days) > 0 { // the running shift's transcript grows
		entries, _ := os.ReadDir(days[0])
		for _, e := range entries {
			add(filepath.Join(days[0], e.Name()))
		}
	}
	live := c.Live()
	fmt.Fprintf(h, "%v %v %s", live != nil, live != nil && live.Stale, time.Now().Format("15:04"))
	return strconv.FormatUint(h.Sum64(), 36)
}

// StopOrganizer stops the running organizer (SIGTERM, like Ctrl-C) and waits up to ten
// seconds for it to go: its running shift is recorded as interrupted.
func (t *Team) StopOrganizer() error {
	live := t.Live()
	if live == nil {
		return fmt.Errorf("no organizer is running here")
	}
	if err := syscall.Kill(live.Pid, syscall.SIGTERM); err != nil {
		return err
	}
	for i := 0; i < 50 && syscall.Kill(live.Pid, 0) == nil; i++ {
		time.Sleep(200 * time.Millisecond)
	}
	return nil
}

// StartOrganizer runs exe start for this team in the background, its output appended to
// rota.log (the previous log kept as rota.log.1 once it passes 5 MB). Detached, it runs in
// a session of its own and outlives whoever started it.
func (t *Team) StartOrganizer(exe string, detach bool) (*exec.Cmd, error) {
	logPath := t.Path("rota.log")
	if st, err := os.Stat(logPath); err == nil && st.Size() > 5<<20 {
		os.Rename(logPath, logPath+".1")
	}
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	defer logf.Close() // the child has its own copy
	cmd := exec.Command(exe, "start", "-root", t.Root, "-config", t.File)
	cmd.Stdout, cmd.Stderr = logf, logf
	if detach {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go cmd.Wait()
	return cmd, nil
}

// WebAddr is where this machine reaches the pages.
func (c *Config) WebAddr() string { return net.JoinHostPort("127.0.0.1", strconv.Itoa(c.WebPort)) }

// PagesURL is a page's address for links that other devices open too: at this machine's
// Tailscale address when the pages are served there, else on this machine.
func (c *Config) PagesURL(path string) string {
	if c.Web == "tailscale" {
		if u := TailscaleHTTPS(c.WebPort); u != "" { // a phone allows notifications only there
			return u + path
		}
		if ip := TailscaleIP(); ip != "" {
			return "http://" + net.JoinHostPort(ip, strconv.Itoa(c.WebPort)) + path
		}
	}
	return c.WebURL(path)
}

// WebURL is a page's address.
func (c *Config) WebURL(path string) string { return "http://" + c.WebAddr() + path }

// ---- small helpers ----

func readFile(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

// appendFile adds to a file under its lock: two writers never mix their lines.
func appendFile(path, s string) error {
	os.MkdirAll(filepath.Dir(path), 0o755)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
	_, err = f.WriteString(s)
	return err
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "none"
	}
	return s
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}

func expand(tmpl string, vars map[string]string) string {
	pairs := make([]string, 0, 2*len(vars))
	for k, v := range vars {
		pairs = append(pairs, k, v)
	}
	return strings.NewReplacer(pairs...).Replace(tmpl)
}

func or(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

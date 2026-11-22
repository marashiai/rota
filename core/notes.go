package core

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// An agent's inbox holds the notes waiting for it, each a record of its own: from a teammate,
// the human or the organizer, in general or about one pull request or issue. The human
// addresses notes the way agents do: @developer, @developer shop-creator#554. A note that
// arrives while the agent's shift runs reaches that shift after its current step; otherwise
// its next shift reads it. Every step names notes by id: what a shift was briefed with, what
// reached it, what it read, and what leaves the inbox.

// Note is one note waiting in an agent's inbox.
type Note struct {
	ID    string    `json:"id"`
	At    time.Time `json:"at"`
	From  string    `json:"from"`            // a teammate, human or organizer
	About string    `json:"about,omitempty"` // owner/name#n: one pull request or issue; "" for a general note
	Text  string    `json:"text"`
	// Message: the human wrote it in the mail and waits for the agent's answer (sent/<id>.json).
	Message bool `json:"message,omitempty"`
	// Chat: the human wrote it in the team chat while the agent worked, and waits for its answer there.
	Chat bool `json:"chat,omitempty"`
}

// Inbox is the file of an agent's notes, inbox/<agent>.json.
func (c *Config) Inbox(agent string) string { return filepath.Join(c.Path(inboxDir), agent+".json") }

// Notes are the notes waiting for an agent, oldest first. The inbox is replaced whole, so it
// is read without a lock.
func (c *Config) Notes(agent string) []Note {
	notes, _ := c.readNotes(agent)
	return notes
}

func (c *Config) readNotes(agent string) ([]Note, error) {
	var notes []Note
	b, err := os.ReadFile(c.Inbox(agent))
	if err != nil {
		if os.IsNotExist(err) {
			err = nil
		}
		return nil, err
	}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &notes); err != nil {
			return nil, fmt.Errorf("%s: %v", c.Rel(c.Inbox(agent)), err)
		}
	}
	return notes, nil
}

// lockInbox takes an agent's inbox for changing it, until unlock: one change at a time.
func (c *Config) lockInbox(agent string) (unlock func(), err error) {
	if agent == "" || filepath.Base(agent) != agent || strings.HasPrefix(agent, ".") {
		return nil, fmt.Errorf("%q is not an agent", agent)
	}
	os.MkdirAll(c.Path(inboxDir), 0o755)
	f, err := os.OpenFile(c.Path(filepath.Join(inboxDir, "."+agent+".lock")), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
	return func() { f.Close() }, nil
}

// changeNotes replaces an agent's notes by what edit makes of them, under the inbox's lock,
// whole: a reader sees the old notes or the new ones. An inbox that cannot be read is left as
// it is.
func (c *Config) changeNotes(agent string, edit func([]Note) []Note) error {
	unlock, err := c.lockInbox(agent)
	if err != nil {
		return err
	}
	defer unlock()
	notes, err := c.readNotes(agent)
	if err != nil {
		return err
	}
	return c.writeNotes(agent, edit(notes))
}

// writeNotes replaces an agent's inbox; its lock is held.
func (c *Config) writeNotes(agent string, notes []Note) error {
	path := c.Inbox(agent)
	if len(notes) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	b, _ := json.MarshalIndent(notes, "", "  ")
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp")
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// addNote puts a note in an agent's inbox: a general one, or about ref (owner/name#n).
func (c *Config) addNote(agent, ref, from, text string, at time.Time) error {
	n := Note{ID: at.Format("20060102-150405") + "-" + randHex(3), At: at, From: from, About: ref, Text: strings.TrimSpace(text)}
	return c.changeNotes(agent, func(notes []Note) []Note { return append(notes, n) })
}

// dropNotes removes notes from an agent's inbox by id: its shift read them. The human's messages
// among them are marked read, if they are not yet.
func (c *Config) dropNotes(agent string, ids map[string]bool) error {
	var read []Note
	err := c.changeNotes(agent, func(notes []Note) []Note {
		return slices.DeleteFunc(notes, func(n Note) bool {
			if ids[n.ID] {
				read = append(read, n)
			}
			return ids[n.ID]
		})
	})
	c.messagesRead(read, time.Now())
	return err
}

// DeleteNote removes one note from an agent's inbox.
func (c *Config) DeleteNote(agent, id string) error {
	found := false
	err := c.changeNotes(agent, func(notes []Note) []Note {
		return slices.DeleteFunc(notes, func(n Note) bool { found = found || n.ID == id; return n.ID == id })
	})
	if err == nil && !found {
		err = fmt.Errorf("that note is no longer there")
	}
	return err
}

// ids are the notes' ids.
func ids(notes []Note) map[string]bool {
	m := map[string]bool{}
	for _, n := range notes {
		m[n.ID] = true
	}
	return m
}

// NotesText is notes as an agent reads them: the general ones first, then those about each pull
// request or issue under its heading, "## owner/name#n", each "- <date> from <who>: <text>".
func (c *Config) NotesText(notes []Note) string {
	var b strings.Builder
	var refs []string
	for _, n := range notes {
		if n.About == "" {
			b.WriteString(c.NoteLine(n) + "\n")
		} else if !slices.Contains(refs, n.About) {
			refs = append(refs, n.About)
		}
	}
	for _, ref := range refs {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("## " + ref + "\n")
		for _, n := range notes {
			if n.About == ref {
				b.WriteString(c.NoteLine(n) + "\n")
			}
		}
	}
	return b.String()
}

// NoteLine is one note as its list item, a text of several lines indented under it.
func (c *Config) NoteLine(n Note) string {
	return fmt.Sprintf("- %s: %s", c.NoteHead(n), strings.ReplaceAll(n.Text, "\n", "\n  "))
}

// NoteHead says when a note was left and by whom.
func (c *Config) NoteHead(n Note) string {
	who := n.From
	if who == "human" {
		who = "the human"
	}
	if n.Message { // the agent answers it by its id
		who += ", message " + n.ID
	}
	day := "2006-01-02"
	if n.Chat { // how long they have waited for its answer
		who, day = who+" in the team chat", day+" 15:04"
	}
	return n.At.In(c.Zone.Location).Format(day) + " from " + who
}

// Address is where a note can go: an agent's general notes, or those about one pull request
// or issue of its repositories.
type Address struct {
	Agent string
	Ref   string // owner/name#n; "" for the agent's general notes
	Name  string // Ref as agents write it: name#n, or owner/name#n when two repositories share the name
	Title string // what GitHub calls it, once GitHub has said
	PR    bool
}

// String is the address as agents write it.
func (a Address) String() string {
	if a.Ref == "" {
		return "@" + a.Agent
	}
	return "@" + a.Agent + " " + a.Name
}

// Label is the address with what it is about, for a list to choose from.
func (a Address) Label() string {
	if a.Title == "" {
		return a.String()
	}
	return a.String() + " · " + a.Title
}

// AddressOf is the address of an agent's notes about ref (owner/name#n), or with ref "" of its
// general notes.
func (t *Team) AddressOf(agent, ref string) Address {
	a := Address{Agent: agent, Ref: ref, Name: ref}
	if m := fullRefOnly.FindStringSubmatch(ref); m != nil {
		if ag := t.Agent(agent); ag != nil {
			a.Name = shortRef(ag.Repos, m[1]) + "#" + m[2]
		}
	}
	return a
}

// addressMax bounds the pull requests and issues offered for one agent.
const addressMax = 30

var (
	journalSection = regexp.MustCompile(`^## \d\d:\d\d (\S+) on `)
	refMention     = regexp.MustCompile(`github\.com/([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)/(?:pull|issues)/(\d+)|([A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)?)#(\d+)`)
	fullRefOnly    = regexp.MustCompile(`^([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)#(\d+)$`)
)

// Addresses are where the human can leave the agent a note: its general notes, then the open
// pull requests and issues it has notes waiting about or named in its journal entries of the
// last journalDays, most recently named first. Which are open, and their titles, come from
// GitHub, asked in the background at most every openFresh; of a repository it has not listed,
// only those with notes waiting are offered.
func (t *Team) Addresses(agent string) []Address {
	a := t.Agent(agent)
	if a == nil {
		return nil
	}
	out := []Address{{Agent: agent}}
	open := t.openWork()
	var waiting []string
	for _, n := range t.Notes(agent) {
		if n.About != "" && !slices.Contains(waiting, n.About) {
			waiting = append(waiting, n.About)
		}
	}
	seen := map[string]bool{}
	for _, ref := range append(waiting, t.mentions(a)...) {
		m := fullRefOnly.FindStringSubmatch(ref)
		if m == nil || seen[ref] || len(out) > addressMax {
			continue
		}
		seen[ref] = true
		n, _ := strconv.Atoi(m[2])
		items, listed := open.Repos[m[1]]
		item, ok := items[n]
		if !listed {
			ok = slices.Contains(waiting, ref)
		}
		if ok {
			to := t.AddressOf(agent, ref)
			to.Title, to.PR = item.Title, item.PR
			out = append(out, to)
		}
	}
	return out
}

// mentions are the pull requests and issues of a's repositories that its journal entries of
// the last journalDays name, owner/name#n, the most recent first.
func (t *Team) mentions(a *Agent) []string {
	cutoff := time.Now().AddDate(0, 0, -journalDays).Format("2006-01-02")
	var refs []string
	for _, f := range t.JournalDays() { // newest first
		if strings.TrimSuffix(filepath.Base(f), ".md") < cutoff {
			break
		}
		var mine []string // a's entries of that day
		cur, in := "", false
		for _, l := range strings.Split(readFile(f), "\n") {
			if m := journalSection.FindStringSubmatch(l); m != nil {
				if in {
					mine = append(mine, cur)
				}
				cur, in = "", m[1] == a.Name
			} else if in {
				cur += l + "\n"
			}
		}
		if in {
			mine = append(mine, cur)
		}
		for i := len(mine) - 1; i >= 0; i-- {
			for _, m := range refMention.FindAllStringSubmatch(mine[i], -1) {
				short, n := m[1], m[2]
				if short == "" {
					short, n = m[3], m[4]
				}
				if repo, ok := resolveRepo(a.Repos, short); ok {
					refs = append(refs, repo+"#"+n)
				}
			}
		}
	}
	return refs
}

// shortRef is a repository as agents name it: its name, or owner/name when another of repos
// has the same name.
func shortRef(repos []string, repo string) string {
	name := repo[strings.LastIndex(repo, "/")+1:]
	n := 0
	for _, r := range repos {
		if strings.EqualFold(r[strings.LastIndex(r, "/")+1:], name) {
			n++
		}
	}
	if n > 1 {
		return repo
	}
	return name
}

// openList is the team's open pull requests and issues as GitHub last listed them: by
// repository, then number. A repository GitHub has never listed is missing.
type openList struct {
	Time  time.Time                   `json:"time"`  // when GitHub last listed all of them
	Tried time.Time                   `json:"tried"` // when it was last asked
	Repos map[string]map[int]openItem `json:"repos"`
}

type openItem struct {
	Title string `json:"title"`
	PR    bool   `json:"pr,omitempty"`
	// Of a pull request: its branch, the branch it merges into, and the pull requests and
	// issues (owner/name#n) its description says it "Depends on", as rota merge reads them;
	// its head commit, and the issues it is for (forIssues).
	Head    string    `json:"head,omitempty"`
	Base    string    `json:"base,omitempty"`
	Deps    []string  `json:"deps,omitempty"`
	Commit  string    `json:"commit,omitempty"`
	Issues  []string  `json:"issues,omitempty"`
	Labels  []string  `json:"labels,omitempty"` // of a pull request or an issue
	Created time.Time `json:"created,omitzero"` // when an issue was opened
	// Of a pull request: how big it is, and whether it is a draft.
	Add   int  `json:"add,omitempty"`
	Del   int  `json:"del,omitempty"`
	Files int  `json:"files,omitempty"`
	Draft bool `json:"draft,omitempty"`
	// When GitHub last saw it change, its notes included, and where its notes stand.
	Updated time.Time `json:"updated,omitzero"`
	Talk    talk      `json:"talk,omitzero"`
}

// openFresh is how long GitHub's list of what is open is used before it is asked again, and
// openRetry how soon it is asked again when it did not answer.
var (
	openFresh = 5 * time.Minute
	openRetry = time.Minute
)

func (l openList) due() bool {
	return time.Since(l.Time) > openFresh && time.Since(l.Tried) > openRetry
}

// PRAgent is the agent that wrote an open pull request of the team's, by its branch (<agent>/…) as
// GitHub listed it last; "" when no agent of the team did.
func (t *Team) PRAgent(repo string, number int) string {
	if a, _, ok := strings.Cut(t.openWork().Repos[repo][number].Head, "/"); ok && t.Agent(a) != nil {
		return a
	}
	return ""
}

// openWork is GitHub's last list of what is open; an older one than openFresh is refreshed in
// the background.
func (t *Team) openWork() openList {
	var l openList
	if b, err := os.ReadFile(t.Path(openFile)); err == nil {
		json.Unmarshal(b, &l)
	}
	if l.due() && len(t.teamRepos()) > 0 {
		refreshing.Add(1)
		go func() { defer refreshing.Done(); t.refreshOpen() }()
	}
	return l
}

// refreshing counts the refreshes under way in the background, which a test waits for before its
// team folder is removed: one that writes in it then fails the removal.
var refreshing sync.WaitGroup

// refreshOpen asks GitHub for the open pull requests and issues of every team repository, one
// process at a time; a repository GitHub does not answer for keeps its last list, if any.
func (t *Team) refreshOpen() {
	lock, err := os.OpenFile(t.Path(openFile+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return
	}
	defer lock.Close()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return // another process is asking
	}
	var old openList
	if b, err := os.ReadFile(t.Path(openFile)); err == nil && json.Unmarshal(b, &old) == nil && !old.due() {
		return // it has just asked
	}
	l := openList{Time: old.Time, Tried: time.Now(), Repos: map[string]map[int]openItem{}}
	load, _ := reviewLoader() // without gh's login, the notes keep where they last stood
	var mu sync.Mutex
	var wg sync.WaitGroup
	failed := false
	var news []Answer
	for _, repo := range t.teamRepos() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			items, err := t.listOpen(repo)
			listed := err == nil
			var told []Answer
			if listed {
				told = t.readTalk(repo, items, old.Repos[repo], load)
			} else {
				items, listed = old.Repos[repo]
			}
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed = true
			}
			if listed {
				l.Repos[repo] = items
			}
			news = append(news, told...)
		}()
	}
	wg.Wait()
	if !failed { // a repository that did not answer is asked again after openRetry
		l.Time = l.Tried
	}
	t.writeOpen(l)
	t.tellAnswers(news)
}

// relistOpen asks GitHub again for one repository's open pull requests and issues, at once: after
// the human changed one of them, the pages show it as GitHub has it now.
func (t *Team) relistOpen(repo string) error {
	lock, err := os.OpenFile(t.Path(openFile+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil { // after a refresh under way
		return err
	}
	items, err := t.listOpen(repo)
	if err != nil {
		return err
	}
	var l openList
	if b, err := os.ReadFile(t.Path(openFile)); err == nil {
		json.Unmarshal(b, &l)
	}
	if l.Repos == nil {
		l.Repos = map[string]map[int]openItem{}
	}
	load, _ := reviewLoader()
	news := t.readTalk(repo, items, l.Repos[repo], load)
	l.Repos[repo] = items
	t.writeOpen(l)
	t.tellAnswers(news)
	return nil
}

func (t *Team) writeOpen(l openList) {
	b, _ := json.Marshal(l)
	tmp := t.Path("." + openFile + ".tmp")
	if os.WriteFile(tmp, b, 0o644) == nil {
		os.Rename(tmp, t.Path(openFile))
	}
}

// listOpen is one repository's open pull requests and issues.
func (t *Team) listOpen(repo string) (map[int]openItem, error) {
	var prs []struct {
		Number      int       `json:"number"`
		Title       string    `json:"title"`
		HeadRefName string    `json:"headRefName"`
		BaseRefName string    `json:"baseRefName"`
		HeadRefOid  string    `json:"headRefOid"`
		UpdatedAt   time.Time `json:"updatedAt"`
		Body        string    `json:"body"`
		Labels      []ghLabel `json:"labels"`
		Additions   int       `json:"additions"`
		Deletions   int       `json:"deletions"`
		Files       int       `json:"changedFiles"`
		IsDraft     bool      `json:"isDraft"`
		Closes      []struct {
			Number     int `json:"number"`
			Repository struct {
				Name  string `json:"name"`
				Owner struct {
					Login string `json:"login"`
				} `json:"owner"`
			} `json:"repository"`
		} `json:"closingIssuesReferences"`
	}
	var issues []struct {
		Number    int       `json:"number"`
		Title     string    `json:"title"`
		Labels    []ghLabel `json:"labels"`
		CreatedAt time.Time `json:"createdAt"`
	}
	ctx := context.Background()
	if err := t.gh(ctx, &prs, "pr", "list", "-R", repo, "--state", "open", "--limit", "1000", "--json", "number,title,headRefName,baseRefName,headRefOid,updatedAt,body,labels,additions,deletions,changedFiles,isDraft,closingIssuesReferences"); err != nil {
		return nil, err
	}
	if err := t.gh(ctx, &issues, "issue", "list", "-R", repo, "--state", "open", "--limit", "1000", "--json", "number,title,labels,createdAt"); err != nil {
		return nil, err
	}
	names := func(ls []ghLabel) []string {
		var out []string
		for _, l := range ls {
			out = append(out, l.Name)
		}
		return out
	}
	items := map[int]openItem{}
	for _, p := range prs {
		var linked []string
		for _, c := range p.Closes {
			linked = append(linked, c.Repository.Owner.Login+"/"+c.Repository.Name+"#"+strconv.Itoa(c.Number))
		}
		items[p.Number] = openItem{Title: p.Title, PR: true, Head: p.HeadRefName, Base: p.BaseRefName, Deps: t.dependencies(p.Body, repo), Commit: p.HeadRefOid,
			Issues: t.forIssues(p.Title, p.Body, repo, linked), Labels: names(p.Labels), Updated: p.UpdatedAt,
			Add: p.Additions, Del: p.Deletions, Files: p.Files, Draft: p.IsDraft}
	}
	for _, i := range issues {
		items[i.Number] = openItem{Title: i.Title, Labels: names(i.Labels), Created: i.CreatedAt}
	}
	return items, nil
}

type ghLabel struct {
	Name string `json:"name"`
}

// issueMention is a reference to an issue: its link on GitHub, or [owner/name or name]#n; a
// Markdown link is read by where it leads, not by its text.
var (
	issueMention = regexp.MustCompile(`github\.com/([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)/issues/(\d+)|((?:[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)?)?#\d+)`)
	markdownLink = regexp.MustCompile(`\[[^\]]*\]\(([^)\s]*)\)`)
)

// forIssues are the issues a pull request in repo is for, as owner/name#n: those GitHub links to
// it (linked: its description's "Fixes #n", or linked by hand), then those its title or the first
// line of its description names ("Part of #n"). One in no repository of the team is left out; one
// that turns out to be a pull request, where they are read.
func (t *Team) forIssues(title, body, repo string, linked []string) []string {
	var out []string
	add := func(ref, in string) {
		if r, n, err := t.parseRef(ref, in); err == nil && !slices.Contains(out, r+"#"+strconv.Itoa(n)) {
			out = append(out, r+"#"+strconv.Itoa(n))
		}
	}
	for _, ref := range linked {
		add(ref, "")
	}
	first, _, _ := strings.Cut(strings.TrimSpace(body), "\n")
	for _, m := range issueMention.FindAllStringSubmatch(markdownLink.ReplaceAllString(title+"\n"+first, "$1"), -1) {
		if m[1] != "" {
			add(m[1]+"#"+m[2], "")
		} else {
			add(m[3], repo)
		}
	}
	return out
}

// LeaveNote adds a note to an agent's inbox: a general one, or with ref (owner/name#n, of one
// of its repositories) about that pull request or issue. The agent's running shift gets it
// after its current step; otherwise its next shift reads it, and a note from the human asks
// for that shift, before the agent's interval is up.
func (t *Team) LeaveNote(agent, ref, from, text string) error {
	a, err := t.agent(agent)
	if err != nil {
		return err
	}
	if text = strings.TrimSpace(text); text == "" {
		return nil
	}
	if m := fullRefOnly.FindStringSubmatch(ref); ref != "" && (m == nil || !slices.Contains(a.Repos, m[1])) {
		return fmt.Errorf("%s is not a pull request or issue of %s's repositories", ref, agent)
	}
	if err := t.addNote(agent, ref, from, text, time.Now()); err != nil {
		return err
	}
	if from == "human" && !t.Steerable(agent) { // checked after the note is in: a shift that ends meanwhile wakes the agent itself
		t.wakeForHuman(agent)
	}
	return nil
}

// Steerable says whether the agent's shift is running and takes notes: one left now reaches it
// after its current step.
func (c *Config) Steerable(agent string) bool {
	b, err := os.ReadFile(c.steerFile(agent))
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return err == nil && pid > 0 && syscall.Kill(pid, 0) == nil
}

func (c *Config) steerFile(agent string) string { return c.Path(filepath.Join(steerDir, agent)) }

// noteHeader begins the message that brings a running shift the notes that came meanwhile.
const noteHeader = "New in your inbox during this shift:"

// steerEvery is how often a running shift's inbox is looked at for new notes.
var steerEvery = 2 * time.Second

// steering brings a running shift the notes that arrive in its agent's inbox: each batch goes
// to it as a message, and counts as read once claude echoes it, which it does when the message
// enters the conversation; the inbox then drops it. A batch claude never took stays there for
// the next shift. One with the human's words from the chat begins a talk (talk.go): while it goes
// on, the human's words wait for rota listen, and every other note for the talk's end.
type steering struct {
	t     *Team
	agent string
	talks bool            // a shift on this machine: the human talks with it in the chat (talk.go)
	read  map[string]bool // the notes the shift was briefed with
	mu    sync.Mutex
	sent  []batch // sent, not yet taken
	took  []Note  // read during the shift
}

// batch is notes sent to a shift together, and the text they were sent as.
type batch struct {
	text  string
	notes []Note
}

// watch looks for new notes until ctx ends; send hands a message to the shift.
func (s *steering) watch(ctx context.Context, send func(string) bool) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(steerEvery):
		}
		s.look(send)
	}
}

// look is one look at the inbox, under its lock: a note deleted meanwhile is deleted before it is
// sent, or after; and the talk, which the human's words in the chat begin, and their silence ends.
// The chat says so after the inbox's lock, as everyone writes it.
func (s *steering) look(send func(string) bool) {
	if s.lookLocked(send) {
		s.t.sayQuiet(s.agent)
	}
}

// lookLocked is look under the inbox's lock. It says whether a talk went quiet with no word of the
// human's left for rota listen to bring: those begin another talk at once, and the chat says that.
func (s *steering) lookLocked(send func(string) bool) (quietLine bool) {
	unlock, err := s.t.lockInbox(s.agent)
	if err != nil {
		return false
	}
	defer unlock()
	var tk chatTalk
	if s.talks {
		tk = s.t.loadTalk(s.agent)
	}
	now := time.Now()
	quiet := tk.quiet(now, s.t.talkQuiet())
	if quiet {
		quietLine = len(tk.For) == 0
		tk.end("quiet")
	}
	changed := quiet
	if st := tk.Stop; st != nil { // the human stopped it (chatedit.go): told before anything else reaches it
		tk.Stop, changed = nil, true
		if st.Cut && tk.Open {
			send(stopTurn) // its answer under way stops at once
		}
		send(stopText(*st, tk.Open))
	}
	s.mu.Lock()
	gone := maps.Clone(s.read) // briefed, sent, or taken and on their way out of the inbox
	maps.Copy(gone, ids(s.took))
	for _, b := range s.sent {
		maps.Copy(gone, ids(b.notes))
	}
	maps.Copy(gone, ids(tk.Heard)) // the talk's: brought by rota listen, or waiting for it
	maps.Copy(gone, ids(tk.For))
	var b batch
	for _, n := range s.t.Notes(s.agent) {
		switch {
		case gone[n.ID]:
		case tk.Open && n.Chat: // the human's next words, for rota listen
			tk.For, tk.Last, changed = append(tk.For, n), now, true
		case tk.Open: // a note waits for the talk's end
		default:
			b.notes = append(b.notes, n)
		}
	}
	if len(b.notes) > 0 {
		b.text = s.t.NotesText(b.notes)
		switch chat := len(chatIDs(b.notes)) > 0; {
		case chat && s.talks: // the human's first words: what to do now
			b.text += "\n" + talkBegins + "\n"
		case chat: // on another machine: how to answer them
			b.text += "\n" + chatAway + "\n"
		}
		s.sent = append(s.sent, b) // before it is sent: claude may echo it at once
	}
	s.mu.Unlock()
	if len(b.notes) > 0 {
		if !send(noteHeader + "\n\n" + b.text) {
			s.mu.Lock()
			s.sent = slices.DeleteFunc(s.sent, func(x batch) bool { return x.notes[0].ID == b.notes[0].ID })
			s.mu.Unlock()
		} else if chat := chatIDs(b.notes); s.talks && len(chat) > 0 { // the human talks to it: a talk begins
			tk = chatTalk{Open: true, Since: now, Last: now, Opening: chat, Heard: tk.Heard}
			changed = true
		}
	}
	if changed {
		if err := s.t.saveTalk(s.agent, tk); err != nil {
			fmt.Fprintln(os.Stderr, "warn: the talk with the human is not kept:", err)
		}
	}
	return quietLine
}

// chatIDs are the ids of the human's words from the chat among notes.
func chatIDs(notes []Note) []string {
	var chat []string
	for _, n := range notes {
		if n.Chat {
			chat = append(chat, n.ID)
		}
	}
	return chat
}

// taken is claude's echo of a batch: its notes are read, and leave the inbox. The inbox's lock
// is taken without holding s.mu, which watch takes inside it. The human's first words in a talk,
// taken, start its clock: the agent answers them now.
func (s *steering) taken(text string) {
	s.mu.Lock()
	var read []Note
	for i, b := range s.sent {
		if strings.TrimSpace(b.text) == strings.TrimSpace(text) {
			s.sent, s.took, read = slices.Delete(s.sent, i, i+1), append(s.took, b.notes...), b.notes
			break
		}
	}
	s.mu.Unlock()
	if read == nil {
		return
	}
	if err := s.t.dropNotes(s.agent, ids(read)); err != nil {
		fmt.Fprintln(os.Stderr, "warn: the notes the shift read stay in its inbox:", err)
	}
	if chat := chatIDs(read); s.talks && len(chat) > 0 {
		s.t.changeTalk(s.agent, func(tk *chatTalk) {
			if tk.Open && len(tk.Opening) > 0 {
				tk.Opening, tk.Last = slices.DeleteFunc(tk.Opening, func(id string) bool { return slices.Contains(chat, id) }), time.Now()
			}
		})
	}
}

// during are the notes the shift read while it ran.
func (s *steering) during() []Note {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.took)
}

package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
)

// The human's mail: what the team asks the human, as threads a mail client shows. Each thread is
// made from what rota keeps anyway: a question an agent asked, a pull request that waits for the
// human, the human's message to an agent and its answers, the day's report. What a shift says the
// human should know is no message: the day's report has it. Only the human's own marks are kept for the mail itself, in .mail.json:
// what they read, starred, snoozed, archived or moved, and how they like their inbox.

// Where a thread is.
const (
	BoxInbox   = "inbox"   // it needs the human
	BoxWaiting = "waiting" // the human waits on an agent
	BoxUpdates = "updates" // the day's report: nothing to do
	BoxDone    = "done"    // answered, archived or approved
	BoxOpen    = "open"    // a pull request the agents still work on: listed, not in the inbox
)

// The inbox's order, by group: the human's place in a talk an agent answered, important
// questions, questions, reviews by priority, then unimportant questions; each group
// keeps its own order inside its range.
const (
	rankAnswered = 10_000_000 * (iota + 1)
	rankHigh
	rankAsk
	rankReview
	rankLow
)

// mailKeep is how long what is done stays in the mail: answered questions, read answers.
const mailKeep = 30 * 24 * time.Hour

// reportDays is how many days' reports the mail holds.
const reportDays = 7

// Thread is one conversation about one thing.
type Thread struct {
	ID      string    `json:"id"`
	Kind    string    `json:"kind"` // ask, do (a to-do: steps the human takes), review, digest, or msg (the human's message to an agent)
	Box     string    `json:"box"`
	Subject string    `json:"subject"`
	Repo    string    `json:"repo,omitempty"`  // owner/name
	Ref     string    `json:"ref,omitempty"`   // the pull request or issue it is about, owner/name#n
	Label   string    `json:"label,omitempty"` // the same as the pages show it: creator#673
	At      time.Time `json:"at"`              // its newest message
	Rank    int       `json:"rank"`            // its place among what needs the human, 0 first
	Why     []string  `json:"why,omitempty"`   // why it matters, a word or two each
	Msgs    []Message `json:"msgs"`
	QID     string    `json:"qid,omitempty"` // an open question's id, to answer or dismiss it
	PR      *PRThread `json:"pr,omitempty"`
	// The human's marks.
	Unread bool      `json:"unread,omitempty"`
	Star   bool      `json:"star,omitempty"`
	Snooze time.Time `json:"snooze,omitzero"` // out of sight until then
	Place  string    `json:"place,omitempty"` // focused or other: where the human moved it
	Ticks  []int     `json:"ticks,omitempty"` // the steps of a to-do the human ticked
	Seen   time.Time `json:"seen,omitzero"`   // the human's messages: when the agent read the last, if it read all

	news time.Time // when it last brought the human something new
}

// PRThread is what a pull request's thread says about it.
type PRThread struct {
	State    string   `json:"state"`            // review: it waits for the human; waiting: on an agent's answer to their notes; approved; open: the agents still work on it
	Author   string   `json:"author,omitempty"` // the agent that wrote it
	Number   int      `json:"number"`
	Order    int      `json:"order"` // its place in the order to review them
	Add      int      `json:"add,omitempty"`
	Del      int      `json:"del,omitempty"`
	Files    int      `json:"files,omitempty"`
	Draft    bool     `json:"draft,omitempty"`
	Waits    []string `json:"waits,omitempty"` // the open pull requests it waits for, as agents name them
	Depth    int      `json:"depth,omitempty"` // how far it sits under the one it waits for
	Waiting  int      `json:"waiting,omitempty"`
	Answered bool     `json:"answered,omitempty"` // an agent answered the human's notes since they last opened the review
	Why      string   `json:"why,omitempty"`      // why it comes there in the order to review them, as the agent that ordered them said: a line of the thread, no message
	Labels   []string `json:"labels,omitempty"`   // its own labels, then those of the open issues it is for: what the Reviews folder narrows to
	Marks    []Mark   `json:"marks,omitempty"`    // where it stands, as the console marks it, which narrows the folder too
}

// Message is one message of a thread.
type Message struct {
	From string    `json:"from"` // an agent, "you", or "team": rota itself
	To   string    `json:"to,omitempty"`
	At   time.Time `json:"at"`
	Text string    `json:"text"`          // Markdown
	Log  string    `json:"log,omitempty"` // the transcript of the shift that said it: <date>/<HHMM>-<agent>
	// A question's answers to choose from and the one its agent recommends; a to-do's steps.
	Options   []string `json:"options,omitempty"`
	Recommend string   `json:"recommend,omitempty"`
	Steps     []string `json:"steps,omitempty"` // Markdown, one line each
	Quotes    []Quote  `json:"quotes,omitempty"`
}

// Quote is a review note a message quotes: where it is, what the human wrote, and an agent's answer.
type Quote struct {
	Where  string `json:"where,omitempty"` // file:line; "" for the whole change
	Note   string `json:"note"`            // Markdown
	Answer string `json:"answer,omitempty"`
}

// Member is one of the team as the mail shows it: its face and what it is doing.
type Member struct {
	Name  string `json:"name"`
	State string `json:"state"`         // working, needs (it asks the human something), failed, done (its last run just finished), idle
	Line  string `json:"line"`          // what it does now, or when it runs next
	Why   string `json:"why,omitempty"` // why it does not run now, where its line does not say (WhyWaits); the Team's and its own page only
	Color string `json:"color"`
}

// RepoLabel is one of the team's repositories with the short name the mail gives it.
type RepoLabel struct {
	Repo  string `json:"repo"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

// MailPrefs is how the human likes their inbox.
type MailPrefs struct {
	Split  bool   `json:"split"`  // Focused and Other; off, one inbox
	Focus  int    `json:"focus"`  // how many Focused shows at most
	After  string `json:"after"`  // after an answer or an archive, "next" opens the next one, "list" goes back to the list
	Notify string `json:"notify"` // what the human's devices hear of the mail, which they read when they choose: focused, all or off
	Chat   string `json:"chat"`   // what they hear of the chat, at once: all (what an agent or the Team says them there) or off
}

var defaultMailPrefs = MailPrefs{Split: true, Focus: 5, After: "next", Notify: "focused", Chat: "all"}

// Mail is the human's mail now.
type Mail struct {
	Threads []Thread              `json:"threads"`
	Members []Member              `json:"members"`
	Repos   []RepoLabel           `json:"repos"`
	Prefs   MailPrefs             `json:"prefs"`
	Filters map[string]MailFilter `json:"filters"` // by folder, as MailFilter says
}

// MailFilter is what narrows one folder of the mail (inbox, waiting, starred, snoozed, sent, all, or
// repo:<owner/name>) as the human left it: only unread or starred threads, from one member or the
// team, of one kind, of one repository, of today or the past week. The folder shows it again each
// time the human comes back to it, on every device, until they change it; a search has filters of
// its own, which end with it.
type MailFilter struct {
	Unread bool   `json:"unread,omitempty"`
	Star   bool   `json:"star,omitempty"`
	From   string `json:"from,omitempty"`
	Kind   string `json:"kind,omitempty"`
	Repo   string `json:"repo,omitempty"`
	Date   string `json:"date,omitempty"` // today or week
}

// mailFolders are the folders a MailFilter narrows, besides one repository's (repo:<owner/name>).
var mailFolders = []string{"inbox", "waiting", "starred", "snoozed", "sent", "all"}

func isMailFolder(folder string) bool {
	if slices.Contains(mailFolders, folder) {
		return true
	}
	rest := strings.TrimPrefix(folder, "repo:")
	owner, name, ok := strings.Cut(rest, "/")
	return strings.HasPrefix(folder, "repo:") && ok && owner != "" && name != "" && !strings.ContainsAny(rest, " \t\n") && !strings.Contains(name, "/")
}

// ChangeMailFilter changes what narrows the folder by what change names, under the mail's lock, so
// that what another device chose for it meanwhile stays: unread and star (true or false); from,
// kind, repo and date ("" for any; a date is today or week); clear (true) takes every one off first.
func (c *Config) ChangeMailFilter(folder string, change map[string]any) error {
	if !isMailFolder(folder) {
		return fmt.Errorf("no folder %q to filter", folder)
	}
	if len(change) == 0 {
		return fmt.Errorf("which filter?")
	}
	var failed error
	err := c.changeMail(time.Now(), func(s *mailState) {
		f := s.Filters[folder]
		if clear, _ := change["clear"].(bool); clear {
			f = MailFilter{}
		}
		for k, v := range change {
			switch k {
			case "clear":
			case "unread", "star":
				b, ok := v.(bool)
				if !ok {
					failed = fmt.Errorf("%s is true or false", k)
					return
				}
				if k == "unread" {
					f.Unread = b
				} else {
					f.Star = b
				}
			case "from", "kind", "repo", "date":
				str, ok := v.(string)
				if !ok {
					failed = fmt.Errorf("%s is a word, or none for any", k)
					return
				}
				switch str = strings.TrimSpace(str); k {
				case "from":
					f.From = str
				case "kind":
					f.Kind = str
				case "repo":
					f.Repo = str
				default:
					f.Date = str
				}
			default:
				failed = fmt.Errorf("no filter %q", k)
				return
			}
		}
		if f.Date != "" && f.Date != "today" && f.Date != "week" {
			failed = fmt.Errorf("a date filter is today or week")
			return
		}
		if f == (MailFilter{}) {
			delete(s.Filters, folder)
			return
		}
		if s.Filters == nil {
			s.Filters = map[string]MailFilter{}
		}
		s.Filters[folder] = f
	})
	return errors.Join(failed, err)
}

// mailMark is what the human did to one thread.
type mailMark struct {
	Read   time.Time `json:"read,omitzero"`
	Unread bool      `json:"unread,omitempty"` // marked unread: it stays so until read again
	Star   bool      `json:"star,omitempty"`
	Snooze time.Time `json:"snooze,omitzero"`
	Done   time.Time `json:"done,omitzero"` // archived then: until it brings something new
	Place  string    `json:"place,omitempty"`
	Ticks  []int     `json:"ticks,omitempty"`
	Box    string    `json:"box,omitempty"` // of a pull request: where it was last seen, and since when
	In     time.Time `json:"in,omitzero"`
	Kept   bool      `json:"kept,omitempty"` // moved back to the inbox: an answer the human read stays there until archived
}

// mailState is .mail.json.
type mailState struct {
	Since   time.Time             `json:"since"` // the mail began then: what came before counts as read
	Prefs   *MailPrefs            `json:"prefs,omitempty"`
	Marks   map[string]mailMark   `json:"marks,omitempty"`
	Filters map[string]MailFilter `json:"filters,omitempty"` // what narrows each folder, as the human left it
}

// readMail reads .mail.json: ok is false when it does not exist yet, and err says why one that
// exists could not be read; the human's marks are never written over then.
func (c *Config) readMail() (s mailState, ok bool, err error) {
	s.Marks = map[string]mailMark{}
	b, err := os.ReadFile(c.Path(mailFile))
	if os.IsNotExist(err) {
		return s, false, nil
	}
	if err == nil {
		err = json.Unmarshal(b, &s)
	}
	if err != nil {
		return mailState{Marks: map[string]mailMark{}}, false, fmt.Errorf("%s: %w", mailFile, err)
	}
	if s.Marks == nil {
		s.Marks = map[string]mailMark{}
	}
	return s, true, nil
}

// changeMail changes .mail.json under its lock; one that does not exist yet begins at now.
func (c *Config) changeMail(now time.Time, edit func(*mailState)) error {
	lock, err := os.OpenFile(c.Path(mailFile+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	s, ok, err := c.readMail()
	if err != nil {
		return err
	}
	if !ok {
		s.Since = now
	}
	edit(&s)
	b, _ := json.MarshalIndent(s, "", "  ")
	tmp := c.Path("." + mailFile + ".tmp")
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, c.Path(mailFile))
}

// Mail is the human's mail now: every thread with the human's marks, newest first; the team as
// its members; its repositories; and how the human likes to read it.
func (t *Team) Mail(now time.Time) Mail {
	labels := t.repoLabels()
	threads := t.questionThreads(labels, now)
	threads = append(threads, t.reviewThreads(labels)...)
	threads = append(threads, t.reportThreads(now)...)
	threads = append(threads, t.messageThreads(labels, now)...)
	t.markThreads(threads, now)
	threads = slices.DeleteFunc(threads, func(th Thread) bool { // an answer read, done, goes after mailKeep as any done thread
		return th.Kind == "msg" && th.Box == BoxDone && now.Sub(th.At) > mailKeep
	})
	slices.SortStableFunc(threads, func(a, b Thread) int { return b.At.Compare(a.At) })
	m := Mail{Threads: threads, Members: t.Members(now), Repos: []RepoLabel{}, Prefs: t.mailPrefs(), Filters: map[string]MailFilter{}}
	if s, _, _ := t.readMail(); s.Filters != nil {
		m.Filters = s.Filters
	}
	if m.Members == nil {
		m.Members = []Member{}
	}
	repos := t.teamRepos()
	paint := colorsOf(repos)
	for _, r := range repos {
		m.Repos = append(m.Repos, RepoLabel{r, labels[r], paint[r]})
	}
	return m
}

// markThreads puts the human's marks on the threads: an archived one is done until it brings
// something new, and one back from a snooze is new again. A pull request's thread is new when it
// comes into the inbox, which the mail keeps: GitHub does not say when it began to wait for the
// human. An agent's answer to the human's message is done once they read it. What the mail finds
// when it begins counts as read.
func (t *Team) markThreads(threads []Thread, now time.Time) {
	s, ok, err := t.readMail()
	if !ok {
		s.Since = now
	}
	type move struct{ id, from, to string }
	var moved []move
	for i := range threads {
		th := &threads[i]
		m := s.Marks[th.ID]
		if th.PR != nil {
			switch {
			case m.Box != th.Box:
				moved = append(moved, move{th.ID, m.Box, th.Box})
				if th.Box == BoxInbox && ok {
					th.news = later(th.news, now)
				}
			case th.Box == BoxInbox:
				th.news = later(th.news, m.In)
			}
		}
		th.Star, th.Place, th.Ticks = m.Star, m.Place, m.Ticks
		if m.Snooze.After(now) {
			th.Snooze = m.Snooze
		} else if !m.Snooze.IsZero() {
			th.news = later(th.news, m.Snooze)
		}
		if !m.Done.IsZero() && !th.news.After(m.Done) && th.Box != BoxOpen {
			th.Box = BoxDone
		}
		th.Unread = m.Unread || th.news.After(later(m.Read, s.Since))
		if th.Kind == "msg" && th.Box == BoxInbox && !th.Unread && !m.Kept { // an answer the human read needs nothing more of them
			th.Box = BoxDone
		}
	}
	if err != nil || ok && len(moved) == 0 {
		return
	}
	t.changeMail(now, func(s *mailState) {
		for _, mv := range moved {
			m := s.Marks[mv.id]
			if m.Box != mv.from { // another read saw it move since: that one is newer
				continue
			}
			m.Box, m.In = mv.to, now
			if !ok {
				m.In = time.Time{} // there before the mail began
			}
			s.Marks[mv.id] = m
		}
	})
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// MarkMail does what the human did to a thread: read, unread, star, unstar, archive, unarchive,
// snooze (until arg, RFC 3339), unsnooze, place (arg: focused, other, or "" for where it
// belongs), or tick (arg: a to-do's step, from 0, and on or off: "2:on").
func (c *Config) MarkMail(id, action, arg string, now time.Time) error {
	_, err := c.markMail(id, action, arg, now)
	return err
}

// markMail is MarkMail, which also returns the thread's marks as they were.
func (c *Config) markMail(id, action, arg string, now time.Time) (was mailMark, err error) {
	if id == "" || len(id) > 300 {
		return was, fmt.Errorf("which message?")
	}
	var until time.Time
	switch action {
	case "read", "unread", "star", "unstar", "archive", "unarchive", "unsnooze":
	case "snooze":
		if until, err = time.Parse(time.RFC3339, arg); err != nil || !until.After(now) {
			return was, fmt.Errorf("snooze it until when?")
		}
	case "place":
		if arg != "" && arg != "focused" && arg != "other" {
			return was, fmt.Errorf("move it to Focused or Other")
		}
	case "tick": // arg: the step and what it is to be, "<k>:on" or "<k>:off"
		k, on, _ := strings.Cut(arg, ":")
		if n, err := strconv.Atoi(k); err != nil || n < 0 || n >= stepsMax || on != "on" && on != "off" {
			return was, fmt.Errorf("which step, ticked or not?")
		}
	default:
		return was, fmt.Errorf("unknown action %q", action)
	}
	return was, c.changeMail(now, func(s *mailState) {
		m := s.Marks[id]
		was = m
		was.Ticks = slices.Clone(m.Ticks)
		switch action {
		case "read":
			m.Read, m.Unread = now, false
		case "unread":
			m.Unread = true
		case "star":
			m.Star = true
		case "unstar":
			m.Star = false
		case "archive":
			m.Done = now
		case "unarchive":
			m.Done, m.Kept = time.Time{}, true
		case "snooze":
			m.Snooze = until
		case "unsnooze":
			m.Snooze = time.Time{}
		case "place":
			m.Place = arg
		case "tick":
			k, on, _ := strings.Cut(arg, ":")
			n, _ := strconv.Atoi(k)
			i := slices.Index(m.Ticks, n)
			switch {
			case on == "off" && i >= 0:
				m.Ticks = slices.Delete(m.Ticks, i, i+1)
			case on == "on" && i < 0:
				m.Ticks = append(m.Ticks, n)
				slices.Sort(m.Ticks)
			}
		}
		s.Marks[id] = m
	})
}

// mailPrefs is how the human likes their inbox, as they set it last.
func (c *Config) mailPrefs() MailPrefs {
	if s, _, _ := c.readMail(); s.Prefs != nil {
		p := *s.Prefs
		if p.Chat == "" { // set before the chat had a setting of its own
			p.Chat = defaultMailPrefs.Chat
		}
		return p
	}
	return defaultMailPrefs
}

// Notifies says whether the human's devices hear of something in their mail of importance (high,
// "" or low), as they chose: everything, what Focused shows (all but the unimportant), or nothing.
// The mail is what they read when they choose; the chat, their talks, which only they begin, has a setting of its
// own (NotifiesChat).
func (c *Config) Notifies(importance string) bool {
	switch c.mailPrefs().Notify {
	case "off":
		return false
	case "all":
		return true
	}
	return importance != "low"
}

// NotifiesChat says whether the human's devices hear at once of what an agent or the Team says them
// in the chat, which they have not seen, as they chose.
func (c *Config) NotifiesChat() bool { return c.mailPrefs().Chat != "off" }

// SetMailPrefs keeps how the human likes their inbox.
func (c *Config) SetMailPrefs(p MailPrefs) error {
	if p.Chat == "" { // a page from before the chat had a setting of its own
		p.Chat = c.mailPrefs().Chat
	}
	if err := p.check(); err != nil {
		return err
	}
	return c.changeMail(time.Now(), func(s *mailState) { s.Prefs = &p })
}

// check says why the inbox cannot work so.
func (p MailPrefs) check() error {
	switch {
	case p.Focus < 1 || p.Focus > 20:
		return fmt.Errorf("Focused shows between 1 and 20")
	case p.After != "next" && p.After != "list":
		return fmt.Errorf("after an answer: next or list")
	case p.Notify != "focused" && p.Notify != "all" && p.Notify != "off":
		return fmt.Errorf("notify about focused, all or off")
	case p.Chat != "all" && p.Chat != "off":
		return fmt.Errorf("notify about the chat: all or off")
	}
	return nil
}

// NoteRef is the pull request or issue a note to agent is about, owner/name#n, from what the
// human wrote with it: "#673" in the agent's only repository, "creator#673" or
// "o/shop-creator#673"; "" when they wrote none.
func (t *Team) NoteRef(agent, about string) (string, error) {
	about = strings.TrimSpace(about)
	if about == "" {
		return "", nil
	}
	in := ""
	if a := t.Agent(agent); a != nil && len(a.Repos) == 1 {
		in = a.Repos[0]
	}
	repo, n, err := t.parseRef(about, in)
	if err != nil {
		return "", err
	}
	return repo + "#" + strconv.Itoa(n), nil
}

// ---- the threads ----

// questionThreads are the agents' questions: open ones wait in the inbox, oldest first, an
// important one before the others and an unimportant one after every review; closed ones are
// done, with their answer, for mailKeep.
func (t *Team) questionThreads(labels map[string]string, now time.Time) []Thread {
	var out []Thread
	group := map[string]int{"high": rankHigh, "": rankAsk, "low": rankLow}
	for _, q := range t.Questions() {
		th := t.questionThread(q, labels)
		th.Box, th.Rank, th.QID, th.news = BoxInbox, group[q.Importance], q.ID, q.Asked
		group[q.Importance]++ // oldest first within its importance
		out = append(out, th)
	}
	dir := filepath.Join(t.Path(questionsDir), closedDir)
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		fi, err := e.Info() // closing a question writes it: older than the mail keeps, it is not read
		if err != nil || !strings.HasSuffix(e.Name(), ".json") || now.Sub(fi.ModTime()) > mailKeep {
			continue
		}
		f := filepath.Join(dir, e.Name())
		var q Question
		if b, err := os.ReadFile(f); err != nil || json.Unmarshal(b, &q) != nil {
			continue
		}
		q.ID = strings.TrimSuffix(filepath.Base(f), ".json")
		th := t.questionThread(q, labels)
		th.Box = BoxDone
		switch {
		case !q.Answered.IsZero():
			th.Msgs = append(th.Msgs, Message{From: "you", To: q.Agent, At: q.Answered, Text: q.Answer})
		case !q.Dismissed.IsZero():
			th.Msgs = append(th.Msgs, Message{From: "you", To: q.Agent, At: q.Dismissed, Text: "_Dismissed without an answer._"})
		case q.Settled != "":
			th.Msgs = append(th.Msgs, Message{From: "team", At: q.Withdrawn, Text: "_Closed unanswered: " + q.Settled + "._"})
		case !q.Withdrawn.IsZero():
			th.Msgs = append(th.Msgs, Message{From: q.Agent, At: q.Withdrawn, Text: "_No longer needs an answer._"})
		}
		th.At = th.Msgs[len(th.Msgs)-1].At
		if now.Sub(th.At) > mailKeep {
			continue
		}
		out = append(out, th)
	}
	return out
}

// refPrefix is the "<repository>#<n>: " a question about a pull request or issue starts with.
var refPrefix = regexp.MustCompile(`^([A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)?)#(\d+):\s*`)

func (t *Team) questionThread(q Question, labels map[string]string) Thread {
	th := Thread{ID: "q:" + q.ID, Kind: "ask", Subject: q.Subject, At: q.Asked,
		Msgs: []Message{{From: q.Agent, To: "you", At: q.Asked, Text: q.Text, Options: q.Options, Recommend: q.Recommend, Steps: q.Steps}}}
	if th.Subject == "" {
		th.Subject = subjectOf(refPrefix.ReplaceAllString(q.Text, ""))
	}
	if len(q.Steps) > 0 {
		th.Kind = "do"
	}
	if q.Importance == "high" {
		th.Why = []string{"High"}
	}
	if ref := q.ref(); ref != "" {
		th.Ref = ref
	} else if m := refPrefix.FindStringSubmatch(q.Text); m != nil {
		if a := t.Agent(q.Agent); a != nil {
			if repo, ok := resolveRepo(a.Repos, m[1]); ok {
				th.Ref = repo + "#" + m[2]
			}
		}
	}
	th.Repo, th.Label = refLabel(th.Ref, labels)
	return th
}

// refLabel is the repository of owner/name#n and how the mail shows it: creator#673.
func refLabel(ref string, labels map[string]string) (repo, label string) {
	repo, n, ok := strings.Cut(ref, "#")
	if !ok {
		return "", ""
	}
	name := labels[repo]
	if name == "" {
		name = repo[strings.LastIndex(repo, "/")+1:]
	}
	return repo, name + "#" + n
}

// reviewThreads are the team's open pull requests, in the order the human reviews them: one
// that waits for the human is in the inbox, one whose notes wait on an agent in Waiting, an
// approved one is done, and one the agents still work on is only listed. One an agent answered
// the human's notes on comes first.
func (t *Team) reviewThreads(labels map[string]string) []Thread {
	q, open := t.ReviewQueue(), t.openWork()
	forYou, refs := map[string]bool{}, map[string]string{}
	for _, p := range q.ForYou(t.ReviewLabel) {
		forYou[p.Ref()] = true
	}
	for _, p := range q.PRs {
		refs[p.Name] = p.Ref()
	}
	marks := map[string]Label{} // the team's labels that mean something: those with an emoji
	for _, l := range t.Labels() {
		if l.Emoji != "" && !strings.EqualFold(l.Name, t.ReviewLabel) && l.Name != ApprovedLabel {
			marks[strings.ToLower(l.Name)] = l
		}
	}
	var out []Thread
	for i, p := range q.PRs {
		it := open.Repos[p.Repo][p.Number]
		pr := &PRThread{Number: p.Number, Order: i, Add: it.Add, Del: it.Del, Files: it.Files, Draft: it.Draft, Depth: p.Depth,
			Waiting: it.Talk.Waiting, Answered: p.answered, Labels: p.Labels, Marks: p.Marks}
		for _, w := range p.Waits {
			if _, label := refLabel(refs[w], labels); label != "" {
				w = label
			}
			pr.Waits = append(pr.Waits, w)
		}
		if a, _, ok := strings.Cut(it.Head, "/"); ok && t.Agent(a) != nil {
			pr.Author = a
		}
		switch {
		case forYou[p.Ref()]:
			pr.State = "review"
		case p.waits:
			pr.State = "waiting"
		case hasLabel(p.own, ApprovedLabel):
			pr.State = "approved"
		default:
			pr.State = "open"
		}
		th := Thread{ID: "pr:" + p.Ref(), Kind: "review", Subject: prSubject(p.Title), Ref: p.Ref(), Rank: rankReview + i, PR: pr, At: it.Updated}
		th.Repo, th.Label = refLabel(th.Ref, labels)
		th.Box = map[string]string{"review": BoxInbox, "waiting": BoxWaiting, "approved": BoxDone}[pr.State]
		if th.Box == "" {
			th.Box = BoxOpen
		}
		from := pr.Author
		if from == "" {
			from = "team"
		}
		say := map[string]string{
			"review": "Ready for your review.", "waiting": "Your notes wait for an answer.", "approved": "You approved it: it merges once nothing holds it back.",
			"open": "Not ready for your review yet."}[pr.State]
		if it.Draft { // an agent works on it
			switch pr.State {
			case "open":
				say = "A draft: being worked on, or parked. It comes to your review once it is ready."
			case "approved":
				say = "You approved it. It is a draft again, and merges once it is ready."
			}
		}
		pr.Why = p.Why // ordering them again, as the agent that orders them does every shift, brings the human nothing new
		talked := talkMessages(it.Talk, pr.Author)
		slices.SortStableFunc(talked, func(a, b Message) int { return a.At.Compare(b.At) })
		th.Msgs = append([]Message{{From: from, To: "you", At: it.Updated, Text: say}}, talked...) // where it stands first: never after an answer, which shows open as the last
		if p.answered {
			if len(it.Talk.Notes) == 0 { // read before the talk was kept: its latest answer
				th.Msgs = append(th.Msgs, Message{From: it.Talk.By, To: "you", At: it.Talk.Answered, Text: it.Talk.Answer})
			}
			th.news, th.Rank = it.Talk.Answered, rankAnswered+i
		}
		for _, name := range it.Labels {
			if l, ok := marks[strings.ToLower(name)]; ok && len(th.Why) < 2 {
				th.Why = append(th.Why, l.Emoji+" "+strings.NewReplacer("-", " ", ":", " ").Replace(strings.TrimPrefix(l.Name, "rota:")))
			}
		}
		th.At = later(th.At, th.Msgs[len(th.Msgs)-1].At)
		out = append(out, th)
	}
	return out
}

// talkMessages are a pull request's talk as messages: the human's notes sent together as one, an
// agent's answers given together as one, each quoting the notes; and the human's approval.
func talkMessages(k talk, author string) []Message {
	var out []Message
	for i := 0; i < len(k.Notes); {
		n, j := k.Notes[i], i
		for j < len(k.Notes) && k.Notes[j].By == n.By && k.Notes[j].At.Sub(n.At) < 10*time.Minute {
			j++
		}
		group := k.Notes[i:j]
		i = j
		if n.By == "" || n.Person {
			from := "you"
			if n.Person {
				from = n.By
			}
			m := Message{From: from, To: author, At: n.At, Text: "Sent " + plural(len(group), "note") + "."}
			for _, g := range group {
				m.Quotes = append(m.Quotes, Quote{Where: g.Where, Note: g.Text})
			}
			out = append(out, m)
			continue
		}
		m := Message{From: n.By, To: "you", At: group[len(group)-1].At, Text: "Answered " + plural(len(group), "note") + "."}
		for _, g := range group {
			m.Quotes = append(m.Quotes, Quote{Where: g.Where, Note: g.Re, Answer: g.Text})
		}
		out = append(out, m)
	}
	if !k.Approved.IsZero() {
		from := "you"
		if k.ApprovedBy != "" {
			from = k.ApprovedBy
		}
		out = append(out, Message{From: from, To: author, At: k.Approved, Text: "Approved."})
	}
	return out
}

// commitType is the "fix(studio): " a pull request's title may start with: the mail leaves it out.
var commitType = regexp.MustCompile(`^[A-Za-z]+(\([^)]*\))?!?:\s+`)

// prSubject is a pull request's title as its thread's subject: without its commit type, starting
// with a capital.
func prSubject(title string) string {
	s := commitType.ReplaceAllString(strings.TrimSpace(title), "")
	if s == "" {
		return title
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// messageThreads are the threads of the human's messages to agents: one waits while its agent
// has not read one of the human's messages since its last answer, comes into the inbox when the
// agent answers, until the human read it (markThreads), and is otherwise only sent. One whose only message was taken back is gone. Seen
// is when the agent read the human's last message, if it read all of them.
func (t *Team) messageThreads(labels map[string]string, now time.Time) []Thread {
	var out []Thread
	for i, m := range t.MessageThreads() {
		if !m.Gone.IsZero() {
			continue
		}
		th := Thread{ID: "m:" + m.ID, Kind: "msg", Subject: subjectOf(m.Said[0].Text), Ref: m.About}
		unread := false
		for _, said := range m.Said {
			to := m.To
			if said.From != "you" {
				to, unread = "you", false
			} else {
				th.Seen, unread = said.Read, unread || said.Read.IsZero()
			}
			th.Msgs = append(th.Msgs, Message{From: said.From, To: to, At: said.At, Text: said.Text})
		}
		if unread {
			th.Seen = time.Time{}
		}
		last := m.Said[len(m.Said)-1]
		th.At = last.At
		switch {
		case last.From != "you":
			th.Box, th.news, th.Rank = BoxInbox, last.At, rankAnswered+i
		case unread:
			th.Box = BoxWaiting
		default:
			th.Box = BoxDone
		}
		if th.Box == BoxDone && now.Sub(th.At) > mailKeep {
			continue
		}
		th.Repo, th.Label = refLabel(m.About, labels)
		out = append(out, th)
	}
	return out
}

// reportThreads are the day's report and those of the days before, each a message from the team.
func (t *Team) reportThreads(now time.Time) []Thread {
	var out []Thread
	for i, f := range t.Reports() {
		if i == reportDays {
			break
		}
		fi, err := os.Stat(f)
		if err != nil {
			continue
		}
		day := strings.TrimSuffix(filepath.Base(f), ".md")
		text := strings.TrimSpace(readFile(f))
		title := day
		if strings.HasPrefix(text, "# ") {
			line, rest, _ := strings.Cut(text, "\n")
			title, text = strings.TrimSpace(strings.TrimPrefix(line, "# ")), strings.TrimSpace(rest)
		}
		subject := "Your team on " + title
		if day == now.In(t.Zone.Location).Format("2006-01-02") {
			subject = "Your team today"
		}
		out = append(out, Thread{ID: "d:" + day, Kind: "digest", Box: BoxUpdates, Subject: subject, At: fi.ModTime(), news: fi.ModTime(),
			Msgs: []Message{{From: "team", To: "you", At: fi.ModTime(), Text: text}}})
	}
	return out
}

// Members are the team as the mail shows them, the one working first: as the running organizer
// last saw it, which costs nothing to read; with no organizer, nothing runs, and an agent is only
// asking the human something or idle.
func (t *Team) Members(now time.Time) []Member { return t.members(t.Live(), now, nil) }

// MembersNow are the team as the Team's page and an agent's show them: by the team as it is now,
// st, each that does not work with why it does not run now.
func (t *Team) MembersNow(st Status, now time.Time) []Member {
	return t.members(&st, now, t.WhyWaits(st))
}

func (t *Team) members(st *Status, now time.Time, why map[string]string) []Member {
	asks := map[string]int{}
	for _, q := range t.Questions() {
		asks[q.Agent]++
	}
	var names []string
	for _, a := range t.Agents {
		names = append(names, a.Name)
	}
	paint := colorsOf(names)
	if st == nil || st.Pid == 0 {
		st = &Status{}
		for _, a := range t.Agents {
			st.Agents = append(st.Agents, AgentStatus{Name: a.Name, Enabled: a.Enabled, State: "not running"})
		}
	}
	var out []Member
	for _, as := range st.Agents {
		m := Member{Name: as.Name, State: "idle", Color: paint[as.Name], Why: why[as.Name]}
		switch {
		case st.Running != nil && st.Running.Agent == as.Name:
			m.State, m.Line = "working", "Working since "+st.Running.Since.Local().Format("15:04") // not its every step: the mail stays the same while it works
		case asks[as.Name] > 0:
			m.State, m.Line = "needs", fmt.Sprintf("Asks you %d %s", asks[as.Name], map[bool]string{true: "thing", false: "things"}[asks[as.Name] == 1])
		case !as.Enabled:
			m.Line = "Off"
		case as.Paused:
			m.Line = "Paused"
		case as.State == "not running":
			m.Line = "The team is not running"
		case strings.HasPrefix(as.State, "retry") || strings.HasPrefix(as.State, "failed"):
			m.State, m.Line = "failed", "Its last run failed: "+as.State
		case as.LastStatus == "ok" && now.Sub(as.LastRun) < time.Hour:
			m.State, m.Line = "done", "Finished at "+as.LastRun.Local().Format("15:04")
		case strings.HasPrefix(as.State, "ready"):
			m.Line = "Ready to run"
		case !as.NextRun.IsZero() && as.NextRun.Before(now): // the organizer's last look is older: it came due since
			m.Line = "Due since " + as.NextRun.Local().Format("15:04")
		default:
			m.Line = "Next run " + as.NextText(st.NextTick)
		}
		out = append(out, m)
	}
	return out
}

// repoLabels are the names the mail gives the team's repositories: each its own name, as GitHub
// has it, with its owner only when another of the team's has the same name. Nothing is guessed:
// any team's repositories, however named, keep their names.
func (t *Team) repoLabels() map[string]string {
	repos := t.teamRepos()
	out := map[string]string{}
	for _, r := range repos {
		out[r] = shortRef(repos, r)
	}
	return out
}

// colors are the faces' colours.
var colors = []string{"#5b5bd6", "#0f8a7e", "#14894a", "#c2417f", "#b8650a", "#c03b3b", "#7a5a3a", "#0e86a8", "#d9661a", "#5f6b7a", "#2f7d5b", "#8e44ad"}

// colorsOf gives each name its colour: the one its name hashes to, or the next one no other name
// has, in the order of the names sorted; a name keeps its colour as long as no other takes it
// first.
func colorsOf(names []string) map[string]string {
	sorted := slices.Clone(names)
	slices.Sort(sorted)
	out, taken := map[string]string{}, map[int]bool{}
	for _, n := range sorted {
		h := fnv.New32a()
		h.Write([]byte(n))
		i := int(h.Sum32() % uint32(len(colors)))
		for k := 0; k < len(colors) && taken[i]; k++ {
			i = (i + 1) % len(colors)
		}
		taken[i], out[n] = true, colors[i]
		if len(taken) == len(colors) {
			clear(taken)
		}
	}
	return out
}

var (
	mdLink  = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	mdNoise = regexp.MustCompile("[*_`>]+|^#+ ")
	spaces  = regexp.MustCompile(`\s+`)
)

// subjectOf is a message's subject: its first sentence, plain, at most about 90 characters.
func subjectOf(text string) string {
	s := strings.TrimSpace(text)
	if line, _, ok := strings.Cut(s, "\n"); ok {
		s = line
	}
	s = mdLink.ReplaceAllString(s, "$1")
	s = strings.TrimSpace(strings.TrimLeft(mdNoise.ReplaceAllString(s, ""), "-•· "))
	s = spaces.ReplaceAllString(s, " ")
	for i := 0; i < len(s)-1; i++ {
		if (s[i] == '.' || s[i] == '?' || s[i] == '!') && s[i+1] == ' ' && i > 12 {
			if s[i] == '.' {
				s = s[:i]
			} else {
				s = s[:i+1]
			}
			break
		}
	}
	if !strings.HasSuffix(s, "..") {
		s = strings.TrimSuffix(s, ".") // a subject ends without a full stop
	}
	if r := []rune(s); len(r) > 0 && unicode.IsLower(r[0]) {
		r[0] = unicode.ToUpper(r[0]) // and starts with a capital
		s = string(r)
	}
	if r := []rune(s); len(r) > 90 {
		cut := 88
		for cut > 40 && r[cut] != ' ' {
			cut--
		}
		if cut <= 40 {
			cut = 88
		}
		s = strings.TrimRight(string(r[:cut]), ",;: ") + "…"
	}
	return s
}

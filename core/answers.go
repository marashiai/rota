package core

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"rota/gloss/review"
)

// Who waits on whom over a pull request's notes, as the Reviews tab marks it: ⏳ while a note the
// human sent waits on an agent's answer, 💬 once an agent answered one after the human last opened
// the pull request's review, which also tells the human's phone and Mac. The notes are GitHub's,
// read with gloss when GitHub says the pull request changed; rota keeps only when the human last
// opened each review.

const (
	WaitsMark    = "⏳"
	AnsweredMark = "💬"
	waitsName    = "waits on an agent"
	answeredName = "answered"
	waitsText    = "You sent notes an agent has not answered yet: you wait on it"
	answeredText = "An agent answered your notes since you last opened the review: it waits on you"
)

// readSlots is how many pull requests' notes this process reads from GitHub at once.
var readSlots = make(chan struct{}, 4)

// talk is where a pull request's notes stand, as gloss last read them.
type talk struct {
	Read       time.Time  `json:"read,omitzero"`         // the pull request's updatedAt when they were read; zero: never
	Waiting    int        `json:"waiting,omitempty"`     // threads whose last note the human sent
	Answered   time.Time  `json:"answered,omitzero"`     // an agent's latest note after one the human sent
	By         string     `json:"by,omitempty"`          // that agent
	Answer     string     `json:"answer,omitempty"`      // that note's first line
	Notes      []talkNote `json:"notes,omitempty"`       // the talk itself, oldest first: the human's notes and the agents' answers, the last talkNotes
	Approved   time.Time  `json:"approved,omitzero"`     // when someone approved it, as gloss says
	ApprovedBy string     `json:"approved_by,omitempty"` // who, when not the human: another person on GitHub
}

// talkNote is one note of a pull request's talk, as the mail shows it.
type talkNote struct {
	At     time.Time `json:"at"`
	By     string    `json:"by,omitempty"`     // an agent, or another person on GitHub (Person); "" for the human
	Person bool      `json:"person,omitempty"` // By is a person, not an agent
	Where  string    `json:"where,omitempty"`  // the file and line it is on; "" for the whole change or a commit
	Text   string    `json:"text"`             // at most noteChars
	Re     string    `json:"re,omitempty"`     // an agent's: the human's note it answers, at most reChars
}

// How much of a pull request's talk is kept for the mail.
const (
	talkNotes = 40
	noteChars = 600
	reChars   = 160
)

// cut is s at most n bytes, on a character, marked when cut.
func cut(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return strings.TrimSpace(s[:n]) + "…"
}

// talkOf reads where a review's notes stand, as gloss tells who waits: a thread waits on an agent
// while it is open and its last note is one the human sent; drafts are not sent yet.
func talkOf(rv *review.Review) talk {
	var k talk
	for _, th := range rv.Threads {
		asked := false
		var last *review.Note
		where := ""
		if th.Path != "" {
			where = th.Path
			if th.Line > 0 {
				where += ":" + strconv.Itoa(th.Line)
			}
		}
		re := ""
		for _, n := range th.Notes {
			if n.Draft {
				continue
			}
			last = n
			if n.AsksAgents() {
				asked = true
				re = cut(n.Text, reChars)
				note := talkNote{At: n.Created, Where: where, Text: cut(n.Text, noteChars)}
				if !rv.Mine(n) { // a person on GitHub, not the one rota acts for
					note.By, note.Person = n.Account, true
				}
				k.Notes = append(k.Notes, note)
			} else if n.Named && asked {
				if n.Created.After(k.Answered) {
					k.Answered, k.By, k.Answer = n.Created, n.Author, FirstLine(n.Text, 200)
				}
				k.Notes = append(k.Notes, talkNote{At: n.Created, By: n.Author, Where: where, Text: cut(n.Text, noteChars), Re: re})
			}
		}
		if last != nil && !th.Resolved && last.AsksAgents() {
			k.Waiting++
		}
	}
	slices.SortStableFunc(k.Notes, func(a, b talkNote) int { return a.At.Compare(b.At) })
	if len(k.Notes) > talkNotes {
		k.Notes = k.Notes[len(k.Notes)-talkNotes:]
	}
	if rv.Approval != nil {
		k.Approved = rv.Approval.At
		if rv.Approval.By != rv.Viewer {
			k.ApprovedBy = rv.Approval.By
		}
	}
	return k
}

// reviewLoader reads a pull request's notes as the human: gh's login on this machine. Tests
// replace it.
var reviewLoader = func() (func(context.Context, review.Ref) (*review.Review, error), error) {
	c, err := ReviewClient()
	if err != nil {
		return nil, err
	}
	return c.Load, nil
}

// readTalk gives repo's freshly listed pull requests their talk: the last listing's while GitHub
// says one has not changed since, else read again; a failed read keeps the last. It returns the
// answers that came since the last read and after the human last opened the review, to tell them;
// a pull request's first read tells nothing, though the Reviews tab marks what it found unseen.
func (t *Team) readTalk(repo string, items, old map[int]openItem, load func(context.Context, review.Ref) (*review.Review, error)) []Answer {
	owner, name, _ := strings.Cut(repo, "/")
	type pr struct {
		n  int
		it openItem
	}
	var changed []pr
	for n, it := range items {
		if !it.PR {
			continue
		}
		it.Talk = old[n].Talk
		items[n] = it
		if load != nil && (it.Talk.Read.IsZero() || !it.Talk.Read.Equal(it.Updated)) {
			changed = append(changed, pr{n, it})
		}
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	var news []Answer
	for _, c := range changed { // items is not read meanwhile: the reads write it
		n, it := c.n, c.it
		wg.Add(1)
		go func() {
			defer wg.Done()
			readSlots <- struct{}{}
			defer func() { <-readSlots }()
			ctx, cancel := context.WithTimeout(context.Background(), ghTimeout)
			defer cancel()
			rv, err := load(ctx, review.Ref{Owner: owner, Name: name, Number: n})
			if err != nil {
				return
			}
			last, k := it.Talk, talkOf(rv)
			k.Read = it.Updated
			if !last.Read.IsZero() && k.Answered.After(last.Answered) && k.Answered.After(t.seenReviews()[repo+"#"+strconv.Itoa(n)]) {
				mu.Lock()
				news = append(news, Answer{Repo: repo, Number: n, Title: it.Title, By: k.By, Text: k.Answer, At: k.Answered})
				mu.Unlock()
			}
			it.Talk = k
			mu.Lock()
			items[n] = it
			mu.Unlock()
		}()
	}
	wg.Wait()
	return news
}

// tellAnswers tells the human's phone that agents answered their notes; the Mac's menu sees them
// in the Reviews tab's list on its own.
func (c *Config) tellAnswers(news []Answer) {
	if len(news) == 0 || len(c.PushSubscriptions()) == 0 || !c.Notifies("") {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c.PushAll(ctx)
}

// Answer is an agent's answer to the human's notes on an open pull request, after they last
// opened its review.
type Answer struct {
	Repo   string    `json:"repo"`
	Number int       `json:"number"`
	Name   string    `json:"name"` // as agents name it: <name>#<n>
	Title  string    `json:"title"`
	By     string    `json:"agent"`
	Text   string    `json:"text"` // its first line
	At     time.Time `json:"at"`
}

// Ref is owner/name#n.
func (a Answer) Ref() string { return a.Repo + "#" + strconv.Itoa(a.Number) }

// Page is the pull request's review in the pages.
func (a Answer) Page() string { return ReviewPage(a.Repo, a.Number) }

// ReviewPage is a pull request's review in the pages.
func ReviewPage(repo string, number int) string {
	return "/review/" + repo + "/" + strconv.Itoa(number) + "/"
}

// Answers are the agents' answers the human has not seen, one per pull request, oldest first.
func (t *Team) Answers() []Answer {
	open := t.openWork() // before what was seen: a first read writes that first
	repos, seen := t.teamRepos(), t.seenReviews()
	var out []Answer
	for repo, items := range open.Repos {
		for n, it := range items {
			k := it.Talk
			if it.PR && k.Answered.After(seen[repo+"#"+strconv.Itoa(n)]) {
				out = append(out, Answer{Repo: repo, Number: n, Name: shortRef(repos, repo) + "#" + strconv.Itoa(n), Title: it.Title, By: k.By, Text: k.Answer, At: k.Answered})
			}
		}
	}
	slices.SortFunc(out, func(a, b Answer) int { return a.At.Compare(b.At) })
	return out
}

// SeenReview records that the human saw a pull request's notes as they were at a time: when they
// opened its review. An earlier time than the one recorded changes nothing.
func (t *Team) SeenReview(repo string, number int, at time.Time) error {
	lock, err := os.OpenFile(t.Path(seenFile+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	seen := t.seenReviews()
	ref := repo + "#" + strconv.Itoa(number)
	if !at.After(seen[ref]) {
		return nil
	}
	seen[ref] = at
	b, _ := json.MarshalIndent(seen, "", "  ")
	tmp := t.Path("." + seenFile + ".tmp")
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, t.Path(seenFile))
}

// seenReviews is when the human last opened each pull request's review, by owner/name#n.
func (c *Config) seenReviews() map[string]time.Time {
	seen := map[string]time.Time{}
	if b, err := os.ReadFile(c.Path(seenFile)); err == nil {
		json.Unmarshal(b, &seen)
	}
	return seen
}

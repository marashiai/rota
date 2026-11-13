package review

import (
	"sort"
	"strings"
	"time"
)

// Review is a pull request with its commits and notes, as the account gloss runs as sees it.
type Review struct {
	Ref       Ref       `json:"-"`
	PR        string    `json:"pr"` // owner/name#N
	ID        string    `json:"id"` // the pull request's node id
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	URL       string    `json:"url"`
	State     string    `json:"state"`  // OPEN, CLOSED or MERGED
	Author    string    `json:"author"` // who opened it
	Viewer    string    `json:"viewer"` // the account gloss runs as
	Base      string    `json:"base"`
	Head      string    `json:"head"`
	Commits   []Commit  `json:"commits"`
	Threads   []*Thread `json:"threads"`
	Approval  *Approval `json:"approval,omitempty"`  // the latest person's review approves; nil when it does not
	Truncated bool      `json:"truncated,omitempty"` // more than GitHub's first 100 of something

	pending  string    // the viewer's pending review, "" when it has none
	sections []section // that review's body
}

// Approval is a person's word that the pull request may be merged: gloss's Approve, a section of
// the review's body (GitHub lets no one approve their own pull request, and agents share their
// reviewer's account), or GitHub's own approval of someone else's pull request; either only from
// the repository's owner, members and collaborators. A later review by a person that does not
// approve takes it back.
type Approval struct {
	By string    `json:"by"`
	At time.Time `json:"at"`
}

// Approved says whether a person approved the pull request.
func (rv *Review) Approved() bool { return rv.Approval != nil }

// Commit is one commit of the pull request.
type Commit struct {
	SHA     string    `json:"sha"`
	Title   string    `json:"title"`
	Message string    `json:"message,omitempty"`
	Author  string    `json:"author"`
	Date    time.Time `json:"date"`
}

// Short is the commit's first seven characters.
func (c Commit) Short() string { return c.SHA[:min(7, len(c.SHA))] }

// Thread is a note and its replies, all at one place: a line or a file in the whole change's
// diff (Commit "") or in one commit's own, a whole commit (Path ""), or the whole change (both
// empty).
type Thread struct {
	ID       string  `json:"id"` // the first note's
	Commit   string  `json:"commit,omitempty"`
	Path     string  `json:"path,omitempty"`
	Line     int     `json:"line,omitempty"`      // in that diff as it was written; 0: the whole file
	Old      bool    `json:"old,omitempty"`       // Line is on the old side: a deleted line
	Head     string  `json:"head,omitempty"`      // the head when a note on the whole change's diff was written
	HeadLine int     `json:"head_line,omitempty"` // where GitHub tracks it to in the diff from the base to the head now; 0 when it cannot
	HeadOld  bool    `json:"head_old,omitempty"`
	Outdated bool    `json:"outdated,omitempty"` // a later commit changed its line
	Resolved bool    `json:"resolved,omitempty"`
	Notes    []*Note `json:"notes"`

	ghThread string // the review thread's node id: "" for notes GitHub does not thread
	topDB    int64  // the first comment's database id, for REST replies
}

// Note is one note or reply.
type Note struct {
	ID      string    `json:"id"`
	Author  string    `json:"author"`          // the name in its marker, else its account
	Named   bool      `json:"named,omitempty"` // Author came from the marker (an agent)
	Account string    `json:"account"`
	Text    string    `json:"text"`
	Created time.Time `json:"created"`
	Draft   bool      `json:"draft,omitempty"` // the viewer's, not sent yet
	URL     string    `json:"url,omitempty"`
	// PRComment says it is a PR comment (GitHub's Conversation tab): bots and tools that share the
	// reviewer's account write those, so one is never surely the reviewer's own.
	PRComment bool `json:"pr_comment,omitempty"`

	kind noteKind
	meta Meta // its marker, kept when the text is edited
}

type noteKind int

const (
	inComment noteKind = iota // a review comment, in a thread GitHub keeps
	inSection                 // a section of a review's body
	inIssue                   // a PR comment
)

// Mine says whether the reviewer (the viewer, under no other name) wrote it.
func (rv *Review) Mine(n *Note) bool { return !n.Named && n.Account == rv.Viewer }

// AsksAgents says whether n waits on an agent's answer: a person sent it in a review (a note on
// a line, a file or a commit, a section of a review's body, or a reply there). Agents' notes do
// not, nor PR comments: bots and tools that share the reviewer's account write those.
func (n *Note) AsksAgents() bool { return !n.Named && !n.Draft && n.kind != inIssue }

// Last is the thread's latest note.
func (t *Thread) Last() *Note { return t.Notes[len(t.Notes)-1] }

// Drafts counts the viewer's unsent notes.
func (rv *Review) Drafts() (n int) {
	for _, t := range rv.Threads {
		for _, x := range t.Notes {
			if x.Draft {
				n++
			}
		}
	}
	return n
}

// Commit returns the commit whose SHA starts with sha, or that is number n (1-based) when sha is
// a small number.
func (rv *Review) Commit(sha string) (int, bool) {
	sha = strings.TrimSpace(sha)
	if n := atoi(sha); n >= 1 && n <= len(rv.Commits) && len(sha) <= 3 {
		return n - 1, true
	}
	if len(sha) < 4 {
		return 0, false
	}
	for i, c := range rv.Commits {
		if strings.HasPrefix(c.SHA, sha) {
			return i, true
		}
	}
	return 0, false
}

// Thread finds the thread holding the note with that id.
func (rv *Review) Thread(noteID string) (*Thread, *Note) {
	for _, t := range rv.Threads {
		for _, n := range t.Notes {
			if n.ID == noteID {
				return t, n
			}
		}
	}
	return nil, nil
}

// sortThreads orders the threads: the whole change first, then commit by commit, file by file,
// line by line.
func (rv *Review) sortThreads() {
	idx := map[string]int{}
	for i, c := range rv.Commits {
		idx[c.SHA] = i + 1
	}
	for _, t := range rv.Threads {
		replies := t.Notes[1:] // the first note stays first; drafts come after what was sent
		sort.SliceStable(replies, func(i, j int) bool {
			a, b := replies[i], replies[j]
			if a.Draft != b.Draft {
				return !a.Draft
			}
			return a.Created.Before(b.Created)
		})
	}
	sort.SliceStable(rv.Threads, func(i, j int) bool {
		a, b := rv.Threads[i], rv.Threads[j]
		if idx[a.Commit] != idx[b.Commit] {
			return idx[a.Commit] < idx[b.Commit]
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Notes[0].Created.Before(b.Notes[0].Created)
	})
}

// Clone is a deep copy: one goroutine may change a review while another reads its copy.
func (rv *Review) Clone() *Review {
	c := *rv
	if rv.Approval != nil {
		a := *rv.Approval
		c.Approval = &a
	}
	c.Commits = append([]Commit{}, rv.Commits...)
	c.sections = append([]section(nil), rv.sections...)
	c.Threads = make([]*Thread, len(rv.Threads))
	for i, t := range rv.Threads {
		tc := *t
		tc.Notes = make([]*Note, len(t.Notes))
		for j, n := range t.Notes {
			nc := *n
			tc.Notes[j] = &nc
		}
		c.Threads[i] = &tc
	}
	return &c
}

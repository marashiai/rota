package review

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

const loadQuery = `query($owner:String!,$name:String!,$number:Int!){
  viewer{login}
  repository(owner:$owner,name:$name){
    pullRequest(number:$number){
      id title body url state author{login} baseRefOid headRefOid
      commits(first:100){pageInfo{hasNextPage} nodes{commit{oid message authoredDate author{name user{login}}}}}
      reviewThreads(first:100){pageInfo{hasNextPage} nodes{id isResolved isOutdated path line originalLine diffSide subjectType
        comments(first:100){pageInfo{hasNextPage} nodes{id databaseId body url createdAt state author{login} pullRequestReview{id}}}}}
      reviews(first:100){pageInfo{hasNextPage} nodes{id body url state createdAt submittedAt authorAssociation author{login}}}
      comments(first:100){pageInfo{hasNextPage} nodes{id body url createdAt author{login}}}
    }
  }
}`

type login struct {
	Login string `json:"login"`
}

func (l *login) name() string {
	if l == nil {
		return "ghost"
	}
	return l.Login
}

type page struct {
	HasNextPage bool `json:"hasNextPage"`
}

type loadData struct {
	Viewer     login `json:"viewer"`
	Repository struct {
		PullRequest *struct {
			ID         string `json:"id"`
			Title      string `json:"title"`
			Body       string `json:"body"`
			URL        string `json:"url"`
			State      string `json:"state"`
			Author     *login `json:"author"`
			BaseRefOid string `json:"baseRefOid"`
			HeadRefOid string `json:"headRefOid"`
			Commits    struct {
				PageInfo page `json:"pageInfo"`
				Nodes    []struct {
					Commit struct {
						Oid          string    `json:"oid"`
						Message      string    `json:"message"`
						AuthoredDate time.Time `json:"authoredDate"`
						Author       struct {
							Name string `json:"name"`
							User *login `json:"user"`
						} `json:"author"`
					} `json:"commit"`
				} `json:"nodes"`
			} `json:"commits"`
			ReviewThreads struct {
				PageInfo page `json:"pageInfo"`
				Nodes    []struct {
					ID           string `json:"id"`
					IsResolved   bool   `json:"isResolved"`
					IsOutdated   bool   `json:"isOutdated"`
					Path         string `json:"path"`
					Line         int    `json:"line"` // in the diff from the base to the head now; null when outdated
					OriginalLine int    `json:"originalLine"`
					DiffSide     string `json:"diffSide"`
					SubjectType  string `json:"subjectType"`
					Comments     struct {
						PageInfo page `json:"pageInfo"`
						Nodes    []struct {
							ID                string    `json:"id"`
							DatabaseID        int64     `json:"databaseId"`
							Body              string    `json:"body"`
							URL               string    `json:"url"`
							CreatedAt         time.Time `json:"createdAt"`
							State             string    `json:"state"`
							Author            *login    `json:"author"`
							PullRequestReview *struct {
								ID string `json:"id"`
							} `json:"pullRequestReview"`
						} `json:"nodes"`
					} `json:"comments"`
				} `json:"nodes"`
			} `json:"reviewThreads"`
			Reviews struct {
				PageInfo page `json:"pageInfo"`
				Nodes    []struct {
					ID          string     `json:"id"`
					Body        string     `json:"body"`
					URL         string     `json:"url"`
					State       string     `json:"state"`
					CreatedAt   time.Time  `json:"createdAt"`
					SubmittedAt *time.Time `json:"submittedAt"`
					Association string     `json:"authorAssociation"`
					Author      *login     `json:"author"`
				} `json:"nodes"`
			} `json:"reviews"`
			Comments struct {
				PageInfo page `json:"pageInfo"`
				Nodes    []struct {
					ID        string    `json:"id"`
					Body      string    `json:"body"`
					URL       string    `json:"url"`
					CreatedAt time.Time `json:"createdAt"`
					Author    *login    `json:"author"`
				} `json:"nodes"`
			} `json:"comments"`
		} `json:"pullRequest"`
	} `json:"repository"`
}

// Load reads a pull request: its commits, every note and reply, and the viewer's drafts.
// Diffs are read when first asked for (Diff).
func (c *Client) Load(ctx context.Context, ref Ref) (*Review, error) {
	var d loadData
	err := c.graphql(ctx, loadQuery, map[string]any{"owner": ref.Owner, "name": ref.Name, "number": ref.Number}, &d)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ref, err)
	}
	pr := d.Repository.PullRequest
	if pr == nil {
		return nil, fmt.Errorf("%s: no such pull request", ref)
	}
	rv := &Review{
		Ref: ref, PR: ref.String(), ID: pr.ID, Title: pr.Title, Body: pr.Body, URL: pr.URL, State: pr.State,
		Author: pr.Author.name(), Viewer: d.Viewer.Login, Base: pr.BaseRefOid, Head: pr.HeadRefOid,
		Commits: []Commit{}, Threads: []*Thread{}, // a pull request without notes is [] in JSON, not null
	}
	rv.Truncated = pr.Commits.PageInfo.HasNextPage || pr.ReviewThreads.PageInfo.HasNextPage ||
		pr.Reviews.PageInfo.HasNextPage || pr.Comments.PageInfo.HasNextPage
	for _, n := range pr.Commits.Nodes {
		cm := n.Commit
		author := cm.Author.Name
		if cm.Author.User != nil {
			author = cm.Author.User.Login
		}
		title, body, _ := strings.Cut(cm.Message, "\n") // GitHub's own headline cuts a long title short
		rv.Commits = append(rv.Commits, Commit{SHA: cm.Oid, Title: strings.TrimSpace(title), Message: strings.TrimSpace(body), Author: author, Date: cm.AuthoredDate})
	}

	byNote := map[string]*Thread{}
	add := func(t *Thread) {
		rv.Threads = append(rv.Threads, t)
		for _, n := range t.Notes {
			byNote[n.ID] = t
		}
	}
	note := func(id, account, body, url string, at time.Time, draft bool, kind noteKind) (*Note, Meta) {
		m, text := decode(body)
		n := &Note{ID: id, Author: account, Account: account, Text: text, Created: at, Draft: draft, URL: url, PRComment: kind == inIssue, kind: kind, meta: m}
		if m.Author != "" {
			n.Author, n.Named = m.Author, true
		}
		return n, m
	}

	// review threads: notes on lines and files, threaded by GitHub
	personal, named := map[string]bool{}, map[string]bool{} // the reviews that hold a person's note, an agent's
	for _, th := range pr.ReviewThreads.Nodes {
		if len(th.Comments.Nodes) == 0 {
			continue
		}
		rv.Truncated = rv.Truncated || th.Comments.PageInfo.HasNextPage
		t := &Thread{ghThread: th.ID, Resolved: th.IsResolved, Outdated: th.IsOutdated}
		if th.SubjectType != "FILE" {
			t.HeadLine, t.HeadOld = th.Line, th.DiffSide == "LEFT"
		}
		for i, cm := range th.Comments.Nodes {
			n, m := note(cm.ID, cm.Author.name(), cm.Body, cm.URL, cm.CreatedAt, cm.State == "PENDING", inComment)
			t.Notes = append(t.Notes, n)
			if cm.PullRequestReview != nil {
				if n.Named {
					named[cm.PullRequestReview.ID] = true
				} else {
					personal[cm.PullRequestReview.ID] = true
				}
			}
			if i > 0 {
				continue
			}
			t.ID, t.topDB = cm.ID, cm.DatabaseID
			if m.Commit != "" || m.Path != "" {
				t.Commit, t.Path, t.Line, t.Old, t.Head = m.Commit, m.Path, m.Line, m.Old, m.Head
				continue
			}
			// written on github.com, in the whole change's diff: GitHub's own anchor
			t.Path = th.Path
			if th.SubjectType != "FILE" {
				t.Line, t.Old = th.OriginalLine, th.DiffSide == "LEFT"
			}
		}
		add(t)
	}

	// notes GitHub does not thread: sections of review bodies, and PR comments
	type loose struct {
		n *Note
		m Meta
	}
	var replies []loose
	place := func(n *Note, m Meta) {
		if m.Re != "" {
			replies = append(replies, loose{n, m})
			return
		}
		add(&Thread{ID: n.ID, Commit: m.Commit, Path: m.Path, Line: m.Line, Old: m.Old, Head: m.Head, Notes: []*Note{n}})
	}
	var sent []sentReview
	for _, r := range pr.Reviews.Nodes {
		ss := splitSections(r.Body)
		if r.State == "PENDING" {
			rv.pending = r.ID
			rv.sections = ss
		}
		s := sentReview{by: r.Author.name(), person: personal[r.ID], named: named[r.ID], state: r.State}
		for i, sec := range ss {
			if sec.Meta.Approval { // the approval, not a note
				if sec.Meta.Author == "" {
					s.approve = true
				} else {
					s.named = true
				}
				continue
			}
			n := &Note{ID: fmt.Sprintf("%s/%d", r.ID, i), Author: r.Author.name(), Account: r.Author.name(), Text: sec.Text,
				Created: r.CreatedAt, Draft: r.State == "PENDING", URL: r.URL, kind: inSection, meta: sec.Meta}
			if sec.Meta.Author != "" {
				n.Author, n.Named = sec.Meta.Author, true
			}
			s.person = s.person || !n.Named
			s.named = s.named || n.Named
			place(n, sec.Meta)
		}
		if r.State != "PENDING" && r.SubmittedAt != nil {
			s.at = *r.SubmittedAt
			if r.State == "APPROVED" {
				s.approve = true // approved on github.com
			}
			if !mayApprove(r.Association) { // anyone who may comment can write the marker: it takes an approval back, gives none
				s.approve = false
			}
			sent = append(sent, s)
		}
	}
	partial := pr.Reviews.PageInfo.HasNextPage || pr.ReviewThreads.PageInfo.HasNextPage
	for _, th := range pr.ReviewThreads.Nodes {
		partial = partial || th.Comments.PageInfo.HasNextPage
	}
	if !partial { // with a review or a note missing, a later one may have taken the approval back
		rv.Approval = approval(sent)
	}
	for _, cm := range pr.Comments.Nodes {
		n, m := note(cm.ID, cm.Author.name(), cm.Body, cm.URL, cm.CreatedAt, false, inIssue)
		place(n, m)
	}
	sort.SliceStable(replies, func(i, j int) bool { return replies[i].n.Created.Before(replies[j].n.Created) })
	for _, r := range replies {
		t := byNote[r.m.Re]
		if t == nil { // what it answered is gone
			r.n.Text = strings.TrimSpace("(a reply to a note that was removed)\n\n" + r.n.Text)
			add(&Thread{ID: r.n.ID, Notes: []*Note{r.n}})
			continue
		}
		t.Notes = append(t.Notes, r.n)
		byNote[r.n.ID] = t
	}
	rv.sortThreads()
	return rv, nil
}

// mayApprove says whether a review's author may approve: the repository's owner, a member of its
// organization or a collaborator, as GitHub counts approvals.
func mayApprove(association string) bool {
	switch association {
	case "OWNER", "MEMBER", "COLLABORATOR":
		return true
	}
	return false
}

// sentReview is what decides whether a submitted review approves.
type sentReview struct {
	by, state     string
	approve       bool
	person, named bool // it holds a person's note; an agent's
	at            time.Time
}

// approval is the latest person's review, when it approves: one that does not takes back an
// earlier approval, since its person has more to say, and so do a request for changes and a
// dismissed review. A review that holds only agents' notes counts for nothing, whatever GitHub
// says of it: agents share their reviewer's account and never approve.
func approval(sent []sentReview) *Approval {
	sort.SliceStable(sent, func(i, j int) bool { return sent[i].at.Before(sent[j].at) })
	var a *Approval
	for _, s := range sent {
		switch {
		case s.named && !s.person:
		case s.state == "DISMISSED" || s.state == "CHANGES_REQUESTED":
			a = nil
		case s.approve:
			a = &Approval{By: s.by, At: s.at}
		case s.person:
			a = nil
		}
	}
	return a
}

// Diff is the commit's own diff, from its parent.
func (c *Client) Diff(ctx context.Context, rv *Review, sha string) ([]File, error) {
	return c.diff(ctx, rv.Ref.Repo()+"@"+sha, fmt.Sprintf("/repos/%s/commits/%s", rv.Ref.Repo(), sha))
}

// Whole is the whole change's diff, from the base to the head: what GitHub's "Files changed" shows.
func (c *Client) Whole(ctx context.Context, rv *Review) ([]File, error) {
	return c.cumulative(ctx, rv, rv.Head)
}

// cumulative is the diff from the merge base to the commit: GitHub counts diff positions in it.
func (c *Client) cumulative(ctx context.Context, rv *Review, sha string) ([]File, error) {
	return c.diff(ctx, rv.Ref.Repo()+"@"+rv.Base+"..."+sha, fmt.Sprintf("/repos/%s/compare/%s...%s", rv.Ref.Repo(), rv.Base, sha))
}

func (c *Client) diff(ctx context.Context, key, path string) ([]File, error) {
	c.mu.Lock()
	f, ok := c.diffs[key]
	c.mu.Unlock()
	if ok {
		return f, nil
	}
	data, err := c.do(ctx, "GET", path, "application/vnd.github.diff", nil)
	if err != nil {
		return nil, fmt.Errorf("diff %s: %w", key, err)
	}
	f = ParseDiff(string(data))
	c.mu.Lock()
	if c.diffs == nil {
		c.diffs = map[string][]File{}
	}
	c.diffs[key] = f
	c.mu.Unlock()
	return f, nil
}

func fileIn(files []File, path string) (File, bool) {
	for _, f := range files {
		if f.Path == path {
			return f, true
		}
	}
	return File{}, false
}

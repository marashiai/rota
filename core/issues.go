package core

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The Issues tab lists the team's open issues, as GitHub last listed them, each with where it
// stands: a pull request open for it, and the labels it carries that have an emoji, and, for one in
// the plan (plan.go), its place there and why. Which agent takes which issue is its role's
// business: the tab names none.

// OpenIssue is an open issue of the team's repositories, as the Issues tab lists it.
type OpenIssue struct {
	Repo    string
	Number  int
	Title   string
	Name    string    // as agents name it: <name>#<n>, or <owner>/<name>#<n> when two repositories share the name
	Created time.Time // when it was opened; zero when GitHub's list did not say
	Marks   []Mark    // a pull request open for it, then its labels that have an emoji
	Labels  []string
	PRs     []string // the open pull requests for it, by Ref (owner/name#n)
	Rank    int      // its place in the plan, from 1; 0 when the plan leaves it out
	Why     string   // why the plan puts it there
}

func (i OpenIssue) listed() (string, string, []Mark, []string) {
	return i.Repo, i.Name, i.Marks, i.Labels
}

// Ref is owner/name#n.
func (i OpenIssue) Ref() string { return i.Repo + "#" + strconv.Itoa(i.Number) }

// URL is the issue on GitHub.
func (i OpenIssue) URL() string {
	return "https://github.com/" + i.Repo + "/issues/" + strconv.Itoa(i.Number)
}

// The orders the Issues tab lists them in: as planned unless the human chose another, which is
// newest first while there is no plan. IssueOrders reads them for a select; as planned goes by the
// plan, then newest first; priority goes by the first of the labels setting's labels an issue
// carries (its order), newest first among those alike.
const (
	OrderPlan     = ""
	OrderNewest   = "newest"
	OrderOldest   = "oldest"
	OrderPriority = "priority"
)

var IssueOrders = []Filter{{OrderPlan, "As planned", 0}, {OrderNewest, "Newest first", 0}, {OrderOldest, "Oldest first", 0}, {OrderPriority, "By priority", 0}}

// prMark marks an issue a pull request is open for.
var prMark = Mark{"🔧", "pull request", "a pull request is open for it"}

// Issues are the team's open issues, as GitHub last listed them, in order (one of IssueOrders).
func (t *Team) Issues(order string) []OpenIssue {
	repos, labels, open := t.teamRepos(), t.Labels(), t.openWork()
	planned := map[string]PlannedIssue{}
	rank := map[string]int{}
	for k, p := range t.Plan().Issues {
		planned[p.Ref()], rank[p.Ref()] = p, k+1
	}
	prs := map[string][]string{} // by issue
	for repo, items := range open.Repos {
		for n, it := range items {
			if it.PR {
				for _, ref := range it.Issues {
					prs[ref] = append(prs[ref], repo+"#"+strconv.Itoa(n))
				}
			}
		}
	}
	var out []OpenIssue
	for repo, items := range open.Repos {
		for n, it := range items {
			if it.PR {
				continue
			}
			i := OpenIssue{Repo: repo, Number: n, Title: it.Title, Name: shortRef(repos, repo) + "#" + strconv.Itoa(n),
				Created: it.Created, Labels: it.Labels, PRs: prs[repo+"#"+strconv.Itoa(n)]}
			i.Rank, i.Why = rank[i.Ref()], planned[i.Ref()].Why
			slices.SortFunc(i.PRs, compareRefs)
			if len(i.PRs) > 0 {
				i.Marks = append(i.Marks, prMark)
			}
			for _, l := range labels {
				if l.Emoji != "" && hasLabel(it.Labels, l.Name) {
					i.Marks = append(i.Marks, Mark{l.Emoji, l.Name, strings.TrimSuffix(l.Name+": "+l.Description, ": ")})
				}
			}
			out = append(out, i)
		}
	}
	byLabel := func(i OpenIssue) int { // the first of the labels setting's it carries
		for r, l := range t.Config.Labels {
			if hasLabel(i.Labels, l.Name) {
				return r
			}
		}
		return len(t.Config.Labels)
	}
	newest := func(a, b OpenIssue) int {
		return cmp.Or(b.Created.Compare(a.Created), cmp.Compare(a.Repo, b.Repo), cmp.Compare(b.Number, a.Number))
	}
	inPlan := func(i OpenIssue) int { // its place in the plan; those it leaves out after them all
		if i.Rank == 0 {
			return len(rank) + 1
		}
		return i.Rank
	}
	slices.SortFunc(out, func(a, b OpenIssue) int {
		switch order {
		case OrderPlan:
			return cmp.Or(cmp.Compare(inPlan(a), inPlan(b)), newest(a, b))
		case OrderOldest:
			return newest(b, a)
		case OrderPriority:
			return cmp.Or(cmp.Compare(byLabel(a), byLabel(b)), newest(a, b))
		}
		return newest(a, b)
	})
	return out
}

// IssueText is an issue as GitHub has it now, to read in the pages: what it says and what was
// said on it, oldest first.
type IssueText struct {
	Title    string
	State    string // OPEN or CLOSED
	Reason   string // why it was closed: COMPLETED, NOT_PLANNED or DUPLICATE; "" while it is open
	Author   string
	Created  time.Time
	Body     string
	Labels   []string
	Comments []IssueComment
}

// IssueComment is one comment on an issue.
type IssueComment struct {
	Author string
	At     time.Time
	Body   string
}

// ReadIssue asks GitHub for issue n of repo, one of the team's repositories, as the human's gh
// reads it.
func (t *Team) ReadIssue(ctx context.Context, repo string, n int) (IssueText, error) {
	k := slices.IndexFunc(t.teamRepos(), func(r string) bool { return strings.EqualFold(r, repo) })
	if k < 0 {
		return IssueText{}, fmt.Errorf("%s is not one of the team's repositories", repo)
	}
	repo = t.teamRepos()[k] // as the team spells it
	type login struct {
		Login string `json:"login"`
	}
	var raw struct {
		Title       string    `json:"title"`
		State       string    `json:"state"`
		StateReason string    `json:"stateReason"`
		Author      login     `json:"author"`
		CreatedAt   time.Time `json:"createdAt"`
		Body        string    `json:"body"`
		Labels      []ghLabel `json:"labels"`
		Comments    []struct {
			Author    login     `json:"author"`
			CreatedAt time.Time `json:"createdAt"`
			Body      string    `json:"body"`
		} `json:"comments"`
	}
	if err := t.gh(ctx, &raw, "issue", "view", strconv.Itoa(n), "--repo", repo, "--json", "title,state,stateReason,author,createdAt,body,labels,comments"); err != nil {
		return IssueText{}, err
	}
	out := IssueText{Title: raw.Title, State: raw.State, Author: raw.Author.Login, Created: raw.CreatedAt, Body: raw.Body}
	if raw.State != "OPEN" {
		out.Reason = raw.StateReason
	}
	for _, l := range raw.Labels {
		out.Labels = append(out.Labels, l.Name)
	}
	for _, c := range raw.Comments {
		out.Comments = append(out.Comments, IssueComment{Author: c.Author.Login, At: c.CreatedAt, Body: c.Body})
	}
	return out, nil
}

// OnlyIssues are the issues of repo that carry a mark with the emoji mark and every one of
// labels; "" for repo or mark keeps all.
func OnlyIssues(issues []OpenIssue, repo, mark string, labels ...string) []OpenIssue {
	var out []OpenIssue
	for _, i := range issues {
		if matches(i, repo, mark, labels) {
			out = append(out, i)
		}
	}
	return out
}

// compareRefs orders owner/name#n by repository, then number.
func compareRefs(a, b string) int {
	ra, na, _ := strings.Cut(a, "#")
	rb, nb, _ := strings.Cut(b, "#")
	x, _ := strconv.Atoi(na)
	y, _ := strconv.Atoi(nb)
	return cmp.Or(cmp.Compare(ra, rb), cmp.Compare(x, y))
}

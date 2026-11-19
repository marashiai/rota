package core

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"rota/gloss/review"
)

// The human reviews the team's pull requests commit by commit with gloss, in the pages and the
// console. Every note lives on the pull request on GitHub; when the human sends notes, the agent
// that wrote the pull request finds them in its inbox, which wakes it, and answers them with
// gloss's commands (GLOSS_AUTHOR names it in its shift). When the human approves one, it gets
// ApprovedLabel, and the agents whose role merges are told to merge it. GitHub is the record of all
// of it: rota keeps none of its own.
//
// A pull request's draft state says whether the agents are done with it: a draft is being worked
// on, and waits neither for the human's review nor for a merge; ready for review is finished. Its
// author marks it ready once it is finished, and any agent that pushes to one makes it a draft
// first, and ready again once it is done (the brief says so). The human's approval stays through
// that: an approved pull request merges once it is ready again, what was pushed since included.

// OpenPR is an open pull request of the team's repositories, as the review queue lists it.
type OpenPR struct {
	Repo   string
	Number int
	Title  string
	Name   string   // as agents name it: <name>#<n>, or <owner>/<name>#<n> when two repositories share the name
	Depth  int      // how far it is indented: under the one open pull request it waits for, that one's depth + 1
	Waits  []string // the open pull requests it waits for, by Name: stacked on, or its description "Depends on"
	Why    string   // why it is there, as the agent that ordered them said; "" when it did not order it
	Marks  []Mark   // where it stands: its labels that have an emoji
	Labels []string // its own labels and those of the open issues it is for, by name

	own             []string // its own labels only
	waits, answered bool     // the human waits on an agent's answer to their notes; an agent answered since they last looked
	draft           bool     // an agent works on it: not for the human's review until it is ready
}

// Mark is where a pull request stands, as the Reviews tab shows it: Name is short, for its filter.
type Mark struct{ Emoji, Name, Text string }

// marks are where a pull request stands: who waits on whom over its notes, then the labels it
// carries that have an emoji. Approved is the step after waiting for review (review, the
// review_label setting), and so are notes waiting on an agent or an answer since the human last
// opened the review (seen): then that label is not shown.
func marks(it openItem, labels []Label, review string, seen time.Time) []Mark {
	has := func(name string) bool {
		return slices.ContainsFunc(it.Labels, func(l string) bool { return strings.EqualFold(l, name) })
	}
	var out []Mark
	if it.Talk.Waiting > 0 {
		out = append(out, Mark{WaitsMark, waitsName, waitsText})
	}
	if it.Talk.Answered.After(seen) {
		out = append(out, Mark{AnsweredMark, answeredName, answeredText})
	}
	talking := len(out) > 0
	for _, l := range labels {
		if l.Emoji == "" || !has(l.Name) || (strings.EqualFold(l.Name, review) && (has(ApprovedLabel) || talking)) {
			continue
		}
		out = append(out, Mark{l.Emoji, l.Name, strings.TrimSuffix(l.Name+": "+l.Description, ": ")})
	}
	return out
}

// prLabels are a pull request's own labels, then those of the issues it is for that GitHub last
// listed as open, each name once.
func prLabels(it openItem, open openList) []string {
	var out []string
	add := func(ls []string) {
		for _, l := range ls {
			if !hasLabel(out, l) {
				out = append(out, l)
			}
		}
	}
	add(it.Labels)
	for _, ref := range it.Issues {
		repo, n, _ := strings.Cut(ref, "#")
		num, _ := strconv.Atoi(n)
		if issue, ok := open.Repos[repo][num]; ok && !issue.PR {
			add(issue.Labels)
		}
	}
	return out
}

// Ref is owner/name#n.
func (p OpenPR) Ref() string { return p.Repo + "#" + strconv.Itoa(p.Number) }

// ReviewQueue is the team's open pull requests, as GitHub last listed them, in the order the
// human reviews them: the order an agent gave last (rota review-order), with every pull
// request after the open ones it waits for, whatever that order says.
type ReviewQueue struct {
	PRs  []OpenPR
	By   string    // the agent that ordered them; "" when none has
	Time time.Time // when
}

// QueueText says how the queue is ordered, for the human.
func QueueText(q ReviewQueue) string {
	how := "No agent has ordered them yet: by repository, oldest first"
	if q.By != "" {
		how = "In the order " + q.By + " gave at " + q.Time.Local().Format("Mon 15:04")
	}
	return how + "; each after the open pull requests it waits for, and one that waits for only one indented under it."
}

// ListFilter is what narrows a tab that lists open pull requests or issues: a repository
// (owner/name), a mark (its emoji) and labels, each "" or none for all, and the order the tab
// lists them in, "" for its own. The pages and the console share each tab's, and keep it until
// the human changes it.
type ListFilter struct {
	Repo   string   `json:"repo,omitempty"`
	Mark   string   `json:"mark,omitempty"`
	Labels []string `json:"labels,omitempty"`
	Order  string   `json:"order,omitempty"`
}

// The tabs a ListFilter narrows.
const (
	ReviewsTab = "review"
	IssuesTab  = "issue"
)

func filterFile(tab string) string { return "." + tab + "-filter.json" }

// Clean is f with its labels trimmed, and each once, whatever its case.
func (f ListFilter) Clean() ListFilter {
	var labels []string
	for _, l := range f.Labels {
		if l = strings.TrimSpace(l); l != "" && !slices.ContainsFunc(labels, func(c string) bool { return strings.EqualFold(c, l) }) {
			labels = append(labels, l)
		}
	}
	return ListFilter{Repo: strings.TrimSpace(f.Repo), Mark: strings.TrimSpace(f.Mark), Labels: labels, Order: strings.TrimSpace(f.Order)}
}

// ListFilter is what narrows the tab, as the human last chose it.
func (c *Config) ListFilter(tab string) ListFilter {
	var f ListFilter
	if b, err := os.ReadFile(c.Path(filterFile(tab))); err == nil {
		json.Unmarshal(b, &f)
	}
	return f.Clean()
}

// SetListFilter keeps f as what narrows the tab.
func (c *Config) SetListFilter(tab string, f ListFilter) error {
	f = f.Clean()
	if reflect.DeepEqual(f, c.ListFilter(tab)) {
		return nil
	}
	path := c.Path(filterFile(tab))
	if reflect.DeepEqual(f, ListFilter{}) {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	b, _ := json.MarshalIndent(f, "", "  ")
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(append(b, '\n'))
	if err := errors.Join(werr, tmp.Close()); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Only is the queue narrowed to the pull requests of repo that carry a mark with the emoji mark
// and every one of labels; "" for repo or mark keeps all. Narrowed, none is indented: what it
// waits for may be left out, and its Waits still names it.
func (q ReviewQueue) Only(repo, mark string, labels ...string) ReviewQueue {
	if repo == "" && mark == "" && len(labels) == 0 {
		return q
	}
	out := ReviewQueue{By: q.By, Time: q.Time}
	for _, pr := range q.PRs {
		if matches(pr, repo, mark, labels) {
			pr.Depth = 0
			out.PRs = append(out.PRs, pr)
		}
	}
	return out
}

// ForYou are the pull requests that wait on the human, in the queue's order: one whose notes an
// agent answered since the human last opened its review, and one that asks for their review
// (carries reviewLabel itself; when the team has none, any open one) and is neither approved nor
// waiting on an agent's answer to their notes. The labels of the issues it is for do not count. A
// draft is none of them: an agent works on it, and it comes to the human once it is ready.
func (q ReviewQueue) ForYou(reviewLabel string) []OpenPR {
	var out []OpenPR
	for _, p := range q.PRs {
		if p.draft {
			continue
		}
		asks := reviewLabel == "" || hasLabel(p.own, reviewLabel)
		if p.answered || asks && !hasLabel(p.own, ApprovedLabel) && !p.waits {
			out = append(out, p)
		}
	}
	return out
}

// Listed is what the Reviews and Issues tabs list and narrow: an open pull request or issue.
type Listed interface {
	// listed is its repository (owner/name), its name (<name>#<n>), its marks and its labels.
	listed() (repo, name string, marks []Mark, labels []string)
}

func (p OpenPR) listed() (string, string, []Mark, []string) { return p.Repo, p.Name, p.Marks, p.Labels }

// matches says whether it is of repo, carries a mark with the emoji mark and every one of labels;
// "" for repo or mark matches all.
func matches(it Listed, repo, mark string, labels []string) bool {
	r, _, ms, ls := it.listed()
	return (repo == "" || strings.EqualFold(r, repo)) && (mark == "" || slices.ContainsFunc(ms, func(m Mark) bool { return m.Emoji == mark })) &&
		!slices.ContainsFunc(labels, func(l string) bool { return !hasLabel(ls, l) })
}

func hasLabel(labels []string, name string) bool {
	return slices.ContainsFunc(labels, func(l string) bool { return strings.EqualFold(l, name) })
}

// LabelFilters are the labels of the items but those chosen, by name, each with how many of
// them carry it: as many as choosing it too keeps.
func LabelFilters[T Listed](items []T, chosen []string) []Filter {
	var out []Filter
	for _, it := range items {
		_, _, _, labels := it.listed()
		for _, l := range labels {
			if hasLabel(chosen, l) {
				continue
			}
			if i := slices.IndexFunc(out, func(f Filter) bool { return strings.EqualFold(f.Value, l) }); i >= 0 {
				out[i].N++
			} else {
				out = append(out, Filter{l, l, 1})
			}
		}
	}
	slices.SortFunc(out, func(a, b Filter) int { return strings.Compare(strings.ToLower(a.Label), strings.ToLower(b.Label)) })
	return out
}

// Filter is a way to narrow or order a tab: a repository (owner/name), a mark (its emoji), a
// label or an order, how it reads, and how many it keeps.
type Filter struct {
	Value, Label string
	N            int
}

// Filters are the items' repositories, by name, and their marks, in the order they first appear,
// each with how many of them it keeps.
func Filters[T Listed](items []T) (repos, marks []Filter) {
	add := func(list []Filter, value, label string) []Filter {
		if i := slices.IndexFunc(list, func(f Filter) bool { return f.Value == value }); i >= 0 {
			list[i].N++
			return list
		}
		return append(list, Filter{value, label, 1})
	}
	for _, it := range items {
		repo, name, ms, _ := it.listed()
		repos = add(repos, repo, name[:strings.LastIndex(name, "#")])
		for _, m := range ms {
			marks = add(marks, m.Emoji, m.Emoji+" "+m.Name)
		}
	}
	slices.SortStableFunc(repos, func(a, b Filter) int { return strings.Compare(a.Label, b.Label) })
	return repos, marks
}

// Legend is every mark the items show, once, in the order they first appear.
func Legend[T Listed](items []T) []Mark {
	var out []Mark
	for _, it := range items {
		_, _, ms, _ := it.listed()
		for _, m := range ms {
			if !slices.Contains(out, m) {
				out = append(out, m)
			}
		}
	}
	return out
}

// reviewOrder is the order an agent gave last, most worth reviewing first.
type reviewOrder struct {
	By   string        `json:"by"`
	Time time.Time     `json:"time"`
	PRs  []reviewEntry `json:"prs"`
}

type reviewEntry struct {
	Ref string `json:"ref"` // owner/name#n
	Why string `json:"why"`
}

// SetReviewOrder replaces the order in which the human reviews the open pull requests with
// agent's: each entry is "<repository>#<n> <why>", most worth reviewing first. It returns those
// GitHub did not last list as open pull requests, which the queue leaves out.
func (t *Team) SetReviewOrder(agent string, entries []string) ([]string, error) {
	o := reviewOrder{By: agent, Time: time.Now()}
	open := t.openWork()
	var unknown []string
	seen := map[string]bool{}
	for _, e := range entries {
		ref, why, _ := strings.Cut(strings.TrimSpace(e), " ")
		repo, n, err := t.parseRef(ref, "")
		if err != nil {
			return nil, fmt.Errorf("%w: give each pull request as \"<repository>#<number> <why>\"", err)
		}
		if why = strings.TrimSpace(why); why == "" {
			return nil, fmt.Errorf("%s: say why it comes there, after its number", ref)
		}
		full := repo + "#" + strconv.Itoa(n)
		if seen[full] {
			return nil, fmt.Errorf("%s is in the order twice", ref)
		}
		seen[full] = true
		if items, listed := open.Repos[repo]; listed && !items[n].PR {
			unknown = append(unknown, ref)
		}
		o.PRs = append(o.PRs, reviewEntry{full, why})
	}
	b, _ := json.MarshalIndent(o, "", "  ")
	tmp := t.Path("." + queueFile + ".tmp")
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return nil, err
	}
	return unknown, os.Rename(tmp, t.Path(queueFile))
}

// ReviewQueue orders the open pull requests: each comes after every open pull request it
// waits for, and is as urgent as the most urgent of those that wait for it; otherwise they
// go by the agent's order, then by repository and oldest first. One that waits for a single
// open pull request is indented under it; one that waits for more stands on its own, after
// them all. A loop of waits is broken where it closes.
func (t *Team) ReviewQueue() ReviewQueue {
	var o reviewOrder
	if b, err := os.ReadFile(t.Path(queueFile)); err == nil {
		json.Unmarshal(b, &o)
	}
	rank := map[string]int{}
	why := map[string]string{}
	for i, e := range o.PRs {
		rank[e.Ref], why[e.Ref] = i, e.Why
	}
	type key struct {
		rank   int
		repo   string
		number int
	}
	compare := func(a, b key) int {
		return cmp.Or(cmp.Compare(a.rank, b.rank), cmp.Compare(a.repo, b.repo), cmp.Compare(a.number, b.number))
	}
	type node struct {
		pr          OpenPR
		item        openItem
		own, urgent key
		waits, kids []*node
		waitedBy    []*node
	}
	repos, labels, open, seen := t.teamRepos(), t.Labels(), t.openWork(), t.seenReviews()
	nodes := map[string]*node{}
	heads := map[string]*node{} // "<repo> <branch>"
	var all []*node
	for repo, items := range open.Repos {
		for n, it := range items {
			if !it.PR {
				continue
			}
			pr := OpenPR{Repo: repo, Number: n, Title: it.Title, Name: shortRef(repos, repo) + "#" + strconv.Itoa(n)}
			pr.Why, pr.Marks, pr.Labels = why[pr.Ref()], marks(it, labels, t.ReviewLabel, seen[pr.Ref()]), prLabels(it, open)
			pr.own, pr.waits, pr.answered, pr.draft = it.Labels, it.Talk.Waiting > 0, it.Talk.Answered.After(seen[pr.Ref()]), it.Draft
			r, ok := rank[pr.Ref()]
			if !ok {
				r = len(o.PRs)
			}
			nd := &node{pr: pr, item: it, own: key{r, repo, n}}
			nodes[pr.Ref()], all = nd, append(all, nd)
			if it.Head != "" {
				heads[repo+" "+it.Head] = nd
			}
		}
	}
	slices.SortFunc(all, func(a, b *node) int { return compare(a.own, b.own) })
	for _, nd := range all { // what it waits for: the open pull request it is stacked on, and those it depends on
		var waits []*node
		if under := heads[nd.pr.Repo+" "+nd.item.Base]; under != nil {
			waits = append(waits, under)
		}
		for _, ref := range nd.item.Deps {
			if d := nodes[ref]; d != nil {
				waits = append(waits, d)
			}
		}
		for _, w := range waits {
			if w != nd && !slices.Contains(nd.waits, w) {
				nd.waits = append(nd.waits, w)
			}
		}
	}
	state := map[*node]int{} // 1 on the path, 2 done
	var visit func(nd *node)
	visit = func(nd *node) {
		state[nd] = 1
		kept := nd.waits[:0]
		for _, w := range nd.waits {
			if state[w] == 1 {
				continue // it closes a loop
			}
			if state[w] == 0 {
				visit(w)
			}
			kept = append(kept, w)
		}
		nd.waits = kept
		state[nd] = 2
	}
	for _, nd := range all {
		if state[nd] == 0 {
			visit(nd)
		}
	}
	for _, nd := range all {
		for _, w := range nd.waits {
			w.waitedBy = append(w.waitedBy, nd)
		}
	}
	known := map[*node]bool{}
	var urgency func(nd *node) key
	urgency = func(nd *node) key {
		if !known[nd] {
			nd.urgent = nd.own
			for _, d := range nd.waitedBy {
				if u := urgency(d); compare(u, nd.urgent) < 0 {
					nd.urgent = u
				}
			}
			known[nd] = true
		}
		return nd.urgent
	}
	before := func(a, b *node) int { return cmp.Or(compare(urgency(a), urgency(b)), compare(a.own, b.own)) }
	top := func(nd *node) *node {
		for len(nd.waits) == 1 {
			nd = nd.waits[0]
		}
		return nd
	}
	var tops []*node
	for _, nd := range all {
		if len(nd.waits) == 1 {
			nd.waits[0].kids = append(nd.waits[0].kids, nd)
		} else {
			tops = append(tops, nd)
		}
	}
	q := ReviewQueue{By: o.By, Time: o.Time}
	var emit func(nd *node, depth int)
	emit = func(nd *node, depth int) {
		nd.pr.Depth = depth
		for _, w := range nd.waits {
			nd.pr.Waits = append(nd.pr.Waits, w.pr.Name)
		}
		q.PRs = append(q.PRs, nd.pr)
		slices.SortFunc(nd.kids, before)
		for _, k := range nd.kids {
			emit(k, depth+1)
		}
	}
	placed := map[*node]bool{}
	for len(tops) > 0 { // the most urgent whose pull requests it waits for are all listed
		slices.SortFunc(tops, before)
		i := slices.IndexFunc(tops, func(nd *node) bool {
			return !slices.ContainsFunc(nd.waits, func(w *node) bool { return !placed[top(w)] })
		})
		if i < 0 {
			i = 0 // cannot be: what it waits for is listed before it
		}
		nd := tops[i]
		tops = slices.Delete(tops, i, i+1)
		placed[nd] = true
		emit(nd, 0)
	}
	return q
}

// Reviewable tells whether the pages review repo's pull requests: only the team's repositories,
// though gh's login reaches more.
func (t *Team) Reviewable(repo string) bool {
	return slices.ContainsFunc(t.teamRepos(), func(r string) bool { return strings.EqualFold(r, repo) })
}

// reviewing is what the mail does on a pull request as the human, the way gloss does it: tests
// replace it.
type reviewing interface {
	Load(ctx context.Context, ref review.Ref) (*review.Review, error)
	Approve(ctx context.Context, rv *review.Review) (int, error)
	AddDraft(ctx context.Context, rv *review.Review, in review.NoteInput) (string, error)
	Send(ctx context.Context, rv *review.Review) (int, error)
}

var reviewer = func() (reviewing, error) { return ReviewClient() }

// ApprovePR approves a pull request as the human, with the drafts they left on it in gloss, and
// tells the agents, as gloss's Approve does. Once GitHub has the approval, a failure to tell them
// is what it returns as warn, not an error: the approval stands.
func (t *Team) ApprovePR(ctx context.Context, repo string, number int) (warn string, err error) {
	c, rv, err := t.loadReview(ctx, repo, number)
	if err != nil {
		return "", err
	}
	sent, err := c.Approve(ctx, rv)
	if err != nil {
		return "", err
	}
	t.SeenReview(repo, number, time.Now()) // what the human approved, they saw
	if err := t.ReviewSent(ctx, repo, number, sent, true); err != nil {
		return err.Error(), nil
	}
	return "", nil
}

// NotePR sends the human's note on a pull request's whole change, with the drafts they left on it
// in gloss, as one review, and tells the agent that wrote it, as gloss's Send does: the pull request
// then waits on that agent's answer. A draft of the same note that a failed try left is sent, not
// added again. Once GitHub has the review, a failure to tell the agent is warn, not an error.
func (t *Team) NotePR(ctx context.Context, repo string, number int, text string) (warn string, err error) {
	if text = strings.TrimSpace(text); text == "" {
		return "", fmt.Errorf("the note is empty")
	}
	c, rv, err := t.loadReview(ctx, repo, number)
	if err != nil {
		return "", err
	}
	if !hasDraft(rv, text) {
		if _, err := c.AddDraft(ctx, rv, review.NoteInput{Text: text}); err != nil {
			return "", err
		}
		owner, name, _ := strings.Cut(repo, "/")
		if rv, err = c.Load(ctx, review.Ref{Owner: owner, Name: name, Number: number}); err != nil { // Send sends the drafts it reads
			return "", err
		}
	}
	sent, err := c.Send(ctx, rv)
	if err != nil {
		return "", err
	}
	if sent == 0 {
		return "", fmt.Errorf("GitHub kept no draft to send: nothing was sent")
	}
	t.SeenReview(repo, number, time.Now())
	if err := t.ReviewSent(ctx, repo, number, sent, false); err != nil {
		return err.Error(), nil
	}
	return "", nil
}

// hasDraft says whether the human's drafts on a review include text.
func hasDraft(rv *review.Review, text string) bool {
	for _, th := range rv.Threads {
		for _, n := range th.Notes {
			if n.Draft && strings.TrimSpace(n.Text) == text {
				return true
			}
		}
	}
	return false
}

func (t *Team) loadReview(ctx context.Context, repo string, number int) (reviewing, *review.Review, error) {
	if !t.Reviewable(repo) {
		return nil, nil, fmt.Errorf("%s is not one of the team's repositories", repo)
	}
	c, err := reviewer()
	if err != nil {
		return nil, nil, err
	}
	owner, name, _ := strings.Cut(repo, "/")
	rv, err := c.Load(ctx, review.Ref{Owner: owner, Name: name, Number: number})
	return c, rv, err
}

// ReviewClient reads and writes notes on pull requests as the human: gh's login on this machine.
func ReviewClient() (*review.Client, error) {
	token, err := review.Token()
	if err != nil {
		return nil, err
	}
	return review.NewClient(token), nil
}

// Approved says whether the human approved the pull request a review was just sent on, as GitHub
// has it now. The review in hand is not enough: it keeps an approval it loaded after notes sent
// without one, which take it back.
func Approved(ctx context.Context, c *review.Client, rv *review.Review) (bool, error) {
	if !rv.Approved() {
		return false, nil
	}
	now, err := c.Load(ctx, rv.Ref)
	if err != nil {
		return false, fmt.Errorf("whether you approved %s: %w", rv.PR, err)
	}
	return now.Approved(), nil
}

// ReviewSent tells the agents what the human did in a review of a pull request: the agent that
// wrote it finds the notes they sent in its inbox; when they approved it, it gets ApprovedLabel,
// and every agent whose role merges is told to merge it. A draft stays one: an agent works on it,
// and it merges once that agent marks it ready. The Reviews tab shows it once GitHub is listed again.
func (t *Team) ReviewSent(ctx context.Context, repo string, number, sent int, approved bool) error {
	ref := repo + "#" + strconv.Itoa(number)
	var errs []error
	if sent > 0 {
		s := "s"
		if sent == 1 {
			s = ""
		}
		text := fmt.Sprintf("I sent %d review note%s on this pull request. "+
			"List what waits on you with \"gloss notes %s --open\", change what the notes ask for, and answer each one with gloss reply.", sent, s, ref)
		if approved {
			text = fmt.Sprintf("I sent %d review note%s on this pull request and approved it. "+
				"List what waits on you with \"gloss notes %s --open\" and answer each one with gloss reply. Push a commit only where a note asks for a change: the team may merge the pull request as it is.", sent, s, ref)
		}
		if agent, err := t.prAuthor(ctx, repo, number); err != nil {
			errs = append(errs, err)
		} else if err := t.LeaveNote(agent, ref, "human", text); err != nil {
			errs = append(errs, err)
		}
	}
	if approved {
		n := strconv.Itoa(number) // as the human: gh's login here
		labelled := t.gh(ctx, nil, "pr", "edit", n, "-R", repo, "--add-label", ApprovedLabel)
		if labelled != nil {
			errs = append(errs, fmt.Errorf("%s did not get %s, which is your approval: put it on by hand on GitHub (%w)", ref, ApprovedLabel, labelled))
		}
		mergers := t.mergers(repo)
		switch {
		case labelled != nil:
			mergers = nil // without the label, there is nothing for them to merge yet
		case len(mergers) == 0:
			errs = append(errs, fmt.Errorf("no agent of the team merges in %s: merge %s yourself", repo, ref))
		}
		for _, a := range mergers {
			if err := t.LeaveNote(a, ref, "human", fmt.Sprintf("I approved this pull request: it carries %s. Merge it with \"rota merge %s <its head commit>\" once nothing holds it back: "+
				"rota merge waits for its rollout and for what it depends on, and while it is a draft, for the agent working on it to mark it ready.", ApprovedLabel, ref)); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if sent > 0 || approved {
		t.relistOpen(repo) // the Reviews tab shows ⏳ and the label at once; else at the next listing
	}
	return errors.Join(errs...)
}

// mergers are the enabled agents of repo whose role merges: their tools allow rota merge.
func (t *Team) mergers(repo string) []string {
	var out []string
	for _, a := range t.Agents {
		if a.Enabled && strings.Contains(a.Tools, "rota merge") && slices.ContainsFunc(a.Repos, func(r string) bool { return strings.EqualFold(r, repo) }) {
			out = append(out, a.Name)
		}
	}
	return out
}

// prAuthor is the agent that wrote a pull request: its branch is <agent>/…, or it carries the
// label rota:<agent>.
func (t *Team) prAuthor(ctx context.Context, repo string, number int) (string, error) {
	var pr struct {
		HeadRefName string `json:"headRefName"`
		Labels      []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := t.gh(ctx, &pr, "pr", "view", strconv.Itoa(number), "-R", repo, "--json", "headRefName,labels"); err != nil {
		return "", err
	}
	var names []string
	if prefix, _, ok := strings.Cut(pr.HeadRefName, "/"); ok {
		names = append(names, prefix)
	}
	for _, l := range pr.Labels {
		if name, ok := strings.CutPrefix(l.Name, "rota:"); ok {
			names = append(names, name)
		}
	}
	for _, name := range names {
		if a, err := t.agent(name); err == nil && slices.Contains(a.Repos, repo) {
			return a.Name, nil
		}
	}
	return "", fmt.Errorf("no agent of the team wrote %s#%d (its branch is %q)", repo, number, pr.HeadRefName)
}

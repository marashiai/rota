package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

// The organizer watches the repositories the agents' triggers name (triggers.go). Every few
// minutes it asks GitHub, in one query per repository through gh, for its open issues and pull
// requests and the pull requests merged lately, and tells what happened since it last looked by
// what changed: a new issue or pull request, a label, a comment or a review, a draft made ready,
// an approval, a merge, a conflict, failing checks, a base branch that moved. It keeps what it saw
// in watchFile. Its first look at a repository only takes note of it: what was there before is
// nothing new.

// watchEvery is how often the organizer looks.
var watchEvery = 2 * time.Minute

// watchFile keeps what the organizer last saw of each repository it watches.
const watchFile = ".watch.json"

// watchNew is how far before the last look an issue or pull request it had not seen yet may have
// been opened and still count as new: GitHub may list one a little late.
const watchNew = 10 * time.Minute

// watchForget is how long an issue or pull request is kept that the organizer has not seen since:
// closed, or open and unchanged, as most of a backlog is, whose labels a later change is told by.
const watchForget = 3 * 365 * 24 * time.Hour

// watchItem is an open issue or pull request as the organizer last saw it.
type watchItem struct {
	PR       bool      `json:"pr,omitempty"`
	Title    string    `json:"title"`
	Labels   []string  `json:"labels,omitempty"`
	Comments int       `json:"comments,omitempty"` // its comments, and a pull request's reviews
	Draft    bool      `json:"draft,omitempty"`
	Approved bool      `json:"approved,omitempty"`
	Conflict bool      `json:"conflict,omitempty"`
	Failed   bool      `json:"failed,omitempty"` // its checks fail
	Base     string    `json:"base,omitempty"`   // the commit the branch it merges into is at
	Updated  time.Time `json:"updated"`          // as GitHub says
	Seen     time.Time `json:"seen"`             // when the organizer last saw it open
}

// watchRepo is what the organizer saw of one repository.
type watchRepo struct {
	Since  time.Time         `json:"since"` // the latest time GitHub gave, of anything it listed
	Items  map[int]watchItem `json:"items"`
	Merged []int             `json:"merged,omitempty"` // the pull requests whose merge it told
}

// watchQuery asks GitHub for what the triggers look at in one repository: its open issues that
// changed since the last look (all of them, up to 100, at the first), its open pull requests and the
// pull requests merged lately. It costs a few points of GitHub's hourly 5000, which the agents' gh
// shares.
const watchQuery = `query($owner: String!, $name: String!, $since: DateTime) {
  repository(owner: $owner, name: $name) {
    issues(states: OPEN, first: 100, filterBy: {since: $since}, orderBy: {field: UPDATED_AT, direction: DESC}) {
      nodes { number title createdAt updatedAt labels(first: 20) { nodes { name } } comments { totalCount } }
    }
    pullRequests(states: OPEN, first: 50, orderBy: {field: UPDATED_AT, direction: DESC}) {
      nodes { number title createdAt updatedAt isDraft mergeable reviewDecision
        labels(first: 20) { nodes { name } } comments { totalCount } reviews { totalCount }
        baseRef { target { oid } } commits(last: 1) { nodes { commit { statusCheckRollup { state } } } } }
    }
    merged: pullRequests(states: MERGED, first: 30, orderBy: {field: UPDATED_AT, direction: DESC}) {
      nodes { number title mergedAt }
    }
  }
}`

// watchedItem is an issue or a pull request as watchQuery gets it.
type watchedItem struct {
	Number                         int
	Title                          string
	CreatedAt, UpdatedAt, MergedAt time.Time
	IsDraft                        bool
	Mergeable, ReviewDecision      string
	Labels                         struct{ Nodes []struct{ Name string } }
	Comments, Reviews              struct{ TotalCount int }
	BaseRef                        *struct{ Target struct{ Oid string } }
	Commits                        struct {
		Nodes []struct {
			Commit struct{ StatusCheckRollup *struct{ State string } }
		}
	}
}

// watchedRepo is one repository as watchQuery gets it.
type watchedRepo struct {
	Issues, PullRequests, Merged struct{ Nodes []watchedItem }
}

// watchedRepos are the repositories of the enabled agents with a trigger on GitHub.
func (t *Team) watchedRepos() []string {
	var repos []string
	for _, a := range t.Agents {
		if a.Enabled && a.On.onGitHub() {
			for _, r := range a.Repos {
				if !slices.Contains(repos, r) {
					repos = append(repos, r)
				}
			}
		}
	}
	return repos
}

// watchOnce looks at the watched repositories once, all at once: it brings what happened since
// the last look to the agents whose triggers it matches, and keeps what it saw for the next. It
// says what reached whom, a line each; a repository GitHub did not answer for is looked at again
// next time, from where it was.
func (t *Team) watchOnce(ctx context.Context, now time.Time) (said []string, err error) {
	repos := t.watchedRepos()
	if len(repos) == 0 {
		return nil, nil
	}
	state := map[string]watchRepo{}
	if b, rerr := os.ReadFile(t.Path(watchFile)); rerr == nil {
		if jerr := json.Unmarshal(b, &state); jerr != nil { // a first look again, rather than none ever
			state, err = map[string]watchRepo{}, fmt.Errorf("%s could not be read, so this look only takes note of what is there: %v", watchFile, jerr)
		}
	} else if !os.IsNotExist(rerr) {
		return nil, rerr
	}
	type answer struct {
		repo   string
		events []RepoEvent
		seen   watchRepo
		err    error
	}
	answers := make([]answer, len(repos))
	var wg sync.WaitGroup
	for i, repo := range repos {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a := answer{repo: repo}
			var old *watchRepo
			since := "1970-01-01T00:00:00Z"
			if o, ok := state[repo]; ok {
				old, since = &o, o.Since.Add(-watchNew).UTC().Format(time.RFC3339)
			}
			owner, name, _ := strings.Cut(repo, "/")
			var out struct {
				Data struct{ Repository *watchedRepo }
			}
			if gerr := t.gh(ctx, &out, "api", "graphql", "-f", "query="+watchQuery, "-f", "owner="+owner, "-f", "name="+name, "-f", "since="+since); gerr != nil || out.Data.Repository == nil {
				if gerr == nil {
					gerr = errors.New("GitHub has no such repository, or this machine's gh cannot read it")
				}
				a.err = fmt.Errorf("%s: %v", repo, gerr)
				if old != nil {
					a.seen = *old
				}
			} else {
				a.events, a.seen = watchDiff(repo, old, *out.Data.Repository, now)
			}
			answers[i] = a
		}()
	}
	wg.Wait()
	next := map[string]watchRepo{}
	var events []RepoEvent
	for _, a := range answers {
		err = errors.Join(err, a.err)
		if a.err == nil || a.seen.Items != nil {
			next[a.repo] = a.seen
		}
		events = append(events, a.events...)
	}
	if runs, rerr := loadRuns(t.Config); rerr == nil { // the shifts as they are now, after the minutes GitHub took
		t.Runs = runs
	}
	said, derr := t.onEvents(events, now)
	err = errors.Join(err, derr)
	b, _ := json.MarshalIndent(next, "", "  ")
	return said, errors.Join(err, writeWhole(t.Path(watchFile), append(b, '\n')))
}

// watchDiff tells what happened in the repository since old, from what GitHub lists now, and is
// what to keep for the next look. Without old, it is the first look: nothing happened.
func watchDiff(repo string, old *watchRepo, cur watchedRepo, now time.Time) ([]RepoEvent, watchRepo) {
	next := watchRepo{Items: map[int]watchItem{}}
	if old != nil {
		next.Since, next.Merged = old.Since, old.Merged
		for n, it := range old.Items {
			if now.Sub(it.Seen) < watchForget {
				next.Items[n] = it
			}
		}
	}
	later := func(t time.Time) {
		if t.After(next.Since) {
			next.Since = t
		}
	}
	var events []RepoEvent
	add := func(kind string, n watchedItem, label string, at time.Time) {
		events = append(events, RepoEvent{Kind: kind, Repo: repo, Number: n.Number, Title: n.Title, Label: label, At: at})
	}
	look := func(n watchedItem, pr bool) {
		later(n.CreatedAt)
		later(n.UpdatedAt)
		var prev watchItem
		had := false
		if old != nil {
			prev, had = old.Items[n.Number]
		}
		it := watchItem{PR: pr, Title: n.Title, Comments: n.Comments.TotalCount + n.Reviews.TotalCount, Draft: n.IsDraft, Updated: n.UpdatedAt, Seen: now}
		for _, l := range n.Labels.Nodes {
			it.Labels = append(it.Labels, l.Name)
		}
		it.Approved = n.ReviewDecision == "APPROVED" || slices.ContainsFunc(it.Labels, func(l string) bool { return strings.EqualFold(l, ApprovedLabel) })
		switch n.Mergeable {
		case "CONFLICTING":
			it.Conflict = true
		case "MERGEABLE":
		default: // GitHub has not worked it out yet: as it was
			it.Conflict = prev.Conflict
		}
		if cs := n.Commits.Nodes; len(cs) > 0 && cs[0].Commit.StatusCheckRollup != nil {
			s := cs[0].Commit.StatusCheckRollup.State
			it.Failed = s == "FAILURE" || s == "ERROR"
		}
		if n.BaseRef != nil {
			it.Base = n.BaseRef.Target.Oid
		}
		next.Items[n.Number] = it
		kind := func(k string) string {
			if pr {
				return "pr-" + k
			}
			return "issue-" + k
		}
		switch {
		case old == nil:
		case !had: // new, unless it is an old one the organizer had not seen yet
			if old.Since.IsZero() || n.CreatedAt.After(old.Since.Add(-watchNew)) {
				add(kind("opened"), n, "", n.CreatedAt)
				if pr && !n.IsDraft {
					add("pr-ready", n, "", n.CreatedAt)
				}
				for _, l := range it.Labels {
					add(kind("labeled"), n, l, n.CreatedAt)
				}
			}
		default:
			for _, l := range it.Labels {
				if !slices.ContainsFunc(prev.Labels, func(p string) bool { return strings.EqualFold(p, l) }) {
					add(kind("labeled"), n, l, n.UpdatedAt)
				}
			}
			if it.Comments > prev.Comments {
				add(kind("commented"), n, "", n.UpdatedAt)
			}
			if !pr {
				break
			}
			if prev.Draft && !it.Draft {
				add("pr-ready", n, "", n.UpdatedAt)
			}
			if it.Approved && !prev.Approved {
				add("pr-approved", n, "", n.UpdatedAt)
			}
			if it.Conflict && !prev.Conflict {
				add("pr-conflict", n, "", now)
			}
			if it.Failed && !prev.Failed {
				add("pr-checks-failed", n, "", now)
			}
			if it.Base != "" && prev.Base != "" && it.Base != prev.Base {
				add("base-moved", n, "", now)
			}
		}
	}
	for _, n := range cur.Issues.Nodes {
		look(n, false)
	}
	for _, n := range cur.PullRequests.Nodes {
		look(n, true)
	}
	for _, n := range cur.Merged.Nodes {
		later(n.MergedAt)
		delete(next.Items, n.Number)
		if slices.Contains(next.Merged, n.Number) {
			continue
		}
		next.Merged = append(next.Merged, n.Number)
		if old != nil && (old.Since.IsZero() || n.MergedAt.After(old.Since.Add(-watchNew))) {
			add("pr-merged", n, "", n.MergedAt)
		}
	}
	if len(next.Merged) > 100 { // GitHub lists the last 30: the rest are long told
		next.Merged = next.Merged[len(next.Merged)-100:]
	}
	if next.Since.IsZero() {
		next.Since = now
	}
	return events, next
}

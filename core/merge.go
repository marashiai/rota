package core

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// rota merge is a guarded merge, for an agent whose role merges while gh pr merge is not
// allowed to it: what a pull request depends on is checked by rota, not left to the agent's
// reading of notes. It merges a pull request only when it does not carry the team's
// hold_label itself, when each pull request or issue its description says it "Depends on" is
// merged or closed and does not carry that label either, when it is not stacked on another
// open pull request's branch. One behind its base merges too: it is checked with the base after the
// merge, as a repository that must have its pull requests up to date says in GitHub, whose refusal
// stops the merge. Before it merges one that others are stacked on, it points those at its base, so GitHub does not close them when its branch goes; after, it rebases them onto the
// merge (restack), so each lists only its own commits. Whether the merged branch is deleted is
// the repository's setting.

var (
	dependsOn = regexp.MustCompile(`(?im)^\W*depends on\b:?(.*)$`)
	issueRef  = regexp.MustCompile(`([A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)?)?#(\d+)`)
	fullRef   = regexp.MustCompile(`^([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+#\d+): `)
)

type ghItem struct {
	Number      int    `json:"number"`
	State       string `json:"state"`
	IsDraft     bool   `json:"isDraft"`
	BaseRefName string `json:"baseRefName"`
	HeadRefName string `json:"headRefName"`
	HeadRefOid  string `json:"headRefOid"`
	IsCross     bool   `json:"isCrossRepository"` // from a fork
	Body        string `json:"body"`
	Labels      []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

// has says whether it carries the label; GitHub's label names ignore case.
func (g ghItem) has(label string) bool {
	return slices.ContainsFunc(g.Labels, func(l struct {
		Name string `json:"name"`
	}) bool {
		return strings.EqualFold(l.Name, label)
	})
}

// Merge squash-merges the pull request ref names (<repository>#<n>) at head, the commit the
// agent checked, when what it depends on is in place; otherwise it says what it waits for.
func (t *Team) Merge(ctx context.Context, ref, head string) (string, error) {
	repo, n, err := t.parseRef(ref, "")
	if err != nil {
		return "", err
	}
	o, err := t.merge(ctx, repo, n, head, false)
	return o.text(), err
}

// mergeAsHuman squash-merges a pull request because the human said so, in the chat: at its head
// as it is now, a draft marked ready first, whatever holds the team back from it (its rollout,
// what it depends on, its base having moved on, the human's own leave-alone), its labels left as
// they are, so its rollout is still followed up. One stacked on another open pull request merges
// into that one's branch, as its card says. rota merge, which agents run, never gets here: only a
// card the human applies does.
func (t *Team) mergeAsHuman(ctx context.Context, repo string, n int) (mergeOutcome, error) {
	return t.merge(ctx, repo, n, "", true)
}

// mergeOutcome is what a merge did: what it merged into which branch, and what became of the pull
// requests stacked on it: pointed at that branch before the merge, then rebased onto the merge, or
// not, each with why, or left alone as the human asked.
type mergeOutcome struct {
	name, into                  string
	moved, rebased, left, alone []string
	already                     bool // it was merged before: nothing was done
}

// text is all of it, as rota merge says it.
func (o mergeOutcome) text() string {
	if o.name == "" {
		return ""
	}
	msg := fmt.Sprintf("merged %s into %s", o.name, o.into)
	if len(o.moved) == 0 {
		return msg
	}
	msg += fmt.Sprintf("; %s, stacked on it, now point at %s", strings.Join(o.moved, ", "), o.into)
	if len(o.rebased) > 0 {
		msg += fmt.Sprintf("; rebased, so each lists only its own commits: %s", strings.Join(o.rebased, ", "))
	}
	if len(o.left) > 0 {
		msg += fmt.Sprintf("; not rebased, so its base still has to be merged into each: %s", strings.Join(o.left, "; "))
	}
	if len(o.alone) > 0 {
		msg += fmt.Sprintf("; not rebased, as it carries %s: %s", LeaveLabel, strings.Join(o.alone, ", "))
	}
	return msg
}

// short is it in a line of a card, the pull request named as label.
func (o mergeOutcome) short(label string) string {
	if o.already {
		return sentence(label + " was merged into " + o.into + " already")
	}
	msg := "Merged " + label + " into " + o.into
	if len(o.rebased) > 0 {
		msg += "; rebased onto it: " + strings.Join(o.rebased, ", ")
	}
	if len(o.left) > 0 {
		msg += "; not rebased, so its base still has to be merged into each: " + strings.Join(o.left, "; ")
	}
	if len(o.alone) > 0 {
		msg += "; not rebased, as you asked the team to leave it alone: " + strings.Join(o.alone, ", ")
	}
	return sentence(msg)
}

// mergeAgain is GitHub saying a merge may go through when tried again: the head or the base has
// just moved, as when the pull request was just rebased onto the one merged before it.
var (
	mergeAgain = regexp.MustCompile(`(?i)was modified|not mergeable|try the merge again`)
	mergePause = 3 * time.Second
)

func (t *Team) merge(ctx context.Context, repo string, n int, head string, human bool) (mergeOutcome, error) {
	name := repo + "#" + strconv.Itoa(n)
	var pr ghItem
	view := func() error {
		return t.gh(ctx, &pr, "pr", "view", strconv.Itoa(n), "-R", repo, "--json", "number,state,isDraft,baseRefName,headRefName,headRefOid,isCrossRepository,body,labels")
	}
	if err := view(); err != nil {
		return mergeOutcome{}, err
	}
	switch {
	case human && pr.State == "MERGED": // as another card or the team did meanwhile
		return mergeOutcome{name: name, into: pr.BaseRefName, already: true}, nil
	case pr.State != "OPEN":
		return mergeOutcome{}, fmt.Errorf("%s is %s", name, strings.ToLower(pr.State))
	case human: // what holds the team back is the human's to overrule
	case pr.IsDraft:
		return mergeOutcome{}, fmt.Errorf("%s is a draft: an agent is working on it, and it merges once that agent marks it ready for review", name)
	case pr.has(LeaveLabel):
		return mergeOutcome{}, fmt.Errorf("%s carries %s: the human asked the team to leave it alone", name, LeaveLabel)
	case t.HoldLabel != "" && pr.has(t.HoldLabel):
		return mergeOutcome{}, fmt.Errorf("%s carries %s: it is not merged until that label is removed", name, t.HoldLabel)
	case len(head) < 7 || !strings.HasPrefix(pr.HeadRefOid, head):
		return mergeOutcome{}, fmt.Errorf("%s's head is %.7s, not the commit you checked (%s): check it again", name, pr.HeadRefOid, head)
	}
	if !human { // the human may merge one into the branch of the one it is stacked on
		var under []ghItem
		if err := t.gh(ctx, &under, "pr", "list", "-R", repo, "--state", "open", "--head", pr.BaseRefName, "--limit", "1000", "--json", "number"); err != nil {
			return mergeOutcome{}, err
		}
		if len(under) > 0 {
			return mergeOutcome{}, fmt.Errorf("%s is stacked on #%d (its base, %s, is that pull request's branch): merge #%d first", name, under[0].Number, pr.BaseRefName, under[0].Number)
		}
	}
	if !human {
		var waits []string
		for _, m := range dependsOnRefs(pr.Body) {
			drepo, dn, err := t.parseRef(m, repo)
			if err != nil {
				waits = append(waits, fmt.Sprintf("%s (%v)", m, err))
				continue
			}
			if why := t.unmet(ctx, drepo, dn); why != "" {
				waits = append(waits, why)
			}
		}
		if len(waits) > 0 {
			return mergeOutcome{}, fmt.Errorf("%s waits for: %s", name, strings.Join(waits, "; "))
		}
	} else if pr.IsDraft {
		if err := t.gh(ctx, nil, "pr", "ready", strconv.Itoa(n), "-R", repo); err != nil {
			return mergeOutcome{}, fmt.Errorf("%s is a draft, and marking it ready did not work: %w", name, err)
		}
	}
	var above []ghItem // none on a fork's branch: one here with the same name is another branch
	if err := t.gh(ctx, &above, "pr", "list", "-R", repo, "--state", "open", "--base", pr.HeadRefName, "--limit", "1000", "--json", stackFields); err != nil {
		return mergeOutcome{}, err
	} else if pr.IsCross {
		above = nil
	}
	var moved []string
	var movedPRs []int
	// back points those stacked on it at its branch again when it does not merge after all: left on
	// its base, one would bring its commits in when merged itself, without it.
	back := func(err error) error {
		var still []string
		for _, a := range movedPRs {
			if berr := t.gh(context.WithoutCancel(ctx), nil, "pr", "edit", strconv.Itoa(a), "-R", repo, "--base", pr.HeadRefName); berr != nil {
				still = append(still, "#"+strconv.Itoa(a))
			}
		}
		if len(still) > 0 {
			return fmt.Errorf("%w; %s still point at %s instead of %s: point them back before merging them", err, strings.Join(still, ", "), pr.BaseRefName, pr.HeadRefName)
		}
		return err
	}
	for _, a := range above {
		if err := t.gh(ctx, nil, "pr", "edit", strconv.Itoa(a.Number), "-R", repo, "--base", pr.BaseRefName); err != nil {
			return mergeOutcome{}, back(fmt.Errorf("pointing #%d at %s before the merge: %w", a.Number, pr.BaseRefName, err))
		}
		moved, movedPRs = append(moved, "#"+strconv.Itoa(a.Number)), append(movedPRs, a.Number)
	}
	base := pr.BaseRefName
	for try := 1; ; try++ {
		err := t.gh(ctx, nil, "pr", "merge", strconv.Itoa(n), "-R", repo, "--squash", "--match-head-commit", pr.HeadRefOid)
		if err == nil {
			break
		}
		if !human || try == 3 || !mergeAgain.MatchString(err.Error()) {
			return mergeOutcome{}, back(err)
		}
		time.Sleep(mergePause) // GitHub has not caught up with a push yet: the head it merges is read again
		if err := view(); err != nil {
			return mergeOutcome{}, back(err)
		}
		if pr.State == "MERGED" { // the try GitHub answered with an error went through all the same
			break
		}
		if pr.State != "OPEN" || pr.BaseRefName != base {
			return mergeOutcome{}, back(fmt.Errorf("%s changed while it was being merged: it is %s, into %s now", name, strings.ToLower(pr.State), pr.BaseRefName))
		}
	}
	o := mergeOutcome{name: name, into: pr.BaseRefName, moved: moved}
	if len(moved) == 0 {
		return o, nil
	}
	var merged struct {
		MergeCommit struct {
			Oid string `json:"oid"`
		} `json:"mergeCommit"`
	}
	if err := t.gh(ctx, &merged, "pr", "view", strconv.Itoa(n), "-R", repo, "--json", "mergeCommit"); err != nil {
		o.left = append(o.left, fmt.Sprintf("all (%v)", err))
	} else if merged.MergeCommit.Oid == "" {
		o.left = append(o.left, "all (GitHub gave no merge commit)")
	} else {
		o.rebased, o.left, o.alone = t.restack(ctx, repo, pr.HeadRefOid, merged.MergeCommit.Oid, above)
	}
	return o, nil
}

// dependsOnRefs are the pull requests and issues a description's "Depends on" lines name, as
// written: <repository>#<n> or #<n>.
func dependsOnRefs(body string) []string {
	var out []string
	for _, line := range dependsOn.FindAllStringSubmatch(body, -1) {
		for _, m := range issueRef.FindAllString(line[1], -1) {
			out = append(out, m)
		}
	}
	return out
}

// dependencies are those of a description in repo, as owner/name#n; one that names no
// repository of the team is left out.
func (t *Team) dependencies(body, repo string) []string {
	var out []string
	for _, m := range dependsOnRefs(body) {
		if drepo, dn, err := t.parseRef(m, repo); err == nil {
			out = append(out, drepo+"#"+strconv.Itoa(dn))
		}
	}
	return out
}

// unmet says why a pull request or issue that one depends on is not in place, or "".
func (t *Team) unmet(ctx context.Context, repo string, n int) string {
	name := repo + "#" + strconv.Itoa(n)
	var it ghItem
	kind := "pull request"
	if err := t.gh(ctx, &it, "pr", "view", strconv.Itoa(n), "-R", repo, "--json", "state,labels"); err != nil {
		kind = "issue"
		if err := t.gh(ctx, &it, "issue", "view", strconv.Itoa(n), "-R", repo, "--json", "state,labels"); err != nil {
			return fmt.Sprintf("%s (cannot be read: %v)", name, err)
		}
	}
	switch {
	case kind == "pull request" && it.State == "CLOSED":
		return name + " was closed without merging"
	case kind == "pull request" && it.State != "MERGED":
		return name + " is not merged"
	case kind == "issue" && it.State != "CLOSED":
		return name + " is open"
	case t.HoldLabel != "" && it.has(t.HoldLabel):
		return name + " carries " + t.HoldLabel
	}
	return ""
}

// parseRef reads <repository>#<n>, the repository named as resolveRepo reads it among the
// team's; a bare #<n> is in in.
func (t *Team) parseRef(ref, in string) (string, int, error) {
	m := issueRef.FindStringSubmatch(strings.TrimSpace(ref))
	if m == nil || m[0] != strings.TrimSpace(ref) {
		return "", 0, fmt.Errorf("%q is not <repository>#<number>", ref)
	}
	n, _ := strconv.Atoi(m[2])
	if m[1] == "" {
		if in == "" {
			return "", 0, fmt.Errorf("%q names no repository", ref)
		}
		return in, n, nil
	}
	repo, ok := resolveRepo(t.teamRepos(), m[1])
	if !ok {
		return "", 0, fmt.Errorf("no repository of the team is %q", m[1])
	}
	return repo, n, nil
}

package core

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// rota delete-branch is a guarded delete, so an agent can remove its own remote branch once
// it is done with it: the agent decides when, rota checks that it is safe. It deletes only a
// branch named <agent>/..., every pull request from which is merged or closed, whose tip is the
// head of one of them (no work was pushed after), and that no open pull request is based on.
// GitHub can restore it from the pull request's page.

// DeleteBranch deletes branch in the team's repository repoName for agent, or says why not.
func (t *Team) DeleteBranch(ctx context.Context, agent, repoName, branch string) (string, error) {
	repo, ok := resolveRepo(t.teamRepos(), repoName)
	if !ok {
		return "", fmt.Errorf("no repository of the team is %q", repoName)
	}
	if !strings.HasPrefix(branch, agent+"/") {
		return "", fmt.Errorf("%s is not yours: you delete only branches named %s/<topic>", branch, agent)
	}
	var open, prs []ghItem // open ones asked for apart, so no limit on the history can hide one
	if err := t.gh(ctx, &open, "pr", "list", "-R", repo, "--head", branch, "--state", "open", "--limit", "1000", "--json", "number,state,isCrossRepository"); err != nil {
		return "", err
	}
	if err := t.gh(ctx, &prs, "pr", "list", "-R", repo, "--head", branch, "--state", "all", "--limit", "1000", "--json", "number,state,headRefOid,isCrossRepository"); err != nil {
		return "", err
	}
	var heads, done []string
	for _, pr := range append(open, prs...) {
		switch {
		case pr.IsCross: // a fork's branch of the same name is another branch
			continue
		case pr.State == "OPEN":
			return "", fmt.Errorf("%s#%d, from %s, is still open", repo, pr.Number, branch)
		}
		heads = append(heads, pr.HeadRefOid)
		done = append(done, "#"+strconv.Itoa(pr.Number)+" "+strings.ToLower(pr.State))
	}
	if len(heads) == 0 {
		return "", fmt.Errorf("no pull request was opened from %s in %s: only a branch whose pull request is merged or closed is deleted", branch, repo)
	}
	var above []ghItem
	if err := t.gh(ctx, &above, "pr", "list", "-R", repo, "--base", branch, "--state", "open", "--limit", "1000", "--json", "number"); err != nil {
		return "", err
	}
	if len(above) > 0 {
		return "", fmt.Errorf("%s#%d is based on %s: deleting the branch would close it", repo, above[0].Number, branch)
	}
	ref := "repos/" + repo + "/git/refs/heads/" + escapePath(branch)
	var tip struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := t.gh(ctx, &tip, "api", ref); err != nil {
		return "", fmt.Errorf("reading the tip of %s in %s (deleted already?): %w", branch, repo, err)
	}
	if !slices.Contains(heads, tip.Object.SHA) {
		return "", fmt.Errorf("%s's tip %.8s is the head of none of its pull requests (%s): it holds work pushed since; it was not deleted", branch, tip.Object.SHA, strings.Join(done, ", "))
	}
	if err := t.gh(ctx, nil, "api", "-X", "DELETE", ref); err != nil {
		return "", err
	}
	return fmt.Sprintf("deleted %s in %s (%s); GitHub can restore it from the pull request's page", branch, repo, strings.Join(done, ", ")), nil
}

// escapePath escapes each segment of a branch name for a URL path, keeping its slashes.
func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

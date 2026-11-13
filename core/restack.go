package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// After a squash merge, the pull requests stacked on the merged one still carry its commits,
// which the base now holds only as one new commit: GitHub would list them, and their whole
// change, as theirs. restack makes each of them again on the merge, its own commits only, the
// way "git rebase --onto" does, and then those stacked on each the same way, so every pull
// request of the stack shows only what it changes. The agents merge, never rebase; rota does
// this itself, in its own clone of the repository (reposDir), made with clone_cmd.

// gitTimeout bounds one git command: a first fetch of a large repository takes a while.
var gitTimeout = 5 * time.Minute

// stackFields are what restack needs of each pull request GitHub lists.
const stackFields = "number,headRefName,headRefOid,isCrossRepository,labels"

// restack rebases the pull requests prs, stacked on a branch whose head was old, onto onto, and
// then those stacked on each of them. It names those it rebased, those it left as they were, each
// with why, and those it left alone as the human asked. One whose commits do not go onto onto
// cleanly is left as it is, with those stacked on it: merging its base in is left to the team.
func (t *Team) restack(ctx context.Context, repo, old, onto string, prs []ghItem) (done, left, alone []string) {
	dir, err := t.repoClone(ctx, repo)
	if err != nil {
		for _, p := range prs {
			left = append(left, fmt.Sprintf("#%d (%v)", p.Number, err))
		}
		return nil, left, nil
	}
	var walk func(old, onto string, prs []ghItem)
	walk = func(old, onto string, prs []ghItem) {
		for _, p := range prs {
			ref := "#" + strconv.Itoa(p.Number)
			if p.has(LeaveLabel) {
				alone = append(alone, ref)
				continue
			}
			head, err := t.replay(ctx, dir, old, onto, p)
			if err == nil {
				_, err = t.git(ctx, dir, "", nil, "push", "-q", "--force-with-lease=refs/heads/"+p.HeadRefName+":"+p.HeadRefOid,
					"origin", head+":refs/heads/"+p.HeadRefName)
			}
			if err != nil {
				left = append(left, ref+" ("+err.Error()+")")
				continue
			}
			done = append(done, ref)
			var next []ghItem
			if err := t.gh(ctx, &next, "pr", "list", "-R", repo, "--state", "open", "--base", p.HeadRefName, "--limit", "1000", "--json", stackFields); err != nil {
				left = append(left, "those stacked on "+ref+" ("+err.Error()+")")
				continue
			}
			walk(p.HeadRefOid, head, next)
		}
	}
	walk(old, onto, prs)
	return done, left, alone
}

// replay makes p's own commits, those its head has and old has not, again on onto, each with its
// author, date and message, and gives the new head. It refuses when one does not apply cleanly,
// and when the result is not what merging onto into p gives: then a merge in p's own history
// held a change the replay would lose.
func (t *Team) replay(ctx context.Context, dir, old, onto string, p ghItem) (string, error) {
	if p.IsCross {
		return "", errors.New("its branch is in a fork")
	}
	var missing []string
	for _, c := range []string{old, onto, p.HeadRefOid} {
		if _, err := t.git(ctx, dir, "", nil, "cat-file", "-e", c+"^{commit}"); err != nil {
			missing = append(missing, c)
		}
	}
	if len(missing) > 0 {
		if _, err := t.git(ctx, dir, "", nil, append([]string{"fetch", "-q", "--no-tags", "origin"}, missing...)...); err != nil {
			return "", err
		}
	}
	forks, err := t.git(ctx, dir, "", nil, "merge-base", "--all", p.HeadRefOid, old)
	if err != nil || forks == "" {
		return "", fmt.Errorf("it shares no history with what it was stacked on: %v", err)
	}
	fork, more, _ := strings.Cut(forks, "\n")
	if more != "" { // a criss-cross: the check below could pass with a change lost
		return "", errors.New("it branched from what it was stacked on at more than one point")
	}
	want, err := t.mergeTree(ctx, dir, fork, onto, p.HeadRefOid)
	if err != nil {
		return "", err
	}
	list, err := t.git(ctx, dir, "", nil, "rev-list", "--reverse", "--topo-order", "--no-merges", p.HeadRefOid, "^"+old)
	if err != nil {
		return "", err
	}
	commits := strings.Fields(list)
	if len(commits) == 0 {
		return "", errors.New("it has no commits of its own")
	}
	head := onto
	for _, c := range commits {
		tree, err := t.mergeTree(ctx, dir, c+"^", head, c)
		if err != nil {
			return "", fmt.Errorf("%.8s: %w", c, err)
		}
		was, _ := t.git(ctx, dir, "", nil, "rev-parse", c+"^^{tree}", c+"^{tree}")
		before, after, _ := strings.Cut(was, "\n")
		if now, _ := t.git(ctx, dir, "", nil, "rev-parse", head+"^{tree}"); now == tree && before != after {
			continue // already in what it goes onto, as git rebase drops it; one made empty on purpose stays
		}
		raw, err := t.run(ctx, dir, "", nil, "cat-file", "commit", c)
		if err != nil {
			return "", err
		}
		hdr, msg, _ := strings.Cut(raw, "\n\n")
		env, enc, err := authorEnv(hdr)
		if err != nil {
			return "", fmt.Errorf("%.8s: %w", c, err)
		}
		if enc == "" {
			enc = "UTF-8" // what git means by no encoding, whatever this machine's setting
		}
		args := []string{"-c", "i18n.commitEncoding=" + enc, "commit-tree", tree, "-p", head} // its message's bytes are in enc
		if head, err = t.git(ctx, dir, msg, env, args...); err != nil {
			return "", err
		}
	}
	if head == onto {
		return "", errors.New("all it changes is already in what it goes onto")
	}
	if got, _ := t.git(ctx, dir, "", nil, "rev-parse", head+"^{tree}"); got != want {
		return "", errors.New("its commits made again differ from merging its base into it: a merge of its own holds a change")
	}
	return head, nil
}

// mergeTree is the tree that merging theirs into ours gives, from base, or why it conflicts.
func (t *Team) mergeTree(ctx context.Context, dir, base, ours, theirs string) (string, error) {
	out, err := t.git(ctx, dir, "", nil, "merge-tree", "--write-tree", "--name-only", "--no-messages", "--merge-base="+base, ours, theirs)
	if err == nil {
		return out, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 { // the tree, then the conflicting files
		files := strings.Fields(out)
		if len(files) > 1 {
			files = files[1:]
		}
		return "", fmt.Errorf("it conflicts with its new base in %s", strings.Join(files, ", "))
	}
	return "", fmt.Errorf("git merge-tree: %w", err)
}

// authorEnv keeps a commit's author and date, from its header, for the commit made again, and
// gives the encoding its message is in when the header names one.
func authorEnv(hdr string) (env []string, enc string, err error) {
	for _, line := range strings.Split(hdr, "\n") {
		if e, ok := strings.CutPrefix(line, "encoding "); ok {
			enc = e
		}
		rest, ok := strings.CutPrefix(line, "author ")
		if !ok || env != nil {
			continue
		}
		lt, gt := strings.LastIndex(rest, " <"), strings.LastIndex(rest, "> ")
		if lt < 0 || gt < lt {
			return nil, "", errors.New("its author line is not as git writes it")
		}
		// @ reads the date as seconds since 1970, whatever the number
		env = []string{"GIT_AUTHOR_NAME=" + rest[:lt], "GIT_AUTHOR_EMAIL=" + rest[lt+2:gt], "GIT_AUTHOR_DATE=@" + rest[gt+2:]}
	}
	if env == nil {
		return nil, "", errors.New("no author in it")
	}
	return env, enc, nil
}

// repoClone is rota's own clone of repo, made with clone_cmd the first time: in a folder of its
// own, then moved into place, so two merges at once never clone into, or remove, the same one.
func (t *Team) repoClone(ctx context.Context, repo string) (string, error) {
	dir := t.Path(filepath.Join(reposDir, filepath.FromSlash(repo)))
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return dir, nil
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dir), ".clone-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	name := repo[strings.LastIndex(repo, "/")+1:]
	into := filepath.Join(tmp, name)
	words := strings.Fields(expand(t.CloneCmd, map[string]string{"{repo}": repo, "{dir}": into, "{name}": name}))
	if len(words) == 0 {
		return "", errors.New("clone_cmd is empty")
	}
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := command(ctx, t.Config, tmp, append(os.Environ(), "GIT_TERMINAL_PROMPT=0"), words[0], words[1:]...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := runTracked(cmd); err != nil {
		return "", fmt.Errorf("cloning %s: %v: %s", repo, err, strings.TrimSpace(tail(stderr.String(), 300)))
	}
	if err := os.Rename(into, dir); err != nil { // another merge may have put its clone there first
		if _, serr := os.Stat(filepath.Join(dir, ".git")); serr != nil {
			return "", fmt.Errorf("putting the clone of %s in place: %w", repo, err)
		}
	}
	return dir, nil
}

// git runs git in dir with stdin and env added, and gives what it printed, trimmed.
func (t *Team) git(ctx context.Context, dir, stdin string, env []string, args ...string) (string, error) {
	out, err := t.run(ctx, dir, stdin, env, args...)
	return strings.TrimSpace(out), err
}

// run is git as it printed, byte for byte.
func (t *Team) run(ctx context.Context, dir, stdin string, env []string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := command(ctx, t.Config, dir, append(append(os.Environ(), "GIT_TERMINAL_PROMPT=0"), env...), "git", args...)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := runTracked(cmd)
	out := stdout.String()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && args[0] == "merge-tree" {
			return out, err
		}
		return out, fmt.Errorf("git %s: %v: %s", args[0], err, strings.TrimSpace(tail(stderr.String(), 300)))
	}
	return out, nil
}

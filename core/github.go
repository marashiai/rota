package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
)

// What rota itself does on GitHub, through gh: the guarded merge (merge.go) and the team's
// labels (labels.go).

// ghTimeout bounds one gh call.
var ghTimeout = time.Minute

// teamRepos are the repositories of every agent, each once.
func (t *Team) teamRepos() []string {
	var repos []string
	for _, a := range t.Agents {
		for _, r := range a.Repos {
			if !slices.Contains(repos, r) {
				repos = append(repos, r)
			}
		}
	}
	return repos
}

// ---- the team's repositories ----

// The team's repositories are default_repos in rota.conf: every agent whose file names no list of
// its own works on them.

var (
	githubLink = regexp.MustCompile(`^(?:(?:https?://)?(?:www\.)?github\.com/|git@github\.com:)([^/?#\s]+/[^/?#\s]+)`)
	repoShape  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9._-]+$`)
)

// RepoOf is the repository s names, as owner/name: owner/name itself or a link to it on GitHub;
// "" when s names none.
func RepoOf(s string) string {
	s = strings.TrimSpace(s)
	if m := githubLink.FindStringSubmatch(s); m != nil {
		s = m[1]
	}
	s = strings.TrimSuffix(s, ".git")
	if _, name, _ := strings.Cut(s, "/"); !repoShape.MatchString(s) || strings.Trim(name, ".") == "" {
		return ""
	}
	return s
}

// AddRepo adds the repository that repo names to the team's, at position at (out of range: at the
// end), as GitHub names it, once GitHub, asked through this machine's gh, says it exists: a name
// mistyped would fail the clone before every agent's shift. It says the name it added.
func (t *Team) AddRepo(ctx context.Context, repo string, at int) (string, error) {
	name := RepoOf(repo)
	if name == "" {
		return "", fmt.Errorf("%q is not a repository: write owner/name, or paste its link on GitHub", strings.TrimSpace(repo))
	}
	has := func(l []string, name string) bool {
		return slices.ContainsFunc(l, func(r string) bool { return strings.EqualFold(r, name) })
	}
	if has(t.Def.Repos, name) {
		return "", fmt.Errorf("%s is one of the team's repositories already", name)
	}
	var gh struct{ NameWithOwner string }
	if err := t.gh(ctx, &gh, "repo", "view", name, "--json", "nameWithOwner"); err != nil || gh.NameWithOwner == "" {
		if err != nil && !strings.Contains(err.Error(), "Could not resolve") {
			return "", fmt.Errorf("GitHub could not say whether %s exists, so it was not added: %v", name, err)
		}
		return "", fmt.Errorf("GitHub has no repository %s that gh on this machine can see", name)
	}
	name = gh.NameWithOwner // its case, or its new name after a rename
	err := t.changeRepos(func(l List) (List, error) {
		if has(l, name) {
			return nil, fmt.Errorf("%s is one of the team's repositories already", name)
		}
		if at < 0 || at > len(l) {
			at = len(l)
		}
		return slices.Insert(l, at, name), nil
	})
	if err != nil {
		return "", err
	}
	return name, nil
}

// RemoveRepo takes repo off the team's repositories, and says its name as the list had it. Before
// its next shift an agent no longer working on it removes its checkout, unless that holds work found
// nowhere else.
func (t *Team) RemoveRepo(repo string) (string, error) {
	name := strings.TrimSpace(repo)
	err := t.changeRepos(func(l List) (List, error) {
		i := slices.IndexFunc(l, func(r string) bool { return strings.EqualFold(r, name) })
		if i < 0 {
			return nil, fmt.Errorf("%s is not one of the team's repositories", name)
		}
		name = l[i]
		return slices.DeleteFunc(l, func(r string) bool { return strings.EqualFold(r, name) }), nil
	})
	return name, err
}

// changeRepos rewrites default_repos in rota.conf through change, which gets the list as the file
// has it, all under the file's lock: a change made meanwhile, in the console or another page, stays.
func (t *Team) changeRepos(change func(List) (List, error)) error {
	path := t.Path(t.File)
	return changeLocked(path, func() error {
		b, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		kv, err := parseKV(string(b))
		if err != nil { // read as no list, the list would be lost
			return fmt.Errorf("%s: %w", t.File, err)
		}
		var l List
		l.UnmarshalText([]byte(kv["default_repos"]))
		if l, err = change(l); err != nil {
			return err
		}
		v, _ := l.MarshalText()
		return writeWhole(path, []byte(withConfKey(string(b), "default_repos", string(v))))
	})
}

// gh runs gh with the environment it was given (a shift's GH_TOKEN), decoding its JSON into
// out when out is not nil; a *string gets what it printed, as it is.
func (t *Team) gh(ctx context.Context, out any, args ...string) error {
	return t.ghWith(ctx, os.Environ(), out, args...)
}

// ghWith runs gh as gh does, with env as its environment.
func (t *Team) ghWith(ctx context.Context, env []string, out any, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, ghTimeout)
	defer cancel()
	cmd := command(ctx, t.Config, t.Root, env, "gh", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := runTracked(cmd); err != nil {
		return fmt.Errorf("gh %s: %v: %s", strings.Join(args[:min(2, len(args))], " "), err, strings.TrimSpace(tail(stderr.String(), 300)))
	}
	switch out := out.(type) {
	case nil:
		return nil
	case *string:
		*out = stdout.String()
		return nil
	default:
		return json.Unmarshal(stdout.Bytes(), out)
	}
}

package review

import (
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// Ref names a pull request.
type Ref struct {
	Owner, Name string
	Number      int
}

// Repo is owner/name.
func (r Ref) Repo() string { return r.Owner + "/" + r.Name }

func (r Ref) String() string { return fmt.Sprintf("%s/%s#%d", r.Owner, r.Name, r.Number) }

var (
	refURL   = regexp.MustCompile(`^https?://github\.com/([\w.-]+)/([\w.-]+)/pull/(\d+)`)
	refShort = regexp.MustCompile(`^([\w.-]+)/([\w.-]+)#(\d+)$`)
	repoName = regexp.MustCompile(`^([\w.-]+)/([\w.-]+)$`)
)

// ParseRef reads owner/name#N, a pull request URL, or a bare number with repo (owner/name).
func ParseRef(s, repo string) (Ref, error) {
	s = strings.TrimSpace(s)
	if m := refURL.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[3])
		return Ref{m[1], m[2], n}, nil
	}
	if m := refShort.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[3])
		return Ref{m[1], m[2], n}, nil
	}
	if n, err := strconv.Atoi(strings.TrimPrefix(s, "#")); err == nil && n > 0 {
		m := repoName.FindStringSubmatch(repo)
		if m == nil {
			return Ref{}, fmt.Errorf("pull request %s: which repository? give owner/name#%d", s, n)
		}
		return Ref{m[1], m[2], n}, nil
	}
	return Ref{}, fmt.Errorf("not a pull request: %q (want owner/name#N, a pull request URL, or a number)", s)
}

var remoteRepo = regexp.MustCompile(`[:/]([\w.-]+)/([\w.-]+?)(?:\.git)?/?$`)

// RepoFromGit is the owner/name of the origin remote of the git checkout in dir ("" for the
// current directory), or "" when there is none.
func RepoFromGit(dir string) string {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	url := strings.TrimSpace(string(out))
	if m := remoteRepo.FindStringSubmatch(url); m != nil {
		return m[1] + "/" + m[2]
	}
	return ""
}

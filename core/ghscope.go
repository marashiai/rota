package core

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// The Team reaches GitHub with gh, within rota's scope only: the team's repositories and the
// work in them, their pull requests, issues, releases, workflow runs and labels. It reads them
// with a tool, and changes them with a card the human applies. ghScope checks a gh command it
// reads with or proposes. Only what is listed passes: each command and each of its flags, in its
// long form, a repository named with --repo and never by a link, and an api path under one of the
// team's repositories that is its work, not its settings. Outside the scope are other repositories,
// a repository's settings, other hosts, and this machine: a file gh would read or write here, a
// browser or an editor it would open.

// ghFlags lists flags, "--name" for one without a value and "--name=" for one with.
func ghFlags(names ...string) map[string]bool {
	m := map[string]bool{}
	for _, n := range names {
		name, valued := strings.CutSuffix(n, "=")
		m[name] = valued
	}
	return m
}

// ghOut are the flags that shape what a read prints.
var ghOut = []string{"--json=", "--jq=", "--template="}

func withOut(names ...string) map[string]bool {
	return ghFlags(append(slices.Clone(ghOut), names...)...)
}

// ghReads and ghChanges are what gh may do: each command, with the flags it may get.
var (
	ghReads = map[string]map[string]bool{
		"pr view":        withOut("--comments"),
		"pr list":        withOut("--state=", "--limit=", "--label=", "--base=", "--head=", "--author=", "--assignee=", "--search=", "--app=", "--draft"),
		"pr diff":        ghFlags("--name-only", "--patch"),
		"pr checks":      withOut("--required"),
		"pr status":      withOut("--conflict-status"),
		"issue view":     withOut("--comments"),
		"issue list":     withOut("--state=", "--limit=", "--label=", "--assignee=", "--author=", "--mention=", "--milestone=", "--search=", "--app="),
		"issue status":   withOut(),
		"release view":   withOut(),
		"release list":   withOut("--limit=", "--exclude-drafts", "--exclude-pre-releases", "--order="),
		"run view":       withOut("--log", "--log-failed", "--verbose", "--job=", "--attempt=", "--exit-status"),
		"run list":       withOut("--limit=", "--branch=", "--workflow=", "--status=", "--event=", "--user=", "--commit=", "--created="),
		"workflow view":  ghFlags("--yaml", "--ref="),
		"workflow list":  withOut("--all", "--limit="),
		"label list":     withOut("--limit=", "--search=", "--sort=", "--order="),
		"search prs":     withOut("--limit=", "--state=", "--label=", "--author=", "--assignee=", "--sort=", "--order=", "--created=", "--updated=", "--merged-at=", "--closed=", "--draft", "--merged", "--base=", "--head=", "--review=", "--checks=", "--match=", "--involves=", "--mentions="),
		"search issues":  withOut("--limit=", "--state=", "--label=", "--author=", "--assignee=", "--sort=", "--order=", "--created=", "--updated=", "--closed=", "--match=", "--involves=", "--mentions="),
		"search commits": withOut("--limit=", "--author=", "--committer=", "--hash=", "--sort=", "--order=", "--author-date=", "--committer-date="),
		"api":            ghFlags("--method=", "--jq=", "--template=", "--paginate", "--slurp", "--include", "--silent", "--header=", "--raw-field=", "--field="),
	}
	ghChanges = map[string]map[string]bool{
		"pr ready":         ghFlags("--undo"),
		"pr edit":          ghFlags("--title=", "--body=", "--base=", "--add-label=", "--remove-label=", "--add-assignee=", "--remove-assignee=", "--add-reviewer=", "--remove-reviewer=", "--milestone=", "--remove-milestone"),
		"pr comment":       ghFlags("--body=", "--edit-last", "--create-if-none", "--delete-last", "--yes"),
		"pr close":         ghFlags("--comment=", "--delete-branch"),
		"pr reopen":        ghFlags("--comment="),
		"pr review":        ghFlags("--approve", "--request-changes", "--comment", "--body="),
		"pr lock":          ghFlags("--reason="),
		"pr unlock":        ghFlags(),
		"pr update-branch": ghFlags("--rebase"),
		"issue create":     ghFlags("--title=", "--body=", "--label=", "--assignee=", "--milestone="),
		"issue edit":       ghFlags("--title=", "--body=", "--add-label=", "--remove-label=", "--add-assignee=", "--remove-assignee=", "--milestone=", "--remove-milestone"),
		"issue comment":    ghFlags("--body=", "--edit-last", "--create-if-none", "--delete-last", "--yes"),
		"issue close":      ghFlags("--comment=", "--reason="),
		"issue reopen":     ghFlags("--comment="),
		"issue lock":       ghFlags("--reason="),
		"issue unlock":     ghFlags(),
		"issue pin":        ghFlags(),
		"issue unpin":      ghFlags(),
		"issue transfer":   ghFlags(),
		"issue delete":     ghFlags("--yes"),
		"release create":   ghFlags("--title=", "--notes=", "--target=", "--draft", "--prerelease", "--latest", "--generate-notes", "--notes-start-tag=", "--verify-tag"),
		"release edit":     ghFlags("--title=", "--notes=", "--target=", "--tag=", "--draft", "--prerelease", "--latest", "--verify-tag"),
		"release delete":   ghFlags("--yes", "--cleanup-tag"),
		"run rerun":        ghFlags("--failed", "--debug", "--job="),
		"run cancel":       ghFlags(),
		"run delete":       ghFlags(),
		"workflow run":     ghFlags("--ref=", "--raw-field="),
		"label create":     ghFlags("--color=", "--description=", "--force"),
		"label edit":       ghFlags("--name=", "--color=", "--description="),
		"label delete":     ghFlags("--yes"),
		"label clone":      ghFlags("--force"),
		"api":              ghFlags("--method=", "--jq=", "--silent", "--include", "--header=", "--raw-field=", "--field="),
	}
)

// ghReadPaths and ghWorkPaths are what gh api may read and change under one of the team's
// repositories: its work, never its settings (hooks, keys, collaborators, secrets, protection).
var (
	ghReadPaths = regexp.MustCompile(`^(pulls|issues|commits|compare|contents|readme|tags|releases|labels|milestones|comments|statuses|check-runs|check-suites|deployments|languages|contributors|events|stats|actions/(runs|jobs|workflows|artifacts)|git/(refs|ref|matching-refs|commits|trees|blobs|tags))(/|$)|^branches$`)
	ghWorkPaths = regexp.MustCompile(`^(pulls|issues|releases|labels|milestones|git/refs|actions/runs)(/|$)|^actions/workflows/[^/]+/dispatches$`)
	// ghElsewhere are what under an issue or a pull request reaches issues of other repositories.
	ghElsewhere = regexp.MustCompile(`(^|/)(sub_issues?|parent|dependencies|timeline|transfer)(/|$)`)
	ghQualifier = regexp.MustCompile(`(?i)(?:^|[\s(])-?(repo|org|user|owner):("[^"]*"|\S+)`)
)

// ghScope checks a gh command, its arguments after gh: one that reads (change false) or one that
// changes (change true) the team's work on GitHub. It gives it as gh is to run it: each repository
// named as the team names it, and each flag's value attached to its flag.
func (t *Team) ghScope(args []string, change bool) ([]string, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("which gh command?")
	}
	cmds, what := ghReads, "reads"
	if change {
		cmds, what = ghChanges, "changes"
	}
	cmd, start := args[0], 1
	if cmd != "api" && len(args) > 1 {
		cmd, start = args[0]+" "+args[1], 2
	}
	allowed, ok := cmds[cmd]
	if !ok {
		known := slices.Sorted(func(yield func(string) bool) {
			for c := range cmds {
				if !yield(c) {
					return
				}
			}
		})
		return nil, fmt.Errorf("gh %s is not a command that %s the team's work on GitHub: those are gh %s", cmd, what, strings.Join(known, ", gh "))
	}
	team := func(name string) (string, error) {
		name = strings.Trim(strings.TrimSuffix(strings.TrimSpace(name), ".git"), `"`)
		repo, ok := resolveRepo(t.teamRepos(), name)
		if !ok {
			return "", fmt.Errorf("%s is not one of the team's repositories (%s): it is outside rota's scope", name, strings.Join(t.teamRepos(), ", "))
		}
		return repo, nil
	}
	searched := func(s string) error { // a search reaches only the team's repositories
		for _, m := range ghQualifier.FindAllStringSubmatch(s, -1) {
			if strings.ToLower(m[1]) != "repo" {
				return fmt.Errorf("%s: in a search reaches beyond the team's repositories: name each with repo:", m[1])
			}
			if _, err := team(m[2]); err != nil {
				return err
			}
		}
		return nil
	}
	out := strings.Fields(cmd)
	var positional []string
	repos, method := 0, ""
	for i := start; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") || a == "-" {
			positional = append(positional, a)
			continue
		}
		flag, value, hasValue := strings.Cut(a, "=")
		if strings.HasPrefix(a, "-R") { // -R x, -Rx, -R=x
			flag, value, hasValue = "--repo", strings.TrimPrefix(a[2:], "="), len(a) > 2
		}
		valued, known := allowed[flag]
		if flag == "--repo" {
			valued, known = true, cmd != "api"
		}
		switch {
		case !strings.HasPrefix(flag, "--"):
			return nil, fmt.Errorf("%s: give gh's flags in their long form, such as --repo or --state", a)
		case !known:
			return nil, fmt.Errorf("gh %s %s is not one rota lets the Team use: it may reach beyond the team's work on GitHub", cmd, flag)
		}
		if valued && !hasValue {
			if i+1 == len(args) {
				return nil, fmt.Errorf("%s needs a value", flag)
			}
			i++
			value, hasValue = args[i], true
		}
		if !valued && hasValue && value != "true" && value != "false" {
			return nil, fmt.Errorf("%s takes no value", flag)
		}
		switch flag {
		case "--repo":
			repo, err := team(value)
			if err != nil {
				return nil, err
			}
			value, repos = repo, repos+1
		case "--method":
			method = strings.ToUpper(value)
		case "--field":
			if _, v, _ := strings.Cut(value, "="); strings.HasPrefix(v, "@") || strings.ContainsAny(v, "{}") {
				return nil, fmt.Errorf("a field read from a file or filled in from this machine: give its value itself, with --raw-field")
			}
		case "--header":
			if name, _, _ := strings.Cut(value, ":"); !strings.EqualFold(strings.TrimSpace(name), "Accept") {
				return nil, fmt.Errorf("only an Accept header may go to GitHub")
			}
		case "--search":
			if err := searched(value); err != nil {
				return nil, err
			}
		}
		if hasValue { // attached, so gh never reads a value as an argument of its own
			out = append(out, flag+"="+value)
		} else {
			out = append(out, flag)
		}
	}
	for _, p := range positional {
		plain, err := url.PathUnescape(p)
		lower := strings.ToLower(plain)
		switch {
		case err != nil || slices.Contains(strings.Split(plain, "/"), "..") || slices.Contains(strings.Split(plain, "/"), "."):
			return nil, fmt.Errorf("%s is not a plain argument: a path or a name without . or ..", p)
		case strings.Contains(plain, "://") || strings.Contains(lower, "github.com"):
			return nil, fmt.Errorf("%s: name it by its number or name with --repo, not by a link", p)
		}
	}
	switch cmd {
	case "api":
		if len(positional) != 1 {
			return nil, fmt.Errorf("gh api takes one endpoint, under repos/<owner>/<name>/ of the team's repositories")
		}
		path, query, _ := strings.Cut(strings.TrimPrefix(positional[0], "/"), "?")
		if strings.ContainsAny(path, "%{}\\:") || strings.ContainsAny(query, "{}") {
			return nil, fmt.Errorf("gh api %s: give the path plainly, without %%, braces, backslashes or colons", positional[0])
		}
		plain := path
		parts := strings.SplitN(plain, "/", 4)
		if len(parts) < 3 || parts[0] != "repos" || strings.Contains(plain, "//") {
			return nil, fmt.Errorf("gh api %s is not under repos/<owner>/<name>/ of a team repository: it is outside rota's scope", positional[0])
		}
		repo, err := team(parts[1] + "/" + parts[2])
		if err != nil {
			return nil, err
		}
		rest := ""
		if len(parts) == 4 {
			rest = strings.TrimSuffix(parts[3], "/")
		}
		if method == "" {
			method = "GET"
			if slices.ContainsFunc(out, func(a string) bool { return strings.HasPrefix(a, "--field=") || strings.HasPrefix(a, "--raw-field=") }) {
				method = "POST"
			}
		}
		switch {
		case !change && method != "GET":
			return nil, fmt.Errorf("gh api with %s changes GitHub: propose it", method)
		case change && method == "GET":
			return nil, fmt.Errorf("gh api with GET only reads: read it yourself")
		case ghElsewhere.MatchString(rest):
			return nil, fmt.Errorf("gh api %s reaches issues of other repositories: it is outside rota's scope", positional[0])
		case !change && !ghReadPaths.MatchString(rest):
			return nil, fmt.Errorf("gh api %s reads the repository's settings, not the team's work: it is outside rota's scope", positional[0])
		case change && !ghWorkPaths.MatchString(rest):
			return nil, fmt.Errorf("gh api %s %s changes the repository's settings, not the team's work: it is outside rota's scope", method, positional[0])
		}
		endpoint := "repos/" + repo
		if rest != "" {
			endpoint += "/" + rest
		}
		if query != "" {
			endpoint += "?" + query
		}
		return append(out, endpoint), nil
	case "search prs", "search issues", "search commits":
		for _, p := range positional {
			if err := searched(p); err != nil {
				return nil, err
			}
		}
		if repos == 0 {
			return nil, fmt.Errorf("gh search reaches beyond the team's repositories: name each with --repo")
		}
		return append(out, positional...), nil
	}
	if repos == 0 {
		return nil, fmt.Errorf("name the repository with --repo <owner/name>, one of the team's: %s", strings.Join(t.teamRepos(), ", "))
	}
	switch cmd {
	case "issue transfer":
		if len(positional) != 2 {
			return nil, fmt.Errorf("gh issue transfer <number> <repository>, one of the team's")
		}
		repo, err := team(positional[1])
		if err != nil {
			return nil, err
		}
		positional[1] = repo
	case "label clone":
		if len(positional) != 1 {
			return nil, fmt.Errorf("gh label clone <repository>, one of the team's")
		}
		repo, err := team(positional[0])
		if err != nil {
			return nil, err
		}
		positional[0] = repo
	case "release create":
		if len(positional) > 1 {
			return nil, fmt.Errorf("gh release create would upload files from this machine: create it without them")
		}
	}
	return append(out, positional...), nil
}

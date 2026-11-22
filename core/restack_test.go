package core

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// After a squash merge, each pull request stacked on the merged one, and each stacked on those,
// is made again on the merge with only its own commits; one that conflicts, or whose branch
// moved since GitHub listed it, is left as it is.
func TestMergeRebasesThePullRequestsStackedOnIt(t *testing.T) {
	root := t.TempDir()
	for k, v := range map[string]string{"GIT_AUTHOR_NAME": "Dev", "GIT_AUTHOR_EMAIL": "dev@x", "GIT_COMMITTER_NAME": "Dev", "GIT_COMMITTER_EMAIL": "dev@x",
		"GIT_CONFIG_GLOBAL": filepath.Join(root, "gitconfig"), "GIT_CONFIG_NOSYSTEM": "1"} {
		t.Setenv(k, v)
	}
	remote, w := filepath.Join(root, "o", "r.git"), filepath.Join(root, "w")
	g := func(dir string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	os.MkdirAll(w, 0o755)
	os.MkdirAll(filepath.Dir(remote), 0o755)
	g(root, "init", "-q", "--bare", "-b", "main", remote)
	g(remote, "config", "uploadpack.allowAnySHA1InWant", "true") // as GitHub serves any commit by its id
	g(w, "init", "-q", "-b", "main")
	g(w, "remote", "add", "origin", remote)
	lines := func(edit map[int]string) string {
		var b strings.Builder
		for i := 1; i <= 10; i++ {
			if s, ok := edit[i]; ok {
				b.WriteString(s + "\n")
			} else {
				b.WriteString("line\n")
			}
		}
		return b.String()
	}
	who := "" // the author, when not the committer
	commit := func(msg, date string, files map[string]string) string {
		t.Helper()
		for name, body := range files {
			write(t, filepath.Join(w, name), body)
		}
		g(w, "add", "-A")
		cmd := exec.Command("git", "-C", w, "commit", "-q", "--allow-empty", "--cleanup=verbatim", "-m", msg)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE="+date)
		if who != "" {
			cmd.Env = append(cmd.Env, "GIT_AUTHOR_NAME="+who, "GIT_AUTHOR_EMAIL=child@x")
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("commit: %v\n%s", err, out)
		}
		return g(w, "rev-parse", "HEAD")
	}
	m0 := commit("start", "1700000000 +0100", map[string]string{"a.txt": lines(nil), "b.txt": "b\n"})
	g(w, "checkout", "-q", "-b", "p")
	p1 := commit("parent: line 1", "1700000100 +0100", map[string]string{"a.txt": lines(map[int]string{1: "P1"})})
	g(w, "checkout", "-q", "-b", "c")
	who = "Child Author"
	c1 := commit("child: c.txt\n\nWith a body.  \n\n", "1700000200 +0200", map[string]string{"c.txt": "c\n"})
	commit("an empty marker, on purpose", "1700000250 +0100", nil)
	c2 := commit("child: line 9", "1700000300 +0100", map[string]string{"a.txt": lines(map[int]string{1: "P1", 9: "C9"})})
	who = ""
	g(w, "checkout", "-q", "-b", "g")
	g1 := commit("grandchild: d.txt", "1700000400 +0100", map[string]string{"d.txt": "d\n"})
	g(w, "checkout", "-q", "-b", "x", p1)
	x1 := commit("conflicting child: line 3", "1700000500 +0100", map[string]string{"a.txt": lines(map[int]string{1: "P1", 3: "X3"})})
	g(w, "checkout", "-q", "-b", "y", p1)
	y1 := commit("moved child", "1700000600 +0100", map[string]string{"y.txt": "1\n"})
	y2 := commit("pushed after GitHub listed it", "1700000700 +0100", map[string]string{"y.txt": "2\n"})
	g(w, "checkout", "-q", "-b", "la", p1)
	la := commit("left alone", "1700000650 +0100", map[string]string{"la.txt": "1\n"})
	g(w, "checkout", "-q", "main")
	m1 := commit("main moves", "1700000800 +0100", map[string]string{"b.txt": "b2\n"})
	g(w, "checkout", "-q", "-b", "cc", p1)
	commit("criss-cross child", "1700000820 +0100", map[string]string{"cc.txt": "1\n"})
	g(w, "merge", "-q", "--no-edit", m1)
	cc := g(w, "rev-parse", "HEAD")
	g(w, "checkout", "-q", "p")
	g(w, "merge", "-q", "--no-edit", "main")
	pm := g(w, "rev-parse", "HEAD")
	g(w, "checkout", "-q", "-b", "z", p1)
	commit("child with a merge of its own", "1700000850 +0100", map[string]string{"z.txt": "z\n"})
	g(w, "merge", "-q", "--no-commit", pm)
	z := commit("merge the parent, and a change only this merge holds", "1700000860 +0100", map[string]string{"e.txt": "e\n"})
	g(w, "checkout", "-q", "p")
	u := commit("parent: line 3", "1700000900 +0100", map[string]string{"a.txt": lines(map[int]string{1: "P1", 3: "P3"})})
	n := g(w, "commit-tree", u+"^{tree}", "-p", m1, "-m", "parent (#10)") // GitHub's squash merge
	g(w, "push", "-q", "origin", n+":refs/heads/main", "p", "c", "g", "x", "y", "z", "la", "cc")
	_ = m0

	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\nclone_cmd: git clone -q "+root+"/{repo}.git {dir}\n")
	write(t, filepath.Join(root, "agents", "tri.md"), "---\nenabled: true\nrepos: o/r\n---\ntri\n")
	gh := filepath.Join(root, "gh")
	fakeGh(t, `exec python3 - "$@" <<'PY'
import json, os, sys
d = "`+gh+`"
a = sys.argv[1:]
open(d + "/log", "a").write(" ".join(a) + "\n")
if a[:2] == ["pr", "view"]: print(open(f"{d}/pr-{a[2]}.json").read())
elif a[:2] == ["pr", "list"]:
    k = "head" if "--head" in a else "base"
    p = os.path.join(d, f"{k}-{a[a.index('--' + k) + 1]}.json")
    print(open(p).read() if os.path.exists(p) else "[]")
PY`)
	os.MkdirAll(gh, 0o755)
	set := func(name, v string) { write(t, filepath.Join(gh, name), v) }
	set("pr-10.json", `{"number":10,"state":"OPEN","isDraft":false,"baseRefName":"main","headRefName":"p","headRefOid":"`+u+`","labels":[],"body":"","mergeCommit":{"oid":"`+n+`"}}`)
	set("base-p.json", `[{"number":11,"headRefName":"c","headRefOid":"`+c2+`"},{"number":13,"headRefName":"x","headRefOid":"`+x1+`"},{"number":15,"headRefName":"y","headRefOid":"`+y1+`"},{"number":16,"headRefName":"z","headRefOid":"`+z+`"},`+
		`{"number":17,"headRefName":"la","headRefOid":"`+la+`","labels":[{"name":"rota:leave-alone"}]},{"number":18,"headRefName":"fk","headRefOid":"`+la+`","isCrossRepository":true},`+
		`{"number":19,"headRefName":"cc","headRefOid":"`+cc+`"}]`)
	set("base-c.json", `[{"number":12,"headRefName":"g","headRefOid":"`+g1+`"}]`)

	team := open(t, root)
	msg, err := team.Merge(context.Background(), "o/r#10", u[:7])
	if err != nil || !strings.Contains(msg, "#11, #13, #15, #16, #17, #18, #19, stacked on it, now point at main") || !strings.Contains(msg, "rebased, so each lists only its own commits: #11, #12") {
		t.Fatalf("merged, and the stack rebased: %q %v", msg, err)
	}
	if !strings.Contains(msg, "not rebased, so its base still has to be merged into each: #13 (it conflicts with its new base in a.txt); #15 (") {
		t.Fatalf("the conflicting one and the moved one are left: %q", msg)
	}

	c := g(remote, "rev-parse", "c")
	if got := g(remote, "log", "--format=%s|%an %ae %ad|%cn", "--date=raw", "--reverse", n+".."+c); got != "child: c.txt|Child Author child@x 1700000200 +0200|Dev\n"+
		"an empty marker, on purpose|Child Author child@x 1700000250 +0100|Dev\nchild: line 9|Child Author child@x 1700000300 +0100|Dev" {
		t.Fatalf("the child holds only its own commits, the empty one included, each with its author and date:\n%s", got)
	}
	message := func(c string) string {
		out, err := exec.Command("git", "-C", remote, "cat-file", "commit", c).Output()
		if err != nil {
			t.Fatal(err)
		}
		_, m, _ := strings.Cut(string(out), "\n\n")
		return m
	}
	if got, want := message(c+"~2"), message(c1); got != want || !strings.Contains(got, "body.  \n\n") {
		t.Fatalf("a message is not kept byte for byte: %q, want %q", got, want)
	}
	if got := g(remote, "show", c+":a.txt"); got != strings.TrimSpace(lines(map[int]string{1: "P1", 3: "P3", 9: "C9"})) {
		t.Fatalf("the child's change is made on the merge: %q", got)
	}
	if got := g(remote, "show", c+":b.txt") + g(remote, "show", c+":c.txt"); got != "b2c" {
		t.Fatalf("with main's change under it: %q", got)
	}
	if g(remote, "rev-parse", "g~1") != c || g(remote, "show", "g:d.txt") != "d" || g(remote, "rev-list", "--count", c+"..g") != "1" {
		t.Fatal("the grandchild is made again on the child's new head, with only its own commit")
	}
	if !strings.Contains(msg, "#16 (its commits made again differ from merging its base into it") {
		t.Fatalf("one whose own merge holds a change a rebase would drop is left: %q", msg)
	}
	for _, want := range []string{"#18 (its branch is in a fork)", "#19 (it branched from what it was stacked on at more than one point)", "not rebased, as it carries rota:leave-alone: #17"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("%s: %q", want, msg)
		}
	}
	if g(remote, "rev-parse", "x") != x1 || g(remote, "rev-parse", "y") != y2 || g(remote, "rev-parse", "z") != z || g(remote, "rev-parse", "la") != la || g(remote, "rev-parse", "cc") != cc {
		t.Fatal("a pull request that conflicts, whose branch moved since GitHub listed it, or that a rebase would change, was changed")
	}
	if _, err := os.Stat(filepath.Join(root, reposDir, "o", "r", ".git")); err != nil {
		t.Fatalf("rota's own clone is not in the team folder: %v", err)
	}
	_ = c1
}

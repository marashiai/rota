package core

// Behavior tests: they drive the core against a fake claude and check what a user would see:
// files, processes, the status.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"rota/gloss/review"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeClaude prints a result like the real CLI after running body (a shell snippet), and
// answers "auth status" as a logged-in account.
func fakeClaude(t *testing.T, dir, body string) string {
	return fakeClaudeAs(t, dir, body, "someone@example.com")
}

func fakeClaudeAs(t *testing.T, dir, body, email string) string {
	t.Helper()
	p := filepath.Join(dir, "claude")
	script := "#!/bin/bash\n" +
		"if [ \"$1\" = auth ]; then printf '{\"loggedIn\":true,\"email\":\"" + email + "\"}'; exit 0; fi\n" +
		"head -n1 >/dev/null\n" + body + "\n" +
		`printf '{"type":"result","subtype":"success","is_error":false,"result":"ran in %s","num_turns":3,"session_id":"s1","usage":{"input_tokens":1,"output_tokens":2,"cache_read_input_tokens":3,"cache_creation_input_tokens":4}}' "$PWD"` + "\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// fakeGh puts a gh first on PATH that runs script: shifts find gh there, as on a machine.
func fakeGh(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte("#!/bin/bash\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}

func open(t *testing.T, root string) *Team {
	t.Helper()
	team, err := Open(root, "rota.conf")
	if err != nil {
		t.Fatal(err)
	}
	return team
}

func at(s string) time.Time {
	if s == "" {
		return time.Now()
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// runShift is rota run [agent] at now ("" is now).
func runShift(t *testing.T, root, name, now string, force bool) error {
	t.Helper()
	team, err := Open(root, "rota.conf")
	if err != nil {
		return err
	}
	return team.RunShift(context.Background(), name, at(now), force, false)
}

func status(t *testing.T, root, now string) Status {
	t.Helper()
	return open(t, root).Status(at(now))
}

func agentState(st Status, name string) AgentStatus {
	for _, a := range st.Agents {
		if a.Name == name {
			return a
		}
	}
	return AgentStatus{}
}

func runs(t *testing.T, root string) []Run {
	t.Helper()
	r, err := loadRuns(open(t, root).Config)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func count(rs []Run, agent string) (n int) {
	for _, r := range rs {
		if r.Agent == agent {
			n++
		}
	}
	return n
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func readAll(glob string) string {
	files, _ := filepath.Glob(glob)
	var b strings.Builder
	for _, f := range files {
		data, _ := os.ReadFile(f)
		b.Write(data)
	}
	return b.String()
}

func alive(pidFile string) bool {
	b, err := os.ReadFile(pidFile)
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return err == nil && pid > 0 && syscall.Kill(pid, 0) == nil
}

func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	f()
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	io.Copy(&buf, r)
	return buf.String()
}

// inboxText is an agent's notes as it reads them.
func inboxText(team *Team, agent string) string { return team.NotesText(team.Notes(agent)) }

// putNotes fills an agent's inbox: each note a "from: text" or "owner/name#n from: text".
func putNotes(t *testing.T, root, agent string, notes ...string) {
	t.Helper()
	c := open(t, root).Config
	for _, n := range notes {
		about, rest, _ := strings.Cut(n, " ")
		if !strings.Contains(about, "#") {
			about, rest = "", n
		}
		from, text, _ := strings.Cut(rest, ": ")
		if err := c.addNote(agent, about, from, text, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
}

// A note about a pull request or issue is kept under its heading in the inbox, general notes
// stay on top, an agent may leave itself one, and a shift removes only the notes it read.
func TestNotesAboutAPullRequestAreKeptUnderIt(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\n")
	write(t, filepath.Join(root, "agents", "dev.md"), "---\nenabled: true\nuntil_done: true\nrepos: acme/shop-backend, acme/shop-creator\n---\ndev\n")
	write(t, filepath.Join(root, "agents", "tri.md"), "---\nenabled: true\nrepos: acme/shop-backend\n---\ntri\n")
	team := open(t, root)
	d := time.Now().Format("2006-01-02")
	team.LeaveNote("dev", "", "human", "a general one")
	lost := team.routeNotes("tri", "## Notes to teammates\n@dev backend#72: not ready: no review\n- @dev shop-creator#551: check it\n@dev nothing#5: lost\n@dev: plain note\n@all backend#9: everyone\n", time.Now())
	if len(lost) != 1 || !strings.Contains(lost[0], "nothing#5") {
		t.Fatalf("a note about an unknown repository is not delivered, and said so: %q", lost)
	}
	want := "- " + d + " from the human: a general one\n- " + d + " from tri: plain note\n\n" +
		"## acme/shop-backend#72\n- " + d + " from tri: not ready: no review\n\n" +
		"## acme/shop-creator#551\n- " + d + " from tri: check it\n\n" +
		"## acme/shop-backend#9\n- " + d + " from tri: everyone\n"
	if got := inboxText(team, "dev"); got != want {
		t.Fatalf("notes are filed under their pull request, general ones on top:\n%s\nwant:\n%s", got, want)
	}
	if inboxText(team, "tri") != "" || !team.Asked()["dev"] {
		t.Fatal("@all does not reach the sender, and a teammate's note wakes the until-done agent")
	}
	team.answered("dev")
	team.routeNotes("dev", "@dev creator#551: measure the latency\n@dev: talking to myself\n", time.Now())
	read := inboxText(team, "dev")
	if !strings.Contains(read, "from tri: check it\n- "+d+" from dev: measure the latency\n\n## acme/shop-backend#9") || strings.Contains(read, "talking to myself") || team.Asked()["dev"] {
		t.Fatalf("an agent leaves itself notes about a pull request only, and is not woken by them:\n%s", read)
	}
	briefed := team.Notes("dev")
	team.routeNotes("tri", "@dev backend#72: not ready: no review", time.Now()) // the same words again, while dev's shift runs
	team.dropNotes("dev", ids(briefed))
	if got := inboxText(team, "dev"); got != "## acme/shop-backend#72\n- "+d+" from tri: not ready: no review\n" {
		t.Fatalf("a shift removes the notes it read, by id, and their empty headings:\n%s", got)
	}
	if _, ok := resolveRepo([]string{"a/backend", "b/backend"}, "backend"); ok {
		t.Fatal("a name two repositories share names neither")
	}
	for short, want := range map[string]string{"puzzle": "o/puzzle", "pf": "o/puzzle-pf", "o/puzzle-shop": "o/puzzle-shop", "backend": "", "web": ""} {
		if got, ok := resolveRepo([]string{"o/puzzle", "o/puzzle-pf", "o/puzzle-shop", "o/a-backend", "o/b-backend"}, short); got != want || ok != (want != "") {
			t.Fatalf("%q names %q, want %q", short, got, want)
		}
	}
}

// rota merge merges a pull request only while it does not carry the hold_label, once what it
// depends on is merged or closed and does not carry that label either, and it is not stacked on
// another; before it merges, it points those stacked on it at its base.
func TestMergeWaitsForWhatAPullRequestDependsOn(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\nlabels: rota:needs-rollout F9D0C4 Waits for a step by the human\nhold_label: rota:needs-rollout\n")
	write(t, filepath.Join(root, "agents", "tri.md"), "---\nenabled: true\nrepos: acme/shop-backend, acme/shop-creator\n---\ntri\n")
	gh := filepath.Join(root, "gh")
	fakeGh(t, `exec python3 - "$@" <<'PY'
import json, os, sys
d = "`+gh+`"
a = sys.argv[1:]
open(d + "/log", "a").write(" ".join(a) + "\n")
repo = a[a.index("-R") + 1].split("/")[1] if "-R" in a else ""
def show(name):
    p = os.path.join(d, name)
    if not os.path.exists(p):
        sys.stderr.write("no pull requests found\n"); sys.exit(1)
    print(open(p).read())
if a[:2] in (["pr", "view"], ["issue", "view"]): show(f"{a[0]}-{repo}-{a[2]}.json")
elif a[0] == "api":
    _, owner, name, _, spec = a[1].split("/", 4)
    p = os.path.join(d, "compare-" + name + "-" + spec.split("...")[1] + ".json")
    print(open(p).read() if os.path.exists(p) else '{"behind_by":0}')
elif a[:2] == ["pr", "list"]:
    k = "head" if "--head" in a else "base"
    v = a[a.index("--" + k) + 1].replace("/", "_")
    p = os.path.join(d, f"{k}-{v}.json")
    print(open(p).read() if os.path.exists(p) else "[]")
PY`)
	os.MkdirAll(gh, 0o755)
	set := func(name, v string) { write(t, filepath.Join(gh, name), v) }
	set("pr-shop-backend-74.json", `{"number":74,"state":"OPEN","isDraft":false,"baseRefName":"acc","headRefName":"developer/55-narrow","headRefOid":"abc1234def","labels":[],
		"body":"Fixes #55\n\nDepends on #69\nDepends on: creator#551, backend#70"}`)
	set("pr-shop-backend-69.json", `{"state":"OPEN","labels":[]}`)
	set("pr-shop-creator-551.json", `{"state":"MERGED","labels":[]}`)
	set("issue-shop-backend-70.json", `{"state":"CLOSED","labels":[]}`)
	set("base-developer_55-narrow.json", `[{"number":75}]`)
	team := open(t, root)
	merge := func(head string) (string, error) { return team.Merge(context.Background(), "backend#74", head) }
	if _, err := merge("abc1234"); err == nil || !strings.Contains(err.Error(), "waits for: acme/shop-backend#69 is not merged") {
		t.Fatalf("a pull request waits for the one it depends on: %v", err)
	}
	set("pr-shop-backend-69.json", `{"state":"MERGED","labels":[{"name":"RoTa:Needs-Rollout"}]}`)
	if _, err := merge("abc1234"); err == nil || !strings.Contains(err.Error(), "#69 carries rota:needs-rollout") {
		t.Fatalf("and while it carries the hold_label, in any case: %v", err)
	}
	if team.HoldLabel = ""; true {
		if _, err := merge("fff9999"); err == nil || !strings.Contains(err.Error(), "not the commit you checked") {
			t.Fatalf("without a hold_label no label holds it back: %v", err)
		}
		team.HoldLabel = "rota:needs-rollout"
	}
	set("pr-shop-backend-69.json", `{"state":"MERGED","labels":[]}`)
	if _, err := merge("fff9999"); err == nil || !strings.Contains(err.Error(), "not the commit you checked") {
		t.Fatalf("a commit pushed since it was checked is not merged: %v", err)
	}
	set("compare-shop-backend-abc1234def.json", `{"behind_by":3,"ahead_by":2}`)
	if _, err := merge("abc1234"); err == nil || !strings.Contains(err.Error(), "#74 is 3 commits behind acc, so it was not checked with what acc has now: merge acc into it") {
		t.Fatalf("one behind its base waits until the base is merged in: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(gh, "log")); !strings.Contains(string(b), "api repos/acme/shop-backend/compare/acc...abc1234def") {
		t.Fatalf("its base was not compared with the head it merges:\n%s", b)
	}
	set("compare-shop-backend-abc1234def.json", `{"behind_by":0,"ahead_by":2}`)
	if b, _ := os.ReadFile(filepath.Join(gh, "log")); strings.Contains(string(b), "pr merge") || strings.Contains(string(b), "pr edit") {
		t.Fatalf("nothing was changed while it waited:\n%s", b)
	}
	msg, err := merge("abc1234")
	if err != nil || !strings.Contains(msg, "merged acme/shop-backend#74 into acc") || !strings.Contains(msg, "#75, stacked on it, now point at acc") {
		t.Fatalf("with all in place it merges: %q %v", msg, err)
	}
	log, _ := os.ReadFile(filepath.Join(gh, "log"))
	edit := strings.Index(string(log), "pr edit 75 -R acme/shop-backend --base acc")
	mrg := strings.Index(string(log), "pr merge 74 -R acme/shop-backend --squash --match-head-commit abc1234def\n")
	if edit < 0 || mrg < edit {
		t.Fatalf("the stacked pull request is moved first, then it is merged at the checked commit, its branch left to the repository's setting:\n%s", log)
	}
	set("pr-shop-backend-77.json", `{"number":77,"state":"OPEN","baseRefName":"acc","headRefName":"developer/55-narrow","headRefOid":"7777777aa","isCrossRepository":true,"labels":[],"body":""}`)
	if msg, err := team.Merge(context.Background(), "backend#77", "7777777"); err != nil || strings.Contains(msg, "stacked") {
		t.Fatalf("a pull request from a fork has nothing stacked on it, whatever its branch is called: %q %v", msg, err)
	}
	if b, _ := os.ReadFile(filepath.Join(gh, "log")); strings.Count(string(b), "pr edit 75") != 1 {
		t.Fatalf("a pull request on the same-named branch here was moved for a fork's merge:\n%s", b)
	}
	set("pr-shop-backend-76.json", `{"number":76,"state":"OPEN","baseRefName":"developer/55-narrow","headRefName":"developer/57-x","headRefOid":"1234567aa","labels":[],"body":""}`)
	set("head-developer_55-narrow.json", `[{"number":74}]`)
	if _, err := team.Merge(context.Background(), "shop-backend#76", "1234567"); err == nil || !strings.Contains(err.Error(), "stacked on #74") {
		t.Fatalf("a stacked pull request waits for its base: %v", err)
	}
	set("head-developer_55-narrow.json", `[]`)
	set("pr-shop-backend-76.json", `{"number":76,"state":"OPEN","baseRefName":"developer/55-narrow","headRefName":"developer/57-x","headRefOid":"1234567aa","labels":[{"name":"Rota:Needs-Rollout"}],"body":""}`)
	if _, err := team.Merge(context.Background(), "acme/shop-backend#76", "1234567"); err == nil || !strings.Contains(err.Error(), "#76 carries rota:needs-rollout: it is not merged until that label is removed") {
		t.Fatalf("a pull request that carries the hold_label itself is not merged: %v", err)
	}
	set("pr-shop-backend-76.json", `{"number":76,"state":"OPEN","baseRefName":"acc","headRefName":"developer/57-x","headRefOid":"1234567aa","labels":[{"name":"rota:leave-alone"}],"body":""}`)
	if _, err := team.Merge(context.Background(), "acme/shop-backend#76", "1234567"); err == nil || !strings.Contains(err.Error(), "#76 carries rota:leave-alone: the human asked the team to leave it alone") {
		t.Fatalf("a pull request the human said to leave alone is not merged: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(gh, "log")); strings.Contains(string(b), "pr merge 76") {
		t.Fatalf("nothing was merged:\n%s", b)
	}
	if _, err := team.Merge(context.Background(), "web#1", "1234567"); err == nil || !strings.Contains(err.Error(), `no repository of the team is "web"`) {
		t.Fatalf("an unknown repository is refused: %v", err)
	}
}

// The team's labels have one name, color and description in every repository: missing ones are
// created, wrong ones corrected, right ones left alone.
func TestTheTeamsLabelsAreTheSameInEveryRepository(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\ndefault_repos: o/one, o/two\nlabels: priority:high B60205 Fix first; rota:qa 000000 not rota's own\n")
	write(t, filepath.Join(root, "agents", "qa.md"), "---\nenabled: true\n---\nqa\n")
	dir := t.TempDir()
	fakeGh(t, `echo "$*" >> `+dir+`/log
case "$1 $2" in "label list") r=${4#o/}; cat `+dir+`/$r.json 2>/dev/null || echo '[]';; esac`)
	team := open(t, root)
	want := team.Labels()
	right, _ := json.Marshal(want)
	write(t, filepath.Join(dir, "one.json"), string(right))
	write(t, filepath.Join(dir, "two.json"), `[{"name":"RoTa","color":"c2e0c6","description":""},{"name":"priority:high","color":"B60205","description":"Fix first"}]`)
	changed, err := team.SyncLabels(context.Background())
	if err != nil || len(changed) != len(want)-1 {
		t.Fatalf("o/two gets every label but one right one: %v %q", err, changed)
	}
	log, _ := os.ReadFile(filepath.Join(dir, "log"))
	if strings.Contains(string(log), "-R o/one --color") || strings.Contains(string(log), "-R o/one --name") || !strings.Contains(string(log), "label edit RoTa -R o/two --name rota --color 5319E7 --description Work for the AI team") ||
		!strings.Contains(string(log), "label create rota:qa -R o/two --color D4C5F9 --description Filed by the qa agent") || strings.Contains(string(log), "priority:high -R o/two") {
		t.Fatalf("a right repository is left alone; a wrong label is corrected, a missing one created:\n%s", log)
	}
}

// The team declares its own labels in the labels setting: name, color and description, separated
// by ";". rota adds only rota and rota:<agent>, and the brief lists the team's for the agents.
func TestTheTeamDeclaresItsOwnLabels(t *testing.T) {
	var l Labels
	if err := l.UnmarshalText([]byte(" priority:high B60205 Fix first ;; rota:not-ready d93f0b 🔙 Sent back: the comment says why; bare 0E8A16; ok 0E8A16 👍🏽; star 0E8A16 ⭐️ 5 stars")); err != nil {
		t.Fatal(err)
	}
	if want := (Labels{{"priority:high", "B60205", "Fix first", ""}, {"rota:not-ready", "d93f0b", "Sent back: the comment says why", "🔙"}, {"bare", "0E8A16", "", ""}, {"ok", "0E8A16", "", "👍🏽"}, {"star", "0E8A16", "5 stars", "⭐️"}}); !reflect.DeepEqual(l, want) {
		t.Fatalf("got %q", l)
	}
	if b, _ := l.MarshalText(); string(b) != "priority:high B60205 Fix first; rota:not-ready d93f0b 🔙 Sent back: the comment says why; bare 0E8A16; ok 0E8A16 👍🏽; star 0E8A16 ⭐️ 5 stars" {
		t.Fatalf("written back as read: %s", b)
	}
	for in, want := range map[string]string{
		"priority:high Fix first":              "six hex digits",
		"onlyname":                             "six hex digits",
		"a B60205 x; A 0E8A16 y":               "A is there twice",
		"a B60205 " + strings.Repeat("x", 101): "at most 100 characters",
	} {
		if err := l.UnmarshalText([]byte(in)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q is refused with %q: %v", in, want, err)
		}
	}
	if err := checkSetting("hold_label", "needs rollout"); err == nil {
		t.Error("a hold_label is one label name")
	}

	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\nlabels: priority:high B60205 Fix first; rota:qa 000000 clashes; bare 0E8A16\nhold_label: rota:needs-rollout\nreview_label: rota:needs-review\n")
	write(t, filepath.Join(root, "agents", "qa.md"), "---\nenabled: true\n---\nqa\n")
	team := open(t, root)
	if !slices.ContainsFunc(team.Warnings, func(w string) bool {
		return strings.Contains(w, "hold_label rota:needs-rollout is not one of the team's labels")
	}) || !slices.ContainsFunc(team.Warnings, func(w string) bool {
		return strings.Contains(w, "review_label rota:needs-review is not one of the team's labels")
	}) {
		t.Fatalf("a hold_label or review_label the team does not have is a warning: %q", team.Warnings)
	}
	var names []string
	for _, x := range team.Labels() {
		names = append(names, x.Name)
	}
	if strings.Join(names, " ") != "rota rota:leave-alone rota:approved rota:qa priority:high bare" {
		t.Fatalf("rota's own labels, then the team's: %q", names)
	}
	if got := team.labelsText(); got != "  - priority:high: Fix first\n  - bare" {
		t.Fatalf("the brief lists the team's labels: %q", got)
	}
	if !strings.Contains(DefaultBrief, "{labels}") || strings.Contains(DefaultBrief, "priority:") || strings.Contains(DefaultBrief, "developer") {
		t.Fatal("the built-in brief lists the team's labels and names no agent or label of a team")
	}
	if !strings.Contains(DefaultBrief, `Leave alone every issue and pull request that carries "`+LeaveLabel+`"`) || !strings.Contains(DefaultBrief, "put that label on it yourself") {
		t.Fatal("the built-in brief tells every agent to leave alone what carries the leave label, and to put it on what the human tells it to leave alone")
	}
	if !strings.Contains(DefaultBrief, `"`+ApprovedLabel+`" on a pull request is the human's approval of it: only the human puts it there. Never add or remove it.`) {
		t.Fatal("the built-in brief tells every agent that only the human approves")
	}
	write(t, filepath.Join(root, "agents", "Leave-Alone.md"), "---\nenabled: true\n---\nx\n")
	if _, err := Open(root, "rota.conf"); err == nil || !strings.Contains(err.Error(), "may not be named Leave-Alone") {
		t.Fatalf("no agent's label may be the leave label: %v", err)
	}
	os.Remove(filepath.Join(root, "agents", "Leave-Alone.md"))
	write(t, filepath.Join(root, "agents", "approved.md"), "---\nenabled: true\n---\nx\n")
	if _, err := Open(root, "rota.conf"); err == nil || !strings.Contains(err.Error(), "may not be named approved") {
		t.Fatalf("nor the approved label: %v", err)
	}
}

// An answer to a question about a pull request goes with that pull request's notes.
func TestAnAnswerAboutAPullRequestIsFiledUnderIt(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\n")
	write(t, filepath.Join(root, "agents", "tri.md"), "---\nenabled: true\nrepos: acme/shop-backend\n---\ntri\n")
	team := open(t, root)
	team.routeNotes("tri", "@human backend#69: re-run the bootstrap to step 5. Done?\n", time.Now())
	qs := team.Questions()
	if len(qs) != 1 || qs[0].Text != "acme/shop-backend#69: re-run the bootstrap to step 5. Done?" {
		t.Fatalf("the question names the pull request in full: %+v", qs)
	}
	team.Answer(qs[0].ID, "done")
	if inbox := inboxText(team, "tri"); !strings.HasPrefix(inbox, "## acme/shop-backend#69\n- ") || !strings.Contains(inbox, ": done\n") {
		t.Fatalf("the answer is filed under the pull request:\n%s", inbox)
	}
}

// A question asked pushes to every registered device: a push without content, signed with
// the team's VAPID key for the push service's origin; a device the service no longer knows
// is forgotten.
func TestAQuestionIsPushedToTheHumansDevices(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\n")
	var mu sync.Mutex
	var got []*http.Request
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r)
		mu.Unlock()
		if r.URL.Path == "/gone" {
			w.WriteHeader(http.StatusGone)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(srv.Close)
	defer func(c *http.Client, s []string) { pushClient, pushServices = c, s }(pushClient, pushServices)
	pushClient, pushServices = srv.Client(), append(pushServices, "127.0.0.1") // the test server plays a push service
	c := open(t, root).Config
	if err := c.Subscribe("http://fcm.googleapis.com/x", "phone"); err == nil {
		t.Fatal("only an https push endpoint is taken")
	}
	if err := c.Subscribe("https://intranet.example/hook", "phone"); err == nil {
		t.Fatal("only a browser's push service is posted to, never any address a page names")
	}
	c.Subscribe(srv.URL+"/phone", "iPhone")
	c.Subscribe(srv.URL+"/phone", "iPhone")
	c.Subscribe(srv.URL+"/gone", "old laptop")
	if len(c.PushSubscriptions()) != 2 {
		t.Fatalf("each device once: %+v", c.PushSubscriptions())
	}
	if _, err := c.Ask("dev", "May I rename the app?"); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("both devices were pushed: %d", len(got))
	}
	r := got[0]
	key, _ := c.vapidKey()
	auth := r.Header.Get("Authorization")
	jwt := strings.TrimSuffix(strings.TrimPrefix(strings.Split(auth, ", k=")[0], "vapid t="), ",")
	if !strings.HasPrefix(auth, "vapid t=") || !verifyJWT(jwt, &key.PublicKey) || r.ContentLength > 0 || r.Header.Get("TTL") == "" {
		t.Fatalf("a signed push without content: %q %d", auth, r.ContentLength)
	}
	claims, _ := base64.RawURLEncoding.DecodeString(strings.Split(jwt, ".")[1])
	if !strings.Contains(string(claims), `"aud":"`+srv.URL+`"`) {
		t.Fatalf("the token is for the push service's origin: %s", claims)
	}
	if subs := c.PushSubscriptions(); len(subs) != 1 || !strings.HasSuffix(subs[0].Endpoint, "/phone") {
		t.Fatalf("the device the push service no longer knows is forgotten: %+v", subs)
	}
	pub, _ := c.VAPIDPublicKey()
	if raw, err := base64.RawURLEncoding.DecodeString(pub); err != nil || len(raw) != 65 || raw[0] != 4 {
		t.Fatalf("the public key is the uncompressed point browsers take: %v %d", err, len(raw))
	}
	if fi, _ := os.Stat(c.Path(vapidFile)); fi.Mode().Perm() != 0o600 {
		t.Fatalf("the key is only the user's: %v", fi.Mode())
	}
	pushed := func(q Question) int { // what one question pushes, as the human chose
		t.Helper()
		mu.Lock()
		before := len(got)
		mu.Unlock()
		if _, err := c.askQuestion(q); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		defer mu.Unlock()
		return len(got) - before
	}
	c.SetMailPrefs(MailPrefs{Split: true, Focus: 5, After: "next", Notify: "focused"})
	if n := pushed(Question{Agent: "dev", Text: "Unimportant?", Importance: "low"}); n != 0 {
		t.Fatalf("an unimportant question is not pushed while the human hears of Focused only: %d", n)
	}
	if n := pushed(Question{Agent: "dev", Text: "Important?"}); n != 1 {
		t.Fatalf("a question pushed: %d", n)
	}
	c.SetMailPrefs(MailPrefs{Split: true, Focus: 5, After: "next", Notify: "off"})
	if n := pushed(Question{Agent: "dev", Text: "Anything?", Importance: "high"}); n != 0 {
		t.Fatalf("nothing pushed while notifications are off: %d", n)
	}
}

// The tests' organizers hold their own lock, not the machine's: an organizer may run here.
func init() {
	organizerLock = filepath.Join(os.TempDir(), fmt.Sprintf("rota-test-organizer-%d.lock", os.Getpid()))
	labelSync = false // the tests' organizers leave GitHub alone
}

// A machine has one organizer: a second one, for any team folder, says which runs.
func TestAMachineHasOneOrganizer(t *testing.T) {
	release, err := claimOrganizer("/teams/one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := claimOrganizer("/teams/two"); err == nil || !strings.Contains(err.Error(), "team folder /teams/one") || !strings.Contains(err.Error(), fmt.Sprintf("pid %d", os.Getpid())) {
		t.Fatalf("a second organizer is refused and told which runs: %v", err)
	}
	release()
	again, err := claimOrganizer("/teams/two")
	if err != nil {
		t.Fatalf("once the first is gone, another may organize: %v", err)
	}
	again()
}

func organize(t *testing.T, root string) (cancel func()) {
	t.Helper()
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Organize(ctx, root, "rota.conf") }()
	return func() {
		stop()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Fatal("the organizer did not exit")
		}
	}
}

// ---- hosts ----

func online(h *hub, name string) bool {
	for _, hs := range h.hosts() {
		if hs.Name == name && hs.Online {
			return true
		}
	}
	return false
}

// organizerAndHost sets up an organizer listening on loopback with one remote agent, and a
// host whose fake claude runs body; it returns once the host has joined.
func organizerAndHost(t *testing.T, ctx context.Context, body string) (org string, h *hub, wc *Config) {
	t.Helper()
	org, wk := t.TempDir(), t.TempDir()
	write(t, filepath.Join(org, "rota.conf"), "listen: 127.0.0.1\nlisten_port: 0\nusage: off\ncommit: false\nsync_repos: false\nclaude_bin: "+fakeClaude(t, org, "")+"\n")
	write(t, filepath.Join(org, "agents", "remote.md"), "---\nenabled: true\nhost: w1\nrun_minutes: 5\ngh_user: someone\n---\nremote agent\n")
	oc := open(t, org).Config
	h = newHub(oc)
	ln, err := h.listen()
	if err != nil {
		t.Fatal(err)
	}
	go h.serve(ctx, ln)
	fakeGh(t, `[ "$1 $2 $3 $4" = "auth token --user someone" ] && { echo tok; exit 0; }; exit 1`)
	write(t, filepath.Join(wk, "rota.conf"), "claude_bin: "+fakeClaude(t, wk, body)+"\nworker_name: w1\nusage: off\nsync_repos: false\n")
	wc = open(t, wk).Config
	code, err := oc.Pair()
	if err != nil {
		t.Fatal(err)
	}
	go wc.Join(ctx, h.addr, code)
	waitFor(t, "the host to join", func() bool { return online(h, "w1") })
	return org, h, wc
}

// runVia runs the agent's shift through the organizer's hub, as the organizer does.
func runVia(ctx context.Context, t *testing.T, root, name string, h *hub) error {
	team := open(t, root)
	return team.shift(ctx, team.schedule(time.Now(), nil, h.hosts()), team.Agent(name), nil, h, nil, true)
}

func TestWrongPairingCodeIsRejected(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	org, wk := t.TempDir(), t.TempDir()
	write(t, filepath.Join(org, "rota.conf"), "listen: 127.0.0.1\nlisten_port: 0\nusage: off\n")
	oc := open(t, org).Config
	h := newHub(oc)
	ln, _ := h.listen()
	go h.serve(ctx, ln)
	oc.Pair()
	write(t, filepath.Join(wk, "rota.conf"), "worker_name: w1\nusage: off\n")
	wc := open(t, wk).Config
	if err := wc.Join(ctx, h.addr, "WRONG"); err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("expected rejection, got %v", err)
	}
	if _, err := os.Stat(wc.Path(workerFile)); err == nil {
		t.Fatal("a token was saved although pairing was rejected")
	}
	if err := wc.Join(ctx, h.addr, ""); err == nil || !strings.Contains(err.Error(), "not paired") {
		t.Fatalf("joining without a token should fail clearly, got %v", err)
	}
}

func TestHostPairsRunsShiftAndReconnectsWithToken(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	org, h, wc := organizerAndHost(t, ctx, "head -c 2000000 /dev/zero > build.bin")
	st, err := os.Stat(wc.Path(workerFile))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("host token file: err=%v", err)
	}
	token, _ := os.ReadFile(wc.Path(workerFile))
	if hostsJSON, _ := os.ReadFile(filepath.Join(org, "hosts.json")); strings.Contains(string(hostsJSON), strings.TrimSpace(string(token))) {
		t.Fatal("the organizer stored the raw token instead of its hash")
	}
	if err := runVia(ctx, t, org, "remote", h); err != nil {
		t.Fatal(err)
	}
	wantDir, _ := filepath.EvalSymlinks(wc.Path(filepath.Join(workDir, "remote"))) // macOS: /var is /private/var
	if journal := readAll(filepath.Join(org, "journal", "*.md")); !strings.Contains(journal, "ran in "+wantDir) {
		t.Fatalf("the shift did not run in the host's own work dir; journal:\n%s", journal)
	}
	if rs := runs(t, org); len(rs) != 1 || rs[0].Agent != "remote" || rs[0].Turns != 3 || rs[0].Status != "ok" {
		t.Fatalf("the host's result should be recorded: %+v", rs)
	}
	if d, ok := open(t, org).DiskOf("remote"); !ok || d.Bytes < 2000000 || d.Bytes > 3000000 {
		t.Fatalf("the host measured its own work folder and the organizer kept it: %+v %v", d, ok)
	}
	// the host reconnects with its token alone, no code
	cancel()
	waitFor(t, "the host to leave", func() bool { return !online(h, "w1") })
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	ln, _ := h.listen()
	go h.serve(ctx2, ln)
	go wc.Join(ctx2, h.addr, "")
	waitFor(t, "the host to rejoin with its token", func() bool { return online(h, "w1") })
}

func TestClosingTheOrganizerKillsTheRemoteShift(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pidFile := filepath.Join(t.TempDir(), "pid")
	org, h, _ := organizerAndHost(t, ctx, "sleep 300 & echo $! > "+pidFile+"; wait")
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- runVia(runCtx, t, org, "remote", h) }()
	waitFor(t, "the shift to start on the host", func() bool { return alive(pidFile) })
	stop() // the operator closes the organizer
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("the organizer did not return")
	}
	waitFor(t, "the host to kill the shift and its children", func() bool { return !alive(pidFile) })
	if rs := runs(t, org); len(rs) != 1 || rs[0].Status != "interrupted" {
		t.Fatalf("the interrupted shift should be recorded: %+v", rs)
	}
}

func TestLosingTheConnectionKillsTheRemoteShift(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pidFile := filepath.Join(t.TempDir(), "pid")
	org, h, _ := organizerAndHost(t, ctx, "sleep 300 & echo $! > "+pidFile+"; wait")
	done := make(chan error, 1)
	go func() { done <- runVia(ctx, t, org, "remote", h) }()
	waitFor(t, "the shift to start on the host", func() bool { return alive(pidFile) })
	h.mu.Lock()
	h.workers["w1"].conn.Close() // the network drops
	h.mu.Unlock()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("the organizer did not notice the lost host")
	}
	waitFor(t, "the host to kill the orphaned shift", func() bool { return !alive(pidFile) })
	if rs := runs(t, org); len(rs) != 1 || rs[0].Status != "lost" {
		t.Fatalf("the lost shift should be recorded: %+v", rs)
	}
}

func TestAgentsOnlyGoToHostsThatHaveWhatTheyNeed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	org, h, _ := organizerAndHost(t, ctx, "")
	write(t, filepath.Join(org, "agents", "other.md"), "---\nenabled: true\npriority: 1\nhost: w1\ngh_user: nobody\n---\nneeds a login the host lacks\n")
	write(t, filepath.Join(org, "agents", "anyone.md"), "---\nenabled: true\npriority: 2\nhost: any\ngh_user: nobody\n---\nany host, same problem\n")
	h.setNeeds([]string{"nobody", "someone"}) // the organizer asks the host about the new login
	waitFor(t, "the host to answer", func() bool {
		for _, hs := range h.hosts() {
			if hs.Name == "w1" && hs.Caps != nil && contains(hs.Caps.GhUsers, "someone") && len(hs.MissingGh) > 0 {
				return true
			}
		}
		return false
	})
	team := open(t, org)
	st := team.status(team.schedule(time.Now(), nil, h.hosts()))
	if got := st.Hosts[0].String(); got != "w1 online, missing gh nobody" {
		t.Errorf("host line: %q", got)
	}
	if s := agentState(st, "other").State; s != "host w1 lacks gh nobody" {
		t.Errorf("other: %q", s)
	}
	if s := agentState(st, "anyone").State; s != "no host ready: w1 lacks gh nobody" {
		t.Errorf("anyone: %q", s)
	}
	if st.Next != "remote" {
		t.Errorf("next should be remote, got %q", st.Next)
	}
}

func TestHostOnAnotherClaudeAccountIsNotUsed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	org, wk := t.TempDir(), t.TempDir()
	write(t, filepath.Join(org, "rota.conf"), "listen: 127.0.0.1\nlisten_port: 0\nusage: off\ncommit: false\nclaude_bin: "+fakeClaudeAs(t, org, "", "me@example.com")+"\n")
	write(t, filepath.Join(org, "agents", "remote.md"), "---\nenabled: true\nhost: w1\n---\nremote\n")
	oc := open(t, org).Config
	h := newHub(oc)
	ln, _ := h.listen()
	go h.serve(ctx, ln)
	code, _ := oc.Pair()
	write(t, filepath.Join(wk, "rota.conf"), "claude_bin: "+fakeClaudeAs(t, wk, "", "other@example.com")+"\nworker_name: w1\nusage: off\n")
	go open(t, wk).Join(ctx, h.addr, code)
	waitFor(t, "the host to report", func() bool { hs := h.hosts(); return len(hs) == 1 && hs[0].Caps != nil })
	team := open(t, org)
	st := team.status(team.schedule(time.Now(), nil, h.hosts()))
	if !strings.Contains(st.Hosts[0].String(), "claude account differs (other@example.com)") || st.Next != "" {
		t.Fatalf("a host on another Claude account must be reported and not used: %+v", st)
	}
}

// ---- settings ----

func TestConfigCommentsAndBadValues(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "hold_label: on#2   # inline comment\ndefault_model:   # none\npacing: weekly # x\n")
	c := open(t, root).Config
	if c.HoldLabel != "on#2" || c.Def.Model != "" || c.Pacing != "weekly" {
		t.Fatalf("comments mis-parsed: hold label=%q model=%q pacing=%q", c.HoldLabel, c.Def.Model, c.Pacing)
	}
	for _, bad := range []string{"pacing: sometimes", "tick_minutes: 0", "max_attempts: 0", "clone_cmd: git clone x", "week_start: someday"} {
		write(t, filepath.Join(root, "rota.conf"), bad+"\n")
		if _, err := Open(root, "rota.conf"); err == nil || !strings.Contains(err.Error(), strings.Split(bad, ":")[0]) {
			t.Errorf("%q must be refused, got %v", bad, err)
		}
	}
}

// A setting that does not exist, a typo or an old name, never stops rota: it is ignored,
// and the warning names the setting that was probably meant.
func TestUnknownSettingsAreIgnoredWithASuggestion(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\ndefault_hourz: 05:00-07:00\nhours: 05:00-06:00\nsomething: 1\ntick_minutes: 30\n")
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: true\nmodle: opus\ndefault_effort: high\nweight: 2\n---\nalpha\n")
	team := open(t, root)
	if team.TickMinutes != 30 || team.Agent("alpha").Weight != 2 {
		t.Fatal("the known settings must apply")
	}
	warnings := strings.Join(team.Warnings, "\n")
	for _, want := range []string{"default_hourz is not a setting, ignored (did you mean default_hours?)", "hours is not a setting, ignored (did you mean default_hours?)",
		"modle is not a setting, ignored (did you mean model?)", "default_effort is not a setting, ignored (did you mean effort?)", "something is not a setting, ignored\n"} {
		if !strings.Contains(warnings+"\n", want) {
			t.Errorf("warnings should say %q:\n%s", want, warnings)
		}
	}
	if st := team.Status(time.Now()); len(st.Agents) != 1 {
		t.Fatal("the status must work despite unknown settings")
	}
}

// Every setting is declared once; the documented rota.conf that init writes reads back as
// the starter values and every other default.
func TestTheDocumentedConfigReadsBack(t *testing.T) {
	b, _ := templates.ReadFile("templates/rota.conf")
	starter, err := parseKV(string(b))
	if err != nil {
		t.Fatal(err)
	}
	for k := range starter {
		if _, ok := lookup(ConfigFields, k); !ok {
			t.Errorf("the starter sets %s, which is not a setting", k)
		}
	}
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), ConfFile(starter))
	c := open(t, root).Config
	def := DefaultConfig(root)
	for _, f := range ConfigFields {
		want := f.Get(def)
		if v, ok := starter[f.Key]; ok {
			want = v
		}
		if got := f.Get(c); got != want {
			t.Errorf("%s: got %q, want %q", f.Key, got, want)
		}
		if f.Section == "" {
			t.Errorf("%s has no section", f.Key)
		}
	}
	for _, f := range AgentFields {
		if f.Label == "" {
			t.Errorf("agent setting %s has no label for the editors", f.Key)
		}
	}
}

// The console and the menu change one setting in rota.conf; everything else stays.
func TestSetConfKeyChangesOneLineOnly(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "rota.conf")
	write(t, path, "# the team\ntick_minutes: 60            # how often\nhold_label: held\n\n# added later\n#max_attempts: 3   # retries\n")
	team := open(t, root)
	for _, kv := range [][2]string{{"tick_minutes", "15"}, {"max_attempts", "5"}, {"team_pct", "30"}, {"hold_label", "on#2"}} {
		if err := team.SetSetting(kv[0], kv[1]); err != nil {
			t.Fatal(err)
		}
	}
	want := "# the team\ntick_minutes: 15            # how often\nhold_label: on#2\n\n# added later\nmax_attempts: 5   # retries\nteam_pct: 30\n"
	if got, _ := os.ReadFile(path); string(got) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if c := open(t, root).Config; c.TickMinutes != 15 || c.MaxAttempts != 5 || c.TeamPct != 30 || c.HoldLabel != "on#2" {
		t.Fatalf("values not read back: %+v", c)
	}
	for _, bad := range [][2]string{{"tick_minutes", "soon"}, {"hold_label", "a # b"}, {"nonsense", "1"}} {
		if err := team.SetSetting(bad[0], bad[1]); err == nil {
			t.Errorf("%s: %q should be refused", bad[0], bad[1])
		}
	}
}

// A repository added to the team's is owner/name or a link to it on GitHub; anything else names
// none, so gh never takes it for a flag.
func TestARepositoryIsOwnerAndName(t *testing.T) {
	for in, want := range map[string]string{
		"o/r": "o/r", " acme/Puzzle-Arcade ": "acme/Puzzle-Arcade", "https://github.com/o/r": "o/r",
		"https://github.com/o/r.git": "o/r", "github.com/o/r/pull/3": "o/r", "http://www.github.com/o/r/": "o/r",
		"git@github.com:o/r.git": "o/r", "o/.github": "o/.github",
		"o": "", "o/r/x": "", "-o/r": "", "--web": "", "o/..": "", "o/r x": "", "https://gitlab.com/o/r": "", "": "",
	} {
		if got := RepoOf(in); got != want {
			t.Errorf("RepoOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAgentSettingsAreWrittenSafely(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\ndefault_effort: high\n")
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: true\nmodel: sonnet\neffort: low\n---\nalpha\n")
	write(t, filepath.Join(root, "agents", "beta.md"), "---\nenabled: true\n---\nbeta\n")
	team := open(t, root)
	if a, b := strings.Join(claudeArgs(team.Config, team.Agent("alpha")), " "), strings.Join(claudeArgs(team.Config, team.Agent("beta")), " "); !strings.Contains(a, "--model sonnet --effort low") || !strings.Contains(b, "--effort high") || strings.Contains(b, "--model") {
		t.Fatalf("model and effort, and the team default: %q / %q", a, b)
	}
	if err := team.SetAgent("alpha", map[string]string{"effort": "", "weight": "3", "run_minutes": ""}); err != nil {
		t.Fatal(err)
	}
	if err := team.SetRole("alpha", "alpha, edited"); err != nil {
		t.Fatal(err)
	}
	a := open(t, root).Agent("alpha")
	if a.Effort != "high" || a.Weight != 3 || a.Role != "alpha, edited" || a.Model != "sonnet" {
		t.Fatalf("an empty value takes the team default: %+v", a)
	}
	for _, bad := range []map[string]string{{"effort": "extreme"}, {"hours": "morning"}, {"every": "often"}, {"nonsense": "1"}} {
		if err := team.SetAgent("alpha", bad); err == nil {
			t.Errorf("%v should be refused", bad)
		}
	}
	for _, bad := range []string{"effort: extreme", "every: often"} {
		write(t, filepath.Join(root, "agents", "bad.md"), "---\nenabled: true\n"+bad+"\n---\nbad\n")
		if _, err := Open(root, "rota.conf"); err == nil || !strings.Contains(err.Error(), strings.Split(bad, ":")[0]) {
			t.Errorf("%q in a file should be refused, got %v", bad, err)
		}
	}
}

func TestHoursAreParsedStrictly(t *testing.T) {
	var h Hours
	for _, ok := range []string{"", "any", "05:00-07:00", "05:00-07:00, 15:00-17:00", "23:00-01:00", "00:00-24:00"} {
		if err := h.UnmarshalText([]byte(ok)); err != nil {
			t.Errorf("%q should parse: %v", ok, err)
		}
	}
	for _, bad := range []string{"5-7", "05:00", "05:00-05:00", "25:00-26:00", "morning"} {
		if err := h.UnmarshalText([]byte(bad)); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
	var d Days
	for in, want := range map[string]string{"": "", "any": "", "fri": "fri", "Friday": "fri", "mon, thu": "mon, thu"} {
		if err := d.UnmarshalText([]byte(in)); err != nil {
			t.Errorf("%q should parse: %v", in, err)
		} else if got, _ := d.MarshalText(); string(got) != want {
			t.Errorf("%q reads as %q", in, got)
		}
	}
	for _, bad := range []string{"fr", "funday", "mon-fri", "mon,"} {
		if err := d.UnmarshalText([]byte(bad)); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
	var e Interval
	for in, want := range map[string]string{"90m": "90m", "2h": "2h", "1d": "1d", "7d": "7d", "20": "20h", "36h": "36h"} {
		if err := e.UnmarshalText([]byte(in)); err != nil {
			t.Errorf("%q: %v", in, err)
		} else if got, _ := e.MarshalText(); string(got) != want {
			t.Errorf("%q reads as %s", in, got)
		}
	}
}

// ---- shifts ----

func TestFailedRepoSyncFailsTheShiftBeforeClaudeStarts(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "claude-ran")
	write(t, filepath.Join(root, "rota.conf"), "usage: off\ntriage_model: off\ncommit: false\nlisten: off\nclaude_bin: "+fakeClaude(t, root, "touch "+marker)+"\nclone_cmd: sh -c exit_1_{repo}\n")
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: true\nrepos: example/nope\n---\nalpha\n")
	putNotes(t, root, "alpha", "qa: still to read")
	if err := runShift(t, root, "alpha", "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("claude was started although the repo could not be cloned")
	}
	if !strings.Contains(inboxText(open(t, root), "alpha"), "from qa: still to read") || strings.Contains(readAll(filepath.Join(root, "journal", "*.md")), "Inbox read this shift") {
		t.Fatal("a shift whose claude never ran read no notes: they wait")
	}
	if rs := runs(t, root); len(rs) != 1 || rs[0].Status != "failed" || rs[0].Turns != 0 {
		t.Fatalf("a failed sync must be recorded as failed, with no turns: %+v", rs)
	}
	if journal := readAll(filepath.Join(root, "journal", "*.md")); !strings.Contains(journal, "repository sync failed") || !strings.Contains(journal, "example/nope") {
		t.Fatalf("the journal should explain the failure:\n%s", journal)
	}
}

// spend.csv has one layout: a new file gets the header, every shift a row in Row's order,
// and a file with another header is refused, not guessed at.
func TestSpendFileRoundTrips(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\n")
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: true\n---\nalpha\n")
	c := open(t, root).Config
	want := []Run{{Time: at("2026-10-21T10:00:00Z"), Agent: "alpha", Status: "ok", Turns: 4, Seconds: 70, Session: "s1", In: 1, Out: 2, CacheRead: 3, CacheWrite: 4, HasPct: true, PctUsed: 0.5, PctAfter: 40, SessAfter: 9},
		{Time: at("2026-10-22T10:00:00Z"), Agent: "alpha", Status: "error", Retry: "no", Reason: "It would only repeat, with a comma"}}
	for _, r := range want {
		if err := appendRun(c, r); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(filepath.Join(root, "spend.csv"))
	if !strings.HasPrefix(string(b), strings.Join(SpendColumns, ",")+"\n") || strings.Count(string(b), "\n") != 3 {
		t.Fatalf("one header, one row per shift:\n%s", b)
	}
	got := runs(t, root)
	for i := range want {
		got[i].Time = got[i].Time.UTC()
		if fmt.Sprint(got[i]) != fmt.Sprint(want[i]) {
			t.Fatalf("row %d reads back as\n%+v\nnot\n%+v", i, got[i], want[i])
		}
	}
	write(t, filepath.Join(root, "spend.csv"), "time,agent,turns\n2026-10-20T10:00:00+01:00,alpha,3\n")
	if _, err := Open(root, "rota.conf"); err == nil || !strings.Contains(err.Error(), "the header is not") {
		t.Fatalf("another layout is refused: %v", err)
	}
}

func TestWithUsageOffThePoolIsNotEnforced(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\ncommit: false\nclaude_bin: "+fakeClaude(t, root, "")+"\n")
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: true\n---\nalpha\n")
	if err := runShift(t, root, "", "", false); err != nil {
		t.Fatal(err)
	}
	if count(runs(t, root), "alpha") != 1 {
		t.Fatal("alpha should run when usage is off")
	}
}

// What a shift's Claude Code learns stays in the team folder, memory/<agent>, never under
// ~/.claude where the human's own sessions keep theirs.
func TestAShiftKeepsItsMemoryInTheTeamFolder(t *testing.T) {
	root := t.TempDir()
	stub := fakeClaude(t, root, `printf '%s\n' "$@" > "$PWD/../../args"`)
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\ncommit: false\nsync_repos: false\ntriage_model: off\nclaude_bin: "+stub+"\n")
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: true\ntools: Read,Bash(git *)\n---\nalpha\n")
	if err := runShift(t, root, "alpha", "", true); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(root, "args"))
	args := strings.Split(strings.TrimSpace(string(b)), "\n")
	i, tools := slices.Index(args, "--settings"), slices.Index(args, "--allowedTools")
	if n := strings.Count("\n"+string(b), "\n--settings\n"); n != 1 { // claude takes the last one
		t.Fatalf("the shift's claude gets %d --settings, want one: %q", n, args)
	}
	if i < 0 || i+1 >= len(args) || tools < i {
		t.Fatalf("the shift's claude gets no --settings before the flags that take every word after them: %q", args)
	}
	var settings map[string]string
	want := filepath.Join(root, "memory", "alpha")
	if err := json.Unmarshal([]byte(args[i+1]), &settings); err != nil || settings["autoMemoryDirectory"] != want {
		t.Fatalf("its memory is not the team folder's memory/alpha: %q %v", args[i+1], err)
	}
	if fi, err := os.Stat(want); err != nil || !fi.IsDir() {
		t.Fatalf("memory/alpha is not made for it: %v", err)
	}
}

func TestShiftActsAsTheConfiguredGhAccount(t *testing.T) {
	root := t.TempDir()
	fakeGh(t, `[ "$1 $2 $3 $4" = "auth token --user someone" ] && { echo tok-someone; exit 0; }; exit 1`)
	stub := fakeClaude(t, root, `printf '%s' "$GH_TOKEN" > "$PWD/../../seen-token"`)
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\ncommit: false\nsync_repos: false\ntriage_model: off\nclaude_bin: "+stub+"\n")
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: true\ngh_user: someone\n---\nalpha\n")
	write(t, filepath.Join(root, "agents", "beta.md"), "---\nenabled: true\ngh_user: nobody\n---\nbeta\n")
	if err := runShift(t, root, "alpha", "", true); err != nil {
		t.Fatal(err)
	}
	if seen, _ := os.ReadFile(filepath.Join(root, "seen-token")); string(seen) != "tok-someone" {
		t.Fatalf("the shift should run with someone's gh token, got %q", seen)
	}
	os.Remove(filepath.Join(root, "seen-token"))
	if err := runShift(t, root, "beta", "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "seen-token")); err == nil {
		t.Fatal("claude ran although gh has no login for the configured account")
	}
	if journal := readAll(filepath.Join(root, "journal", "*.md")); !strings.Contains(journal, "not logged in as nobody") {
		t.Fatalf("the journal should say which gh login is missing:\n%s", journal)
	}
}

func TestLiveLogAndShiftLog(t *testing.T) {
	root := t.TempDir()
	stream := filepath.Join(root, "claude")
	os.WriteFile(stream, []byte("#!/bin/bash\nhead -n1 >/dev/null\n"+
		`echo '{"type":"system","subtype":"init","tools":["Bash","Read"]}'`+"\n"+
		`echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"cargo check --workspace"}}]}}'`+"\n"+
		`echo '{"type":"assistant","message":{"content":[{"type":"text","text":"Looks fine."}]}}'`+"\n"+
		`echo '{"type":"result","subtype":"success","is_error":false,"result":"## Summary\nchecked\n## PRs\nnone\n## Next time\n-\n## Notes to teammates\nnone","num_turns":2,"session_id":"s9","usage":{"input_tokens":5,"output_tokens":6}}'`+"\n"), 0o755)
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\ncommit: false\nsync_repos: false\nclaude_bin: "+stream+"\n")
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: true\n---\nalpha\n")
	out := captureStdout(t, func() {
		if err := runShift(t, root, "alpha", "", true); err != nil {
			t.Error(err)
		}
	})
	if !strings.Contains(out, "[alpha] claude started, 2 tools") || !strings.Contains(out, "[alpha] Bash cargo check --workspace") || strings.Contains(out, "Looks fine") {
		t.Fatalf("live_log tools shows the tool calls and not the agent's words:\n%s", out)
	}
	trs := open(t, root).Transcripts("alpha")
	if len(trs) != 1 || trs[0].Run == nil || trs[0].Run.Session != "s9" {
		t.Fatalf("one transcript, matched to its run: %+v", trs)
	}
	events, ended := ReadEvents(trs[0].Path)
	if !ended || len(events) != 4 || events[1].Tool != "Bash" || events[1].Arg != "cargo check --workspace" || events[3].Turns != 2 {
		t.Fatalf("the transcript holds the whole stream: %+v", events)
	}
	if journal := readAll(filepath.Join(root, "journal", "*.md")); !strings.Contains(journal, "checked") || !strings.Contains(journal, "log logs/") || !strings.Contains(journal, "| 2 turns |") {
		t.Fatalf("the journal carries the result, the turns and the log:\n%s", journal)
	}
}

// A shift that ends with something for the human rewrites their report: report_model gets the
// report as it stands, the shift's "For the human" lines and the open questions, and its
// summary is the report. A shift with nothing for them leaves it alone. Without an answer the
// lines wait unsummarized, and the next rewrite folds them in. A new day starts from the last
// day's report.
func TestTheReportIsAnExecutiveSummaryOfWhatShiftsSay(t *testing.T) {
	root := t.TempDir()
	claude := filepath.Join(root, "claude")
	os.WriteFile(claude, []byte("#!/bin/bash\n"+ // a shift's first message is one line; the report's prompt is all of it
		`case "$*" in *"--tools "*) printf '%s' "$CLAUDE_CODE_DISABLE_AUTO_MEMORY" > `+root+`/memory-off; cat > `+root+`/prompt; cat `+root+`/summary 2>/dev/null || exit 1;; *) head -n1 >/dev/null; cat `+root+`/final;; esac`+"\n"), 0o755)
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\ncommit: false\nsync_repos: false\ntriage_model: off\ntimezone: UTC\nclaude_bin: "+claude+"\n")
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: true\n---\nalpha\n")
	result := func(text string) string {
		b, _ := json.Marshal(map[string]any{"type": "result", "subtype": "success", "is_error": false, "result": text, "num_turns": 1, "session_id": "s1", "usage": map[string]int{}})
		return string(b) + "\n"
	}
	shift := func(when, final, summary string) string {
		t.Helper()
		write(t, filepath.Join(root, "final"), result(final))
		os.Remove(filepath.Join(root, "summary"))
		os.Remove(filepath.Join(root, "prompt"))
		if summary != "" {
			write(t, filepath.Join(root, "summary"), result(summary))
		}
		captureStdout(t, func() {
			if err := runShift(t, root, "alpha", when, true); err != nil {
				t.Error(err)
			}
		})
		b, _ := os.ReadFile(filepath.Join(root, "prompt"))
		return string(b)
	}
	read := func(day string) string {
		b, _ := os.ReadFile(filepath.Join(root, "reports", day+".md"))
		return string(b)
	}

	prompt := shift("2026-10-26T09:00:00Z", "## For the human\nMerged [backend#63](https://x/63): sign-in works again.\n\n## Summary\nmerged it\n\n## Notes to teammates\n@human: May I rename the app?",
		"# A title of its own\n\nSign-in works again.\n\n## Done today\n- [backend#63](https://x/63): sign-in works again.")
	if !strings.Contains(prompt, "09:00 alpha: Merged [backend#63](https://x/63): sign-in works again.") || !strings.Contains(prompt, "alpha asks: May I rename the app?") || strings.Contains(prompt, "merged it") {
		t.Fatalf("report_model gets the shift's lines for the human and the open questions, not the rest:\n%s", prompt)
	}
	if off, _ := os.ReadFile(filepath.Join(root, "memory-off")); string(off) != "1" {
		t.Errorf("the summary's claude keeps an auto memory under ~/.claude: CLAUDE_CODE_DISABLE_AUTO_MEMORY=%q", off)
	}
	want := "# Monday 26 October\n\nSign-in works again.\n\n## Done today\n- [backend#63](https://x/63): sign-in works again.\n"
	if got := read("2026-10-26"); got != want {
		t.Fatalf("the summary is the report, under the day's title:\n%s", got)
	}
	if prompt := shift("2026-10-26T10:00:00Z", "## For the human\nnone\n\n## Summary\nnothing", "unused"); prompt != "" || read("2026-10-26") != want {
		t.Fatalf("a shift with nothing for the human leaves the report alone:\n%s", read("2026-10-26"))
	}
	shift("2026-10-26T11:00:00Z", "## For the human\nFound: the editor loses unsaved work.\n## Summary\nfiled #9", "")
	if got := read("2026-10-26"); got != want+"\n## Not summarized yet\n\n- 11:00 alpha: Found: the editor loses unsaved work.\n" {
		t.Fatalf("without a summary the lines wait under the summary:\n%s", got)
	}
	prompt = shift("2026-10-27T08:00:00Z", "## For the human\n**Waiting for you:** [creator#551](https://x/551) needs your dictation test.\n## Summary\nasked", "Two things wait for you.")
	for _, w := range []string{"from 2026-10-26", "Sign-in works again.", "- 11:00 alpha: Found: the editor loses unsaved work.\n- 08:00 alpha: **Waiting for you:** [creator#551]", "this section starts empty today"} {
		if !strings.Contains(prompt, w) {
			t.Fatalf("a new day starts from the last report and folds in what waited (%q):\n%s", w, prompt)
		}
	}
	if got := read("2026-10-27"); got != "# Tuesday 27 October\n\nTwo things wait for you.\n\n## Not summarized yet\n\n- 08:00 alpha: **Waiting for you:** [creator#551](https://x/551) needs your dictation test.\n" {
		t.Fatalf("a line whose link the summary left out keeps waiting:\n%s", got)
	}
	if read("2026-10-26") != want+"\n## Not summarized yet\n\n- 11:00 alpha: Found: the editor loses unsaved work.\n" {
		t.Fatal("an earlier day's report stays as it was")
	}
	shift("2026-10-28T08:00:00Z", "## For the human\nMerged [creator#551](https://x/551).\n## Summary\nmerged", "")
	if got := read("2026-10-28"); got != "# Wednesday 28 October\n\nNot summarized for today yet: what waited on [2026-10-27](2026-10-27) still waits.\n\n## Not summarized yet\n\n"+
		"- 08:00 alpha: **Waiting for you:** [creator#551](https://x/551) needs your dictation test.\n- 08:00 alpha: Merged [creator#551](https://x/551).\n" {
		t.Fatalf("without a summary, a new day points to the last report and keeps what waited:\n%s", got)
	}
	prompt = shift("2026-10-28T09:00:00Z", "## For the human\nnone\n## Notes to teammates\n@human: Shall I pin the toolchain?", "[creator#551](https://x/551) is merged.")
	if !strings.Contains(prompt, "from 2026-10-27") || !strings.Contains(prompt, "Two things wait for you.") || strings.Count(prompt, "needs your dictation test") != 1 || !strings.Contains(prompt, "09:00 alpha asks: Shall I pin the toolchain?") {
		t.Fatalf("the day's first summary starts from the last day's, with each waiting line once and the new question:\n%s", prompt)
	}
	if got := read("2026-10-28"); got != "# Wednesday 28 October\n\n[creator#551](https://x/551) is merged.\n" {
		t.Fatalf("the day's own summary:\n%s", got)
	}
}

// After days without a summary, the report points to the last day that has one, and keeps
// every line that waited. A link is accounted for only by the same link: #5 is not #51.
func TestTheReportKeepsWhatWaitedAcrossDaysWithoutASummary(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\nreport_model: off\ntimezone: UTC\n")
	write(t, filepath.Join(root, "reports", "2026-10-26.md"), "# Monday 26 October\n\nOne thing waits.\n")
	write(t, filepath.Join(root, "reports", "2026-10-27.md"), "# Tuesday 27 October\n\n"+carried+" [2026-10-26](2026-10-26) still waits.\n\n"+notSummarized+"\n\n- 09:00 dev: Opened [#5](https://x/5).\n")
	team := open(t, root)
	if err := team.report(context.Background(), "dev", "Merged [#6](https://x/6).", nil, at("2026-10-28T08:00:00Z")); err != nil {
		t.Fatal(err)
	}
	want := "# Wednesday 28 October\n\n" + carried + " [2026-10-26](2026-10-26) still waits.\n\n" + notSummarized + "\n\n- 09:00 dev: Opened [#5](https://x/5).\n- 08:00 dev: Merged [#6](https://x/6).\n"
	if b, _ := os.ReadFile(filepath.Join(root, "reports", "2026-10-28.md")); string(b) != want {
		t.Fatalf("the report points to the last summary and keeps what waited:\n%s", b)
	}
	if left := unaccounted("- a: [#5](https://x/5)\n- b: [#6](https://x/6).\n", "[#51](https://x/51) and https://x/6."); left != "- a: [#5](https://x/5)\n" {
		t.Fatalf("only the same link accounts for a line: %q", left)
	}
}

// A shift that stops because the organizer stops keeps its Claude session, and the agent's
// next shift, asked for at once, resumes it: --resume, the notes that came since, what is
// left of run_minutes, and the checkouts as they were (no sync).
func TestAShiftTheOrganizerStopsResumesWhereItLeftOff(t *testing.T) {
	root := t.TempDir()
	claude := filepath.Join(root, "claude")
	os.WriteFile(claude, []byte("#!/bin/bash\nread -r prompt\n"+
		`case "$*" in *--resume*) printf '%s' "$*" > `+root+`/args; printf '%s' "$prompt" > `+root+`/prompt; echo '{"type":"result","subtype":"success","is_error":false,"result":"## Summary\nresumed and finished","num_turns":2,"session_id":"sess-9","usage":{}}';;`+
		` *) echo '{"type":"system","subtype":"init","session_id":"sess-9"}'; sleep 30;; esac`+"\n"), 0o755)
	conf := "usage: off\nlisten: off\ncommit: false\ntriage_model: off\nclaude_bin: " + claude + "\n"
	write(t, filepath.Join(root, "rota.conf"), conf+"sync_repos: false\n")
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: true\nrun_minutes: 60\nrepos: x/r\ntools: Read\n---\nalpha\n")
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond) // the organizer stops
	defer cancel()
	team := open(t, root)
	captureStdout(t, func() { team.RunShift(ctx, "alpha", time.Now(), true, false) })
	if rs := runs(t, root); len(rs) != 1 || rs[0].Status != "interrupted" || rs[0].Session != "sess-9" || rs[0].Retry != "yes" {
		t.Fatalf("the interrupted shift keeps its session: %+v", rs)
	}
	if !open(t, root).Asked()["alpha"] {
		t.Fatal("the agent's next shift is asked for at once")
	}
	if j := readAll(filepath.Join(root, "journal", "*.md")); !strings.Contains(j, "resumes where it left off") {
		t.Fatalf("the journal says it resumes:\n%s", j)
	}
	if _, why := open(t, root).schedule(time.Now(), nil, nil).eligible(open(t, root).Agent("alpha")); why != "ready to resume its interrupted shift" {
		t.Fatalf("the status says it resumes: %q", why)
	}
	open(t, root).LeaveNote("alpha", "", "human", "the tests are flaky")
	write(t, filepath.Join(root, "rota.conf"), conf+"sync_repos: true\nclone_cmd: sh -c exit_1_{repo}\n") // a sync would fail the shift
	captureStdout(t, func() {
		if err := runShift(t, root, "alpha", "", false); err != nil {
			t.Error(err)
		}
	})
	args, _ := os.ReadFile(filepath.Join(root, "args"))
	prompt, _ := os.ReadFile(filepath.Join(root, "prompt"))
	if !strings.Contains(string(args), "--resume sess-9 --allowedTools Read") {
		t.Fatalf("it resumes the session, before the variadic flags: %s", args)
	}
	if !strings.Contains(string(prompt), "stopped after 0 minutes") || !strings.Contains(string(prompt), "You have 60 minutes") || !strings.Contains(string(prompt), "the tests are flaky") {
		t.Fatalf("the prompt says where it stopped, how long it has, and the notes since:\n%s", prompt)
	}
	if rs := runs(t, root); len(rs) != 2 || rs[1].Status != "ok" {
		t.Fatalf("the resumed shift finishes without a sync: %+v", rs)
	}
	if _, ok := open(t, root).toResume("alpha"); ok {
		t.Fatal("a resumed shift is resumed once")
	}
	// the organizer was killed before it could record the stop: its next start asks for the shift
	team = open(t, root)
	team.keepForResume("alpha", resumable{Session: "sess-10", Began: time.Now(), Minutes: 40})
	team.askToResume()
	if !open(t, root).Asked()["alpha"] {
		t.Fatal("the organizer's start asks for a shift that kept its session")
	}
	for spent, left := range map[int]int{40: 20, 50: resumeMinutes} {
		if j := resumeJob(Job{Args: []string{"-p"}, Repos: []string{"x/r"}}, team.Agent("alpha"), resumable{Session: "s", Minutes: spent}, "", 0); j.RunMinutes != left || j.Repos != nil {
			t.Fatalf("after %d of 60 minutes, %d are left, without a sync: %+v", spent, left, j)
		}
	}
	if j := resumeJob(Job{Args: []string{"-p"}}, team.Agent("alpha"), resumable{Session: "s", Minutes: 10}, "", 5); j.RunMinutes != 5 {
		t.Fatalf("a window the agent owns ends its resumed shift too: %d minutes", j.RunMinutes)
	}
}

// An agent's setup for a repository lives in the team folder's secrets and is put into its
// checkout as .env before each shift, {work} replaced by its work folder on this machine.
func TestAnAgentsRepositorySetupIsPutInItsCheckout(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\ncommit: false\nsync_repos: false\ntriage_model: off\nclaude_bin: "+fakeClaude(t, root, "")+"\n")
	write(t, filepath.Join(root, "agents", "qa.md"), "---\nenabled: true\nrepos: o/creator, o/backend\n---\nqa\n")
	write(t, filepath.Join(root, "secrets", "qa", "creator.env"), "TUNNEL_NAME=shop-creator-qa\nSHOP_DATA_HOME={work}/creator-home\n")
	os.MkdirAll(filepath.Join(root, "work", "qa", "creator"), 0o755)
	captureStdout(t, func() {
		if err := runShift(t, root, "qa", "", true); err != nil {
			t.Error(err)
		}
	})
	work := filepath.Join(root, "work", "qa")
	b, err := os.ReadFile(filepath.Join(work, "creator", ".env"))
	if err != nil || string(b) != placedMark+"\nTUNNEL_NAME=shop-creator-qa\nSHOP_DATA_HOME="+work+"/creator-home\n" {
		t.Fatalf("the checkout gets the agent's setup, with its work folder filled in: %v\n%s", err, b)
	}
	if fi, _ := os.Stat(filepath.Join(work, "creator", ".env")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("only the user reads it: %v", fi.Mode())
	}
	if _, err := os.Stat(filepath.Join(work, "backend", ".env")); err == nil {
		t.Fatal("a repository without a setup gets none")
	}
	c := open(t, root).Config
	os.Remove(filepath.Join(root, "secrets", "qa", "creator.env"))
	if placeRepoEnv(c, "qa", work, "o/creator"); fileExists(filepath.Join(work, "creator", ".env")) {
		t.Fatal("when the setup is removed, the copy rota placed goes too")
	}
	write(t, filepath.Join(root, "secrets", "qa", "backend.env"), "X=1\n")
	os.MkdirAll(filepath.Join(work, "backend"), 0o755)
	os.Symlink(filepath.Join(root, "secrets", "qa.env"), filepath.Join(work, "backend", ".env"))
	if err := placeRepoEnv(c, "qa", work, "o/backend"); err == nil || fileExists(filepath.Join(root, "secrets", "qa.env")) {
		t.Fatalf("a link in place of .env is not followed: %v", err)
	}
	os.Remove(filepath.Join(work, "backend", ".env"))
	repo := filepath.Join(work, "backend")
	exec.Command("git", "-C", repo, "init", "-q").Run()
	write(t, filepath.Join(repo, ".env"), "TRACKED=1\n")
	exec.Command("git", "-C", repo, "add", ".env").Run()
	if err := placeRepoEnv(c, "qa", work, "o/backend"); err == nil || readFile(filepath.Join(repo, ".env")) != "TRACKED=1\n" {
		t.Fatalf("a .env git tracks is never written, so secrets are never committed: %v", err)
	}
}

// {root} in an agent's secrets is the team folder on the machine running the shift.
func TestAnAgentsSecretsMayPointIntoTheTeamFolder(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\n")
	write(t, filepath.Join(root, "secrets", "qa.env"), "AWS_CONFIG_FILE={root}/secrets/qa/aws-config\n")
	env, err := loadEnv(open(t, root).Config, filepath.Join("secrets", "qa.env"))
	if err != nil || !slices.Contains(env, "AWS_CONFIG_FILE="+root+"/secrets/qa/aws-config") {
		t.Fatalf("{root} is the team folder: %v", err)
	}
}

func TestInitCreatesTheTeamFolderAndNeverOverwrites(t *testing.T) {
	root := filepath.Join(t.TempDir(), "team")
	captureStdout(t, func() {
		if err := Init(root); err != nil {
			t.Error(err)
		}
	})
	for _, want := range []string{"rota.conf", "brief.md", "agents", filepath.Join("secrets", "example.env"), "journal", "inbox", "reports"} {
		if _, err := os.Stat(filepath.Join(root, want)); err != nil {
			t.Fatalf("init should create %s: %v", want, err)
		}
	}
	write(t, filepath.Join(root, "agents", "mine.md"), "---\nenabled: true\nweight: 9\n---\nmine\n")
	write(t, filepath.Join(root, "brief.md"), "my brief\n")
	captureStdout(t, func() { Init(root) })
	if b, _ := os.ReadFile(filepath.Join(root, "brief.md")); string(b) != "my brief\n" {
		t.Fatal("init overwrote an edited brief")
	}
	appendFile(filepath.Join(root, "rota.conf"), "usage: off\n") // tests never touch the network
	if st := status(t, root, ""); len(st.Agents) != 1 || st.Agents[0].Name != "mine" {
		t.Fatalf("a fresh team folder ships no agents, only the ones you add: %+v", st.Agents)
	}
	if conf, _ := os.ReadFile(filepath.Join(root, "rota.conf")); !strings.Contains(string(conf), "week_start: claude") || !strings.Contains(string(conf), "#team_pct: 20") {
		t.Fatalf("rota.conf has the starter's values set and the rest commented out:\n%s", conf)
	}
}

func TestProbesAndRepoSyncTimeOut(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "claude-ran")
	defer func(p, s time.Duration) { probeTimeout, syncTimeout = p, s }(probeTimeout, syncTimeout)
	probeTimeout, syncTimeout = time.Second, 1200*time.Millisecond
	fakeGh(t, "sleep 30") // a gh that never answers
	slow := filepath.Join(root, "slowclone")
	os.WriteFile(slow, []byte("#!/bin/bash\nsleep 30\n"), 0o755) // a clone that never finishes
	write(t, filepath.Join(root, "rota.conf"), "usage: off\ntriage_model: off\nlisten: off\ncommit: false\nclaude_bin: "+fakeClaude(t, root, "touch "+marker)+"\nclone_cmd: "+slow+" {repo} {dir}\n")
	start := time.Now()
	if caps := capabilities(open(t, root).Config, []string{"someone"}); len(caps.GhUsers) != 0 || time.Since(start) > 5*time.Second {
		t.Fatalf("a hung gh is not a login, and the probe gives up in time (%s)", time.Since(start))
	}
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: true\nrepos: example/slow\n---\nalpha\n")
	start = time.Now()
	if err := runShift(t, root, "alpha", "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil || time.Since(start) > 10*time.Second {
		t.Fatalf("the sync must give up in time, without claude (%s)", time.Since(start))
	}
	if journal := readAll(filepath.Join(root, "journal", "*.md")); !strings.Contains(journal, "timed out after 1.2s") {
		t.Fatalf("the journal should say the sync timed out:\n%s", journal)
	}
}

func TestHousekeeping(t *testing.T) {
	root := t.TempDir()
	captureStdout(t, func() { Init(root) })
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\nteam_pct: 25\n")
	captureStdout(t, func() { Init(root) })
	if conf, _ := os.ReadFile(filepath.Join(root, "rota.conf")); string(conf) != "usage: off\nlisten: off\nteam_pct: 25\n" {
		t.Fatalf("init leaves an existing rota.conf alone:\n%s", conf)
	}
	old, recent := filepath.Join(root, "logs", "2020-01-01"), filepath.Join(root, "logs", time.Now().Format("2006-01-02"))
	os.MkdirAll(old, 0o755)
	os.MkdirAll(recent, 0o755)
	pruneLogs(open(t, root).Config)
	if _, err := os.Stat(old); err == nil {
		t.Fatal("old logs should be pruned")
	}
	if _, err := os.Stat(recent); err != nil {
		t.Fatal("recent logs must be kept")
	}
}

// ---- the schedule ----

// usageServer serves Claude's usage endpoint from a file holding the weekly percent; the
// stub claude it returns adds step to it, as if every shift used that much of the window.
func usageServer(t *testing.T, root string, step int, body string) (conf string) {
	t.Helper()
	usageFile := filepath.Join(root, "usage.txt")
	write(t, usageFile, "30")
	resets := time.Now().Add(72 * time.Hour).Format(time.RFC3339)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := os.ReadFile(usageFile)
		fmt.Fprintf(w, `{"five_hour":{"utilization":1,"resets_at":%q},"seven_day":{"utilization":%s,"resets_at":%q},"limits":[]}`, resets, strings.TrimSpace(string(b)), resets)
	}))
	t.Cleanup(srv.Close)
	t.Setenv(usageTokenEnv, "x")
	prev, floor := usageURL, usageFloor
	t.Cleanup(func() { usageURL, usageFloor = prev, floor })
	usageURL, usageFloor = srv.URL, 0 // these tests read usage around shifts that last a moment
	stub := fakeClaude(t, root, fmt.Sprintf("v=$(cat %s); echo $((v+%d)) > %s\n%s", usageFile, step, usageFile, body))
	return "usage: on\nclaude_bin: " + stub + "\nlisten: off\ncommit: false\nsync_repos: false\nteam_pct: 20\nweb: off\n"
}

func today(hhmm string) string { return time.Now().Format("2006-01-02") + "T" + hhmm + ":00Z" }

func TestPoolPacingAndOfflineHost(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), usageServer(t, root, 1, "")+"default_every: 0\n")
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: true\npriority: 1\nweight: 3\n---\nalpha\n")
	write(t, filepath.Join(root, "agents", "beta.md"), "---\nenabled: true\npriority: 2\nweight: 1\nhost: w9\n---\nbeta\n")
	for i := 0; i < 4; i++ {
		if err := runShift(t, root, "", today("09:00"), false); err != nil {
			t.Fatal(err)
		}
	}
	if n := count(runs(t, root), "alpha"); n != 3 {
		t.Fatalf("alpha runs while its 2.14%% share has room (at 0, 1 and 2%%), then is held; ran %d times", n)
	}
	st := status(t, root, today("09:00"))
	if st.Pool.Used != 3 || fmt.Sprintf("%.2f", st.Pool.Quota) != "2.86" || !st.Pool.Enforced {
		t.Fatalf("pool: %+v", st.Pool)
	}
	if st.Usage == nil || st.Usage.Weekly != 33 || st.UsageError != "" {
		t.Fatalf("the status carries what Claude reports: %+v %q", st.Usage, st.UsageError)
	}
	if s := agentState(st, "alpha").State; s != "team pool, 105.0% used today" {
		t.Fatalf("alpha: %q", s)
	}
	if s := agentState(st, "beta").State; s != "host w9 never paired" {
		t.Fatalf("beta: %q", s)
	}
}

func TestNextRunIsShown(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\ncommit: false\nsync_repos: false\nclaude_bin: "+fakeClaude(t, root, "")+"\ndefault_every: 20h\n")
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: true\n---\nalpha\n")
	write(t, filepath.Join(root, "agents", "beta.md"), "---\nenabled: true\nevery: 7d\n---\nbeta\n")
	write(t, filepath.Join(root, "agents", "gamma.md"), "---\nenabled: false\n---\ngamma\n")
	now := "2026-10-26T10:00:00Z" // a Monday
	for _, name := range []string{"alpha", "beta"} {
		if err := runShift(t, root, name, now, true); err != nil {
			t.Fatal(err)
		}
	}
	st := status(t, root, now)
	if a, b, g := agentState(st, "alpha"), agentState(st, "beta"), agentState(st, "gamma"); !a.NextRun.Equal(at(now).Add(20*time.Hour)) || !b.NextRun.Equal(at(now).AddDate(0, 0, 7)) || g.NextNote != "disabled" {
		t.Fatalf("next runs: %v %v %q", a.NextRun, b.NextRun, g.NextNote)
	}
}

func TestMissedDueTimeRunsOnceAndDueAgentsRunBackToBack(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\ncommit: false\nsync_repos: false\nclaude_bin: "+fakeClaude(t, root, "")+"\ndefault_every: 1h\n")
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: true\npriority: 1\n---\nalpha\n")
	clock := func(h, m int) string { return fmt.Sprintf("2026-10-26T%02d:%02d:00Z", h, m) }
	n := func() int { return count(runs(t, root), "alpha") }
	runShift(t, root, "", clock(10, 0), false)
	runShift(t, root, "", clock(10, 30), false)
	if n() != 1 {
		t.Fatalf("not due yet at 10:30, ran %d times", n())
	}
	for _, m := range [][2]int{{13, 10}, {13, 11}, {13, 40}} { // three hours without an organizer: one run is owed, not three
		runShift(t, root, "", clock(m[0], m[1]), false)
	}
	if n() != 2 {
		t.Fatalf("a missed due time must produce exactly one run, got %d", n())
	}
	runShift(t, root, "", clock(14, 11), false) // due again an hour after the real run
	if n() != 3 {
		t.Fatalf("the next run comes an hour after the actual one, got %d", n())
	}
	// several due agents: the organizer serves them back to back, not one per tick
	write(t, filepath.Join(root, "agents", "beta.md"), "---\nenabled: true\npriority: 2\n---\nbeta\n")
	write(t, filepath.Join(root, "agents", "gamma.md"), "---\nenabled: true\npriority: 3\n---\ngamma\n")
	os.Remove(filepath.Join(root, "spend.csv"))
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\ncommit: false\nsync_repos: false\nclaude_bin: "+fakeClaude(t, root, "")+"\ndefault_every: 1h\ntick_minutes: 60\n")
	stop := organize(t, root)
	waitFor(t, "all three agents to run within seconds", func() bool {
		rs := runs(t, root)
		return count(rs, "alpha") == 1 && count(rs, "beta") == 1 && count(rs, "gamma") == 1
	})
	stop()
}

func TestHoldReasonNamesTheLongestWait(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), usageServer(t, root, 1, ""))
	write(t, filepath.Join(root, "agents", "hourly.md"), "---\nenabled: true\npriority: 1\nevery: 1h\n---\nhourly\n")
	write(t, filepath.Join(root, "agents", "weekly.md"), "---\nenabled: true\npriority: 2\nevery: 7d\n---\nweekly\n")
	for _, name := range []string{"hourly", "hourly", "weekly"} { // 3% used: the pool's 2.86% is gone for the day
		if err := runShift(t, root, name, today("09:00"), true); err != nil {
			t.Fatal(err)
		}
	}
	st := status(t, root, today("09:00"))
	if s := agentState(st, "hourly").State; s != "team pool, 105.0% used today" {
		t.Fatalf("hourly is due in an hour, but the pool is gone for the day: %q", s)
	}
	if s := agentState(st, "weekly").State; !strings.HasPrefix(s, "due ") {
		t.Fatalf("weekly waits a week, longer than the pool: %q", s)
	}
}

func TestHigherPriorityAgentsGetTheirFairShareFirst(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), usageServer(t, root, 2, "")+"default_every: 0\n")
	for i, name := range []string{"top", "mid", "low"} { // equal weights: 0.95% of the 2.86% pool each
		write(t, filepath.Join(root, "agents", name+".md"), fmt.Sprintf("---\nenabled: true\npriority: %d\n---\n%s\n", i+1, name))
	}
	if err := runShift(t, root, "mid", today("09:00"), true); err != nil {
		t.Fatal(err) // mid overruns its share: 2% of the pool is used
	}
	st := status(t, root, today("09:00"))
	if s := agentState(st, "low").State; !strings.Contains(s, "held for higher priority") {
		t.Fatalf("what is left of the pool is top's fair share; low waits: %q", s)
	}
	if s := agentState(st, "top").State; s != "ready" {
		t.Fatalf("top still has its share: %q", s)
	}
	if err := runShift(t, root, "", today("09:00"), false); err != nil {
		t.Fatal(err)
	}
	if rs := runs(t, root); len(rs) != 2 || rs[1].Agent != "top" {
		t.Fatalf("the next shift is top's: %+v", rs)
	}
}

// A higher-priority agent's unused share is held for it only while it can still run that day.
func TestAShareIsHeldOnlyForAgentsThatCanStillRun(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), usageServer(t, root, 2, ""))
	write(t, filepath.Join(root, "agents", "top.md"), "---\nenabled: true\npriority: 1\nevery: 7d\n---\ntop\n")
	write(t, filepath.Join(root, "agents", "mid.md"), "---\nenabled: true\npriority: 2\nevery: 7d\n---\nmid\n")
	write(t, filepath.Join(root, "agents", "low.md"), "---\nenabled: true\npriority: 3\nevery: 0\n---\nlow\n")
	team := open(t, root)
	appendRun(team.Config, Run{Time: at(today("06:00")), Agent: "top", Status: "ok", HasPct: true}) // done for the day, its share unused
	if err := runShift(t, root, "mid", today("09:00"), true); err != nil {
		t.Fatal(err) // mid uses 2% of the 2.86% pool
	}
	if s := agentState(status(t, root, today("09:00")), "low").State; s != "ready" {
		t.Fatalf("top cannot run again today, so its share does not hold low back: %q", s)
	}
	team.RunNow("top") // asked to run now, top can use its share today after all
	if s := agentState(status(t, root, today("09:00")), "low").State; !strings.Contains(s, "held for higher priority") {
		t.Fatalf("top was asked to run now; its share is held for it: %q", s)
	}
}

func TestThePoolStopsARunningShiftWhenItRunsOut(t *testing.T) {
	defer func(lo, hi time.Duration) { poolPollMin, poolPollMax = lo, hi }(poolPollMin, poolPollMax)
	poolPollMin, poolPollMax = 50*time.Millisecond, 50*time.Millisecond
	root := t.TempDir()
	pidFile := filepath.Join(root, "pid")
	write(t, filepath.Join(root, "rota.conf"), usageServer(t, root, 3, "sleep 60 & echo $! > "+pidFile+"; wait"))
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: true\n---\nalpha\n")
	start := time.Now()
	if err := runShift(t, root, "", "", false); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 30*time.Second {
		t.Fatal("the shift was not stopped when the pool ran out")
	}
	team := open(t, root)
	if len(team.Runs) != 1 || team.Runs[0].Status != "pool" || team.Runs[0].Retry != "" {
		t.Fatalf("one shift stopped by the pool, not retried: %+v", team.Runs)
	}
	if s := team.schedule(time.Now(), nil, nil).stat(team.Agent("alpha")); s.Runs != 1 || s.Attempts != 0 || s.Failed {
		t.Fatalf("a shift the pool stopped is a run, not a failed attempt: %+v", s)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "journal", time.Now().Format("2006-01-02")+".md")); !strings.Contains(string(b), "team pool for today ran out") {
		t.Fatalf("the journal says why the shift stopped:\n%s", b)
	}
}

// A running shift's usage is read more often the closer the team comes to the end of its pool,
// in proportion to what is left: every 20 minutes with all of it, every minute with none.
func TestThePoolIsReadMoreOftenAsItRunsLow(t *testing.T) {
	for _, c := range []struct {
		pool, used float64
		want       time.Duration
	}{
		{8, 0, poolPollMax},
		{8, 4, 10*time.Minute + 30*time.Second},
		{8, 7.6, time.Minute + 57*time.Second},
		{8, 8, poolPollMin},
		{8, 9, poolPollMin},
		{0, 0, poolPollMin},
	} {
		if got := nextPoolCheck(c.pool, c.used); got != c.want {
			t.Fatalf("%.1f of a %.0f%% pool used: next reading in %s, want %s", c.used, c.pool, got, c.want)
		}
	}
}

// After "rate limited" the endpoint is not asked again for as long as it says, or twice the last
// wait; a reading halves the wait. Within a minute of the last request, or the wait, the last
// answer stands, except the reading after a shift, which is fresh unless Claude is refusing.
func TestUsageIsNotAskedAgainSoonAfterARefusal(t *testing.T) {
	root := t.TempDir()
	var asked atomic.Int32
	var status, after atomic.Value
	status.Store(429)
	after.Store("")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		if ra := after.Load().(string); ra != "" {
			w.Header().Set("Retry-After", ra)
		}
		w.WriteHeader(status.Load().(int))
		fmt.Fprint(w, `{"five_hour":{"utilization":1},"seven_day":{"utilization":30},"limits":[]}`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv(usageTokenEnv, "x")
	defer func(u string) { usageURL = u }(usageURL)
	usageURL = srv.URL
	write(t, filepath.Join(root, "rota.conf"), "usage: on\n")
	c := open(t, root).Config
	back := func(d time.Duration) { // as if the last request was d ago
		uc := cachedUsage(c)
		uc.Asked = uc.Asked.Add(-d)
		if !uc.Time.IsZero() {
			uc.Time = uc.Time.Add(-d)
		}
		saveUsage(c, *uc)
	}
	if _, err := fetchUsage(c, 0, 3); err == nil || asked.Load() != 1 || cachedUsage(c).Wait != 2*time.Minute {
		t.Fatalf("a refusal is not retried, and waits two minutes: %v, %d requests, %s", err, asked.Load(), cachedUsage(c).Wait)
	}
	if _, err := fetchUsage(c, 0, 0); err == nil || !strings.Contains(err.Error(), "HTTP 429") || !strings.Contains(err.Error(), "asked again from") || asked.Load() != 1 {
		t.Fatalf("meanwhile the refusal is the answer: %v, %d requests", err, asked.Load())
	}
	after.Store("600")
	back(3 * time.Minute)
	if fetchUsage(c, measure, 0); asked.Load() != 2 || cachedUsage(c).Wait != 10*time.Minute {
		t.Fatalf("it asked again after the wait, and waits as long as Retry-After says: %d, %s", asked.Load(), cachedUsage(c).Wait)
	}
	status.Store(200)
	back(11 * time.Minute)
	if u, err := fetchUsage(c, 0, 0); err != nil || u.Weekly != 30 || cachedUsage(c).Wait != 5*time.Minute {
		t.Fatalf("a reading halves the wait: %v %+v %s", err, u, cachedUsage(c).Wait)
	}
	if u, err := fetchUsage(c, 0, 0); err != nil || u.Weekly != 30 || asked.Load() != 3 {
		t.Fatalf("while the wait lasts, the last reading stands: %v %d", err, asked.Load())
	}
	if fetchUsage(c, measure, 0); asked.Load() != 4 {
		t.Fatal("the reading after a shift is fresh while Claude answers")
	}
	uc := cachedUsage(c)
	uc.Wait = 0
	saveUsage(c, *uc)
	if fetchUsage(c, 0, 0); asked.Load() != 4 {
		t.Fatal("within a minute of the last request the last reading stands")
	}
	status.Store(429)
	after.Store("")
	back(2 * time.Minute)
	fetchUsage(c, measure, 0)
	if _, err := fetchUsage(c, 20, 0); err == nil || asked.Load() != 5 {
		t.Fatalf("after a refusal no reading lets a shift start, however recent: %v", err)
	}
}

// The organizer asks Claude for its usage only when a shift is due: the last reading, however
// old, tells it whether one is.
func TestTheOrganizerReadsUsageOnlyWhenAShiftIsDue(t *testing.T) {
	root := t.TempDir()
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		fmt.Fprint(w, `{"five_hour":{"utilization":1},"seven_day":{"utilization":30},"limits":[]}`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv(usageTokenEnv, "x")
	defer func(u string) { usageURL = u }(usageURL)
	usageURL = srv.URL
	write(t, filepath.Join(root, "rota.conf"), "usage: on\nlisten: off\ncommit: false\nsync_repos: false\nclaude_bin: "+fakeClaude(t, root, "")+"\n")
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: true\nevery: 1d\n---\nalpha\n")
	team := open(t, root)
	appendRun(team.Config, Run{Time: time.Now(), Agent: "alpha", Status: "ok"})
	b, _ := json.Marshal(usageCache{Time: time.Now().Add(-30 * time.Minute), Usage: Usage{Weekly: 30}})
	write(t, team.Path(stateFile+".usage"), string(b))
	o := &organizer{Config: team.Config, monitor: monitor{Config: team.Config, started: time.Now(), lastShift: map[string]time.Time{}}}
	for i := 0; i < 3; i++ {
		o.look(context.Background(), open(t, root), false, false)
	}
	if n := asked.Load(); n != 0 {
		t.Fatalf("nothing is due, so Claude is not asked: %d readings", n)
	}
	if st := open(t, root).Status(time.Now()); st.Usage == nil || st.Usage.Weekly != 30 || st.UsageError != "" {
		t.Fatalf("the status shows the last reading: %+v %q", st.Usage, st.UsageError)
	}
	open(t, root).RunNow("alpha")
	captureStdout(t, func() { o.look(context.Background(), open(t, root), false, false) })
	if n := asked.Load(); n != 2 || len(open(t, root).Runs) != 2 {
		t.Fatalf("a due shift reads usage before it starts and after it ends: %d readings", n)
	}
}

func TestAssistantTurnsAreCountedInACutOffStream(t *testing.T) {
	stream := "{\"type\":\"system\",\"subtype\":\"init\"}\n{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"hi\"}]}}\n{\"type\":\"user\",\"message\":{}}\n{\"type\":\"assistant\",\"message\":{\"content\":[]}}\ngarbage"
	if n := assistantTurns(stream); n != 2 || assistantTurns("") != 0 {
		t.Fatalf("want 2 turns, got %d", n)
	}
}

// A background job that ends after the agent's report wakes claude again, and each wake ends
// with its own result: the shift is all of them.
func TestAShiftWokenAfterItsReportCountsEveryResult(t *testing.T) {
	root := t.TempDir()
	first := `{"type":"result","subtype":"success","is_error":false,"result":"the first report","num_turns":161,"session_id":"s1","usage":{"input_tokens":318,"output_tokens":99079,"cache_read_input_tokens":27497428,"cache_creation_input_tokens":262447}}`
	woken := `{"type":"system","subtype":"task_notification","status":"completed"}`
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\ncommit: false\nsync_repos: false\nclaude_bin: "+
		fakeClaude(t, root, "echo '"+first+"'; echo '"+woken+"'")+"\n")
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: true\n---\nalpha\n")
	captureStdout(t, func() {
		if err := runShift(t, root, "alpha", "", true); err != nil {
			t.Error(err)
		}
	})
	if rs := runs(t, root); len(rs) != 1 || rs[0].Status != "ok" || rs[0].Turns != 164 || rs[0].Session != "s1" ||
		rs[0].In != 319 || rs[0].Out != 99081 || rs[0].CacheRead != 27497431 || rs[0].CacheWrite != 262451 {
		t.Fatalf("the shift's turns and tokens are those of both results: %+v", rs)
	}
	if journal := readAll(filepath.Join(root, "journal", "*.md")); !strings.Contains(journal, "| 164 turns |") ||
		!strings.Contains(journal, "ran in ") || strings.Contains(journal, "the first report") {
		t.Fatalf("the journal counts every turn and carries the last report:\n%s", journal)
	}
}

func TestFailedShiftsAreRetriedWithBackoffThenReportedFailed(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\ncommit: false\nsync_repos: false\nclaude_bin: "+fakeClaude(t, root, "exit 1")+
		"\ndefault_every: 20h\nmax_attempts: 3\nretry_minutes: 10\n") // the stub answers nothing to the triage either: retry
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: true\n---\nalpha\n")
	t0 := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC) // a Monday
	when := func(d time.Duration) string { return t0.Add(d).Format(time.RFC3339) }
	state := func(d time.Duration) AgentStatus { return agentState(status(t, root, when(d)), "alpha") }
	if err := runShift(t, root, "alpha", when(0), false); err != nil { // attempt 1 fails: wait 10 minutes, not 20 hours
		t.Fatal(err)
	}
	if s := state(time.Minute).State; !strings.HasPrefix(s, "retry 2/3 at Mon") {
		t.Fatalf("the first retry: %q", s)
	}
	if err := runShift(t, root, "alpha", when(5*time.Minute), false); err == nil || !strings.Contains(err.Error(), "retry 2/3") || len(runs(t, root)) != 1 {
		t.Fatalf("a retry before its time is held, got %v", err)
	}
	if err := runShift(t, root, "alpha", when(11*time.Minute), false); err != nil { // attempt 2 fails: the wait doubles
		t.Fatal(err)
	}
	if err := runShift(t, root, "alpha", when(25*time.Minute), false); err == nil || !strings.Contains(err.Error(), "retry 3/3") {
		t.Fatalf("the second retry waits 20 minutes, got %v", err)
	}
	if err := runShift(t, root, "alpha", when(32*time.Minute), false); err != nil { // attempt 3 fails: the series is over
		t.Fatal(err)
	}
	if a := state(33 * time.Minute); !strings.HasPrefix(a.State, "failed, due Tue") || a.Runs != 1 || len(runs(t, root)) != 3 {
		t.Fatalf("after max_attempts the series is one failed run: %+v", a)
	}
	j, _ := os.ReadFile(filepath.Join(root, "journal", "2026-10-26.md"))
	for _, want := range []string{"Attempt 1 of 3; retry at Mon", "Attempt 2 of 3", "Failed after 3 attempts; this counts as the run"} {
		if !strings.Contains(string(j), want) {
			t.Fatalf("the journal should say %q:\n%s", want, j)
		}
	}
	if err := runShift(t, root, "alpha", when(34*time.Minute), false); err == nil { // the failed run anchors the interval
		t.Fatal("a failed series must not run again before its interval")
	}
}

func TestTheOrganizerJudgesWhetherARetryWouldHelp(t *testing.T) {
	root := t.TempDir()
	// the shift dies without a result; the judgment (no tools, json output) gets a verdict
	verdict := `{\"retry\":false,\"reason\":\"It ran out of time running the full test suite\",\"note\":\"Run only the unit tests of the crate you touched\"}`
	stub := fakeClaude(t, root, `case "$*" in *"--tools "*) printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"`+verdict+`"}'; exit 0;; esac; exit 1`)
	conf := "usage: off\nlisten: off\ncommit: false\nsync_repos: false\nclaude_bin: " + stub + "\ndefault_every: 20h\nmax_attempts: 3\nretry_minutes: 10\n"
	write(t, filepath.Join(root, "rota.conf"), conf+"triage_model: haiku\n")
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: true\n---\nalpha\n")
	t0 := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	if err := runShift(t, root, "alpha", t0.Format(time.RFC3339), false); err != nil {
		t.Fatal(err)
	}
	if rs := runs(t, root); len(rs) != 1 || rs[0].Status != "no-json" || rs[0].Retry != "no" || !strings.Contains(rs[0].Reason, "ran out of time") {
		t.Fatalf("the verdict is in the spend row: %+v", rs)
	}
	if s := agentState(status(t, root, t0.Add(time.Minute).Format(time.RFC3339)), "alpha").State; !strings.HasPrefix(s, "failed, due Tue") {
		t.Fatalf("a failure judged not worth retrying ends the series: %q", s)
	}
	if j, _ := os.ReadFile(filepath.Join(root, "journal", "2026-10-26.md")); !strings.Contains(string(j), "Not retried: It ran out of time running the full test suite. This counts as the run") {
		t.Fatalf("the journal carries the verdict:\n%s", j)
	}
	if inbox := inboxText(open(t, root), "alpha"); !strings.Contains(inbox, "from organizer: Run only the unit tests of the crate you touched.") {
		t.Fatalf("the advice waits in the agent's inbox:\n%s", inbox)
	}
	write(t, filepath.Join(root, "rota.conf"), conf+"triage_model: off\n") // with triage off the same failure is retried
	if err := runShift(t, root, "alpha", t0.Add(2*time.Minute).Format(time.RFC3339), true); err != nil {
		t.Fatal(err)
	}
	if s := agentState(status(t, root, t0.Add(3*time.Minute).Format(time.RFC3339)), "alpha").State; !strings.HasPrefix(s, "retry 2/3 at Mon") {
		t.Fatalf("with triage off the failure is retried: %q", s)
	}
}

func TestShiftsStartOnlyInsideTheAgentsHours(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\ncommit: false\nsync_repos: false\nclaude_bin: "+fakeClaude(t, root, "")+
		"\ndefault_every: 12h\ndefault_hours: 05:00-07:00, 15:00-17:00\n")
	write(t, filepath.Join(root, "agents", "developer.md"), "---\nenabled: true\npriority: 2\nhours: 05:00-07:00\n---\ndev\n")
	write(t, filepath.Join(root, "agents", "errors.md"), "---\nenabled: true\npriority: 1\nevery: 8h\n---\nerr\n") // the team's windows
	write(t, filepath.Join(root, "agents", "night.md"), "---\nenabled: true\npriority: 3\nhours: 23:00-01:00\n---\nnight\n")
	day := time.Date(2026, 10, 26, 0, 0, 0, 0, time.Local) // a Monday
	clock := func(h, m int) string {
		return day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute).Format(time.RFC3339)
	}
	n := func(agent string) int { return count(runs(t, root), agent) }
	runShift(t, root, "", clock(4, 0), false) // 04:00: nobody starts
	if n("developer")+n("errors")+n("night") != 0 {
		t.Fatal("no shift may start outside the agents' hours")
	}
	if err := runShift(t, root, "developer", clock(4, 0), false); err == nil || !strings.Contains(err.Error(), "outside hours, next Mon 05:00") {
		t.Fatalf("a named agent is held too, got %v", err)
	}
	if a := agentState(status(t, root, clock(4, 0)), "night"); a.NextRun.Format("Mon 15:04") != "Mon 23:00" {
		t.Fatalf("night's next window: %v", a.NextRun)
	}
	runShift(t, root, "", clock(5, 0), false) // 05:00: errors, then the developer, back to back
	runShift(t, root, "", clock(5, 0), false)
	if n("errors") != 1 || n("developer") != 1 {
		t.Fatalf("both morning agents run once: errors %d developer %d", n("errors"), n("developer"))
	}
	st := status(t, root, clock(6, 0)) // due times outside the hours move to the next window
	if d, e := agentState(st, "developer").State, agentState(st, "errors").State; d != "due Tue 05:00" || e != "due Mon 15:00" {
		t.Fatalf("due times land in the next window: %q %q", d, e)
	}
	runShift(t, root, "", clock(12, 0), false)
	if n("developer") != 1 {
		t.Fatal("nothing starts at noon")
	}
	open(t, root).RunNow("developer") // run now skips the hours, once
	if err := runShift(t, root, "", clock(12, 0), false); err != nil || n("developer") != 2 || len(open(t, root).Asked()) != 0 {
		t.Fatalf("an agent asked to run now runs outside its hours, once: %v, %d runs", err, n("developer"))
	}
	runShift(t, root, "", clock(15, 0), false) // 15:00 is errors' alone; 00:30 belongs to the window that wraps midnight
	runShift(t, root, "", clock(24, 30), false)
	if n("errors") != 2 || n("developer") != 2 || n("night") != 1 {
		t.Fatalf("errors %d developer %d night %d", n("errors"), n("developer"), n("night"))
	}
	m := &monitor{Config: open(t, root).Config, st: Status{Agents: []AgentStatus{{Enabled: true, NextRun: day.Add(5 * time.Hour)}, {Enabled: true, NextRun: day.Add(15 * time.Hour)}, {Enabled: false, NextRun: day.Add(2 * time.Hour)}}}}
	if got := m.wakeBefore(day.Add(4*time.Hour+30*time.Minute), day.Add(5*time.Hour+30*time.Minute)); !got.Equal(day.Add(5*time.Hour + time.Second)) {
		t.Fatalf("the organizer wakes when the first window opens: %s", got)
	}
	if got := m.wakeBefore(day.Add(6*time.Hour), day.Add(7*time.Hour)); !got.Equal(day.Add(7 * time.Hour)) {
		t.Fatalf("a tick that comes first stays: %s", got)
	}
}

// An agent that owns its hours runs only on its days and goes ahead of the others in its
// window, all but those the human asks for; it starts nothing in the window's last quarter
// hour, is told the time its window leaves it, and its shift stops when the window ends,
// which counts as a run.
func TestAnAgentThatOwnsItsHours(t *testing.T) {
	root := t.TempDir()
	slow := filepath.Join(root, "slow")
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\ncommit: false\nsync_repos: false\ntriage_model: off\nclaude_bin: "+
		fakeClaude(t, root, `[ -f "`+slow+`" ] && sleep 30`)+"\n")
	write(t, filepath.Join(root, "agents", "pentester.md"), "---\nenabled: true\npriority: 5\nevery: 0\ndays: fri\nhours: 05:00-08:00\nown_hours: true\nrun_minutes: 180\n---\npentest\n")
	write(t, filepath.Join(root, "agents", "errors.md"), "---\nenabled: true\npriority: 1\nevery: 12h\nhours: 05:00-07:00\n---\nerr\n")
	write(t, filepath.Join(root, "agents", "night.md"), "---\nenabled: true\nevery: 0\ndays: fri\nhours: 23:00-01:00\n---\nnight\n")
	clock := func(day, h, m, s int) string { // day 0 is a Monday, 4 a Friday
		return time.Date(2026, 10, 26+day, h, m, s, 0, time.Local).Format(time.RFC3339)
	}
	team := open(t, root)
	appendRun(team.Config, Run{Time: at(clock(-14, 5, 0, 0)), Agent: "errors", Status: "ok"})   // due for two weeks: it waited longest
	appendRun(team.Config, Run{Time: at(clock(-3, 5, 0, 0)), Agent: "pentester", Status: "ok"}) // last Friday
	n := func(agent string) int { return count(runs(t, root), agent) }
	if s := agentState(status(t, root, clock(0, 5, 0, 0)), "pentester").State; s != "outside hours, next Fri 05:00" {
		t.Fatalf("on a Monday it waits for Friday: %q", s)
	}
	runShift(t, root, "", clock(4, 5, 0, 0), false)
	runShift(t, root, "", clock(4, 5, 30, 0), false)
	if n("pentester") != 3 || n("errors") != 1 {
		t.Fatalf("in its window it goes first, shift after shift: pentester %d, errors %d", n("pentester")-1, n("errors")-1)
	}
	open(t, root).RunNow("errors")
	if runShift(t, root, "", clock(4, 6, 0, 0), false); n("errors") != 2 {
		t.Fatal("the human's request goes ahead of it")
	}
	if s := agentState(status(t, root, clock(4, 7, 50, 0)), "pentester").State; s != "outside hours, next Fri 05:00" {
		t.Fatalf("no shift starts in its window's last quarter hour: %q", s)
	}
	team = open(t, root)
	for now, want := range map[string]string{clock(4, 5, 0, 0): "180", clock(4, 7, 0, 30): "60"} {
		job, vars, _, err := team.brief(team.Agent("pentester"), team.schedule(at(now), nil, nil))
		if err != nil || strconv.Itoa(job.RunMinutes) != want || vars["{run_minutes}"] != want {
			t.Fatalf("at %s its shift has %s minutes: %d, %q, %v", now, want, job.RunMinutes, vars["{run_minutes}"], err)
		}
	}
	write(t, slow, "")
	began := time.Now()
	if err := runShift(t, root, "pentester", clock(4, 7, 59, 57), true); err != nil {
		t.Fatal(err)
	}
	if rs := runs(t, root); time.Since(began) > 20*time.Second || rs[len(rs)-1].Status != "hours" || !rs[len(rs)-1].IsRun() {
		t.Fatalf("the shift stops when its window ends, and that counts as a run: %+v after %s", rs[len(rs)-1], time.Since(began))
	}
	if !strings.Contains(readAll(filepath.Join(root, "journal", "*.md")), "its hours ended at 08:00") {
		t.Fatal("the journal says why the shift stopped")
	}
	if s := agentState(status(t, root, clock(4, 8, 1, 0)), "pentester").State; s != "due Fri 05:00" {
		t.Fatalf("it waits for next Friday, not for a retry: %q", s)
	}
	if s := agentState(status(t, root, clock(4, 0, 30, 0)), "night").State; !strings.HasPrefix(s, "outside hours") {
		t.Fatalf("00:30 on a Friday is Thursday's window: %q", s)
	}
	if s := agentState(status(t, root, clock(5, 0, 30, 0)), "night").State; s != "ready" {
		t.Fatalf("00:30 on a Saturday is Friday's window: %q", s)
	}
}

func TestAnHourHasOneOwner(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\ncommit: false\nsync_repos: false\n")
	for name, front := range map[string]string{
		"alpha": "priority: 1\ndays: fri\nhours: 05:00-08:00\nown_hours: true",
		"beta":  "priority: 2\nhours: 07:00-09:00\nown_hours: true",            // every day: Friday 07:00 is alpha's
		"gamma": "priority: 3\ndays: thu\nhours: 23:00-06:00\nown_hours: true", // into Friday
		"delta": "priority: 4\ndays: sat\nhours: 05:00-08:00\nown_hours: true",
		"eta":   "priority: 5\ndays: sat\nhours: 23:00-06:00\nown_hours: true", // into Sunday
		"theta": "priority: 6\ndays: sun\nhours: 05:00-07:00\nown_hours: true",
		"plain": "priority: 7\ndays: fri\nhours: 05:00-08:00",
	} {
		write(t, filepath.Join(root, "agents", name+".md"), "---\nenabled: true\n"+front+"\n---\n"+name+"\n")
	}
	write(t, filepath.Join(root, "agents", "idle.md"), "---\nenabled: false\ndays: fri\nhours: 05:00-08:00\nown_hours: true\n---\nidle\n")
	team := open(t, root)
	want := []string{
		"agents/beta.md: own_hours ignored: on fri 07:00-08:00 its hours overlap those alpha owns",
		"agents/gamma.md: own_hours ignored: on fri 05:00-06:00 its hours overlap those alpha owns",
		"agents/theta.md: own_hours ignored: on sun 05:00-06:00 its hours overlap those eta owns",
	}
	if !slices.Equal(team.Warnings, want) {
		t.Fatalf("the first by priority keeps an hour, the others are warned:\n%s", strings.Join(team.Warnings, "\n"))
	}
	for name, owns := range map[string]bool{"alpha": true, "beta": false, "gamma": false, "delta": true, "eta": true, "theta": false} {
		if team.Agent(name).OwnHours != owns {
			t.Fatalf("%s owns its hours: %v", name, !owns)
		}
	}
	file := filepath.Join(root, "agents", "delta.md")
	before := readFile(file)
	if err := team.SetAgent("delta", map[string]string{"days": "fri"}); err == nil || err.Error() != "own_hours: on fri 05:00-08:00 its hours would overlap those alpha owns" || readFile(file) != before {
		t.Fatalf("a change that makes an overlap is refused: %v", err)
	}
	if err := team.SetAgent("idle", map[string]string{"enabled": "true"}); err == nil {
		t.Fatal("enabling an agent whose owned hours overlap is refused too")
	}
	if err := team.SetAgent("beta", map[string]string{"weight": "2"}); err != nil {
		t.Fatalf("one already warned may change other settings: %v", err)
	}
	if err := team.SetAgent("delta", map[string]string{"days": "fri", "hours": "08:00-09:00"}); err != nil {
		t.Fatalf("hours that meet are no overlap: %v", err)
	}
	if team = open(t, root); !team.Agent("delta").OwnHours || len(team.Warnings) != 3 {
		t.Fatalf("delta keeps Friday 08:00-09:00: %v", team.Warnings)
	}
}

// ---- the organizer ----

func TestPauseKickCancelAndStateFile(t *testing.T) {
	root := t.TempDir()
	pidFile := filepath.Join(root, "pid")
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\ncommit: false\nsync_repos: false\nclaude_bin: "+fakeClaude(t, root, "sleep 60 & echo $! > "+pidFile+"; wait")+
		"\ntick_minutes: 0.05\ndefault_every: 1h\nweb: off\n")
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: true\n---\nalpha\n")
	team := open(t, root)
	team.Pause()
	if err := team.CancelShift(""); err == nil {
		t.Fatal("with no shift running there is nothing to cancel")
	}
	os.WriteFile(team.cancelFile(), nil, 0o644) // as an older rota left it: not for the next shift
	stop := organize(t, root)
	defer stop()
	waitFor(t, "a paused snapshot", func() bool { st := team.Live(); return st != nil && st.Paused && len(st.Agents) == 1 })
	if _, err := os.Stat(team.cancelFile()); err == nil {
		t.Fatal("a leftover cancel is cleared when the organizer starts")
	}
	time.Sleep(4 * time.Second)
	if len(runs(t, root)) != 0 {
		t.Fatal("a shift ran while paused")
	}
	team.Unpause()
	team.RunNow("alpha")
	waitFor(t, "the asked-for shift to run", func() bool { st := team.Live(); return st != nil && st.Running != nil && st.Running.Agent == "alpha" })
	waitFor(t, "claude to start", func() bool { return alive(pidFile) })
	if err := team.CancelShift("beta"); err == nil {
		t.Fatal("a stop for an agent that is not working")
	}
	os.WriteFile(team.cancelFile(), []byte("beta"), 0o644) // beta's, read once alpha's began: not alpha's
	time.Sleep(3 * time.Second)
	if !alive(pidFile) {
		t.Fatal("a stop for beta stopped alpha's shift")
	}
	if err := team.CancelShift("alpha"); err != nil { // the shift dies, is recorded as stopped, and the organizer goes on
		t.Fatal(err)
	}
	waitFor(t, "the shift to be killed", func() bool { return !alive(pidFile) })
	waitFor(t, "the stop to be recorded", func() bool { rs := runs(t, root); return len(rs) == 1 && rs[0].Status == "stopped" })
	if rs := runs(t, root); !strings.Contains(rs[0].Reason, "Stopped by the operator") {
		t.Fatalf("the reason names the operator: %q", rs[0].Reason)
	}
	waitFor(t, "the organizer to be idle again", func() bool { st := team.Live(); return st != nil && st.Running == nil })
}

// Run now marks an agent due; nothing is dropped. Asked for while another shift runs, every
// agent asked for runs after it, back to back, in the order asked for, and only once.
func TestEveryRunNowRequestIsServedInTheOrderAsked(t *testing.T) {
	root := t.TempDir()
	gate := filepath.Join(root, "gate")
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\ncommit: false\nsync_repos: false\nclaude_bin: "+fakeClaude(t, root, "while [ ! -e "+gate+" ]; do sleep 0.1; done")+
		"\ntick_minutes: 60\ndefault_every: 7d\ndefault_hours: 03:00-03:01\nweb: off\n")
	for name, prio := range map[string]int{"first": 1, "high": 2, "low": 9, "idle": 5} {
		write(t, filepath.Join(root, "agents", name+".md"), fmt.Sprintf("---\nenabled: true\npriority: %d\n---\n%s\n", prio, name))
	}
	team := open(t, root)
	for _, name := range []string{"first", "high", "low", "idle"} { // all ran an hour ago: none is due for a week
		appendRun(team.Config, Run{Time: time.Now().Add(-time.Hour), Agent: name, Status: "ok"})
	}
	stop := organize(t, root)
	defer stop()
	waitFor(t, "the organizer's first look", func() bool { st := team.Live(); return st != nil && len(st.Agents) == 4 })
	team.RunNow("first")
	waitFor(t, "first's shift to start", func() bool { st := team.Live(); return st != nil && st.Running != nil && st.Running.Agent == "first" })
	team.RunNow("low") // while first runs: low, then high
	team.RunNow("high")
	time.Sleep(2500 * time.Millisecond)
	os.WriteFile(gate, nil, 0o644)
	waitFor(t, "every asked-for agent to run", func() bool { rs := runs(t, root); return count(rs, "high") == 2 && count(rs, "low") == 2 })
	var order []string
	for _, r := range runs(t, root)[4:] {
		order = append(order, r.Agent)
	}
	if strings.Join(order, ",") != "first,low,high" {
		t.Fatalf("first, then low, asked for before high: %v", order)
	}
	if len(team.Asked()) != 0 || count(runs(t, root), "idle") != 1 {
		t.Fatalf("requests are cleared once served, and nobody else runs: %v", team.Asked())
	}
}

// The agent that has waited longest goes first, whatever its priority: since it came due, or
// since it was asked for. Asking again keeps its turn, and at the same moment priority decides.
// A teammate's request skips the interval, but not the hours, a retry wait or a pause: only
// the human's Run now does.
func TestTheAgentThatWaitedLongestGoesFirst(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\ncommit: false\nsync_repos: false\nweb: off\ndefault_every: 12h\n")
	for name, front := range map[string]string{"builder": "priority: 2\nuntil_done: true\nhours: any", "lister": "priority: 3\nhours: any",
		"judge": "priority: 1\nhours: any", "night": "priority: 1\nhours: 03:00-03:01", "retry": "priority: 1\nhours: any", "old": "priority: 9\nhours: 03:00-03:01"} {
		write(t, filepath.Join(root, "agents", name+".md"), "---\nenabled: true\n"+front+"\n---\n"+name+"\n")
	}
	team := open(t, root)
	now := today("09:00")
	appendRun(team.Config, Run{Time: at(now).Add(-24 * time.Hour), Agent: "lister", Status: "ok"}) // due since last night
	for _, name := range []string{"builder", "judge", "night"} {
		appendRun(team.Config, Run{Time: at(today("08:30")), Agent: name, Status: "ok"})
	}
	appendRun(team.Config, Run{Time: at(now).Add(-time.Minute), Agent: "retry", Status: "interrupted"}) // its retry waits
	appendRun(team.Config, Run{Time: at(now).Add(-48 * time.Hour), Agent: "old", Status: "ok"})         // due long ago, outside its hours
	team.askFor("builder")                                                                              // it has more
	// The requests go by the test's clock, not the wall clock.
	os.Chtimes(filepath.Join(team.askedDir(), "builder"), at(now), at(now))
	if st := status(t, root, now); st.Next != "lister" {
		t.Fatalf("lister, waiting since last night, goes before builder, asked just now: next %q", st.Next)
	}
	appendRun(team.Config, Run{Time: at(today("08:55")), Agent: "lister", Status: "ok"}) // its shift: not due again today
	team.askFor("judge")
	asked := at(now).Add(-time.Hour)
	for _, name := range []string{"builder", "judge"} {
		os.Chtimes(filepath.Join(team.askedDir(), name), asked, asked)
	}
	if st := status(t, root, now); st.Next != "judge" {
		t.Fatalf("asked for at the same moment, judge has the higher priority: next %q", st.Next)
	}
	os.Chtimes(filepath.Join(team.askedDir(), "judge"), asked.Add(time.Minute), asked.Add(time.Minute))
	team.askFor("builder") // again: it keeps its turn
	if st := status(t, root, now); st.Next != "builder" {
		t.Fatalf("builder, asked for before judge, goes first: next %q", st.Next)
	}
	team.askFor("night")
	team.askFor("retry")
	st := status(t, root, now)
	if s := agentState(st, "night").State; !strings.HasPrefix(s, "outside hours") {
		t.Fatalf("asked for by a teammate, night still keeps its hours: %q", s)
	}
	if s := agentState(st, "retry").State; !strings.HasPrefix(s, "retry 2/") {
		t.Fatalf("asked for by a teammate, retry still waits for its retry: %q", s)
	}
	s := open(t, root).schedule(at(now), nil, nil)
	if s.only = true; s.pick() != nil {
		t.Fatalf("paused, the team's requests wait: %s", s.pick().Name)
	}
	team.RunNow("night")
	team.RunNow("retry")
	team.RunNow("old")
	for i, name := range []string{"night", "retry", "old"} {
		os.Chtimes(filepath.Join(team.askedDir(), name), asked.Add(time.Duration(i)*time.Minute), asked.Add(time.Duration(i)*time.Minute))
	}
	team.RunNow("night") // again: it keeps its place
	if s := open(t, root).schedule(at(now), nil, nil); func() bool { s.only = true; return s.pick() == nil || s.pick().Name != "night" }() {
		t.Fatal("paused, the human's Run now goes, ahead of every agent waiting, its hours skipped, and in the order the human asked")
	}
	if ok, why := open(t, root).schedule(at(now), nil, nil).eligible(team.Agent("retry")); !ok {
		t.Fatalf("the human's Run now skips the retry wait: %q", why)
	}
}

// A paused agent starts no shift, but keeps what asks for it and its turn, and holds back no
// pool from others; the human's Run now still runs it, and resuming it lets it go.
func TestAPausedAgentKeepsItsTurn(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\ncommit: false\nsync_repos: false\nweb: off\ndefault_every: 12h\n")
	write(t, filepath.Join(root, "agents", "builder.md"), "---\nenabled: true\npriority: 2\nuntil_done: true\nhours: any\n---\nbuilder\n")
	write(t, filepath.Join(root, "agents", "lister.md"), "---\nenabled: true\npriority: 3\nhours: any\n---\nlister\n")
	team := open(t, root)
	now := today("09:00")
	appendRun(team.Config, Run{Time: at(today("08:00")), Agent: "builder", Status: "ok"})
	appendRun(team.Config, Run{Time: at(today("08:30")), Agent: "lister", Status: "ok"}) // not due
	team.askFor("builder")
	if err := team.PauseAgent("builder"); err != nil {
		t.Fatal(err)
	}
	team.askFor("lister") // asked for after builder
	// The requests go by the test's clock, not the wall clock.
	for name, ago := range map[string]time.Duration{"builder": 2 * time.Hour, "lister": 30 * time.Minute} {
		os.Chtimes(filepath.Join(team.askedDir(), name), at(now).Add(-ago), at(now).Add(-ago))
	}
	st := status(t, root, now)
	if b := agentState(st, "builder"); b.State != "paused" || !b.Paused || st.Next != "lister" {
		t.Fatalf("paused, builder waits and lister goes: %+v, next %q", b, st.Next)
	}
	if !team.Asked()["builder"] {
		t.Fatal("builder keeps its request")
	}
	team.RunNow("builder")
	if st := status(t, root, now); st.Next != "builder" {
		t.Fatalf("the human's Run now still runs it: next %q", st.Next)
	}
	team.answered("builder")
	team.askFor("builder")
	os.Chtimes(filepath.Join(team.askedDir(), "builder"), at(now).Add(-time.Hour), at(now).Add(-time.Hour))
	if err := team.ResumeAgent("builder"); err != nil {
		t.Fatal(err)
	}
	if st := status(t, root, now); st.Next != "builder" || agentState(st, "builder").Paused {
		t.Fatalf("resumed, builder, asked for first, goes first: next %q", st.Next)
	}
	if team.PauseAgent("nobody") == nil {
		t.Fatal("only an agent of the team is paused")
	}
	os.Remove(team.kickFile())
	team.Pause()
	team.PauseAgent("lister")
	if err := team.ResumeAgent("lister"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(team.kickFile()); err == nil {
		t.Fatal("resumed while the team is paused, the organizer is not woken: that would start a shift")
	}
}

// The human's Run now goes ahead of priority: before an agent due on its own or woken by a
// teammate, and the pool held for higher priority does not hold it back. A teammate's wake-up
// keeps to priority order, and does not take a request from the human.
func TestTheHumansRunNowGoesAheadOfPriority(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), usageServer(t, root, 2, "")+"default_every: 7d\n")
	for name, prio := range map[string]int{"top": 1, "mid": 2, "low": 3} { // equal weights: 0.95% of the 2.86% pool each
		write(t, filepath.Join(root, "agents", name+".md"), fmt.Sprintf("---\nenabled: true\npriority: %d\n---\n%s\n", prio, name))
	}
	team := open(t, root)
	appendRun(team.Config, Run{Time: at(today("08:00")), Agent: "low", Status: "ok"}) // not due for a week
	if err := runShift(t, root, "mid", today("08:30"), true); err != nil {
		t.Fatal(err) // mid overruns its share: 2% of the pool is used, and top's share is held for it
	}
	team.RunNow("mid") // the human asks for mid, which has used up its own share
	team.askFor("low") // a teammate wakes low
	if st := status(t, root, today("09:00")); st.Next != "top" || !strings.HasPrefix(agentState(st, "mid").State, "fair share") ||
		!strings.Contains(agentState(st, "low").State, "held for higher priority") {
		t.Fatalf("mid waits for its share, and low, woken by a teammate, for top: next %q, mid %q, low %q", st.Next, agentState(st, "mid").State, agentState(st, "low").State)
	}
	team.RunNow("low")
	team.askFor("low") // woken again: the request stays the human's
	if st := status(t, root, today("09:00")); st.Next != "low" || agentState(st, "low").State != "ready" {
		t.Fatalf("asked for by the human, low goes first: next %q, low %q", st.Next, agentState(st, "low").State)
	}
	if err := runShift(t, root, "", today("09:00"), false); err != nil {
		t.Fatal(err)
	}
	if rs := runs(t, root); rs[len(rs)-1].Agent != "low" || !open(t, root).Asked()["mid"] {
		t.Fatalf("the next shift is low's, and mid stays asked for: %+v", rs)
	}
}

// Asked for while the pool is used up, an agent waits for the pool, and its request is kept.
func TestRunNowWaitsForThePool(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), usageServer(t, root, 3, "")+"default_every: 7d\n")
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: true\n---\nalpha\n")
	write(t, filepath.Join(root, "agents", "beta.md"), "---\nenabled: true\n---\nbeta\n")
	if err := runShift(t, root, "alpha", today("09:00"), true); err != nil {
		t.Fatal(err) // 3% used: the 2.86% pool is gone for the day
	}
	open(t, root).RunNow("beta")
	if err := runShift(t, root, "", today("09:00"), false); err != nil {
		t.Fatal(err)
	}
	if rs, asked := runs(t, root), open(t, root).Asked(); len(rs) != 1 || !asked["beta"] {
		t.Fatalf("beta waits for the pool and keeps its request: %d runs, %v", len(rs), asked)
	}
	if s := agentState(status(t, root, today("09:00")), "beta").State; s != "team pool, 105.0% used today" {
		t.Fatalf("beta's state is the pool: %q", s)
	}
}

// Paused, the team starts no shift of its own, not even one a teammate asked for, but an agent
// the human asks for still runs, and so does the next shift when the human asks for it.
func TestWhilePausedOnlyAgentsAskedForRun(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\ncommit: false\nsync_repos: false\nclaude_bin: "+fakeClaude(t, root, "")+"\ntick_minutes: 0.05\nweb: off\n")
	write(t, filepath.Join(root, "agents", "due.md"), "---\nenabled: true\npriority: 1\nevery: 0\n---\ndue\n")
	write(t, filepath.Join(root, "agents", "asked.md"), "---\nenabled: true\npriority: 2\nevery: 7d\n---\nasked\n")
	team := open(t, root)
	appendRun(team.Config, Run{Time: time.Now().Add(-time.Hour), Agent: "asked", Status: "ok"})
	team.Pause()
	team.askFor("due") // a teammate's request waits too
	stop := organize(t, root)
	defer stop()
	waitFor(t, "a paused snapshot", func() bool { st := team.Live(); return st != nil && st.Paused })
	team.RunNow("asked")
	waitFor(t, "asked to run", func() bool { return count(runs(t, root), "asked") == 2 && !team.Asked()["asked"] })
	team.LeaveNote("due", "", "human", "look at the logs") // wakes the organizer, which starts nothing
	time.Sleep(4 * time.Second)                            // several ticks
	if count(runs(t, root), "due") != 0 {
		t.Fatal("a paused team must not start the due agent on its own, though a teammate asked for it")
	}
	team.RunNow("") // the human asks for the next shift
	waitFor(t, "the next shift", func() bool { return count(runs(t, root), "due") == 1 })
}

// Unpausing wakes the organizer, so what waited for it starts at once, not at the next tick.
func TestUnpausingWakesTheOrganizer(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\n")
	team := open(t, root)
	team.Pause()
	if err := team.Unpause(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(team.kickFile()); err != nil {
		t.Fatalf("unpausing wakes the organizer: %v", err)
	}
}

func TestStoppingTheOrganizerLeavesTheAgentDue(t *testing.T) {
	root := t.TempDir()
	pidFile := filepath.Join(root, "pid")
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\ncommit: false\nsync_repos: false\nclaude_bin: "+fakeClaude(t, root, "sleep 60 & echo $! > "+pidFile+"; wait")+
		"\ntick_minutes: 0.05\ndefault_every: 20h\nweb: off\n")
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: true\n---\nalpha\n")
	stop := organize(t, root)
	waitFor(t, "claude to start", func() bool { return alive(pidFile) })
	stop() // the operator quits the organizer, not the shift
	team := open(t, root)
	if len(team.Runs) != 1 || team.Runs[0].Status != "interrupted" || team.Runs[0].Turns != 0 {
		t.Fatalf("one interrupted shift, cut off before its first turn: %+v", team.Runs)
	}
	s := team.schedule(time.Now(), nil, nil)
	if ok, why := s.eligible(team.Agent("alpha")); ok || !strings.HasPrefix(why, "retry 2/3 at ") {
		t.Fatalf("alpha waits for its first retry: %v %q", ok, why)
	}
	if ok, why := team.schedule(time.Now().Add(16*time.Minute), nil, nil).eligible(team.Agent("alpha")); !ok || why != "ready, retry 2/3" {
		t.Fatalf("alpha is retried after retry_minutes: %v %q", ok, why)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "journal", time.Now().Format("2006-01-02")+".md")); !strings.Contains(string(b), "organizer was stopped") {
		t.Fatalf("the journal explains the interruption:\n%s", b)
	}
}

// The agent at work leads the status's list; the others keep the team's order.
func TestTheRunningAgentLeadsTheStatus(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\n")
	for _, a := range []string{"alpha", "beta", "gamma"} {
		write(t, filepath.Join(root, "agents", a+".md"), "---\nenabled: true\n---\n"+a+"\n")
	}
	team := open(t, root)
	names := func() string {
		var out []string
		for _, a := range team.Status(time.Now()).Agents {
			out = append(out, a.Name)
		}
		return strings.Join(out, " ")
	}
	before := names()
	last := before[strings.LastIndex(before, " ")+1:]
	b, _ := json.Marshal(Status{Time: time.Now(), Pid: os.Getpid(), StartedAt: time.Now(), Running: &Running{Agent: last, Host: "local", Since: time.Now()}})
	write(t, team.Path(stateFile), string(b))
	if got, want := names(), last+" "+strings.TrimSuffix(before, " "+last); got != want {
		t.Fatalf("the running agent first, then the others as before (%s): %s", before, got)
	}
}

func TestASilentButLiveOrganizerIsReportedNotHidden(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\n")
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: true\n---\nalpha\n")
	team := open(t, root)
	old := Status{Time: time.Now().Add(-5 * time.Hour), Pid: os.Getpid(), StartedAt: time.Now().Add(-6 * time.Hour)}
	b, _ := json.Marshal(old)
	os.WriteFile(team.Path(stateFile), b, 0o644)
	if live := team.Live(); live == nil || !live.Stale {
		t.Fatalf("a live pid with an old snapshot is stale: %+v", live)
	}
	if st := team.Status(time.Now()); !strings.Contains(st.Organizer(), "silent since") {
		t.Fatalf("the status says the organizer is silent: %q", st.Organizer())
	}
	old.Pid = 999999
	b, _ = json.Marshal(old)
	os.WriteFile(team.Path(stateFile), b, 0o644)
	if team.Live() != nil {
		t.Fatal("a dead pid reads as not running")
	}
}

func TestEventsAreRead(t *testing.T) {
	var all []Event
	for _, line := range []string{
		`{"type":"system","subtype":"init","model":"claude-opus-5-5","tools":["Bash","Read"]}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Looking."},{"type":"tool_use","name":"Bash","input":{"command":"ls -la\nmore"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","is_error":true,"content":[{"type":"text","text":"denied"}]}]}}`,
		`{"type":"result","subtype":"error_max_turns","is_error":true,"result":"stopped","num_turns":3}`,
		`repository sync failed`,
	} {
		all = append(all, ParseLine(line)...)
	}
	want := []string{"start", "text", "tool", "output", "end", "note"}
	for i, e := range all {
		if i >= len(want) || e.Kind != want[i] {
			t.Fatalf("events: %+v", all)
		}
	}
	if all[0].Model != "claude-opus-5-5" || all[0].Tools != 2 || all[2].Arg != "ls -la …" || !all[3].Error || all[3].Text != "denied" || all[4].Subtype != "error_max_turns" || all[4].Turns != 3 {
		t.Fatalf("events: %+v", all)
	}
	if all[2].Summary("tools") != "Bash ls -la …" || all[1].Summary("tools") != "" || all[1].Summary("all") != `"Looking."` {
		t.Fatal("summaries")
	}
}

// An empty value in a file is empty: no repositories, any hours. A field that needs a value
// keeps its default. The editors write no line for "the team default".
func TestEmptyValuesAndTheTeamsDefaults(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\ndefault_repos: a/b\ndefault_hours: 05:00-07:00\ndefault_enabled: true\ntick_minutes:\nweek_start: Monday\nteam_pct: 0\ngit_author: Me<me@example.com>\n")
	write(t, filepath.Join(root, "agents", "plain.md"), "---\npriority: 5\n---\nplain\n")
	write(t, filepath.Join(root, "agents", "bare.md"), "---\nrepos:\nhours:\nweight:\n---\nbare\n")
	team := open(t, root)
	p, b := team.Agent("plain"), team.Agent("bare")
	if strings.Join(p.Repos, ",") != "a/b" || len(p.Hours) != 1 || !p.Enabled {
		t.Fatalf("an agent without the keys takes the team's: %+v", p)
	}
	if len(b.Repos) != 0 || len(b.Hours) != 0 || b.Weight != 1 {
		t.Fatalf("empty is empty; a number keeps its default: repos %v hours %v weight %v", b.Repos, b.Hours, b.Weight)
	}
	if team.TickMinutes != 60 || team.Week.Claude || team.Week.Day != time.Monday || team.TeamPct != 0 {
		t.Fatalf("conf: tick %v week %+v pct %v", team.TickMinutes, team.Week, team.TeamPct)
	}
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlog_keep_days: 1\n")
	os.MkdirAll(filepath.Join(root, "logs", "notes"), 0o755)
	pruneLogs(open(t, root).Config)
	if _, err := os.Stat(filepath.Join(root, "logs", "notes")); err != nil {
		t.Fatal("pruning deletes only day folders")
	}
}

func TestTheFrontEndsHelpers(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\ndefault_every: 7d\n")
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: true\n---\nalpha\n")
	write(t, filepath.Join(root, "hosts.json"), `{"linux1":{"hash":"x"}}`)
	putNotes(t, root, "gone", "human: a note for an agent that was removed")
	team := open(t, root)
	if got := strings.Join(team.Inboxes(), ","); got != "alpha,gone" {
		t.Fatalf("inboxes: %s", got)
	}
	for _, f := range AgentFields {
		if f.Key == "host" && strings.Join(team.Options(f), ",") != ",any,linux1" {
			t.Fatalf("host options: %q", team.Options(f))
		}
	}
	appendRun(team.Config, Run{Time: time.Now(), Agent: "alpha", Status: "ok"})
	team = open(t, root)
	st := team.Status(time.Now())
	if r := team.RunWait("alpha", st); r.Why != "" {
		t.Fatalf("asked to run now, alpha can start: %+v", r)
	}
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\ndefault_every: 7d\ndefault_hours: 03:00-03:01\n")
	if r := open(t, root).RunWait("alpha", st); r.Why != "" {
		t.Fatalf("run now skips the hours too: %+v", r)
	}
	write(t, filepath.Join(root, "agents", "far.md"), "---\nenabled: true\nhost: w9\n---\nfar\n")
	if r := open(t, root).RunWait("far", st); r.Why != "host w9 never paired" || r.Next != "when host w9 never paired" {
		t.Fatalf("an agent whose host is away waits for it: %+v", r)
	}
	os.RemoveAll(filepath.Join(root, "agents"))
	if w := strings.Join(open(t, root).Warnings, ""); !strings.Contains(w, "no agents in") {
		t.Fatalf("a team without agents says so: %q", w)
	}
}

// A wake-up is answered by a shift only when nobody is still asked for by name: an attempt
// that failed before claude started does not swallow another agent's request.
func TestARunNowRequestIsNotSwallowedByAnotherShift(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\n")
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: true\n---\nalpha\n")
	team := open(t, root)
	o := &organizer{Config: team.Config, monitor: monitor{Config: team.Config, lastShift: map[string]time.Time{}}}
	asked := time.Now()
	o.lastShift["beta"] = asked.Add(time.Second) // beta started after the request
	if !o.answered(asked) {
		t.Fatal("a bare request is answered by the next shift")
	}
	team.RunNow("alpha")
	if o.answered(asked) {
		t.Fatal("alpha is still asked for: the wake-up must serve it")
	}
}

// With usage on, no shift starts while Claude's usage cannot be read, and the status says the
// team is on hold and why, so agents that do not run surprise nobody.
func TestAnUnreadableUsageHoldsTheTeamVisibly(t *testing.T) {
	root := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "token expired", 401) }))
	t.Cleanup(srv.Close)
	t.Setenv(usageTokenEnv, "x")
	prev := usageURL
	t.Cleanup(func() { usageURL = prev })
	usageURL = srv.URL
	write(t, filepath.Join(root, "rota.conf"), "usage: on\nlisten: off\ncommit: false\nsync_repos: false\n")
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: true\n---\nalpha\n")
	team := open(t, root)
	if err := team.RunShift(context.Background(), "alpha", time.Now(), false, false); err == nil || !strings.Contains(err.Error(), heldText) || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("rota run says why no shift starts: %v", err)
	}
	o := &organizer{Config: team.Config, monitor: monitor{Config: team.Config, started: time.Now(), lastShift: map[string]time.Time{}}}
	o.look(context.Background(), team, false, false)
	st := team.Status(time.Now())
	if st.Held() != heldText || st.Organizer() != "on hold: "+heldText || !strings.Contains(st.UsageError, "HTTP 401: token expired") {
		t.Fatalf("the status says the team is on hold and why: %q %q", st.Organizer(), st.UsageError)
	}
	if len(o.lastShift) != 0 || len(open(t, root).Runs) != 0 || !o.held() {
		t.Fatal("no shift started, and the organizer looks again soon")
	}
	live := *team.Live()
	for name, st := range map[string]Status{"paused": func() Status { s := live; s.Paused = true; return s }(),
		"silent": func() Status { s := live; s.Stale = true; return s }()} {
		if st.Held() != "" || strings.HasPrefix(st.Organizer(), "on hold") {
			t.Fatalf("a %s organizer says that instead: %q", name, st.Organizer())
		}
	}
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\ncommit: false\nsync_repos: false\n")
	if st := open(t, root).Status(time.Now()); st.Held() != "" || st.UsageError != "" {
		t.Fatalf("usage set off clears the hold before the organizer looks again: %q", st.Organizer())
	}
	os.Remove(team.Path(stateFile))
	if st := team.Status(time.Now()); st.Held() != "" || st.UsageError == "" {
		t.Fatalf("without an organizer nothing is on hold, but the error still shows: %q %q", st.Held(), st.UsageError)
	}
}

// The organizer on hold starts the shift soon after usage is set off: it reads a changed
// rota.conf within one slice of its wait, not at its next look.
func TestTheOrganizerReadsChangedSettingsWhileItWaits(t *testing.T) {
	root := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "token expired", 401) }))
	t.Cleanup(srv.Close)
	t.Setenv(usageTokenEnv, "x")
	prevURL, prevSlice := usageURL, waitSlice
	t.Cleanup(func() { usageURL, waitSlice = prevURL, prevSlice })
	usageURL, waitSlice = srv.URL, 100*time.Millisecond
	conf := "listen: off\ncommit: false\nsync_repos: false\nclaude_bin: " + fakeClaude(t, root, "") + "\n"
	write(t, filepath.Join(root, "rota.conf"), "usage: on\n"+conf)
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: true\n---\nalpha\n")
	stop := organize(t, root)
	defer stop()
	team := open(t, root)
	waitFor(t, "the organizer to be on hold", func() bool { live := team.Live(); return live != nil && live.Held() != "" })
	write(t, filepath.Join(root, "rota.conf"), "usage: off\n"+conf)
	waitFor(t, "alpha's shift", func() bool { return len(open(t, root).Runs) == 1 })
}

// An agent asks the human with "@human:" in its final message. The question stays open, and
// in the agent's brief so it is not asked again, until the human answers; the answer goes to
// the agent's inbox.
func TestAgentsAskTheHumanAndGetTheAnswer(t *testing.T) {
	root := t.TempDir()
	body := `printf '%s' '{"type":"result","subtype":"success","is_error":false,"result":"done\n@human: Is production on gpt-5?\n@beta: hi\n@human: none","num_turns":1,"session_id":"s1","usage":{}}'; exit 0`
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\ncommit: false\nsync_repos: false\nclaude_bin: "+fakeClaude(t, root, body)+"\n")
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: true\n---\nalpha\n")
	write(t, filepath.Join(root, "agents", "beta.md"), "---\nenabled: true\n---\nbeta\n")
	for i := 0; i < 2; i++ {
		if err := runShift(t, root, "alpha", today("09:00"), true); err != nil {
			t.Fatal(err)
		}
	}
	team := open(t, root)
	qs := team.Questions()
	if len(qs) != 1 || qs[0].Agent != "alpha" || qs[0].Text != "Is production on gpt-5?" {
		t.Fatalf("one open question from alpha, asked twice: %+v", qs)
	}
	if !strings.Contains(inboxText(team, "beta"), "from alpha: hi") {
		t.Fatal("notes to teammates still reach their inboxes")
	}
	job, _, _, err := team.brief(team.Agent("alpha"), team.schedule(time.Now(), nil, nil))
	if err != nil || !strings.Contains(job.Prompt, ": Is production on gpt-5?") || strings.Contains(job.Prompt, "{questions}") {
		t.Fatalf("alpha's brief lists its open question:\n%s", job.Prompt)
	}
	if team.Asked()["beta"] {
		t.Fatal("a teammate's note waits for a scheduled agent's next shift")
	}
	if err := team.Answer(qs[0].ID, "  no,   luna only "); err != nil {
		t.Fatal(err)
	}
	if !team.Asked()["alpha"] {
		t.Fatal("the answer wakes alpha, though its next shift is not due")
	}
	appendRun(team.Config, Run{Time: time.Now().Add(-time.Minute), Agent: "beta", Status: "ok"}) // not due again for a day
	if ok, _ := open(t, root).schedule(time.Now(), nil, nil).eligible(team.Agent("beta")); ok {
		t.Fatal("beta is not due")
	}
	if err := team.LeaveNote("beta", "", "human", "look at the logs"); err != nil {
		t.Fatal(err)
	}
	if ok, why := open(t, root).schedule(time.Now(), nil, nil).eligible(team.Agent("beta")); !ok {
		t.Fatalf("a note from the human wakes beta before its interval is up: %q", why)
	}
	if b := inboxText(team, "alpha"); !strings.Contains(b, `from the human: answering your question "Is production on gpt-5?": no, luna only`) {
		t.Fatalf("the answer is in alpha's inbox:\n%s", b)
	}
	if len(team.Questions()) != 0 || team.Answer(qs[0].ID, "again") == nil || team.Answer("../rota", "x") == nil {
		t.Fatal("an answered question is closed, and only open questions can be answered")
	}
}

// Several settings are written all or none: one bad value leaves the file as it was.
func TestSettingsAreSavedAllOrNone(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\n")
	team := open(t, root)
	if err := team.SetSettings(map[string]string{"team_pct": "15", "tick_minutes": "0"}); err == nil {
		t.Fatal("tick_minutes 0 is refused")
	}
	if open(t, root).TeamPct != team.TeamPct {
		t.Fatal("nothing was written")
	}
	if err := team.SetSettings(map[string]string{"team_pct": "15", "tick_minutes": "30"}); err != nil || open(t, root).TeamPct != 15 || open(t, root).TickMinutes != 30 {
		t.Fatalf("good values are written: %v", err)
	}
}

// An until-done agent runs again at once while it ends with "More: yes"; then it waits for
// new work, which an issue a teammate's shift opened, a note from the human or an answer brings.
func TestAnUntilDoneAgentWorksUntilNothingIsLeft(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\n")
	write(t, filepath.Join(root, "agents", "dev.md"), "---\nenabled: true\nuntil_done: true\npriority: 1\nrepos: o/backend\non: note, issue-labeled:rota\n---\ndev\n")
	write(t, filepath.Join(root, "agents", "scout.md"), "---\nenabled: true\nevery: 1d\nrepos: o/backend\n---\nscout\n")
	write(t, filepath.Join(root, "agents", "fixer.md"), "---\nenabled: true\nuntil_done: true\nrepos: o/backend\n---\nfixer\n")
	team := open(t, root)
	dev, scout := team.Agent("dev"), team.Agent("scout")
	ran := func(a *Agent, text string) {
		team.answered(a.Name) // its shift started
		r := Run{Time: time.Now(), Agent: a.Name, Status: "ok"}
		team.Runs = append(team.Runs, r)
		team.wake(a, r, text)
	}
	state := func() string { _, why := team.schedule(time.Now(), nil, nil).eligible(dev); return why }
	if state() != "ready" {
		t.Fatalf("an until-done agent that never ran is ready: %q", state())
	}
	ran(dev, "## Summary\nfixed #1\n\nMore: yes")
	if state() != "ready" {
		t.Fatalf("with more to do it runs again at once: %q", state())
	}
	ran(dev, "## Summary\nfixed #2\n\n**More:** no")
	if state() != "waiting for new work" {
		t.Fatalf("with nothing left it waits: %q", state())
	}
	if team.Asked()["fixer"] {
		t.Fatal("two until-done agents do not wake each other")
	}
	if at, why := team.schedule(time.Now(), nil, nil).nextRun(dev); !at.IsZero() || why != "when there is new work" {
		t.Fatalf("its next run is new work: %v %q", at, why)
	}
	bashShift(t, root, "scout", "gh issue create --label rota --title Leak --body-file b.md", "https://github.com/o/backend/issues/3")
	ran(scout, "## Summary\nfiled #3")
	if state() != "waiting for new work" {
		t.Fatalf("a teammate's shift brings it nothing by itself: %q", state())
	}
	// the issue on GitHub: one of its triggers wakes it, and its note says why; but not one that
	// happened during its own shift, which may be that shift's doing
	leak := RepoEvent{Kind: "issue-labeled", Repo: "o/backend", Number: 3, Title: "Leak", Label: "rota", At: team.Runs[len(team.Runs)-2].Time}
	if _, err := team.onEvents([]RepoEvent{leak}, time.Now()); err != nil || state() != "waiting for new work" {
		t.Fatalf("an event during its own shift starts none: %v %q", err, state())
	}
	leak.At = time.Now().Add(eventSlack + 5*time.Minute) // well after its last shift
	if _, err := team.onEvents([]RepoEvent{leak, {Kind: "issue-labeled", Repo: "o/backend", Number: 4, Label: "docs", At: leak.At}, {Kind: "issue-labeled", Repo: "o/web", Number: 5, Label: "rota", At: leak.At}}, time.Now()); err != nil || state() != "ready" {
		t.Fatalf("an event it starts on wakes it: %v %q", err, state())
	}
	if n := team.Notes("dev"); len(n) != 2 || n[1].From != "github" || n[1].About != "o/backend#3" || n[1].Text != "«Leak»: the issue got the label rota (issue-labeled:rota, one of your triggers)" {
		t.Fatalf("each event reaches it as a note, and only those it starts on in its repositories: %+v", n)
	}
	if team.Asked()["fixer"] || len(team.Notes("fixer")) != 0 {
		t.Fatal("an agent without the trigger hears nothing")
	}
	team.changeNotes("dev", func([]Note) []Note { return nil })
	ran(dev, "## Summary\nnothing")
	if state() != "waiting for new work" {
		t.Fatalf("a final message without More waits: %q", state())
	}
	team.LeaveNote("dev", "", "human", "please look at #4")
	if state() != "ready" {
		t.Fatalf("a note from the human wakes it: %q", state())
	}
	ran(dev, "More: no")
	id, _ := team.Ask("dev", "May a rename reuse a name from another app?")
	if err := team.Answer(id, "no"); err != nil || state() != "ready" {
		t.Fatalf("an answer wakes it: %v %q", err, state())
	}
	if _, why := team.schedule(time.Now(), nil, nil).eligible(scout); why == "ready" {
		t.Fatal("a scheduled agent keeps its interval")
	}
	ran(dev, "More: no")
	team.MaxAttempts = 1
	team.Runs[len(team.Runs)-1].Status = "error:crash" // a series that failed for good
	if !strings.HasPrefix(state(), "failed, due ") {
		t.Fatalf("after a failed series its interval applies, not idling: %q", state())
	}
}

// The human may dismiss a question unanswered: the agent's inbox says so, a waiting shift hears
// it, and nothing wakes the agent. An agent withdraws only its own questions, during its shift
// or with a line in its final message, by the ids its brief lists.
func TestAQuestionCanBeDismissedOrWithdrawn(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\n")
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: true\n---\nalpha\n")
	write(t, filepath.Join(root, "agents", "beta.md"), "---\nenabled: true\n---\nbeta\n")
	team := open(t, root)
	banner, _ := team.Ask("alpha", "Keep the old banner?")
	if err := team.Dismiss(banner); err != nil {
		t.Fatal(err)
	}
	if b := inboxText(team, "alpha"); !strings.Contains(b, `from the human: dismissed your question "Keep the old banner?" without an answer`) || team.Asked()["alpha"] {
		t.Fatalf("alpha's inbox says the question was dismissed, and nothing wakes it:\n%s", b)
	}
	if _, _, err := team.WaitAnswer(banner, time.Second, time.Time{}); !errors.Is(err, ErrDismissed) {
		t.Fatalf("a waiting shift hears it was dismissed: %v", err)
	}
	if team.Answer(banner, "yes") == nil || team.Dismiss(banner) == nil {
		t.Fatal("a dismissed question is closed")
	}
	bucket, _ := team.Ask("alpha", "Rename the bucket?")
	if !strings.Contains(team.questionsFor("alpha"), "id "+bucket+": Rename the bucket?") {
		t.Fatalf("the brief lists each question with its id: %s", team.questionsFor("alpha"))
	}
	if err := team.Withdraw("beta", bucket); err == nil || len(team.Questions()) != 1 {
		t.Fatalf("beta cannot withdraw alpha's question: %v", err)
	}
	if err := team.Withdraw("alpha", bucket); err != nil || len(team.Questions()) != 0 {
		t.Fatalf("alpha withdraws its own: %v", err)
	}
	if _, _, err := team.WaitAnswer(bucket, time.Second, time.Time{}); err == nil || !strings.Contains(err.Error(), "withdrawn") {
		t.Fatalf("waiting for a withdrawn question says so: %v", err)
	}
	sdk, _ := team.Ask("alpha", "Bump the SDK?")
	undelivered := team.routeNotes("alpha", "done\n- @human withdraw "+sdk+"\n@human withdraw: nope-1", time.Now())
	if len(team.Questions()) != 0 || len(undelivered) != 1 || !strings.Contains(undelivered[0], "nope-1") {
		t.Fatalf("a final message's line withdraws the question, and a wrong id is reported: %v", undelivered)
	}
	for id, field := range map[string]string{banner: `"dismissed"`, bucket: `"withdrawn"`, sdk: `"withdrawn"`} {
		if b, err := os.ReadFile(filepath.Join(root, "questions", "closed", id+".json")); err != nil || !strings.Contains(string(b), field) {
			t.Fatalf("a closed question is kept with how it closed: %s %v", b, err)
		}
	}
}

// During its shift an agent asks with rota ask and waits for the answer with rota wait: the
// answer arrives at once, or after answer_minutes it is told to set the work aside.
func TestAShiftAsksAndWaitsForTheAnswer(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\nanswer_minutes: 30\n")
	write(t, filepath.Join(root, "agents", "dev.md"), "---\nenabled: true\n---\ndev\n")
	team := open(t, root)
	id, err := team.Ask("dev", "Pin cypress to 15.20.1?")
	if again, _ := team.Ask("dev", " Pin  cypress to 15.20.1? "); err != nil || again != id {
		t.Fatalf("the same open question is asked once: %v %s %s", err, id, again)
	}
	if _, more, err := team.WaitAnswer(id, 100*time.Millisecond, time.Time{}); err != nil || !more {
		t.Fatalf("no answer yet: keep waiting: %v %v", more, err)
	}
	go func() { time.Sleep(300 * time.Millisecond); team.Answer(id, "yes, pin it") }()
	if answer, _, err := team.WaitAnswer(id, time.Minute, time.Time{}); err != nil || answer != "yes, pin it" {
		t.Fatalf("the answer reaches the waiting shift: %q %v", answer, err)
	}
	if answer, _, _ := team.WaitAnswer(id, time.Second, time.Time{}); answer != "yes, pin it" || len(team.Questions()) != 0 {
		t.Fatal("an answered question is kept with its answer, and no longer open")
	}
	team.AnswerMinutes = 0.001
	late, _ := team.Ask("dev", "Rename the bucket?")
	if answer, more, err := team.WaitAnswer(late, time.Minute, time.Time{}); answer != "" || more || err != nil {
		t.Fatalf("after answer_minutes: set it aside: %q %v %v", answer, more, err)
	}
	if _, _, err := team.WaitAnswer("../x", time.Second, time.Time{}); err == nil {
		t.Fatal("only a question's id")
	}
	team.AnswerMinutes = 30
	parked, _ := team.Ask("dev", "Drop the old table?")
	if answer, more, _ := team.WaitAnswer(parked, time.Minute, time.Now().Add(-time.Second)); answer != "" || more {
		t.Fatal("near the shift's end it stops waiting, to put the work away")
	}
}

// After a shift the organizer checks that the agent left every checkout on its default branch
// with nothing uncommitted, and says so in the journal and the agent's inbox when it did not.
func TestAnUncleanCheckoutIsFlagged(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "work", "dev", "app")
	for _, args := range [][]string{{"init", "-q", "-b", "main", repo}, {"-C", repo, "commit", "-q", "--allow-empty", "-m", "start"},
		{"-C", repo, "update-ref", "refs/remotes/origin/main", "HEAD"}, {"-C", repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main"}} {
		if out, err := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\ncommit: false\nsync_repos: false\nclaude_bin: "+
		fakeClaude(t, root, "cd app && git checkout -q -b developer/1-fix && touch half-done.rs")+"\n")
	write(t, filepath.Join(root, "agents", "dev.md"), "---\nenabled: true\nrepos: o/app\n---\ndev\n")
	if err := runShift(t, root, "dev", today("09:00"), true); err != nil {
		t.Fatal(err)
	}
	want := "app with uncommitted changes and branch developer/1-fix checked out, not main"
	if j := readAll(filepath.Join(root, "journal", "*.md")); !strings.Contains(j, "Left unclean: "+want) {
		t.Fatalf("the journal says so:\n%s", j)
	}
	if !strings.Contains(inboxText(open(t, root), "dev"), "your last shift left "+want) {
		t.Fatal("the agent's next shift is told to clean up first")
	}
}

// Before a shift rota removes the agent's checkouts of repositories it no longer has, so no
// shift works on one; a checkout that holds work found nowhere else is kept and said, and folders
// that are not such a checkout (another name, another owner, its own repositories) stay.
func TestACheckoutOfARepositoryNoLongerListedIsRemoved(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "work", "dev")
	git := func(dir string, args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	checkout := func(name, origin string, pushed bool) string {
		dir := filepath.Join(work, name)
		os.MkdirAll(dir, 0o755)
		git(dir, "init", "-q", "-b", "main")
		write(t, filepath.Join(dir, "f.txt"), "one\n")
		git(dir, "add", "f.txt")
		git(dir, "commit", "-q", "-m", "start")
		git(dir, "remote", "add", "origin", origin)
		if pushed {
			git(dir, "update-ref", "refs/remotes/origin/main", "HEAD")
		}
		return dir
	}
	checkout("app", "git@github-x:o/app.git", true)
	checkout("old", "git@github-x:o/old.git", true)
	checkout("Gone", "https://github.com/o/Gone", true)
	write(t, filepath.Join(checkout("dirty", "git@github-x:o/dirty.git", true), "f.txt"), "changed\n")
	checkout("unpushed", "git@github-x:o/unpushed.git", false)
	checkout("lib", "https://github.com/other/lib.git", true)
	checkout("old-copy", "git@github-x:o/old.git", true)
	git(checkout("linked", "git@github-x:o/linked.git", true), "worktree", "add", "-q", filepath.Join(work, "wt-linked"))
	git(checkout("moved", "git@github-x:o/moved.git", true), "worktree", "add", "-q", filepath.Join(work, "wt-moved"))
	os.Rename(filepath.Join(work, "wt-moved"), filepath.Join(work, "wt-moved-elsewhere")) // moved by hand: still its worktree
	write(t, filepath.Join(checkout("untracked", "git@github-x:o/untracked.git", true), "fix.go"), "package x\n")
	detached := checkout("detached", "git@github-x:o/detached.git", true)
	git(detached, "checkout", "-q", "--detach")
	git(detached, "commit", "-q", "--allow-empty", "-m", "on no branch")
	tagged := checkout("tagged", "git@github-x:o/tagged.git", true)
	git(tagged, "commit", "-q", "--allow-empty", "-m", "only tagged")
	git(tagged, "tag", "v1")
	git(tagged, "reset", "-q", "--hard", "HEAD~1")
	stashed := checkout("stashed", "git@github-x:o/stashed.git", true)
	write(t, filepath.Join(stashed, "f.txt"), "stash me\n")
	git(stashed, "stash", "-q")
	os.MkdirAll(filepath.Join(checkout("withsub", "git@github-x:o/withsub.git", true), ".git", "modules", "sub"), 0o755)
	checkout("cased", "https://github.com/o/Cased.git", true)
	checkout("elsewhere", "https://gitlab.com/o/elsewhere.git", true)
	checkout("local", filepath.Join(root, "srv", "o", "local.git"), true)
	write(t, filepath.Join(work, "notes", "x.md"), "mine\n")
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\ncommit: false\nsync_repos: false\nclaude_bin: "+fakeClaude(t, root, "ls > seen.txt")+"\n")
	write(t, filepath.Join(root, "agents", "dev.md"), "---\nenabled: true\nrepos: o/app\n---\ndev\n")
	if err := runShift(t, root, "dev", today("09:00"), true); err != nil {
		t.Fatal(err)
	}
	for name, there := range map[string]bool{"old": false, "Gone": false, "cased": false, "moved": true, "linked": true, "wt-linked": true, "untracked": true,
		"detached": true, "tagged": true, "stashed": true, "withsub": true, "elsewhere": true, "local": true, "app": true, "dirty": true, "unpushed": true, "lib": true, "old-copy": true, "notes": true} {
		if _, err := os.Stat(filepath.Join(work, name)); (err == nil) != there {
			t.Errorf("%s: there %v, want %v", name, err == nil, there)
		}
	}
	if seen, err := os.ReadFile(filepath.Join(work, "seen.txt")); err != nil || !strings.Contains(string(seen), "app\n") || strings.Contains(string(seen), "old\n") {
		t.Fatalf("the shift ran and never saw the removed checkout: %v\n%s", err, seen)
	}
	j := readAll(filepath.Join(root, "journal", "*.md"))
	for _, want := range []string{"Removed its checkouts of repositories it no longer has: Gone (o/Gone); cased (o/Cased); old (o/old).",
		"detached (o/detached), which holds commits on no remote", "dirty (o/dirty), which holds uncommitted changes", "linked (o/linked), which holds other worktrees",
		"moved (o/moved), which holds other worktrees", "stashed (o/stashed), which holds a stash", "tagged (o/tagged), which holds commits on no remote",
		"untracked (o/untracked), which holds uncommitted changes", "unpushed (o/unpushed), which holds commits on no remote", "withsub (o/withsub), which holds submodules",
		". Delete it by hand"} {
		if !strings.Contains(j, want) {
			t.Fatalf("the journal says %q:\n%s", want, j)
		}
	}
	job, _, _, _ := open(t, root).brief(open(t, root).Agent("dev"), open(t, root).schedule(time.Now(), nil, nil))
	if !strings.Contains(job.Prompt, "Work only on the repositories checked out for you, listed above.") {
		t.Fatal("the brief says to work only on the agent's repositories")
	}
}

// After a shift its work folder is measured: the next brief says what it held against the
// agent's disk cap, and a folder over the cap is said in the journal and gets the agent a note
// to free space before anything else.
func TestAWorkFolderOverItsDiskCapIsFlagged(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\ncommit: false\nsync_repos: false\nclaude_bin: "+
		fakeClaude(t, root, "head -c 3000000 /dev/zero > build.bin")+"\n")
	write(t, filepath.Join(root, "agents", "dev.md"), "---\nenabled: true\ndisk_gb: 100\n---\ndev\n")
	if err := runShift(t, root, "dev", today("09:00"), true); err != nil {
		t.Fatal(err)
	}
	team := open(t, root)
	if d, ok := team.DiskOf("dev"); !ok || d.Bytes < 3000000 || d.Bytes > 4000000 {
		t.Fatalf("du measured the 3 MB the shift left: %+v %v", d, ok)
	}
	if strings.Contains(inboxText(team, "dev"), "over your cap") {
		t.Fatal("a folder under the cap gets no note")
	}
	dir := t.TempDir() // a du that finds 153.6 GB
	write(t, filepath.Join(dir, "du"), "#!/bin/sh\nprintf '150000000\\t.\\n'\n")
	os.Chmod(filepath.Join(dir, "du"), 0o755)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	if err := runShift(t, root, "dev", today("10:00"), true); err != nil {
		t.Fatal(err)
	}
	team = open(t, root)
	if j := readAll(filepath.Join(root, "journal", "*.md")); !strings.Contains(j, "Over its disk cap: its work folder holds 153.6 GB of 100 GB.") {
		t.Fatalf("the journal says so:\n%s", j)
	}
	if !strings.Contains(inboxText(team, "dev"), "held 153.6 GB when your last shift ended, over your cap of 100 GB. Before anything else, free space") {
		t.Fatalf("the agent's next shift is told to free space first:\n%s", inboxText(team, "dev"))
	}
	job, _, _, err := team.brief(team.Agent("dev"), team.schedule(time.Now(), nil, nil))
	if err != nil || !strings.Contains(job.Prompt, "Disk: your work folder held 153.6 GB when your last shift ended (") || !strings.Contains(job.Prompt, "your cap is 100 GB. Over the cap") {
		t.Fatalf("the brief says what the folder held and the cap:\n%v %s", err, job.Prompt)
	}
	st := team.Status(time.Now())
	if a := st.Agents[0]; a.DiskText() != "153.6 / 100 GB" || !a.OverCap() {
		t.Fatalf("the status shows it over the cap: %q %v", a.DiskText(), a.OverCap())
	}
	write(t, filepath.Join(root, "agents", "dev.md"), "---\nenabled: true\ndisk_gb: 0\n---\ndev\n")
	team = open(t, root)
	if a := team.Status(time.Now()).Agents[0]; a.DiskText() != "153.6 GB" || a.OverCap() || !strings.HasSuffix(team.diskText(team.Agent("dev")), "; you have no cap") {
		t.Fatalf("a cap of 0 is none: %q %v %q", a.DiskText(), a.OverCap(), team.diskText(team.Agent("dev")))
	}
}

// The work folder is measured once claude ends, as du counts it, an empty one too; a shift that
// is being stopped is not measured, nor is one whose du prints no size, and a du that also
// reports files it could not read still gives what it counted.
func TestTheWorkFolderIsMeasuredWhenClaudeEnds(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\nsync_repos: false\nclaude_bin: "+fakeClaude(t, root, `[ -f "$PWD/../../slow" ] && sleep 30`)+"\n")
	write(t, filepath.Join(root, "agents", "dev.md"), "---\nenabled: true\n---\ndev\n")
	c := open(t, root).Config
	job := Job{ID: "j1", Agent: "dev", Prompt: "go", RunMinutes: 5, WorkDir: "work/dev", EnvFile: "secrets/dev.env"}
	if res := execShift(context.Background(), c, job, nil, nil); res.Status != "done" || res.Disk == nil || *res.Disk > 64*1024 {
		t.Fatalf("an empty folder is measured too, at what its directory takes: %s %v", res.Status, res.Disk)
	}
	du := func(script string) {
		dir := t.TempDir()
		write(t, filepath.Join(dir, "du"), "#!/bin/sh\n"+script+"\n")
		os.Chmod(filepath.Join(dir, "du"), 0o755)
		t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	}
	du(`printf '42\t.\n'; echo "du: ./secret: Permission denied" >&2; exit 1`)
	if res := execShift(context.Background(), c, job, nil, nil); res.Disk == nil || *res.Disk != 42*1024 {
		t.Fatalf("what du counted stands, though it could not read everything: %v", res.Disk)
	}
	du(`echo "du: .: No such file" >&2; exit 1`)
	if res := execShift(context.Background(), c, job, nil, nil); res.Status != "done" || res.Disk != nil {
		t.Fatalf("a du that prints no size measures nothing, and the shift is still done: %s %v", res.Status, res.Disk)
	}
	write(t, filepath.Join(root, "slow"), "")
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	if res := execShift(ctx, c, job, nil, nil); res.Status != "stopped" || res.Disk != nil {
		t.Fatalf("a shift being stopped does not wait for du: %s %v", res.Status, res.Disk)
	}
	team := open(t, root)
	if err := team.keepDisk("dev", Disk{Bytes: 5e9, At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	r := Run{Time: time.Now(), Agent: "dev", Status: "ok"}
	if err := team.record(context.Background(), team.Agent("dev"), r, Result{Status: "done", Started: true}, "done", nil, nil, map[string]string{}, time.Now(), resumable{}); err != nil {
		t.Fatal(err)
	}
	if d, ok := open(t, root).DiskOf("dev"); !ok || d.Bytes != 5e9 {
		t.Fatalf("a shift that was not measured keeps the last measurement: %+v %v", d, ok)
	}
	var a Agent
	f, _ := lookup(AgentFields, "disk_gb")
	if f.Default != "100" || f.Kind != "number" || f.Set(&a, "-1") == nil || f.Set(&a, "0") != nil || f.Set(&a, "250") != nil || a.DiskGB != 250 {
		t.Fatalf("disk_gb is a number of GB, 100 by default, 0 or more: %+v %v", f, a.DiskGB)
	}
}

// rota delete-branch deletes only the agent's own branch, once every pull request from it is
// merged or closed, its tip is the head of one of them and no open pull request is based on it.
func TestAnAgentDeletesOnlyItsOwnFinishedBranch(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\n")
	write(t, filepath.Join(root, "agents", "dev.md"), "---\nenabled: true\nrepos: o/app\n---\ndev\n")
	gh := t.TempDir()
	fakeGh(t, `d=`+gh+`; echo "$*" >> $d/log
case "$*" in
  "pr list -R o/app --head "*"--state open"*) b=${6//\//_}; cat $d/open-$b.json 2>/dev/null || echo '[]';;
  "pr list -R o/app --head "*) b=${6//\//_}; cat $d/head-$b.json 2>/dev/null || echo '[]';;
  "pr list -R o/app --base "*) b=${6//\//_}; cat $d/base-$b.json 2>/dev/null || echo '[]';;
  "api -X DELETE "*) ;;
  "api "*) f=$d/tip-$(echo "$2" | tr / _).json; [ -f $f ] && cat $f || { echo 'Not Found (HTTP 404)' >&2; exit 1; };;
esac`)
	set := func(name, v string) { write(t, filepath.Join(gh, name), v) }
	team := open(t, root)
	del := func(agent, branch string) (string, error) {
		return team.DeleteBranch(context.Background(), agent, "app", branch)
	}
	if _, err := del("dev", "maintainer/x"); err == nil || !strings.Contains(err.Error(), "not yours") {
		t.Fatalf("another agent's branch is refused: %v", err)
	}
	if _, err := del("dev", "dev/1-fix"); err == nil || !strings.Contains(err.Error(), "no pull request was opened from dev/1-fix") {
		t.Fatalf("a branch without a pull request is kept: %v", err)
	}
	set("head-dev_1-fix.json", `[{"number":7,"state":"CLOSED","headRefOid":"aaa","isCrossRepository":true}]`)
	if _, err := del("dev", "dev/1-fix"); err == nil || !strings.Contains(err.Error(), "no pull request was opened from dev/1-fix") {
		t.Fatalf("a fork's pull request from a branch of the same name does not count: %v", err)
	}
	set("open-dev_1-fix.json", `[{"number":9,"state":"OPEN"}]`)
	set("head-dev_1-fix.json", `[{"number":7,"state":"CLOSED","headRefOid":"aaa"}]`)
	if _, err := del("dev", "dev/1-fix"); err == nil || !strings.Contains(err.Error(), "o/app#9, from dev/1-fix, is still open") {
		t.Fatalf("a branch with an open pull request is kept, though the history leaves it out: %v", err)
	}
	set("open-dev_1-fix.json", `[{"number":12,"state":"OPEN","isCrossRepository":true}]`)
	set("head-dev_1-fix.json", `[{"number":7,"state":"CLOSED","headRefOid":"aaa"},{"number":9,"state":"MERGED","headRefOid":"bbb"},{"number":12,"state":"OPEN","headRefOid":"fff","isCrossRepository":true}]`)
	set("base-dev_1-fix.json", `[{"number":11}]`)
	if _, err := del("dev", "dev/1-fix"); err == nil || !strings.Contains(err.Error(), "o/app#11 is based on dev/1-fix") {
		t.Fatalf("a branch another pull request is based on is kept: %v", err)
	}
	set("base-dev_1-fix.json", `[]`)
	set("tip-repos_o_app_git_refs_heads_dev_1-fix.json", `{"object":{"sha":"ccc"}}`)
	if _, err := del("dev", "dev/1-fix"); err == nil || !strings.Contains(err.Error(), "holds work pushed since") {
		t.Fatalf("a branch with commits after its pull requests is kept: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(gh, "log")); strings.Contains(string(b), "DELETE") {
		t.Fatalf("nothing was deleted so far:\n%s", b)
	}
	set("tip-repos_o_app_git_refs_heads_dev_1-fix.json", `{"object":{"sha":"bbb"}}`)
	msg, err := del("dev", "dev/1-fix")
	if err != nil || !strings.Contains(msg, "deleted dev/1-fix in o/app (#7 closed, #9 merged)") {
		t.Fatalf("a finished branch is deleted: %q %v", msg, err)
	}
	if b, _ := os.ReadFile(filepath.Join(gh, "log")); !strings.Contains(string(b), "api -X DELETE repos/o/app/git/refs/heads/dev/1-fix\n") {
		t.Fatalf("through GitHub's API:\n%s", b)
	}
}

// An agent with after: developer runs right after each of the developer's shifts, and its own
// shifts do not wake the developer back: the two cannot take turns forever.
func TestAnAgentRunsAfterAnotherWithoutPingPong(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\n")
	write(t, filepath.Join(root, "agents", "developer.md"), "---\nenabled: true\nuntil_done: true\non: note\n---\ndev\n")
	write(t, filepath.Join(root, "agents", "triage.md"), "---\nenabled: true\nafter: developer\nevery: 1d\n---\ntriage\n")
	write(t, filepath.Join(root, "agents", "scout.md"), "---\nenabled: true\nevery: 1d\nafter: scout, pair\n---\nscout\n")
	write(t, filepath.Join(root, "agents", "pair.md"), "---\nenabled: true\nevery: 1d\nafter: scout\n---\npair\n")
	team := open(t, root)
	ran := func(name, text string) {
		team.answered(name)
		r := Run{Time: time.Now(), Agent: name, Status: "ok"}
		team.Runs = append(team.Runs, r)
		team.wake(team.Agent(name), r, text)
	}
	ran("developer", "More: no")
	if !team.Asked()["triage"] || team.Asked()["developer"] {
		t.Fatalf("the developer's shift asks for the triage, and not for itself: %v", team.Asked())
	}
	ran("triage", "merged #70")
	if team.Asked()["developer"] {
		t.Fatal("the triage does not wake the developer it runs after")
	}
	team.routeNotes("triage", "## Notes to teammates\n@developer: backend#72 is not ready: no independent review\n", time.Now())
	if !team.Asked()["developer"] || !strings.Contains(inboxText(team, "developer"), "backend#72 is not ready") {
		t.Fatal("a note to the developer is work for it, and wakes it")
	}
	team.answered("developer")
	team.routeNotes("triage", "@all: labels created\n", time.Now())
	if team.Asked()["developer"] {
		t.Fatal("a note to everyone wakes no one")
	}
	write(t, filepath.Join(root, "agents", "fixer.md"), "---\nenabled: true\nuntil_done: true\non: note\n---\nfixer\n")
	team = open(t, root)
	team.routeNotes("fixer", "@developer: over to you\n", time.Now())
	if team.Asked()["developer"] {
		t.Fatal("the note of an agent that starts on notes does not wake another that does: they would take turns forever")
	}
	bashShift(t, root, "scout", "cd backend &&\n  gh issue create --label rota --title Leak --body-file b.md", "Creating issue in o/backend\n\nhttps://github.com/o/backend/issues/71")
	ran("scout", "## Summary\nfiled #71")
	if team.Asked()["developer"] || team.Asked()["triage"] {
		t.Fatalf("a scout's shift wakes neither by itself: the issue it opened does, on GitHub, as a trigger: %v", team.Asked())
	}
	if team.Asked()["scout"] || team.Asked()["pair"] {
		t.Fatalf("an agent after itself, or two after each other, do not wake each other: %v", team.Asked())
	}
}

// Evidence is read only from evidence/: no path, link or symlink leads out of it.
func TestEvidenceStaysInItsFolder(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\n")
	write(t, filepath.Join(root, "evidence", "app", "developer", "1-fix", "after.png"), "png")
	write(t, filepath.Join(root, "secrets", "dev.env"), "TOKEN=x")
	os.Symlink(filepath.Join(root, "secrets"), filepath.Join(root, "evidence", "leak"))
	team := open(t, root)
	if team.Evidence("app/developer/1-fix/after.png") == "" {
		t.Fatal("a file in evidence/ is found")
	}
	for _, bad := range []string{"../secrets/dev.env", "app/../../secrets/dev.env", "leak/dev.env", "/etc/passwd"} {
		if p := team.Evidence(bad); p != "" {
			t.Errorf("%q must not lead to %s", bad, p)
		}
	}
	os.RemoveAll(filepath.Join(root, "evidence"))
	os.Symlink(filepath.Join(root, "secrets"), filepath.Join(root, "evidence"))
	if team.Evidence("") != "" || team.Evidence("dev.env") != "" {
		t.Fatal("evidence/ replaced by a link leads nowhere")
	}
}

// A role links to the pages with {pages}, filled in like the brief's placeholders; the role
// on disk keeps it.
func TestARoleLinksToThePages(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\ncommit: false\nsync_repos: false\nweb: off\nweb_port: 7999\n")
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: true\n---\nlink it as {pages}/evidence/x.png\n")
	team := open(t, root)
	job, _, _, err := team.brief(team.Agent("alpha"), team.schedule(time.Now(), nil, nil))
	if err != nil || !strings.Contains(strings.Join(job.Args, " "), "link it as http://127.0.0.1:7999/evidence/x.png") || !strings.Contains(team.Agent("alpha").Role, "{pages}") {
		t.Fatalf("the role links to the pages: %v %q", err, job.Args)
	}
}

// A note the human leaves while the agent's shift runs reaches that shift: claude echoes it
// when it takes it, and then it is read, quoted in the journal, gone from the inbox, and no
// reason for another shift. One the shift never takes waits in the inbox and wakes the agent.
func TestANoteReachesTheRunningShift(t *testing.T) {
	defer func(d time.Duration) { steerEvery = d }(steerEvery)
	steerEvery = 50 * time.Millisecond
	for _, takes := range []bool{true, false} {
		root := t.TempDir()
		take := "sleep 1" // a shift that never reads another message
		if takes {
			take = `read -r note; printf '%s\n' "$note"` // echoes it, as claude does once the message enters the conversation
		}
		claude := filepath.Join(root, "claude")
		write(t, claude, "#!/bin/bash\nread -r prompt\ntouch "+root+"/started\n"+take+"\n"+
			`echo '{"type":"result","subtype":"success","is_error":false,"result":"## Summary\ndone","num_turns":2,"session_id":"s1","usage":{}}'`+"\n")
		os.Chmod(claude, 0o755)
		write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\ncommit: false\nsync_repos: false\ntriage_model: off\nreport_model: off\nclaude_bin: "+claude+"\n")
		write(t, filepath.Join(root, "agents", "dev.md"), "---\nenabled: true\nuntil_done: true\nrepos: o/backend\n---\ndev\n")
		putNotes(t, root, "dev", "qa: read at the start")
		team := open(t, root)
		done := make(chan error)
		go func() { done <- team.RunShift(context.Background(), "dev", time.Now(), true, false) }()
		waitFor(t, "the shift", func() bool { _, err := os.Stat(filepath.Join(root, "started")); return err == nil })
		if !team.Steerable("dev") {
			t.Fatal("a running shift takes notes")
		}
		if err := team.LeaveNote("dev", "o/backend#7", "human", "change of plan"); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		inbox, journal := inboxText(team, "dev"), readFile(filepath.Join(root, "journal", time.Now().Format("2006-01-02")+".md"))
		if team.Steerable("dev") {
			t.Fatal("an ended shift takes no notes")
		}
		if takes && (inbox != "" || !strings.Contains(journal, "> Read while it ran:\n> ## o/backend#7\n> - ") || !strings.Contains(journal, "from the human: change of plan") || team.Asked()["dev"]) {
			t.Fatalf("a note the shift took is read and wakes nobody: asked %v\ninbox:\n%s\njournal:\n%s", team.Asked()["dev"], inbox, journal)
		}
		if !takes && (!strings.Contains(inbox, "## o/backend#7\n- ") || strings.Contains(inbox, "read at the start") || strings.Contains(journal, "Read while it ran") || !team.Asked()["dev"]) {
			t.Fatalf("a note the shift never took waits, and wakes the agent: asked %v\ninbox:\n%s\njournal:\n%s", team.Asked()["dev"], inbox, journal)
		}
	}
}

// The human addresses notes the way agents do: an agent, or one of the open pull requests
// and issues it has notes about or named in its journal, newest first, as agents name them.
func TestAddressesAreWhatTheAgentWorksOn(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\n")
	write(t, filepath.Join(root, "agents", "dev.md"), "---\nenabled: true\nrepos: o/backend, o/creator, p/creator\n---\ndev\n")
	write(t, filepath.Join(root, "agents", "qa.md"), "---\nenabled: true\nrepos: o/backend\n---\nqa\n")
	day := func(n int) string { return time.Now().AddDate(0, 0, -n).Format("2006-01-02") }
	write(t, filepath.Join(root, "journal", day(1)+".md"), "# x\n\n## 09:00 dev on local | 1% | 3 turns | 2 min | ok | log x\n\nOpened https://github.com/o/backend/pull/5 for backend#4.\n\n## 10:00 qa on local | 1% | 3 turns | 2 min | ok | log x\n\nbackend#9 fails\n")
	write(t, filepath.Join(root, "journal", day(0)+".md"), "# x\n\n## 08:00 dev on local | 1% | 3 turns | 2 min | ok | log x\n\n> Inbox read this shift:\n> ## o/backend#3\n\nWorked on p/creator#8, gone#2 and backend#6.\n")
	write(t, filepath.Join(root, "journal", day(20)+".md"), "# x\n\n## 08:00 dev on local | 1% | 3 turns | 2 min | ok | log x\n\nbackend#1\n")
	putNotes(t, root, "dev", "qa: general", "o/creator#2 qa: about it")
	team := open(t, root)
	b, _ := json.Marshal(openList{Time: time.Now(), Repos: map[string]map[int]openItem{
		"o/backend": {1: {Title: "old"}, 3: {Title: "three"}, 4: {Title: "four"}, 5: {Title: "five", PR: true}, 9: {Title: "nine"}},
		"o/creator": {2: {Title: "two", PR: true}}, "p/creator": {8: {Title: "eight"}}}})
	write(t, filepath.Join(root, openFile), string(b))
	var got []string
	for _, a := range team.Addresses("dev") {
		got = append(got, a.Label())
	}
	want := []string{"@dev", "@dev o/creator#2 · two", "@dev backend#3 · three", "@dev p/creator#8 · eight", "@dev backend#5 · five", "@dev backend#4 · four"}
	if !slices.Equal(got, want) {
		t.Fatalf("waiting notes first, then the open ones it named, newest first; closed, unknown, old and a teammate's left out:\n%q\nwant:\n%q", got, want)
	}
	if err := team.LeaveNote("dev", "o/backend#5", "human", "rebase it"); err != nil || !strings.Contains(inboxText(team, "dev"), "## o/backend#5\n- "+time.Now().Format("2006-01-02")+" from the human: rebase it\n") {
		t.Fatalf("a note about a pull request is filed under it: %v\n%s", err, inboxText(team, "dev"))
	}
	for _, bad := range []string{"x/y#1", "o/backend", "backend#5"} {
		if team.LeaveNote("dev", bad, "human", "no") == nil {
			t.Errorf("%s is not a pull request or issue of dev's repositories", bad)
		}
	}
	os.Remove(filepath.Join(root, openFile))
	fakeGh(t, "exit 1")      // GitHub cannot be asked: only those with notes waiting
	for i := 0; i < 2; i++ { // before it was asked, and after it failed
		if got := team.Addresses("dev"); len(got) != 3 || got[1].String() != "@dev o/creator#2" || got[2].String() != "@dev backend#5" {
			t.Fatalf("without GitHub, the pull requests and issues with notes waiting: %+v", got)
		}
		waitFor(t, "GitHub asked", func() bool { _, err := os.Stat(filepath.Join(root, openFile)); return err == nil })
	}
}

// An agent's reports folder, or reports/, that is a link leads elsewhere: nothing in it is listed.
func TestAgentReportsFollowNoLink(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\n")
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: true\n---\nalpha\n")
	write(t, filepath.Join(root, "secrets", "keys.md"), "secret")
	os.MkdirAll(filepath.Join(root, "reports"), 0o755)
	os.Symlink(filepath.Join(root, "secrets"), filepath.Join(root, "reports", "alpha"))
	if rs := open(t, root).AgentReports("alpha"); len(rs) != 0 {
		t.Fatalf("nothing through a link: %v", rs)
	}
	os.RemoveAll(filepath.Join(root, "reports"))
	write(t, filepath.Join(root, "work", "alpha", "credentials.md"), "secret")
	os.Symlink(filepath.Join(root, "work"), filepath.Join(root, "reports"))
	if team := open(t, root); len(team.AgentReports("alpha")) != 0 || team.AgentReport("alpha", "credentials.md") != "" {
		t.Fatal("nothing through a link put in place of reports/")
	}
}

// When the human sends review notes, the agent that wrote the pull request (its branch, or else
// its rota:<agent> label) finds a note about it in its inbox, which wakes it.
func TestASentReviewReachesTheAgentThatWroteThePullRequest(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\n")
	write(t, filepath.Join(root, "agents", "dev.md"), "---\nenabled: true\nrepos: o/app\n---\ndev\n")
	write(t, filepath.Join(root, "agents", "qa.md"), "---\nenabled: true\nrepos: o/app\n---\nqa\n")
	fakeGh(t, `case "$*" in
*"view 7 "*) echo '{"headRefName":"dev/12-login","labels":[{"name":"rota:not-ready"}]}' ;;
*"view 8 "*) echo '{"headRefName":"fix-typo","labels":[{"name":"rota:qa"}]}' ;;
*) echo '{"headRefName":"someone/else","labels":[]}' ;;
esac`)
	team := open(t, root)
	if err := team.ReviewSent(context.Background(), "o/app", 7, 3, false); err != nil {
		t.Fatal(err)
	}
	if err := team.ReviewSent(context.Background(), "o/app", 8, 1, false); err != nil {
		t.Fatal(err)
	}
	if got := inboxText(team, "dev"); !strings.Contains(got, "## o/app#7\n") || !strings.Contains(got, "from the human: I sent 3 review notes") || !strings.Contains(got, "gloss notes o/app#7 --open") {
		t.Fatalf("the branch names the author:\n%s", got)
	}
	if got := inboxText(team, "qa"); !strings.Contains(got, "## o/app#8\n") || !strings.Contains(got, "I sent 1 review note on") {
		t.Fatalf("else the label names it:\n%s", got)
	}
	if err := team.ReviewSent(context.Background(), "o/app", 9, 1, false); err == nil || !strings.Contains(err.Error(), "no agent of the team wrote o/app#9") {
		t.Fatalf("a pull request no agent wrote is said so: %v", err)
	}
	if err := team.ReviewSent(context.Background(), "o/app", 7, 0, true); err == nil || !strings.Contains(err.Error(), "no agent of the team merges in o/app: merge o/app#7 yourself") {
		t.Fatalf("an approval no agent can merge is said so: %v", err)
	}
}

// When the human approves a pull request, it gets rota:approved, and every agent whose role merges
// (its tools allow rota merge) is told to merge it once it is ready: a draft stays one, as an agent
// works on it. The author hears of it only with notes.
func TestAnApprovalReachesTheAgentsThatMerge(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\n")
	write(t, filepath.Join(root, "agents", "dev.md"), "---\nenabled: true\nrepos: o/app\n---\ndev\n")
	write(t, filepath.Join(root, "agents", "tri.md"), "---\nenabled: true\nrepos: o/app\ntools: Read,Bash(rota merge *)\n---\ntri\n")
	write(t, filepath.Join(root, "agents", "off.md"), "---\nenabled: false\nrepos: o/app\ntools: Bash(rota merge *)\n---\noff\n")
	write(t, filepath.Join(root, "agents", "far.md"), "---\nenabled: true\nrepos: o/web\ntools: Bash(rota merge *)\n---\nfar\n")
	log := filepath.Join(t.TempDir(), "gh.log")
	fakeGh(t, `echo "$*" >> `+log+`; echo '{"headRefName":"dev/12-login","labels":[]}'`)
	team := open(t, root)
	if err := team.ReviewSent(context.Background(), "o/app", 8, 1, false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(log); strings.Contains(string(b), "ready") || strings.Contains(string(b), "--add-label") {
		t.Fatalf("notes alone leave a draft and its labels as they are:\n%s", b)
	}
	if err := team.ReviewSent(context.Background(), "o/app", 7, 0, true); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(log); !strings.Contains(string(b), "pr edit 7 -R o/app --add-label rota:approved\n") || strings.Contains(string(b), "pr ready") {
		t.Fatalf("an approval labels it, and leaves a draft one, as an agent works on it:\n%s", b)
	}
	if got := inboxText(team, "tri"); !strings.Contains(got, "## o/app#7\n") || !strings.Contains(got, "I approved this pull request: it carries rota:approved") || !strings.Contains(got, `"rota merge o/app#7 <its head commit>"`) || !strings.Contains(got, "while it is a draft, for the agent working on it to mark it ready") {
		t.Fatalf("the agent that merges is told to merge it:\n%s", got)
	}
	if strings.Contains(inboxText(team, "dev"), "## o/app#7") || inboxText(team, "off") != "" || inboxText(team, "far") != "" {
		t.Fatal("no one else: not the author without notes, a disabled agent, nor one of other repositories")
	}
	if err := team.ReviewSent(context.Background(), "o/app", 7, 2, true); err != nil {
		t.Fatal(err)
	}
	if got := inboxText(team, "dev"); !strings.Contains(got, "I sent 2 review notes on this pull request and approved it.") || !strings.Contains(got, "the team may merge the pull request as it is") {
		t.Fatalf("with notes, the author hears of both:\n%s", got)
	}
	fakeGh(t, `case "$*" in *--add-label*) echo "label not found" >&2; exit 1 ;; *) echo '{"headRefName":"dev/12-login","labels":[]}' ;; esac`)
	if err := team.ReviewSent(context.Background(), "o/app", 6, 0, true); err == nil || !strings.Contains(err.Error(), "o/app#6 did not get rota:approved, which is your approval: put it on by hand on GitHub") {
		t.Fatalf("an approval GitHub does not show is said so: %v", err)
	}
	if strings.Contains(inboxText(team, "tri"), "## o/app#6") {
		t.Fatal("and no one is told to merge what does not carry the label")
	}
}

// When another rota is installed where the organizer was started from, the organizer runs it in
// its place between shifts, once it has stayed as it is for a moment; one that would not start
// is not tried again.
func TestTheOrganizerRestartsWithANewlyInstalledRota(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "rota")
	write(t, exe, "old")
	os.Chmod(exe, 0o755)
	prevSettle, prevGiveUp, prevRestart := selfSettle, selfGiveUp, restart
	t.Cleanup(func() { selfSettle, selfGiveUp, restart = prevSettle, prevGiveUp, prevRestart })
	selfSettle = 20 * time.Millisecond
	var ran []string
	restart = func(path string) error { ran = append(ran, path); return errors.New("exec format error") }
	w := watchSelf(exe)
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\n")
	team := open(t, root)
	o := &organizer{Config: team.Config, monitor: monitor{Config: team.Config, started: time.Now(), lastShift: map[string]time.Time{}}}
	if o.updateSelf(context.Background(), w) || len(ran) != 0 {
		t.Fatal("the rota it runs is not restarted")
	}
	if pending, _ := w.check(time.Now()); pending {
		t.Fatal("nothing pending while the file is the same")
	}
	os.Remove(exe) // installing removes the old one, then copies the new one in
	if pending, ready := w.check(time.Now()); !pending || ready {
		t.Fatal("a missing rota is being installed: pending, not ready")
	}
	write(t, exe, "new, a longer file")
	os.Chmod(exe, 0o755)
	o.updateSelf(context.Background(), w)
	if len(ran) != 1 || ran[0] != w.exe {
		t.Fatalf("the new one runs once it settles: %q", ran)
	}
	if o.updateSelf(context.Background(), w) || len(ran) != 1 {
		t.Fatalf("one that would not start is not tried again soon: %q", ran)
	}
	w.failedAt = w.failedAt.Add(-selfRetry)
	if o.updateSelf(context.Background(), w); len(ran) != 2 {
		t.Fatalf("but later, in case it failed for a moment: %q", ran)
	}
	write(t, exe, "newer still, and longer again")
	os.Chmod(exe, 0o755)
	o.updateSelf(context.Background(), w)
	if len(ran) != 3 {
		t.Fatalf("a newer one is tried: %q", ran)
	}
	os.Remove(exe) // an install that failed halfway: the organizer schedules again
	selfGiveUp = 50 * time.Millisecond
	start := time.Now()
	if o.updateSelf(context.Background(), w) || time.Since(start) > 2*time.Second || len(ran) != 3 {
		t.Fatalf("it gives up waiting for a missing rota: %v %q", time.Since(start), ran)
	}
	if w := watchSelf(exe); w == nil || w.started != (fileSig{}) {
		t.Fatal("a missing rota is watched too: what is copied there next is new")
	}
}

// The menu bar takes a newly installed rota as soon as it has settled, with what it hands over in
// the new one's environment; one that would not start is not tried again soon.
func TestTheMenuBarRestartsWithANewlyInstalledRota(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "rota")
	write(t, exe, "old")
	os.Chmod(exe, 0o755)
	prevSettle, prevRestart := selfSettle, restartWith
	t.Cleanup(func() { selfSettle, restartWith = prevSettle, prevRestart })
	selfSettle = 20 * time.Millisecond
	var ran []string
	var handed []string
	restartWith = func(path string, env []string) error {
		ran, handed = append(ran, path), env
		return errors.New("exec format error")
	}
	s := &SelfUpdate{watchSelf(exe)}
	if s.Ready() || (*SelfUpdate)(nil).Ready() {
		t.Fatal("the rota it runs is not new")
	}
	write(t, exe, "new, a longer file")
	os.Chmod(exe, 0o755)
	if s.Ready() {
		t.Fatal("just copied in: not settled yet")
	}
	time.Sleep(30 * time.Millisecond)
	if !s.Ready() {
		t.Fatal("settled: ready")
	}
	if err := s.Restart("ROTA_TRAY_ORGANIZER=42"); err == nil || len(ran) != 1 || ran[0] != s.w.exe || !slices.Contains(handed, "ROTA_TRAY_ORGANIZER=42") {
		t.Fatalf("the new one runs with what is handed over: %q %q %v", ran, handed, err)
	}
	if s.Ready() {
		t.Fatal("one that would not start is not tried again soon")
	}
}

// The review queue lists every pull request after the open ones it waits for, stacked on or
// "Depends on", whatever the agent's order says; one that waits for one is indented under it,
// one that waits for more stands after them all, and a loop is broken where it closes.
func TestReviewQueueKeepsWhatWaitsAfterWhatItWaitsFor(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\n")
	write(t, filepath.Join(root, "agents", "triage.md"), "---\nenabled: true\nrepos: o/app, o/api\n---\ntriage\n")
	team := open(t, root)
	b, _ := json.Marshal(openList{Time: time.Now(), Repos: map[string]map[int]openItem{
		"o/app": {
			1: {Title: "base", PR: true, Head: "dev/1", Base: "main"},
			2: {Title: "on 1", PR: true, Head: "dev/2", Base: "dev/1"},
			3: {Title: "on 2", PR: true, Head: "dev/3", Base: "dev/2"},
			5: {Title: "needs 2 and api 4", PR: true, Head: "dev/5", Base: "main", Deps: []string{"o/app#2", "o/api#4", "o/app#5"}},
			6: {Title: "needs an issue", PR: true, Head: "dev/6", Base: "main", Deps: []string{"o/app#9"}},
			7: {Title: "needs 8", PR: true, Head: "dev/7", Base: "main", Deps: []string{"o/app#8"}},
			8: {Title: "needs 7", PR: true, Head: "dev/8", Base: "main", Deps: []string{"o/app#7"}},
			9: {Title: "an issue"},
		},
		"o/api": {
			4:  {Title: "api", PR: true, Head: "dev/4", Base: "main"},
			10: {Title: "needs a merged one", PR: true, Head: "dev/10", Base: "main", Deps: []string{"o/api#11"}},
		}}})
	write(t, filepath.Join(root, openFile), string(b))
	line := func(q ReviewQueue) string {
		var out []string
		for _, pr := range q.PRs {
			s := strings.Repeat("  ", pr.Depth) + pr.Name
			if len(pr.Waits) > 0 {
				s += " after " + strings.Join(pr.Waits, ",")
			}
			if pr.Why != "" {
				s += ": " + pr.Why
			}
			out = append(out, s)
		}
		return strings.Join(out, "\n")
	}
	want := `api#4
api#10
app#1
  app#2 after app#1
    app#3 after app#2
app#5 after app#2,api#4
app#6
app#8
  app#7 after app#8`
	if got := line(team.ReviewQueue()); got != want {
		t.Fatalf("with no order, by repository and oldest first:\n%s\nwant:\n%s", got, want)
	}
	if QueueText(team.ReviewQueue()) == "" || team.ReviewQueue().By != "" {
		t.Fatal("no agent ordered them")
	}
	for _, bad := range [][]string{{"#3 no repository"}, {"app#3"}, {"x/y#3 not the team's"}, {"app#3 one", "o/app#3 twice"}} {
		if _, err := team.SetReviewOrder("triage", bad); err == nil {
			t.Errorf("%q is refused", bad)
		}
	}
	unknown, err := team.SetReviewOrder("triage", []string{"app#3 The new sign-in: check the flow", "api#4 Changes the API the app reads", "app#6 Small copy fix", "app#9 an issue"})
	if err != nil || !slices.Equal(unknown, []string{"app#9"}) {
		t.Fatalf("an issue is not an open pull request: %v %v", unknown, err)
	}
	want = `app#1
  app#2 after app#1
    app#3 after app#2: The new sign-in: check the flow
api#4: Changes the API the app reads
app#6: Small copy fix
api#10
app#5 after app#2,api#4
app#8
  app#7 after app#8`
	if q := team.ReviewQueue(); line(q) != want || q.By != "triage" || time.Since(q.Time) > time.Minute {
		t.Fatalf("in the agent's order, each after what it waits for, which is as urgent as it:\n%s\nwant:\n%s", line(q), want)
	}
	if _, err := team.SetReviewOrder("triage", nil); err != nil || strings.Contains(line(team.ReviewQueue()), ": ") || !strings.HasPrefix(line(team.ReviewQueue()), "api#4\n") {
		t.Fatalf("an empty order clears it: %v\n%s", err, line(team.ReviewQueue()))
	}
}

// The Reviews tab narrows to a repository and to a mark; narrowed, none is indented, and each still
// names what it waits for. The choices say how many each keeps.
func TestTheReviewQueueNarrows(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\nlabels: rota:needs-review 1D76DB 👀 Waits for the human\n")
	b, _ := json.Marshal(openList{Time: time.Now(), Repos: map[string]map[int]openItem{
		"o/app": {1: {Title: "base", PR: true, Head: "dev/1", Base: "main", Labels: []string{"rota:needs-review"}},
			2: {Title: "stacked", PR: true, Head: "dev/2", Base: "dev/1", Labels: []string{"rota:approved"}}},
		"o/web": {3: {Title: "web", PR: true, Head: "dev/3", Base: "main", Labels: []string{"rota:needs-review"}}}}})
	write(t, filepath.Join(root, openFile), string(b))
	q := open(t, root).ReviewQueue()
	repos, marks := Filters(q.PRs)
	if fmt.Sprint(repos) != "[{o/app app 2} {o/web web 1}]" || fmt.Sprint(marks) != "[{👀 👀 rota:needs-review 2} {✅ ✅ rota:approved 1}]" {
		t.Fatalf("each repository and mark, with how many: %v %v", repos, marks)
	}
	if n := q.Only("", ""); len(n.PRs) != 3 || n.PRs[1].Depth != 1 {
		t.Fatalf("nothing narrows it: %+v", n.PRs)
	}
	if n := q.Only("O/App", ""); len(n.PRs) != 2 || n.PRs[1].Name != "app#2" || n.PRs[1].Depth != 0 || fmt.Sprint(n.PRs[1].Waits) != "[app#1]" {
		t.Fatalf("one repository, none indented, what it waits for still named: %+v", n.PRs)
	}
	if n := q.Only("", "👀"); len(n.PRs) != 2 || n.PRs[0].Name != "app#1" || n.PRs[1].Name != "web#3" {
		t.Fatalf("one mark: %+v", n.PRs)
	}
	if n := q.Only("o/web", "✅"); len(n.PRs) != 0 {
		t.Fatalf("both: %+v", n.PRs)
	}
}

// A pull request carries its own labels and those of the open issues it is for; the Reviews tab
// keeps those that carry every label chosen, and offers the others with how many that keeps.
func TestTheReviewQueueNarrowsByLabels(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\n")
	b, _ := json.Marshal(openList{Time: time.Now(), Repos: map[string]map[int]openItem{
		"o/app": {1: {Title: "one", PR: true, Issues: []string{"o/app#10", "o/api#20"}},
			2:  {Title: "two", PR: true, Labels: []string{"P1"}, Issues: []string{"o/app#11"}},
			3:  {Title: "three", PR: true, Issues: []string{"o/app#2", "o/app#12"}}, // #2 is a pull request; #12 is not open
			10: {Title: "ten", Labels: []string{"security", "P1"}},
			11: {Title: "eleven", Labels: []string{"p1", "ui"}}},
		"o/api": {20: {Title: "twenty", Labels: []string{"security", "backend"}}}}})
	write(t, filepath.Join(root, openFile), string(b))
	q := open(t, root).ReviewQueue()
	labels := map[string][]string{}
	for _, pr := range q.PRs {
		labels[pr.Name] = pr.Labels
	}
	if fmt.Sprint(labels) != "map[app#1:[security P1 backend] app#2:[P1 ui] app#3:[]]" {
		t.Fatalf("its own labels and its open issues', each once: %v", labels)
	}
	names := func(q ReviewQueue) string {
		var out []string
		for _, pr := range q.PRs {
			out = append(out, pr.Name)
		}
		return strings.Join(out, " ")
	}
	if got := names(q.Only("", "", "p1")); got != "app#1 app#2" {
		t.Fatalf("one label, in any case: %s", got)
	}
	if got := names(q.Only("", "", "P1", "security")); got != "app#1" {
		t.Fatalf("every label chosen: %s", got)
	}
	if got := names(q.Only("", "", "ui", "security")); got != "" {
		t.Fatalf("no pull request carries both: %s", got)
	}
	if got := fmt.Sprint(LabelFilters(q.PRs, nil)); got != "[{backend backend 1} {P1 P1 2} {security security 1} {ui ui 1}]" {
		t.Fatalf("each label once, by name, with how many carry it: %s", got)
	}
	if got := fmt.Sprint(LabelFilters(q.Only("", "", "p1").PRs, []string{"p1"})); got != "[{backend backend 1} {security security 1} {ui ui 1}]" {
		t.Fatalf("those besides the chosen, with how many choosing them too keeps: %s", got)
	}
}

// The queue marks where each pull request stands: the labels it carries that have an emoji, as
// GitHub last listed them. Approved is the step after waiting for review: a pull request with
// rota:approved does not show the review label too.
func TestReviewQueueMarksWhereEachPullRequestStands(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\nlabels: rota:needs-review 1D76DB 👀 Waits for the human; priority:high B60205 Fix first\nreview_label: rota:needs-review\n")
	write(t, filepath.Join(root, "agents", "dev.md"), "---\nenabled: true\nrepos: o/app\n---\ndev\n")
	b, _ := json.Marshal(openList{Time: time.Now(), Repos: map[string]map[int]openItem{"o/app": {
		1: {Title: "approved", PR: true, Labels: []string{"ROTA:needs-review", "rota:approved", "priority:high"}},
		2: {Title: "waits for review", PR: true, Labels: []string{"rota:needs-review"}},
		3: {Title: "approved by hand on github.com", PR: true, Labels: []string{"Rota:Approved"}},
		4: {Title: "left alone", PR: true, Labels: []string{"rota:leave-alone"}},
		5: {Title: "an issue"}}}})
	write(t, filepath.Join(root, openFile), string(b))
	team := open(t, root)
	var out []string
	for _, pr := range team.ReviewQueue().PRs {
		s := pr.Name
		for _, m := range pr.Marks {
			s += " " + m.Emoji
		}
		out = append(out, s)
	}
	if got, want := strings.Join(out, "\n"), "app#1 ✅\napp#2 👀\napp#3 ✅\napp#4 ✋"; got != want {
		t.Fatalf("its labels, approved in place of waiting for review:\n%s\nwant:\n%s", got, want)
	}
	if legend := Legend(team.ReviewQueue().PRs); len(legend) != 3 || legend[1].Text != "rota:needs-review: Waits for the human" {
		t.Fatalf("each mark once, with what it means: %q", legend)
	}
	team.Config.ReviewLabel = ""
	if marks := team.ReviewQueue().PRs[0].Marks; len(marks) != 2 {
		t.Fatalf("with no review label, every label shows: %q", marks)
	}
}

// A draft is still being worked on: it never asks for the human's review, whatever its labels, and
// the inbox says an agent works on it; approved, it merges once it is ready again. Ready for review,
// it asks for theirs as the review label says, or always when the team has none.
func TestADraftIsNotForTheHumansReview(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\nreview_label: rota:needs-review\n")
	write(t, filepath.Join(root, "agents", "dev.md"), "---\nenabled: true\nrepos: o/app\n---\ndev\n")
	b, _ := json.Marshal(openList{Time: time.Now(), Repos: map[string]map[int]openItem{"o/app": {
		1: {Title: "finished", PR: true, Head: "dev/1", Labels: []string{"rota:needs-review"}},
		2: {Title: "being changed", PR: true, Head: "dev/2", Draft: true, Labels: []string{"rota:needs-review"}},
		3: {Title: "approved, being changed", PR: true, Head: "dev/3", Draft: true, Labels: []string{"rota:needs-review", "rota:approved"}},
		4: {Title: "not judged yet", PR: true, Head: "dev/4", Draft: true}}}})
	write(t, filepath.Join(root, openFile), string(b))
	team := open(t, root)
	names := func() string {
		var out []string
		for _, p := range team.ReviewQueue().ForYou(team.ReviewLabel) {
			out = append(out, p.Name)
		}
		return strings.Join(out, " ")
	}
	if got := names(); got != "app#1" {
		t.Fatalf("for the human's review: %s", got)
	}
	threads := map[string]Thread{}
	for _, th := range team.Mail(time.Now()).Threads {
		threads[th.ID] = th
	}
	for id, want := range map[string][2]string{
		"pr:o/app#1": {"review", "Ready for your review."},
		"pr:o/app#2": {"open", "A draft: being worked on, or parked. It comes to your review once it is ready."},
		"pr:o/app#3": {"approved", "You approved it. It is a draft again, and merges once it is ready."},
	} {
		if th := threads[id]; th.PR == nil || th.PR.State != want[0] || len(th.Msgs) == 0 || th.Msgs[0].Text != want[1] {
			t.Fatalf("%s: %+v", id, th)
		}
	}
	team.Config.ReviewLabel = ""
	if got := names(); got != "app#1" {
		t.Fatalf("with no review label, every one that is ready, and no draft: %s", got)
	}
}

// A pull request's dependencies are read from its description as rota merge reads them, and
// kept with GitHub's list of what is open; so are the issues it is for, and every label.
func TestOpenPullRequestsKeepWhatTheyDependOn(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\n")
	write(t, filepath.Join(root, "agents", "dev.md"), "---\nenabled: true\nrepos: o/app, o/api\n---\ndev\n")
	fakeGh(t, `case "$*" in
*"pr list"*) echo '[{"number":2,"title":"two","headRefName":"dev/2","baseRefName":"dev/1","headRefOid":"abc","labels":[{"name":"x"}],"body":"Fixes #7\n\nDepends on #1 and api#4\n- depends on: other/repo#3"}]' ;;
*) echo '[{"number":7,"title":"seven","labels":[{"name":"P1"}]}]' ;;
esac`)
	items, err := open(t, root).listOpen("o/app")
	if err != nil {
		t.Fatal(err)
	}
	if pr := items[2]; !pr.PR || pr.Head != "dev/2" || pr.Base != "dev/1" || !slices.Equal(pr.Deps, []string{"o/app#1", "o/api#4"}) || pr.Commit != "abc" || !slices.Equal(pr.Labels, []string{"x"}) || items[7].PR {
		t.Fatalf("its branches and what it depends on in the team's repositories: %+v", items)
	}
	if !slices.Equal(items[2].Issues, []string{"o/app#7"}) || !slices.Equal(items[7].Labels, []string{"P1"}) {
		t.Fatalf("the issue its first line names, and the issue's labels: %+v", items)
	}
}

// The issues a pull request is for: those GitHub links to it, then those its title or the first
// line of its description names, in the team's repositories; nothing further down counts.
func TestAPullRequestIsForTheIssuesItNames(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\n")
	write(t, filepath.Join(root, "agents", "dev.md"), "---\nenabled: true\nrepos: o/app, o/api\n---\ndev\n")
	fakeGh(t, `case "$*" in
*"pr list"*) echo '[{"number":2,"title":"fix: it (#9 step 2)","body":"Part of api#4, see https://github.com/o/app/issues/5#issuecomment-1, [#7](https://github.com/o/api/issues/7) and https://github.com/o/app/pull/6\n\nAlso #8","closingIssuesReferences":[{"number":3,"repository":{"name":"api","owner":{"login":"o"}}},{"number":1,"repository":{"name":"repo","owner":{"login":"other"}}}]}]' ;;
*) echo '[]' ;;
esac`)
	items, err := open(t, root).listOpen("o/app")
	if err != nil {
		t.Fatal(err)
	}
	if got := items[2].Issues; !slices.Equal(got, []string{"o/api#3", "o/app#9", "o/api#4", "o/app#5", "o/api#7"}) {
		t.Fatalf("linked, then named in the title and the first line, in the team's repositories, a link by where it leads: %q", got)
	}
}

// An approval is acted on only once GitHub confirms it: the review in hand keeps one it loaded
// after notes sent without one, which take it back.
func TestAnApprovalIsWhatGitHubHasNow(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer srv.Close()
	c := review.NewClient("test")
	c.API = srv.URL
	rv := &review.Review{Ref: review.Ref{Owner: "o", Name: "app", Number: 7}, PR: "o/app#7", Head: "abc"}
	if got, err := Approved(context.Background(), c, rv); got || err != nil || calls != 0 {
		t.Fatalf("notes without an approval ask GitHub nothing: %v %v %d", got, err, calls)
	}
	rv.Approval = &review.Approval{By: "me"}
	if got, err := Approved(context.Background(), c, rv); got || err == nil || calls == 0 {
		t.Fatalf("an approval GitHub cannot confirm is not one: %v %v", got, err)
	}
}

// A rebalance gives each agent judged what one shift uses times the shifts it runs a week,
// with a quarter to spare, and the until-done agent the rest; too few shifts keep a share.
func TestARebalanceProposesSharesFromHowTheAgentsUsedThePool(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "team_pct: 60\npacing: weekly\ntimezone: UTC\n")
	agents := map[string]string{
		"dev":   "enabled: true\nuntil_done: true\nweight: 50",
		"busy":  "enabled: true\nevery: 1d\nweight: 10",
		"quiet": "enabled: true\nevery: 1d\nweight: 30",
		"new":   "enabled: true\nevery: 1d\nweight: 10",
		"off":   "enabled: false\nweight: 5",
	}
	for name, front := range agents {
		write(t, filepath.Join(root, "agents", name+".md"), "---\n"+front+"\n---\n"+name+"\n")
	}
	team := open(t, root)
	now := at("2026-11-02T12:00:00Z")
	shift := func(agent string, ago time.Duration, out int, pct float64) {
		appendRun(team.Config, Run{Time: now.Add(-ago), Agent: agent, Status: "ok", Out: out, HasPct: true, PctUsed: pct})
	}
	shift("dev", 20*24*time.Hour, 1, 50) // before the history: not counted
	for i := 0; i < 7; i++ {
		day := time.Duration(i)*24*time.Hour + time.Hour
		shift("dev", day, 20000, 1) // Claude's whole percents all land on dev's shifts
		shift("dev", day+6*time.Hour, 20000, 1)
		shift("busy", day, 10000, 0)
		shift("quiet", day, 1000, 0)
	}
	shift("new", time.Hour, 1000, 0)
	for i := 0; i < 3; i++ { // failed attempts: not shifts
		appendRun(team.Config, Run{Time: now.Add(-time.Duration(i+2) * time.Hour), Agent: "new", Status: "error:timeout", HasPct: true})
	}
	team = open(t, root)

	p := team.Rebalance(now)
	got := map[string]Balance{}
	var sum float64
	for _, b := range p.Agents {
		got[b.Name], sum = b, sum+b.Proposed
	}
	if _, ok := got["off"]; ok || len(p.Agents) != 4 {
		t.Fatalf("only the enabled agents are rebalanced: %+v", p.Agents)
	}
	if b := got["new"]; b.Proposed != 10 || !strings.Contains(b.Why, "too few") {
		t.Fatalf("one shift is too few to judge: new keeps its 10%%: %+v", b)
	}
	// 14% measured, split by tokens: busy's 7 shifts are 350k of 1.79M weighted tokens
	busy := got["busy"]
	if busy.Shifts != 7 || busy.PerShift < 0.38 || busy.PerShift > 0.40 || busy.PerWeek < 8 || busy.PerWeek > 8.2 {
		t.Fatalf("busy used about 0.39%% a shift, 8 shifts a week: %+v", busy)
	}
	if want := round1(100 * busy.Need * 1.25 / 60); busy.Proposed != want {
		t.Fatalf("busy gets its need and a quarter: %v, want %v", busy.Proposed, want)
	}
	if q := got["quiet"]; q.Proposed != round1(100*1.0/60) || !strings.Contains(q.Why, "the least") {
		t.Fatalf("quiet needs less than 1%% a week and gets 1%%: %+v", q)
	}
	if d := got["dev"]; d.Proposed < 100-10-busy.Proposed-1.7-0.2 || !strings.Contains(d.Why, "takes what the others leave") {
		t.Fatalf("dev takes the rest: %+v", d)
	}
	if sum < 99.8 || sum > 100.2 || p.Note != "" {
		t.Fatalf("the weights add up to 100: %v, %q", sum, p.Note)
	}

	if err := team.SetWeights(map[string]float64{"busy": -1}); err == nil {
		t.Fatal("a negative weight is refused")
	}
	if err := team.SetWeights(map[string]float64{"busy": 0, "dev": 0, "quiet": 0, "new": 0}); err == nil {
		t.Fatal("weights that add up to 0 are refused")
	}
	if err := team.SetWeights(map[string]float64{"nobody": 1}); err == nil {
		t.Fatal("an unknown agent is refused")
	}
	if err := team.SetWeights(map[string]float64{"busy": 0, "dev": 0, "quiet": 0, "new": 0, "off": 1}); err == nil {
		t.Fatal("a disabled agent's weight does not keep the enabled ones from adding up to 0")
	}
	if err := team.SetWeights(map[string]float64{"busy": busy.Proposed, "quiet": 1.7}); err != nil {
		t.Fatal(err)
	}
	if team = open(t, root); team.Agent("busy").Weight != busy.Proposed || team.Agent("quiet").Weight != 1.7 || team.Agent("dev").Weight != 50 {
		t.Fatalf("the weights are written, the others left: busy %v quiet %v dev %v", team.Agent("busy").Weight, team.Agent("quiet").Weight, team.Agent("dev").Weight)
	}

	// a share about what the agent needs is kept, since the estimate moves with every shift
	if err := team.SetWeights(map[string]float64{"busy": 7, "quiet": 1.7, "dev": 81.3, "new": 10}); err != nil {
		t.Fatal(err)
	}
	p = open(t, root).Rebalance(now)
	for _, b := range p.Agents {
		if b.Name == "busy" && (b.Proposed != 7 || !strings.Contains(b.Why, "keeps it")) {
			t.Fatalf("busy has 4.2%% a week and needs 3.9%%: it keeps its share: %+v", b)
		}
	}

	write(t, filepath.Join(root, "rota.conf"), "team_pct: 2\npacing: weekly\ntimezone: UTC\n")
	team = open(t, root)
	p = team.Rebalance(now)
	sum = 0
	for _, b := range p.Agents {
		sum += b.Proposed
	}
	if !strings.Contains(p.Note, "more than the pool") || sum < 99.8 || sum > 100.2 {
		t.Fatalf("when the needs exceed the pool, all are cut alike and the proposal says so: %q, %v", p.Note, sum)
	}
	// applied and made again, a proposal is the same: it never depends on the weights it replaces
	weights := map[string]float64{}
	for _, b := range p.Agents {
		weights[b.Name] = b.Proposed
	}
	if err := team.SetWeights(weights); err != nil {
		t.Fatal(err)
	}
	for _, b := range open(t, root).Rebalance(now).Agents {
		if math.Abs(b.Proposed-weights[b.Name]) > 0.11 {
			t.Fatalf("%s: proposed %v after applying %v: a proposal applied and made again drifts", b.Name, b.Proposed, weights[b.Name])
		}
	}
}

func TestTheShiftsAScheduleAllowsAWeek(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "timezone: UTC\n")
	write(t, filepath.Join(root, "agents", "a.md"), "---\nenabled: true\n---\na\n")
	team := open(t, root)
	now := at("2026-11-02T12:00:00Z")
	for _, c := range []struct {
		every, hours string
		until        bool
		want         float64
	}{{"12h", "", false, 14}, {"12h", "05:00-07:00", false, 7}, {"20h", "18:00-19:00", false, 7}, {"7d", "", false, 1}, {"1d", "", true, 0}, {"0", "", false, 0}, {"1ns", "", false, 7 * 24 * 60}} {
		a := &Agent{UntilDone: c.until}
		if err := a.Every.UnmarshalText([]byte(c.every)); err != nil {
			t.Fatal(err)
		}
		if c.hours != "" {
			if err := a.Hours.UnmarshalText([]byte(c.hours)); err != nil {
				t.Fatal(err)
			}
		}
		if got := team.scheduledPerWeek(a, now); got != c.want {
			t.Errorf("every %s, hours %q, until done %v: %v a week, want %v", c.every, c.hours, c.until, got, c.want)
		}
	}
}

// An agent that keeps its share never gets weight 0, however small its share.
func TestARebalanceNeverStopsAnAgentThatKeepsItsShare(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "team_pct: 60\npacing: weekly\n")
	write(t, filepath.Join(root, "agents", "big.md"), "---\nenabled: true\nweight: 9999\n---\nbig\n")
	write(t, filepath.Join(root, "agents", "tiny.md"), "---\nenabled: true\nweight: 1\n---\ntiny\n")
	for _, b := range open(t, root).Rebalance(time.Now()).Agents {
		if b.Proposed <= 0 {
			t.Fatalf("%s would be stopped: %+v", b.Name, b)
		}
	}
}

// What an agent cannot use before the pool fills up again is free: an agent whose fair share
// is used runs on it, while the others keep what they can still use.
func TestWhatAnAgentCannotUseIsFreeForOneWhoseShareIsUsed(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "team_pct: 10\npacing: weekly\ntimezone: UTC\nusage: off\n")
	agents := map[string]string{
		"dev":    "enabled: true\npriority: 2\nuntil_done: true\nweight: 30",
		"weekly": "enabled: true\npriority: 1\nevery: 7d\nweight: 50",
		"fresh":  "enabled: true\npriority: 1\nevery: 1d\nweight: 20",
	}
	for name, front := range agents {
		write(t, filepath.Join(root, "agents", name+".md"), "---\n"+front+"\n---\n"+name+"\n")
	}
	now := at("2026-11-04T20:00:00Z")
	u := &Usage{ResetsAt: now.Add(36 * time.Hour)} // the week began 5.5 days ago and fills up again in 1.5
	team := open(t, root)
	shift := func(agent string, ago time.Duration, pct float64) {
		appendRun(team.Config, Run{Time: now.Add(-ago), Agent: agent, Status: "ok", Out: int(pct * 10000), HasPct: true, PctUsed: pct})
	}
	for i := 0; i < 6; i++ {
		shift("dev", time.Duration(i+1)*time.Hour, 0.6) // 3.6% of its 3%: its share is used
	}
	shift("weekly", 13*24*time.Hour, 0.5)
	shift("weekly", 160*time.Hour, 0.5)
	shift("weekly", 24*time.Hour, 0.5) // due again in 6 days: after the pool fills up
	shift("fresh", 2*time.Hour, 0.2)   // one shift: too few to judge
	team = open(t, root)
	s := team.schedule(now, u, nil)
	if c := s.canUse["weekly"]; c < 0.62 || c > 0.63 || !strings.Contains(s.canWhy["weekly"], "1 more shift") {
		t.Fatalf("weekly keeps one shift at its recent pace, 0.625%%: %v %q", c, s.canWhy["weekly"])
	}
	if c := s.canUse["fresh"]; c < 1.79 || c > 1.81 {
		t.Fatalf("fresh has too few shifts to judge and keeps all it has left, 1.8%%: %v", c)
	}
	if s.free < 3.27 || s.free > 3.28 { // 10 - 4.3 used - 0.625 - 1.8
		t.Fatalf("free: %v", s.free)
	}
	s.asked["dev"] = true // it has new work
	if ok, why := s.eligible(team.Agent("dev")); !ok || !strings.Contains(why, "on the free part") {
		t.Fatalf("dev's share is used, and its shift of about 0.75%% fits in what is free: %v %q", ok, why)
	}

	// an agent that runs after another's shifts keeps all it has left
	write(t, filepath.Join(root, "agents", "weekly.md"), "---\nenabled: true\npriority: 1\nevery: 7d\nafter: dev\nweight: 50\n---\nweekly\n")
	s = open(t, root).schedule(now, u, nil)
	s.asked["dev"] = true
	if c := s.canUse["weekly"]; c < 4.49 || c > 4.51 || s.free > 0.5 {
		t.Fatalf("weekly runs after dev and keeps its 4.5%%: %v, free %v", c, s.free)
	}
	if ok, why := s.eligible(open(t, root).Agent("dev")); ok || s.free != 0 || why != "fair share, 36.0 of 30.0% of the pool used this week" {
		t.Fatalf("nothing is free, and dev waits: %v %v %q", s.free, ok, why)
	}

	// a paused agent frees all it has left
	write(t, filepath.Join(root, "agents", "weekly.md"), "---\nenabled: true\npriority: 1\nevery: 7d\nweight: 50\n---\nweekly\n")
	team = open(t, root)
	if err := team.PauseAgent("weekly"); err != nil {
		t.Fatal(err)
	}
	if s = open(t, root).schedule(now, u, nil); s.canUse["weekly"] != 0 || s.free < 3.89 || s.free > 3.91 {
		t.Fatalf("paused, weekly frees all: %v, free %v", s.canUse["weekly"], s.free)
	}

	// the team pool stays the only hard limit
	write(t, filepath.Join(root, "rota.conf"), "team_pct: 4.3\npacing: weekly\ntimezone: UTC\nusage: off\n")
	s = open(t, root).schedule(now, u, nil)
	s.asked["dev"] = true
	if ok, why := s.eligible(open(t, root).Agent("dev")); ok || !strings.HasPrefix(why, "team pool") {
		t.Fatalf("the pool is used up: %v %q", ok, why)
	}
}

// A shift stopped before its result recorded no tokens: what it measured stays its agent's.
func TestAShiftWithoutTokensKeepsWhatItMeasured(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "timezone: UTC\nusage: off\n")
	write(t, filepath.Join(root, "agents", "a.md"), "---\nenabled: true\n---\na\n")
	write(t, filepath.Join(root, "agents", "b.md"), "---\nenabled: true\n---\nb\n")
	team := open(t, root)
	now := at("2026-11-04T20:00:00Z")
	appendRun(team.Config, Run{Time: now.Add(-3 * time.Hour), Agent: "a", Status: "error:timeout", HasPct: true, PctUsed: 1})
	appendRun(team.Config, Run{Time: now.Add(-2 * time.Hour), Agent: "b", Status: "ok", Out: 1000, HasPct: true, PctUsed: 0})
	uses := open(t, root).shiftUses(now)
	if uses["a"].pct != 1 || uses["b"].pct != 0 {
		t.Fatalf("a's timeout used 1%%, b nothing: a %v, b %v", uses["a"].pct, uses["b"].pct)
	}
}

// The Issues tab lists the team's open issues, newest first unless another order is chosen,
// each marked when a pull request is open for it and by its labels that have an emoji; it names
// no agent. What narrows and orders it is kept.
func TestTheIssuesTabListsOpenIssues(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\nlabels: priority:high B60205 Fix first; priority:low C2E0C6 Fix last; rota:needs-rollout F9D0C4 🚀 Waits for a step by the human\n")
	write(t, filepath.Join(root, "agents", "dev.md"), "---\nenabled: true\nrepos: o/app, o/api\n---\ndev\n")
	team := open(t, root)
	day := func(d int) time.Time { return time.Date(2026, 11, d, 9, 0, 0, 0, time.UTC) }
	b, _ := json.Marshal(openList{Time: time.Now(), Repos: map[string]map[int]openItem{
		"o/app": {
			1: {Title: "old and urgent", Labels: []string{"priority:high"}, Created: day(1)},
			2: {Title: "newest, low", Labels: []string{"priority:low", "rota:leave-alone"}, Created: day(5)},
			3: {Title: "a fix for #1", PR: true, Issues: []string{"o/app#1"}},
		},
		"o/api": {
			7: {Title: "middle, unlabelled", Created: day(3)},
			8: {Title: "high, held", Labels: []string{"Priority:High", "rota:needs-rollout"}, Created: day(2)},
		},
	}})
	write(t, filepath.Join(root, ".open.json"), string(b))
	names := func(is []OpenIssue) (out []string) {
		for _, i := range is {
			out = append(out, i.Name)
		}
		return out
	}
	newest := team.Issues(OrderNewest)
	if got := names(newest); !reflect.DeepEqual(got, []string{"app#2", "api#7", "api#8", "app#1"}) {
		t.Fatalf("newest first, no pull request among them: %v", got)
	}
	if got := names(team.Issues(OrderOldest)); !reflect.DeepEqual(got, []string{"app#1", "api#8", "api#7", "app#2"}) {
		t.Fatalf("oldest first: %v", got)
	}
	if got := names(team.Issues(OrderPriority)); !reflect.DeepEqual(got, []string{"api#8", "app#1", "app#2", "api#7"}) {
		t.Fatalf("by the labels setting's order, newest first among those alike, the unlabelled last: %v", got)
	}
	marks := func(i OpenIssue) (out []string) {
		for _, m := range i.Marks {
			out = append(out, m.Emoji)
		}
		return out
	}
	one := team.Issues(OrderOldest)[0]
	if one.Ref() != "o/app#1" || !reflect.DeepEqual(one.PRs, []string{"o/app#3"}) || !reflect.DeepEqual(marks(one), []string{"🔧"}) || one.URL() != "https://github.com/o/app/issues/1" {
		t.Fatalf("an issue with a pull request for it: %+v", one)
	}
	if got := marks(newest[0]); !reflect.DeepEqual(got, []string{"✋"}) {
		t.Fatalf("the human's leave-alone label marks it: %v", got)
	}
	if got := marks(newest[2]); !reflect.DeepEqual(got, []string{"🚀"}) {
		t.Fatalf("a label of the labels setting with an emoji marks it: %v", got)
	}
	if got := names(OnlyIssues(newest, "", "", "PRIORITY:HIGH")); !reflect.DeepEqual(got, []string{"api#8", "app#1"}) {
		t.Fatalf("narrowed to a label, whatever its case: %v", got)
	}
	if got := names(OnlyIssues(newest, "o/api", "🚀")); !reflect.DeepEqual(got, []string{"api#8"}) {
		t.Fatalf("narrowed to a repository and a mark: %v", got)
	}
	repos, ms := Filters(newest)
	if fmt.Sprint(repos) != "[{o/api api 2} {o/app app 2}]" || len(ms) != 3 {
		t.Fatalf("the filters: %v %v", repos, ms)
	}
	if err := team.SetListFilter(IssuesTab, ListFilter{Order: OrderPriority, Labels: []string{" ui ", "UI"}}); err != nil {
		t.Fatal(err)
	}
	if f := team.ListFilter(IssuesTab); f.Order != OrderPriority || !reflect.DeepEqual(f.Labels, []string{"ui"}) || team.ListFilter(ReviewsTab).Order != "" {
		t.Fatalf("the Issues tab keeps its own filter and order: %+v", f)
	}
}

// An agent's settings apply within seconds too: the organizer, waiting for its next look an hour
// away, looks again when an agent's file changes, and starts the agent just enabled.
func TestTheOrganizerReadsAnAgentsChangedFileWhileItWaits(t *testing.T) {
	root := t.TempDir()
	prev := waitSlice
	t.Cleanup(func() { waitSlice = prev })
	waitSlice = 100 * time.Millisecond
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\ncommit: false\nsync_repos: false\ntick_minutes: 60\nclaude_bin: "+fakeClaude(t, root, "")+"\n")
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: false\n---\nalpha\n")
	stop := organize(t, root)
	defer stop()
	waitFor(t, "the organizer's first look", func() bool { live := open(t, root).Live(); return live != nil && !live.NextTick.IsZero() })
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: true\n---\nalpha\n")
	waitFor(t, "alpha's shift", func() bool { return len(open(t, root).Runs) == 1 })
}

// Hosts join where listen and listen_port say, as they change while the organizer runs, and
// cannot join once listen is off.
func TestHostsJoinWhereTheSettingsSayAsTheyChange(t *testing.T) {
	root := t.TempDir()
	prev := waitSlice
	t.Cleanup(func() { waitSlice = prev })
	waitSlice = 100 * time.Millisecond
	free := func() int {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		return ln.Addr().(*net.TCPAddr).Port
	}
	open := func(port int) bool {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
		if err == nil {
			c.Close()
		}
		return err == nil
	}
	a, b := free(), free()
	conf := func(listen string, port int) {
		write(t, filepath.Join(root, "rota.conf"), fmt.Sprintf("usage: off\nlisten: %s\nlisten_port: %d\ncommit: false\nsync_repos: false\nclaude_bin: /nonexistent\n", listen, port))
	}
	conf("127.0.0.1", a)
	stop := organize(t, root)
	defer stop()
	waitFor(t, "hosts may join on the first port", func() bool { return open(a) })
	time.Sleep(20 * time.Millisecond) // a new modification time
	conf("127.0.0.1", b)
	waitFor(t, "on the second port, and no longer on the first", func() bool { return open(b) && !open(a) })
	time.Sleep(20 * time.Millisecond)
	conf("off", b)
	waitFor(t, "nowhere once listen is off", func() bool { return !open(b) && !open(a) })
}

// Saves that come at once, from the pages and the console, keep each other's changes, and a
// reader never sees rota.conf half written.
func TestSettingsSavedAtOnceKeepEachOther(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\nweb: tailscale\n")
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: true\n---\nalpha\n")
	team := open(t, root)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	go func() { // a reader all along: the file is the old one or the new one, whole
		for {
			select {
			case <-stop:
				return
			default:
			}
			if b, err := os.ReadFile(filepath.Join(root, "rota.conf")); err == nil && !strings.Contains(string(b), "usage: off") {
				t.Errorf("rota.conf half written: %q", b)
				return
			}
		}
	}()
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func(i int) { defer wg.Done(); team.SetSettings(map[string]string{"tick_minutes": fmt.Sprint(30 + i)}) }(i)
		go func(i int) { defer wg.Done(); team.SetSettings(map[string]string{"team_pct": fmt.Sprint(10 + i)}) }(i)
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			team.SetAgent("alpha", map[string]string{"run_minutes": fmt.Sprint(40 + i)})
		}(i)
	}
	wg.Wait()
	close(stop)
	b, _ := os.ReadFile(filepath.Join(root, "rota.conf"))
	if !strings.Contains(string(b), "tick_minutes:") || !strings.Contains(string(b), "team_pct:") || !strings.Contains(string(b), "web: tailscale") {
		t.Fatalf("a save undid another: %s", b)
	}
	if a := open(t, root).Agent("alpha"); a == nil || a.RunMinutes < 40 || a.Role != "alpha" {
		t.Fatalf("the agent's file: %+v", a)
	}
	if left, _ := filepath.Glob(filepath.Join(root, ".*.tmp")); len(left) > 0 {
		t.Fatalf("temporary files left: %v", left)
	}
}

// A newly installed rota takes over a running shift: the shift stops between its steps, or while
// it only waits for the human's answer, but not while it talks with the human, or once the grace is
// over; once, and only after its claude began, so it has a session to resume. Steps are told apart
// by their calls' ids.
func TestANewRotaStopsTheRunningShiftBetweenItsSteps(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "rota")
	write(t, exe, "old")
	os.Chmod(exe, 0o755)
	prevSettle, prevGrace := selfSettle, updateGrace
	t.Cleanup(func() { selfSettle, updateGrace = prevSettle, prevGrace })
	selfSettle = 10 * time.Millisecond
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\n")
	team := open(t, root)
	o := &organizer{Config: team.Config, self: watchSelf(exe), monitor: monitor{Config: team.Config, started: time.Now(), lastShift: map[string]time.Time{}}}
	var causes []error
	shift := func() { o.setCancel(func(err error) { causes = append(causes, err) }); causes = nil }
	stopped := func(want int, why string) {
		t.Helper()
		o.takeUpdate(time.Now())
		if len(causes) != want || want > 0 && causes[0] != errUpdating {
			t.Fatalf("%s: %v", why, causes)
		}
	}
	tool := func(id, arg string) { o.step(Event{Kind: "tool", Tool: "Bash", ID: id, Arg: arg}) }
	answer := func(id string) { o.step(Event{Kind: "output", ID: id, Text: "ok"}) }
	captureStdout(t, func() {
		shift()
		write(t, exe, "new, and longer")
		o.takeUpdate(time.Now()) // seen
		time.Sleep(30 * time.Millisecond)
		stopped(0, "a shift still preparing has no session to resume")
		o.step(Event{Kind: "start"})
		tool("t1", "go test ./...")
		stopped(0, "a step is cut short")
		answer("t9") // a call not seen
		stopped(0, "an answer to another call leaves this one open")
		answer("t1")
		stopped(1, "between steps it stops for the update")
		stopped(1, "once")

		shift()
		o.step(Event{Kind: "start"})
		tool("w1", "rota wait 20261108-180247-qa-77ab 2>&1 | tail -1")
		stopped(1, "waiting for the human's answer is no step to finish")

		shift()
		o.step(Event{Kind: "start"})
		tool("w2", "rota wait 20261108-180247-qa-77ab && ./deploy.sh")
		stopped(0, "a wait that goes on into more is a step")
		answer("w2")
		tool("w3", "rota wait 20261108-180247-qa-77ab")
		tool("t2", "npm test")
		stopped(0, "a wait beside a step")
		answer("t2")
		stopped(1, "the wait left alone, it stops")

		shift()
		o.step(Event{Kind: "start"})
		os.MkdirAll(team.Path(steerDir), 0o755)
		os.WriteFile(team.steerFile("qa"), []byte(strconv.Itoa(os.Getpid())), 0o644)
		team.saveTalk("qa", chatTalk{Open: true, Since: time.Now(), Last: time.Now()})
		o.running = &Running{Agent: "qa"}
		stopped(0, "a talk with the human is not cut short")
		team.saveTalk("qa", chatTalk{Ended: "end"})
		stopped(1, "the talk over, it stops")
		o.running = nil

		shift()
		updateGrace = 50 * time.Millisecond
		o.step(Event{Kind: "start"})
		tool("t3", "sleep 600")
		stopped(0, "a long step")
		time.Sleep(60 * time.Millisecond)
		stopped(1, "a step longer than the grace is cut short")
		o.setCancel(nil)
		o.takeUpdate(time.Now()) // between shifts: the organizer's loop takes the update
	})
}

// A shift stopped for an update is interrupted, not stopped: it keeps its session, says why, and
// its next shift, asked for at once, resumes it.
func TestAShiftStoppedForAnUpdateResumes(t *testing.T) {
	root := t.TempDir()
	claude := filepath.Join(root, "claude")
	os.WriteFile(claude, []byte("#!/bin/bash\nread -r prompt\n"+`echo '{"type":"system","subtype":"init","session_id":"sess-u"}'; sleep 30`+"\n"), 0o755)
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\ncommit: false\nsync_repos: false\ntriage_model: off\nclaude_bin: "+claude+"\n")
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: true\n---\nalpha\n")
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	go func() { time.Sleep(1500 * time.Millisecond); cancel(errUpdating) }()
	captureStdout(t, func() { open(t, root).RunShift(ctx, "alpha", time.Now(), true, false) })
	if rs := runs(t, root); len(rs) != 1 || rs[0].Status != "interrupted" || rs[0].Session != "sess-u" || rs[0].Retry != "yes" {
		t.Fatalf("interrupted, with its session: %+v", rs)
	}
	if !open(t, root).Asked()["alpha"] {
		t.Fatal("its next shift is asked for at once")
	}
}

// A tool call and the answer to it carry the call's id, which tells parallel calls apart.
func TestToolCallsKeepTheirIDs(t *testing.T) {
	call := ParseLine(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls"}}]}}`)
	answer := ParseLine(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"a"}]}}`)
	if len(call) != 1 || call[0].ID != "toolu_1" || len(answer) != 1 || answer[0].ID != "toolu_1" {
		t.Fatalf("ids: %+v %+v", call, answer)
	}
}

// A dry run shows the role as the shift gets it, its placeholders filled in, so it can be checked.
func TestADryRunShowsTheRoleFilledIn(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\n")
	write(t, filepath.Join(root, "agents", "alpha.md"), "---\nenabled: true\n---\nYou are {agent}; reports go in {root}/reports/{agent}/.\n")
	team := open(t, root)
	out := captureStdout(t, func() {
		if err := team.RunShift(context.Background(), "alpha", time.Now(), true, true); err != nil {
			t.Error(err)
		}
	})
	if !strings.Contains(out, "--- system prompt ---\nYou are alpha; reports go in "+root+"/reports/alpha/.") {
		t.Fatalf("the system prompt is the role as the shift gets it:\n%s", out)
	}
}

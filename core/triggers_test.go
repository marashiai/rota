package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// on lists the triggers an agent starts on: each once, as the settings name them, a label after
// the colon of a labeled one, * matching any text. Anything else is refused, saying what there is.
func TestTriggersAreWhatTheSettingNames(t *testing.T) {
	var ts Triggers
	if err := ts.UnmarshalText([]byte(" Note, issue-labeled:priority:*, PR-merged,note, pr-labeled: rota:approved ")); err != nil {
		t.Fatal(err)
	}
	if want := (Triggers{"note", "issue-labeled:priority:*", "pr-merged", "pr-labeled:rota:approved"}); !slices.Equal(ts, want) {
		t.Fatalf("%q", ts)
	}
	if b, _ := ts.MarshalText(); string(b) != "note, issue-labeled:priority:*, pr-merged, pr-labeled:rota:approved" {
		t.Fatalf("%s", b)
	}
	if !ts.has("note") || !ts.onGitHub() || (Triggers{"note"}).onGitHub() {
		t.Fatal("note is a teammate's; the rest are on GitHub")
	}
	for in, want := range map[string]string{
		"pr-closed":       `no trigger "pr-closed": use issue-opened, issue-labeled`,
		"pr-merged:main":  "pr-merged takes no label",
		"issue-labeled:":  "name a label after the colon",
		"issue-labeled: ": "name a label after the colon",
	} {
		var bad Triggers
		if err := bad.UnmarshalText([]byte(in)); err == nil || !strings.Contains(err.Error(), want) || bad != nil {
			t.Errorf("%q: %v", in, err)
		}
	}
	ev := func(kind, label string) RepoEvent { return RepoEvent{Kind: kind, Label: label} }
	for _, c := range []struct {
		e    RepoEvent
		want string
	}{
		{ev("issue-labeled", "Priority:High"), "issue-labeled:priority:*"},
		{ev("issue-labeled", "docs"), ""},
		{ev("pr-labeled", "rota:approved"), "pr-labeled:rota:approved"},
		{ev("pr-merged", ""), "pr-merged"},
		{ev("issue-opened", ""), ""},
	} {
		if got, ok := ts.match(c.e); got != c.want || ok != (c.want != "") {
			t.Errorf("%+v matches %q, want %q", c.e, got, c.want)
		}
	}
	if got, _ := (Triggers{"issue-labeled"}).match(ev("issue-labeled", "anything")); got != "issue-labeled" {
		t.Fatal("without a label, any label")
	}
	for label, want := range map[string]bool{"area/ui": true, "Area/UI/forms": true, "area": false, "my-area/ui": false} {
		if _, ok := (Triggers{"issue-labeled:area/*"}).match(ev("issue-labeled", label)); ok != want {
			t.Errorf("issue-labeled:area/* matches %q: %v", label, ok)
		}
	}
	if _, ok := (Triggers{"pr-labeled:a.b+c"}).match(ev("pr-labeled", "aXb+c")); ok {
		t.Fatal("only * is a wildcard")
	}
}

// What the organizer tells from two looks at a repository: a new issue and its labels, a label
// added, a comment, a draft made ready, an approval (on GitHub, or the human's in rota), a
// conflict, failing checks, a base that moved, a merge. Its first look tells nothing, and an old
// issue it had not seen before is nothing new.
func TestTheOrganizerTellsWhatHappenedOnGitHub(t *testing.T) {
	t0 := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	item := func(n int, title string, created, updated time.Time, labels ...string) watchedItem {
		var it watchedItem
		it.Number, it.Title, it.CreatedAt, it.UpdatedAt = n, title, created, updated
		for _, l := range labels {
			it.Labels.Nodes = append(it.Labels.Nodes, struct{ Name string }{l})
		}
		return it
	}
	pr := func(it watchedItem, draft bool, mergeable, base, checks string, comments int) watchedItem {
		it.IsDraft, it.Mergeable, it.Comments.TotalCount = draft, mergeable, comments
		it.BaseRef = &struct{ Target struct{ Oid string } }{}
		it.BaseRef.Target.Oid = base
		it.Commits.Nodes = append(it.Commits.Nodes, struct {
			Commit struct{ StatusCheckRollup *struct{ State string } }
		}{})
		it.Commits.Nodes[0].Commit.StatusCheckRollup = &struct{ State string }{checks}
		return it
	}
	first := watchedRepo{}
	first.Issues.Nodes = []watchedItem{item(1, "Leak", t0.Add(-48*time.Hour), t0.Add(-time.Hour), "rota")}
	first.PullRequests.Nodes = []watchedItem{
		pr(item(10, "Fix leak", t0.Add(-5*time.Hour), t0.Add(-time.Hour)), true, "MERGEABLE", "aaa", "SUCCESS", 0),
		pr(item(11, "Speed up", t0.Add(-5*time.Hour), t0.Add(-time.Hour)), false, "MERGEABLE", "aaa", "PENDING", 2),
		pr(item(12, "Docs", t0.Add(-5*time.Hour), t0.Add(-time.Hour)), false, "CONFLICTING", "aaa", "SUCCESS", 0),
	}
	first.Merged.Nodes = []watchedItem{{Number: 9, Title: "Old one", MergedAt: t0.Add(-24 * time.Hour)}}
	evs, seen := watchDiff("o/app", nil, first, t0)
	if len(evs) != 0 || len(seen.Items) != 4 || !seen.Since.Equal(t0.Add(-time.Hour)) || !slices.Equal(seen.Merged, []int{9}) {
		t.Fatalf("the first look tells nothing, and takes note of what is there: %+v %+v", evs, seen)
	}

	t1 := t0.Add(2 * time.Minute)
	next := watchedRepo{}
	next.Issues.Nodes = []watchedItem{
		item(1, "Leak", t0.Add(-48*time.Hour), t1, "rota", "priority:high"),
		item(2, "Crash", t1.Add(-time.Minute), t1.Add(-time.Minute), "rota"),
		item(3, "Ancient", t0.Add(-900*time.Hour), t1), // older than any look, never seen: nothing new
	}
	approved := pr(item(11, "Speed up", t0.Add(-5*time.Hour), t1, ApprovedLabel), false, "UNKNOWN", "aaa", "FAILURE", 3)
	next.PullRequests.Nodes = []watchedItem{
		pr(item(10, "Fix leak", t0.Add(-5*time.Hour), t1), false, "CONFLICTING", "bbb", "SUCCESS", 0),
		approved,
		pr(item(12, "Docs", t0.Add(-5*time.Hour), t0.Add(-time.Hour)), false, "UNKNOWN", "bbb", "SUCCESS", 0),
	}
	next.Merged.Nodes = []watchedItem{{Number: 13, Title: "Bump", MergedAt: t1.Add(-30 * time.Second)}, {Number: 9, Title: "Old one", MergedAt: t0.Add(-24 * time.Hour)}}
	evs, seen = watchDiff("o/app", &seen, next, t1)
	var got []string
	for _, e := range evs {
		got = append(got, e.Kind+labelPart(e)+" #"+itoa(e.Number))
	}
	want := []string{
		"issue-labeled:priority:high #1", "issue-opened #2", "issue-labeled:rota #2",
		"pr-ready #10", "pr-conflict #10", "base-moved #10",
		"pr-labeled:rota:approved #11", "pr-commented #11", "pr-approved #11", "pr-checks-failed #11",
		"base-moved #12", "pr-merged #13",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("what happened:\n got %q\nwant %q", got, want)
	}
	if !seen.Items[12].Conflict || seen.Items[11].Conflict {
		t.Fatal("a conflict GitHub has not worked out yet stays as it was")
	}
	if _, ok := seen.Items[13]; ok || !slices.Contains(seen.Merged, 13) {
		t.Fatal("a merged pull request is told once, and no longer watched")
	}
	if evs, _ := watchDiff("o/app", &seen, next, t1.Add(2*time.Minute)); len(evs) != 0 {
		t.Fatalf("nothing changed, nothing happened: %+v", evs)
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// The organizer asks GitHub through gh, one query per repository its agents' triggers watch, and
// brings what happened to the agents it concerns: a note each, and a request for the shift. What
// it saw waits in the team folder for the next look; a repository GitHub does not answer for keeps
// what was seen of it.
func TestTheOrganizerWatchesTheTriggersRepositories(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\n")
	write(t, filepath.Join(root, "agents", "maintainer.md"), "---\nenabled: true\nevery: 1d\nrepos: o/app, o/web\non: pr-merged\n---\nm\n")
	write(t, filepath.Join(root, "agents", "scout.md"), "---\nenabled: true\nevery: 1d\nrepos: o/other\n---\ns\n")
	gh := filepath.Join(root, "gh")
	os.MkdirAll(gh, 0o755)
	fakeGh(t, `exec python3 - "$@" <<'PY'
import os, sys
d = "`+gh+`"
a = sys.argv[1:]
open(d + "/log", "a").write(" ".join(x for x in a if not x.startswith("query=")) + "\n")
name = [x for x in a if x.startswith("name=")][0][5:]
p = os.path.join(d, name + ".json")
if not os.path.exists(p):
    sys.stderr.write("Could not resolve to a Repository\n"); sys.exit(1)
print(open(p).read())
PY`)
	answer := func(name, merged string) {
		write(t, filepath.Join(gh, name+".json"), `{"data":{"repository":{"issues":{"nodes":[]},"pullRequests":{"nodes":[]},"merged":{"nodes":[`+merged+`]}}}}`)
	}
	answer("app", `{"number":5,"title":"Old","mergedAt":"2026-11-19T10:00:00Z"}`)
	team := open(t, root)
	ctx := context.Background()
	said, err := team.watchOnce(ctx, time.Now())
	if len(said) != 0 || err == nil || !strings.Contains(err.Error(), "o/web: ") {
		t.Fatalf("a first look tells nothing; a repository GitHub does not answer for says so: %q %v", said, err)
	}
	if b, _ := os.ReadFile(filepath.Join(gh, "log")); !strings.Contains(string(b), "-f owner=o -f name=app") || strings.Contains(string(b), "name=other") {
		t.Fatalf("it asks for the watched repositories only:\n%s", b)
	}
	answer("app", `{"number":6,"title":"Fix leak","mergedAt":"`+time.Now().UTC().Format(time.RFC3339)+`"},{"number":5,"title":"Old","mergedAt":"2026-11-19T10:00:00Z"}`)
	said, _ = team.watchOnce(ctx, time.Now().Add(watchEvery))
	if len(said) != 1 || said[0] != "pr-merged o/app#6 → maintainer (asked for)" || !team.Asked()["maintainer"] {
		t.Fatalf("a merge reaches the agent that starts on it: %q %v", said, team.Asked())
	}
	if n := team.Notes("maintainer"); len(n) != 1 || n[0].From != "github" || n[0].About != "o/app#6" || len(team.Notes("scout")) != 0 {
		t.Fatalf("as a note: %+v", n)
	}
	var state map[string]watchRepo
	b, _ := os.ReadFile(filepath.Join(root, watchFile))
	if json.Unmarshal(b, &state) != nil || !slices.Equal(state["o/app"].Merged, []int{5, 6}) || len(state) != 1 {
		t.Fatalf("what it saw waits for the next look: %s", b)
	}
	if said, _ := team.watchOnce(ctx, time.Now().Add(2*watchEvery)); len(said) != 0 {
		t.Fatalf("told once: %q", said)
	}
}

// While the team is paused, a trigger asks for the shift but does not wake the organizer: a
// wake-up while paused is the human's Run now, and would start whoever is due.
func TestATriggerDoesNotWakeAPausedTeam(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\n")
	write(t, filepath.Join(root, "agents", "triage.md"), "---\nenabled: true\nevery: 1d\nrepos: o/app\non: pr-approved\n---\nt\n")
	team := open(t, root)
	approved := []RepoEvent{{Kind: "pr-approved", Repo: "o/app", Number: 7, At: time.Now()}}
	if err := team.Pause(); err != nil {
		t.Fatal(err)
	}
	if _, err := team.onEvents(approved, time.Now()); err != nil || !team.Asked()["triage"] {
		t.Fatalf("the request waits: %v %v", err, team.Asked())
	}
	if _, err := os.Stat(team.kickFile()); !os.IsNotExist(err) {
		t.Fatal("a paused organizer is woken, as by the human's Run now")
	}
	team.Unpause()
	if _, err := team.onEvents(approved, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(team.kickFile()); err != nil {
		t.Fatal("going on, the organizer is woken to look now")
	}
}

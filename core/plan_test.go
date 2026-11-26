package core

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The plan is the order an agent gives the open issues with rota work-order, each with why, and
// those it leaves out for now, each with why: it replaces the last one, does not show what GitHub
// does not list as an open issue, and is what the Issues tab lists first, as planned, and what a
// role reads as {plan}.
func TestThePlanOrdersTheOpenIssues(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\n")
	write(t, filepath.Join(root, "agents", "planner.md"), "---\nenabled: true\nrepos: o/app, o/api\n---\nplan\n")
	write(t, filepath.Join(root, "agents", "developer.md"), "---\nenabled: true\nrepos: o/app, o/api\n---\nWork from {plan}\n")
	team := open(t, root)
	day := func(d int) time.Time { return time.Date(2026, 11, d, 9, 0, 0, 0, time.UTC) }
	b, _ := json.Marshal(openList{Time: time.Now(), Repos: map[string]map[int]openItem{
		"o/app": {
			1: {Title: "old crash", Created: day(1)},
			2: {Title: "newest typo", Created: day(5)},
			3: {Title: "a fix for #1", PR: true, Issues: []string{"o/app#1"}},
		},
		"o/api": {7: {Title: "slow login", Created: day(3)}},
	}})
	write(t, filepath.Join(root, ".open.json"), string(b))
	names := func(is []OpenIssue) (out []string) {
		for _, i := range is {
			out = append(out, i.Name)
		}
		return out
	}

	if got := names(team.Issues(OrderPlan)); !reflect.DeepEqual(got, []string{"app#2", "api#7", "app#1"}) {
		t.Fatalf("without a plan, as planned is newest first: %v", got)
	}
	if got := team.planText(); !strings.HasPrefix(got, "none: no agent has ordered") {
		t.Fatalf("a role reads that there is no plan: %q", got)
	}
	for entry, want := range map[string]string{
		"o/app#1":                    "say why",
		"nope#1 because":             "no repository of the team",
		"o/app#1 why\x00o/app#1 why": "twice",
	} {
		if _, err := team.SetWorkOrder("planner", strings.Split(entry, "\x00"), nil); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: want an error about %q, got %v", entry, want, err)
		}
	}
	if _, err := team.SetWorkOrder("planner", []string{"app#1 why"}, []string{"o/app#1 why not"}); err == nil || !strings.Contains(err.Error(), "both in the order and left out") {
		t.Errorf("an issue is in the order or left out of it, not both: %v", err)
	}
	unknown, err := team.SetWorkOrder("planner", []string{"api#7 every user waits 9 s to sign in", "o/app#3 a pull request, not an issue", "o/app#9 closed since", "app#1 crashes for one user a day"}, nil)
	if err != nil || !reflect.DeepEqual(unknown, []string{"o/app#3", "o/app#9"}) {
		t.Fatalf("what GitHub does not list as an open issue is said: %v %v", unknown, err)
	}
	p := team.Plan()
	if p.By != "planner" || len(p.Issues) != 2 || p.Issues[0].Ref() != "o/api#7" || p.Issues[0].Title != "slow login" || p.Issues[1].Why != "crashes for one user a day" {
		t.Fatalf("the plan holds the open issues in the order given, each with why: %+v", p)
	}
	issues := team.Issues(OrderPlan)
	if got := names(issues); !reflect.DeepEqual(got, []string{"api#7", "app#1", "app#2"}) {
		t.Fatalf("as planned: the plan's, then those it leaves out, newest first: %v", got)
	}
	if issues[0].Rank != 1 || issues[0].Why != "every user waits 9 s to sign in" || issues[2].Rank != 0 || issues[2].Why != "" {
		t.Fatalf("each planned issue has its place and why: %+v", issues)
	}
	if got := names(team.Issues(OrderNewest)); !reflect.DeepEqual(got, []string{"app#2", "api#7", "app#1"}) || team.Issues(OrderNewest)[1].Rank != 1 {
		t.Fatalf("another order keeps the place in the plan, for the list to show: %v", got)
	}
	text := team.planText()
	if !strings.Contains(text, "in the order planner gave at") || !strings.Contains(text, "\n1. o/api#7 «slow login»: every user waits 9 s to sign in\n2. o/app#1 «old crash»: crashes for one user a day") {
		t.Fatalf("a role reads the plan in order, each with its title and why:\n%s", text)
	}
	job, _, _, err := team.brief(team.Agent("developer"), team.schedule(time.Now(), nil, nil))
	if err != nil || !strings.Contains(strings.Join(job.Args, " "), "Work from the open issues in the order planner gave") {
		t.Fatalf("the developer's role reads the plan as {plan}: %v %v", err, job.Args)
	}

	b, _ = json.Marshal(openList{Time: time.Now(), Repos: map[string]map[int]openItem{"o/app": {2: {Title: "newest typo", Created: day(5)}}, "o/api": {}}})
	write(t, filepath.Join(root, ".open.json"), string(b))
	if got := team.planText(); !strings.HasPrefix(got, "none: every issue in the order planner gave") {
		t.Fatalf("once the planned issues are closed, nothing is left of the plan: %q", got)
	}
	if _, err := team.SetWorkOrder("planner", nil, nil); err != nil || team.Plan().By != "planner" || len(team.Plan().Issues) != 0 {
		t.Fatalf("an empty order replaces the last: %v %+v", err, team.Plan())
	}
}

// The plan names the open issues the team does not take now too (--out), each with why: the
// Issues tab lists them after the order, in the order given, then those it names neither way,
// newest first; a role reads them after the order, as not to be taken. Each issue's labels come
// in the labels setting's order, so its priority comes first, and by priority goes by it.
func TestThePlanSaysWhyItLeavesAnIssueOut(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\nlabels: priority:high B60205 Fix first; priority:low C2E0C6 Fix last\n")
	write(t, filepath.Join(root, "agents", "planner.md"), "---\nenabled: true\nrepos: o/app, o/api\n---\nplan\n")
	team := open(t, root)
	day := func(d int) time.Time { return time.Date(2026, 11, d, 9, 0, 0, 0, time.UTC) }
	b, _ := json.Marshal(openList{Time: time.Now(), Repos: map[string]map[int]openItem{
		"o/app": {
			1: {Title: "old crash", Created: day(1)},
			2: {Title: "newest typo", Created: day(5)},
			4: {Title: "slow page", Created: day(2), Labels: []string{"bug", "Priority:Low", "rota"}},
		},
		"o/api": {7: {Title: "slow login", Created: day(3)}},
	}})
	write(t, filepath.Join(root, ".open.json"), string(b))
	names := func(is []OpenIssue) (out []string) {
		for _, i := range is {
			out = append(out, i.Name)
		}
		return out
	}
	unknown, err := team.SetWorkOrder("planner", []string{"api#7 High: every user waits 9 s to sign in."},
		[]string{"app#4 Low: its fix waits for review in o/app#5.", "o/app#9 closed since", "app#1 None: waits for the human's answer."})
	if err != nil || !reflect.DeepEqual(unknown, []string{"o/app#9"}) {
		t.Fatalf("what GitHub does not list as an open issue is said: %v %v", unknown, err)
	}
	if p := team.Plan(); len(p.Issues) != 1 || len(p.Out) != 2 || p.Out[0].Ref() != "o/app#4" || p.Out[1].Why != "None: waits for the human's answer." {
		t.Fatalf("the plan keeps those left out, in order, each with why: %+v", p)
	}
	issues := team.Issues(OrderPlan)
	if got := names(issues); !reflect.DeepEqual(got, []string{"api#7", "app#4", "app#1", "app#2"}) {
		t.Fatalf("as planned: the order, then those left out as given, then the rest newest first: %v", got)
	}
	if i := issues[1]; i.Rank != 0 || !i.Out || i.Why != "Low: its fix waits for review in o/app#5." || issues[3].Out || issues[0].Out {
		t.Fatalf("one left out says why, and has no place: %+v", issues)
	}
	if got := issues[1].Labels; !reflect.DeepEqual(got, []string{"Priority:Low", "bug", "rota"}) {
		t.Fatalf("its priority comes first among its labels: %v", got)
	}
	if got := names(team.Issues(OrderPriority)); !reflect.DeepEqual(got, []string{"app#4", "app#2", "api#7", "app#1"}) {
		t.Fatalf("by priority goes by the labels setting: %v", got)
	}
	text := team.planText()
	if !strings.Contains(text, "1. o/api#7 «slow login»: High: every user waits 9 s to sign in.\n\nLeft out of the order for now, not to be taken, most important first:\n- o/app#4 «slow page»: Low: its fix waits for review in o/app#5.\n- o/app#1 «old crash»: None: waits") {
		t.Fatalf("a role reads those left out after the order:\n%s", text)
	}
	if _, err := team.SetWorkOrder("planner", nil, []string{"app#1 None: waits for the human's answer."}); err != nil {
		t.Fatal(err)
	}
	if text := team.planText(); !strings.HasPrefix(text, "none: every issue in the order planner gave") || !strings.Contains(text, "\n- o/app#1 «old crash»") {
		t.Fatalf("with none to take, a role still reads why the rest wait:\n%s", text)
	}
}

// An agent that waits for another (wait_for) holds back while that one would start a shift, so its
// own starts from what the other leaves: the planner goes before the developer that works from its
// plan. One that cannot start then holds nothing back, and two that wait for each other do not
// hold each other back forever.
func TestAnAgentWaitsForTheOneItWaitsFor(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\ncommit: false\nsync_repos: false\nweb: off\n")
	write(t, filepath.Join(root, "agents", "developer.md"), "---\nenabled: true\npriority: 1\nuntil_done: true\nhours: any\nwait_for: planner\nafter: planner\n---\ndev\n")
	write(t, filepath.Join(root, "agents", "planner.md"), "---\nenabled: true\npriority: 5\nevery: 1d\nhours: any\n---\nplan\n")
	write(t, filepath.Join(root, "agents", "ping.md"), "---\nenabled: true\nevery: 1d\nhours: any\nwait_for: pong\n---\nping\n")
	write(t, filepath.Join(root, "agents", "pong.md"), "---\nenabled: true\nevery: 1d\nhours: any\nwait_for: ping\n---\npong\n")
	team := open(t, root)
	now := today("09:00")
	for _, name := range []string{"developer", "ping", "pong"} {
		appendRun(team.Config, Run{Time: at(today("08:00")), Agent: name, Status: "ok"})
	}
	team.askFor("developer") // it has more
	st := status(t, root, now)
	if s := agentState(st, "developer").State; s != "waits for planner: its shift goes first" || st.Next != "planner" {
		t.Fatalf("the planner, never run, goes first, and the developer waits for it: %q, next %q", s, st.Next)
	}
	if why := open(t, root).WhyWaits(Status{Pid: 1, Agents: st.Agents})["developer"]; !strings.Contains(why, "It waits for planner's shift, which goes first") {
		t.Fatalf("the pages say why it waits: %q", why)
	}
	team.RunNow("developer")
	if ask := open(t, root).RunWait("developer", st); ask.Why != "waits for planner: its shift goes first" || ask.Next != "after planner's shift" {
		t.Fatalf("the human's Run now waits for the planner too: %+v", ask)
	}
	team.answered("developer")
	team.askFor("developer")
	appendRun(team.Config, Run{Time: at(today("08:30")), Agent: "planner", Status: "ok"})
	if st := status(t, root, now); agentState(st, "developer").State != "ready" || st.Next != "developer" {
		t.Fatalf("once the planner had its shift, the developer goes: %q, next %q", agentState(st, "developer").State, st.Next)
	}
	team.askFor("planner") // a new issue, or a new tag
	if st := status(t, root, now); st.Next != "planner" {
		t.Fatalf("asked again, the planner goes before the developer, which was asked first: next %q", st.Next)
	}
	write(t, filepath.Join(root, "agents", "planner.md"), "---\nenabled: true\npriority: 5\nevery: 1d\nhours: 03:00-03:01\n---\nplan\n")
	if st := status(t, root, now); agentState(st, "developer").State != "ready" {
		t.Fatalf("a planner outside its hours holds nothing back: %q", agentState(st, "developer").State)
	}
	write(t, filepath.Join(root, "agents", "planner.md"), "---\nenabled: true\npriority: 5\nevery: 1d\nhours: any\n---\nplan\n")
	s := open(t, root).schedule(at(now), nil, nil)
	if s.only = true; s.waitsFor(s.Agent("developer")) != "" {
		t.Fatal("paused, the planner the human did not ask for starts nothing, so holds nothing back")
	}
	team.askFor("ping")
	team.askFor("pong")
	st = status(t, root, now)
	if agentState(st, "ping").State != "ready" || agentState(st, "pong").State != "ready" {
		t.Fatalf("two that wait for each other hold each other back not at all: %q %q", agentState(st, "ping").State, agentState(st, "pong").State)
	}
}

// A new tag in a watched repository is told once, as tag-created, to the agents that start on it,
// as a note about the repository; the first look at a repository's tags, as after an update, only
// takes note of them.
func TestANewTagIsTold(t *testing.T) {
	t0 := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	tags := func(names ...string) watchedRepo {
		var r watchedRepo
		for _, n := range names {
			r.Tags.Nodes = append(r.Tags.Nodes, struct{ Name string }{n})
		}
		return r
	}
	evs, seen := watchDiff("o/app", nil, tags("2026-11-20-1", "2026-11-19-1"), t0)
	if len(evs) != 0 || len(seen.Tags) != 2 {
		t.Fatalf("the first look tells nothing: %+v %+v", evs, seen)
	}
	old := watchRepo{Since: t0, Items: map[int]watchItem{}} // kept before rota looked at tags
	if evs, seen := watchDiff("o/app", &old, tags("2026-11-20-1"), t0); len(evs) != 0 || len(seen.Tags) != 1 {
		t.Fatalf("the first look at the tags tells nothing either: %+v %+v", evs, seen)
	}
	evs, seen = watchDiff("o/app", &seen, tags("2026-11-21-2", "2026-11-21-1", "2026-11-20-1", "2026-11-19-1"), t0.Add(2*time.Minute))
	if len(evs) != 2 || evs[0].Tag != "2026-11-21-1" || evs[1].Tag != "2026-11-21-2" || evs[0].Kind != "tag-created" || evs[0].Ref() != "" {
		t.Fatalf("each new tag, oldest first, about no issue: %+v", evs)
	}
	if evs, _ := watchDiff("o/app", &seen, tags("2026-11-21-2", "2026-11-21-1"), t0.Add(4*time.Minute)); len(evs) != 0 {
		t.Fatalf("a tag is told once: %+v", evs)
	}

	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\n")
	write(t, filepath.Join(root, "agents", "planner.md"), "---\nenabled: true\nrepos: o/app\non: tag-created\n---\nplan\n")
	write(t, filepath.Join(root, "agents", "developer.md"), "---\nenabled: true\nrepos: o/app\non: issue-opened\n---\ndev\n")
	team := open(t, root)
	said, err := team.onEvents(evs[:1], t0)
	if err != nil || len(said) != 1 || said[0] != "tag-created o/app 2026-11-21-1 → planner (asked for)" {
		t.Fatalf("the tag reaches the planner alone: %q %v", said, err)
	}
	notes := team.Notes("planner")
	if len(notes) != 1 || notes[0].About != "" || notes[0].Text != "o/app got the tag 2026-11-21-1 (tag-created, one of your triggers)" || !team.Asked()["planner"] {
		t.Fatalf("a note about the repository, and a request for its shift: %+v", notes)
	}
}

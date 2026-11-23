package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mailTeam is a team with every kind of thread: questions open and closed, pull requests in each
// state, an update and a report.
func mailTeam(t *testing.T) (string, *Team) {
	t.Helper()
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\ncommit: false\nsync_repos: false\nreview_label: rota:needs-review\n"+
		"labels: high B60205 🔥 Fix first; low C2E0C6 Fix last; rota:needs-rollout F9D0C4 🚀 Needs a step by the human\n")
	write(t, filepath.Join(root, "agents", "developer.md"), "---\nenabled: true\nrepos: o/shop-creator\n---\nwrites code\n")
	write(t, filepath.Join(root, "agents", "triage.md"), "---\nenabled: true\nrepos: o/shop-creator, o/shop-backend\n---\norders reviews\n")
	asked := time.Now().Add(-time.Hour)
	b, _ := json.Marshal(openList{Time: time.Now(), Repos: map[string]map[int]openItem{
		"o/shop-creator": {
			1: {Title: "fix(studio): ready one", PR: true, Head: "developer/one", Base: "main", Labels: []string{"rota:needs-review", "high", "low", "rota", "rota:needs-rollout"}, Add: 12, Del: 3, Files: 2, Updated: asked},
			2: {Title: "Notes sent", PR: true, Head: "developer/two", Base: "main", Labels: []string{"rota:needs-review"}, Talk: talk{Read: asked, Waiting: 1}, Updated: asked},
			3: {Title: "Approved one", PR: true, Head: "developer/three", Base: "main", Labels: []string{ApprovedLabel}, Updated: asked},
			4: {Title: "Still worked on", PR: true, Head: "developer/four", Base: "main", Updated: asked},
			5: {Title: "Answered one", PR: true, Head: "developer/five", Base: "main", Labels: []string{"rota:needs-review"},
				Talk: talk{Read: asked, Answered: asked.Add(time.Minute), By: "developer", Answer: "Done, in a new commit."}, Updated: asked},
			6: {Title: "On top of one", PR: true, Head: "developer/six", Base: "developer/one", Labels: []string{"rota:needs-review"}, Updated: asked},
			7: {Title: "an issue"},
		},
		"o/shop-backend": {},
	}})
	write(t, filepath.Join(root, openFile), string(b))
	q, _ := json.Marshal(reviewOrder{By: "triage", Time: asked, PRs: []reviewEntry{{"o/shop-creator#1", "High: it blocks the rollout"}}})
	write(t, filepath.Join(root, queueFile), string(q))
	team := open(t, root)
	if _, err := team.ask("developer", "shop-creator#7: Drop the share code? I recommend dropping it.", asked); err != nil {
		t.Fatal(err)
	}
	id, _ := team.ask("triage", "Keep the old page?", asked)
	if err := team.Answer(id, "No"); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, reportsDir, time.Now().Format("2006-01-02")+".md"), "# Sunday 8 November\n\nAll quiet.\n")
	return root, open(t, root)
}

func threadOf(t *testing.T, m Mail, id string) Thread {
	t.Helper()
	for _, th := range m.Threads {
		if th.ID == id {
			return th
		}
	}
	t.Fatalf("no thread %s", id)
	return Thread{}
}

func TestTheMailHoldsWhatWaitsAndWhatHappened(t *testing.T) {
	_, team := mailTeam(t)
	m := team.Mail(time.Now())
	var ask, answered Thread
	for _, th := range m.Threads {
		if th.Kind == "ask" && th.Box == BoxInbox {
			ask = th
		}
		if th.Kind == "ask" && th.Box == BoxDone {
			answered = th
		}
	}
	if ask.Subject != "Drop the share code?" || ask.Ref != "o/shop-creator#7" || ask.Label != "shop-creator#7" || ask.QID == "" || ask.Msgs[0].From != "developer" {
		t.Fatalf("the open question: %+v", ask)
	}
	if len(answered.Msgs) != 2 || answered.Msgs[1].From != "you" || answered.Msgs[1].Text != "No" {
		t.Fatalf("the answered question: %+v", answered)
	}
	for id, want := range map[string]string{"pr:o/shop-creator#1": BoxInbox, "pr:o/shop-creator#2": BoxWaiting, "pr:o/shop-creator#3": BoxDone,
		"pr:o/shop-creator#4": BoxOpen, "pr:o/shop-creator#5": BoxInbox} {
		if th := threadOf(t, m, id); th.Box != want {
			t.Errorf("%s is in %s, want %s", id, th.Box, want)
		}
	}
	one := threadOf(t, m, "pr:o/shop-creator#1")
	if one.Subject != "Ready one" || one.PR.Author != "developer" || one.PR.Add != 12 || one.PR.Why != "High: it blocks the rollout" || len(one.Msgs) != 1 ||
		one.Msgs[0].From != "developer" || one.Msgs[0].Text != "Ready for your review." || !one.At.Equal(one.Msgs[0].At) ||
		strings.Join(one.Why, ",") != "🔥 high,🚀 needs rollout" {
		t.Fatalf("a pull request ready for review: %+v %+v", one, one.PR)
	}
	if six := threadOf(t, m, "pr:o/shop-creator#6"); len(six.PR.Waits) != 1 || six.PR.Waits[0] != "shop-creator#1" || six.PR.Depth != 1 {
		t.Fatalf("a stacked pull request waits for the one under it, by its name: %+v", six.PR)
	}
	five := threadOf(t, m, "pr:o/shop-creator#5")
	if !five.PR.Answered || five.Msgs[len(five.Msgs)-1].Text != "Done, in a new commit." {
		t.Fatalf("an answered pull request: %+v", five)
	}
	if !(five.Rank < ask.Rank && ask.Rank < one.Rank) {
		t.Fatalf("an answer, then a question, then a review: ranks %d %d %d", five.Rank, ask.Rank, one.Rank)
	}
	var report Thread
	for _, th := range m.Threads {
		switch th.Kind {
		case "update":
			t.Fatalf("what a shift says the human should know is no message, only the day's report: %+v", th)
		case "digest":
			report = th
		}
	}
	if report.Subject != "Your team today" || report.Msgs[0].Text != "All quiet." || report.Msgs[0].From != "team" {
		t.Fatalf("the report: %+v", report)
	}
	for _, th := range m.Threads {
		if th.Unread {
			t.Errorf("%s is unread, but it was there when the mail began", th.ID)
		}
	}
	if len(m.Repos) != 2 || m.Repos[0].Name != "shop-creator" || m.Repos[1].Name != "shop-backend" {
		t.Fatalf("repositories go by their own names, nothing guessed: %+v", m.Repos)
	}
	write(t, filepath.Join(team.Root, "agents", "fork.md"), "---\nenabled: true\nrepos: elsewhere/shop-creator, o/Puzzle-Arcade\n---\nforks\n")
	if names := open(t, team.Root).repoLabels(); names["o/shop-creator"] != "o/shop-creator" || names["elsewhere/shop-creator"] != "elsewhere/shop-creator" ||
		names["o/Puzzle-Arcade"] != "Puzzle-Arcade" || names["o/shop-backend"] != "shop-backend" {
		t.Fatalf("two of one name are told apart by their owners, the others keep their names: %v", names)
	}
	if len(m.Members) != 2 || m.Members[0].Color == "" {
		t.Fatalf("members: %+v", m.Members)
	}
	if m.Prefs != defaultMailPrefs {
		t.Fatalf("prefs: %+v", m.Prefs)
	}
}

func TestWhatComesLaterIsUnreadUntilRead(t *testing.T) {
	root, team := mailTeam(t)
	team.Mail(time.Now())
	if _, err := team.ask("developer", "A new question?", time.Now()); err != nil {
		t.Fatal(err)
	}
	var b openList // pull request 4 now asks for a review: it comes into the inbox, new
	raw, _ := os.ReadFile(filepath.Join(root, openFile))
	json.Unmarshal(raw, &b)
	it := b.Repos["o/shop-creator"][4]
	it.Labels = []string{"rota:needs-review"}
	b.Repos["o/shop-creator"][4] = it
	raw, _ = json.Marshal(b)
	write(t, filepath.Join(root, openFile), string(raw))
	m := open(t, root).Mail(time.Now())
	var fresh Thread
	for _, th := range m.Threads {
		if th.Kind == "ask" && th.Box == BoxInbox && strings.HasPrefix(th.Subject, "A new") {
			fresh = th
		}
	}
	four := threadOf(t, m, "pr:o/shop-creator#4")
	if !fresh.Unread || four.Box != BoxInbox || !four.Unread {
		t.Fatalf("new things are unread: question %v, pull request %s %v", fresh.Unread, four.Box, four.Unread)
	}
	if err := team.MarkMail(four.ID, "read", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if th := threadOf(t, open(t, root).Mail(time.Now()), four.ID); th.Unread {
		t.Fatal("read, it stays unread")
	}
	team.MarkMail(four.ID, "unread", "", time.Now())
	if th := threadOf(t, open(t, root).Mail(time.Now()), four.ID); !th.Unread {
		t.Fatal("marked unread, it is read")
	}
}

func TestArchiveSnoozeStarAndMove(t *testing.T) {
	root, team := mailTeam(t)
	now := time.Now()
	team.Mail(now)
	one := "pr:o/shop-creator#1"
	if err := team.MarkMail(one, "archive", "", now); err != nil {
		t.Fatal(err)
	}
	if th := threadOf(t, open(t, root).Mail(now), one); th.Box != BoxDone {
		t.Fatalf("archived, it is in %s", th.Box)
	}
	team.MarkMail(one, "unarchive", "", now)
	if th := threadOf(t, open(t, root).Mail(now), one); th.Box != BoxInbox {
		t.Fatalf("unarchived, it is in %s", th.Box)
	}
	ask := ""
	for _, th := range team.Mail(now).Threads {
		if th.Kind == "ask" && th.Box == BoxInbox {
			ask = th.ID
		}
	}
	team.MarkMail(ask, "read", "", now)
	until := now.Add(time.Hour)
	if err := team.MarkMail(ask, "snooze", until.Format(time.RFC3339), now); err != nil {
		t.Fatal(err)
	}
	if th := threadOf(t, open(t, root).Mail(now), ask); th.Snooze.IsZero() || th.Unread {
		t.Fatalf("snoozed: %+v", th)
	}
	if th := threadOf(t, open(t, root).Mail(until.Add(time.Minute)), ask); !th.Snooze.IsZero() || !th.Unread {
		t.Fatalf("back from its snooze, it is new: %+v", th)
	}
	if team.MarkMail(ask, "snooze", now.Add(-time.Minute).Format(time.RFC3339), now) == nil {
		t.Fatal("a snooze into the past")
	}
	team.MarkMail(one, "star", "", now)
	team.MarkMail(one, "place", "other", now)
	if th := threadOf(t, open(t, root).Mail(now), one); !th.Star || th.Place != "other" {
		t.Fatalf("starred and moved: %+v", th)
	}
	if team.MarkMail(one, "place", "elsewhere", now) == nil || team.MarkMail(one, "explode", "", now) == nil || team.MarkMail("", "read", "", now) == nil {
		t.Fatal("nonsense is refused")
	}
}

func TestAnArchivedThreadComesBackWithNews(t *testing.T) {
	root, team := mailTeam(t)
	now := time.Now()
	team.Mail(now)
	five := "pr:o/shop-creator#5"
	team.MarkMail(five, "archive", "", now)
	var b openList
	raw, _ := os.ReadFile(filepath.Join(root, openFile))
	json.Unmarshal(raw, &b)
	it := b.Repos["o/shop-creator"][5]
	it.Talk.Answered, it.Talk.Answer = now.Add(time.Minute), "And another answer."
	b.Repos["o/shop-creator"][5] = it
	raw, _ = json.Marshal(b)
	write(t, filepath.Join(root, openFile), string(raw))
	th := threadOf(t, open(t, root).Mail(now.Add(2*time.Minute)), five)
	if th.Box != BoxInbox || !th.Unread {
		t.Fatalf("an answer after the archive brings it back, new: %s %v", th.Box, th.Unread)
	}
}

func TestMailPrefs(t *testing.T) {
	_, team := mailTeam(t)
	p := MailPrefs{Split: false, Focus: 3, After: "list", Notify: "all", Chat: "off"}
	if err := team.SetMailPrefs(p); err != nil {
		t.Fatal(err)
	}
	if got := team.Mail(time.Now()).Prefs; got != p {
		t.Fatalf("prefs %+v", got)
	}
	for _, bad := range []MailPrefs{{Focus: 0, After: "next", Notify: "all"}, {Focus: 5, After: "back", Notify: "all"}, {Focus: 5, After: "next", Notify: "loud"}, {Focus: 5, After: "next", Notify: "all", Chat: "loud"}} {
		if team.SetMailPrefs(bad) == nil {
			t.Errorf("%+v is accepted", bad)
		}
	}
}

func TestEachFolderKeepsItsFilters(t *testing.T) {
	_, team := mailTeam(t)
	for _, step := range []struct {
		folder string
		change map[string]any
	}{
		{"inbox", map[string]any{"unread": true, "kind": " review ", "date": "week"}},
		{"repo:o/app", map[string]any{"star": true}},
		{"inbox", map[string]any{"from": "alpha"}}, // another device's change: the rest stays
		{"inbox", map[string]any{"date": ""}},
	} {
		if err := team.ChangeMailFilter(step.folder, step.change); err != nil {
			t.Fatalf("%s %v: %v", step.folder, step.change, err)
		}
	}
	got := team.Mail(time.Now()).Filters
	if want := (MailFilter{Unread: true, Kind: "review", From: "alpha"}); got["inbox"] != want || got["repo:o/app"] != (MailFilter{Star: true}) || len(got) != 2 {
		t.Fatalf("filters %+v", got)
	}
	if err := team.ChangeMailFilter("inbox", map[string]any{"clear": true, "star": true}); err != nil {
		t.Fatal(err)
	}
	if err := team.ChangeMailFilter("repo:o/app", map[string]any{"star": false}); err != nil { // none left: the folder has none
		t.Fatal(err)
	}
	if got := team.Mail(time.Now()).Filters; len(got) != 1 || got["inbox"] != (MailFilter{Star: true}) {
		t.Fatalf("after clearing: %+v", got)
	}
	for _, bad := range []struct {
		folder string
		change map[string]any
	}{
		{"issues", map[string]any{"star": true}}, {"repo:o", map[string]any{"star": true}}, {"repo: /x", map[string]any{"star": true}},
		{"all", map[string]any{"date": "year"}}, {"all", map[string]any{"unread": "yes"}}, {"all", map[string]any{"from": 3.0}},
		{"all", map[string]any{"loud": true}}, {"all", nil},
	} {
		if team.ChangeMailFilter(bad.folder, bad.change) == nil {
			t.Errorf("%s %v is kept", bad.folder, bad.change)
		}
	}
	if got := team.Mail(time.Now()).Filters; len(got) != 1 || got["inbox"] != (MailFilter{Star: true}) {
		t.Fatalf("a refused change changed them: %+v", got)
	}
	if p := team.Mail(time.Now()).Prefs; p != defaultMailPrefs { // the prefs beside them are untouched
		t.Errorf("prefs %+v", p)
	}
}

func TestSubjects(t *testing.T) {
	for in, want := range map[string]string{
		"Drop the share code? I recommend dropping it.":                 "Drop the share code?",
		"**Opened** [#675](https://x/675): routes the hosts. And more.": "Opened #675: routes the hosts",
		"- Fix it.\nSecond line":                                        "Fix it",
		strings.Repeat("word ", 40):                                     "W" + strings.TrimSpace(strings.Repeat("word ", 17))[1:] + "…",
		"copy cert.pem to production":                                   "Copy cert.pem to production",
		"Ask about naïve café’s menu, which is a long sentence that goes on and on and on without a stop": "Ask about naïve café’s menu, which is a long sentence that goes on and on and on without…",
	} {
		if got := subjectOf(in); got != want {
			t.Errorf("subjectOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestANotesSubjectIsWhatTheHumanWrote(t *testing.T) {
	_, team := mailTeam(t)
	for _, c := range []struct{ agent, about, want string }{
		{"developer", "", ""},
		{"developer", "#673", "o/shop-creator#673"}, // its only repository
		{"triage", "creator#673", "o/shop-creator#673"},
		{"triage", "o/shop-backend#9", "o/shop-backend#9"},
	} {
		if got, err := team.NoteRef(c.agent, c.about); err != nil || got != c.want {
			t.Errorf("NoteRef(%s, %q) = %q, %v; want %q", c.agent, c.about, got, err, c.want)
		}
	}
	for _, bad := range [][2]string{{"triage", "#673"}, {"developer", "nowhere#1"}, {"developer", "673"}} {
		if got, err := team.NoteRef(bad[0], bad[1]); err == nil {
			t.Errorf("NoteRef(%s, %q) = %q, want an error", bad[0], bad[1], got)
		}
	}
}

// A team with no repositories or agents yet still has lists, and a .mail.json that cannot be read
// is never written over: the human's marks stay until it can be read again.
func TestTheMailOfANewTeamAndAnUnreadableOne(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\ncommit: false\n")
	m := open(t, root).Mail(time.Now())
	if m.Members == nil || m.Repos == nil || len(m.Threads) != 0 {
		t.Fatalf("a new team's mail: %+v", m)
	}
	root, team := mailTeam(t)
	team.Mail(time.Now())
	team.MarkMail("pr:o/shop-creator#1", "star", "", time.Now())
	path := filepath.Join(root, mailFile)
	good, _ := os.ReadFile(path)
	os.WriteFile(path, []byte("{broken"), 0o644)
	if err := team.MarkMail("pr:o/shop-creator#1", "archive", "", time.Now()); err == nil {
		t.Fatal("an unreadable .mail.json is written over")
	}
	open(t, root).Mail(time.Now())
	if b, _ := os.ReadFile(path); string(b) != "{broken" {
		t.Fatalf("reading the mail wrote over it: %s", b)
	}
	os.WriteFile(path, good, 0o644)
	if th := threadOf(t, open(t, root).Mail(time.Now()), "pr:o/shop-creator#1"); !th.Star {
		t.Fatal("the star is back once the file reads again")
	}
}

func TestOldClosedQuestionsAreNotRead(t *testing.T) {
	root, team := mailTeam(t)
	old := filepath.Join(root, questionsDir, closedDir, "20250101-100000-developer-ab12.json")
	write(t, old, `{"agent":"developer","asked":"2025-01-01T10:00:00Z","text":"Old?","answer":"Yes","answered":"2025-01-01T11:00:00Z"}`)
	long := time.Now().Add(-mailKeep - 24*time.Hour)
	os.Chtimes(old, long, long)
	for _, th := range team.Mail(time.Now()).Threads {
		if strings.HasSuffix(th.ID, "20250101-100000-developer-ab12") {
			t.Fatal("a question closed before what the mail keeps is in it")
		}
	}
}

// rota ask may say more than its text: a subject, the answers to tap and the one recommended,
// the steps of a to-do, how much it matters, and what it is about. The mail shows all of it; an
// important question comes before the others, an unimportant one after every review.
func TestAQuestionSaysWhatTheHumanNeeds(t *testing.T) {
	root, team := mailTeam(t)
	id, err := team.AskWith(Question{Agent: "developer", Text: "The share code is unused since the play host.", Subject: "Drop the share code?",
		Options: []string{"Drop it", "Keep it"}, Recommend: "Drop it", Importance: "high", About: "#519"})
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := team.AskWith(Question{Agent: "developer", Text: "The share code is unused since the play host.", About: "shop-creator#519"}); again != id {
		t.Fatal("the same question is asked twice")
	}
	todo, err := team.AskWith(Question{Agent: "triage", Text: "Finish the rollout.", Subject: "Finish the play-host rollout",
		Steps: []string{"Add `STUDIO_PLAY_HOST` to `.env`", "Restart the tunnel"}, Importance: "low"})
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Question{
		{Agent: "developer", Text: "x", Options: []string{"only one"}},
		{Agent: "developer", Text: "x", Options: []string{"a", "b"}, Recommend: "c"},
		{Agent: "developer", Text: "x", Importance: "urgent"},
		{Agent: "developer", Text: "x", Subject: strings.Repeat("s", subjectMax+1)},
		{Agent: "developer", Text: "x", About: "nowhere#1"},
		{Agent: "developer", Text: ""},
	} {
		if _, err := team.AskWith(bad); err == nil {
			t.Errorf("%+v is asked", bad)
		}
	}
	m := open(t, root).Mail(time.Now())
	ask, do := threadOf(t, m, "q:"+id), threadOf(t, m, "q:"+todo)
	if ask.Subject != "Drop the share code?" || ask.Ref != "o/shop-creator#519" || len(ask.Why) != 1 || ask.Why[0] != "High" ||
		strings.Join(ask.Msgs[0].Options, "|") != "Drop it|Keep it" || ask.Msgs[0].Recommend != "Drop it" {
		t.Fatalf("a question with options: %+v", ask)
	}
	if do.Kind != "do" || len(do.Msgs[0].Steps) != 2 || do.Subject != "Finish the play-host rollout" {
		t.Fatalf("a to-do: %+v", do)
	}
	var plain Thread
	for _, th := range m.Threads {
		if th.Kind == "ask" && th.Box == BoxInbox && th.ID != ask.ID {
			plain = th
		}
	}
	if !(ask.Rank < plain.Rank && plain.Rank < threadOf(t, m, "pr:o/shop-creator#1").Rank && threadOf(t, m, "pr:o/shop-creator#1").Rank < do.Rank) {
		t.Fatalf("important, normal, a review, unimportant: %d %d %d %d", ask.Rank, plain.Rank, threadOf(t, m, "pr:o/shop-creator#1").Rank, do.Rank)
	}
	team.MarkMail(do.ID, "tick", "1:on", time.Now())
	team.MarkMail(do.ID, "tick", "0:on", time.Now())
	team.MarkMail(do.ID, "tick", "0:on", time.Now()) // two windows ticking it: still ticked
	team.MarkMail(do.ID, "tick", "1:off", time.Now())
	if th := threadOf(t, open(t, root).Mail(time.Now()), do.ID); len(th.Ticks) != 1 || th.Ticks[0] != 0 {
		t.Fatalf("ticks: %v", th.Ticks)
	}
	for _, bad := range []string{"-1:on", "x:on", "1", "1:maybe"} {
		if team.MarkMail(do.ID, "tick", bad, time.Now()) == nil {
			t.Errorf("tick %q is taken", bad)
		}
	}
	other, err := team.AskWith(Question{Agent: "developer", Text: "The share code is unused since the play host.", About: "#520"})
	if err != nil || other == id {
		t.Fatalf("the same question about another issue is another question: %s %v", other, err)
	}
	if _, err := team.AskWith(Question{Agent: "developer", Text: "x", Steps: []string{"one\ntwo"}}); err == nil {
		t.Fatal("a step of two lines is taken")
	}
	spaced, _ := team.AskWith(Question{Agent: "developer", Text: "Copy it", Steps: []string{"  cp 'a  b.env' .env  "}})
	if th := threadOf(t, open(t, root).Mail(time.Now()), "q:"+spaced); th.Msgs[0].Steps[0] != "cp 'a  b.env' .env" {
		t.Fatalf("a step's command keeps its spaces: %q", th.Msgs[0].Steps[0])
	}
	listed, _ := team.AskWith(Question{Agent: "developer", Text: "The plan, in order:  \r\n1. the share code\n2. the play host\n\n\n\nAgree?\n",
		Options: []string{"Agree", "Change it"}})
	if th := threadOf(t, open(t, root).Mail(time.Now()), "q:"+listed); th.Msgs[0].Text != "The plan, in order:\n1. the share code\n2. the play host\n\nAgree?" {
		t.Fatalf("a question keeps its list and its paragraphs: %q", th.Msgs[0].Text)
	}
	if err := team.Answer(listed, "Agree"); err != nil {
		t.Fatal(err)
	}
	if n := team.Notes("developer"); !strings.Contains(n[len(n)-1].Text, `"The plan, in order: 1. the share code 2. the play host Agree?": Agree`) {
		t.Fatalf("the answer quotes the question on one line: %q", n[len(n)-1].Text)
	}
	if err := team.Answer(id, "Drop it"); err != nil {
		t.Fatal(err)
	}
	if n := team.Notes("developer"); len(n) == 0 || n[len(n)-1].About != "o/shop-creator#519" {
		t.Fatalf("the answer goes with what the question is about: %+v", n)
	}
}

func TestThePhoneHearsWhatTheHumanChose(t *testing.T) {
	_, team := mailTeam(t)
	for _, c := range []struct {
		notify string
		want   map[string]bool
	}{
		{"focused", map[string]bool{"high": true, "": true, "low": false}},
		{"all", map[string]bool{"high": true, "": true, "low": true}},
		{"off", map[string]bool{"high": false, "": false, "low": false}},
	} {
		if err := team.SetMailPrefs(MailPrefs{Split: true, Focus: 5, After: "next", Notify: c.notify}); err != nil {
			t.Fatal(err)
		}
		for importance, want := range c.want {
			if got := team.Notifies(importance); got != want {
				t.Errorf("notify %s, importance %q: %v, want %v", c.notify, importance, got, want)
			}
		}
	}
}

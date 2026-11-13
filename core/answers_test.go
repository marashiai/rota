package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"rota/gloss/review"
)

// No test reads a pull request's notes from GitHub: one that wants them gives its own.
func TestMain(m *testing.M) {
	reviewLoader = func() (func(context.Context, review.Ref) (*review.Review, error), error) {
		return nil, errors.New("no GitHub in tests")
	}
	os.Exit(m.Run())
}

func fakeNotes(t *testing.T, threads *[]*review.Thread, loads *int) {
	t.Helper()
	old := reviewLoader
	reviewLoader = func() (func(context.Context, review.Ref) (*review.Review, error), error) {
		return func(context.Context, review.Ref) (*review.Review, error) {
			*loads++
			return &review.Review{Threads: *threads}, nil
		}, nil
	}
	t.Cleanup(func() { reviewLoader = old })
}

func human(text string, at time.Time) *review.Note {
	return &review.Note{Author: "me", Account: "me", Text: text, Created: at}
}

func byAgent(text string, at time.Time) *review.Note {
	return &review.Note{Author: "dev", Named: true, Account: "me", Text: text, Created: at}
}

// ⏳ while a note the human sent waits on an agent, 💬 once an agent answered after the human last
// opened the review; either takes the review label's place. The notes are read again only when
// GitHub says the pull request changed.
func TestTheReviewsTabSaysWhoWaitsOnWhom(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\nlabels: rota:needs-review 1D76DB 👀 Waits for the human\nreview_label: rota:needs-review\n")
	write(t, filepath.Join(root, "agents", "dev.md"), "---\nenabled: true\nrepos: o/app\n---\ndev\n")
	prs := filepath.Join(root, "prs.json")
	fakeGh(t, `case "$1 $2" in "pr list") cat `+prs+`;; "pr view") echo '{"headRefName":"dev/1","labels":[]}';; *) echo '[]';; esac`)
	var threads []*review.Thread
	loads := 0
	fakeNotes(t, &threads, &loads)
	t0 := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	list := func(updated time.Time) {
		write(t, prs, `[{"number":1,"title":"one","headRefName":"dev/1","baseRefName":"main","headRefOid":"abc","updatedAt":"`+
			updated.Format(time.RFC3339)+`","body":"","labels":[{"name":"rota:needs-review"}]}]`)
	}
	marks := func() string {
		var out []string
		for _, m := range open(t, root).ReviewQueue().PRs[0].Marks {
			out = append(out, m.Emoji)
		}
		return strings.Join(out, " ")
	}
	marked := func(updated time.Time) string {
		t.Helper()
		list(updated)
		fresh, retry := openFresh, openRetry
		openFresh, openRetry = 0, 0 // asked now; the queue then uses what it got
		open(t, root).refreshOpen()
		openFresh, openRetry = fresh, retry
		return marks()
	}

	threads = []*review.Thread{{Notes: []*review.Note{human("why?", t0), byAgent("because", t0.Add(time.Minute))}}}
	if m := marked(t0.Add(time.Minute)); m != AnsweredMark || loads != 1 || len(open(t, root).Answers()) != 1 {
		t.Fatalf("an answer from before rota first read the notes is not seen until the review is opened: %q, %d reads", m, loads)
	}
	if m := marked(t0.Add(time.Minute)); m != AnsweredMark || loads != 1 {
		t.Fatalf("unchanged on GitHub, the notes are not read again: %q, %d reads", m, loads)
	}
	open(t, root).SeenReview("o/app", 1, t0.Add(30*time.Minute))
	if m := marked(t0.Add(time.Minute)); m != "👀" {
		t.Fatalf("the human opened the review: %q", m)
	}
	threads = append(threads, &review.Thread{Notes: []*review.Note{human("rename it", t0.Add(time.Hour))}})
	if m := marked(t0.Add(time.Hour)); m != WaitsMark || loads != 2 {
		t.Fatalf("a note the human sent waits on the agent: %q, %d reads", m, loads)
	}
	threads[1].Notes = append(threads[1].Notes, byAgent("Renamed in abc.\nDetails.", t0.Add(2*time.Hour)))
	if m := marked(t0.Add(2 * time.Hour)); m != AnsweredMark {
		t.Fatalf("the agent answered: %q", m)
	}
	if a := open(t, root).Answers(); len(a) != 1 || a[0].By != "dev" || a[0].Text != "Renamed in abc. …" || a[0].Name != "app#1" || a[0].Page() != "/review/o/app/1/" {
		t.Fatalf("the answer the human has not seen: %+v", a)
	}
	if err := open(t, root).SeenReview("o/app", 1, t0.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if m := marked(t0.Add(2 * time.Hour)); m != "👀" || len(open(t, root).Answers()) != 0 {
		t.Fatalf("opening the review shows the answer seen: %q", m)
	}
	threads = append(threads,
		&review.Thread{Resolved: true, Notes: []*review.Note{human("nit", t0.Add(4*time.Hour))}},
		&review.Thread{Notes: []*review.Note{{Author: "me", Account: "me", Text: "unsent", Draft: true, Created: t0.Add(4 * time.Hour)}}},
		&review.Thread{Notes: []*review.Note{byAgent("check this line", t0.Add(4*time.Hour)), byAgent("and this", t0.Add(5*time.Hour))}})
	if m := marked(t0.Add(5 * time.Hour)); m != "👀" {
		t.Fatalf("a resolved thread, a draft and the agents' own notes wait on no one: %q", m)
	}
	threads[1].Notes = append(threads[1].Notes, human("thanks, and the test?", t0.Add(6*time.Hour)))
	threads[0].Notes = append(threads[0].Notes, human("and the docs?", t0.Add(6*time.Hour)), byAgent("done", t0.Add(7*time.Hour)))
	if m := marked(t0.Add(7 * time.Hour)); m != WaitsMark+" "+AnsweredMark {
		t.Fatalf("one note waits while another was answered: %q", m)
	}
	threads[1].Resolved = true
	open(t, root).SeenReview("o/app", 1, t0.Add(8*time.Hour))
	if m := marked(t0.Add(8 * time.Hour)); m != "👀" {
		t.Fatalf("all answered and seen: %q", m)
	}
	threads[0].Notes = append(threads[0].Notes, human("one more thing", t0.Add(9*time.Hour)))
	list(t0.Add(9 * time.Hour))
	if err := open(t, root).ReviewSent(context.Background(), "o/app", 1, 1, false); err != nil {
		t.Fatal(err)
	}
	if m := marks(); m != WaitsMark {
		t.Fatalf("sending a note shows it waits at once, not at the next listing: %q", m)
	}
}

// Only an answer that comes after the notes were first read, and after the human last opened the
// review, is news to tell them.
func TestOnlyNewAnswersAreTold(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\n")
	team := open(t, root)
	t0 := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	threads := []*review.Thread{{Notes: []*review.Note{human("why?", t0), byAgent("because", t0.Add(time.Minute))}}}
	load := func(context.Context, review.Ref) (*review.Review, error) {
		return &review.Review{Threads: threads}, nil
	}
	read := func(old map[int]openItem, updated time.Time) (map[int]openItem, []Answer) {
		items := map[int]openItem{1: {Title: "one", PR: true, Updated: updated}, 2: {Title: "an issue"}}
		return items, team.readTalk("o/app", items, old, load)
	}
	items, news := read(nil, t0.Add(time.Minute))
	if len(news) != 0 || !items[1].Talk.Read.Equal(t0.Add(time.Minute)) || len(team.seenReviews()) != 0 {
		t.Fatalf("a first read tells nothing, and sees nothing for the human: %v %+v", news, items[1].Talk)
	}
	threads[0].Notes = append(threads[0].Notes, human("and?", t0.Add(time.Hour)), byAgent("and this", t0.Add(2*time.Hour)))
	items, news = read(items, t0.Add(2*time.Hour))
	if len(news) != 1 || news[0].By != "dev" || news[0].Text != "and this" || news[0].Ref() != "o/app#1" {
		t.Fatalf("a new answer is news: %+v", news)
	}
	if _, news = read(items, t0.Add(2*time.Hour)); len(news) != 0 {
		t.Fatalf("read again unchanged, it is not told twice: %+v", news)
	}
	team.SeenReview("o/app", 1, t0.Add(4*time.Hour))
	threads[0].Notes = append(threads[0].Notes, human("ok?", t0.Add(3*time.Hour)), byAgent("ok", t0.Add(3*time.Hour+time.Minute)))
	if _, news = read(items, t0.Add(3*time.Hour)); len(news) != 0 {
		t.Fatalf("an answer the human already saw is not told: %+v", news)
	}
	failing := func(context.Context, review.Ref) (*review.Review, error) { return nil, errors.New("down") }
	kept := map[int]openItem{1: {Title: "one", PR: true, Updated: t0.Add(9 * time.Hour)}}
	if team.readTalk("o/app", kept, items, failing); !kept[1].Talk.Read.Equal(items[1].Talk.Read) || kept[1].Talk.Answered.IsZero() {
		t.Fatalf("a failed read keeps where the notes last stood: %+v", kept[1].Talk)
	}
}

// The talk on a pull request is kept for the mail: the human's notes, where they are, the agents'
// answers with the note each answers, and the approval; notes sent together read as one message.
func TestATalkIsKeptForTheMail(t *testing.T) {
	t0 := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	rv := &review.Review{Viewer: "me", Approval: &review.Approval{By: "me", At: t0.Add(2 * time.Hour)}, Threads: []*review.Thread{
		{Path: "app/play.ts", Line: 42, Notes: []*review.Note{human("Why a frame?", t0), byAgent("So a game's code cannot reach Studio.", t0.Add(time.Hour))}},
		{Notes: []*review.Note{human("Fold #676 in, please.", t0.Add(time.Minute)), byAgent("Done.", t0.Add(time.Hour+time.Minute))}},
		{Notes: []*review.Note{{Author: "me", Account: "me", Text: "a draft", Created: t0, Draft: true}}},
	}}
	k := talkOf(rv)
	if len(k.Notes) != 4 || k.Notes[0].Where != "app/play.ts:42" || k.Notes[0].By != "" || k.Notes[2].By != "dev" || k.Notes[2].Re != "Why a frame?" || !k.Approved.Equal(t0.Add(2*time.Hour)) {
		t.Fatalf("the talk: %+v", k)
	}
	msgs := talkMessages(k, "dev")
	if len(msgs) != 3 || msgs[0].Text != "Sent 2 notes." || len(msgs[0].Quotes) != 2 || msgs[0].Quotes[1].Note != "Fold #676 in, please." ||
		msgs[1].From != "dev" || msgs[1].Text != "Answered 2 notes." || msgs[1].Quotes[0].Answer != "So a game's code cannot reach Studio." || msgs[1].Quotes[0].Note != "Why a frame?" ||
		msgs[2].Text != "Approved." || msgs[2].From != "you" {
		t.Fatalf("the talk as messages: %+v", msgs)
	}
	other := &review.Review{Viewer: "me", Approval: &review.Approval{By: "colleague", At: t0}, Threads: []*review.Thread{
		{Notes: []*review.Note{{Author: "colleague", Account: "colleague", Text: "Looks odd.", Created: t0}}}}}
	ko := talkOf(other)
	if len(ko.Notes) != 1 || ko.Notes[0].By != "colleague" || !ko.Notes[0].Person || ko.ApprovedBy != "colleague" {
		t.Fatalf("another person's note and approval are theirs: %+v", ko)
	}
	if ms := talkMessages(ko, "dev"); ms[0].From != "colleague" || ms[1].From != "colleague" {
		t.Fatalf("as messages: %+v", ms)
	}
	long := strings.Repeat("é", noteChars)
	if got := cut(long, noteChars); len(got) > noteChars+len("…") || !strings.HasSuffix(got, "…") || !utf8.ValidString(got) {
		t.Fatalf("a long note is cut on a character: %q", got)
	}
}

// fakeReviewer is GitHub as gloss sees it: a draft is there once the review is read again, Send
// sends the drafts in the review it is given.
type fakeReviewer struct {
	calls   []string
	drafts  []string
	sendErr error
}

func (f *fakeReviewer) Load(_ context.Context, ref review.Ref) (*review.Review, error) {
	f.calls = append(f.calls, "load "+ref.Owner+"/"+ref.Name)
	rv := &review.Review{State: "OPEN"}
	for _, d := range f.drafts {
		rv.Threads = append(rv.Threads, &review.Thread{Notes: []*review.Note{{Text: d, Draft: true}}})
	}
	return rv, nil
}
func (f *fakeReviewer) Approve(context.Context, *review.Review) (int, error) {
	f.calls = append(f.calls, "approve")
	f.drafts = nil
	return 0, nil
}
func (f *fakeReviewer) AddDraft(_ context.Context, _ *review.Review, in review.NoteInput) (string, error) {
	f.calls = append(f.calls, "draft "+in.Text)
	f.drafts = append(f.drafts, in.Text)
	return "n1", nil
}
func (f *fakeReviewer) Send(_ context.Context, rv *review.Review) (int, error) {
	f.calls = append(f.calls, "send")
	if f.sendErr != nil {
		return 0, f.sendErr
	}
	f.drafts = nil
	return rv.Drafts(), nil
}

// From the mail the human approves a pull request, or sends a note on its whole change, the way
// gloss does: the agents are told as when they do it there. A note is sent once, also after a try
// that failed; and once GitHub has it, a failure to tell the agent is a warning, not a failure.
func TestTheMailApprovesAndNotesAsGlossDoes(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rota.conf"), "usage: off\nlisten: off\n")
	write(t, filepath.Join(root, "agents", "dev.md"), "---\nenabled: true\nrepos: o/app\ntools: Bash(rota merge *)\n---\ndev\n")
	log := filepath.Join(root, "gh.log")
	fakeGh(t, `echo "$*" >> `+log+`; case "$1 $2 $3" in "pr view 9") echo '{"headRefName":"someone/9","labels":[]}';; "pr view"*) echo '{"headRefName":"dev/1","labels":[]}';; *) echo '[]';; esac`)
	f := &fakeReviewer{}
	old := reviewer
	reviewer = func() (reviewing, error) { return f, nil }
	t.Cleanup(func() { reviewer = old })
	team := open(t, root)
	if warn, err := team.ApprovePR(context.Background(), "o/app", 1); err != nil || warn != "" {
		t.Fatal(warn, err)
	}
	if b, _ := os.ReadFile(log); !strings.Contains(string(b), "--add-label "+ApprovedLabel) || strings.Join(f.calls, ",") != "load o/app,approve" {
		t.Fatalf("approved as gloss does: %v\n%s", f.calls, b)
	}
	if !strings.Contains(inboxText(team, "dev"), "I approved this pull request") || team.seenReviews()["o/app#1"].IsZero() {
		t.Fatalf("the agent that merges is told, and what the human approved they saw: %s", inboxText(team, "dev"))
	}
	f.calls = nil
	if warn, err := team.NotePR(context.Background(), "o/app", 1, "Rebase it, please."); err != nil || warn != "" {
		t.Fatal(warn, err)
	}
	if strings.Join(f.calls, ",") != "load o/app,draft Rebase it, please.,load o/app,send" || !strings.Contains(inboxText(team, "dev"), "I sent 1 review note") {
		t.Fatalf("a note on the whole change, read again with it and sent as a review: %v\n%s", f.calls, inboxText(team, "dev"))
	}
	f.calls, f.sendErr = nil, errors.New("GitHub is down")
	if _, err := team.NotePR(context.Background(), "o/app", 1, "Again."); err == nil {
		t.Fatal("a send that failed says so")
	}
	f.calls, f.sendErr = nil, nil
	if _, err := team.NotePR(context.Background(), "o/app", 1, "Again."); err != nil || strings.Count(strings.Join(f.calls, ","), "draft") != 0 {
		t.Fatalf("the draft the failed try left is sent, not added again: %v %v", f.calls, err)
	}
	warn, err := team.NotePR(context.Background(), "o/app", 9, "Whose is this?")
	if err != nil || !strings.Contains(warn, "no agent of the team wrote o/app#9") {
		t.Fatalf("sent, but no agent to tell: a warning: %q %v", warn, err)
	}
	if _, err := team.NotePR(context.Background(), "o/app", 1, "  "); err == nil {
		t.Fatal("an empty note is refused")
	}
	if _, err := team.ApprovePR(context.Background(), "o/elsewhere", 1); err == nil {
		t.Fatal("a repository not the team's is refused")
	}
}

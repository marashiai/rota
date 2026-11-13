package review

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLive runs the reviewer's and an agent's whole round on a real pull request with at least
// three commits, where an agent already posted notes (gloss post). It writes to that pull request.
//
//	GLOSS_LIVE_PR=owner/name#N go test ./review -run Live -v
func TestLive(t *testing.T) {
	pr := os.Getenv("GLOSS_LIVE_PR")
	if pr == "" {
		t.Skip("set GLOSS_LIVE_PR to a throwaway pull request")
	}
	ref, err := ParseRef(pr, "")
	if err != nil {
		t.Fatal(err)
	}
	token, err := Token()
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient(token)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	load := func() *Review {
		t.Helper()
		rv, err := c.Load(ctx, ref)
		if err != nil {
			t.Fatal(err)
		}
		return rv
	}
	rv := load()
	if rv.Drafts() != 0 {
		t.Fatalf("start without drafts: %d", rv.Drafts())
	}
	c1, c2, c3 := rv.Commits[0].SHA, rv.Commits[1].SHA, rv.Commits[2].SHA
	var agentLine, agentWhole *Thread
	for _, th := range rv.Threads {
		if th.ghThread != "" && th.Notes[0].Named && agentLine == nil {
			agentLine = th
		}
		if th.Commit == "" && th.Notes[0].Named && agentWhole == nil {
			agentWhole = th
		}
	}
	if agentLine == nil || agentWhole == nil {
		t.Fatal("needs an agent's line note and whole-change note (gloss post)")
	}

	must := func(id string, err error) string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	stamp := time.Now().Format("15:04:05")
	older := must(c.AddDraft(ctx, rv, NoteInput{Commit: "1", Path: "sample/calc.go", Line: 4, Text: "draft on an older commit " + stamp}))
	whole := must(c.AddDraft(ctx, rv, NoteInput{Text: "draft on the whole change " + stamp}))
	must(c.AddDraft(ctx, rv, NoteInput{Commit: "2", Text: "draft on commit 2 " + stamp}))
	must(c.AddDraft(ctx, rv, NoteInput{Commit: "3", Path: "sample/neg.go", Text: "draft on a file " + stamp}))
	must(c.DraftReply(ctx, rv, agentLine.ID, "draft reply in a thread "+stamp))
	must(c.DraftReply(ctx, rv, agentWhole.ID, "draft reply to the whole change "+stamp))

	rv = load()
	if rv.Drafts() != 6 {
		t.Fatalf("6 drafts, got %d", rv.Drafts())
	}
	th, n := rv.Thread(older)
	if th == nil || th.Commit != c1 || th.Path != "sample/calc.go" || th.Line != 4 || !n.Draft || !rv.Mine(n) {
		t.Fatalf("older draft: %+v %+v", th, n)
	}
	if th, _ := rv.Thread(agentLine.ID); th.Last().Text != "draft reply in a thread "+stamp || !th.Last().Draft {
		t.Fatalf("thread reply: %+v", th.Last())
	}
	if th, _ := rv.Thread(agentWhole.ID); th.Last().Text != "draft reply to the whole change "+stamp {
		t.Fatalf("whole-change reply: %+v", th.Last())
	}
	found := map[string]bool{}
	for _, th := range rv.Threads {
		switch {
		case th.Commit == c2 && th.Path == "" && th.Notes[0].Draft:
			found["commit"] = true
		case th.Commit == c3 && th.Path == "sample/neg.go" && th.Line == 0 && th.Notes[0].Draft:
			found["file"] = true
		}
	}
	if !found["commit"] || !found["file"] {
		t.Fatalf("commit and file drafts: %v", found)
	}

	// an agent on the same account, while the reviewer has drafts
	reply := must(c.Reply(ctx, rv, "developer", agentLine.ID, "agent reply while drafts are open "+stamp))
	ids, err := c.Post(ctx, rv, "developer", []NoteInput{{Commit: "3", Path: "sample/calc.go", Line: 3, Text: "agent note while drafts are open " + stamp}})
	if err != nil || len(ids) != 1 {
		t.Fatal(ids, err)
	}
	rv = load()
	if th, n := rv.Thread(reply); th == nil || th.ID != agentLine.ID || !n.Named || n.Draft {
		t.Fatalf("agent reply belongs in its thread: %+v %+v", th, n)
	}
	if th, n := rv.Thread(ids[0]); th == nil || th.Commit != c3 || th.Line != 3 || n.Author != "developer" {
		t.Fatalf("agent note keeps its place: %+v %+v", th, n)
	}

	// edit and delete drafts
	if err := c.EditDraft(ctx, rv, older, "edited "+stamp); err != nil {
		t.Fatal(err)
	}
	if err := c.EditDraft(ctx, rv, whole, "edited whole "+stamp); err != nil {
		t.Fatal(err)
	}
	rv = load()
	if th, n := rv.Thread(older); n == nil || n.Text != "edited "+stamp || th.Commit != c1 || th.Line != 4 {
		t.Fatalf("edited draft keeps its place: %+v %+v", th, n)
	}
	var wholeNow *Note
	for _, th := range rv.Threads {
		for _, n := range th.Notes {
			if n.Draft && strings.HasPrefix(n.Text, "edited whole") {
				wholeNow = n
			}
		}
	}
	if wholeNow == nil {
		t.Fatal("edited section is gone")
	}
	if err := c.DeleteDraft(ctx, rv, wholeNow.ID); err != nil {
		t.Fatal(err)
	}
	rv = load()
	if rv.Drafts() != 5 {
		t.Fatalf("5 drafts after one delete, got %d", rv.Drafts())
	}

	sent, err := c.Send(ctx, rv)
	if err != nil || sent != 5 {
		t.Fatal(sent, err)
	}

	// deleting the last draft drops the pending review, so an agent can write in threads again
	lone := must(c.AddDraft(ctx, load(), NoteInput{Text: "lone draft " + stamp}))
	rv = load()
	if err := c.DeleteDraft(ctx, rv, lone); err != nil {
		t.Fatal(err)
	}
	if rv = load(); rv.pending != "" {
		t.Fatalf("the pending review is gone with its last draft: %s", rv.pending)
	}
	rv = load()
	if rv.Drafts() != 0 {
		t.Fatalf("no drafts after send, got %d", rv.Drafts())
	}
	if th, n := rv.Thread(older); n == nil || n.Draft || th.Commit != c1 || th.Line != 4 {
		t.Fatalf("sent note keeps its place: %+v %+v", th, n)
	}
}

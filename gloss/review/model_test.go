package review

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// An agent answers what a person sent it in a review, not other agents' notes or the PR
// comments that bots and tools on the shared account write.
func TestWhatAsksAgents(t *testing.T) {
	for _, c := range []struct {
		name string
		n    Note
		want bool
	}{
		{"a sent note on a line", Note{kind: inComment}, true},
		{"a sent note on the whole change", Note{kind: inSection}, true},
		{"a draft", Note{kind: inSection, Draft: true}, false},
		{"an agent's note", Note{kind: inComment, Named: true}, false},
		{"an agent's PR comment", Note{kind: inIssue, Named: true}, false},
		{"a bot's verdict on the shared account", Note{kind: inIssue}, false},
	} {
		if got := c.n.AsksAgents(); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// A person approves the pull request; their own later review that does not approve takes it
// back, and agents' reviews on the shared account count for nothing. Commits pushed since do not.
func TestTheLatestPersonsReviewDecidesTheApproval(t *testing.T) {
	at := func(m int) time.Time { return time.Date(2026, 11, 1, 10, m, 0, 0, time.UTC) }
	ok := func(by string) sentReview { return sentReview{by: by, state: "COMMENTED", approve: true, person: true} }
	for _, c := range []struct {
		name string
		sent []sentReview
		want string // who approved it, "" for nobody
	}{
		{"nothing sent", nil, ""},
		{"approved", []sentReview{ok("me")}, "me"},
		{"an agent's review after it", []sentReview{ok("me"), {state: "COMMENTED"}}, "me"},
		{"the person's notes after it", []sentReview{ok("me"), {state: "COMMENTED", person: true}}, ""},
		{"approved again after notes", []sentReview{{state: "COMMENTED", person: true}, ok("you")}, "you"},
		{"changes requested on github.com", []sentReview{ok("me"), {state: "CHANGES_REQUESTED"}}, ""},
		{"approved on github.com", []sentReview{{by: "them", state: "APPROVED", approve: true}}, "them"},
		{"dismissed", []sentReview{ok("me"), {state: "DISMISSED"}}, ""},
		{"an agent's review GitHub calls approving", []sentReview{{state: "APPROVED", approve: true, named: true}}, ""},
		{"an agent's request for changes", []sentReview{ok("me"), {state: "CHANGES_REQUESTED", named: true}}, "me"},
		{"a request for changes that holds an approval", []sentReview{ok("me"), {state: "CHANGES_REQUESTED", approve: true, person: true}}, ""},
		{"a person's and an agent's notes in one review", []sentReview{ok("me"), {state: "COMMENTED", person: true, named: true}}, ""},
	} {
		for i := range c.sent {
			c.sent[i].at = at(i)
		}
		slices := append([]sentReview(nil), c.sent...)
		for i, j := 0, len(slices)-1; i < j; i, j = i+1, j-1 { // the order GitHub lists them in does not matter
			slices[i], slices[j] = slices[j], slices[i]
		}
		got := ""
		if a := approval(slices); a != nil {
			got = a.By
		}
		if got != c.want {
			t.Errorf("%s: approved by %q, want %q", c.name, got, c.want)
		}
	}
	if rv := (&Review{Head: "bbb", Approval: &Approval{By: "me"}}); !rv.Approved() {
		t.Error("an approval is of the pull request, whatever its head")
	}
}

// An approval is a section of a review's body that is no note, and a plain Send drops one an
// Approve that failed left behind.
func TestAnApprovalIsNoNote(t *testing.T) {
	body := joinSections([]section{{Meta: Meta{Approval: true}, Text: "Approved."}, {Text: "a note"}})
	ss := splitSections(body)
	if len(ss) != 2 || !ss[0].Meta.Approval || !strings.Contains(body, `"approval":true`) {
		t.Fatalf("the approval survives the body: %q", body)
	}
	if w := withoutApproval(ss); len(w) != 1 || w[0].Text != "a note" {
		t.Fatalf("withoutApproval keeps the notes only: %+v", w)
	}
}

// Only those GitHub lets approve may: anyone who may comment could write gloss's marker.
func TestWhoMayApprove(t *testing.T) {
	for assoc, want := range map[string]bool{"OWNER": true, "MEMBER": true, "COLLABORATOR": true, "CONTRIBUTOR": false, "FIRST_TIME_CONTRIBUTOR": false, "NONE": false, "": false} {
		if mayApprove(assoc) != want {
			t.Errorf("%q may approve: %v, want %v", assoc, !want, want)
		}
	}
}

// Notes sent after an approval take it back, in the review in hand too: a hook told of the send
// must not read it as an approval.
func TestSentNotesTakeTheApprovalBack(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":{"node":{"body":""}}}`))
	}))
	defer gh.Close()
	c := NewClient("x")
	c.API = gh.URL
	rv := &Review{PR: "o/r#1", Approval: &Approval{By: "me"}, pending: "R1",
		Threads: []*Thread{{Notes: []*Note{{ID: "n1", Draft: true, Text: "a note"}}}}}
	if n, err := c.Send(context.Background(), rv); err != nil || n != 1 {
		t.Fatalf("send: %d %v", n, err)
	}
	if rv.Approved() {
		t.Fatal("the approval stays after notes that do not approve")
	}
}

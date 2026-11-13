package review

import (
	"context"
	"testing"
)

// The whole change shows every note with a path where GitHub tracks it to in the diff from the
// base to the head, whichever commit it was written on; notes on a whole commit stay in the
// commit, and what the diff cannot show goes on the file, or at the top.
func TestTheWholeChangePlacesEveryNoteWhereItIsNow(t *testing.T) {
	const c1, c2 = "1111111111111111111111111111111111111111", "2222222222222222222222222222222222222222"
	rv := &Review{Head: c2, Commits: []Commit{{SHA: c1}, {SHA: c2}}}
	files := ParseDiff("diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,4 +1,4 @@\n a\n+b\n-c\n d\n e\n")
	th := func(id string, t Thread) *Thread { t.ID = id; t.Notes = []*Note{{ID: id}}; return &t }
	rv.Threads = []*Thread{
		th("whole", Thread{}),
		th("tracked", Thread{Commit: c1, Path: "a.go", Line: 7, HeadLine: 2}),                  // written on commit 1, GitHub tracks it to line 2 now
		th("outdated", Thread{Commit: c1, Path: "a.go", Line: 9, HeadLine: 0, Outdated: true}), // its line changed since
		th("mine", Thread{Path: "a.go", Line: 1, Old: true, Head: c2, HeadLine: 0}),            // a section on the whole change, written at this head
		th("stale", Thread{Path: "a.go", Line: 2, Head: c1}),                                   // a section written at an earlier head
		th("head", Thread{Commit: c2, Path: "a.go", Line: 4}),                                  // a PR comment on the head's new side: exact
		th("headold", Thread{Commit: c2, Path: "a.go", Line: 1, Old: true}),                    // the head's old side is not the base's
		th("onfile", Thread{Commit: c1, Path: "a.go"}),
		th("gone", Thread{Commit: c1, Path: "gone.go", Line: 3, HeadLine: 3}),
		th("oncommit", Thread{Commit: c1}),
		th("lost", Thread{Commit: "3333333333333333333333333333333333333333"}),
	}
	p := rv.PlaceWhole(files)
	ids := func(ts []*Thread) (out []string) {
		for _, t := range ts {
			out = append(out, t.ID)
		}
		return out
	}
	want := map[string][]string{
		"whole": {"whole", "gone", "lost"},
		"file":  {"outdated", "stale", "headold", "onfile"},
		"+2":    {"tracked"},
		"-1":    {"mine"},
		"+4":    {"head"},
	}
	got := map[string][]string{
		"whole": ids(p.Whole), "file": ids(p.Files["a.go"]),
		"+2": ids(p.Lines[LineKey{"a.go", 2, false}]), "-1": ids(p.Lines[LineKey{"a.go", 1, true}]), "+4": ids(p.Lines[LineKey{"a.go", 4, false}]),
	}
	for k, w := range want {
		if g := got[k]; len(g) != len(w) || func() bool {
			for i := range g {
				if g[i] != w[i] {
					return true
				}
			}
			return false
		}() {
			t.Errorf("%s: %v, want %v", k, g, w)
		}
	}
	if p.Count != 10 {
		t.Errorf("count %d, want 10: the note on a commit still here stays in it", p.Count)
	}
	if n := p.OnFile("a.go"); n != 7 {
		t.Errorf("a.go has %d notes, want 7", n)
	}
	if n := rv.WholeThreads(); n != 3 {
		t.Errorf("%d notes on the whole change, want 3", n)
	}
	if o := ids(rv.Overall()); len(o) != 2 || o[0] != "whole" || o[1] != "lost" {
		t.Errorf("overall %v, want the whole change's own note and the one on a commit that is gone", o)
	}
	// a commit's own view places only its own notes, by their own lines
	cp := rv.Place(0, files)
	if ids(cp.Lines[LineKey{"a.go", 7, false}]) != nil || len(cp.Files["a.go"]) != 3 || len(cp.Whole) != 2 {
		t.Errorf("commit 1: lines %v files %v whole %v", cp.Lines, ids(cp.Files["a.go"]), ids(cp.Whole))
	}
}

// A note on a deleted line of the whole change takes that line's position in the diff from the
// base to the head, as a note on an added line does.
func TestAWholeChangeNoteOnADeletedLineTakesItsPosition(t *testing.T) {
	rv := &Review{Ref: Ref{Owner: "o", Name: "r", Number: 1}, Base: "bbbbbbb", Head: "hhhhhhh"}
	c := NewClient("x")
	c.diffs = map[string][]File{rv.Ref.Repo() + "@" + rv.Base + "..." + rv.Head: ParseDiff(
		"diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,3 +1,3 @@\n a\n-b\n+c\n d\n")}
	for _, tc := range []struct {
		a   anchor
		pos int
	}{
		{anchor{Path: "a.go", Line: 2, Old: true}, 2},  // -b
		{anchor{Path: "a.go", Line: 2, Old: false}, 3}, // +c
		{anchor{Path: "a.go", Line: 3, Old: true}, 4},  // d, on either side
	} {
		pos, ok, err := c.position(context.Background(), rv, tc.a)
		if err != nil || !ok || pos != tc.pos {
			t.Errorf("%+v: position %d ok %v err %v, want %d", tc.a, pos, ok, err, tc.pos)
		}
	}
}

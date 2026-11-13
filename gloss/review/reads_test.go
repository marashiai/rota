package review

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestANoteIsReadUpToItsThreadsMarkOrAll(t *testing.T) {
	t0 := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	rv := &Review{Viewer: "me", Threads: []*Thread{
		{ID: "a", Notes: []*Note{{ID: "a", Account: "me", Author: "triage", Named: true, Created: at(1)}, {ID: "a2", Account: "me", Created: at(2)}, {ID: "a3", Account: "me", Author: "dev", Named: true, Created: at(5)}}},
		{ID: "b", Notes: []*Note{{ID: "b", Account: "me", Author: "maintainer", Named: true, Created: at(3)}}},
		{ID: "c", Notes: []*Note{{ID: "c", Account: "me", Created: at(4)}, {ID: "c2", Account: "me", Draft: true, Created: at(6)}}},
	}}
	a, b, c := rv.Threads[0], rv.Threads[1], rv.Threads[2]
	var r Read
	if r.Has(rv, a, a.Notes[0]) || !r.Has(rv, a, a.Notes[1]) || r.Folded(rv, b) {
		t.Fatal("with nothing read, an agent's note is new and the reviewer's own is not")
	}
	if r.Folded(rv, c) {
		t.Fatal("a thread with a draft folded")
	}
	if n := r.New(rv, rv.Threads); n != 3 {
		t.Fatalf("new notes: %d, want 3", n)
	}
	r.Mark(a, at(2))
	if !r.Has(rv, a, a.Notes[0]) || r.Has(rv, a, a.Notes[2]) || r.Folded(rv, a) {
		t.Fatal("a reply written after the mark is new, and keeps its thread open")
	}
	r.Mark(a, at(1)) // an older mark takes nothing back
	r.All = at(5)
	if !r.Folded(rv, a) || !r.Folded(rv, b) || r.Folded(rv, c) {
		t.Fatal("All reads every note written by then; a draft still keeps its thread open")
	}
	if r.Threads["a"] != at(2) {
		t.Fatalf("an older mark replaced a newer one: %v", r.Threads["a"])
	}

	pc := &Thread{ID: "p", Notes: []*Note{{ID: "p", Account: "me", Created: at(1), PRComment: true}}}
	if (Read{}).Has(rv, pc, pc.Notes[0]) {
		t.Error("a PR comment from the reviewer's account counts as theirs: an agent may have written it")
	}

	var all Read
	all.MarkAll(rv)
	if !all.All.Equal(at(5)) {
		t.Fatalf("MarkAll: %v, want the latest sent note's time, not the draft's", all.All)
	}
}

func TestReadsKeepWhatWasReadInAFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gloss", "read.json")
	s := NewReads(path)
	if r, err := s.Get("o/r#1"); err != nil || !r.All.IsZero() || len(r.Threads) != 0 {
		t.Fatalf("before any mark: %+v %v", r, err)
	}
	t1 := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	if err := s.Mark("o/r#1", Read{Threads: map[string]time.Time{"a": t1}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Mark("o/r#1", Read{All: t1.Add(-time.Hour), Threads: map[string]time.Time{"b": t1, "a": t1.Add(-time.Minute)}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Mark("o/r#2", Read{All: t1}); err != nil {
		t.Fatal(err)
	}
	r, err := NewReads(path).Get("o/r#1") // as another program reads it
	if err != nil || !r.Threads["a"].Equal(t1) || !r.Threads["b"].Equal(t1) || !r.All.Equal(t1.Add(-time.Hour)) {
		t.Fatalf("kept: %+v %v", r, err)
	}
	if r2, _ := s.Get("o/r#2"); !r2.All.Equal(t1) || len(r2.Threads) != 0 {
		t.Fatalf("another pull request's: %+v", r2)
	}

	// a mark that adds nothing leaves the file alone
	info, _ := os.Stat(path)
	old := time.Now().Add(-time.Hour)
	os.Chtimes(path, old, old)
	if err := s.Mark("o/r#1", Read{Threads: map[string]time.Time{"a": t1}}); err != nil {
		t.Fatal(err)
	}
	if info2, _ := os.Stat(path); !info2.ModTime().Equal(old) {
		t.Errorf("a mark that adds nothing wrote the file (was %v)", info.ModTime())
	}

	// a pull request not marked for long is forgotten at the next write
	all, _ := s.all()
	stale := all["o/r#2"]
	stale.At = time.Now().Add(-forget - time.Hour)
	all["o/r#2"] = stale
	writeAll(t, path, all)
	if err := s.Mark("o/r#3", Read{All: t1}); err != nil {
		t.Fatal(err)
	}
	if r2, _ := s.Get("o/r#2"); !r2.All.IsZero() {
		t.Errorf("a stale pull request's marks stayed: %+v", r2)
	}

	// a file it cannot read is started again
	os.WriteFile(path, []byte("{not json"), 0o600)
	if _, err := s.Get("o/r#1"); err == nil {
		t.Error("a broken file reads without an error")
	}
	if err := s.Mark("o/r#1", Read{All: t1}); err != nil {
		t.Fatal(err)
	}
	if r, err := s.Get("o/r#1"); err != nil || !r.All.Equal(t1) {
		t.Errorf("after a broken file: %+v %v", r, err)
	}
	if b, err := os.ReadFile(path + ".broken"); err != nil || string(b) != "{not json" {
		t.Errorf("the broken file was not kept beside: %q %v", b, err)
	}

	var none *Reads // keeps nothing
	if err := none.Mark("o/r#1", Read{All: t1}); err != nil {
		t.Fatal(err)
	}
	if r, err := none.Get("o/r#1"); err != nil || !r.All.IsZero() {
		t.Fatalf("a nil Reads: %+v %v", r, err)
	}
}

func writeAll(t *testing.T, path string, all map[string]Read) {
	t.Helper()
	b, err := json.Marshal(all)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

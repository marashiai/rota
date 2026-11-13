//go:build darwin || freebsd || netbsd || openbsd || dragonfly || linux

package tui

import (
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"rota/gloss/review"
)

func TestParseKeys(t *testing.T) {
	keys, rest := parseKeys([]byte("j\x1b[A\x04é\x1b[200~two\nlines\x1b[201~\x1b[<64;3;4M\x1b[<0;3;4M\r\x7f\x1b"))
	var got []string
	for _, k := range keys {
		switch {
		case k.Name == "paste":
			got = append(got, "paste:"+k.Text)
		case k.Name != "":
			got = append(got, k.Name)
		default:
			got = append(got, string(k.Rune))
		}
	}
	want := []string{"j", "up", "ctrl+d", "é", "paste:two\nlines", "wheelup", "enter", "backspace", "esc"}
	if !reflect.DeepEqual(got, want) || len(rest) != 0 {
		t.Fatalf("got %q rest %q", got, rest)
	}
	if keys, rest := parseKeys([]byte("\x1b[200~unfinished")); len(keys) != 0 || string(rest) != "\x1b[200~unfinished" {
		t.Fatalf("an unfinished paste waits: %v %q", keys, rest)
	}
	if keys, rest := parseKeys([]byte{0xc3}); len(keys) != 0 || len(rest) != 1 {
		t.Fatalf("half a rune waits: %v %q", keys, rest)
	}
}

func TestEditorLines(t *testing.T) {
	ed := &editor{text: []rune("abcdefgh\nij")}
	starts := ed.lines(5)
	if !reflect.DeepEqual(starts, []int{0, 5, 9}) {
		t.Fatalf("starts %v", starts)
	}
	if ed.lineText(starts, 0) != "abcde" || ed.lineText(starts, 1) != "fgh" || ed.lineText(starts, 2) != "ij" {
		t.Fatal("line text")
	}
	ed.pos = 7 // the g, on the second line
	if r, c := ed.cursor(starts); r != 1 || c != 2 {
		t.Fatalf("cursor %d %d", r, c)
	}
	ed.vertical(1, 5)
	if ed.pos != 11 { // "ij" has two runes: the end of it
		t.Fatalf("down to %d", ed.pos)
	}
	ed.vertical(-1, 5)
	if ed.pos != 7 {
		t.Fatalf("up to %d", ed.pos)
	}
	ed.pos = ed.lineEnd()
	ed.insert("X")
	if string(ed.text) != "abcdefghX\nij" {
		t.Fatalf("insert: %q", string(ed.text))
	}
}

func TestWrap(t *testing.T) {
	lines := wrap("one two three four five six seven", 12)
	for _, l := range lines {
		if len([]rune(l)) > 12 {
			t.Fatalf("too long: %q", l)
		}
	}
	if strings.Join(lines, " ") != "one two three four five six seven" {
		t.Fatalf("lost words: %q", lines)
	}
}

// The whole change and a commit lay out the same way, and the choosers list the views and files.
func TestTheWholeChangeAndACommitLayOutAlike(t *testing.T) {
	const c1, c2 = "1111111111111111111111111111111111111111", "2222222222222222222222222222222222222222"
	rv := &review.Review{Title: "T", PR: "o/r#1", State: "OPEN", Viewer: "me", Base: "0000000", Head: c2,
		Commits: []review.Commit{{SHA: c1, Title: "one"}, {SHA: c2, Title: "two"}},
		Threads: []*review.Thread{
			{ID: "w", Notes: []*review.Note{{ID: "w", Author: "dev", Text: "check this"}}},
			{ID: "l", Commit: c1, Path: "a.go", Line: 2, HeadLine: 2, Notes: []*review.Note{{ID: "l", Author: "dev", Text: "on a line"}}},
			{ID: "c", Commit: c1, Notes: []*review.Note{{ID: "c", Author: "dev", Text: "what it does"}}},
		}}
	files := review.ParseDiff("diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,2 +1,3 @@\n a\n+b\n c\n")
	m := &model{rv: rv, w: 80, h: 24, files: map[string][]review.File{}, loading: map[string]bool{}}
	m.files[m.diffKey(0)] = files
	m.files[c1] = files
	text := func() string {
		var sb strings.Builder
		for _, r := range m.rows {
			for _, s := range r.segs {
				sb.WriteString(s.text)
			}
			sb.WriteString("\n")
		}
		return sb.String()
	}
	m.layout(false)
	for _, want := range []string{"NOTES ON THE WHOLE CHANGE", "check this", "── a.go", "in commit 1", "on a line"} {
		if !strings.Contains(text(), want) {
			t.Errorf("the whole change lacks %q:\n%s", want, text())
		}
	}
	if strings.Contains(text(), "what it does") {
		t.Errorf("the whole change shows a note on a whole commit:\n%s", text())
	}
	m.go_(1)
	for _, want := range []string{"NOTES ON THIS COMMIT", "what it does", "── a.go", "on a line"} {
		if !strings.Contains(text(), want) {
			t.Errorf("commit 1 lacks %q:\n%s", want, text())
		}
	}
	if strings.Contains(text(), "in commit 1") {
		t.Errorf("a commit's own view labels its own notes:\n%s", text())
	}
	m.pickView()
	if len(m.pick.items) != 3 || m.pick.cur != 1 {
		t.Fatalf("the views: %d items, cursor %d", len(m.pick.items), m.pick.cur)
	}
	m.pickKey(key{Rune: 'k'})
	m.pickKey(key{Name: "enter"})
	if m.pick != nil || m.view != 0 {
		t.Fatalf("picking the whole change: view %d", m.view)
	}
	m.pickFile()
	if len(m.pick.items) != 1 {
		t.Fatalf("the files: %d items", len(m.pick.items))
	}
	m.pickKey(key{Name: "enter"})
	if it, ok := m.item(); !ok || it.kind != "file" || it.path != "a.go" {
		t.Fatalf("picking a file put the cursor on %+v", it)
	}
}

func TestCodeKeepsTheLineColourAroundItsTokens(t *testing.T) {
	segs := code(sAdd, "\tif x // c", []review.Span{{Kind: "k", Start: 1, Len: 2}, {Kind: "c", Start: 6, Len: 4}})
	want := []seg{{sAdd, "    "}, {sAdd + sBold, "if"}, {sAdd, " x "}, {sAdd + sDim, "// c"}}
	if !reflect.DeepEqual(segs, want) {
		t.Errorf("got %q", segs)
	}
	if segs := code(sDel, "plain", nil); !reflect.DeepEqual(segs, []seg{{sDel, "plain"}}) {
		t.Errorf("without spans: %q", segs)
	}
}

// Notes the reviewer has read fold: one line each, and the read ones on the whole change one line
// together; } passes over them, z folds and opens, and what was on the screen is kept as read,
// to fold the next time the review opens.
func TestReadNotesFold(t *testing.T) {
	t0 := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	const c1 = "1111111111111111111111111111111111111111"
	note := func(id, by string, m int, text string) *review.Note {
		return &review.Note{ID: id, Author: by, Named: by != "me", Account: "me", Created: at(m), Text: text}
	}
	rv := &review.Review{Title: "T", PR: "o/r#1", State: "OPEN", Viewer: "me", Base: "0000000", Head: c1,
		Commits: []review.Commit{{SHA: c1, Title: "one"}},
		Threads: []*review.Thread{
			{ID: "w0", Notes: []*review.Note{note("w0", "developer", 0, "An earlier report.")}},
			{ID: "w1", Notes: []*review.Note{note("w1", "triage", 1, "**Triage verdict: needs-review (unchanged).** Not merged.")}},
			{ID: "w2", Notes: []*review.Note{note("w2", "maintainer", 2, "Maintainer: merged main.")}},
			{ID: "w3", Notes: []*review.Note{note("w3", "developer", 5, "Changed as asked.")}},
			{ID: "l", Path: "a.go", Line: 2, HeadLine: 2, Head: c1, Notes: []*review.Note{note("l", "me", 1, "why b?"), note("l2", "developer", 6, "because c")}},
		}}
	diff := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,2 +1,3 @@\n a\n+b\n c\n"
	path := filepath.Join(t.TempDir(), "read.json")
	reads := review.NewReads(path)
	if err := reads.Mark("o/r#1", review.Read{Threads: map[string]time.Time{"w1": at(1), "w2": at(2)}}); err != nil {
		t.Fatal(err)
	}
	open := func(h int) *model { // as Run opens the review
		m := &model{rv: rv, ref: review.Ref{Owner: "o", Name: "r", Number: 1}, w: 100, h: h, files: map[string][]review.File{}, loading: map[string]bool{},
			reads: reads, unfold: map[string]bool{}, showRead: map[int]bool{}}
		m.read, _ = reads.Get("o/r#1")
		m.saved = m.read.Clone()
		m.files[m.diffKey(0)] = review.ParseDiff(diff)
		m.layout(false)
		return m
	}
	m := open(60)
	text := func() string {
		var sb strings.Builder
		for _, r := range m.rows {
			for _, s := range r.segs {
				sb.WriteString(s.text)
			}
			sb.WriteString("\n")
		}
		return sb.String()
	}
	kinds := func() (out []string) {
		for _, it := range m.items {
			if it.kind == "fold" || it.kind == "folds" || it.kind == "note" {
				id := ""
				if it.note != nil {
					id = it.note.ID
				}
				out = append(out, it.kind+":"+id)
			}
		}
		return out
	}
	cursor := func() string {
		it, _ := m.item()
		if it.note == nil {
			return it.kind
		}
		return it.kind + ":" + it.note.ID
	}
	if got, want := kinds(), []string{"folds:", "note:w0", "note:w3", "note:l", "note:l2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("the notes: %v, want %v\n%s", got, want, text())
	}
	for _, want := range []string{"▸ 2 notes you have read", "An earlier report.", "Changed as asked.", "because c"} {
		if !strings.Contains(text(), want) {
			t.Errorf("lacks %q:\n%s", want, text())
		}
	}
	if strings.Contains(text(), "Maintainer: merged main") {
		t.Errorf("a read note shows open:\n%s", text())
	}
	if n := strings.Count(text(), " new"); n != 3 {
		t.Errorf("%d notes say new, want 3 (w0, w3 and l2):\n%s", n, text())
	}

	// } goes from one open thread to the next, and to a new reply in one
	m.setCur(0)
	var stops []string
	for i := 0; i < 5; i++ {
		m.key(key{Rune: '}'})
		stops = append(stops, cursor())
	}
	if want := []string{"note:w0", "note:w3", "note:l", "note:l2", "note:l2"}; !reflect.DeepEqual(stops, want) {
		t.Errorf("} stops at %v, want %v", stops, want)
	}

	// z on the line of read ones shows them one by one, the cursor on the first; Enter opens one
	m.setCur(slicesIndex(m.items, "folds"))
	m.key(key{Rune: 'z'})
	if got, want := kinds(), []string{"note:w0", "fold:w1", "fold:w2", "note:w3", "note:l", "note:l2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after z on the read ones: %v, want %v", got, want)
	}
	if !strings.Contains(text(), "▸ triage · ") || !strings.Contains(text(), "Triage verdict: needs-review (unchanged). Not merged.") {
		t.Errorf("a folded note's line lacks who wrote it or how it starts:\n%s", text())
	}
	if c := cursor(); c != "fold:w1" {
		t.Errorf("the cursor is on %s, not the first read note", c)
	}
	m.key(key{Name: "enter"})
	if c := cursor(); c != "note:w1" || !strings.Contains(text(), "Triage verdict") {
		t.Fatalf("Enter on a folded note: the cursor is on %s", c)
	}

	// z on a note folds its thread and keeps it read; the cursor stays on it
	m.setCur(slicesIndexNote(m.items, "w3"))
	m.key(key{Rune: 'z'})
	if got := kinds(); !reflect.DeepEqual(got, []string{"note:w0", "note:w1", "fold:w2", "fold:w3", "note:l", "note:l2"}) {
		t.Fatalf("after z on w3: %v", got)
	}
	if c := cursor(); c != "fold:w3" {
		t.Errorf("after folding w3 the cursor is on %s", c)
	}
	if r, _ := reads.Get("o/r#1"); !r.Threads["w3"].Equal(at(5)) {
		t.Errorf("folding w3 was not kept: %+v", r)
	}
	// c or r on a folded thread replies to it, open
	m.key(key{Rune: 'c'})
	if m.ed == nil || m.ed.mode != "reply" || m.ed.note != "w3" || !m.unfold["w3"] {
		t.Fatalf("c on a folded thread: %+v", m.ed)
	}
	m.ed = nil

	// in a review opened again, a note that was on the screen folds; one that was not stays new
	m = open(12) // only the top rows show
	m.setCur(0)
	m.draw(io.Discard)
	m.flush()
	r, _ := reads.Get("o/r#1")
	if !r.Threads["w0"].Equal(at(0)) || r.Threads["l"].After(at(1)) {
		t.Fatalf("on a short screen: %+v", r)
	}
	if !strings.Contains(text(), "An earlier report.") {
		t.Errorf("a note on the screen folded at once:\n%s", text())
	}
	m = open(60)
	if got, want := kinds(), []string{"folds:", "note:l", "note:l2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("opened again: %v, want %v", got, want)
	}

	// z on a note whose thread joins the line of read ones: the cursor goes on to what came next
	m = open(60)
	m.unfold["w3"] = true
	m.layout(true)
	m.setCur(slicesIndexNote(m.items, "w3"))
	m.key(key{Rune: 'z'})
	if c := cursor(); c != "file" { // the whole change's notes end with it: a.go comes next
		t.Errorf("after folding into the read ones, the cursor is on %s", c)
	}

	// Z folds them all, and the picker counts none new
	m.key(key{Rune: 'Z'})
	if got := kinds(); !reflect.DeepEqual(got, []string{"folds:", "fold:l"}) {
		t.Fatalf("after Z: %v", got)
	}
	m.pickView()
	for _, c := range m.pick.items {
		for _, sg := range c.segs {
			if strings.Contains(sg.text, "new") {
				t.Errorf("the picker still counts new notes: %q", sg.text)
			}
		}
	}
}

func slicesIndex(items []item, kind string) int {
	for i, it := range items {
		if it.kind == kind {
			return i
		}
	}
	return -1
}

func slicesIndexNote(items []item, id string) int {
	for i, it := range items {
		if it.kind == "note" && it.note.ID == id {
			return i
		}
	}
	return -1
}

package review

import (
	"encoding/json"
	"testing"
)

const twoFiles = `diff --git a/a.go b/a.go
index 1111111..2222222 100644
--- a/a.go
+++ b/a.go
@@ -1,4 +1,4 @@ package a
 package a
-func Old() {}
+func New() {}
 
 // end
@@ -10,2 +10,3 @@ func x() {
 	a := 1
+	b := 2
 	return
\ No newline at end of file
diff --git a/new.txt b/new.txt
new file mode 100644
--- /dev/null
+++ b/new.txt
@@ -0,0 +1,2 @@
+one
+two
diff --git a/gone.txt b/gone.txt
deleted file mode 100644
--- a/gone.txt
+++ /dev/null
@@ -1 +0,0 @@
-bye
diff --git a/old name.go b/new name.go
similarity index 100%
rename from old name.go
rename to new name.go
diff --git a/img.png b/img.png
Binary files a/img.png and b/img.png differ
`

func TestParseDiff(t *testing.T) {
	files := ParseDiff(twoFiles)
	if len(files) != 5 {
		t.Fatalf("got %d files: %+v", len(files), files)
	}
	a := files[0]
	if a.Path != "a.go" || a.Status != "modified" || len(a.Hunks) != 2 || a.Added() != 2 || a.Deleted() != 1 {
		t.Fatalf("a.go: %+v", a)
	}
	// positions count from the first @@; the second @@ takes one
	want := []struct {
		kind     byte
		old, new int
		pos      int
	}{{' ', 1, 1, 1}, {'-', 2, 0, 2}, {'+', 0, 2, 3}, {' ', 3, 3, 4}, {' ', 4, 4, 5}, {' ', 10, 10, 7}, {'+', 0, 11, 8}, {' ', 11, 12, 9}}
	var got []Line
	for _, h := range a.Hunks {
		got = append(got, h.Lines...)
	}
	if len(got) != len(want) {
		t.Fatalf("a.go lines: %+v", got)
	}
	for i, w := range want {
		if g := got[i]; g.Kind != w.kind || g.Old != w.old || g.New != w.new || g.Pos != w.pos {
			t.Errorf("line %d: got %c old %d new %d pos %d, want %+v", i, g.Kind, g.Old, g.New, g.Pos, w)
		}
	}
	if got[3].Text != "" {
		t.Errorf("an empty context line keeps empty text: %q", got[3].Text)
	}
	if l, hi, ok := a.Find(11, false); !ok || hi != 1 || l.Text != "\tb := 2" {
		t.Errorf("Find new 11: %+v %d %v", l, hi, ok)
	}
	if _, _, ok := a.Find(2, true); !ok {
		t.Error("Find old 2")
	}
	if n := files[1]; n.Path != "new.txt" || n.Status != "added" || n.Hunks[0].Lines[1].New != 2 {
		t.Errorf("new.txt: %+v", n)
	}
	if g := files[2]; g.Path != "gone.txt" || g.Status != "deleted" || g.Hunks[0].Lines[0].Old != 1 {
		t.Errorf("gone.txt: %+v", g)
	}
	if r := files[3]; r.Path != "new name.go" || r.OldPath != "old name.go" || r.Status != "renamed" {
		t.Errorf("rename: %+v", r)
	}
	if b := files[4]; b.Path != "img.png" || !b.Binary {
		t.Errorf("binary: %+v", b)
	}
}

func TestALineCarriesItsSpansInJSON(t *testing.T) {
	b, _ := json.Marshal(Line{Kind: '+', New: 1, Text: "func", Spans: []Span{{Kind: "k", Start: 0, Len: 4}}})
	if want := `{"k":"+","new":1,"text":"func","hl":[{"k":"k","s":0,"n":4}]}`; string(b) != want {
		t.Errorf("got %s", b)
	}
	b, _ = json.Marshal(Line{Kind: ' ', Old: 1, New: 1, Text: "x"})
	if want := `{"k":" ","old":1,"new":1,"text":"x"}`; string(b) != want {
		t.Errorf("without spans: %s", b)
	}
}

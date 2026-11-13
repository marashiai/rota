package syntax

import (
	"reflect"
	"strings"
	"testing"

	"rota/gloss/review"
)

func diff(path, hunk string) []review.File {
	return review.ParseDiff("diff --git a/" + path + " b/" + path + "\n--- a/" + path + "\n+++ b/" + path + "\n@@ -1,9 +1,9 @@\n" + hunk)
}

func TestHighlightMarksTokensInRunes(t *testing.T) {
	files := diff("a.go", " package a\n+var é = \"ok\" // 1\n-func old() {}\n")
	lines := Highlight(files)[0].Hunks[0].Lines
	want := [][]review.Span{
		{{Kind: "k", Start: 0, Len: 7}},
		{{Kind: "k", Start: 0, Len: 3}, {Kind: "s", Start: 8, Len: 4}, {Kind: "c", Start: 13, Len: 4}}, // é is one rune
		{{Kind: "k", Start: 0, Len: 4}, {Kind: "nf", Start: 5, Len: 3}},
	}
	for i, w := range want {
		if !reflect.DeepEqual(lines[i].Spans, w) {
			t.Errorf("line %d %q: %v, want %v", i, lines[i].Text, lines[i].Spans, w)
		}
	}
	for _, h := range files[0].Hunks {
		for _, l := range h.Lines {
			if l.Spans != nil {
				t.Fatalf("the given files changed: %v", l.Spans)
			}
		}
	}
}

// Each side is lexed on its own: a comment one side opens cannot run into the lines the other
// side has, and a string that runs over lines is one on each of them.
func TestEachSideIsLexedWhole(t *testing.T) {
	lines := Highlight(diff("a.go", "-/* a\n+x := 1\n  b */\n"))[0].Hunks[0].Lines
	if c := []review.Span{{Kind: "c", Start: 0, Len: 4}}; !reflect.DeepEqual(lines[0].Spans, c) {
		t.Errorf("the deleted line opening a comment: %v", lines[0].Spans)
	}
	if m := []review.Span{{Kind: "m", Start: 5, Len: 1}}; !reflect.DeepEqual(lines[1].Spans, m) {
		t.Errorf("the added line: %v", lines[1].Spans)
	}
	for _, s := range lines[2].Spans {
		if s.Kind == "c" {
			t.Errorf("the context line is a comment only on the old side: %v", lines[2].Spans)
		}
	}
	lines = Highlight(diff("b.py", " s = \"\"\"a\n-b\n+c\n \"\"\"\n"))[0].Hunks[0].Lines
	for i, w := range [][]review.Span{{{Kind: "s", Start: 4, Len: 4}}, {{Kind: "s", Start: 0, Len: 1}}, {{Kind: "s", Start: 0, Len: 1}}, {{Kind: "s", Start: 0, Len: 3}}} {
		if !reflect.DeepEqual(lines[i].Spans, w) {
			t.Errorf("line %d %q of a string over lines: %v", i, lines[i].Text, lines[i].Spans)
		}
	}
}

func TestWhatStaysPlain(t *testing.T) {
	for name, files := range map[string][]review.File{
		"an unknown language": diff("a.unknownext", "+hello world\n"),
		"plain text":          diff("a.txt", "+hello world\n"),
		"a binary file":       review.ParseDiff("diff --git a/a.go b/a.go\nBinary files a/a.go and b/a.go differ\n"),
		"a hunk too large":    diff("a.go", "+x := \""+strings.Repeat("a", maxSide)+"\"\n"),
	} {
		for _, f := range Highlight(files) {
			for _, h := range f.Hunks {
				for _, l := range h.Lines {
					if l.Spans != nil {
						t.Errorf("%s has spans: %v", name, l.Spans)
					}
				}
			}
		}
	}
}

func TestKindsFollowTheParentType(t *testing.T) {
	lines := Highlight(diff("c.yaml", "+every: 1h  # hourly\n+enabled: true\n"))[0].Hunks[0].Lines
	want := [][]review.Span{
		{{Kind: "nt", Start: 0, Len: 5}, {Kind: "c", Start: 11, Len: 8}},
		{{Kind: "nt", Start: 0, Len: 7}, {Kind: "k", Start: 9, Len: 4}},
	}
	for i, w := range want {
		if !reflect.DeepEqual(lines[i].Spans, w) {
			t.Errorf("line %d %q: %v, want %v", i, lines[i].Text, lines[i].Spans, w)
		}
	}
}

func TestGoModStaysPlain(t *testing.T) {
	for _, l := range Highlight(diff("go.mod", "+go 1.25\n"))[0].Hunks[0].Lines {
		if l.Spans != nil {
			t.Errorf("go.mod lexed as another language's .mod: %v", l.Spans)
		}
	}
}

package md

import (
	"fmt"
	"testing"
)

func TestBlocks(t *testing.T) {
	doc := "# Title\n\nSome text\non two lines.\n\n- one\n  more\n* two\n  1. nested\n\n```\ncode  kept\n```\n\n> quoted\n> - item\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n---\n#tag is text\n"
	var got []string
	for _, b := range Parse(doc) {
		got = append(got, fmt.Sprintf("%s/%d/%s/%s", b.Kind, b.Level, b.Marker, b.Text))
	}
	want := []string{"heading/1//Title", "para/0//Some text on two lines.", "item/0/-/one more", "item/0/-/two", "item/1/1./nested",
		"code/0//", "quote/0//", "table/0//", "rule/0//", "para/0//#tag is text"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	blocks := Parse(doc)
	if blocks[5].Lines[0] != "code  kept" || len(blocks[6].Blocks) != 2 || blocks[7].Rows[1][1] != "2" {
		t.Fatalf("contents: %+v", blocks)
	}
}

func TestInline(t *testing.T) {
	var got []string
	for _, s := range Inline("a `x*y*` **b** *c* _d_ snake_case [l](https://x.y) end") {
		got = append(got, fmt.Sprintf("%q code=%v bold=%v it=%v link=%s", s.Text, s.Code, s.Bold, s.Italic, s.Link))
	}
	want := []string{`"a " code=false bold=false it=false link=`, `"x*y*" code=true bold=false it=false link=`, `" " code=false bold=false it=false link=`,
		`"b" code=false bold=true it=false link=`, `" " code=false bold=false it=false link=`, `"c" code=false bold=false it=true link=`, `" " code=false bold=false it=false link=`,
		`"d" code=false bold=false it=true link=`, `" snake_case " code=false bold=false it=false link=`, `"l" code=false bold=false it=false link=https://x.y`,
		`" end" code=false bold=false it=false link=`}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

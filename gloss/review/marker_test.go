package review

import (
	"strings"
	"testing"
)

func TestMarker(t *testing.T) {
	m := Meta{Author: "developer", Commit: "abc", Path: "a.go", Line: 3, Old: true}
	body := encode(m, "  Moves the check -->  first.\n")
	if !strings.HasPrefix(body, "<!-- gloss {") || strings.Count(body, "-->") != 2 || !strings.Contains(body, "**developer**") {
		t.Fatalf("body: %q", body)
	}
	gm, text := decode(body)
	if gm != m || text != "Moves the check -->  first." {
		t.Fatalf("decoded %+v %q", gm, text)
	}
	if gm, text := decode("plain text from github.com"); gm != (Meta{}) || text != "plain text from github.com" {
		t.Fatalf("plain: %+v %q", gm, text)
	}
	if encode(Meta{}, " hi ") != "hi" {
		t.Fatal("an empty marker is not written")
	}
}

func TestSections(t *testing.T) {
	body := joinSections([]section{{Meta{}, "whole change"}, {Meta{Re: "IC_1"}, "a reply\n\nin two paragraphs"}, {Meta{Commit: "abc"}, "on a commit"}})
	ss := splitSections(body)
	if len(ss) != 3 || ss[0].Text != "whole change" || ss[1].Meta.Re != "IC_1" || ss[1].Text != "a reply\n\nin two paragraphs" || ss[2].Meta.Commit != "abc" {
		t.Fatalf("sections: %+v\n%s", ss, body)
	}
	if ss := splitSections("typed on github.com"); len(ss) != 1 || ss[0].Text != "typed on github.com" {
		t.Fatalf("plain body: %+v", ss)
	}
	if ss := splitSections(""); len(ss) != 0 {
		t.Fatalf("empty body: %+v", ss)
	}
}

func TestParseRef(t *testing.T) {
	for in, want := range map[string]string{
		"acme/gloss#12": "acme/gloss#12",
		"https://github.com/acme/gloss/pull/7/files": "acme/gloss#7",
		"5":  "o/r#5",
		"#9": "o/r#9",
	} {
		r, err := ParseRef(in, "o/r")
		if err != nil || r.String() != want {
			t.Errorf("%s: %v %v", in, r, err)
		}
	}
	if _, err := ParseRef("5", ""); err == nil {
		t.Error("a bare number needs a repository")
	}
	for url, want := range map[string]string{
		"git@github-work:acme/rota.git":         "acme/rota",
		"https://github.com/modem-dev/hunk.git": "modem-dev/hunk",
		"https://github.com/o/r":                "o/r",
	} {
		if m := remoteRepo.FindStringSubmatch(url); m == nil || m[1]+"/"+m[2] != want {
			t.Errorf("%s: %v", url, m)
		}
	}
}

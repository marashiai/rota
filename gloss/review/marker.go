package review

import (
	"encoding/json"
	"strings"
)

// Meta is what gloss writes into a note's hidden marker, an HTML comment GitHub does not show:
//
//	<!-- gloss {"author":"developer","commit":"…","path":"a.go","line":12} -->
//
// Author names who wrote a note when its account cannot tell (an agent sharing its reviewer's
// account); a note without one is the account's own. Commit, Path, Line and Old say exactly where
// the note sits in that commit's own diff, which GitHub's anchor cannot always say; without
// Commit, in the whole change's diff when its head was Head. Re is the note a reply answers when
// GitHub cannot thread it (a PR comment, a section of a review's body). Approval says a section
// of a review's body is a person's approval of the pull request, not a note.
type Meta struct {
	Author   string `json:"author,omitempty"`
	Re       string `json:"re,omitempty"`
	Commit   string `json:"commit,omitempty"`
	Path     string `json:"path,omitempty"`
	Line     int    `json:"line,omitempty"`
	Old      bool   `json:"old,omitempty"`
	Head     string `json:"head,omitempty"`
	Approval bool   `json:"approval,omitempty"`
}

const markerOpen, markerClose = "<!-- gloss ", " -->"

// encode writes a note's body: the marker, the author's name for readers on github.com, the text.
func encode(m Meta, text string) string {
	if m == (Meta{}) {
		return strings.TrimSpace(text)
	}
	return marked(m, text)
}

// marked writes the marker even when it is empty.
func marked(m Meta, text string) string {
	text = strings.TrimSpace(text)
	b, _ := json.Marshal(m) // escapes < and >, so the comment cannot end early
	body := markerOpen + string(b) + markerClose + "\n"
	if m.Author != "" {
		body += "**" + m.Author + "**\n\n"
	}
	return body + text
}

// decode reads a body encode wrote; a body without a marker is plain text.
func decode(body string) (Meta, string) {
	var m Meta
	body = strings.TrimLeft(body, " \t\r\n")
	if !strings.HasPrefix(body, markerOpen) {
		return m, strings.TrimSpace(body)
	}
	end := strings.Index(body, markerClose)
	if end < 0 || json.Unmarshal([]byte(body[len(markerOpen):end]), &m) != nil {
		return Meta{}, strings.TrimSpace(body)
	}
	text := strings.TrimLeft(body[end+len(markerClose):], " \t\r\n")
	if m.Author != "" {
		text = strings.TrimPrefix(text, "**"+m.Author+"**")
	}
	return m, strings.TrimSpace(text)
}

// section is one note in a review's body. A review has a single body, so gloss keeps each of
// the reviewer's notes on the whole change, and each reply GitHub cannot thread, as a section
// that starts with its marker.
type section struct {
	Meta Meta
	Text string
}

func splitSections(body string) []section {
	var out []section
	var cur []string
	flush := func() {
		if s := strings.TrimSpace(strings.Join(cur, "\n")); s != "" {
			if m, text := decode(s); text != "" { // a marker alone holds no note
				out = append(out, section{m, text})
			}
		}
		cur = nil
	}
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, markerOpen) {
			flush()
		}
		cur = append(cur, line)
	}
	flush()
	return out
}

// joinSections writes a review's body; with no sections, an empty marker, because GitHub refuses
// to edit a review whose body is empty.
// withoutApproval drops the approval from a pending review's sections, so a plain Send does not
// approve.
func withoutApproval(ss []section) []section {
	var out []section
	for _, s := range ss {
		if !s.Meta.Approval {
			out = append(out, s)
		}
	}
	return out
}

func joinSections(ss []section) string {
	if len(ss) == 0 {
		return marked(Meta{}, "")
	}
	var parts []string
	for _, s := range ss {
		parts = append(parts, marked(s.Meta, s.Text))
	}
	return strings.Join(parts, "\n\n")
}

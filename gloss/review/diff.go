package review

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// File is one file's part of a unified diff.
type File struct {
	Path    string `json:"path"`
	OldPath string `json:"old_path,omitempty"` // set when renamed
	Status  string `json:"status"`             // added, deleted, renamed, modified
	Binary  bool   `json:"binary,omitempty"`
	Hunks   []Hunk `json:"hunks"`
}

// Hunk is one @@ section of a file's diff.
type Hunk struct {
	OldStart int    `json:"old_start"`
	OldCount int    `json:"old_count"`
	NewStart int    `json:"new_start"`
	NewCount int    `json:"new_count"`
	Section  string `json:"section,omitempty"` // the text after the second @@
	Lines    []Line `json:"lines"`
}

// Line is one line of a hunk. Old and New are its line numbers on each side, 0 where it has
// none: an added line has no Old, a deleted one no New. Spans mark the tokens in its text once
// syntax.Highlight has run; until then there are none.
type Line struct {
	Kind  byte   `json:"-"` // ' ', '+' or '-'
	Old   int    `json:"old,omitempty"`
	New   int    `json:"new,omitempty"`
	Text  string `json:"text"`
	Pos   int    `json:"-"` // GitHub's diff position: lines below the file's first @@
	Spans []Span `json:"hl,omitempty"`
}

// Span is a stretch of a line's text that is one kind of token: a keyword ("k"), a string
// ("s"), a comment ("c"), a number ("m") and so on, by the classes Pygments and chroma use.
// Start and Len count runes; the text between spans is plain.
type Span struct {
	Kind  string `json:"k"`
	Start int    `json:"s"`
	Len   int    `json:"n"`
}

// MarshalJSON writes the line's kind as "k": " ", "+" or "-".
func (l Line) MarshalJSON() ([]byte, error) {
	type plain Line
	return json.Marshal(struct {
		K string `json:"k"`
		plain
	}{string(l.Kind), plain(l)})
}

// Added and Deleted count the file's changed lines.
func (f File) Added() (n int)   { return f.count('+') }
func (f File) Deleted() (n int) { return f.count('-') }

func (f File) count(k byte) (n int) {
	for _, h := range f.Hunks {
		for _, l := range h.Lines {
			if l.Kind == k {
				n++
			}
		}
	}
	return n
}

// Find returns the line with that number on that side (old for a deleted or context line's
// old number), with its hunk's index, or false.
func (f File) Find(line int, old bool) (Line, int, bool) {
	for hi, h := range f.Hunks {
		for _, l := range h.Lines {
			if old && l.Old == line && l.Kind != '+' || !old && l.New == line && l.Kind != '-' {
				return l, hi, true
			}
		}
	}
	return Line{}, 0, false
}

var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@ ?(.*)$`)

// ParseDiff reads a unified diff of any number of files, as git and GitHub write it.
func ParseDiff(text string) []File {
	var files []File
	var f *File
	var h *Hunk
	var oldN, newN, pos int
	flush := func() {
		if f != nil {
			if f.Path == "" {
				f.Path = f.OldPath
			}
			if f.Status == "" {
				f.Status = "modified"
			}
			if f.Status != "renamed" {
				f.OldPath = ""
			}
			files = append(files, *f)
		}
		f, h = nil, nil
	}
	for _, raw := range strings.Split(text, "\n") {
		raw = strings.TrimSuffix(raw, "\r")
		if strings.HasPrefix(raw, "diff --git ") {
			flush()
			f = &File{}
			if a, b, ok := gitPaths(raw[len("diff --git "):]); ok {
				f.OldPath, f.Path = a, b
			}
			pos = 0
			continue
		}
		if f == nil {
			continue
		}
		if h != nil && (oldN < h.OldStart+h.OldCount+empty(h.OldCount) || newN < h.NewStart+h.NewCount+empty(h.NewCount)) {
			if raw == "" {
				raw = " " // an empty context line whose space was trimmed
			}
			if k := raw[0]; k == ' ' || k == '+' || k == '-' {
				pos++
				l := Line{Kind: k, Text: raw[1:], Pos: pos}
				switch k {
				case ' ':
					l.Old, l.New = oldN, newN
					oldN++
					newN++
				case '+':
					l.New = newN
					newN++
				case '-':
					l.Old = oldN
					oldN++
				}
				h.Lines = append(h.Lines, l)
				continue
			}
		}
		if strings.HasPrefix(raw, "\\") {
			continue // "\ No newline at end of file"
		}
		if m := hunkHeader.FindStringSubmatch(raw); m != nil {
			if len(f.Hunks) > 0 {
				pos++ // a later hunk's header takes a position too
			}
			hk := Hunk{OldStart: atoi(m[1]), OldCount: count(m[2]), NewStart: atoi(m[3]), NewCount: count(m[4]), Section: m[5]}
			f.Hunks = append(f.Hunks, hk)
			h = &f.Hunks[len(f.Hunks)-1]
			oldN, newN = hk.OldStart, hk.NewStart
			if hk.OldCount == 0 {
				oldN++ // "-0,0" (and "-5,0") count from the line after
			}
			if hk.NewCount == 0 {
				newN++
			}
			continue
		}
		switch {
		case strings.HasPrefix(raw, "new file mode"):
			f.Status = "added"
		case strings.HasPrefix(raw, "deleted file mode"):
			f.Status = "deleted"
		case strings.HasPrefix(raw, "rename from "):
			f.Status, f.OldPath = "renamed", unquote(raw[len("rename from "):])
		case strings.HasPrefix(raw, "rename to "):
			f.Status, f.Path = "renamed", unquote(raw[len("rename to "):])
		case strings.HasPrefix(raw, "Binary files "):
			f.Binary = true
		case strings.HasPrefix(raw, "--- "):
			f.OldPath = diffPath(raw[4:])
		case strings.HasPrefix(raw, "+++ "):
			if p := diffPath(raw[4:]); p != "" {
				f.Path = p
			} else {
				f.Path = f.OldPath
			}
		}
	}
	flush()
	return files
}

// empty is 1 for a side with no lines, whose start names the line before.
func empty(count int) int {
	if count == 0 {
		return 1
	}
	return 0
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

func count(s string) int {
	if s == "" {
		return 1
	}
	return atoi(s)
}

func unquote(s string) string {
	if strings.HasPrefix(s, `"`) {
		if u, err := strconv.Unquote(s); err == nil {
			return u
		}
	}
	return s
}

// diffPath reads the path of a ---/+++ line: "" for /dev/null.
func diffPath(s string) string {
	s = unquote(strings.TrimSpace(strings.SplitN(s, "\t", 2)[0]))
	if s == "/dev/null" {
		return ""
	}
	if strings.HasPrefix(s, "a/") || strings.HasPrefix(s, "b/") {
		return s[2:]
	}
	return s
}

// gitPaths reads "a/x b/y" from a diff --git line; good enough when neither path has " b/".
func gitPaths(s string) (string, string, bool) {
	if strings.HasPrefix(s, `"`) {
		return "", "", false // quoted: the ---/+++ or rename lines give the paths
	}
	i := strings.Index(s, " b/")
	if !strings.HasPrefix(s, "a/") || i < 0 {
		return "", "", false
	}
	return s[2:i], s[i+3:], true
}

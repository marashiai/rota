// Package md reads the markdown the agents write (headings, paragraphs, lists, tables,
// quotes, code blocks, rules, and inline code, bold, italic and links) into blocks that the
// console and the web pages each draw their own way. Anything else is text.
package md

import (
	"regexp"
	"strings"
)

// Block is one block of a document.
type Block struct {
	Kind   string     // heading, para, item, quote, code, table, rule
	Level  int        // heading: 1 to 6; item: nesting depth from 0
	Marker string     // item: "-" or the number of an ordered item, "1."
	Text   string     // heading, para, item: inline markdown
	Lines  []string   // code
	Rows   [][]string // table: the cells, header row first
	Blocks []Block    // quote: what it quotes
}

var (
	itemRe  = regexp.MustCompile(`^(\s*)([-*+]|\d+[.)])\s+(.*)$`)
	ruleRe  = regexp.MustCompile(`^(\*\s*){3,}$|^(-\s*){3,}$|^(_\s*){3,}$`)
	tableRe = regexp.MustCompile(`^\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?$`)
)

// Parse reads a document into blocks.
func Parse(text string) []Block {
	src := strings.Split(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\t", "    "), "\n")
	var out []Block
	var para []string
	flush := func() {
		if len(para) > 0 {
			out = append(out, Block{Kind: "para", Text: strings.Join(para, " ")})
			para = nil
		}
	}
	for i := 0; i < len(src); i++ {
		line := src[i]
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "```"):
			flush()
			var code []string
			for i++; i < len(src) && !strings.HasPrefix(strings.TrimSpace(src[i]), "```"); i++ {
				code = append(code, src[i])
			}
			out = append(out, Block{Kind: "code", Lines: code})
		case t == "":
			flush()
		case ruleRe.MatchString(t):
			flush()
			out = append(out, Block{Kind: "rule"})
		case strings.HasPrefix(t, "#") && (strings.TrimLeft(t, "#") == "" || strings.TrimLeft(t, "#")[0] == ' '):
			flush()
			level := len(t) - len(strings.TrimLeft(t, "#"))
			out = append(out, Block{Kind: "heading", Level: min(level, 6), Text: strings.TrimSpace(t[level:])})
		case strings.HasPrefix(t, "|") && i+1 < len(src) && tableRe.MatchString(strings.TrimSpace(src[i+1])):
			flush()
			b := Block{Kind: "table", Rows: [][]string{cells(t)}}
			for i += 2; i < len(src) && strings.HasPrefix(strings.TrimSpace(src[i]), "|"); i++ {
				b.Rows = append(b.Rows, cells(src[i]))
			}
			i--
			out = append(out, b)
		case strings.HasPrefix(t, ">"):
			flush()
			var q []string
			for ; i < len(src) && strings.HasPrefix(strings.TrimSpace(src[i]), ">"); i++ {
				q = append(q, strings.TrimPrefix(strings.TrimSpace(src[i])[1:], " "))
			}
			i--
			out = append(out, Block{Kind: "quote", Blocks: Parse(strings.Join(q, "\n"))})
		case itemRe.MatchString(line):
			flush()
			m := itemRe.FindStringSubmatch(line)
			b := Block{Kind: "item", Level: min(len(m[1])/2, 3), Marker: m[2], Text: m[3]}
			if strings.ContainsAny(m[2][:1], "*+") {
				b.Marker = "-"
			}
			for i+1 < len(src) && continues(src[i+1], len(m[1])) {
				i++
				b.Text += " " + strings.TrimSpace(src[i])
			}
			out = append(out, b)
		default:
			para = append(para, t)
		}
	}
	flush()
	return out
}

// continues says whether a line carries on the list item above it: indented deeper, and
// not a block of its own.
func continues(line string, indent int) bool {
	t := strings.TrimSpace(line)
	return t != "" && len(line)-len(strings.TrimLeft(line, " ")) > indent && !itemRe.MatchString(line) &&
		!strings.HasPrefix(t, "```") && !strings.HasPrefix(t, "|") && !strings.HasPrefix(t, ">") && !strings.HasPrefix(t, "#")
}

func cells(row string) []string {
	row = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(row), "|"), "|")
	parts := strings.Split(row, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// Span is a run of text with one style.
type Span struct {
	Text               string
	Code, Bold, Italic bool
	Link               string // the target of a link
}

var linkRe = regexp.MustCompile(`^\[([^\]]+)\]\(([^)\s]+)\)`)

func isWord(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// Inline reads code, bold, italic and links; the rest is text.
func Inline(s string) []Span { return inline(s, Span{}) }

func inline(s string, base Span) []Span {
	var out []Span
	var plain strings.Builder
	flush := func() {
		if plain.Len() > 0 {
			sp := base
			sp.Text = plain.String()
			out = append(out, sp)
			plain.Reset()
		}
	}
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '`':
			if j := strings.IndexByte(s[i+1:], '`'); j >= 0 {
				flush()
				sp := base
				sp.Text, sp.Code = s[i+1:i+1+j], true
				out = append(out, sp)
				i += j + 2
				continue
			}
		case strings.HasPrefix(s[i:], "**"):
			if j := strings.Index(s[i+2:], "**"); j > 0 {
				flush()
				b := base
				b.Bold = true
				out = append(out, inline(s[i+2:i+2+j], b)...)
				i += j + 4
				continue
			}
		case (c == '*' || c == '_') && i+1 < len(s) && s[i+1] != ' ' && s[i+1] != c && (i == 0 || !isWord(s[i-1])):
			if j := strings.IndexByte(s[i+1:], c); j > 0 && s[i+j] != ' ' && (i+2+j >= len(s) || !isWord(s[i+2+j])) {
				flush()
				b := base
				b.Italic = true
				out = append(out, inline(s[i+1:i+1+j], b)...)
				i += j + 2
				continue
			}
		case c == '[':
			if m := linkRe.FindStringSubmatchIndex(s[i:]); m != nil {
				flush()
				sp := base
				sp.Text, sp.Link = s[i+m[2]:i+m[3]], s[i+m[4]:i+m[5]]
				out = append(out, sp)
				i += m[1]
				continue
			}
		}
		plain.WriteByte(c)
		i++
	}
	flush()
	return out
}

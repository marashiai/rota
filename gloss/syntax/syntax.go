// Package syntax marks the tokens in a diff's lines, so the console and the review page can
// colour them: keywords, strings, comments, numbers and the names that stand out, as chroma
// lexes each file's language.
package syntax

import (
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"rota/gloss/review"
)

// kinds names the kinds of token a span can carry, by the class Pygments and chroma give each,
// and the token types that have it; a type not here takes its parent's kind, or none. Names in
// general, operators and punctuation stay plain, as on GitHub.
var kinds = map[chroma.TokenType]string{
	chroma.Keyword:            "k",  // a keyword
	chroma.OperatorWord:       "k",  // and, or, in, not
	chroma.KeywordType:        "kt", // a type
	chroma.LiteralString:      "s",  // a string, its escapes and interpolations too
	chroma.LiteralNumber:      "m",  // a number
	chroma.Comment:            "c",  // a comment
	chroma.CommentPreproc:     "cp", // a preprocessor line
	chroma.CommentPreprocFile: "cp",
	chroma.NameFunction:       "nf", // a function's name, where it is defined
	chroma.NameFunctionMagic:  "nf",
	chroma.NameClass:          "nc", // a class's name
	chroma.NameBuiltin:        "nb", // a built-in: len, print, True
	chroma.NameBuiltinPseudo:  "nb",
	chroma.NameConstant:       "nb",
	chroma.NameTag:            "nt", // a tag in HTML or XML, a key in YAML
	chroma.NameAttribute:      "na", // an attribute
	chroma.NameDecorator:      "nd", // a decorator
	chroma.GenericHeading:     "gh", // a heading in Markdown
	chroma.GenericSubheading:  "gu",
	chroma.GenericStrong:      "gs",
	chroma.GenericEmph:        "ge",
}

// kind is a token type's kind: its own, or the nearest type it is a kind of; "" for plain text.
func kind(t chroma.TokenType) string {
	for ; t != 0; t = t.Parent() {
		if k, ok := kinds[t]; ok {
			return k
		}
	}
	return ""
}

// maxSide is the most text one side of a hunk is lexed with: past it, as in a minified file, a
// regex lexer can take seconds.
const maxSide = 256 << 10

// Highlight gives back the files with the tokens in every line marked, as chroma lexes each
// file's language, told by its name; the given files stay as they are. A file in a language
// chroma has no lexer for, a binary file and a hunk too large to lex in good time stay plain.
// Each hunk is lexed whole, its old side and its new side apart, so a comment or a string that
// runs over several lines is one on each of them, and what one side deletes cannot colour the
// lines the other keeps.
func Highlight(files []review.File) []review.File {
	out := make([]review.File, len(files))
	for i, f := range files {
		out[i] = f
		if f.Binary || len(f.Hunks) == 0 {
			continue
		}
		lexer := lexerFor(f.Path)
		if lexer == nil {
			continue
		}
		lexer = chroma.Coalesce(lexer)
		out[i].Hunks = make([]review.Hunk, len(f.Hunks))
		for j, h := range f.Hunks {
			out[i].Hunks[j] = h
			out[i].Hunks[j].Lines = hunk(lexer, h.Lines)
		}
	}
	return out
}

// lexerFor is the lexer for a file, by its name; nil when chroma has none. go.mod and go.sum
// would take the lexers of other languages' .mod files, so they stay plain.
func lexerFor(path string) chroma.Lexer {
	switch filepath.Base(path) {
	case "go.mod", "go.sum":
		return nil
	}
	return lexers.Match(path)
}

// hunk is the hunk's lines with their spans. A context line takes the new side's: the state the
// file's reader sees now.
func hunk(lexer chroma.Lexer, lines []review.Line) []review.Line {
	old, new := side(lexer, lines, '-'), side(lexer, lines, '+')
	out := make([]review.Line, len(lines))
	oi, ni := 0, 0
	for i, l := range lines {
		switch l.Kind {
		case ' ':
			if new != nil {
				l.Spans = new[ni]
			}
			oi++
			ni++
		case '-':
			if old != nil {
				l.Spans = old[oi]
			}
			oi++
		case '+':
			if new != nil {
				l.Spans = new[ni]
			}
			ni++
		}
		out[i] = l
	}
	return out
}

// side lexes one side of a hunk as one text, its context lines with the deleted ones (kind '-')
// or with the added ones ('+'), and gives each of its lines' spans; nil when it cannot.
func side(lexer chroma.Lexer, lines []review.Line, kind byte) [][]review.Span {
	n, size := 0, 0
	for _, l := range lines {
		if l.Kind == ' ' || l.Kind == kind {
			n++
			size += len(l.Text) + 1
		}
	}
	if n == 0 || size > maxSide {
		return nil
	}
	var sb strings.Builder
	sb.Grow(size)
	for _, l := range lines {
		if l.Kind == ' ' || l.Kind == kind {
			sb.WriteString(l.Text)
			sb.WriteByte('\n')
		}
	}
	it, err := lexer.Tokenise(&chroma.TokeniseOptions{State: "root"}, sb.String())
	if err != nil {
		return nil
	}
	rows := chroma.SplitTokensIntoLines(it.Tokens())
	if len(rows) != n {
		return nil
	}
	out := make([][]review.Span, 0, n)
	for _, l := range lines {
		if l.Kind == ' ' || l.Kind == kind {
			spans, ok := spansOf(rows[len(out)], l.Text)
			if !ok {
				return nil
			}
			out = append(out, spans)
		}
	}
	return out
}

// spansOf turns one line's tokens into its spans, one for each run of tokens of a kind and none
// for the plain text between; false when the tokens do not spell the line.
func spansOf(tokens []chroma.Token, text string) ([]review.Span, bool) {
	var spans []review.Span
	at, n := 0, 0 // in runes, in bytes
	for _, t := range tokens {
		v := strings.TrimSuffix(t.Value, "\n") // only the row's last token has one
		runes := utf8.RuneCountInString(v)
		n += len(v)
		if k := kind(t.Type); k != "" && runes > 0 {
			if last := len(spans) - 1; last >= 0 && spans[last].Kind == k && spans[last].Start+spans[last].Len == at {
				spans[last].Len += runes
			} else {
				spans = append(spans, review.Span{Kind: k, Start: at, Len: runes})
			}
		}
		at += runes
	}
	return spans, n == len(text)
}

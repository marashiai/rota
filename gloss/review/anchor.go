package review

import (
	"context"
	"fmt"
	"strings"
)

// NoteInput is a new note: on a line, a hunk or a whole file of the whole change's diff (no
// Commit) or of a commit's own; on a whole commit (Path ""); or on the whole change (neither).
type NoteInput struct {
	Commit string `json:"commit,omitempty"` // a SHA, a prefix of one, or the commit's number (1 is the first)
	Path   string `json:"file,omitempty"`
	Line   int    `json:"line,omitempty"` // in that diff; 0 with Hunk 0: the whole file
	Old    bool   `json:"old,omitempty"`  // Line is a deleted line's number on the old side
	Hunk   int    `json:"hunk,omitempty"` // 1 is the file's first hunk: the note goes on its first changed line
	Text   string `json:"text"`
}

// anchor is where a note goes, checked against the diff it is in: a commit's own, or the whole
// change's when Commit is "".
type anchor struct {
	Commit, Path string
	Line         int
	Old          bool
	head         string // the head, for a note in the whole change's diff
}

func (a anchor) meta() Meta {
	return Meta{Commit: a.Commit, Path: a.Path, Line: a.Line, Old: a.Old, Head: a.head}
}

// on is the commit GitHub puts the note on: the head, for the whole change's diff.
func (a anchor) on(rv *Review) string {
	if a.Commit != "" {
		return a.Commit
	}
	return rv.Head
}

// resolve checks a new note's place against the pull request and the diff it is in.
func (c *Client) resolve(ctx context.Context, rv *Review, in NoteInput) (anchor, error) {
	if strings.TrimSpace(in.Text) == "" {
		return anchor{}, fmt.Errorf("a note needs text")
	}
	a := anchor{Path: in.Path}
	var files []File
	var err error
	where := "the change"
	if in.Commit == "" {
		if in.Path == "" {
			if in.Line != 0 || in.Hunk != 0 {
				return anchor{}, fmt.Errorf("a note on a line needs its file")
			}
			return anchor{}, nil
		}
		a.head = rv.Head
		files, err = c.Whole(ctx, rv)
	} else {
		i, ok := rv.Commit(in.Commit)
		if !ok {
			return anchor{}, fmt.Errorf("%s has no commit %q", rv.PR, in.Commit)
		}
		a.Commit = rv.Commits[i].SHA
		if in.Path == "" {
			if in.Line != 0 || in.Hunk != 0 {
				return anchor{}, fmt.Errorf("a note on a line needs its file")
			}
			return a, nil
		}
		where = "commit " + rv.Commits[i].Short()
		files, err = c.Diff(ctx, rv, a.Commit)
	}
	if err != nil {
		return anchor{}, err
	}
	f, ok := fileIn(files, in.Path)
	if !ok {
		return anchor{}, fmt.Errorf("%s does not change %s", where, in.Path)
	}
	switch {
	case in.Hunk != 0:
		if in.Hunk < 1 || in.Hunk > len(f.Hunks) {
			return anchor{}, fmt.Errorf("%s has %d hunks in %s, not %d", in.Path, len(f.Hunks), where, in.Hunk)
		}
		for _, l := range f.Hunks[in.Hunk-1].Lines {
			if l.Kind == '+' {
				a.Line = l.New
				break
			}
			if l.Kind == '-' {
				a.Line, a.Old = l.Old, true
				break
			}
		}
	case in.Line != 0:
		if _, _, ok := f.Find(in.Line, in.Old); !ok {
			side := "new"
			if in.Old {
				side = "old"
			}
			return anchor{}, fmt.Errorf("%s line %d (%s side) is not in %s's diff", in.Path, in.Line, side, where)
		}
		a.Line, a.Old = in.Line, in.Old
	}
	return a, nil
}

// position is where GitHub shows a note on a line: the line's position in the diff from the
// merge base to the commit the note is on. For the whole change that is the diff the note is in;
// for a commit it is not the commit's own diff. ok is false when that diff lacks the file (a
// commit later undone); the note then goes where GitHub threads nothing.
func (c *Client) position(ctx context.Context, rv *Review, a anchor) (pos int, ok bool, err error) {
	cum, err := c.cumulative(ctx, rv, a.on(rv))
	if err != nil {
		return 0, false, err
	}
	f, found := fileIn(cum, a.Path)
	if !found || len(f.Hunks) == 0 {
		return 0, false, nil
	}
	if a.Commit == "" { // the whole change's diff is this one: the line is in it, on its side
		if l, _, ok := f.Find(a.Line, a.Old); ok {
			return l.Pos, true, nil
		}
		return nearest(f, a.Line), true, nil
	}
	if !a.Old {
		if l, _, ok := f.Find(a.Line, false); ok {
			return l.Pos, true, nil
		}
		return nearest(f, a.Line), true, nil
	}
	// A deleted line: the same text deleted in the cumulative diff, else the line after it.
	own, err := c.Diff(ctx, rv, a.Commit)
	if err != nil {
		return 0, false, err
	}
	of, _ := fileIn(own, a.Path)
	dl, hi, found := of.Find(a.Line, true)
	if !found {
		return nearest(f, a.Line), true, nil
	}
	best, bestGap := 0, -1
	for _, h := range f.Hunks {
		for _, l := range h.Lines {
			if l.Kind == '-' && l.Text == dl.Text {
				if gap := abs(l.Old - dl.Old); bestGap < 0 || gap < bestGap {
					best, bestGap = l.Pos, gap
				}
			}
		}
	}
	if best > 0 {
		return best, true, nil
	}
	next, seen := 0, false // the first line after it that the new side has
	for _, l := range of.Hunks[hi].Lines {
		if l.Pos == dl.Pos {
			seen = true
		} else if seen && l.New > 0 {
			next = l.New
			break
		}
	}
	if next > 0 {
		if l, _, ok := f.Find(next, false); ok {
			return l.Pos, true, nil
		}
		return nearest(f, next), true, nil
	}
	return nearest(f, a.Line), true, nil
}

// nearest is the position of the line on the new side closest to line.
func nearest(f File, line int) int {
	best, gap := 0, -1
	for _, h := range f.Hunks {
		for _, l := range h.Lines {
			if l.New == 0 {
				continue
			}
			if g := abs(l.New - line); gap < 0 || g < gap {
				best, gap = l.Pos, g
			}
		}
	}
	if best == 0 {
		best = 1
	}
	return best
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

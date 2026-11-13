package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"rota/gloss/review"
)

func header(rv *review.Review, out io.Writer) {
	fmt.Fprintf(out, "%s  %s\nby %s · %s · %d commits · %s\n", rv.PR, rv.Title, rv.Author, strings.ToLower(rv.State), len(rv.Commits), rv.URL)
	if rv.Truncated {
		fmt.Fprintln(out, "(GitHub gave only the first 100 commits, threads or comments: some are missing)")
	}
}

func show(ctx context.Context, c *review.Client, rv *review.Review, a args, out io.Writer) error {
	type commit struct {
		review.Commit
		Number int           `json:"number"`
		Files  []review.File `json:"files"`
		Notes  int           `json:"notes"`
	}
	whole, err := c.Whole(ctx, rv)
	if err != nil {
		return err
	}
	var cs []commit
	for i, cm := range rv.Commits {
		files, err := c.Diff(ctx, rv, cm.SHA)
		if err != nil {
			return err
		}
		cs = append(cs, commit{cm, i + 1, files, rv.CommitThreads(i)})
	}
	if a.has("json") {
		for i := range cs {
			for j := range cs[i].Files {
				cs[i].Files[j].Hunks = nil
			}
		}
		for i := range whole {
			whole[i].Hunks = nil
		}
		return json.NewEncoder(out).Encode(map[string]any{"pr": rv.PR, "title": rv.Title, "url": rv.URL, "author": rv.Author,
			"state": rv.State, "base": rv.Base, "head": rv.Head, "approval": rv.Approval, "approved": rv.Approved(),
			"files": whole, "whole_notes": rv.WholeThreads(), "commits": cs})
	}
	header(rv, out)
	fmt.Fprintln(out, approvalLine(rv))
	placed := rv.PlaceWhole(whole)
	onIt := 0
	for _, t := range rv.Threads {
		if t.Commit == "" && t.Path == "" {
			onIt++
		}
	}
	fmt.Fprintf(out, "\nthe whole change  (%d notes: %d on it, %d on its diff)\n", rv.WholeThreads(), onIt, rv.WholeThreads()-onIt)
	for _, f := range whole {
		fmt.Fprintf(out, "    %s  %s +%d -%d, %d hunks%s\n", f.Path, f.Status, f.Added(), f.Deleted(), len(f.Hunks), noteCount(placed.OnFile(f.Path)))
	}
	fmt.Fprintln(out, "\ncommits")
	for _, cm := range cs {
		fmt.Fprintf(out, "%d %s %s%s\n", cm.Number, cm.Short(), cm.Title, noteCount(cm.Notes))
		for _, f := range cm.Files {
			fmt.Fprintf(out, "    %s  %s +%d -%d, %d hunks\n", f.Path, f.Status, f.Added(), f.Deleted(), len(f.Hunks))
		}
	}
	return nil
}

func noteCount(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("  (%d note%s)", n, plural(n))
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// approvalLine says whether a person approved the pull request, and who.
func approvalLine(rv *review.Review) string {
	switch a := rv.Approval; {
	case a == nil && rv.Truncated:
		return "cannot tell: GitHub gave only the first 100 reviews, threads or comments, and a later one may have taken an approval back"
	case a == nil:
		return "not approved"
	default:
		return fmt.Sprintf("approved by %s, %s", a.By, a.At.Local().Format("2006-01-02 15:04"))
	}
}

// approved says whether a person approved the pull request, and fails when not: the command for
// whoever merges.
func approved(rv *review.Review, out io.Writer) error {
	if !rv.Approved() {
		return errors.New(rv.PR + ": " + approvalLine(rv))
	}
	fmt.Fprintln(out, approvalLine(rv))
	return nil
}

// diff prints the whole change's diff (which ""), or a commit's own, with the notes in place.
func diff(ctx context.Context, c *review.Client, rv *review.Review, which string, a args, out io.Writer) error {
	var files []review.File
	var placed review.Placed
	var err error
	whole := which == ""
	if whole {
		if files, err = c.Whole(ctx, rv); err != nil {
			return err
		}
		placed = rv.PlaceWhole(files)
		if a.has("json") {
			return json.NewEncoder(out).Encode(map[string]any{"base": rv.Base, "head": rv.Head, "files": files})
		}
		fmt.Fprintf(out, "the whole change  %s  %d commits, %s..%s\n", rv.Title, len(rv.Commits), rv.Base[:7], rv.Head[:7])
	} else {
		i, ok := rv.Commit(which)
		if !ok {
			return fmt.Errorf("%s has no commit %q", rv.PR, which)
		}
		cm := rv.Commits[i]
		if files, err = c.Diff(ctx, rv, cm.SHA); err != nil {
			return err
		}
		placed = rv.Place(i, files)
		if a.has("json") {
			return json.NewEncoder(out).Encode(map[string]any{"number": i + 1, "commit": cm, "files": files})
		}
		fmt.Fprintf(out, "commit %d/%d %s %s\n", i+1, len(rv.Commits), cm.Short(), cm.Title)
		if cm.Message != "" {
			fmt.Fprintf(out, "%s\n", indent(cm.Message, "    "))
		}
	}
	draft := a.author() == ""
	for _, t := range placed.Whole {
		inline(t, draft, where(rv, t, whole, true), out)
	}
	for _, f := range files {
		name := f.Path
		if f.OldPath != "" {
			name = f.OldPath + " → " + f.Path
		}
		fmt.Fprintf(out, "\n%s  (%s +%d -%d)\n", name, f.Status, f.Added(), f.Deleted())
		for _, t := range placed.Files[f.Path] {
			inline(t, draft, where(rv, t, whole, true), out)
		}
		if f.Binary {
			fmt.Fprintln(out, "  binary file")
		}
		for hi, h := range f.Hunks {
			fmt.Fprintf(out, "  hunk %d  @@ -%d,%d +%d,%d @@ %s\n", hi+1, h.OldStart, h.OldCount, h.NewStart, h.NewCount, h.Section)
			for _, l := range h.Lines {
				fmt.Fprintf(out, "  %5s %5s %c %s\n", num(l.Old), num(l.New), l.Kind, l.Text)
				keys := []review.LineKey{{Path: f.Path, Line: l.New}, {Path: f.Path, Line: l.Old, Old: true}} // a context line has both
				switch l.Kind {
				case '+':
					keys = keys[:1]
				case '-':
					keys = keys[1:]
				}
				for _, k := range keys {
					for _, t := range placed.Lines[k] {
						inline(t, draft, where(rv, t, whole, false), out)
					}
				}
			}
		}
	}
	return nil
}

// where says what a thread's place in a view does not show by itself: the commit it was written
// on, in the whole change; the line or file this diff does not show, when it is loose.
func where(rv *review.Review, t *review.Thread, whole, loose bool) string {
	var parts []string
	if whole && t.Commit != "" {
		if n := rv.CommitNumber(t.Commit); n > 0 {
			parts = append(parts, fmt.Sprintf("in commit %d", n))
		} else {
			parts = append(parts, "in a commit no longer in the pull request")
		}
	}
	switch {
	case t.Outdated:
		parts = append(parts, "changed by a later commit")
	case loose && t.Line != 0:
		side := ""
		if t.Old {
			side = " (old side)"
		}
		parts = append(parts, fmt.Sprintf("line %d%s, which this diff does not show", t.Line, side))
	}
	if t.Resolved {
		parts = append(parts, "resolved")
	}
	return strings.Join(parts, " · ")
}

func num(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprint(n)
}

// inline prints a thread under the line it is on.
func inline(t *review.Thread, drafts bool, where string, out io.Writer) {
	if where != "" {
		where = "  [" + where + "]"
	}
	for _, n := range t.Notes {
		if n.Draft && !drafts {
			continue
		}
		fmt.Fprintf(out, "              ┆ %s%s (%s)%s\n%s\n", n.Author, tag(n), n.ID, where, indent(n.Text, "              ┆   "))
		where = ""
	}
}

func tag(n *review.Note) string {
	if n.Draft {
		return " [draft]"
	}
	return ""
}

func indent(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n")
}

// visible drops the reviewer's drafts from what an author sees.
func visible(t *review.Thread, author string) []*review.Note {
	var out []*review.Note
	for _, n := range t.Notes {
		if n.Draft && author != "" {
			continue
		}
		out = append(out, n)
	}
	return out
}

// waiting says whether the thread waits on author: for an agent, a person sent its last note in
// a review; for the reviewer, someone else wrote its last note.
func waiting(rv *review.Review, notes []*review.Note, author string) bool {
	if len(notes) == 0 {
		return false
	}
	last := notes[len(notes)-1]
	if author != "" {
		return last.AsksAgents()
	}
	return !rv.Mine(last)
}

func notes(rv *review.Review, a args, out io.Writer) error {
	author := a.author()
	idx := map[string]int{}
	for i, c := range rv.Commits {
		idx[c.SHA] = i + 1
	}
	type thread struct {
		*review.Thread
		Number  int            `json:"commit_number,omitempty"`
		Notes   []*review.Note `json:"notes"`
		Waiting bool           `json:"waiting"`
	}
	var list []thread
	for _, t := range rv.Threads {
		ns := visible(t, author)
		if len(ns) == 0 {
			continue
		}
		w := !t.Resolved && waiting(rv, ns, author)
		if a.has("open") && !w {
			continue
		}
		list = append(list, thread{t, idx[t.Commit], ns, w})
	}
	if a.has("json") {
		return json.NewEncoder(out).Encode(map[string]any{"pr": rv.PR, "viewer": rv.Viewer, "threads": list})
	}
	header(rv, out)
	who := "you"
	if author != "" {
		who = author
	}
	open := 0
	for _, t := range list {
		if t.Waiting {
			open++
		}
	}
	fmt.Fprintf(out, "%d threads, %d waiting on %s\n", len(list), open, who)
	group := -1
	for _, t := range list {
		if g := t.Number; g != group {
			group = g
			if g == 0 {
				fmt.Fprintln(out, "\n── the whole change")
			} else {
				c := rv.Commits[g-1]
				fmt.Fprintf(out, "\n── commit %d %s %s\n", g, c.Short(), c.Title)
			}
		}
		where := "the whole change"
		switch {
		case t.Path != "" && t.Line == 0:
			where = t.Path
		case t.Path != "":
			side := "+"
			if t.Old {
				side = "-"
			}
			where = fmt.Sprintf("%s:%s%d", t.Path, side, t.Line)
		case t.Commit != "":
			where = "the whole commit"
		}
		if t.Number == 0 && t.Commit != "" {
			where += " in a commit no longer in the pull request"
		}
		var flags []string
		if t.Outdated {
			flags = append(flags, "changed by a later commit")
		}
		if t.Resolved {
			flags = append(flags, "resolved")
		}
		if t.Waiting {
			flags = append(flags, "waiting on "+who)
		}
		fmt.Fprintf(out, "\n%s", where)
		if len(flags) > 0 {
			fmt.Fprintf(out, "  [%s]", strings.Join(flags, ", "))
		}
		fmt.Fprintln(out)
		for _, n := range t.Notes {
			fmt.Fprintf(out, "  %s%s · %s · %s\n%s\n", n.Author, tag(n), n.Created.Local().Format("2006-01-02 15:04"), n.ID, indent(n.Text, "    "))
		}
	}
	return nil
}

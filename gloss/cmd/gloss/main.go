// Command gloss reviews a GitHub pull request, the whole change and commit by commit, with notes
// beside the code.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"rota/gloss/review"
	"rota/gloss/tui"
)

//go:embed skill.md
var skill string

var version = "dev"

const usage = `gloss reviews a GitHub pull request, the whole change and commit by commit, with
notes beside the code. Every note, reply and draft is stored on the pull request; gloss keeps
only which notes you have read, and folds those.

Reading:
  gloss show  <pr>                      the pull request: the files the whole change touches, and its commits
  gloss diff  <pr> [<commit>]           the whole change's diff, or a commit's own, numbered, with its notes in place
  gloss notes <pr> [--open]             every note and reply; --open: only threads waiting on --as
  gloss approved <pr>                   whether a person approved it, and who; fails when not:
                                        for whoever merges

Writing under a name (agents), published at once:
  gloss note  <pr> --as <name> [--commit <c>] [--file <path> [--line <n> | --old-line <n> | --hunk <k>]] --text <text>
                                        on the whole change; on its diff (--file); on a commit; on the commit's own diff
  gloss post  <pr> --as <name> < notes.json     many notes: [{"commit","file","line","old","hunk","text"}]
  gloss reply <pr> <note-id> --as <name> --text <text>

Reviewing:
  gloss review <pr> [--on-send <command>]
                                        the console: the same review in this terminal
  gloss web [<pr>] [--addr host:port] [--on-send <command>] [--no-open]
                                        the review page in a browser, on this machine only;
                                        after Send or Approve, runs the command ($GLOSS_ON_SEND)
                                        with GLOSS_PR, GLOSS_URL, GLOSS_SENT and GLOSS_APPROVED
                                        (1 when you approved it, else empty) set

For agents:
  gloss skill                           how an agent works with gloss

<pr> is owner/name#N, a pull request URL, or N inside a checkout of the repository (or with
--repo owner/name). <commit> is a SHA, a prefix of one, or its number (1 is the first commit).
--text - reads the text from standard input. --as defaults to $GLOSS_AUTHOR. --json prints JSON.
The GitHub token comes from GH_TOKEN, GITHUB_TOKEN or gh auth token.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "gloss:", err)
		os.Exit(1)
	}
}

// args are a command's positional arguments and --flags, in any order.
type args struct {
	pos   []string
	flags map[string]string
}

var boolFlags = map[string]bool{"json": true, "open": true, "help": true, "no-open": true}

func parse(in []string) (args, error) {
	a := args{flags: map[string]string{}}
	for i := 0; i < len(in); i++ {
		s := in[i]
		if !strings.HasPrefix(s, "--") || s == "--" {
			a.pos = append(a.pos, s)
			continue
		}
		name, val, has := strings.Cut(s[2:], "=")
		if !has && !boolFlags[name] {
			if i+1 >= len(in) {
				return a, fmt.Errorf("--%s needs a value", name)
			}
			i++
			val = in[i]
		}
		if !has && boolFlags[name] {
			val = "true"
		}
		a.flags[name] = val
	}
	return a, nil
}

func (a args) has(name string) bool { return a.flags[name] == "true" }

func (a args) int(name string) (int, error) {
	v, ok := a.flags[name]
	if !ok {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("--%s wants a number from 1, not %q", name, v)
	}
	return n, nil
}

func (a args) author() string {
	if v := a.flags["as"]; v != "" {
		return v
	}
	return os.Getenv("GLOSS_AUTHOR")
}

// text is --text, read from stdin when it is "-".
func (a args) text(stdin io.Reader) (string, error) {
	t, ok := a.flags["text"]
	if !ok {
		return "", errors.New("--text is missing")
	}
	if t == "-" {
		b, err := io.ReadAll(stdin)
		if err != nil {
			return "", err
		}
		t = string(b)
	}
	return t, nil
}

func run(ctx context.Context, argv []string, stdin io.Reader, out io.Writer) error {
	if len(argv) == 0 || argv[0] == "help" || argv[0] == "--help" || argv[0] == "-h" {
		fmt.Fprint(out, usage)
		return nil
	}
	cmd := argv[0]
	switch cmd {
	case "version", "--version":
		fmt.Fprintln(out, "gloss", version)
		return nil
	case "skill":
		fmt.Fprint(out, skill)
		return nil
	}
	a, err := parse(argv[1:])
	if err != nil {
		return err
	}
	if cmd == "web" {
		return serve(ctx, a, out)
	}
	if len(a.pos) == 0 {
		return fmt.Errorf("%s: which pull request?\n\n%s", cmd, usage)
	}
	repo := a.flags["repo"]
	if repo == "" {
		repo = review.RepoFromGit("")
	}
	ref, err := review.ParseRef(a.pos[0], repo)
	if err != nil {
		return err
	}
	token, err := review.Token()
	if err != nil {
		return err
	}
	c := review.NewClient(token)
	if cmd == "review" { // the console runs for as long as the reviewer wants
		hook := a.flags["on-send"]
		if hook == "" {
			hook = os.Getenv("GLOSS_ON_SEND")
		}
		return tui.Run(ctx, c, ref, tui.Options{OnSend: onSend(hook)})
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	rv, err := c.Load(ctx, ref)
	if err != nil {
		return err
	}
	switch cmd {
	case "show":
		return show(ctx, c, rv, a, out)
	case "approved":
		return approved(rv, out)
	case "diff":
		which := "" // the whole change
		if len(a.pos) > 1 {
			which = a.pos[1]
		}
		return diff(ctx, c, rv, which, a, out)
	case "notes":
		return notes(rv, a, out)
	case "note":
		text, err := a.text(stdin)
		if err != nil {
			return err
		}
		in := review.NoteInput{Commit: a.flags["commit"], Path: a.flags["file"], Text: text}
		if in.Line, err = a.int("line"); err != nil {
			return err
		}
		if old, err := a.int("old-line"); err != nil {
			return err
		} else if old != 0 {
			in.Line, in.Old = old, true
		}
		if in.Hunk, err = a.int("hunk"); err != nil {
			return err
		}
		return post(ctx, c, rv, a, []review.NoteInput{in}, out)
	case "post":
		var ins []review.NoteInput
		if err := json.NewDecoder(stdin).Decode(&ins); err != nil {
			return fmt.Errorf("post: standard input must be a JSON list of notes: %v", err)
		}
		return post(ctx, c, rv, a, ins, out)
	case "reply":
		if len(a.pos) < 2 {
			return errors.New("reply: which note? (its id, from gloss notes)")
		}
		text, err := a.text(stdin)
		if err != nil {
			return err
		}
		author := a.author()
		if author == "" {
			return errors.New("reply: say who writes it with --as <name> (or $GLOSS_AUTHOR); the reviewer replies in gloss's views")
		}
		id, err := c.Reply(ctx, rv, author, a.pos[1], text)
		if err != nil {
			return err
		}
		return printIDs(a, out, []string{id})
	}
	return fmt.Errorf("no command %q\n\n%s", cmd, usage)
}

func post(ctx context.Context, c *review.Client, rv *review.Review, a args, ins []review.NoteInput, out io.Writer) error {
	author := a.author()
	if author == "" {
		return errors.New("say who writes the notes with --as <name> (or $GLOSS_AUTHOR); the reviewer writes notes in gloss's views")
	}
	ids, err := c.Post(ctx, rv, author, ins)
	if len(ids) > 0 {
		if perr := printIDs(a, out, ids); perr != nil && err == nil {
			err = perr
		}
	}
	return err
}

func printIDs(a args, out io.Writer, ids []string) error {
	if a.has("json") {
		return json.NewEncoder(out).Encode(map[string]any{"ids": ids})
	}
	for _, id := range ids {
		fmt.Fprintln(out, "posted", id)
	}
	return nil
}

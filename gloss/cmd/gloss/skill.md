# Working with gloss

gloss lets a reviewer read your pull request as one change, the diff from the base to the head
with notes beside the code, and open each commit the same way; you talk with them in those notes.
Every note lives on the pull request on GitHub. You write under your own name with `--as <name>`
(or `$GLOSS_AUTHOR`); what you write is published at once.

`<pr>` is `owner/name#N`, a pull request URL, or `N` inside a checkout of the repository.
`<commit>` is a SHA, a prefix of one, or the commit's number (1 is the first).

## When you open a pull request: guide the reviewer through the whole change

The reviewer reads the whole change first. Put what they must check, decide or do on the whole
change: a note on it that lists the points, and a note on each hunk of its diff they must look at
(a risk, a decision that is not obvious, an edge case to check by hand). Skip what the code says
by itself. Then give each commit one short note saying what it does, for a reviewer who opens
the commits: notes on a commit's own diff are for what only that commit shows.

Write for a reviewer reading on a phone:

- Lead with what they must check, decide or do. Then the why, in one clause.
- Short sentences, one idea each, plain words. Say each point once.
- Several points: a numbered list, one point per item, each starting with a bold label:
  **Check:**, **Decide:**, **Rollout:**, **Not verified:**, **Risk:**, **My choice:**.
- Leave out how the code works beyond one clause, which review round found what, and anything the
  diff already says. Keep every path, number, command and issue number exact.
- The reviewer remembers no numbers. Say what a rule, decision or other pull request is, in a few
  words, then its number: "the rule from #861 (a message skips an open optional question)", never
  "#861's rule" alone.
- A note on the whole change stays under 150 words; a note on a hunk under 60; a note on a
  commit under 40.
- Ask in a full sentence ("Say if you want it to answer 404."), not a fragment ("404?").

1. `gloss show <pr>` lists the files the whole change touches, and the commits with the files
   each changes.
2. `gloss diff <pr>` prints the whole change's diff with hunk numbers and line numbers;
   `gloss diff <pr> <commit>` a commit's own.
3. Post all your notes in one call:

```sh
gloss post <pr> --as <name> <<'EOF'
[
  {"text": "What to check:\n1. **Rollout:** ...\n2. **Not verified:** ..."},
  {"file": "src/a.go", "hunk": 2, "text": "**Check:** the lock is taken before the read now: ..."},
  {"file": "src/b.go", "line": 41, "text": "**Decide:** ..."},
  {"file": "src/b.go", "line": 17, "old": true, "text": "**Risk:** removed because ..."},
  {"commit": "1", "text": "Moves the check before the write, so the next commit can drop the retry."},
  {"commit": "2", "text": "Drops the retry and its test."}
]
EOF
```

A note without `commit` or `file` is on the whole change; with `file` but no `commit`, on the
whole change's diff; with `commit` but no `file`, on the whole commit; with both, on the commit's
own diff. With `file` but no `line` or `hunk`, it is on the whole file. `line` is the new line
number from `gloss diff`; for a deleted line give its old number and `"old": true`. `hunk` puts
the note on that hunk's first changed line. The whole change shows every note on a line or a
file, from whichever diff, where GitHub tracks it to now; a line a later commit changed shows its
note on the file instead.

## When the reviewer sends notes: answer every one

1. `gloss notes <pr> --open --as <name>` lists the threads waiting on you, with each note's id.
2. Change the code where a note asks for it, in new commits.
3. Answer each thread: `gloss reply <pr> <note-id> --as <name> --text "Done in 3f2a1c9: ..."`.
   Say what you changed and where, or why you did not.

`gloss notes <pr>` without `--open` shows every thread; `gloss diff <pr> [<commit>]` shows the
notes in place. `--json` prints any of these as JSON. `--text -` reads the text from standard
input, for long notes.

Do not reply to a thread that is not waiting on you. Never write a note without your name: `--as`, or
`$GLOSS_AUTHOR` when it is set.

## Approval

Only a person approves a pull request, in gloss's views. `gloss approved <pr>` says whether a
person approved it, and who: it fails when not. Never write an approval yourself.

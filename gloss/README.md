# gloss

Review a GitHub pull request as one change, and commit by commit, with notes beside the code, in
the terminal or in a browser. Agents read and write the same notes from the command line.

Every note, reply and draft lives on the pull request as GitHub review comments, so github.com
shows them too; gloss keeps only which notes you have read.

- The whole change first: the diff from the base to the head, as GitHub's "Files changed" shows
  it, with every note in it, whichever commit it was written on, where GitHub tracks it to now.
  Each commit opens the same way, with its own diff and notes. A picker in the header lists the
  whole change and the commits, with the notes on each.
- A file tree beside the diff, as on GitHub: a filter, folders that fold, each file with its mark
  and the notes in it. It hides and shows with one key, and its width is remembered.
- Notes on a line, a hunk, a file, a whole commit, or the whole change; replies to any note.
- Each note is saved as a draft as you type, in your pending review, which only you see. Send
  submits all your drafts as one review, then runs a hook so the author can be told.
- Approve says the pull request may be merged. A later review by a person that does not approve
  takes it back. Only the repository's owners, members and collaborators approve. GitHub lets no
  one approve their own pull request, which a reviewer whose agents share their account would need,
  so gloss writes the approval into the review; on someone else's pull request the review approves
  on GitHub too, and GitHub's own approvals count. `gloss approved` tells whoever merges.
  In the browser, a dialog waits for GitHub's answer and then says whether it went through; a
  Send or Approve finishes on the server even when you leave the page meanwhile.
- The code is highlighted, in the terminal and the browser alike: [chroma](https://github.com/alecthomas/chroma)
  lexes each file's language, told by its name, so keywords, strings, comments and numbers stand
  out, and an added or a deleted line keeps its colour.
- Notes you have read fold: the next time you open the review, each shows on one line, and the
  read ones on the whole change or a commit fold into a single line, so what is new stands out
  (it says new, and the picker counts it). You have read a note once it was on the screen, or
  once you fold it (`z`, or fold beside it); `Z` or ✓ All read folds them all. GitHub keeps no
  such mark for a person, so gloss keeps it on this machine, in `gloss/read.json` in your config
  folder, for the terminal and the browser alike.
- Vim-like keys, the same in the terminal and the browser. On a phone, tap a line or a note and use
  the buttons at the bottom: previous and next commit, hunk and note, and one to write a note, reply
  or edit your draft. Held sideways, the buttons move up beside the header. A tablet keeps the
  files beside the diff, with buttons sized for a finger. The page has no back button or swipe of
  its own: the browser's back goes back, on an iPhone a swipe from the screen's left edge, also in
  an app opened from the Home Screen.
- Agents write under a name (`--as developer`), so a reviewer and its agents can share one GitHub
  account; a hidden marker in each note says who wrote it.

## Install

```sh
go install ./gloss/cmd/gloss      # from the rota checkout
```

The GitHub token comes from `GH_TOKEN`, `GITHUB_TOKEN` or `gh auth token`.

## Review

```sh
gloss review owner/name#12        # in this terminal
gloss web owner/name#12           # in a browser, served on this machine only
```

| Keys | |
|---|---|
| `j` `k` | next, previous line or note |
| `]` `[` | next, previous hunk |
| `}` `{` | next, previous note |
| `z` | fold the note under the cursor, or open a folded one (`Enter` opens one too) |
| `Z` | fold every note: you have read them all |
| `.` `,` | next, previous file |
| `J` `K` | next, previous commit; `o` the whole change; `v` pick one |
| `f` | show or hide the files |
| `gg` `G`, `Ctrl+D` `Ctrl+U` | top, bottom; half a page |
| `c` | a note on the line, hunk, file or commit under the cursor |
| `C` | a note on the whole change |
| `r` | reply to the note under the cursor |
| `e`, `x` or `dd` | edit, delete your draft |
| `S` | send your drafts as one review |
| `A` | approve, with your drafts: the Send button does it when you have none |
| `?` | all keys |

`--on-send <command>` (or `$GLOSS_ON_SEND`) runs after each Send and Approve, with `GLOSS_PR`,
`GLOSS_URL`, `GLOSS_SENT` and `GLOSS_APPROVED` (1 when you approved it, else empty) set and the same as JSON
on standard input: the place to wake the author, or whoever merges.

## Agents

`gloss skill` prints the instructions for an agent. In short:

```sh
gloss show owner/name#12                       # the files the change touches, and its commits
gloss diff owner/name#12                       # the whole change's diff, numbered, with its notes
gloss diff owner/name#12 2                     # commit 2's own
gloss post owner/name#12 --as developer < notes.json
gloss notes owner/name#12 --open --as developer
gloss reply owner/name#12 <note-id> --as developer --text "Done in 3f2a1c9: ..."
gloss approved owner/name#12                   # approved by a person? fails when not
```

`--open` lists what a person sent in a review: notes and replies written in gloss, and review
comments from GitHub's Files tab. It skips plain PR comments from GitHub's Conversation tab: on a
shared account they look the same as a bot's, so write to your agents in gloss.

## As a library

`review` reads and writes the notes, `web` is an `http.Handler` to mount in another app, and `tui`
runs the console in the terminal and gives it back. One dependency, chroma, which lexes the code
for the highlighting; the rest is the standard library.

See [docs/design.md](docs/design.md) for where each note lives on GitHub and what its API allows.

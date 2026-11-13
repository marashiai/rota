<p align="center"><img src="assets/rota.png" width="128" alt="rota"></p>

# rota

A team of AI agents that maintains your software in shifts, also while you sleep: they raise issues
and work on them on their own, and tell you in a mail-style inbox what needs you. One installed command,
a team folder of Markdown files, Claude Code, git and `gh`. You review and merge every pull request.

This repository is only the source. The team itself lives in `~/.rota` (or `$ROTA_ROOT`, or `-root`; the
menu bar app, opened from Finder, reads `$ROTA_ROOT` and `$ROTA_CONFIG` from your login shell, so it picks
the same folder as rota in a terminal):

```
go build -o ~/.local/bin/rota .     # or: go install
rota init                           # creates ~/.rota from the built-in templates
```

## How it works

`rota start` is the whole scheduler: one foreground process, no cron, no daemon, and one per machine (a
second one refuses and says which runs, for which team folder). Every tick (hourly by default), and
whenever a shift ends, it takes the agents that are enabled, due (past their cooldown, or asked for),
inside their hours and with fair share of the team pool left (or a share the others cannot use before
the pool fills up again), and starts the one that has waited longest,
since it came due or was asked for: a queue in which an agent waits once, however often it is asked for,
and goes to the back when its shift starts, so no agent keeps the others waiting. Agents that began
waiting at once go by priority. It takes a lock (one agent at a time), and runs one "shift" with
`claude -p` inside `work/<agent>/`. What the agent learns from shift to shift, Claude Code's auto memory,
is kept in `memory/<agent>/` in the team folder (`--settings` sets `autoMemoryDirectory`), never under
`~/.claude`, so your own Claude Code sessions on the machine neither see it nor change it; the claude calls
rota makes for itself, such as the report's summary, keep no memory. Close the process and
everything stops: the running shift and every process it started are killed (SIGTERM, then SIGKILL after 5
seconds; a second Ctrl-C kills at once), the shift is recorded as "stopped", and nothing runs again until
you start it again. Run it inside rex, tmux or screen on the team machine.

The agent's final message is appended to `journal/<date>.md` (the shared changelog). Lines like
`@qa: the auth tests are flaky` are delivered to the qa agent's inbox and shown to it on its next shift. A
line `@human: <question>` is a question for you: it waits in `questions/`, the console and the menu bar
show it and macOS notifies you, and your answer, from either, goes to the agent's inbox and wakes it for a
shift, even one not due yet. Until then the agent's brief lists it, so it does not ask again. During a
shift on the organizer's machine an agent asks at once with `rota ask <question>`, keeps working, and
`rota wait <id>` brings it your answer. `rota ask` also takes a subject the inbox shows it by
(`--subject`), the answers you tap, the one it recommends (`--option`, `--recommend`), the steps of a to-do
you tick and mark done (`--step`), how much it matters (`--importance`) and what it is about (`--about`); after `answer_minutes` (30) without one it sets that work aside
for a later shift. You can dismiss a question instead of answering it: it closes, and the agent's inbox
says so. An agent withdraws its own question that no longer needs an answer with a line
`@human withdraw <id>` in its final message (its brief lists the ids), or `rota withdraw <id>` during
its shift. Closed questions are kept in `questions/closed/`.

An agent with `until_done: true` runs shift after shift while each ends with `More: yes`,
then waits for new work: an issue a scheduled teammate's shift opened, a teammate's note to it, a note
from you or your answer; `every` does not apply to it. After every shift the organizer checks that the
agent left each repository on its default branch with nothing uncommitted, and says so in the journal
and the agent's inbox when it did not.

Agents clean up after themselves. Once a pull request or issue an agent worked on is merged or closed,
the brief has it delete what it made for it: worktrees, build output, scratch folders, local branches,
and its remote branch with `rota delete-branch <repository> <branch>`, which deletes only the agent's
own branch (`<agent>/<topic>`), once every pull request from it is merged or closed, its tip is the head
of one of them and no open pull request is based on it. An agent deletes only its own files and
branches. Each agent also has a disk cap (`disk_gb`, 100 GB by default; 0 is none): once claude ends,
the machine that ran the shift measures the agent's work folder with `du`, the status, the console and
the pages show it against the cap, and the next brief says what it held. Over the cap, the journal says
so and the agent's inbox tells it to free its own unused files before anything else, or to ask you for
more, saying how much and why.
Before, during and after every shift the organizer asks Claude for the subscription's usage (the numbers
`/usage` shows). The rise is the shift's real share of the weekly window, recorded in `spend.csv` with the
exact token counts Claude reports. Journal, inbox and spend are committed and pushed.

```
~/.rota/
  rota.conf          every setting: init writes each one with what it does, at its default
  brief.md            the shift brief every agent gets; edit freely
  agents/<name>.md    front matter = schedule, share, repos, tools, model, effort, host; body = role (system prompt)
  secrets/<name>.env  KEY=VALUE put in that agent's environment; common.env for all
  secrets/<name>/<repo>.env  that agent's local setup for a repository: put into its checkout as .env
                      before each shift, {work} replaced by its work folder on that machine
  work/<name>/        the agent's cwd, repos and Claude memory; .disk/<name>.json: its size after the last shift
  journal/            one file per day, one section per shift
  inbox/              <agent>.json: the notes waiting for each agent, one record each (who, when, about
                      which pull request or issue, the text): from teammates, you and the organizer
  questions/          questions agents asked you, while open; closed/: answered, dismissed or withdrawn
  reports/            your report of each day: an executive summary, updated after each shift
  evidence/           screenshots, saved output and reviews that pull requests point to
  logs/               claude's full event stream per shift
  spend.csv           one row per shift
```

Make `~/.rota` a git repository if you want the journal, inbox and spend committed after every shift
(`commit`/`push` in rota.conf); the team never needs a repository of its own otherwise.

## Agents

rota ships no agents. An agent is one Markdown file, `agents/<name>.md` in the team folder: settings in
front matter, then its role, the prompt that says what it does. Write one by hand, or ask the Team in the
chat to add one; its page in the pages and the console's Agents tab edit every setting.

```
---
enabled: false
priority: 1
weight: 10
model: sonnet
effort: high
run_minutes: 30
tools: Read,Grep,Glob,Write,Bash(git *),Bash(gh *)
disallowed_tools: Bash(git push *),Bash(gh pr merge *)
repos: owner/app, owner/app-backend
every: 12h
---
You review the recent merges of the team's repositories for security problems and open one GitHub
issue per problem you find, with the evidence and the fix you would make. You do not change the code.
```

A new agent starts disabled; `rota enable <name>`, or On on its page in the pages, turns it on. Every
shift gets the brief (`brief.md`, the same for every agent) followed by the agent's role. The brief
tells it how the team works: the journal, notes to teammates, questions to you, GitHub conventions and
cleaning up after itself. `rota config` lists every agent setting with its default and what it does.

An agent's `after` lists teammates whose shifts ask for it: it waits its turn from the end of each, and
its shifts do not ask for them back. Evidence of a pull request's verification (screenshots, saved output,
Codex reviews) is kept in `evidence/<repository>/<branch>/` and shown in the pages at `/evidence/`. An
agent writes a report for you that does not fit its journal entry, or that it keeps up to date for you,
in `reports/<agent>/`, as the brief tells every agent: its page in the agents
tab links each, latest changed first, it opens at `/reports/<agent>/<file>`, and the console lists them
under Reports, after the days. A role links to the pages as `{pages}`, which the brief's placeholders fill
in too.

rota itself knows no agent by name. Of the GitHub labels it
defines `rota`, one `rota:<agent>` per agent, `rota:leave-alone` and `rota:approved`. Put
`rota:leave-alone` on an issue or pull request and no agent works on it, comments on it, labels it or
merges it until you take it off. Tell an agent to leave one alone, ignore it or drop it, and it puts the
label on for you. `rota:approved` is your approval of a pull request: rota puts it on when you approve
in a review, you may put it on by hand on github.com, and only you do (the brief tells every agent never
to add or remove it). The team's
own (priorities, verdicts, a label that holds a merge) are the `labels`
setting, `name color [emoji] description` separated by `;`; the emoji, if any, marks a pull request that
carries the label in the Reviews tab. The organizer makes them the same in every team repository when it starts and when the team's
agents, labels or repositories change (`rota labels` does it now); agents may not create or edit labels,
and the brief lists the team's labels as `{labels}`. `rota merge <repo>#<n> <head commit>` is a guarded
squash-merge for an agent whose role merges: it refuses at any other head commit, while the pull request
carries the `hold_label`, while a pull request or issue the
description says it `Depends on` is not merged or closed or carries that label, while it is stacked
on another open pull request, and while it is behind its base, so it was not checked with what the base
has now: the base must then be merged in and checked again. Before
merging one that others are stacked on, it points them at its base, so GitHub does not close them.
After the merge it rebases them onto it, and those stacked on them in turn, so each lists only its own
commits and changes, not the squashed ones below it: in rota's own clone (`.repos/` in the team folder,
made with `clone_cmd`), each commit made again with its author, date and message, then force-pushed only
if the branch has not moved since. One whose commits conflict with the merge, or whose own merge commits
hold a change a rebase would drop, is left as it is, and the merge's message says so: the base must then be
merged into it. Whether the merged branch is deleted is the repository's own setting.

`rota review-order "<repo>#<n> <why>"...` gives the order in which you review the open pull requests,
most worth it first, each with one sentence on why it comes there; it replaces the last order, and
with no argument it clears it. The
Reviews tab, in the pages and the console, lists every open pull request after the open ones it is
stacked on or its description says it `Depends on`, whatever the order says, and indents one that waits
for only one under it; one that waits for more stands on its own, after all of them, and says so. A
pull request is as urgent as the most urgent one that waits for it; those the order leaves out come
last, by repository and oldest first. Marks show where each stands, as GitHub has it: the emoji of each
label it carries that has one, ✅ for `rota:approved`, and ✋ for `rota:leave-alone`. Marks also say who waits on
whom over its notes: ⏳ while a note you sent waits on an agent's answer, 💬 once an agent answered one
since you last opened the review. Approved is the step after waiting for review, and so are ⏳ and 💬:
then the `review_label` is not shown, though it stays on GitHub.
The list narrows to one repository and to labels, as many as you like: it keeps the pull requests that
carry every one. A pull request's labels are its own and those of the open issues it is for: those GitHub
links to it (`Fixes #12`, or linked by hand), and those its title or the first line of its description
names (`Part of #12`). In the pages, the Reviews folder's Filter button chooses them, each label with how
many pull requests it keeps, and each that is on is a pill whose × takes it off; in the console, `r`
chooses the repository and `L` ticks labels. The console also narrows to one state (a mark, `m`), which
the pages show as a pill too. A link chooses them as well,
`/reviews/?repo=<owner>/<name>&mark=💬&label=priority:high&label=security`, and `/reviews/?all` none.
Narrowed, none is indented, and one that waits says for what. The page updates as the team does. What
narrows the list stays until you change it: on another tab and back, on another device, after a restart,
and in the console alike (`.review-filter.json`).

## Hosts: running agents on other machines

The organizer (`rota start`) stays on one machine. Any other machine on your tailnet can join it and
run shifts for it:

```
on the organizer:   rota pair                      # prints a one-time code, valid 10 minutes
on the host:        rota join <organizer> <code>   # organizer = its Tailscale name or address
                    rota join <organizer>          # afterwards: the saved token is enough
```

When a host joins, the organizer tells it which gh logins the agents need; the host checks those, and
whether its `claude` is logged in on the same account as the organizer, and reports back. `rota status`
shows `linux1 online, ready` or `linux1 online, missing gh team-bot`, and an agent is held with that
reason rather than failing a shift on a host that lacks something. Then give an agent `host: linux1` (that host), `host: any` (whichever joined host is free) or nothing
(the organizer itself). An agent whose host is offline simply waits; its memory, repos and secrets
stay on that host and are never copied elsewhere. With no host joined everything runs locally.

Security, by construction rather than configuration: the organizer listens on its Tailscale address
only; hosts open no port and make one outbound connection; a host pairs once with a code and keeps a
random token (mode 600), of which the organizer stores only the hash; the connection is the lifeline,
so when `rota start` is closed or the network drops, the host kills the shift it is running, children
included, and the organizer records it as stopped or lost.

A host needs the same command (`GOOS=linux GOARCH=amd64 go build -o rota .`, then copy), `rota init`
for its own `~/.rota`, Claude Code and `gh` logged in, and its own secrets. Its `rota.conf` only needs
the keys that differ (`claude_bin`, `worker_name`). Run `rota join` inside rex, tmux or screen.

## Set up the team machine

1. Install Go, git, `gh`, Tailscale and Claude Code; log `gh` and `claude` in, and install any Claude
   Code plugins your agents' tools name.
2. Build and install the command, then `rota init`. Add your agents as `~/.rota/agents/<name>.md` (see
   Agents) and edit `rota.conf`.
3. Log `gh` in as the account the agents should act as (`gh auth login`); `default_gh_user` in
   `rota.conf` names it, and each shift gets that account's token from gh itself. Per-agent overrides go
   in the agent file as `gh_user`, or as `GH_TOKEN` in `secrets/<agent>.env`.
4. Check: `./rota usage` must print the session and weekly percentages. If `claude setup-token` tokens are
   refused by the usage endpoint, run `claude` once interactively on the laptop so `~/.claude/.credentials.json`
   exists. Then `./rota status` and `./rota run -dry <agent>` to see the exact command and prompt.
5. First real shift by hand: `./rota run -force <agent>`. Read `journal/` and what it did on GitHub.
6. `./rota enable <agent>` for the agents you trust (shares are split among enabled agents only), then
   `./rota start`. It prints one line per tick and per shift; stop it with Ctrl-C.

Protect the default branch of every target repository and let CI run the heavy tests. The agents are told
not to run full builds or browser tests on this machine.

## The code

The core is a standalone package; everything you see is a thin layer over it.

```
core/    the team: settings, the schedule and the pool, shifts, hosts, the organizer, events, init
         (core/templates holds the starter files)
tui/     the console          web/   the pages          tray/   the menu bar (macOS)
md/      markdown, parsed once for the console and the pages
main.go  the commands
```

Every setting is declared once, as a tagged field of `core.Config` or `core.Agent`, with its default,
section, label and help. `rota init`, `rota config`, the console's Agents and Settings tabs, the agent
editor in the pages and the tray all read those declarations, so a new setting shows up everywhere.

## Tests

`go test ./...` runs behavior tests against a fake `claude`: pairing and rejection, a shift running on a
joined host, the host killing its shift when the organizer closes or the connection drops, the daily pool
holding an agent at its fair share, higher priority first, and stopping a shift when it runs out, retries
and their triage, hours, run now; every page of the console and the web, their edits, and the palettes'
contrast.

## Commands

```
rota                in a terminal: the console (rota ui)
rota init           create the team folder from the built-in templates
rota start          organizer: run shifts every tick_minutes until the process is closed
rota pair           print a one-time code for a host to join
rota join <organizer> [code]   this machine runs shifts for the organizer
rota run            run one shift now: the next eligible agent
rota run <agent>    run that agent (-force ignores enabled, cooldown, hours and the team pool)
rota enable <agent...>   turn agents on; rota disable <agent...> turns them off (edits agents/<name>.md)
rota status         usage, pool, shares, last runs, who is next; STATE says why an agent can or cannot run now
                     (on a terminal the console's status page, in color; piped, the plain table)
rota pause | unpause     stop picking new shifts | resume
rota kick [agent]        run the next shift now, or run that agent next (see "Run now")
rota cancel              stop the running shift; the organizer keeps going
rota stop                stop the organizer cleanly
rota tray                macOS menu bar item
rota web                 serve the pages while the organizer is off
rota log <agent> [n]     transcript of the latest (or n-th latest) shift
rota attach <agent>      follow the running shift
rota usage          what Claude reports about the rate windows
rota config         the effective configuration
rota version        which build this is (commit, build time) and where it lives
rota brief          the built-in shift brief
flags: -root DIR (default ~/.rota, or $ROTA_ROOT), -config FILE, -dry, -force, -now RFC3339
```

## The console

The console is beta: the pages are where you use rota. `rota` on its own (or `rota ui`) opens the team
in the terminal, with or without the organizer running, with these pages:

1. **Reports**: your report of each day, today's first; the console opens here.
2. **Status**: the organizer, Claude's 5-hour and weekly windows as bars, the team
   pool, hosts, the agents' questions for you, each agent's state, next and last run, share and model,
   and below it the running shift's tool calls as they happen, or the latest shifts.
3. **Reviews**: the team's open pull requests in the order to review them, each under what it waits
   for (see `rota review-order`); Enter or `v` reviews one in gloss, the whole change and commit by commit (see
   "Reviewing pull requests"), and `q` there comes back.
4. **Issues**: the open issues of the team's repositories, newest first; `o` orders them oldest first,
   then by priority, and `r`, `m` and `L` narrow them as on Reviews, kept for the pages too. Enter or
   `v` opens one on GitHub.
5. **Journal**: by day, rendered; `[` and `]` jump between shifts, `/` finds.
6. **Transcripts**: every shift with its outcome and turns; a running one follows its end (`f`), `x` shows
   the tool results.
7. **Inbox**: the notes waiting for each agent, as it reads them; `i` leaves one (see "Steering a shift"),
   `x` deletes one.
8. **Spend**: this week per agent, then
   every shift, with every column of spend.csv for the selected one; Enter opens its transcript. `B`
   proposes new shares in place of the shifts (see "Rebalancing the shares"): Enter changes a weight,
   `A` applies them all, Esc drops the proposal.
9. **Agents**: every setting of an agent, grouped (schedule, share of the team pool, Claude, where it works), with the
   team default shown where the file sets nothing. A switch toggles, a choice opens a picker, a value
   opens a line editor, `d` puts the team default back, and the role opens in `$EDITOR`. Values are
   checked before anything is written. `B` opens the proposal of new shares on Spend.
10. **Settings**, key `0`: the console's theme, then every rota.conf setting by section with what the built-in
   file says about it; `/` filters, Enter changes one (the line is rewritten in place, its comment
   kept), `E` opens the file.

Everywhere: `a` answers an agent's question (a new one rings the terminal's bell, and the tabs show how
many are open), `D` dismisses one without an answer, `p` pauses or resumes the team, `R` runs the next shift now, `c` stops the running shift,
`s` starts the organizer (in the background, output in `rota.log`) or stops it, `w` opens the web
pages. On a selected agent: `e` enables or disables it, `r` runs it now (the console says first when the
pool stands in the way), `t` opens its last Claude session and returns to the console afterwards,
`o` opens its latest transcript. `?` lists the keys; the mouse wheel scrolls and a click selects.

It is made to be read in any terminal. It asks the terminal whether its background is light or dark and
uses a palette made for that; every text color in both palettes meets WCAG AA contrast (4.5:1) on the
usual backgrounds and on the selection band, which a test checks. Nothing is said by color alone:
outcomes, states and the selection each have a shape too (✓ ✗ ■, ● ○ ◷ ▲, the ▌ marker), and every bar
has its number. `NO_COLOR`, or theme `mono` in Settings › Console, removes color altogether and shows the
selection in reverse video; `ROTA_THEME=dark|light|mono` overrides both for one run. Everything works
from the keyboard. For a screen reader, `rota status | cat` prints the plain text table.

## Settings

rota.conf holds only what someone running a team would change: the pool, the schedule, retries,
hosts, the pages, the menu bar, git, logs and the team's agent defaults. The team folder's layout,
timeouts and record formats are fixed. A setting that does not exist is ignored with a warning that
names the one you probably meant (`default_hourz` → `default_hours`, `hours` in rota.conf →
`default_hours`). A wrong value is an error; a running organizer reports it and tries again at its next
look, so a typo never stops the team. `rota version` says which build you are running.

## Install as an app (macOS)

```
./build.sh            # dist/rota.app and dist/rota.dmg, unsigned, no developer account needed
./build.sh install    # also copies rota.app to /Applications and links ~/.local/bin/rota to it
./release.sh v0.1.0   # tags, builds dmg + macOS and Linux binaries + checksums, publishes a GitHub release
```

Spotlight then finds "rota": opening it runs the tray, which on first launch links the terminal command
to the same binary if needed. Terminal and menu bar control the same team folder. The dmg is for other
Macs of yours: drag rota.app to Applications; since it is unsigned, open it the first time with
right-click, Open. The app imports PATH from your login shell, so `claude`, `gh` and `git` are found.

A running organizer notices when another rota is installed where it was started from, and restarts
with it within seconds: the pages and the schedule are the new version's without anyone restarting
it, and it stays the same process, so the menu bar that started it still controls it. A shift that
runs then stops between two of its steps, or while it waits for your answer (after 15 minutes at
most, whatever it does), and resumes its Claude session where it stopped, in the new version. One that will not start is reported in rota.log, and the
old one goes on. The menu bar does the same as soon as the new rota has settled: it runs the new
version in its place, an organizer it started goes on and stays its own, and an `rota web` it started
stops, since the new one starts its own when you open a page.

## The pages

`rota start` serves the team on `http://127.0.0.1:7900` as a mail client, on a phone and on a computer
alike. It is where you use rota:

- **Inbox**: what needs you in Focused, the smaller things in Other, and what the team did in Updates.
  Each agent's question is a thread: you answer it with a tap when it offers answers, tick off a to-do's
  steps, or write back. Each pull request that waits for your review is a thread too, with the notes you
  sent and the agents' answers to each; you review it in gloss and approve or reply from the thread. You
  star, snooze, archive or move a thread, by a swipe on a phone or a key on a computer (`?` lists them).
- **Folders**: Reviews (every open pull request, in the order to review it, each under what it waits
  for), Waiting (what an agent is on for you), Starred, Snoozed, Sent, All messages, Issues (the open
  issues, newest first, oldest first or by priority; one opens in the reading pane as GitHub has it,
  with its comments, the pull requests open for it and Open on GitHub) and, under Repositories, each
  repository's mail. Search finds messages, agents and `#numbers`. On a computer, the dividers between
  the folders, the list and the reading pane drag (or move with the arrow keys; a double click puts one
  back), each browser keeps its widths, and the reading pane keeps at least 400 pixels.
- **Filters**: the Filter button beside the tabs, a folder's title or the search box narrows the list:
  Unread, Starred, From, Type (questions, to-dos, pull requests, your messages, updates, reports),
  Repository and Date. Only the filters that are on show, each a pill whose × takes it off. With Unread
  on, the tabs count what is unread in each, and Mark all read reads the lot; what you open stays in the
  list until you change the filters. They keep across Focused, Other and Updates, and a folder opens
  without them. Reviews and Issues have a Filter button of their own: Repository and Label, each choice
  with how many it keeps (with more than one label, what carries every one), and Sort on Issues. What it
  chooses stays until you change it, on every device and in the console.
- **Messages**: write an agent (New message, or Message on its page) about anything, or about one of its
  pull requests or issues. Your message waits in Waiting until the agent reads it, and its answer comes
  to your inbox; you can take back one it has not read yet.
- **The team chat**: one chat for you and the whole team, docked on a computer and a tab on a phone. On
  a computer, its left and top edges and the corner between them drag, as the panes' dividers do. The
  Team, rota itself, may do anything within rota's scope, and nothing outside it: that is its one rule.
  The scope is rota itself, its agents, and their work and what they did: the team folder, all of it
  (settings, roles, journals, reports, logs, memory, secrets, and the agents' checkouts under `work/`),
  and on GitHub the team's repositories' pull requests, issues, releases, workflow runs and labels.
  Outside are the rest of this machine, other repositories, a repository's settings and deployed systems.
  It reads what is in scope itself, before it answers: the team folder with Claude's file tools, which
  restricted mode confines to it, and GitHub live with rota's own `github` tool, gh commands that only
  read, which rota refuses for any other repository; with each question it also gets the team as the
  organizer sees it (as `rota status` shows it: the pool, each agent's share and use, why each waits and
  until when, what a Run now would do, and a run asked for that has not started), your mail, the open
  pull requests and what came of what it proposed, with what rota answered when you applied it; so it
  says what the organizer will do, not what it guesses: a Run now for an agent whose fair share is used
  still waits, and its card says so, and until when, before you tap. It changes things by proposing them,
  a card you apply with a tap: tell it "go with B" and it answers what the agent asked; it writes an
  agent, ticks a to-do's steps, files your mail, and changes settings, roles, pauses, runs and agents; in
  the team's repositories it approves a pull request, sends a note on it or merges it, closes or reopens
  a pull request or an issue, puts labels on or takes them off, comments, opens an issue, and makes any
  other change of their work as a gh command (a rerun, a branch deleted, a release edited), all on GitHub
  as you, each checked to be the team's work and not a repository's settings. A merge you ask for is
  yours: it squash-merges also one that carries the `hold_label` or waits for what it depends on, leaves its
  labels on, merges one stacked on another after it and rebases
  those stacked on it, as `rota merge` does; one stacked on a pull request it does not merge goes into
  that one's branch, as its card says. A card shows at once that it is being done; a merge shows each
  pull request as it merges, and then that it merged or why not, and one that failed can be tried again.
  Undo works where that can be done (an answer, a run, an approval, a merge, a comment or a gh command
  stays). It is a Claude session (`chat_model`, default sonnet, or off) that runs no commands and changes
  nothing itself. An agent is online in the chat only while it works, during its turn: `@dev`
  reaches its running shift after its current step, and it answers you there. Offline, when it is not
  its turn, it gets your words as a message in its mail, which it reads at its next run. The chat shows
  each agent online or offline under its header, and why and until when it is offline; as you write
  `@` and a name it says which, and where your words go. An agent's page says it too, with Chat now
  while it is online.
- **Team**: whether the team works, the team pool and Claude's usage, and every agent, with Pause or
  Resume, and "Rebalance the agents' shares" (see "Rebalancing the shares"). Under each agent that does
  not run now though it could, its list and its page say why, by the organizer's own reasons: another
  agent's shift first, as one runs at a time; outside its hours; its share of the team pool used, or
  the rest kept for higher priority; the team paused; or a Run now you asked for, and what holds it.
  Each agent has a page:
  Message, Run now or Stop, Pause or Resume; its Messages; its Runs, each with its report and its
  transcript (following a running one), and the reports it keeps for you; its Role, edited in place; and
  its Settings: on or off, when it runs, Importance, model, and Advanced with every setting.
- **Settings**: how the inbox behaves, notifications on each device, the team's budget, the team's
  repositories, which you add and remove there (see "Repositories"), Open on my phone, and Advanced
  with every `rota.conf` setting.

In the pages, a switch or a choice outside Advanced applies as you tap it. What you change under
Advanced, in a role or in the shares waits for Save (Apply, for the shares): a bar at the bottom of the
screen says so as soon as anything waits, wherever you scroll, with Discard beside it, and each setting
changed and not saved has a dot. On a computer, ⌘S saves too. Leaving or closing the page with
something unsaved asks first.

Each folder, page and message you open is a step in the browser's history, so the browser's own back
goes back: on an iPhone a swipe from the screen's left edge, also in the app opened from the Home
Screen, and on a computer the back button. From a message it goes to its list, from a review in gloss
to the folder and the message you left, also once the browser has stopped keeping the page and loads
it again. `j` and `k` move within a step, and a message's back arrow is the browser's back.

A setting you change applies within seconds, whether in the pages, the console or a file: the organizer
looks again within two seconds of a change to `rota.conf` or an agent's file, and the pages and the hosts'
listener move when `web`, `web_port`, `listen` or `listen_port` change, without a restart.

Each page has an address (`/#team`, `/#agent:dev`, `/#run:<date>/<time>-<agent>`, `/#settings`,
`/#shares`), and the addresses of the pages before the app lead there. The reports an agent keeps for you
and the evidence it links to are pages of their own, at `/reports/` and `/evidence/`, rendered with a
small built-in Markdown renderer; today's report updates in place. The app updates itself as the team
does: it keeps a connection to `/events`, which says when the team folder changes. `rota web` serves the
pages while the organizer is off; the menu bar starts it when needed. Every control posts with a token the
page carries, so another site cannot use them; the files on disk stay the source of truth. Any program you
run as yourself on this machine, an agent with a shell included, can use the pages as you, as it can edit
the team folder.

With `web: tailscale` they are also served at this machine's Tailscale address (`rota start` prints
it), so you can follow the team and answer it from your phone. There is no login: anyone on your tailnet
can use them. The pages follow the address as Tailscale comes and goes, and the links the agents write to
their evidence (`{pages}` in a role) use it. With `web: local` or `off` they are this machine's only: when
Tailscale still serves them, as an earlier setting left it, rota has it stop and asks again until it has;
while Tailscale refuses, the pages are not served at all.

With `web: tailscale`, rota also has Tailscale serve the pages over HTTPS at this machine's tailnet name
(`tailscale serve`, with Tailscale's certificate; MagicDNS and HTTPS certificates must be on for the
tailnet), and the agents' links use that address. Something else Tailscale already serves on port 443 is
left alone. In Settings there, "Turn on" under This device registers it for notifications (`push.json` in
the team folder): when an agent asks you something or answers your review notes, each device gets a push
signed with the team's key (`secrets/web-push-key.pem`, made once), as Settings says which you hear of. The
push carries no content: the phone fetches the open questions and the answers you have not seen from the
pages over the tailnet and shows the newest; off the tailnet it only says an agent asks or answers you.
Tapping it opens the question, or the review. On an iPhone, add the page to the Home Screen first (Share,
Add to Home Screen) and turn notifications on from there; Android's Chrome needs nothing extra.

## Reviewing pull requests

The Reviews tab, in the pages and the console, opens a pull request in
[gloss](gloss/README.md): the whole change, and one commit at a time, with the notes beside the lines, and
the same vim-like keys in both (`?` lists them). `c` writes a note on the line, hunk, file or commit under
the cursor, `C` on the whole change, `r` replies, and each note is saved as you type as a draft in your
pending review on GitHub, which only you see. `S` sends them as one review; rota then leaves a note
about the pull request in the inbox of the agent that wrote it (its branch is `<agent>/…`, or it carries
`rota:<agent>`), which wakes it. `A`, or the Send button when you have no drafts, approves the pull
request: rota puts `rota:approved` on it and takes it out of draft at once, as gh's login on this
machine, and every agent whose role merges (its tools allow `rota merge`) is told. Those two
changes on GitHub are the whole record: rota keeps none of its own, and the label counts however it got
there. Only when you last opened each review is kept here (`.seen.json`): what the review read then
counts as seen. rota reads a pull request's notes with gloss again when GitHub says it changed, at most
every 5 minutes, also while no page is open; when an agent has answered you since, the Reviews tab marks it
💬, and your phone and the menu bar notify you, "dev answered you on app#12"; tapping it opens the
review. To take an approval back, take the label off. GitHub lets no one approve their own pull request,
and the agents open theirs from your account, so gloss writes its approval into the review. On the pages, a dialog waits
for GitHub and then says whether the review or approval went through; it finishes on this machine even
when you leave the page meanwhile.

A note you have read folds, the next time you open the review, to one line, and the read notes on the
whole change or a commit fold into a single line, so new ones (marked new) stand out among the agents'
status comments: you have read a note once it was on the screen, or once you fold it with `z` (or fold
beside it); `Z` or ✓ All read folds them all, and `z` or Enter opens a folded one. gloss keeps which
notes you have read in `gloss/read.json` in your config folder, for the pages, every device that opens
them and the console alike.

Every note lives on the pull request, so github.com shows them too. The agents use the `gloss` command
to write notes on the pull requests they open and to answer what you send, each reply in its thread. A
shift has
`GLOSS_AUTHOR` set to its agent's name, which gloss writes into each note, so the agents can share your
GitHub account and their notes still say who wrote them. The pages and the console use gh's login on
this machine.

The agents need the command on every machine that runs their shifts, in a folder on the shell's `PATH`:

```sh
GOBIN=~/.local/bin go install ./gloss/cmd/gloss     # from this checkout
```

## The menu bar (macOS)

`rota tray` puts the team in the menu bar: the bar shows the running agent and minutes, or idle, paused,
on hold or off, after `?N` while agents have N questions for you. Each question is a notification with a
field to answer it, and a menu item that opens a dialog to answer or dismiss it; an agent's answer to your
review notes is a notification that opens the review, and goes once you opened it; macOS asks once whether rota may
send notifications, and the menu says so when they are off. The menu lists every agent with its state and share, each with a submenu to toggle Enabled, Run
now, open its latest run, leave a note and open its work folder; then
hosts, Pause/Resume team, Stop current shift, Run next shift now, Start/Stop organizer, the inbox, the
team, today's report, and the folders.
The tray only reads the organizer's state file and acts through the same files and signals the CLI uses
(`rota pause`, `unpause`, `kick`, `cancel`, `stop`). Quitting the tray stops an organizer it started.
It needs the Xcode command line tools to build (cgo) and no Apple developer account.

## Watching a shift

While a shift runs, `rota start` prints one line per tool call (`live_log: tools`), or also the agent's
text (`all`), prefixed with the agent, and for a host with `host/agent`. The complete event stream of every
shift is written to `logs/<date>/<time>-<agent>.jsonl` as it happens (hosts forward theirs) and linked
from the journal entry. `rota log <agent>` prints the latest shift as a transcript, `rota attach <agent>`
follows the running one.

## Steering a shift

Write an agent from the pages (a message, or `@` it in the team chat while it is online, working), the console (`i`)
or the menu bar (Leave a note…): each reaches it as a note. You choose where it goes the way agents address each
other: `@dev` for its general notes, or `@dev app#554` for one pull request or
issue it works on. Those offered are the open ones (GitHub is asked at most every five minutes) it has
notes waiting about or named in its journal entries of the last two weeks, most recent first.

A note for an agent whose shift is running reaches that shift after its current step: the shift's
`claude -p` reads stream-json input, the organizer looks at the inbox every two seconds and sends what is
new as a message, and Claude echoes the message once it enters the conversation. Only then is the note
read: it leaves the inbox, the journal entry quotes it under "Read while it ran", and the transcript shows
it. A note the shift never took, because it was just finishing, stays for the next shift and wakes an
until-done agent, or any agent when it is yours. Otherwise a note waits for the agent's next shift, and a
note from you wakes the agent for it, a scheduled one before its interval is up. This works the same for a
shift on a joined host.

Agents leave each other notes in their final message: `@qa: the tests are flaky`. A note about one pull
request or issue names it, `@dev app-backend#72: not ready: no review`, and the organizer files
it as about that one, and the agent reads it under a heading of its own (`## owner/app-backend#72`),
so it uses it only when it works on that one and never mixes it up with a similar one. The repository may
be its full name, its name, or the end of its name after a dash when only one of the agent's repositories
has it; a note it cannot place is not guessed at but named in the journal. An agent leaves itself
follow-ups the same way. Every note is a record with an id, and a shift removes by id only the notes it
read: those it was briefed with and those that reached it while it ran.

## Repositories

The team's repositories are `default_repos` in rota.conf. Every agent works on them, unless its own
file has `repos:`, a list of its own that replaces the team's. In the pages, Settings › Team ›
Repositories lists them, each a link to it on GitHub. Add takes owner/name or a link to it on GitHub,
and adds it as GitHub names it once GitHub, asked through this machine's `gh`, says it exists: a name
mistyped would fail the clone before every agent's shift. × removes one, with Undo. Below, the page
names the agents with a list of their own, and "Use the team's" has one work on the team's instead.
Each change applies at once.

Before a shift the organizer clones each repo in `repos:` with `clone_cmd` (default `gh repo clone`; set
`git clone git@github-work:{repo}.git {dir}` for an SSH alias) or fast-forwards an existing clone. If that
fails the shift fails before claude starts and uses nothing. Per agent, `model` and `effort` (low to
max) choose how Claude Code runs the shift.

An agent works only on the repositories in its `repos`: the brief says so, whatever a journal entry, a
note, an issue or an old folder names. Before each shift on the organizer's machine, rota removes the
agent's checkouts of repositories no longer in its list (a clone named for its repository, its origin on
one of the team's GitHub owners), unless one holds work found nowhere else: uncommitted changes,
commits on no remote, a stash or other worktrees. That one is kept, and the journal says so, for you
to delete once it is not needed.

## Nothing waits forever

Every external wait is bounded: a shift by `run_minutes`, cloning and pulling by 10 minutes, measuring
the work folder after a shift by 3 minutes (a shift being stopped is not measured), every
short probe (the keychain, `gh`, `claude auth status`, git metadata, the login shell) by 10 seconds,
the usage endpoint by a 15-second request timeout and a fixed number of retries, host connections by
read, write and hello deadlines, a shift on a host by its timeout plus a grace, and page requests by
server timeouts. Ticks follow the wall clock, so sleep does not stall them, and a heartbeat every 30
seconds keeps the snapshot honest.

## When an agent runs

`every` is an agent's interval: one run is due that long after its last run (`1h`, `20h`, `1d`, `7d`).
A due agent that cannot run yet, because the organizer was off, the team was paused, the pool was
used up or its host was offline, stays due and runs as soon as it can, once: the next due time is
counted from the run that actually happened, so a missed hour never produces two runs. When several
agents are due, the organizer runs them back to back instead of one per tick. The team pool still limits
everything.

`hours` limits when a shift may start: local clock windows such as `05:00-07:00, 15:00-17:00`, per agent or
as `default_hours` for the team (empty: any time). A due agent waits for its next window, the organizer wakes
when a window opens, several due agents run back to back inside it, and `rota kick` ignores it. The windows
also decide when the agents open your 5-hour Claude window: Claude starts one at the first request after the
previous one ran out, so a window that begins five hours before you want a reset gives you results when you
sit down and a fresh window in the middle of your work: agents that start at 05:00 have results by 07:00
and leave you a reset at 10:00. Keep `every` shorter than a day so a run outside the window, say a kick at
noon, never pushes the next one a day out.

`days` limits the hours to weekdays, such as `fri` or `mon, thu` (empty: every day); a window past midnight
belongs to the day it starts. An agent with `own_hours: true` owns its windows: in them it goes ahead of
every agent but those you ask for, it starts no shift in a window's last 15 minutes, its brief gives it
only the minutes left until the window ends, and its shift stops when the window ends, which counts as a
run (`hours ended`). A shift already running when its window opens finishes first. An hour of the week
has one owner, or two owners would starve each other: where the owned hours of two enabled agents overlap,
the first by priority, then name, keeps them, and the other's `own_hours` is ignored with a warning; a
settings change that would make such an overlap is refused. With `days: fri`,
`hours: 05:00-08:00`, `every: 0` and `own_hours: true`, an agent works through Friday from 05:00 to 08:00,
shift after shift, as far as its share and the team pool allow.

Only a shift that finishes ok, or that the organizer stopped because the pool ran out, is a run. Anything else, a killed or interrupted shift, a timeout, an error
from claude, a lost host or a failed clone, is an attempt. The organizer then judges, from what the agent
was doing when it stopped, whether running the shift again would help: it asks `triage_model` (haiku, no
tools) for a verdict. A transient failure (interrupted, crashed, network, host gone) is retried
after `retry_minutes`, doubling with every attempt (15, 30, 60 minutes), up to `max_attempts`. A failure
that would only repeat (out of time or turns; going in circles; blocked by a login, permission or
decision) is not retried: the series ends as one failed run that counts like any other, so the next regular
run is due `every` later, and the organizer's one-line advice waits in the agent's inbox for that shift.
`rota status` shows `retry 2/3 at ...` while a series is open and `failed, due ...` afterwards; the journal
entry of every attempt carries the verdict. What an attempt consumed counts against the pool. "Run now"
(a kick) skips the wait. With `triage_model: off` every failure is retried up to `max_attempts`.

A shift that stops because the organizer stops (a restart, installing a new rota, a logout, a crash) is
not lost and not counted as an attempt. Every shift's Claude session is kept in `.resume/<agent>.json`
from its first event until it ends; when the organizer's stop ends it, the agent is asked to run at once
(and again when the organizer starts), so that shift resumes the session where it stopped (`claude
--resume`), on the same host, in the checkouts as they were (they are not synced), with the notes that
came since and what is left of `run_minutes` across interruptions (at least 15, but never past the end of
a window it owns). Status says `ready to
resume its interrupted shift`. A shift the operator stops, or that runs out of time or pool, starts over
as before.

## Your report

`reports/<date>.md` is an executive summary of where the team stands: one or two sentences, then what
waits for you, what was done today and what was found, each in a line with its link, about 200 words.
Every shift ends with a "For the human" section, or "none": an agent writes there only what is new and
needs you, such as a serious problem it found, never that nothing needs you, nor what already waits in
your inbox; it comes to you as its message. When one ends with something for you, or with a question, the organizer has `report_model` (sonnet, no tools) write the report anew from
the report as it stands, that section and the open questions. So it is never more than a shift behind,
and what is done no longer waits. Each day has its own report, and earlier days' reports are kept as
they were. A new day starts from the last day's: what waited still waits, and Done today starts empty.
The journal keeps the details. The lines are written first, under "Not summarized yet", and wait there
for the next rewrite when `report_model` gives no answer or is off; so does a line whose link the summary
left out. Nothing is dropped. Read it on the Reports page, also on your phone.

## Run now

"Run now" on an agent (`rota kick <agent>`, the tray, `r` in the console) marks that agent due now: a
file in `.run-now/` that says `human`, kept until its shift starts. The organizer runs it next, right
after the running shift, with its interval, hours and retry wait skipped, and ahead of every agent
waiting, even one a teammate asked for; the team pool still applies. Ask for several at once and all of
them run, back to back, in the order you asked; nothing is dropped. If the pool is used up the agent waits
for it and its state says so. When agents wake each other (an until-done agent with new work, a teammate
in `after`), and when you answer an agent or leave it a note, the request is an empty file there: it skips
the agent's interval, not its hours or a retry wait, and the agent waits its turn from when it was asked
for. While the team is paused only the agents you ask for run. "Run next shift now" without an agent runs
whoever is next.

Pause on an agent (the button on its page, `P` in the console, its menu in the menu bar, `rota pause
<agent>`) stops its new shifts until you resume it (`rota unpause <agent>`): its running shift finishes,
and it keeps what asks for it, its turn and its share of the pool, so it picks up what came in meanwhile.
Your Run now still runs it. Pause an agent while you work with another, so it does not take
the next shift first.

## The team pool

The only limit on what the agents use, read from Claude itself:

- **The pool.** Your weekly window is 100%, so one day is 14.3%. `team_pct: 20` lets the agents use 20%
  of that together: 2.86% of the weekly window per day, 20% per week. With `pacing: daily` a day's unused
  part does not carry over, so you always keep 80% of every day.
- **What you see is of the pool.** Every percent the pages, the console, the menu bar, `rota status`
  and the journal show is a percent of the team pool, the part of the window you gave the agents (of the
  day's pool with `pacing: daily`): a share of the pool reads as the agent's part of what the team may
  use, so with weights that add up to 100 each share is its weight. `spend.csv` keeps percents of the
  weekly window, as Claude reports them, and so do Claude's own readings of the account (the Claude line,
  and the account columns on Spend).
- **Fair shares, higher priority first.** Each enabled agent's fair share is `weight / sum of weights` of
  the pool. An agent may start a shift while it has share left. Agents of higher priority (a lower
  `priority` number) get theirs first: what they have not used yet that day is held for them while they
  can still run that day, so a lower-priority agent waits when the rest of the pool is theirs.
- **What an agent cannot use is free.** At every look the organizer estimates what each agent can still
  use of its share before the pool fills up again: the shifts its `every` and `hours` still allow, or as
  many as it ran lately at the same pace, whichever is more, each as much as its shifts used in the last
  14 days with a quarter to spare. The rest of its share is free: an agent whose own share is used may
  start a shift when one of its shifts, so estimated, fits in what is free. The shares never change, and
  the queue is the same: the one that waited longest goes first. Every estimate errs toward the agent
  whose share it is: an agent that works until done, runs `after` other agents, has fewer than 3 shifts
  in the last 14 days, or has no `every` keeps all it has left; a paused one frees it all. What is held
  for higher priority is what they can still use, not all they have left. Status shows what is free and
  what each agent frees, and an agent running on it says so in its state.
- **During a shift.** Nothing caps a shift but its `run_minutes` timeout, the end of a window it owns
  (`own_hours`) and the pool: the organizer reads
  Claude's weekly window and stops the shift once the team has used the day's pool. Such a shift counts
  as a run, is not retried, and its journal entry says why it stopped. It reads more often the closer the
  team comes to the end of the pool, in proportion to what is left: every 20 minutes with all of it,
  every minute with none.
- **Reading usage.** Claude's usage endpoint answers "rate limited" when it is asked too often, and its
  limit is not published. So it is asked only when a reading is needed: before a shift starts, after it
  ends (to measure it) and during it as above; the organizer's other looks use the last reading, and
  read again at least hourly. Every rota on the machine shares one reading, and the endpoint is asked at
  most once a minute, but for the reading after a shift. After a refusal it is not asked again for as long as it says (`Retry-After`), or
  else for twice the last wait, from 2 minutes up to 30; each reading after that halves the wait. Status
  says when the reading was made.

Claude reports whole percents, so the pool is kept to the nearest percent and one shift usually measures 0
or 1. The usage numbers are account-wide, so anything else you do on the same subscription during a shift
lands in that shift's share; on a dedicated machine with one agent at a time that noise is small. With
`usage: off` (an API-key setup, say) nothing limits the agents. With usage on, no shift starts while
Claude's usage cannot be read: the organizer is on hold and looks again every five minutes, and the
console, the web pages, the menu bar and `rota status` say so and why, until Claude answers again or you
set `usage: off`. The organizer reads a changed `rota.conf` within 30 seconds.

### Rebalancing the shares

"Rebalance the agents' shares" on the Team page (`B` in the console) proposes a weight for each enabled agent
from its shifts in the last 14 days of `spend.csv`, for you to change and then apply:

- **What an agent needs** is what one of its shifts used times the shifts it runs a week: as many as it
  ran, or as its `every` and `hours` allow, whichever is more. It gets that with a quarter to spare, and
  at least 1% of the weekly window a week, the step Claude measures in.
- **What one shift used.** Claude reports whole percents, so one shift usually measures 0 or 1. A
  shift's use is therefore its tokens' part of what all agents on the same model used, each kind
  weighted as Claude's prices weigh them: output five times input, writing the cache a quarter more than
  input, reading it a tenth.
- **An until-done agent** works for as long as it has share, so it takes what the others leave; several
  divide it by their weights. Without one, what is left is spread over the agents judged, in proportion
  to their needs.
- **Fewer than 3 shifts** tell too little: such an agent keeps its share. So does one whose share is
  about what it needs (within 15%, or half a percent of the window): the estimate moves a little with
  every shift that lands, for every agent at once, and a proposal made twice should say the same.
  When the needs exceed the pool, every share is cut alike, the until-done agents counted by what they
  used, and the proposal says so. A proposal never depends on the weights it replaces, other than to
  keep a share: applied and made again, it is the same.

The proposed weights add up to 100, so each is the agent's percent of the pool; as you change one, the
page shows the shares that follow. When a share would change, the bar at the bottom offers Apply,
which writes the weights into `agents/*.md` and goes back to the Team page; the organizer uses them
within seconds. Disabled agents are left as they are.

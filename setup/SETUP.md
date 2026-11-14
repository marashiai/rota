# Set up rota: instructions for Claude Code

You are Claude Code, setting up rota for the person you are talking to. Follow these steps in order. Talk
briefly, and ask as "How to ask" below says.

rota runs a team of Claude Code agents on this machine, in shifts, also while the person sleeps. Scouts
look for problems and open GitHub issues, a developer turns issues into draft pull requests, and the
person reviews and merges, from an inbox in the browser that works like mail. Everything the team knows
lives in a team folder of Markdown files (`~/.rota`).

**This version works only with Claude Code.** Every agent is a `claude -p` session, and the team's budget
is read from the person's Claude subscription. If they want to run their agents on another AI tool
(Codex, Gemini, Cursor, Copilot or anything else), stop and tell them rota cannot do that.

This setup runs in Claude Code in a terminal on the machine that will run the team. If you are running
in claude.ai/code or another cloud session, stop: you can't install anything on their computer from
there. If you are in the Claude desktop app, tell them the terminal is the supported way, since rota
needs the `claude` command there anyway.

## Rules for you during setup

- Change nothing in their repositories or on GitHub: no commits, pushes, issues, labels or repository
  settings. rota creates its own labels when it first starts, and only after the person has said yes.
- Ask before you install anything, and say what it is for.
- Never write a secret's value into an agent file, `rota.conf` or the chat. Secrets go only in
  `~/.rota/secrets/` (mode 600), either pasted by the person or filled in by them later.
- Everything you create goes in the team folder and in `~/.local/bin`. Clone repositories you inspect
  into a temporary folder and delete it at the end.
- Every agent you create starts disabled. The person enables them at the end.
- Never overwrite a team that is already there. If the team folder (`$ROTA_ROOT`, else `~/.rota`)
  already has agent files in `agents/`, this machine runs a team: build no other one, and change no
  agent file and no `rota.conf`. Show the person what `rota status` says, and offer to check that team,
  to add one new agent from a blueprint, or to stop.

## How to ask

Every question you ask follows these rules, so the person can set up a whole team mostly by pressing
Enter:

- **Ask with your question tool** (AskUserQuestion in Claude Code): a few related questions at a time,
  at most four, each with two to four answers to choose from. The person can always type their own.
- **Recommend an answer for every question, and make it the default.** Work it out from what you found
  in the repositories and from the guide's advice, put it first, and end its label with
  "(Recommended)", so taking it is the quickest choice. Say in one line why you recommend it.
- If a question closes without an answer (a question can time out), ask it again: silence is never a
  yes.
- **Never ask what you can find out yourself**, but do ask the person to confirm what you found: your
  findings are the recommended answers.
- **Keep every answer.** Before you create anything (step 5), show all the answers in one summary and ask
  for the final confirmation described at the end of step 4. Nothing is written until they confirm, and
  they can change any answer before they do.
- Without a question tool, ask in plain text in the same shape: numbered answers, the recommended one
  first and marked, and "ok" takes every recommendation.

## 1. Check what's needed

Check each item yourself and report the result in one short list.

| Needed | Check | If it's missing |
|---|---|---|
| macOS or Linux | `uname -sm` | Windows is not supported. `Darwin arm64` is an Apple Silicon Mac, which gets the ready-made app (macOS 13 or newer: `sw_vers -productVersion`); any other machine builds rota from source |
| The Claude Code CLI (the `claude` command), logged in with a Claude subscription such as Pro or Max | `claude --version`, and `claude auth status` shows `"loggedIn": true` and `"authMethod": "claude.ai"` | Install it: `curl -fsSL https://claude.ai/install.sh \| bash` (or `brew install --cask claude-code`), then `claude auth login`. The Claude desktop app does not install this command, and its login may not carry over: rota runs `claude -p` for every shift, so the CLI must work in a terminal. With an API key instead of a subscription, rota still works but nothing limits spending: set `usage: off` later, and warn them about the cost |
| Go, only to build from source: not on an Apple Silicon Mac | `go version` | macOS: `brew install go`; Linux: the distribution's package or go.dev/dl. Go fetches the exact version this repository needs by itself |
| git | `git --version` | `brew install git` or the distribution's package |
| GitHub CLI, logged in | `gh auth status` | `brew install gh`, then `gh auth login` |
| Tailscale (optional) | `tailscale status` (on macOS, when `tailscale` is not on the PATH: `/Applications/Tailscale.app/Contents/MacOS/Tailscale status`) | Only needed to manage the team from a phone or another computer, or to let other machines run shifts. Don't install it now: step 4 asks whether they want that |

The agents act on GitHub as a `gh` login on this machine. If the person has more than one, ask which
one the agents should use; that account opens the issues and pull requests. When it is not the active
login (`gh auth status` marks the active one), put `GH_TOKEN=$(gh auth token --user <login>)` in front of
every `gh` command you run during setup. Never run `gh auth switch`: it changes their `gh` for
everything else too.

## 2. Install rota

First see what is there: `command -v rota gloss`, and whether `rota.app` is in `/Applications` or
`~/Applications`. If the person installed the app themselves (the README's download), `rota` and `gloss`
already point into it: just run `rota init`. If a `rota` or `gloss` exists that is not rota's, ask before
replacing it. Make sure `~/.local/bin` is on the `PATH` of their login shell; if it isn't, offer to add
it to their shell profile. If the team folder already has agents, a team runs here: install nothing,
and follow the rules above instead of the rest of this guide.

**On an Apple Silicon Mac** (`uname -sm` prints `Darwin arm64`), install the app from the latest
release. It holds both commands and the menu bar app, from which they can start and stop the team and
answer questions:

```sh
tmp=$(mktemp -d)
curl -fsSL -o "$tmp/rota.dmg" https://github.com/marashiai/rota/releases/latest/download/rota-macos-arm64.dmg
hdiutil attach -nobrowse -quiet -mountpoint "$tmp/mnt" "$tmp/rota.dmg"
dest=/Applications; [ -w "$dest" ] || dest=~/Applications; mkdir -p "$dest"
rm -rf "$dest/rota.app" && cp -R "$tmp/mnt/rota.app" "$dest/"
hdiutil detach -quiet "$tmp/mnt" && rm -rf "$tmp"
mkdir -p ~/.local/bin
ln -sfn "$dest/rota.app/Contents/MacOS/rota" ~/.local/bin/rota
ln -sfn "$dest/rota.app/Contents/MacOS/gloss" ~/.local/bin/gloss   # the agents write review notes with it
rota init                                                           # creates the team folder ~/.rota
```

Downloaded from the terminal, the app opens without macOS asking first, although it isn't signed with an
Apple Developer ID.

**On any other machine** (an Intel Mac or Linux), build it from the root of this repository:

```sh
mkdir -p ~/.local/bin
go build -o ~/.local/bin/rota .
go build -o ~/.local/bin/gloss ./gloss/cmd/gloss    # the agents write review notes with it
rota init                                           # creates the team folder ~/.rota
```

On an Intel Mac, offer the menu bar app too: `./build.sh install` builds `rota.app` (it needs the Xcode
command line tools: `xcode-select --install`), puts it in /Applications (or `~/Applications` when that
isn't writable), and links `~/.local/bin/rota` and `gloss` to it.

Then run `rota usage`. It should print the session and weekly percentages of their Claude subscription:
that is what the team's budget is measured against. If it fails, explain that the budget can't be
enforced and set `usage: off` in step 5 if they want to go on anyway.

## 3. Learn their repositories

Ask which GitHub repositories the team should look after (`owner/name` or links). Then study each one
yourself before asking anything about it:

- `gh repo view <repo>`, then a shallow clone into a temporary folder:
  `gh repo clone <repo> <temp dir> -- --depth 50`. If that fails with "Permission denied (publickey)",
  their SSH key may sit behind a host alias: look in `~/.ssh/config` for a `Host` whose `HostName` is
  `github.com`, and clone with `git clone --depth 50 git@<alias>:<repo>.git <temp dir>`. Remember which
  way worked: step 5 needs it.
- Read the README and CONTRIBUTING, the manifests (package.json, Cargo.toml, go.mod, pyproject.toml,
  Gemfile, pom.xml, composer.json), Dockerfiles and compose files, `.github/workflows/`, infrastructure
  code (Terraform, CDK, serverless.yml, Kubernetes manifests, fly.toml, vercel.json, netlify.toml), and
  how the tests run.
- Look at the open issues and pull requests (`gh issue list`, `gh pr list`) and recent history
  (`git log --oneline -30`): how active it is, and how they work.

Write a profile for each repository and show all of them in one short table:

| Field | Examples |
|---|---|
| Kind | web front end only, backend API, full stack, library or package, CLI, mobile app, infrastructure, docs or marketing site |
| Stack | languages, frameworks, package managers |
| Build and test | the exact commands, how long the suites take, what they need (a database, Docker, secrets) |
| Deployment | automatic on merge (which workflow), Vercel or Netlify, AWS (ECS, Lambda, EC2, S3 and CloudFront), a Docker image pushed to a registry, Kubernetes, Fly, Render or Heroku, a server over SSH, an app store, a published package, or not deployed |
| Environments | production, staging or dev, local; their URLs |
| Users | a public site found through search, an app with users, an internal tool, an API that other repositories call |
| Errors and logs | PostHog, Sentry, Datadog, CloudWatch, plain logs, nothing; and whether Claude Code can read them (a plugin, an MCP server, a CLI) |
| Sensitive | user data, payments, secrets, anything irreversible |

Then ask about each repository, with what you found as the recommended answers:

1. **Profile:** is the table right? ("Yes, it's right (Recommended)", or which field to correct.)
2. **Running it:** how it runs locally, such as `npm ci && npm start`, `docker compose up`, or "it
   can't run locally".
3. **Testing it:** the test commands, which suites matter, which are slow or flaky, and whether the
   agents may run them all.
4. **What a run needs:** environment variables, services (a database, Docker), test accounts and
   secrets. Secrets go in `~/.rota/secrets/<agent>/<repository name>.env`, which rota copies into the
   agent's checkout as `.env` before each shift; the person fills in the values.
5. **Deployment:** how and where it reaches production, and whether that is automatic on merge.
6. **Off limits:** anything the agents must never run or touch, such as deploy scripts, production
   servers or paid APIs. Recommend what you found (for example `./deploy.sh` and `ssh`). Each answer
   becomes a `disallowed_tools` entry in step 5, such as `Bash(./deploy.sh *)` or `Bash(ssh *)`, not
   only a sentence in a role.

## 4. Decide on the team together

The blueprints in `setup/blueprints/` are proven roles. Each one says when to include it, what to fill
in, and how to adjust it when another role is left out.

| Blueprint | Kind | What it does |
|---|---|---|
| `security` | scout | Reviews recent code and the infrastructure for vulnerabilities; opens issues |
| `upstream` | scout | Daily dependency audits, advisories, runtime and provider deprecations; opens issues |
| `errors` | scout | Finds the causes of production errors in the error tracker; opens issues |
| `seo` | scout | Audits public pages for search and speed; opens issues |
| `qa` | scout | Tests what was merged, end to end, on a non-production environment; tags releases |
| `developer` | builder | Turns issues into draft pull requests with tests, evidence and review notes |
| `maintainer` | builder | Keeps every open pull request mergeable: merges the base in, resolves conflicts |
| `triage` | gatekeeper | Judges each pull request; merges low-risk ones itself, holds the rest for the person |
| `rollout` | coordinator | Lists the manual steps of changes and releases, and follows them up with the person |
| `reflection` | coordinator | Weekly: looks back at what each agent delivered for its budget, and proposes how to divide it |

Recommend a team from the profiles, smallest first, and explain each choice in one line. Ask with the
recommended team as the default answer, and a smaller and a larger team as the others. A good start for
most people is **security, upstream and developer**: the scouts open issues, the developer opens draft pull
requests, and the person reviews and merges everything. Then add:

- `errors` when an app's errors are collected somewhere Claude Code can read;
- `seo` when a repository serves public pages found through search;
- `qa` when there is a non-production environment it can run or reach;
- `maintainer` when pull requests stay open long enough to fall behind;
- `triage` only when they want low-risk merges done without them;
- `rollout` when getting a change live takes steps by hand (setting a secret, running a migration,
  changing a cloud setting), with or without the triage;
- `reflection` after a few weeks, with three or more agents.

Leave out a role whose blueprint needs a fact you don't have: `seo` without a live URL, `qa` without an
environment it may use, `errors` without an error tracker Claude Code can read. Tell the person what
would make it possible, so they can add it later.

Then ask about these, each with the recommended answer as the default:

1. **Autonomy.** Issues only (scouts alone), draft pull requests the person merges (recommended to
   start), or also merges of low-risk pull requests by the triage.
2. **Schedule.** Their time zone and when they start and stop working. Shifts start only inside
   `hours` windows. Recommend a window that ends an hour before they start work, so the results wait for
   them in the morning, for example `05:00-07:00`, and a second one in the afternoon if they want two
   rounds a day. Claude's 5-hour usage window starts with the first request after the last one ran out,
   so a team that starts at 05:00 also gives them a fresh window around 10:00.
3. **Budget.** How much of their Claude limit the agents may use: `team_pct`, a share of each day's
   seventh of their weekly window, for all agents together. Their own use of Claude stays theirs, and
   rota stops a shift when the share is used up. Run `rota usage` and show where they stand this week.
   Offer "20% (Recommended)", "10%" and "35%", and recommend less when `rota usage` shows they already
   use most of their week. That is the only limit on the team, and they can change it any time.
4. **Account and author.** Which `gh` login the agents act as (`default_gh_user`). The agents' commits
   carry this machine's git identity (`git config user.name` and `user.email`); if they should carry
   another, step 5 sets it.
5. **Remote access.** Whether they want to manage the team from their phone and other computers, with
   a notification when an agent asks them something. rota does that over Tailscale: it serves the
   inbox on their tailnet only, never on the internet, and without a login, so anyone on their tailnet
   can use it. Recommend "Yes, over Tailscale" when Tailscale worked in step 1, else "Later": they can
   turn it on any time with Settings › Team › Open on my phone. Installing Tailscale is theirs to do
   (the app from tailscale.com, signed in on this machine and their phone).

### Confirm before you create anything

Show one summary of every answer: each repository's profile with how to run, test and deploy it and
what is off limits; the team, each agent with its repositories and when it runs; the autonomy, schedule,
budget, GitHub account and remote access. Then ask one question: "Create it (Recommended)" or "Change an
answer". To change one, ask which, ask that question again with their previous answer as the default,
and show the summary again. Go on to step 5 only when they choose to create it.

## 5. Create the team

Write `~/.rota/rota.conf`. `rota init` already wrote every setting there with what it does:
`week_start` and `default_permission_mode` set, every other one commented out at its default. Set values
by uncommenting or editing those lines, not by adding duplicates.

- `default_repos`: every repository, comma-separated.
- `default_gh_user`, `default_hours` and `team_pct`.
- `timezone`: their time zone as an IANA name (such as `Europe/Amsterdam`) when it differs from this
  machine's; `hours` are read in it.
- `clone_cmd` only if `gh repo clone` did not work in step 3: `clone_cmd: git clone git@<alias>:{repo}.git {dir}`.
- `web: tailscale` when they chose remote access.
- `web_port: 7901` (or another free port) and `listen: off` when something already answers on port
  7900 (`curl -s -o /dev/null http://127.0.0.1:7900/` succeeds): another rota runs on this machine,
  perhaps another user's, and this team's inbox would otherwise open that one's.
- The team's labels. Every team needs the three priority labels: the scouts put one on each issue.
  Add `rota:needs-rollout` and the `hold_label` line when the team has a developer (it marks changes
  that need a step by hand), and the triage's three labels and the `review_label` line when it has the
  triage. With all of them:

```
labels: priority:high B60205 Fix first; priority:medium FBCA04 Fix after the high-priority ones; priority:low C2E0C6 Fix when nothing more important is left; rota:auto-merge 0E8A16 🤖 Ready, and needs no review: the triage merges it; rota:needs-review 1D76DB 👀 Ready: waits for the human's review; rota:not-ready D93F0B 🔙 Sent back to its author: the triage's comment says what is missing; rota:needs-rollout F9D0C4 🚀 Needs a step by the human: it and what depends on it wait until the label is removed
hold_label: rota:needs-rollout
review_label: rota:needs-review
```

  Leave out what the team doesn't need: without the triage, drop `rota:auto-merge`, `rota:needs-review`
  and `rota:not-ready` and the `review_label` line; without a developer, also `rota:needs-rollout` and
  the `hold_label` line. A `hold_label` must be one of the labels, or `rota status` warns. Nobody
  creates these labels by hand: rota creates them in every repository (step 6). `docs/github.md` says
  what each label means, if the person asks.

For each agent, write `~/.rota/agents/<name>.md` from its blueprint's "Agent file" block:

- Keep the blueprint's name (`developer`, `security` and so on). The roles refer to each other by these
  names; if you rename one, change the references in the others.
- Replace every `<<PLACEHOLDER>>` with what you learned in step 3, following the blueprint's "Adapt"
  section. Write facts, not examples: the real test commands, URLs, environments and tool names.
- Give an agent only the repositories it needs with `repos:` when that differs from `default_repos`
  (for example `seo` only on the public site's repository).
- Apply the blueprint's notes for every role you left out, so no agent waits for a teammate that
  doesn't exist.
- Keep `enabled: false`.

If the agents' commits should carry another identity than this machine's git config, put it in
`~/.rota/secrets/common.env`, which rota puts in every shift's environment:

```
GIT_AUTHOR_NAME=<name>
GIT_AUTHOR_EMAIL=<email>
GIT_COMMITTER_NAME=<name>
GIT_COMMITTER_EMAIL=<email>
```

(`git_author` in `rota.conf` is something else: the author of rota's own commits in the team folder,
when that folder is a git repository.)

Secrets: if an agent needs a token beyond `gh` and Claude (an error tracker's API key, a read-only cloud
profile), tell the person which file it goes in (`~/.rota/secrets/<agent>.env` for one agent,
`~/.rota/secrets/common.env` for all, `KEY=VALUE` per line) and let them fill it in, or paste it to you
if they prefer. Then `chmod 600` the file.

Leave `~/.rota/brief.md` as `rota init` wrote it: it is the shared prompt of every shift (the journal,
notes between agents, questions to the person, GitHub conventions). Change it only if they ask.

## 6. Check it

1. `grep -n '<<' ~/.rota/agents/*.md` finds nothing.
2. `rota status` lists the agents, disabled, and prints no warnings.
3. `rota run -dry <agent>` for each agent prints the exact claude command and prompt. Read them: the
   repositories, tools and role should be right.
4. Offer one real shift of a scout, which only reads and opens issues. Its issues need the team's
   labels, which the organizer creates when it starts; so first, with their yes, run `rota labels`: it
   creates them in every repository and changes nothing else. Then `rota run -force security`. It takes
   a few minutes. Show them what it reported: `rota log security`, and the issues on GitHub.

## 7. Start the team

Ask which agents to turn on, then `rota enable <name> ...`. Then start the organizer, which schedules the
shifts and serves the inbox at http://127.0.0.1:7900 (or the `web_port` you set). Closing it stops
everything. Offer the way that
suits them:

- the menu bar app (macOS): open rota from Applications and choose Start;
- the inbox page: `rota web`, open the address it prints and press Start;
- a terminal that stays open: `rota start`, inside tmux or screen.

The organizer's own GitHub work (the inbox's lists, the review pages, keeping the labels the same in
every repository) acts as the active `gh` login, the person's own, while each shift acts as its agent's
`gh_user`. If the active login can't see a repository, the organizer can run with
`GH_TOKEN=$(gh auth token --user <login>)` in its environment, but then every shift acts as that login
too, whatever its `gh_user` says. Do that only when it is the agents' login anyway.

With remote access, check within a minute that Tailscale serves the inbox: `tailscale serve status`
lists `https://<name>.ts.net` with a proxy to `http://127.0.0.1:7900` (or the `web_port`). If it
doesn't, or the organizer warns `no HTTPS on the tailnet`, ask them to turn on MagicDNS and HTTPS
certificates on the DNS page of Tailscale's admin console. rota tries again by itself.

## 8. Hand over

End with a short summary:

- the team in a table: each agent, what it does, its repositories, when it runs;
- where to look: the inbox (http://127.0.0.1:7900 unless `web_port` changed it), where questions wait for an answer, pull requests
  for review, and each agent's messages;
- with remote access: the inbox's address on their tailnet, `https://<name>.ts.net`; on an iPhone, add
  it to the Home Screen (Share, Add to Home Screen), open it from there and turn on notifications in
  Settings › Notifications › This device; that anyone on their tailnet can use the inbox; and that this
  machine must stay on and awake;
- how to change the team: ask the Team in the inbox's chat ("make the developer run only at night",
  "add an agent that watches the docs") and accept its proposal, or edit `~/.rota/agents/*.md`, which
  takes effect at the next shift;
- what to read next: the README's tour, and `docs/reference.md` for every detail.

Delete the temporary clones you made in step 3.

# Set up rota: instructions for Claude Code

You are Claude Code, setting up rota for the person you are talking to. Follow these steps in order. Talk
briefly, ask a few questions at a time, and always offer a recommended answer.

rota runs a team of Claude Code agents on this machine, in shifts, also while the person sleeps. Scouts
look for problems and open GitHub issues, a developer turns issues into draft pull requests, and the
person reviews and merges, from an inbox in the browser that works like mail. Everything the team knows
lives in a team folder of Markdown files (`~/.rota`).

**This version works only with Claude Code.** Every agent is a `claude -p` session, and the team's budget
is read from the person's Claude subscription. If they want to run their agents on another AI tool
(Codex, Gemini, Cursor, Copilot or anything else), stop and tell them rota cannot do that.

## Rules for you during setup

- Change nothing in their repositories or on GitHub: no commits, pushes, issues, labels or repository
  settings. rota creates its own labels when it first starts, and only after the person has said yes.
- Ask before you install anything, and say what it is for.
- Never write a secret's value into an agent file, `rota.conf` or the chat. Secrets go only in
  `~/.rota/secrets/` (mode 600), either pasted by the person or filled in by them later.
- Everything you create goes in the team folder and in `~/.local/bin`. Clone repositories you inspect
  into a temporary folder and delete it at the end.
- Every agent you create starts disabled. The person enables them at the end.

## 1. Check what's needed

Check each item yourself and report the result in one short list.

| Needed | Check | If it's missing |
|---|---|---|
| macOS or Linux | `uname` | Windows is not supported |
| Claude Code, logged in with a Claude subscription (Pro or Max) | `claude --version`, `claude auth status` | `claude` to log in. With an API key only, rota still works but nothing limits spending: set `usage: off` later, and warn them about the cost |
| Go | `go version` | macOS: `brew install go`; Linux: the distribution's package or go.dev/dl. Go fetches the exact version this repository needs by itself |
| git | `git --version` | `brew install git` or the distribution's package |
| GitHub CLI, logged in | `gh auth status` | `brew install gh`, then `gh auth login` |
| Tailscale (optional) | `tailscale status` | Only needed for notifications on a phone or to let other machines run shifts; skip it for now |

The agents act on GitHub as a `gh` login on this machine. If the person has more than one, ask which
one the agents should use; that account opens the issues and pull requests. When it is not the active
login (`gh auth status` marks the active one), put `GH_TOKEN=$(gh auth token --user <login>)` in front of
every `gh` command you run during setup. Never run `gh auth switch`: it changes their `gh` for
everything else too.

## 2. Build and install

From the root of this repository:

```sh
mkdir -p ~/.local/bin
go build -o ~/.local/bin/rota .
go build -o ~/.local/bin/gloss ./gloss/cmd/gloss    # the agents write review notes with it
rota init                                           # creates the team folder ~/.rota
```

Before that, check `command -v rota gloss`: if either already exists and is not from this repository,
ask before replacing it. Make sure `~/.local/bin` is on the `PATH` of their login shell; if it isn't,
offer to add it to their shell profile.

On macOS, offer the menu bar app too: `./build.sh install` builds `rota.app` (it needs the Xcode command
line tools: `xcode-select --install`), puts it in /Applications (or `~/Applications` when that isn't
writable), and links `~/.local/bin/rota` to it.
From the menu bar they can start and stop the team and answer questions.

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

Then ask only what you could not find out. Typical questions: "How does `shop-api` get to production?",
"Is there a staging environment the QA agent may use, and with which test account?", "Where do you see
errors from the app?" Always ask: "Is there anything the agents must never run or touch?" (deploy
scripts, production servers, paid APIs). Each answer becomes a `disallowed_tools` entry in step 5, such
as `Bash(./deploy.sh *)` or `Bash(ssh *)`, not only a sentence in a role.

## 4. Decide on the team together

The blueprints in `setup/blueprints/` are proven roles. Each one says when to include it, what to fill
in, and how to adjust it when another role is left out.

| Blueprint | Kind | What it does |
|---|---|---|
| `security` | scout | Reviews recent code and the infrastructure for vulnerabilities; opens issues |
| `stack` | scout | Daily dependency audits, advisories, runtime and provider deprecations; opens issues |
| `errors` | scout | Finds the causes of production errors in the error tracker; opens issues |
| `seo` | scout | Audits public pages for search and speed; opens issues |
| `qa` | scout | Tests what was merged, end to end, on a non-production environment; tags releases |
| `developer` | builder | Turns issues into draft pull requests with tests, evidence and review notes |
| `maintainer` | builder | Keeps every open pull request mergeable: merges the base in, resolves conflicts |
| `triage` | gatekeeper | Judges each pull request; merges low-risk ones itself, holds the rest for the person |
| `rollout` | coordinator | Lists the manual steps of changes and releases, and follows them up with the person |
| `reviewer` | coordinator | Weekly: recommends how the budget is divided between the agents |

Recommend a team from the profiles, smallest first, and explain each choice in one line. A good start
for most people is **security, stack and developer**: the scouts open issues, the developer opens draft
pull requests, and the person reviews and merges everything. Then add:

- `errors` when an app's errors are collected somewhere Claude Code can read;
- `seo` when a repository serves public pages found through search;
- `qa` when there is a non-production environment it can run or reach;
- `maintainer` when pull requests stay open long enough to fall behind;
- `triage` only when they want low-risk merges done without them;
- `rollout` when getting a change live takes steps by hand (setting a secret, running a migration,
  changing a cloud setting), with or without the triage;
- `reviewer` after a few weeks, with three or more agents.

Leave out a role whose blueprint needs a fact you don't have: `seo` without a live URL, `qa` without an
environment it may use, `errors` without an error tracker Claude Code can read. Tell the person what
would make it possible, so they can add it later.

Then ask about these, with your recommendation for each:

1. **Autonomy.** Issues only (scouts alone), draft pull requests the person merges (recommended to
   start), or also merges of low-risk pull requests by the triage.
2. **Schedule.** Their time zone and when they start and stop working. Shifts start only inside
   `hours` windows. Recommend a window that ends an hour before they start work, so the results wait for
   them in the morning, for example `05:00-07:00`, and a second one in the afternoon if they want two
   rounds a day. Claude's 5-hour usage window starts with the first request after the last one ran out,
   so a team that starts at 05:00 also gives them a fresh window around 10:00.
3. **Budget.** `team_pct` is the share of each day's seventh of their weekly Claude window that the
   agents may use together. The default is 20. That is the only limit on the team.
4. **Account and author.** Which `gh` login the agents act as (`default_gh_user`). The agents' commits
   carry this machine's git identity (`git config user.name` and `user.email`); if they should carry
   another, step 5 sets it.
5. **Phone.** Whether they want notifications and the inbox on their phone. That needs Tailscale and
   `web: tailscale`; it can be added later.

## 5. Create the team

Write `~/.rota/rota.conf`. `rota init` already wrote every setting there with what it does:
`week_start` and `default_permission_mode` set, every other one commented out at its default. Set values
by uncommenting or editing those lines, not by adding duplicates.

- `default_repos`: every repository, comma-separated.
- `default_gh_user`, `default_hours` and `team_pct`.
- `timezone`: their time zone as an IANA name (such as `Europe/Amsterdam`) when it differs from this
  machine's; `hours` are read in it.
- `clone_cmd` only if `gh repo clone` did not work in step 3: `clone_cmd: git clone git@<alias>:{repo}.git {dir}`.
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
  the `hold_label` line. A `hold_label` must be one of the labels, or `rota status` warns.

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

## 8. Hand over

End with a short summary:

- the team in a table: each agent, what it does, its repositories, when it runs;
- where to look: the inbox (http://127.0.0.1:7900 unless `web_port` changed it), where questions wait for an answer, pull requests
  for review, and each agent's messages;
- how to change the team: ask the Team in the inbox's chat ("make the developer run only at night",
  "add an agent that watches the docs") and accept its proposal, or edit `~/.rota/agents/*.md`, which
  takes effect at the next shift;
- what to read next: the README's tour, and `docs/reference.md` for every detail.

Delete the temporary clones you made in step 3.

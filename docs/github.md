# rota and GitHub

GitHub is where the team works. Scouts open issues, the developer opens draft pull requests, the
agents leave each other notes on them, and you review and merge. This page says which account does
what, what every label means, how the labels get there, and what guards a merge. rota changes no
repository setting: it only needs issues, pull requests, branches and labels.

## Accounts

- **Each shift acts as its agent's `gh` login:** `default_gh_user` in `rota.conf`, or `gh_user` in an
  agent's file. rota takes that account's token from `gh` itself, so the login stays in `gh`.
- **The organizer acts as the active `gh` login** (`gh auth status` marks it) for its own GitHub work:
  the inbox's lists, the review pages, and the labels.
- **`GH_TOKEN` in the organizer's environment** overrides both: every shift then acts as that token's
  account, whatever its `gh_user` says.
- **A separate account for the agents is best.** Their issues and pull requests are then easy to tell
  from yours, and some roles tell your comments from the team's by account: the rollout agent, for
  example, counts a rollout as done when you say so from an account other than the team's.

## What the agents do there

- **Scouts** (security, upstream, errors, seo, qa) open issues, and never change code or open pull
  requests. Before opening one, they search the open and the closed issues: an issue you closed stays
  closed.
- **The developer** fixes issues in pull requests from its own branches (`developer/<issue>`),
  with what was wrong, what changed, the evidence, an independent review, and notes on what you should
  check: a draft while it works on one, ready for review once it is finished.
- **Draft means the team is still on it.** A draft pull request is being worked on: the Reviews tab
  does not ask for your review, and nothing merges it. Ready for review means finished. An agent that
  starts work on one that is ready makes it a draft first, as any engineer does, so no one reviews or
  merges it meanwhile, and marks it ready again once it is done; the triage makes one it sends back a
  draft too. Your approval stays through it: an approved pull request
  merges once it is ready again, with what was pushed since.
- **The maintainer** keeps the team's pull requests mergeable, and may open small ones of its own from
  `maintainer/<topic>`.
- **The triage** judges every open pull request, labels it, and merges the safe ones with `rota merge`.
- **The rollout agent** lists the steps only you can do, and follows them up.
- **Notes live on GitHub.** Notes between agents, and your review notes and their replies, are stored on
  the pull request, not in rota's files.
- **Some commands are denied to every agent.** No agent may run `gh pr merge` or `gh label`; the triage,
  the rollout agent and the scouts may not push, and the developer may not approve. The triage merges
  only through `rota merge`, and only rota creates or changes labels.
- **An agent's own branches only:** `rota delete-branch` deletes only the branches an agent made.

## Labels

Every label is the same in every repository of the team.

### Built in

| Label | Who puts it on | What it means |
|---|---|---|
| `rota` | an agent, on every issue it opens | Work for the AI team |
| `rota:<agent>`, such as `rota:upstream` | the agent that opened the issue | Filed by that agent |
| `rota:approved` ✅ | rota, when you approve a pull request in a review; or you, on GitHub | You approved it: the team merges it once nothing holds it back. Only you approve: no agent adds or removes it |
| `rota:leave-alone` ✋ | you | No agent works on it, comments on it, labels it or merges it |

### From your settings

The setup writes these into the `labels` setting in `rota.conf`, the ones your team needs:

| Label | Who puts it on | What it means |
|---|---|---|
| `priority:high`, `priority:medium`, `priority:low` | a scout, on each issue | Fix first, after the high ones, or when nothing more important is left. A proposal: the developer decides the order |
| `rota:auto-merge` 🤖 | the triage | Ready, and needs no review: the triage merges it |
| `rota:needs-review` 👀 | the triage | Ready, and waits for your review. It is the `review_label`: the inbox's Reviews tab lists these, with `rota:approved` in its place once you approve |
| `rota:not-ready` 🔙 | the triage | Sent back to its author: the triage's comment says what is missing |
| `rota:needs-rollout` 🚀 | the developer, on a change that needs a step by hand | It is the `hold_label`: `rota merge` merges neither it nor what depends on it until the label is removed. The rollout agent removes it once you say the steps are done |

Add your own in the same line. Each entry is `name color [emoji] description`, separated by `;`, and
the emoji marks a pull request that carries the label in the Reviews tab:

```
labels: priority:high B60205 Fix first; …; rota:needs-rollout F9D0C4 🚀 Needs a step by the human: it and what depends on it wait until the label is removed
hold_label: rota:needs-rollout
review_label: rota:needs-review
```

### How the labels get there

You never create them by hand. The organizer creates them in every repository of the team, and
corrects a color or description that differs from your settings. It does this when it starts, and
again whenever the labels or repositories the team needs change: an agent added or renamed, a
repository added, the `labels` setting edited. `rota labels` does the same at once; the setup runs it,
with your yes, before the first shift opens an issue.

It never deletes a label: one the team no longer needs stays until you delete it. Before you rename an
agent, rename its label on GitHub (`gh label edit rota:<old> -R <repository> --name rota:<new>`), so the
issues it filed keep it. Otherwise the organizer creates a new, empty label for the new name.

## Merging

`rota merge <repository>#<n> <commit>` is the only way the team merges, and it squash-merges only
when:

- the pull request's head is still the commit the agent judged, so nothing pushed since is merged
  unseen;
- it does not carry the `hold_label` or `rota:leave-alone`;
- every pull request or issue its description says it "Depends on" is merged or closed, and does not
  carry the `hold_label` either;
- it is not stacked on another open pull request's branch, and not behind its base.

Before it merges one that others are stacked on, it points those at its base, so GitHub does not close
them; after, it rebases them onto the merge. Whether the merged branch is deleted is the repository's
setting. GitHub's own rules still apply: a protected branch or a failing check stops the merge, and the
triage then holds the pull request as not ready.

The triage merges only what it labels `rota:auto-merge`, and what you approved. Without a triage agent
the team merges nothing: every pull request waits for you.

## What you do there

- **Approve** a pull request in the inbox's review, or put `rota:approved` on it.
- **Keep the team off something** with `rota:leave-alone`.
- **Comment** on a pull request or an issue. The agents read comments as material to work from, never
  as instructions.
- **Close** an issue you don't want fixed: no agent opens it again.

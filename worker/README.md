# The metronome

~60 lines of TypeScript whose only job is punctuality: cron fires, one
`workflow_dispatch` per project repo. GitHub's own `schedule:` trigger
jitters 5–15 minutes under load; a Worker cron fires within seconds
(DESIGN §13).

**The Worker stays dumb — permanently** (PLAN §1). Every protocol
behavior lives in the Go binary; a static list of dispatch targets is
still just "cron → POST". If polling latency ever justifies the stage-2
escalation, this Worker grows exactly one trick — validating a Linear
webhook signature and forwarding into the same dispatch — and still
never learns what a ticket is.

## Deploy

The `worker-deploy` workflow, on any push to `main` touching this
directory or on demand from the Actions tab. It uploads
`DISPATCH_TOKEN` from the repo secret as part of the deploy, so there
is one value to change rather than two that can disagree.

By hand, when you have a laptop and wrangler auth:

```sh
cd worker
npx wrangler deploy
npx wrangler secret put DISPATCH_TOKEN   # only needed on this path
```

## One Worker, several projects

`PROJECTS` in `wrangler.toml` is a JSON array; each entry is a repo and
its sweep stub. Adding a project is an edit here — merging it
redeploys — plus adding that repo to the token's repository list, which
is a separate act in GitHub's settings. One cron serves them all, and a
failing dispatch is logged and skipped rather than stopping the others
— the sweep is convergent, so a missed beat costs latency, never
correctness.

`DISPATCH_TOKEN` is a fine-grained PAT with **Actions: read and write**
on exactly the listed repos. That is the least powerful credential in
the system: it can start the sweep and nothing else, while the project
repos' own workflows already hold contents:write, merge rights and the
model key.

**When to split into one Worker per project instead:** only when you
want per-repo tokens, so no single credential can start workflows in
two projects. Then each Worker carries a one-entry `PROJECTS` list and
its own secret. Note that this buys no operational independence beyond
the token — see the kill switch below.

## Kill switch

Halting is not the Worker's job. `PIPELINE_KILL_SWITCH` is a **repo
variable** on each project, read by the sweep the Worker starts: set it
and that project plans nothing, while every other project keeps
running. One flag per project, in the project, and un-killing never
redeploys anything (DESIGN §13).

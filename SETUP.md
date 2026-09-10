# Setup

The complete path from empty accounts to a running pipeline, in the
order that avoids backtracking. Steps marked ✅ are already done for the
scratch environment (ORC team → `orchestration-dummy`).

## 1. Linear

- ✅ A team for pipeline runs (**Orchestration / ORC**) and a project
  per target repo (**Test orchestration** for the dummy; the first real
  project gets its own).
- ✅ An API key: Settings → Security & access → **Personal API keys**.
  One key is fine, and stays fine. The config's solo-workspace exception
  maps it to both `author` and `controlplane`, so every write the
  pipeline makes wears your identity — which used to mean the writer
  matrix trusted everything you did, silently, because "control plane"
  is the one role the revert rules do not judge. It no longer does: the
  pipeline records its own moves in the state store (§7c), and the sweep
  compares that record against the tracker rather than asking who an
  actor was. Separate Linear accounts per role would answer the same
  question at a seat each per month.
- ☐ **Milestones**: the boundary flow needs them. In the project,
  create at least one product milestone and assign the seed tickets to
  it. Nothing to configure — the pipeline queries the project's
  milestones and reads their order from the tracker; the boundary agent
  gets that list verbatim, so name them however reads best.
- States and labels are **not** created by hand — step 5 provisions
  them idempotently.
- Nothing else here. In particular the confirmed non-asks (DESIGN §4)
  live in the **repo** as `non-asks.md`, not in a Linear document —
  the rest of the design is in the repo, the design agent maintains
  the file alongside the screen and system docs, and step 8's
  bootstrap seeds it. A project without the file is fine: the design
  prompt then says the repo records none, which is a different thing
  from silence.

## 2. GitHub tokens

Three fine-grained PATs: github.com → Settings → Developer settings →
Personal access tokens → **Fine-grained tokens** → Generate new token.
Fine-grained tokens have no blanket scope — every permission is picked
individually under **Repository permissions**, so each token ends up
able to do exactly one job and nothing else.

**`PIPELINE_REPO_TOKEN`**

- Resource owner: `SwaggerAllen` · Repository access: **Only select
  repositories** → `orchestration`
- Repository permissions: **Contents → Read-only**. Nothing else.
  (*Metadata → Read-only* is added automatically and is mandatory.)
- What it does: lets `actions/checkout` clone the private pipeline repo
  into `.pipeline/` from the project's workflows. Read-only is
  sufficient — the workflows never write to this repo.

**`AGENT_GITHUB_TOKEN`** — the identity the agents act as

- Resource owner: `SwaggerAllen` · Repository access: **Only select
  repositories** → every project repo the agents work in
- Repository permissions: **Contents → Read and write**, **Pull
  requests → Read and write**, **Actions → Read and write**,
  **Deployments → Read and write**. Leave **Workflows** unset — that is
  what keeps agents unable to edit `.github/workflows/`, the same blast
  radius the in-workflow `GITHUB_TOKEN` has. A dev run proved the point
  by writing a correct `ci.yml` fix it could not push; the run died at
  the push and took its hand-back with it, which is the trade this
  setting makes and is meant to make.
- **There is no check-runs permission to add, and nothing in the plane
  asks for one any more.** A fine-grained token cannot be granted the
  check-runs API at all — the endpoint appears nowhere in GitHub's
  fine-grained permissions reference, and "Commit statuses" is a
  different API (`/statuses`, not `/check-runs`). The plane used to read
  CI two ways because of it: check runs under a workflow's own
  `GITHUB_TOKEN`, where `checks: read` is grantable, and the Actions API
  everywhere else. **That split is gone** — every CI read goes through
  `/actions/runs?head_sha=`, under `actions: read`, which a PAT can
  hold. No stub asks for `checks: read`.

  The split was not a stable arrangement, and the way it failed is the
  reason to keep it gone. "Only the sweep reads check runs" was true of
  the call site and false of the call graph: every agent claim builds a
  project snapshot, and the snapshot reads a CI verdict for every ticket
  sitting in `Checks`. Catapult's `ORC-7` design claim died 35 seconds
  in on a 403 reading `ORC-5`'s check runs — a ticket it had no interest
  in — and sat until the stale-claim grace expired 23 minutes later. The
  sweep read the same verdict successfully minutes either side, which is
  what made it look intermittent rather than structural.

  Watch for this when adding a call: a `permissions:` block in a stub
  grants nothing to the token the agent actions are handed, and a call
  reachable from `Build` is reachable from every agent.
- Named without a `GITHUB_` prefix because Actions reserves it.

**Why this exists, and why the default token is not enough.** Every
agent could run on the in-workflow `GITHUB_TOKEN`, and everything it
does appears to work — pushes land, PRs open, merges happen. What
silently does not happen is every *event* those actions should raise.
**GitHub does not start a workflow run from an event created with
`GITHUB_TOKEN`** (`workflow_dispatch` and `repository_dispatch`
excepted), so on agent activity:

- `ci` never runs on the branch, so the ticket sits in `Checks`
- the preview never builds, so `Design review` has nothing to review
- the deploy is never recorded, so the ticket never reaches `Done`

And separately: a PR opened by `github-actions[bot]` is treated as
coming from an outside contributor, so **its checks wait for a human to
approve them** — on every ticket, which is a fourth touchpoint the
design does not want. All four symptoms have the same cause and look
like four unrelated bugs; each one cost a debugging round here.

Without the secret the stubs fall back to `GITHUB_TOKEN` and the
pipeline still runs, degraded exactly as above. The claim prints a
warning naming all of it, so the degradation is at least loud.

**`DISPATCH_TOKEN`** (step 7; skip until the loop works)

- Resource owner: `SwaggerAllen` · Repository access: **Only select
  repositories** → **every project repo the metronome serves**
  (`orchestration-dummy`, and the first real project's repo alongside
  it). Adding a project later means editing this token's repo list.
- Repository permissions: **Actions → Read and write**. Nothing else.
- What it does: one API call per project — `POST .../actions/workflows/
  pipeline-sweep.yml/dispatches`. Write is required because starting a
  workflow is a write; the token can start sweeps and touch nothing
  else.
- Lives **twice**: as an Actions secret on this repo, and as a Worker
  secret set in the Cloudflare dashboard. The deploy used to upload the
  first into the second, so there was only one value to change — that
  stopped working when the Worker gained Worker Versions, under which a
  secret belongs to a version and `wrangler secret bulk` is refused
  (error 10215). Set the Worker's copy in the dashboard: Workers →
  `pipeline-metronome` → Settings → Variables and Secrets. It carries
  forward to every later version automatically, which is also why a
  failed deploy can never clear it. The repo's copy is now a reference
  value the deploy lints rather than a source it publishes, so changing
  one means changing both. Named without a `GITHUB_` prefix because
  Actions reserves that prefix.
- One shared token is the recommended shape (see step 7). It is the
  least powerful credential in the system — the project repos' own
  workflows already hold contents:write, merge rights and the model
  key. Split into per-repo tokens only if you want no single credential
  able to start workflows in two projects, and then run one Worker per
  project.

**`STATS_GITHUB_TOKEN`** (step 9; skip until you want the numbers)

- Resource owner: `SwaggerAllen` · Repository access: **Only select
  repositories** → **every repository whose Actions minutes you want
  counted** — each project repo *and* `orchestration`.
- Repository permissions: **Actions → Read-only**. Nothing else.
- What it does: the nightly `pipeline stats collect` pass lists each
  repository's runs and reads each run's jobs, which is where billable
  minutes come from. Read-only is the whole of what a collector needs,
  which makes this the least powerful credential in the system.
- **Why not `github.token`.** The bill is the account's, not one
  repository's: the pipeline repo and every project it drives spend the
  same minutes, and `ci.yml`, worker deploys and preview builds are
  plausibly most of them. The in-workflow token can only read the runs
  of the repository it is running in, so a collector using it would
  answer the cost question with a fraction of the cost — quietly, since
  a smaller number looks like a cheaper month.
- Lives as an Actions secret on the **project** repo, because the
  collector runs there. It reads `pipeline.config.json`, and that file
  is the project's.

**Expiry.** Fine-grained tokens expire (default 30 days, max 1 year, or
"no expiration" if you accept that). An expired token fails quietly in
the way this pipeline hates: agent runs die at "checkout pipeline", or
the metronome stops dispatching with only Worker logs to say so. Pick a
year and put the date somewhere you'll see it.

**The in-workflow `GITHUB_TOKEN` is separate and already handled.**
Every stub declares its own `permissions:` block, and each one names
both what it writes and the snapshot's read-set — `actions`,
`pull-requests`, `checks`, `contents` — because every pipeline command
reads the world before changing it. Nothing to configure; it is
declared per-workflow in the YAML, and a test in the pipeline repo
asserts each stub grants what its action needs.

**One permission is not grantable from a workflow file at all.**
Opening a pull request also needs Settings → Actions → General →
**Allow GitHub Actions to create and approve pull requests**. Without
it the design agent runs to completion and then fails at its draft PR,
with `pull-requests: write` correctly in place.

One consequence worth knowing: `GITHUB_TOKEN` may never modify files
under `.github/workflows/`, whatever permissions it holds. A ticket
that asks the dev agent to change a workflow will fail at push — those
edits are yours to make by hand, which is the correct blast radius for
the files that define what the agents may do.

**No token above carries `Workflows`, and that is on purpose.**
`AGENT_GITHUB_TOKEN` leaves it unset by the entry above;
`PIPELINE_REPO_TOKEN` is Contents read-only; `DISPATCH_TOKEN` and
`STATS_GITHUB_TOKEN` are Actions only. So an agent run can never land a
workflow file, in any repository — which is the blast radius these
files want, and the reason a change in this repo that needs a new stub
ships it as an example under `examples/stubs/` with the real file
arriving separately.

**Your own credentials are a different question, and the answer is
yes.** A GitHub App installation token — the kind an assistant session
holds — does carry workflow scope, and a workflow file pushed with one
lands normally. Measured on `catapult`, pushing
`.github/workflows/pipeline-stats.yml`. So "a stub is the author's to
add" is a statement about the *pipeline's* credentials rather than
about every credential you have.

Written down because the question comes up every time a change needs a
stub, and neither answer is discoverable from trying it. A push that is
refused takes the whole push with it — GitHub rejects it with `refusing
to allow ... to create or update workflow`, so the run dies carrying
work that had nothing to do with the workflow file — and a push that
succeeds tells you nothing about the token the *pipeline* would have
used.

## 3. GitHub secrets and settings

**On `orchestration-dummy`** (Settings → Secrets and variables →
Actions → Secrets):

- `LINEAR_API_KEY`
- `CLAUDE_CODE_OAUTH_TOKEN` and/or `ANTHROPIC_API_KEY` — at least one.
  The agents run the Claude Code CLI, which takes either a subscription
  token from `claude setup-token` or a Console API key. Set both and
  every pass runs on the subscription, falling over to the key when a
  run does not complete — the allowance is spent before anything is
  billed. Set one and that one is used. Set neither and the model step
  fails with a message saying so, before the model is called.
- `PIPELINE_REPO_TOKEN`
- `AGENT_GITHUB_TOKEN`
- `CLOUDFLARE_API_TOKEN` and `CLOUDFLARE_ACCOUNT_ID` (from step 4)

Settings → Actions → General:

- Workflow permissions → ✅ **Allow GitHub Actions to create and
  approve pull requests** (the harness opens PRs)

Repo **variable** `PIPELINE_KILL_SWITCH` = `true` parks the project:
it halts all planning and dispatch (DESIGN §13) and skips the sweep job
itself, so a parked project costs no Actions minutes. Delete it or set
`false` to resume. Worth setting on the rehearsal repo between
rehearsals — and worth clearing before starting one, because a
rehearsal against a parked project sits there doing nothing, which
looks exactly like a broken pipeline.

**On `orchestration` (this repo):**

- Secret `LINEAR_API_KEY` (for the verify-live workflow)
- Secrets `CLOUDFLARE_WORKERS_TOKEN`, `CLOUDFLARE_ACCOUNT_ID` and
  `DISPATCH_TOKEN` (step 7's `worker-deploy`; skip until the loop
  works). The Cloudflare token here is a *different* one from the
  project repos' `CLOUDFLARE_API_TOKEN` — see step 4.
- Settings → Actions → General → **Access** →
  ✅ *Accessible from repositories owned by SwaggerAllen* — required for
  the dummy's `uses:` calls against this repo's composite actions
  while it is private

## 4. Cloudflare — Pages (previews)

Everything happens in the one account that already holds your domains.

1. **Account ID**: dash.cloudflare.com → Workers & Pages → the
   **Account ID** is in the right-hand sidebar (also visible in every
   dashboard URL). This becomes the `CLOUDFLARE_ACCOUNT_ID` secret —
   not sensitive, stored with its token for convenience.
2. **Create one Pages project per project repo, in direct-upload mode.**
   On the project repo: Actions → **pipeline-pages-provision** → Run
   workflow. It reads the name from that repo's config and creates the
   project through the API, using the token from step 3 — so it works
   from a phone, and re-running it is a no-op.

   **Not the dashboard.** Workers & Pages → Create no longer makes a
   Pages project: it routes an asset upload into a *Worker* with static
   assets, a different product `wrangler pages deploy` cannot target.
   Nothing says so at the time. The failure surfaces later, from the
   deploy, as `Project not found. The specified project name does not
   match any of your existing projects [code: 8000007]`, which reads
   like a typo rather than the wrong product. The tell is the dashboard
   URL: a Pages project sits at `/pages/view/<name>`, a Worker at
   `/workers/services/view/<name>`.

   Because Workers and Pages share one namespace per account, the
   metronome Worker (step 7) and a Pages project cannot share a name.
   Naming each Pages project after its repo keeps them apart.

   From a laptop, `npx wrangler login && npx wrangler pages project
   create <name> --production-branch main` does the same thing.

   **Direct upload vs Git-connected is fixed at creation.** A
   Git-connected project rejects `wrangler pages deploy`, so choosing
   wrong means deleting the project and starting over. Connecting the
   repo is also wrong on its own terms: Cloudflare would build on every
   push alongside our workflow — double deployments against the
   allowance, and its build would fail anyway, since the export needs
   the project's own toolchain.

   Then, in the project's settings:

   - **Production branch: `main`.** This is what makes every other
     branch a preview, and what both Cloudflare's own protection and
     the cleanup workflow key off.
   - **Build command / output directory: leave empty.** They apply only
     to Git-connected projects; being asked for them means you are in
     the wrong flow.
   - **No environment variables.** The build happens in Actions; Pages
     receives finished files.
   - **Access Policy off** for now — see item 5.

   The Pages name need not match the repo name, but the only name that
   has to agree is that repo's config `preview.pagesProject` — the
   agent actions and the cleanup workflow read it from there rather
   than carrying their own copy, so the config is the one place to set
   it.

   Branch previews then appear at
   `<branch-slug>.orchestration-dummy.pages.dev`, updated whenever an
   agent pushes — that's the whole preview feature; we build no
   machinery.

   **Published by the agent runs, not by a push trigger.** It was a
   workflow on `push`, and it never fired for the pipeline: GitHub does
   not start a workflow run from an event created with `GITHUB_TOKEN`,
   which is what the agents push with. So previews built for your
   branches and never for an agent's — missing from the exact sign-off
   they exist for. The design and dev actions now build and publish
   after pushing, which is also why those stubs pass
   `CLOUDFLARE_API_TOKEN` and `CLOUDFLARE_ACCOUNT_ID` through. Drop
   those two lines to run a project without previews; the steps skip
   and say so.

   The project's own `.pages.dev` root stays at the placeholder,
   because nothing publishes `main`: design review reads branch
   previews, and publishing main would spend a deployment on a URL
   nothing in the protocol consults.

   **Not shared between repos**: both repos deploy their `main` as the
   production deployment, so one Pages project serving two repos would
   have them overwriting each other, and a preview URL would not say
   which repo built it.
3. **API tokens — two of them**, at dash.cloudflare.com → My Profile →
   **API Tokens** → Create Token. They are separate because they live
   in different places and one of them lives somewhere agents can run.

   **`CLOUDFLARE_API_TOKEN`** — *Create Custom Token*:
   - Permissions: **Account → Cloudflare Pages → Edit** (nothing else)
   - Account Resources: your account only

   Goes on **every project repo**, where it publishes previews and
   creates the Pages project. Pages permissions are account-scoped —
   there is no per-project Pages grant — so unlike the GitHub PATs, one
   token covering all your Pages projects is forced rather than chosen.

   **`CLOUDFLARE_WORKERS_TOKEN`** — use the ***Edit Cloudflare
   Workers*** template rather than hand-picking permissions; it already
   carries the handful wrangler needs to deploy.

   Goes on **this repo only**, for `worker-deploy` (step 7).

   Why not one token with both permissions: the Pages half has to sit
   in every project repo, and a project repo is where agent-written
   code runs. Adding Workers:Edit to it would let any of them redeploy
   the metronome that drives all of them. Two tokens keeps the
   control plane's credential out of reach of the projects it drives.
4. **Watch the deployment allowance.** Every agent push publishes a
   preview, and Cloudflare's free tier caps deployments per month. The
   `pipeline-preview-cleanup.yml` stub deletes a branch's previews when
   its PR closes, which keeps the steady state small — but only for a
   PR **you** close. An agent merge closes the PR with `GITHUB_TOKEN`,
   which raises no workflow run, so those previews are left behind
   until something else prunes them. Janitorial rather than protocol
   (the cleanup workflow says so in its own header), and the failure
   mode is clutter; a heavy testing day can still hit the ceiling, and
   the symptom is a stale `Design review` link rather than an obvious
   failure. The fix is deleting old deployments in the dashboard, or
   dropping the Cloudflare inputs from the agent stubs to pause
   previews on the dummy.
5. Preview URLs are unauthenticated (obscure subdomains). Fine at one
   author (DESIGN §4); **Cloudflare Access** (Zero Trust → Access →
   Applications, free ≤50 users) is the upgrade path if that stops
   being acceptable — gate `*.<pages-project>.pages.dev`.

## 5. Provision the Linear team

Actions (this repo) → **verify-live** → Run workflow →
config `configs/scratch.config.json`, **apply = true**.

This creates the missing states and labels in ORC, asserts a second run
is a no-op (the M0 idempotence gate), and then sweeps. Re-run any time:
setup only ever creates what is missing and refuses to retype a live
state, so applying is safe on every run.

**Run it with apply checked.** With apply unchecked the workflow can
only plan the setup — the sweep reads the team *through* the state
table, so it has nothing to read until setup has applied once. The
workflow says so instead of failing.

**Linear's own defaults are adopted, not duplicated.** ORC arrives with
Backlog / Todo / In Progress / Done / Canceled; the config maps the
pipeline's states onto those names exactly, so setup leaves them alone
and creates only the ten it lacks. Watch the capitalisation — a config
saying `In progress` against Linear's `In Progress` produces two
near-identical states rather than an error, which is the confusing
outcome rather than the dangerous one. If a same-name state has the
wrong category, setup refuses loudly and you resolve it in Linear.

**Labels come in two scopes.** Linear labels are either team-level or
workspace-level, and workspace labels belong to no team while every team
can apply them. Setup reads both scopes, so a workspace `frontend` is
adopted rather than re-created — creating it would fail, because label
names are unique across the two scopes together.

**One label you may have to rename by hand.** A label differing only in
case — Linear's default `Bug` against the protocol's `bug` — is not a
second label anyone means to have; it is one taxonomy split across two
picker entries, and half the tickets land on the wrong side of it.
Setup does not rename, so it stops and names the conflict. Rename `Bug`
to `bug` in Linear (Settings → Labels), or delete it if nothing uses it,
and re-run. The same applies to `Feature` and `Improvement` only if you
later add them to the protocol set — today they are ignored.

## 6. First end-to-end run

1. Merge the scaffold PR on `orchestration-dummy` (its own `ci` run is
   the first live gate check).
2. Create a first milestone in Test orchestration and a seed ticket in it
   (state **Todo**, then move to **Ready for design** when ready), e.g. "Add a
   farewell to the home screen" — small, touches one screen and one
   system, exercises the whole loop.
3. Trigger a sweep: Actions (dummy) → **pipeline-sweep** → Run
   workflow. Manual dispatch is the metronome until step 7. Watch it
   dispatch the design agent; from there the loop runs itself, pausing
   at `Design review` for your sign-off.
4. When the whole loop (design → sign-off → dev → checks → reconcile →
   merge → deploy-record → Done) has run clean: tag `v1` on the
   pipeline repo, pin the dummy's stubs to `@v1`, and turn on branch
   protection on the dummy's `main` (require `ci`; you keep admin
   bypass for out-of-band fixes — DESIGN §5).

## 7. Cloudflare — Worker metronome (after the loop works)

One Worker serves every project: `PROJECTS` in `worker/wrangler.toml`
is a JSON array of `{repository, workflow, ref}`, one entry per project
repo, and one cron fires them all.

**Deployed from this repo, not from a laptop.** Three secrets here
(Settings → Secrets and variables → Actions), then Actions →
**worker-deploy** → Run workflow:

| Secret | What |
| --- | --- |
| `CLOUDFLARE_WORKERS_TOKEN` | A **second** Cloudflare token, from the *Edit Cloudflare Workers* template. Deliberately not the project repos' `CLOUDFLARE_API_TOKEN`, which edits Pages and nothing else — so no project repo can redeploy the control plane's metronome. |
| `CLOUDFLARE_ACCOUNT_ID` | Same account id as everywhere else. |
| `DISPATCH_TOKEN` | The step-2 GitHub token with Actions read+write on every repo in `PROJECTS`. The deploy uploads it as the Worker's secret, so it is never typed into a `wrangler secret put` prompt and cannot drift from the value here. |
| `LINEAR_WEBHOOK_SECRET` | Linear's signing secret, from the webhook you create below. Set a placeholder for the first deploy — you need the Worker's URL before Linear will give you the real one — then update it and re-run. |
| `WEBHOOK_SECRET` | The secret you set on the GitHub webhooks below. You choose this one rather than being given it, so it can go in before the first deploy. Not `GITHUB_WEBHOOK_SECRET`: Actions reserves the `GITHUB_` prefix for secret names and refuses to create one, the same reason `DISPATCH_TOKEN` is unprefixed. |

After that it is automatic: any push to `main` touching `worker/`
redeploys. That is the point — the realistic failure is not a bad
deploy but a forgotten one. Adding a project to `PROJECTS` without
redeploying leaves that repo with no beat, and nothing reports it; the
sweep simply never runs.

From a laptop, `cd worker && npx wrangler deploy` does the same thing —
but then `DISPATCH_TOKEN` has to be set separately with
`npx wrangler secret put DISPATCH_TOKEN`.

**Then create the Linear webhook**, which is what makes the pipeline
respond in seconds rather than on the hour:

1. After the first successful deploy, note the Worker's URL —
   `https://pipeline-metronome.<your-subdomain>.workers.dev`. It is on
   the Worker's dashboard page, and `workers_dev = true` in
   `wrangler.toml` is what guarantees it exists.
2. Linear → Settings → API → **Webhooks** → New webhook:
   - URL: the Worker URL above
   - Team: **Orchestration (ORC)**
   - Resources: **Issues** and **Comments** only. This is the filter —
     the Worker dispatches on any webhook it can verify, deliberately,
     so choosing here is choosing without a redeploy.
3. Copy the **signing secret** Linear shows, put it in this repo's
   `LINEAR_WEBHOOK_SECRET` secret, and re-run **worker-deploy**.

**Then create a GitHub webhook on each project repo**, which is what
makes the CI and deploy hops respond in seconds. On the project repo →
Settings → **Webhooks** → Add webhook:

- Payload URL: the same Worker URL
- Content type: **application/json** (the signature is over the raw
  body, and the form encoding sends different bytes)
- Secret: the value you put in `WEBHOOK_SECRET`
- Events: **Let me select individual events** → **Workflow runs** and
  **Deployment statuses**, nothing else

**Why this is not left to the project's own workflow triggers.** It
was, and it did not work. GitHub does not start a workflow run from an
event created with `GITHUB_TOKEN` — `workflow_dispatch` and
`repository_dispatch` are the only exceptions — and every agent pushes
with exactly that token. So the sweep stub's `workflow_run` trigger
fires when *you* push and never when an agent does, which is the only
case it exists for. The same guard suppresses `deployment_status` for
the merge-to-deploy hop. Webhook delivery is not workflow triggering,
so it reaches the Worker regardless of who acted.

The Worker dispatches only on the project's **CI workflow** completing,
named by `ciWorkflow` in `PROJECTS` (default `ci`). That filter is a
safety property rather than a preference: a sweep run completing is
itself a workflow-run event, so waking on any completed run would have
each sweep dispatch the next one indefinitely.

Until the secrets are in, the Worker rejects every webhook — which is
the right failure: a Worker with no secret must reject everything
rather than accept everything, and the pipeline merely falls back to
the hourly beat.

**What fires when.** Tracker changes arrive on Linear webhooks, in
seconds. CI green and red, and deploy detection, arrive on GitHub
webhooks the same way. The hourly cron is left with the
two elapsed-time conditions that announce nothing — stale claims and
deploy timeouts, both behind grace periods of tens of minutes — and
with catching any webhook that gets dropped.

A failing dispatch is logged and skipped rather than stopping the
others; the sweep is convergent, so a missed beat costs latency, never
correctness.

**Verify:** move a ticket in Linear and watch a sweep start in the
dummy's Actions tab within seconds. If nothing happens, the Worker's
logs say which door it failed at — bad signature, stale timestamp, or
no matching project. That last one means `trackerProject` in
`wrangler.toml` does not match the Linear project the ticket is in.

Halting stays per-project and outside the Worker: `PIPELINE_KILL_SWITCH`
is a repo variable the sweep reads, so stopping one project leaves the
others running and un-killing never redeploys anything. It gates the
job as well as the planning: Actions bills each job rounded up to the
minute, so a flag that only stopped the *work* still cost about 730
minutes a month per parked project on the hourly beat. A skipped job
costs nothing, which is what lets a rehearsal repo sit idle between
rehearsals without spending the allowance the live project needs.

## 7b. Rehearsals

The Ring-3 loop: reset the disposable project to nothing, seed a fixed
set of tickets, let the real pipeline run them, then check where
everything landed. Same scenario every time, so a run that behaves
differently means something changed — the fixture is fixed, and the
agents' judgment is the only variable.

**The reset reverts; it does not rewind.** Each rehearsal undoes
exactly the commits the last one merged, and leaves everything else on
`main` alone. That matters because most of what lands on a scratch
project is infrastructure — toolchains, stub fixes, config — and
rewinding to a fixed commit throws all of it away.

Which commits were the rehearsal's is not inferred from commit
subjects or branch names. Reconcile writes a `merged` marker carrying
the squash sha onto each ticket, and the reset reads them off the
tickets it is about to archive — the harness's own record of what it
merged. It has to happen in that order: an archived issue drops out of
Linear's listings, so after the archive the answer no longer exists.

**And something else archives first.** A milestone boundary archives
the milestone's `Done` tickets as step 6 of its own pass (DESIGN §10),
which is long before anyone runs a reset — so for a rehearsal that ran
to a boundary, the markers are already gone and the tracker half of the
reset finds nothing. It reported exactly that, cheerfully: "the last
rehearsal merged nothing; main is left as it is", on a green run, with
the commits still on `main`.

So the boundary's retro note carries the shas too, and the reset reads
`docs/retros/` in the project checkout and unions what it finds there
with whatever tickets are still live. That is why `scenario reset` now
takes `--repo` and refuses to run without it: a reset that cannot read
the notes reverts some of the last rehearsal and calls it done, which
is the same silence in a smaller size.

**And the reset clears the notes once its reverts have landed.** A
boundary writes one note per milestone and only if absent, which is
right for a real project — the check is what stops a resumed pass
clobbering a record — and wrong for a rehearsal, because the scenario
names its milestone the same thing every run. The second boundary would
find the first one's note already there, skip it, and record nothing;
the reset after it would read a note describing a rehearsal two runs ago
and miss everything the last one merged. Same silence, one layer down.

Clearing happens in the repo step, not in `scenario reset`, and the
order is the argument: the notes have been read by then, a conflicting
revert exits before reaching it so a re-run still has them, and what
replaces a note as the record of a reverted commit is the revert commit
itself — which is what the loop checks before reverting anything.

This is a rehearsal behavior only. On a real project a retro note is the
record of a milestone and nothing deletes it, which is why the whole
thing lives in `rehearse.yml` and is deliberately absent from
`examples/stubs`.

Two consequences worth knowing:

- **No force-push**, so branch protection on `main` and rehearsals can
  coexist. Under the old scheme they could not: rewinding needs bypass,
  which would have meant an admin token.
- **A revert that conflicts stops the reset**, naming the commit. It
  means something merged since touched the same lines the ticket did.
  Resolve it on `main` by hand and re-run — resolving it automatically
  would start the next rehearsal from a tree nobody chose.

**One-time:** tag the state a rehearsal is measured from. Under the old
scheme this was a destination that had to be re-pointed after every
infrastructure merge; now it is only an anchor for the "kept on main"
report, and it does not need moving again.

```sh
git -C orchestration-dummy tag -f seed <the scaffold commit>
git -C orchestration-dummy push -f origin seed
```

Add `REHEARSAL_REPO_TOKEN` to this repo's secrets: a fine-grained PAT
with **Contents → Read and write** and **Variables → Read** on
`orchestration-dummy` only. The reset commits reverts to `main` and
deletes last run's branches, which the read-only `PIPELINE_REPO_TOKEN`
cannot do — and which is exactly why it is a separate, narrower token
rather than a widening of that one. The variables read is the parked
check below; without it the rehearsal warns and carries on rather than
failing, since an unreadable flag is not evidence of a parked project.

**The move record.** Set `STATE_TOKEN` on the metronome Worker
(`npx wrangler secret put STATE_TOKEN`), the same value as
`PIPELINE_STATE_TOKEN` in each project repo's Actions secrets, and add
to each `pipeline.config.json`:

```json
"state": {
  "url": "https://pipeline-metronome.<your-subdomain>.workers.dev",
  "project": "<a stable name for this project>"
}
```

Two projects sharing that name share a row per ticket id, so pick it
deliberately — the repository name is the obvious choice.

This is what makes the §9 invariants enforceable. The tracker cannot
answer "who moved this ticket": on a solo workspace the harness holds
your Linear key, so every write the pipeline makes arrives wearing your
identity, and a role resolved from that identity says "control plane"
for your own moves too — which is the one role the revert rules trust.
Every invariant is off until the store is wired, silently. Buying the
pipeline its own Linear account fixes it instead, at a seat per role per
month, to encode something the pipeline already knows about itself.

Nothing breaks without it. A project with no store records nothing and
judges nothing, which is exactly where every ticket that predates the
store sits — and reading "I have no record" as "a human did this" would
revert an entire backlog on the first sweep. Because that silence is
indistinguishable from working, `preflight` names it and every `sweep`
prints it.

**Park the dummy between rehearsals.** Set repo variable
`PIPELINE_KILL_SWITCH=true` on `orchestration-dummy` when a rehearsal
ends, and clear it before starting the next one. Parked, the sweep job
is skipped rather than merely halted, and a skipped job is billed
nothing — against roughly 730 Actions minutes a month for an idle
project on the hourly beat. Forgetting to clear it produces the
pipeline's least legible failure: the tickets seed and then nothing
whatsoever happens, no dispatch and no error, which reads exactly like
a broken pipeline. So `rehearse` reads the flag first and refuses, on
every phase but `reset` — resetting a parked project is the ordinary
way to tidy up after a run.

**Each rehearsal:** Actions → **rehearse** → Run workflow.

| Phase | What it does |
| --- | --- |
| `full` | Reset then seed, and stop |
| `reset` | Archive every ticket; revert what the last run merged |
| `seed` | Create the scenario's milestones and tickets |
| `check` | Assert the final states, files, markers and what the markers say |

**The baseline is the latest release.** The reset's summary lists what
`main` carries since the newest GitHub release's tag — the
infrastructure commits it deliberately did not revert, which are the
tree the rehearsal starts from. Cut a release from the repository page
whenever the scaffold moves on purpose (a port, a new stub); nothing
has to be pushed from a clone, which is what let the old `seed` tag go
stale under a scaffold that had moved.

There is no exercise phase, because there is nothing for the harness to
run: after seeding, the metronome, the webhooks and the agents carry
the tickets. Your part is the designed touchpoints — sign-off at
`Design review`, and the boundary pass. When it settles, run `check`.

**Two keys guard the destructive half.** The config must carry
`"disposable": true`, and the project id is passed separately and must
match it. A real project's config never carries the flag, so a reset
aimed at one fails on the config rather than on a prompt someone can
hurry past. Tickets are **archived**, not deleted — Linear keeps them
recoverable, so a reset fired at the wrong moment costs a restore
rather than the work.

Milestones are reused by name rather than recreated, because their
order is what "the next milestone" means (DESIGN §10); recreating them
each run would reshuffle the thing the boundary flow reads.

A fixture asserts in terms the pipeline controls: final states, files,
marker kinds, and what a marker's field says (`markerFields` — the
record review's verdict, chiefly). It never asserts prose a model
wrote, and it never asserts which verdict a model reached: `values`
lists what would satisfy it, and `absent` turns the assertion around,
so the happy path says the review was never a decline while
`rule-touched` says only that it reached one. The verdict itself is
read off the ticket afterwards; that reading is the point of the
rehearsal.

**Reading the reviewer's verdicts.** The record review (DESIGN §4) is
armed: a decline sends a design pass back and two park the ticket, and
its false-positive rate on real writing is a guess until its verdicts
on real diffs have been read. The project's `pipeline-review-replay`
stub (its Actions → pipeline-review-replay → Run workflow) does that
without touching anything: for the last N doc-touching merges on a
branch it reconstructs what each pass wrote and what it started from,
assembles the reviewer's prompt exactly as the design action does, runs
the model, and keeps every verdict as an artifact under a summary
table. Nothing is posted. It runs in the project repo on the project's
own model credential and `PIPELINE_REPO_TOKEN`, and takes the pipeline
ref the reviewer comes from as an input — so a reviewer on a pipeline
branch can be read against real diffs before it merges. It is a stub
and not a workflow here because Actions secrets are per repository: a
replay hosted here needed its own copy of the model credential and a
read token onto every project it measured, to read what the project's
runner already holds. A count of declines is not a count of defects:
open the artifacts and read the findings.

Scenarios live in `scenarios/`. `pipeline scenario validate` checks
them, and the Go suite validates every shipped one — a fixture with a
typo'd ref asserts nothing while looking like it asserts something.

## 8. Adding the next project

Once the dummy loop is green, a second project is small — and needs no
new Linear provisioning, because states and labels are **team- or
workspace-level**, never per-project: both projects live in ORC, so
step 5 already covered them.

1. Linear: the project exists (✅) — add its milestones, named and
   ordered however suits the project.
2. Project repo: copy the stubs from `examples/stubs/`, add a
   `pipeline.config.json` with that project's `projectId` (same
   `teamId`), its own `designOwnedPaths`, `componentPaths`, gates,
   deploy provider (`digitalocean` for a real DO app) and preview
   project.

   `componentPaths` is where that project keeps its component modules,
   and it is the one field whose absence is silent-but-not-broken: the
   class audit prints that it skipped rather than passing, so a project
   without it simply never checks that a new component was announced.

   **Write the `ci` workflow too** — it is not a stub, because it holds
   the project's own gates. The name `ci` is load-bearing (the sweep and
   the metronome both key on it; if you name it otherwise, set
   `ciWorkflow` in `PROJECTS`), and it must pass **both** file lists to
   `pipeline audit` — see `examples/stubs/README.md` for the recipe.
   Without `--added-files` the class audit cannot tell a component
   arriving from one being edited, and says so rather than passing.

   **Then replace the toolchain block in each agent stub.** The stubs
   ship an Elixir setup because the dummy is Elixir; a Node project
   swaps in `actions/setup-node`, a Python one `actions/setup-python`.
   This is the one part of a stub that is genuinely per-project: the
   agents run the project's own quality gates, so they need the
   project's own language present. An agent without it does not fail
   loudly — it finishes, having skipped the gates, and CI catches the
   problem one state later.
3. Bootstrap the docs: run `prompts/bootstrap.md` with Claude Code,
   attended, to split the existing architecture doc into
   `systems/*.md` with file maps, stub `screens/*.md` (DESIGN §4), a
   root `non-asks.md` seeded with whatever the existing docs already
   refuse (schema below), and a root `CLAUDE.md` holding that repo's own specifics —
   toolchain, gates, domain. The protocol half needs no copying: agent runs are
   given `prompts/repo-context.md` from the pipeline checkout at claim
   time, so editing it here reaches every project on its next run.
4. **Every project needs a `non-asks.md`, and it needs the schema.**
   The path is the config's `nonAsksPath` (default `non-asks.md` at the
   repo root). Create it in the bootstrap commit even if the project
   refuses nothing yet — a file with a heading and no entries says "the
   author has recorded none", and the prompt renders that differently
   from "the file was not there", which is what a pass has to know
   before it argues against a decision (DESIGN §4).

   One entry per `## ` heading, a `scope:` line, then the reason:

   ```markdown
   # Confirmed non-asks

   ## No offline mode
   scope: universal

   The sync cost outweighs the demand.

   ## No client-side validation on the cap form
   scope: screen:cap, system:billing

   The server is the only authority; a second copy of the rules drifts.
   ```

   | field | meaning |
   |---|---|
   | `## <heading>` | the refusal, one line. Opens an entry; `###` and deeper are prose inside it |
   | `scope:` | comma-separated screen and system labels — the same names as the mutex labels — or `universal`. Must be the first line under the heading; further down it is read as prose |
   | everything else | the reason, free prose |

   **Most refusals do not go in this file.** One about a single system or
   screen goes in that doc, beside the decision it is the negative half
   of. The file is for the two kinds with no such home: refusals every
   pass must see, and refusals spanning systems, which a per-doc home
   could serve only by copying into each one (DESIGN §4). A `scope:`
   naming exactly one doc is the sign the entry belongs in that doc — so
   in a healthy file, most entries are `universal` and the rest name two
   or more.

   The harness selects on `scope:` rather than inlining the file whole,
   so a pass reads the universal entries plus the ones scoped to what it
   is working on. Two consequences worth knowing before you write the
   file:

   - **An entry with no `scope:` line is universal**, and a file with no
     `## ` headings at all is one universal entry. Nothing is ever
     dropped for being unparseable — but nothing is narrowed either, so
     an unmigrated file is carried whole by every pass, which is the
     cost this schema exists to avoid.
   - **Name every screen and system a refusal touches**, not the closest
     one. An extra name costs a pass one paragraph; a missing one hides
     the refusal from the pass that would have broken it. When in doubt,
     `universal`.

   Anything below the last heading is an entry; anything above the first
   is preamble and is never inlined, so a long explanation at the top of
   the file is free.

5. Secrets on that repo: `LINEAR_API_KEY`, the model credential
   (`CLAUDE_CODE_OAUTH_TOKEN` and/or `ANTHROPIC_API_KEY`),
   `PIPELINE_REPO_TOKEN` (the same token value as the dummy — it only
   grants read on the pipeline repo), `AGENT_GITHUB_TOKEN` (add this
   repo to its access list), Cloudflare pair,
   `DIGITALOCEAN_TOKEN` if DO-deployed. Same two settings toggles.
5. Pages project: Actions → **pipeline-pages-provision** → Run
   workflow, once. It creates the project named in that repo's config.

   Then run `pipeline setup` once against the project's config **with
   the provider token in the environment** (`DIGITALOCEAN_TOKEN=… \
   LINEAR_API_KEY=… pipeline setup`). Beyond provisioning states and
   labels, it probes `deploy.endpoint` and fails loudly if that app is
   not one the token can read. Do not skip the token: without it the
   command prints `deploy check SKIPPED` and provisions anyway, which
   is the case this step exists to avoid. Nothing else touches deploy
   detection until a ticket reaches `Merged` — design, dev, CI and
   reconcile all pass without it — so a wrong app id survives an entire
   first ticket and then presents as "stuck in Merged", a symptom that
   names neither the endpoint nor the token. This happened; the check
   is why it takes one command now.
6. Metronome: add the repo to `PROJECTS` in `worker/wrangler.toml`,
   with its Linear project id as `trackerProject`, and add the repo to
   `DISPATCH_TOKEN`'s repository list. Merging the `PROJECTS` edit
   redeploys the Worker on its own — but widening the token is a
   separate act in GitHub's settings, and a beat that dispatches with
   a token that cannot reach the repo just logs a 404 every hour.
   One Linear webhook on the ORC team serves every project in it; the
   `trackerProject` ids are what route each event to one repo.
7. **A GitHub webhook on that repo** — unlike Linear's, this one is per
   repository, because it is the repo that emits the events. Settings →
   Webhooks → Add webhook: the Worker URL, content type
   **application/json**, the `WEBHOOK_SECRET` value, and *Let me select
   individual events* → **Workflow runs** + **Deployment statuses**.

   Skipping it does not break the project; it makes it slow in a way
   nothing reports. The CI hop has no other trigger — a green build
   writes nothing to Linear — so without this the ticket sits in
   `Checks` until the hourly cron notices.

Note: labels are never per-project either, so `screen:`/`system:` labels from
both projects appear in one list. That is cosmetic only — the queue and
the mutex are scoped by project (DESIGN §2), so a `screen:home` in one
project never collides with a `screen:home` in the other.

## 9. Pipeline stats (optional)

The pipeline's measurement of itself (DESIGN §13): time in each state
per ticket, per milestone and per day, and the Actions minutes each
repository spends. It answers cost and trend questions and nothing in
the loop depends on it, so it can be skipped and added later — the
tracker keeps the history either way, and the first pass recomputes all
of it.

**a. The token.** `STATS_GITHUB_TOKEN`, per step 2: Actions →
Read-only on every repository whose minutes should be counted, this
repo included. Set it as an Actions secret on the **project** repo.

**b. The stub.** Copy `examples/stubs/pipeline-stats.yml` from the
pipeline repo into the project's `.github/workflows/` and edit its
`--repos` list. It runs nightly and on demand. Yours to commit by hand:
no pipeline credential carries `Workflows` (step 2).

**c. The first run.** Actions → **pipeline-stats** → Run workflow, and
read the job summary rather than the log. Three things on it are worth
a look, and each one is there because its failure is silent:

- **Archived count.** A zero on a project old enough to have archived
  tickets means the enumeration lost `includeArchived`. That does not
  fail — it drops the oldest tickets, which renders as a downward trend
  on every per-milestone chart with nothing about the output looking
  wrong.
- **Collected vs New.** The run table is insert-only, because GitHub's
  run data ages out and the stored row becomes the only copy. So a
  second pass reporting many collected and none new is the mechanism
  working. A *first* pass reporting that is a watermark being read from
  somewhere the previous pass did not write.
- **Backfill.** "budget spent, resumes next pass" is the normal shape
  of a first backfill, not an error: the jobs endpoint is one call per
  run, Catapult alone holds thousands, and the hourly API allowance is
  shared with the sweep. It grinds down a night at a time.

Then hold the month's total against **Settings → Billing**. That
comparison is the only cross-check the minute arithmetic has, and it
cannot be automated: account billing is not readable by a
repository-scoped credential, which is every credential the collector
runs under. The pass prints its own total so the comparison is one
glance. A figure that does not land near the host's own means the
arithmetic here is wrong.

**d. Cloudflare Access, which is what the dashboard authenticates
against.** The page is served by the metronome Worker at `/dashboard`,
not from the Pages output — one hostname for page and API means no CORS
and one Access application. It carries no credential of its own: a page
holding `PIPELINE_STATE_TOKEN` would hand it to everyone who opened the
page, and that token writes the move record too. Its viewer is the
credential instead, and an Access identity buys reads only.

Until `ACCESS_TEAM_DOMAIN` and `ACCESS_AUD` are set on the Worker, no
identity is accepted and the dashboard shows a 401 with the reason on
it. That is the shipped state, so the deploy carrying the page opens
nothing.

1. dash.cloudflare.com → **Zero Trust** → Access → **Applications** →
   Add an application → **Self-hosted**.
2. Give it the subdomain of the zone you already own — a hostname
   nothing is serving yet is fine, and doing this first means there is
   no window where it is public.
3. Policy: **Allow**, with an **Emails** include naming your own
   address. One rule; the point is a fence, not a directory.
4. Login method: **One-time PIN** needs no identity provider and works
   from a phone.
5. Copy the application's **Audience (AUD) tag** — it is on the
   application's Overview — and set both values on the Worker: Workers
   & Pages → `pipeline-metronome` → Settings → Variables and Secrets.
   `ACCESS_TEAM_DOMAIN` is the `yourteam` in
   `yourteam.cloudflareaccess.com`; `ACCESS_AUD` is that tag. Plain
   variables, not secrets — neither is one.
6. Point the hostname at the Worker: `pipeline-metronome` → Settings →
   Domains & Routes → Add custom domain. Then open
   `https://<that hostname>/dashboard`.

**Do not put Access in front of the `workers.dev` origin's paths, and
do not turn `workers_dev` off.** The webhooks post there and Access
would block them. That origin stays open, which is exactly why the
Worker verifies the Access token's signature and audience rather than
trusting the header Access sets: on `workers.dev` that header is
whatever the caller typed.

Cloudflare renames things in this dashboard from time to time, so treat
the labels above as the shape rather than the exact words. What matters
is that the hostname has an Access application in front of it before it
resolves to anything, and that `ACCESS_AUD` names *that* application —
one team signs for all of its applications with the same keys, so the
audience is what keeps a token minted elsewhere in the account out.

## Secrets recap

| Where | Name |
|---|---|
| dummy repo Actions secrets | `LINEAR_API_KEY`, `CLAUDE_CODE_OAUTH_TOKEN` and/or `ANTHROPIC_API_KEY`, `PIPELINE_REPO_TOKEN`, `CLOUDFLARE_API_TOKEN` (Pages only), `CLOUDFLARE_ACCOUNT_ID` |
| project repo Actions secrets | the dummy repo's set, plus `PIPELINE_STATE_TOKEN` and — with step 9 wired — `STATS_GITHUB_TOKEN` |
| pipeline repo Actions secrets | `LINEAR_API_KEY`, `CLOUDFLARE_WORKERS_TOKEN`, `CLOUDFLARE_ACCOUNT_ID`, `DISPATCH_TOKEN`, `LINEAR_WEBHOOK_SECRET`, `WEBHOOK_SECRET`, `REHEARSAL_REPO_TOKEN` |
| Cloudflare Worker secret | `DISPATCH_TOKEN` — uploaded by the deploy, not set by hand |

The two Cloudflare tokens are separate on purpose: project repos can
edit Pages and nothing else, and only this repo can deploy the
metronome. `DISPATCH_TOKEN` sits here as a repo secret because the
deploy pushes it into the Worker.

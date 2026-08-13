# Setup

The complete path from empty accounts to a running pipeline, in the
order that avoids backtracking. Steps marked ✅ are already done for the
scratch environment (ORC team → `orchestration-dummy`).

## 1. Linear

- ✅ A team for pipeline runs (**Orchestration / ORC**) and a project
  per target repo (**Test orchestration** for the dummy; the first real
  project gets its own).
- ✅ An API key: Settings → Security & access → **Personal API keys**.
  One key is fine to start — the config's solo-workspace exception maps
  it to both `author` and `controlplane`, which means the pipeline
  trusts everything that identity does. Give agents their own
  identities later if you want the writer matrix enforcing against you.
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

Two fine-grained PATs: github.com → Settings → Developer settings →
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
- Lives **twice, from one source**: as an Actions secret on this repo,
  which is where you set it, and as a Worker secret that the
  `worker-deploy` workflow uploads from it on every deploy. Setting the
  Worker's copy by hand is what lets the two drift; letting the deploy
  carry it means there is only ever one value to change. Named without
  a `GITHUB_` prefix because Actions reserves that prefix.
- One shared token is the recommended shape (see step 7). It is the
  least powerful credential in the system — the project repos' own
  workflows already hold contents:write, merge rights and the model
  key. Split into per-repo tokens only if you want no single credential
  able to start workflows in two projects, and then run one Worker per
  project.

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

## 3. GitHub secrets and settings

**On `orchestration-dummy`** (Settings → Secrets and variables →
Actions → Secrets):

- `LINEAR_API_KEY`
- `ANTHROPIC_API_KEY`
- `PIPELINE_REPO_TOKEN`
- `CLOUDFLARE_API_TOKEN` and `CLOUDFLARE_ACCOUNT_ID` (from step 4)

Settings → Actions → General:

- Workflow permissions → ✅ **Allow GitHub Actions to create and
  approve pull requests** (the harness opens PRs)

Optional: repo **variable** `PIPELINE_KILL_SWITCH` = `true` halts all
planning and dispatch (DESIGN §13); delete or set `false` to resume.

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
   has to agree is that repo's config `preview.pagesProject` — both the
   preview and cleanup workflows read it from there rather than
   carrying their own copy, so the config is the one place to set it.

   Branch previews then appear at
   `<branch-slug>.orchestration-dummy.pages.dev`, updated on every
   push — that's the whole preview feature; we build no machinery.

   The project's own `.pages.dev` root stays at the placeholder,
   because the preview stub deliberately skips `main`: design review
   reads branch previews, and publishing main would spend a deployment
   on a URL nothing in the protocol consults.

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
4. **Watch the deployment allowance.** Every push to every branch
   publishes a preview, and Cloudflare's free tier caps deployments per
   month. The `pipeline-preview-cleanup.yml` stub deletes a branch's
   previews when its PR closes, which keeps the steady state small; a
   heavy testing day can still hit the ceiling, and the symptom is a
   stale `Design review` link rather than an obvious failure. The fix is
   deleting old deployments in the dashboard or pausing the preview stub
   on the dummy.
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
   (state **Todo**, then move to **Designing** when ready), e.g. "Add a
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
| `GITHUB_WEBHOOK_SECRET` | The secret you set on the GitHub webhook below. You choose this one rather than being given it, so it can go in before the first deploy. |

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
- Secret: the value you put in `GITHUB_WEBHOOK_SECRET`
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
others running and un-killing never redeploys anything. Note it stops
the sweep from *planning*, not from *running* — the job still starts
and spends its minute.

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
with **Contents → Read and write** on `orchestration-dummy` only. The
reset commits reverts to `main` and deletes last run's branches, which
the read-only `PIPELINE_REPO_TOKEN` cannot do — and which is exactly
why it is a separate, narrower token rather than a widening of that
one.

**Each rehearsal:** Actions → **rehearse** → Run workflow.

| Phase | What it does |
| --- | --- |
| `full` | Reset then seed, and stop |
| `reset` | Archive every ticket; revert what the last run merged |
| `seed` | Create the scenario's milestones and tickets |
| `check` | Assert the final states, files and markers |

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
   `teamId`), its own `designOwnedPaths`, gates, deploy provider
   (`digitalocean` for a real DO app) and preview project.

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
   refuse, and a root `CLAUDE.md` holding that repo's own specifics —
   toolchain, gates, domain. The protocol half needs no copying: agent runs are
   given `prompts/repo-context.md` from the pipeline checkout at claim
   time, so editing it here reaches every project on its next run.
4. Secrets on that repo: `LINEAR_API_KEY`, `ANTHROPIC_API_KEY`,
   `PIPELINE_REPO_TOKEN` (the same token value as the dummy — it only
   grants read on the pipeline repo), Cloudflare pair,
   `DIGITALOCEAN_TOKEN` if DO-deployed. Same two settings toggles.
5. Pages project: Actions → **pipeline-pages-provision** → Run
   workflow, once. It creates the project named in that repo's config.
6. Metronome: add the repo to `PROJECTS` in `worker/wrangler.toml`,
   with its Linear project id as `trackerProject`, and add the repo to
   `DISPATCH_TOKEN`'s repository list. Merging the `PROJECTS` edit
   redeploys the Worker on its own — but widening the token is a
   separate act in GitHub's settings, and a beat that dispatches with
   a token that cannot reach the repo just logs a 404 every hour.
   One webhook on the ORC team serves every project in it; the
   `trackerProject` ids are what route each event to one repo.

Note: labels are never per-project either, so `screen:`/`system:` labels from
both projects appear in one list. That is cosmetic only — the queue and
the mutex are scoped by project (DESIGN §2), so a `screen:home` in one
project never collides with a `screen:home` in the other.

## Secrets recap

| Where | Name |
|---|---|
| dummy repo Actions secrets | `LINEAR_API_KEY`, `ANTHROPIC_API_KEY`, `PIPELINE_REPO_TOKEN`, `CLOUDFLARE_API_TOKEN` (Pages only), `CLOUDFLARE_ACCOUNT_ID` |
| pipeline repo Actions secrets | `LINEAR_API_KEY`, `CLOUDFLARE_WORKERS_TOKEN`, `CLOUDFLARE_ACCOUNT_ID`, `DISPATCH_TOKEN`, `LINEAR_WEBHOOK_SECRET`, `GITHUB_WEBHOOK_SECRET`, `REHEARSAL_REPO_TOKEN` |
| Cloudflare Worker secret | `DISPATCH_TOKEN` — uploaded by the deploy, not set by hand |

The two Cloudflare tokens are separate on purpose: project repos can
edit Pages and nothing else, and only this repo can deploy the
metronome. `DISPATCH_TOKEN` sits here as a repo secret because the
deploy pushes it into the Worker.

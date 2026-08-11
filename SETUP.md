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
- Lives as a **Cloudflare Worker secret**, not a GitHub secret — the
  Worker is the only thing that uses it. Named without a `GITHUB_`
  prefix because Actions reserves that prefix, and one name everywhere
  beats two.
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
Every reusable workflow declares its own `permissions:` block — dev,
design and reconcile take `contents: write` + `pull-requests: write`
(commit, open, un-draft and merge PRs), boundary takes `contents:
write` (the retro note), the sweep stub takes `actions: write`
(dispatching agents), record-deploy takes `deployments: write`. Nothing
to configure; it is declared per-workflow in the YAML.

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
- Settings → Actions → General → **Access** →
  ✅ *Accessible from repositories owned by SwaggerAllen* — required for
  the dummy's `uses:` calls against this repo's reusable workflows
  while it is private

## 4. Cloudflare — Pages (previews)

Everything happens in the one account that already holds your domains.

1. **Account ID**: dash.cloudflare.com → Workers & Pages → the
   **Account ID** is in the right-hand sidebar (also visible in every
   dashboard URL). This becomes the `CLOUDFLARE_ACCOUNT_ID` secret —
   not sensitive, stored with its token for convenience.
2. **Create one Pages project per project repo, in direct-upload mode.**
   Either:

   ```sh
   npx wrangler login   # one-time browser auth
   npx wrangler pages project create orchestration --production-branch main
   ```

   …or in the dashboard: Workers & Pages → Create → **Pages** tab →
   **Upload assets** (*not* "Connect to Git"), name it, and complete the
   first upload with any placeholder `index.html` — the preview
   workflow replaces it.

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

   **The Pages name need not match the repo name**, and here it does
   not: the dummy repo `orchestration-dummy` publishes to the Pages
   project `orchestration`. The only name that has to agree is that
   repo's config `preview.pagesProject` — both the preview and cleanup
   workflows read it from there rather than carrying their own copy, so
   the config is the one place to change it.

   Branch previews then appear at
   `<branch-slug>.orchestration.pages.dev`, updated on every push —
   that's the whole preview feature; we build no machinery.

   The project's own `.pages.dev` root stays at the placeholder,
   because the preview stub deliberately skips `main`: design review
   reads branch previews, and publishing main would spend a deployment
   on a URL nothing in the protocol consults.

   **Not shared between repos**: both repos deploy their `main` as the
   production deployment, so one Pages project serving two repos would
   have them overwriting each other, and a preview URL would not say
   which repo built it.
3. **API token**: dash.cloudflare.com → My Profile → **API Tokens** →
   Create Token → *Create Custom Token*:
   - Permissions: **Account → Cloudflare Pages → Edit** (nothing else)
   - Account Resources: your account only
   This becomes the `CLOUDFLARE_API_TOKEN` secret on **every** project
   repo. Pages permissions are account-scoped — there is no per-project
   Pages grant — so unlike the GitHub PATs, one token covering all your
   Pages projects is forced rather than chosen.
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

One Worker serves every project: `PROJECTS` in `wrangler.toml` is a
JSON array of `{repository, workflow, ref}`, one entry per project
repo, and one cron fires them all.

```sh
cd worker
# list every project repo in wrangler.toml's PROJECTS
npx wrangler deploy
npx wrangler secret put DISPATCH_TOKEN   # paste the step-2 token
```

Cron fires every 5 minutes and calls `workflow_dispatch` on each sweep
stub — punctual where GitHub's own `schedule:` jitters. A failing
dispatch is logged and skipped rather than stopping the others; the
sweep is convergent, so a missed beat costs latency, never correctness.
Verify in the Worker's dashboard logs (one dispatch per project per
tick) and each Actions tab (sweep runs arriving on the fives).

Halting stays per-project and outside the Worker: `PIPELINE_KILL_SWITCH`
is a repo variable the sweep reads, so stopping one project leaves the
others running and un-killing never redeploys anything.

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
3. Bootstrap the docs: run `prompts/bootstrap.md` with Claude Code,
   attended, to split the existing architecture doc into
   `systems/*.md` with file maps and stub `screens/*.md` (DESIGN §4).
4. Secrets on that repo: `LINEAR_API_KEY`, `ANTHROPIC_API_KEY`,
   `PIPELINE_REPO_TOKEN` (the same token value as the dummy — it only
   grants read on the pipeline repo), Cloudflare pair,
   `DIGITALOCEAN_TOKEN` if DO-deployed. Same two settings toggles.
5. Metronome: add the repo to `PROJECTS`, add it to `DISPATCH_TOKEN`'s
   repository list, redeploy the Worker.

Note: labels are never per-project either, so `screen:`/`system:` labels from
both projects appear in one list. That is cosmetic only — the queue and
the mutex are scoped by project (DESIGN §2), so a `screen:home` in one
project never collides with a `screen:home` in the other.

## Secrets recap

| Where | Name |
|---|---|
| dummy repo Actions secrets | `LINEAR_API_KEY`, `ANTHROPIC_API_KEY`, `PIPELINE_REPO_TOKEN`, `CLOUDFLARE_API_TOKEN`, `CLOUDFLARE_ACCOUNT_ID` |
| pipeline repo Actions secrets | `LINEAR_API_KEY` |
| Cloudflare Worker secret | `DISPATCH_TOKEN` |

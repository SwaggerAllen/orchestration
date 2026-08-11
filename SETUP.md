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
  create at least `M: first` (product) — and assign the seed tickets to
  it. Naming convention lives in the config (`Debt:` / `M:` prefixes).
- States and labels are **not** created by hand — step 5 provisions
  them idempotently.

## 2. GitHub tokens

Both are fine-grained PATs: github.com → Settings → Developer settings
→ Personal access tokens → **Fine-grained tokens** → Generate.

| Token | Repository access | Permissions | Used for |
|---|---|---|---|
| `PIPELINE_REPO_TOKEN` | Only `orchestration` | Contents: **Read-only** | Cross-repo checkout of the (private) pipeline repo from project workflows |
| `GITHUB_DISPATCH_TOKEN` | Only `orchestration-dummy` | Actions: **Read and write** | The Cloudflare Worker metronome starting the sweep — nothing else (step 7, can wait) |

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
2. **Create the Pages project** (direct-upload mode, which is what
   `wrangler pages deploy` publishes to):

   ```sh
   npx wrangler login   # one-time browser auth
   npx wrangler pages project create orchestration-dummy --production-branch main
   ```

   The name must match the config's `preview.pagesProject`
   (`orchestration-dummy`). Branch previews then appear at
   `<branch-slug>.orchestration-dummy.pages.dev`, updated on every
   push — that's the whole preview feature; we build no machinery.
3. **API token**: dash.cloudflare.com → My Profile → **API Tokens** →
   Create Token → *Create Custom Token*:
   - Permissions: **Account → Cloudflare Pages → Edit** (nothing else)
   - Account Resources: your account only
   This becomes the `CLOUDFLARE_API_TOKEN` secret on the dummy repo.
4. Preview URLs are unauthenticated (obscure subdomains). Fine at one
   author (DESIGN §4); **Cloudflare Access** (Zero Trust → Access →
   Applications, free ≤50 users) is the upgrade path if that stops
   being acceptable — gate `*.orchestration-dummy.pages.dev`.

## 5. Provision the Linear team

Actions (this repo) → **verify-live** → Run workflow →
config `configs/scratch.config.json`, **apply = true**.

This creates the fifteen states and eight labels in ORC, asserts a
second run is a no-op (the M0 idempotence gate), and runs a live sweep
dry-run. Re-run any time; it only ever creates what's missing and
refuses to retype live states.

Note: ORC keeps whatever default states Linear gave it (Todo, In
Progress, Done may collide by name — the provisioner adopts same-name
states if their category matches and errors loudly if not; resolve
those in Linear's UI by renaming the defaults away).

## 6. First end-to-end run

1. Merge the scaffold PR on `orchestration-dummy` (its own `ci` run is
   the first live gate check).
2. Create `M: first` in Test orchestration and a seed ticket in it
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

```sh
cd worker
# edit wrangler.toml [vars] if the repo/workflow names differ
npx wrangler deploy
npx wrangler secret put GITHUB_DISPATCH_TOKEN   # paste the step-2 token
```

Cron fires every 5 minutes and calls `workflow_dispatch` on the dummy's
sweep stub — punctual where GitHub's own `schedule:` jitters. Verify in
the Worker's dashboard logs (one dispatch per tick) and the dummy's
Actions tab (sweep runs arriving on the fives). The kill switch stays
in the project repo variable, so un-killing never redeploys the Worker.

## Secrets recap

| Where | Name |
|---|---|
| dummy repo Actions secrets | `LINEAR_API_KEY`, `ANTHROPIC_API_KEY`, `PIPELINE_REPO_TOKEN`, `CLOUDFLARE_API_TOKEN`, `CLOUDFLARE_ACCOUNT_ID` |
| pipeline repo Actions secrets | `LINEAR_API_KEY` |
| Cloudflare Worker secret | `GITHUB_DISPATCH_TOKEN` |

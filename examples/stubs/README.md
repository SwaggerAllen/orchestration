# Project repo stubs

Each project carries these ten-line stubs (DESIGN §5): `on:` can't be
inherited from a reusable workflow, and the stubs are where per-project
bindings live.

| File | Purpose |
|---|---|
| `pipeline-sweep.yml` | Control-plane pass: metronome dispatch + CI-hop trigger |
| `pipeline-agent-dev.yml` | Dev agent run, dispatched by the sweep |
| `pipeline-agent-design.yml` | Design agent run, dispatched on Designing entry and re-evaluate re-reads |
| `pipeline-preview-cleanup.yml` | Deletes a branch's previews when its PR closes |
| `pipeline-agent-reconcile.yml` | Reconcile agent run, dispatched on CI green |
| `pipeline-agent-boundary.yml` | Boundary agent run, dispatched on the author's signal |
| `pipeline-live-suite.yml` | The project's `:live` tests (real network), dispatched once when the boundary ticket opens; the result lands on the ticket before the author's pass. Its job must be able to *run* that command — dependencies and services included, not just the toolchain |
| `pipeline-order.yml` | What to start next and what can run beside it, on demand, rendered to the run summary. Reads the tracker; writes nothing |

**Every stub that runs the project's own commands carries the same
environment block, and it is your `ci.yml` job's environment.** An agent
runs the config's `qualityGates` before it finishes and the live suite
runs the project's test command, so those jobs need whatever those
commands need — the language, the dependencies, and any service the
tests talk to. Copy `ci.yml`'s setup steps and its `services:` block
across rather than reconstructing them; when `ci.yml` gains one, these
gain it too. Two runs were lost to reading the block as "toolchain
only", both with a correct toolchain: `mix test` could not reach a
database CI provides, and a live suite could not start because nothing
had installed dependencies. The second is the sharper one — a suite that
aborts before loading a test reports a red `fail`, not the honest
`no-tests`, so a project's first `:live` test appears to have broken the
build it was written to fix.

A project also needs:

- A **bootstrap pass** if the repo predates the pipeline: run
  `prompts/bootstrap.md` with Claude Code, attended, to split the
  architecture doc into `systems/*.md` with file maps and stub the
  `screens/*.md` docs (DESIGN §4)
- `pipeline.config.json` at the repo root (see `examples/pipeline.config.json`)
- Actions secret `PIPELINE_STATE_TOKEN`, matching the metronome
  Worker's `STATE_TOKEN`, plus a `state` block in `pipeline.config.json`
  naming the Worker's URL and this project's object. This is the
  pipeline's record of its own writes, and it is what makes the DESIGN §9
  invariants enforceable: the tracker cannot say *who* moved a ticket,
  because on a solo workspace the harness holds the author's key, so
  every pipeline write arrives wearing the author's identity. Without the
  store the pipeline records nothing and judges nothing — no reverts, no
  mutex enforcement at promotion, no hand-moved-claim detection. That is
  a safe state and a quiet one, so `preflight` and every `sweep` say so
  out loud rather than letting it pass for working.
- Actions secrets: `LINEAR_API_KEY`, and a model credential — a
  `CLAUDE_CODE_OAUTH_TOKEN` from `claude setup-token` (billed to a
  Claude subscription, tried first), an `ANTHROPIC_API_KEY` (billed to
  API credits), or both, in which case the key is the failover
- A `ci` workflow (name matters — the sweep stub triggers on its
  completion) triggered on `pull_request: branches: [main]` and
  nothing else. A `push` trigger alongside it runs the same tree a
  second time for the same verdict, and required checks read the PR
  run. Dropping `push: [main]` costs one post-merge sweep wake, which
  nothing needs: reconcile records the stand-in deployment itself, and
  that raises a `deployment_status` the metronome routes to a sweep.
  Read the branch from `github.head_ref` — under `pull_request` the ref
  is `refs/pull/N/merge`, so anything parsing the ticket key out of
  `GITHUB_REF_NAME` gets `N/merge` and silently audits nothing.

  It runs the quality gates from the config, plus `pipeline audit` —
  one command for all three of DESIGN §9's checks (mutex, doc lint,
  class):

  ```sh
  BASE="origin/main...HEAD"
  git diff --name-only "$BASE" > /tmp/changed
  git diff --diff-filter=A --name-only "$BASE" > /tmp/added
  pipeline audit --changed-files /tmp/changed --added-files /tmp/added \
    --ticket <key>
  ```

  `--ticket` needs `LINEAR_API_KEY` in the step's env — the class
  audit fetches the issue's labels and text from the tracker, and the
  command exits 1 before checking anything when the key is absent. The
  first project to arm this step learned it from a red run: the recipe
  as previously written omitted the key. (A checkout deep enough to
  reach `origin/main` is also required — `fetch-depth: 0`, since the
  default shallow PR checkout can't compute the diff base.)

  Put `pipeline` on PATH first with the `setup-pipeline` action —
  it checks out the pipeline and builds the binary once per pipeline
  commit, restoring it from cache after that. The agent actions call
  it themselves; a workflow of your own that runs `pipeline` has to
  ask for it.

  The ticket key parses out of the branch name. `--added-files` is what
  turns on the class audit — a component arriving and a component being
  edited are the same line in `--name-only`. Without it, or without
  `componentPaths` in the config, the audit says which check it skipped
  rather than reporting a clean run it didn't make.
- `pipeline-preflight.yml` (recommended). Exercises every
  credential-bearing call the pipeline makes, on demand. Permission
  gaps do not surface where they are introduced — they surface at
  whatever moment first needs the scope, which for the deploy read was
  the first ticket ever to reach `Merged`, four rehearsals later, with
  the ticket mid-flight. Run it after changing any permissions block,
  after rotating a token, and when setting a project up. Reads only —
  it names the write scopes it did not exercise rather than making
  writes somebody would have to undo, so a green run means "the reads
  are fine", not "the permissions are fine".
- `pipeline-order.yml` (recommended). Answers "what goes into
  `Designing` next, and what can run beside it" from the ticket graph,
  on demand — the question that comes up away from a terminal. The
  ordering is derived and never stored, deliberately (DESIGN §6): the
  mutex labels that decide what parallelises do not exist until the
  design pass writes them, so any saved order is wrong by construction
  rather than by neglect. Re-run it rather than keeping a copy.
- The Actions repo setting "Allow GitHub Actions to create and approve
  pull requests" enabled, so the harness can open PRs
- Repo variable `PIPELINE_KILL_SWITCH=true` to park the project. It
  halts dispatch (DESIGN §13) *and* skips the sweep job itself, so a
  parked project costs nothing — Actions bills each job rounded up to
  the minute, and an idle project on the hourly beat otherwise spends
  around 730 minutes a month producing no actions. Set it on a rehearsal
  repo between rehearsals and clear it before starting one; a rehearsal
  run against a parked project will sit there doing nothing, which looks
  exactly like a broken pipeline.
- Branch protection on `main`: require the `ci` checks and pull
  requests; within the pipeline only reconciliation merges (DESIGN §5),
  and the branch protection is what turns that from convention into a
  guarantee. The author's out-of-band fixes use admin bypass,
  deliberately.

# Project repo stubs

Each project carries these ten-line stubs (DESIGN §5): `on:` can't be
inherited from a reusable workflow, and the stubs are where per-project
bindings live.

| File | Purpose |
|---|---|
| `pipeline-sweep.yml` | Control-plane pass: metronome dispatch + CI-hop trigger |
| `pipeline-agent-dev.yml` | Dev agent run, dispatched by the sweep |
| `pipeline-agent-design.yml` | Design agent run, dispatched on Designing entry and re-evaluate re-reads |
| `pipeline-preview.yml` | Per-branch storybook export to Cloudflare Pages |
| `pipeline-preview-cleanup.yml` | Deletes a branch's previews when its PR closes |
| `pipeline-agent-reconcile.yml` | Reconcile agent run, dispatched on CI green |
| `pipeline-agent-boundary.yml` | Boundary agent run, dispatched on the author's signal |
| `pipeline-live-suite.yml` | The project's `:live` tests (real network), dispatched once when the boundary ticket opens; the result lands on the ticket before the author's pass |

A project also needs:

- A **bootstrap pass** if the repo predates the pipeline: run
  `prompts/bootstrap.md` with Claude Code, attended, to split the
  architecture doc into `systems/*.md` with file maps and stub the
  `screens/*.md` docs (DESIGN §4)
- `pipeline.config.json` at the repo root (see `examples/pipeline.config.json`)
- Actions secrets: `LINEAR_API_KEY`, `ANTHROPIC_API_KEY`
- A `ci` workflow (name matters — the sweep stub triggers on its
  completion) running the quality gates from the config, plus
  `pipeline audit` — one command for all three of DESIGN §9's checks
  (mutex, doc lint, class):

  ```sh
  BASE="origin/main...HEAD"
  git diff --name-only "$BASE" > /tmp/changed
  git diff --diff-filter=A --name-only "$BASE" > /tmp/added
  pipeline audit --changed-files /tmp/changed --added-files /tmp/added \
    --ticket <key>
  ```

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
- The Actions repo setting "Allow GitHub Actions to create and approve
  pull requests" enabled, so the harness can open PRs
- Optional repo variable `PIPELINE_KILL_SWITCH=true` to halt dispatch
  (DESIGN §13)
- Branch protection on `main`: require the `ci` checks and pull
  requests; within the pipeline only reconciliation merges (DESIGN §5),
  and the branch protection is what turns that from convention into a
  guarantee. The author's out-of-band fixes use admin bypass,
  deliberately.

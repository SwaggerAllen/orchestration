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
| `pipeline-agent-reconcile.yml` | Reconcile agent run, dispatched on CI green |
| `pipeline-agent-boundary.yml` | Boundary agent run, dispatched on the author's signal |
| `pipeline-record-deploy.yml` | Dummy project only: records a GitHub Deployment per merge so deploy detection has real data (config `deploy.provider: "github"`) |

A project also needs:

- A **bootstrap pass** if the repo predates the pipeline: run
  `prompts/bootstrap.md` with Claude Code, attended, to split the
  architecture doc into `systems/*.md` with file maps and stub the
  `screens/*.md` docs (DESIGN §4)
- `pipeline.config.json` at the repo root (see `examples/pipeline.config.json`)
- Actions secrets: `LINEAR_API_KEY`, `ANTHROPIC_API_KEY`
- A `ci` workflow (name matters — the sweep stub triggers on its
  completion) running the quality gates from the config, plus the mutex
  audit: `git diff --name-only origin/main...HEAD > /tmp/changed &&
  pipeline audit --changed-files /tmp/changed --ticket <key>` (the
  ticket key parses out of the branch name)
- The Actions repo setting "Allow GitHub Actions to create and approve
  pull requests" enabled, so the harness can open PRs
- Optional repo variable `PIPELINE_KILL_SWITCH=true` to halt dispatch
  (DESIGN §13)
- Branch protection on `main`: require the `ci` checks and pull
  requests; within the pipeline only reconciliation merges (DESIGN §5),
  and the branch protection is what turns that from convention into a
  guarantee. The author's out-of-band fixes use admin bypass,
  deliberately.

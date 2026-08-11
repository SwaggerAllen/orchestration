# orchestration

The pipeline repo for an automated design→dev ticket pipeline: a design
agent, a dev agent, a reconcile agent and a boundary agent, coordinated
entirely through Linear state and executed on GitHub Actions. This repo
is a **library, not a runner** — project repos call its reusable
workflows and carry thin stubs (DESIGN §5).

- **[DESIGN.md](DESIGN.md)** — the protocol. The single source of truth;
  every rule states its rationale inline.
- **[PLAN.md](PLAN.md)** — how it was built: three test rings, milestones
  M0–M7, each with an exit gate.

## Repo map

| Path | What |
|---|---|
| `cmd/pipeline` | The CLI: all pipeline logic lives here, workflows are thin shells |
| `internal/core` | The pure sweep: snapshot → actions, no I/O |
| `internal/plane` | Snapshot builder + action executor over the ports |
| `internal/agent` | Run-harness protocol: claim / finish / abort per agent kind |
| `internal/tracker`, `internal/host`, `internal/deploy` | Ports; Linear / GitHub / DO+GitHub-Deployments adapters, in-memory fakes |
| `internal/sim` | Ring-2 harness: full lifecycles on fakes and virtual time |
| `prompts/` | Agent base prompts (design, dev, reconcile, boundary) |
| `.github/workflows/agent-*.yml` | Reusable agent runs projects call |
| `examples/stubs/` | The stubs a project repo carries |
| `worker/` | The Cloudflare metronome (dumb, permanently) |
| `configs/` | Real project configs (ids only — never secrets) |

## Setting up a project

See `examples/stubs/README.md` for the checklist: config file, stubs,
secrets (`LINEAR_API_KEY`, `ANTHROPIC_API_KEY`), a `ci` workflow, branch
protection, and the metronome. `pipeline setup` provisions the Linear
team; `pipeline ids` prints the ids the config needs; `configs/README.md`
covers the scratch/dry-run environment.

## Secrets inventory

| Secret | Lives in | Grants |
|---|---|---|
| `LINEAR_API_KEY` | project + pipeline repo Actions secrets | tracker read/write |
| `ANTHROPIC_API_KEY` | project repo Actions secrets | agent model runs |
| `CLOUDFLARE_API_TOKEN` | project repo Actions secrets | Pages preview publish |
| `CLOUDFLARE_ACCOUNT_ID` | project repo Actions secrets | Pages preview publish (not sensitive, kept with its token) |
| `DIGITALOCEAN_TOKEN` | project repo Actions secrets (DO-deployed projects) | deploy detection |
| `GITHUB_DISPATCH_TOKEN` | Cloudflare Worker secret | starting the sweep, nothing else |

## The trust boundary — read before widening access

Agents read issue text, comments and diffs, and act with repository
write access; reconciliation merges to production unattended. **Anyone
who can write to the Linear project or comment on a PR can steer an
agent — the workspace roster IS the access control list** (DESIGN §9).
This is sound at one trusted author and must be revisited before anyone
else gets write access. What holds regardless: agent prompts treat
tracker and PR content as work to judge, never as instructions; CI gates
run unconditionally; branch protection makes "only reconciliation
merges" a guarantee rather than a convention; and the kill switch — the
`PIPELINE_KILL_SWITCH` Actions variable on the project repo — halts all
planning and dispatch without touching credentials.

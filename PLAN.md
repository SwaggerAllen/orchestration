# Implementation plan

Builds the pipeline specified in `DESIGN.md`. The design doc owns *what* and *why*; this doc
owns *in what order* and *how we know each piece works before it routes real tickets*.

**The governing constraint: everything is testable before it touches production.** A bug in
this system mis-routes tickets silently (DESIGN §5), so every milestone below ends with a test
gate, and the architecture is shaped so the whole protocol can run locally with no external
services at all.

---

## 1. Architecture decisions made for testability

**The control plane is a CLI, not YAML.** All pipeline logic lives in one command-line program
(`pipeline`) with subcommands (`sweep`, `setup`, `check`, …). GitHub workflows are thin shells:
checkout, install, run the CLI. Logic embedded in workflow YAML can only be tested by running
Actions; a CLI runs on a laptop against fakes, in CI against a scratch project, and in
production against the real one — same code path in all three.

**Language: Go for the pipeline, TypeScript for the metronome.** The control-plane CLI, agent
run harness, and test harness are one Go binary — a static executable that starts instantly on
every sweep tick, with `go-github` and the official Anthropic Go SDK covering two of the three
adapters. Linear has no Go SDK, so the Tracker adapter is hand-written GraphQL: bounded, one
module, simple queries, and it gets the heaviest test coverage anyway. The Worker is
TypeScript because that is what Workers support natively; a second language is acceptable only
because of the standing rule below. (Elixir matches the product stack but can't run in the
Worker and pays compile time on every tick; the pipeline repo never touches product code
directly.)

**The Worker stays dumb — permanently.** Cron fires → call `workflow_dispatch`. At the stage-2
escalation (DESIGN §13) it grows to validating a Linear webhook signature and forwarding the
payload into the same dispatch — still zero pipeline logic. This rule is what keeps the Go/TS
split from ever becoming load-bearing: every protocol behavior lives in one binary, and the
Worker never learns what a ticket is.

**A pure core behind adapter ports.** The state machine — invariant checks, precedence
ordering, escalation counting, stale-claim detection, queue pause, dispatch planning — is pure
functions from a *snapshot* (tickets, PRs, runs, deploys, clock) to a list of *actions* (state
moves, comments, dispatches, reverts). Side effects live in three adapters: **Tracker**
(Linear), **Host** (GitHub), **Deploy** (DO App Platform). Each adapter has a real
implementation and an in-memory fake. The pure core is where the protocol bugs would live, and
pure functions over snapshots are the cheapest thing in software to test exhaustively.

**Marker comments are a specified API, not a convention.** Every programmatic comment (DESIGN
§9) — dispatch ids, CI failure counts, bounce counts, boundary step completions, screenless
design passes — gets a versioned marker grammar defined in one module with parse/format
functions and round-trip tests. Two components communicate through these strings; an
unspecified string API is where the sim and production would quietly diverge.

**The clock is injected.** Stale-claim grace periods, deploy timeouts, and `Blocked`-age all
compare against a clock the caller passes in. In the simulator time is virtual (a failure that
takes a week to manifest is tested in milliseconds); in production it is the real clock.

---

## 2. The three test rings

Every feature passes through all three, in order:

**Ring 1 — unit + property tests (no I/O).** The pure core against handcrafted and generated
snapshots. Key property: a sweep over any snapshot produces actions that never violate a §9
invariant, and a second sweep over the resulting snapshot is a no-op (idempotence — the same
property the claim mechanism and boundary resume depend on).

**Ring 2 — local simulation (no network).** A `pipeline sim` harness wires the pure core to
in-memory fakes: fake tracker, real temporary git repositories (git is free and local — no
reason to fake it), scripted CI verdicts, scripted deploys, virtual clock. Agents are
**scripted stand-ins**: a "dev agent" that applies a canned patch and posts a hand-back, a
"reconcile agent" that returns pass / fail / cannot-tell on cue. Scenarios are files, so the
suite is a growing library of full lifecycles: happy path; CI red once then green; CI red
twice; reconcile bounce then pass; double bounce; stale claim mid-`In progress`; boundary
crash-and-resume at each step; screen-mutex collision at promotion; `re-evaluate` in every
state of the §7 table; kill switch mid-queue. This is where the *protocol* is proven, with the
model swapped out — deterministic, seconds per run, runnable before any account or secret
exists.

**Ring 3 — dry runs (real services, scratch targets).** The dummy project repo plus a scratch
Linear team, real Actions, real Worker on a staging schedule, real Claude agents. Identical
code and config schema to production; only the ids differ. This is where API reality bites —
rate limits, webhook shapes, permission scopes, actor attribution on state changes — and the
only ring where the agents' actual judgment is exercised.

### Standing dry-run infrastructure (built in M0, not at the end)

- **Dummy project repo** (`orchestration-dummy`): a deliberately tiny Phoenix + storybook app
  with the stub workflows and a `pipeline.config.json` pointing at scratch ids. Kept minimal so
  CI rounds are fast; realistic enough that design/dev/reconcile agents have something true to
  work on.
- **Scratch Linear team**, created once by hand, then owned by `pipeline setup` — an idempotent
  command that creates/verifies the states, labels, and Triage settings from DESIGN §3/§8 in
  any team it's pointed at. The same command later provisions real projects, so the dummy setup
  path *is* the production setup path.
- **`pipeline scenario`** (reset / seed / check / validate), driven by the `rehearse` workflow:
  reset archives every ticket and force-restores the dummy to its `seed` tag; seed writes a
  fixture's milestones and tickets; check asserts where they ended up. Dry runs must be
  repeatable from zero, or they stop being run. Two keys guard the destructive half — the config
  must declare itself `disposable`, and the project id is confirmed separately — and the harness
  is absent from `examples/stubs` so it can never be copied into a real project. There is no
  exercise phase: after seeding, the real metronome, webhooks and agents carry the tickets, which
  is the whole point of Ring 3.
- **`--dry-run` on every mutating command**: prints the action list without executing it. The
  first live sweep against any new project is always run this way.

---

## 3. Milestones

Each milestone names its scope and its exit gate. Later milestones assume earlier gates held.

### M0 — Foundations and harness skeleton
Repo layout, Go toolchain, CI for the pipeline repo itself. Config schema + validator
(everything in DESIGN §5's config list, including scratch/production being the same shape).
Marker grammar module. Adapter interfaces with in-memory fakes. `pipeline setup` working
against the scratch Linear team. Dummy repo created and seeded. Ring-2 harness skeleton runs
an empty scenario.
**Gate:** `pipeline setup` provisions the scratch team idempotently (second run is a no-op);
one trivial sim scenario passes; config validator rejects each field's absence with a message.

### M1 — The pure core
Snapshot model; sweep planner producing actions for: §9 invariant checks and reverts, §7
precedence and dispatch order, queue pause/drain rules, escalation counting from markers,
stale-claim detection, deploy-timeout detection, boundary-ticket triggers. No real adapters
yet.
**Gate:** Ring-1 suite green including the idempotent-sweep property; Ring-2 scenarios for the
full §7 re-evaluate table and all §12 escalations pass on virtual time.

### M2 — Live control plane
Real Linear and GitHub adapters behind the existing ports. `pipeline sweep` with `--dry-run`,
the Actions concurrency group, the kill switch (repo variable checked at sweep start).
Manual/`workflow_dispatch` triggering only — no Worker yet; a human pressing the button is the
metronome during development.
**Gate:** seeded scenario in the scratch team advances correctly through non-agent transitions
via repeated live sweeps; an induced illegal transition (hand-moving a mutexed ticket into
`Ready for dev`) is reverted with the right marker comment.

### M3 — Dev agent loop
Agent run harness in Actions: claim, pickup assertions (state + §9 invariants + base check),
Claude Code invocation with the dev prompt, hand-back comment, draft-off → `Checks`. CI
workflow in the dummy repo; `workflow_run` event hops for CI green/red including programmatic
failure comments. Branch/PR naming per DESIGN §5.
**Gate:** dry run: a seeded `Ready for dev` ticket ends in `Checks` green with a compliant
hand-back; a ticket engineered to fail CI twice ends `Blocked` with two marker comments.

### M4 — Reconciliation and deploy detection
Reconcile agent + prompt, three-outcome handling, merge via the reconcile identity, branch
protection on the dummy repo, post-deploy thin check. Deploy adapter: DO implementation plus a
GitHub-Deployments-backed fake used by the dummy repo, so dry runs exercise the `≥`-ancestry
logic without a real DO app.
**Gate:** dry runs land pass, fail→rework→pass, and cannot-tell→`needs-review` end-to-end;
merged-but-never-deployed scenario reaches `Blocked` on virtual→real timeout.

### M5 — Design agent loop and previews
Design agent + prompt, `Designing`/`Design review` flow, screenless auto-pass with marker,
storybook static export publishing to Cloudflare Pages from the dummy repo, sign-off actor
verification.
**Gate:** dry run: a screen ticket produces artifacts + a preview URL on the issue and waits in
`Design review`; a backend ticket auto-passes to `Ready for dev`; a non-author sign-off is
reverted.

### M6 — Milestone boundary
Boundary ticket creation/exclusions, pause and blocking-only drain, boundary agent with
step-comment resume, archive + retro note, bounded debt scan, grooming, Triage proposals with
dedupe keys.
**Gate:** Ring-2 covers crash-and-resume after each step; dry run of a full boundary on the
scratch team, including a kill mid-`In progress` and a resume that files no duplicate
proposals.

### M7 — Metronome, hardening, v1
Cloudflare Worker: Linear webhook receiver for the tracker hops plus an hourly cron for the
elapsed-time conditions that announce nothing (DESIGN §13), fine-scoped dispatch token,
stale-claim live test (cancel an agent run mid-flight, watch the sweep catch it), failure
injection day against the dummy, README (setup checklist, secrets inventory, trust-boundary
note per DESIGN §9), tag `v1` and pin the dummy to it.
**Gate:** the dummy project runs a small multi-ticket milestone unattended from `Todo` pull to
boundary `Done` with no human intervention except the designed touchpoints.

---

## 4. Prerequisites from the author

Needed by M0/M2, none blocking the start of M0's local work:

- Scratch Linear **team** (or workspace consent to create one) and a Linear API key.
- Consent to create the `orchestration-dummy` GitHub repo.
- Cloudflare account: a Pages project (M5) and a Worker (M7); API tokens scoped to each.
- An Anthropic API key for agent runs (M3 onward).
- GitHub: a machine identity for the reconcile agent's merges (fine-grained PAT or App) —
  needed at M4 when branch protection turns on.

## 5. Secrets inventory (grows with the milestones)

| Secret | Lives in | Used by | From |
|---|---|---|---|
| Linear API key | pipeline + project repo Actions secrets | control plane, agents | M0 |
| Anthropic API key | project repo Actions secrets | agent runs | M3 |
| Reconcile merge identity | project repo Actions secrets | reconcile merge | M4 |
| Cloudflare Pages token | project repo Actions secrets | storybook publish | M5 |
| GitHub dispatch token (workflow_dispatch only) | pipeline repo secret, uploaded to the Worker by the deploy | metronome | M7 |
| Linear webhook signing secret | pipeline repo secret, uploaded to the Worker by the deploy | webhook verification | M7 |

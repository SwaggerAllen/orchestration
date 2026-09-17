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
  reset archives every ticket and reverts exactly the commits those tickets merged, read off
  their `merged` markers before the archive hides them — never a rewind to a fixed commit, which
  would throw away the infrastructure that lands on a scratch project between runs; seed writes a
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

---

## 6. The cutover (C0–C6)

Implements the decisions in `docs/ops-free-pipeline.md`. That document owns *what* and
*why*; this section owns *in what order*, which is what §1's split has always meant.

**Only decided rows are here.** The run-harness port, the gate relocation into Catapult
and the Worker's disposition are still proposals (`ops-free-pipeline.md` §11) and have no
milestone until somebody rules on them.

### 6.1 Three constraints that shape the order

**Catapult is drained for every orchestration change** (CLAUDE.md): nothing dispatched,
nothing mid-flight. That is an argument for many small phases rather than few large ones —
a phase's length is a length of time the project is parked.

**The two-repo ordering rules decide several of these, and one case is not yet written
down anywhere.** The table in CLAUDE.md covers adding; the cutover also removes:

| the change | merges first | because |
| --- | --- | --- |
| a new protocol state | neither — both together, parked | the whole mapping is required *and* unknown keys are rejected |
| a new config **field** | this repo | `DisallowUnknownFields` rejects a key we have not declared |
| a new **value** for an existing enum (`deploy.provider: "render"`) | this repo | a project naming a value the binary refuses fails validation, which is the field case in miniature |
| **removing a config field** | **the project** — but see C4, it is three steps | undeclaring first makes the project's remaining key unknown, and every command fails |

**Take the baseline before anything lands.** It is the only step here that cannot be done
afterwards (`ops-free-pipeline.md` §9).

### 6.2 The milestones

**C0 — The baseline.** Run `pipeline stats collect` against Catapult to a finished
backfill, then capture the queries `docs/baselines/README.md` defines into a dated file.
Nothing else in this list is reversible with respect to it: once a change lands, the
before-measurement no longer exists.

Two things that document settles, both of which have to be right *before* the number is
taken rather than after:

- **The artifact is the requests and their responses, not a summary.** §9's comparison
  happens months later with no memory of which filters were used, and a figure nobody can
  re-derive is a claim. The store's read side echoes its own exclusions, so committing the
  response verbatim carries the derivation with it.
- **The headline is the per-state rows, never `agentTotal`.** That rollup sums three
  actors this cutover moves independently — agents (C1, C2, C6), CI (C3 and C5 both add
  time to `checks`), and the author in `design_review`, which nothing here touches. As one
  number, slower CI and a faster author cancel out and report no change.

*Exit: `docs/baselines/<date>.json`, its watermark showing a complete collection, and the
per-state and minutes responses stored as returned.*

**C1 — `CHANGE.md`.** The file convention, `.gitattributes` with `merge=ours`, the driver
line in the agent job, the ticket-key header and its assertion in `pipeline agent
claim`/`finish`, the design pass writing it, and the `git log <base>..main -p --
CHANGE.md` briefing handed to dev and reconcile. Amends DESIGN §4, which is where the
sketch is specified as having no file.
*Exit: a rehearsal ticket whose squash commit carries its own spec, and `git log --follow`
returning it. The `merge=ours` probe from §10 run, including the run without the driver
configured.* Both done; what remains for the exit is a rehearsal, which needs the project
drained.

Three things building it settled, all of which the decisions doc had wrong or silent:

- **The assertion is at finish, not claim.** The job claims before it checks out the ticket
  branch, so at claim the tree is still `main` and carries the previous ticket's spec.
- **`CHANGE.md` has to be in `designOwnedPaths`.** DESIGN §5's ownership audit refuses a
  design pass writing outside them, so the gate rejected the write §4 requires. A value in
  an existing array, so it merges project-side.
- **The dev job merges the base in, and C2's `conflict` flavour landed with it.** Nothing
  else in the pipeline does a three-way local merge — the two `git merge` calls are
  `--ff-only`, and the real merge is a server-side squash through GitHub's API, which
  cannot honour a local merge driver — so without this step a divergence reached reconcile
  as `ErrNotMergeable` and bounced the ticket to rework. It goes in dev rather than
  reconcile because a push during `Reconciling` restarts CI. The flavour and the merge had
  to land together: neither has a site without the other.
- **`decisionless` writes the sketch too**, which DESIGN §3 now states. That outcome used
  to commit nothing, so the ticket reached dev with no spec and §1.4's predicate — a commit
  changing code without changing `CHANGE.md` is the author's undesigned work — misread
  every decisionless dev commit as theirs. No decision to approve is not no plan to
  implement, and the holes would fall on exactly the tickets that look ordinary.

**C2 — The `conflict` flavour.** The six sites `ops-free-pipeline.md` §3.4 enumerates,
plus DESIGN §8's label table and §12's failure table, written as §2.4's merge-time half
rather than a second rule. Needs `pipeline setup --apply` per project to create the label.
*Exit: a rehearsal conflict parks the ticket with a comment naming the two claims, and the
mechanical predicate refuses to resolve a hunk touching a rule id.*

C1 before C2 is not arbitrary: the briefing is what makes a parked conflict answerable, so
shipping the park first gives the author a blocker with no better diagnosis than today's.

**C3 — Render, production first.** A `render` deploy provider in `internal/deploy`
alongside `digitalocean` and `github`, its fake, and the enum value — this repo first, per
the table. Then Catapult's production moves, and deploy detection is verified against a
real merge before anything depends on previews.
*Exit: a merge to Catapult's main detected through the Render adapter, `/health` answering
with the merge SHA.* The adapter, the enum value, `RENDER_API_KEY` in the sweep stub and
the setup probe are done; the exit is the Render side, which is provisioning and a merge.

**The status vocabulary is documentation, not a measurement, and it says so where it is
declared.** Render's reference gives eleven values and the adapter maps two groups of them;
the enumeration is the kind of claim about a real system that `tracker.Memory` got wrong by
copying the constants beside it, so it carries its source and its date and is re-checked
against a live service when one exists. Three facts it rests on that the adapter would read
backwards if they were wrong: a failed deploy leaves the previous one `live` (so `Failed`
and `ActiveSHA` are both set and name different commits); `deactivated` is a superseded
deploy rather than a failed one; and `canceled` is where Render's own health-check rollback
lands, which is why it is failed rather than in-flight.

**Two shape differences from the DigitalOcean adapter, both of which fail quietly.** The
response is a bare array of `{deploy, cursor}` wrappers rather than an object with a list
inside it — decoded into the wrong Go type that is an empty list and a *successful* call,
indistinguishable from a service that has never deployed. And Render's reference states no
ordering, so the adapter sorts by `createdAt` rather than trusting one: ordering decides
which deploy sets `Failed`, and assuming it would be a number nobody measured.

**What building it found, which was not in the Render half at all.** Preflight kept its own
copy of `deployPort`'s provider switch — the duplication `deployPort`'s own comment warns
about, in the same file as the warning. Adding the render case to one of them left preflight
unable to probe a Render project, printed as a project with no deploy configured rather than
as a gap. It now calls `deployPort`. Three further holes fell out of probing that: nothing
asserted an unknown `deploy.provider` was rejected; the two enum-walking tests would have
passed if `render` were deleted from the enum, because a loop over an enumeration shrinks
with it (the `tracker.Memory` shape again, from the test side); and the preflight check list
was asserted by grepping one file for a scope string, which stopped seeing a check that was
still being built. `hostChecks` exists so the deploy branch is asserted rather than grepped.

**C4 — Previews on, static storybook off.** Preview environments enabled with
`expireAfterDays` set, then the Cloudflare Pages path retired.

**The retirement is three merges, not two, and a naive two deadlocks.**
`config.Validate` requires `preview.pagesProject`, `preview.buildCommand` and
`preview.outputDir` unconditionally, so removing the block from Catapult's config fails
validation while removing the field here makes Catapult's block an unknown key — each side
alone is invalid, exactly the protocol-state shape that cost ninety minutes:

1. **This repo:** drop the three required checks, keeping the field. Valid against a
   config that has the block and one that does not.
2. **Catapult:** remove the `preview` block and `bin/preview-build.sh`; the
   `agent-design`/`agent-dev` actions stop publishing.
3. **This repo:** remove the `Preview` field.

*Render previews are per-PR, and the PR is already there.* `agent/design.go` creates the
draft PR at finish, on the `artifacts` outcome — which is the only outcome that reaches
`Design review`, and so the only one that needs a preview. What changes is that the
preview is published per branch today and per PR after.
*Exit: design review reads a storybook from a running preview; a failed or still-building
preview is reported on the ticket rather than silently absent — the one genuinely new
rule, since the Pages build could not fail by construction.*

**Merge 1 and the new rule are done; merges 2 and 3 are the project's and what follows
it.** The preview block is optional as a block and required as a whole, `host.PreviewFor`
reads the branch's preview from the code host, and the sweep announces it or says once
that it is not coming (DESIGN §4). What remains is Catapult removing its `preview` block
and `bin/preview-build.sh`, then the `Preview` field coming out here.

Four things building it settled that this entry had wrong or silent:

- **`previews.expireAfterDays` is Render's, not ours.** It is a `render.yaml` Blueprint key
  on the project, so "previews on with `expireAfterDays` set" adds no config field here and
  is entirely a Catapult change. Written as though it were a pipeline field, it would have
  been a fourth merge in the ordering table for no reason.
- **The preview is read from the code host, not from Render.** Render represents a PR
  preview as a GitHub deployment on the PR — its own changelog, 2024-09-09, replacing the
  comment it used to post. That keeps DESIGN §4's "reported by the publisher, never derived"
  intact for a publisher we no longer run, needs no second credential, and works for any
  platform with a GitHub integration rather than for Render alone.
- **The announcement cannot happen at design finish.** The PR is seconds old there, so the
  preview is always still building. §7.3's "Design review already hands the author the ball"
  describes where the ball is, not when the comment is posted; the comment is the sweep's.
- **Pending needs a bound and `deploy.timeout` is it.** Same question — how long to wait for
  a platform before saying it will not finish — and borrowing it invents no threshold, which
  is the failure CLAUDE.md records surviving review once.

The two publishing paths coexist while the cutover runs: a project still on Pages posts the
url-bearing marker from its design job, and the sweep reads that marker as terminal and
stays quiet. So merge 2 has no window to straddle.

**C5 — The manual test gate.** The largest item and the only new subsystem:
`tests/manual/**` in Catapult's `designOwnedPaths`, the judge session and its isolation
from the diff, evidence as run artifacts, verdicts as check runs, the seam-based per-PR
selection, and the boundary batch. Any new configuration is a config field, so this repo
first.
*Exit: a seeded defect of the shape §8 names — a declared-and-unwired crossing — caught by
the gate on a PR, with evidence; and a new test shown failing against the reverted commit
before it counts.*

Take §10's two measurements during C5 rather than after: what a judge pass costs against
the subscription, which sets the per-PR tier's size, and preview provisioning time, which
is the gate's latency on every PR.

**C6 — Conflicts as the mutex.** Last, because its payoff is conditional on concurrency
that does not exist yet: with one dev agent and one PR in checks, the label mutex rarely
fires, so this is the change with the least to show for it today and the most protocol to
move. Removes the screen and system label partition, amends DESIGN §6 and §7.

**Measured rather than assumed, because §14's prose reads worse than the code is:** §14
says system labels "extend the mutex and the re-evaluation machinery to declared
structure", which suggests the re-evaluation rules are entangled. They are not. The
collision verdicts live in `reconcile` with no core coupling to the prefixes at all — one
reference in the whole package.

**The rest of that survey was wrong, in the way a list in prose goes wrong.** Re-derived
from the tree rather than from the sentence above it, because the entry named some of the
sites and a partial list reads as a checklist:

| the surface | where |
| --- | --- |
| the ticket-side accessors | `MutexLabels`, `HoldsMutex` — `snapshot.go` |
| the strict reading (may this ticket *enter* a queue) | `MutexHolder` — `snapshot.go`, one caller in `sweep.go` |
| the tie-broken reading (may work *start* now) | `MutexBlocker` — `snapshot.go`, callers in `sweep.go` and `pickup.go` |
| a third, inline re-implementation | `order.go`, using `HoldsMutex` + `MutexLabels` directly |
| the pickup assertion's own check | `agent/agent.go`, `HoldsMutex` + `HasLabel` |
| the design pass's label reconciliation | `reconcileMutexLabels`, `staleMutexLabels`, `requiredMutexLabels` — `design.go` |
| the CI requirement | `filemap.Audit`'s "a changed path requires that doc's label" |
| the vocabulary | `protocol.ScreenLabelPrefix`, `protocol.SystemLabelPrefix` |

Two corrections in that table. **`MutexBlocker`'s callers are `sweep.go` and `pickup.go`,
not `pickup.go` and `order.go`** — `order.go` never calls it, and instead carries its own
inline loop over `HoldsMutex` and `MutexLabels`, which is the "two implementations of one
rule is how they drift" hazard sitting unnoticed in the survey that was supposed to find
it. And **`MutexHolder` is a third entry point the survey does not mention at all**,
distinct from `MutexBlocker` by exactly the deadlock tie-break.

**`filemap.OwnerLabels` survives C6, and its name will be the only thing left that says
"label".** It maps changed paths to the docs that own them, which the mutex used to consume
as "the labels this branch requires" — and which `manual-tests` now consumes as "the seams
this diff touches" (§8.3 rule 6). So C6 removes its pre-C6 caller and leaves the function,
renamed to say what it does.

**The deadlock the tie-break exists for is an argument for this change, not a footnote to
it.** `MutexBlocker`'s comment records ORC-171 and ORC-174 sharing two system labels and
sitting in `Ready for rework` for an hour and thirty-five minutes, each the other's
holder, the dev agent idle and the sweep planning nothing — broken only when the author
moved one to `Blocked`. Conflicts-as-mutex has no such state: both tickets proceed, and
git decides at merge time. The tie-break is a repair to a failure mode the label mutex
creates and the replacement does not have, which is the strongest thing in favour of
making the swap.

The sim scenarios asserting mutex behaviour are the other half of the work.
*Exit: the scenarios rewritten to assert conflict-parking where they asserted mutex
refusal, and a rehearsal with two overlapping tickets landing without a label between
them.* The behaviour change is done and the scenarios are rewritten; the rehearsal is a
manual step, and it is the one exit in C0–C6 that **cannot** be taken before the pipeline
is running on Render.

**The labels survive this and their removal is a separate change.** They stopped being a
lock; they are still the declared-scope audit CI runs against the file maps, and the
ownership check, the record review and the manual test gate's selection all read them. So
C6 as landed is the behaviour, and what remains is deletion:
`reconcileMutexLabels` and its two helpers, `filemap.Audit`'s label requirement,
`protocol`'s two prefixes, `plane.EnsureMutexLabel`, §7's discovered-label machinery
(`resolveDiscoveredLabel`, `flagCollisions`, `recordDiscoveredLabels`), setup's label
provisioning, and the Linear labels themselves.

**Do that one only after a rehearsal**, because it is the half with a live-system failure
mode: the labels are what CI currently demands of a diff, so removing the audit and the
creation in the wrong order fails every PR or leaves every PR demanding a label nothing
creates. The ordering rule is the config-field one in reverse — the audit is the binary's
and the labels are the tracker's, so the audit's requirement goes first and the
provisioning second.

**Two predicates were renamed rather than removed**, because §7's collision rule still
reads them: `HoldsMutex` is `InFlightOnScope` and `MutexLabels` is `ScopeLabels`. A
function called `HoldsMutex` in a system with no mutex is a comment lying in the one place
a reader cannot skip.

**What the removal turned up, and it is the strongest thing in the entry:** the argument
against labels on unowned files — the router, the manifests, where giving them labels
would serialize every ticket through them — was in DESIGN §6 from the beginning. It was
the argument against labels *anywhere*, applied to the one case where the cost was
obvious.

### 6.3 What this does not include

No time estimates. Five of the seven are contained changes; C5 is a new subsystem and
should not be sized against the others. The figure worth watching is C0's, measured again
after C5 — which is `ops-free-pipeline.md` §9's check and the only claim any of this makes
about whether it worked.

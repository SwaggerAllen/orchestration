# Ops-free pipeline — decisions

**What this is.** Decisions taken in a design conversation about changing how this
pipeline is built and verified. Nothing here is built. `DESIGN.md` stays the protocol
and the single source of truth for what the pipeline *does*; each decision below amends
it in the commit that implements it, and this file loses the entry at the same time.
`PLAN.md` owns build order and test rings; this file owns neither.

**Style requirement, inherited:** every rule states its rationale inline, and records
the failure it exists to prevent. A rule without its reason is a rule the next session
violates reasonably.

**Scope.** Catapult is out of scope as a *feature* target — nothing here builds Catapult
functionality. Two things about delivering it are in scope and land with these changes:
Catapult's deployment moves wholly to Render, which is what makes per-PR previews
possible (§7), and the AI-driven manual test gate those previews carry (§8). What stays
out is per-PR environments for the projects Catapult *produces*, which belong to the
hosted tier (§7).

---

## 0. The measurement that started it

Taken 2026-09-16, on `75422ed`:

```
find internal -name '*.go' -not -name '*_test.go' | xargs cat | wc -l   -> 18454
find cmd      -name '*.go' -not -name '*_test.go' | xargs cat | wc -l   ->  4524
worker/*.ts, excluding *.test.ts                                        ->  2114
                                                                   total ~25092

wc -l prompts/*.md                                                      ->  1224
wc -l DESIGN.md PLAN.md                                                 ->  3158
```

Roughly twenty lines of control-plane machinery per line of agent prompt. The prompts
are what the pipeline is for; the rest is a bespoke distributed system that two
repositories must agree about, and it is where the recorded outages come from — one
ticket marked `Duplicate` taking every sweep down for two hours (DESIGN §3), the
ninety-minute window adding `ready_for_design` (CLAUDE.md), `citationShorthands`
failing Catapult's whole audit with `json: unknown field` (CLAUDE.md).

**The number is a motivation, not a target**, and it turned out to motivate something
smaller than it first appeared to.

The ratio suggested most of the machinery could be composed away. Working the accounting
package by package (§11) did not support that. Linear stays the state machine because a
tracker that does not hold state gives the pipeline two answers to *where is this ticket*
(§11.1). The adapters stay because the control plane is a program whose decisions must be
reproducible, and MCP is for the questions an agent asks rather than the decisions the
plane makes (§11.4). Stats stays because it is the only instrument that measures whether
any of this worked (§11.5). What is left as a genuine replacement is the run harness, and
what is left as a genuine relocation is the gate set — everything else is kept, moved, or
still undecided.

**So the honest framing is not "compose the pipeline away" but "a set of targeted
changes to it."** The blank-slate reading is dead, and §9's check was rewritten once it
became clear that a `DESIGN.md` which did not shrink is now the *expected* outcome rather
than a failure signal.

---

## 1. `CHANGE.md` — one spec file, overwritten per ticket

**Decision.** A single `CHANGE.md` at the repository root. Every ticket's design pass
overwrites it **wholly** with that ticket's spec. It is never deleted and never
accumulates: one file, one revision per merged change.

**Every design pass that commits, including `decisionless`.** That outcome has no decision
for the author to approve, which is why it skips sign-off — but it read the scope and it
has a plan, and DESIGN §3 now says it writes the sketch like any other. Exempting it would
put holes in the history at exactly the tickets that look ordinary, and §1.4's out-of-band
predicate would then misread every decisionless ticket's dev commit as the author's.

**What it holds: the sketch.** DESIGN §4's sketch — the structural output of a design
pass — has no file today. It is a diff against `systems/*.md` plus a touch list carried
in a marker comment on the ticket. Giving it a file is what makes everything below
possible; there is otherwise nothing whose history could be read.

It does not duplicate the Linear description. The description is the immutable argument
(§2.3) and stays canonical for *why*. `CHANGE.md` is *what will be built and where*.

**It is design-owned, and has to be.** DESIGN §5's ownership audit refuses a design pass
that writes outside `designOwnedPaths`, so without the file listed there the gate rejects
the very write this section requires — which is how it was found. Being design-owned is
also what keeps dev out of it: dev implements against the sketch, it does not rewrite it.
Every project's config gains the entry, which is a value in an existing array rather than
a schema change, so it merges project-side with no ordering hazard (§11's table).

**Why one overwritten file rather than `specs/<TICKET>.md` per ticket.** A per-ticket
directory grows without bound, and every entry in it is a claim about the tree that was
true when it merged and is evidence about nothing afterwards — the failure Catapult's
conventions record as "a doc's claim about the tree is not evidence about the tree". It
would need a file-map rule to keep passes from reading stale entries as authority. One
overwritten file needs no such rule: there is only ever one, and it is the current one.

**The tie to the commit is the commit.** The file is written on the ticket branch, so
the squash merge carries the spec and the implementation as one commit.
`git log --follow -- CHANGE.md` returns those commits directly. No sha in a filename,
no archival move, nothing for reconcile to do at merge time.

A move at merge was considered and is wrong: reconcile merges from `Reconciling`, and a
push to the branch there restarts CI and re-enters the state machine at `Checks`.

### 1.1 The conflict briefing

```sh
git log <merge-base>..main -p -- CHANGE.md
```

is the argument for every pipeline change that landed while a ticket was out, in merge
order. **This is the point of the whole arrangement.** DESIGN §2.4 states that the
resolution rule for a moved base "is semantic and has no git equivalent"; this is the
git equivalent. An agent resolving a conflict stops inferring intent from a diff and
reads the intent that was recorded at the time.

### 1.2 `CHANGE.md`'s own conflict is noise, and is resolved by git

Two tickets whose lifetimes overlap conflict on `CHANGE.md` **every time and wholly** —
each replaced the entire file — and the resolution is always *take ours*, because a
ticket's spec is its own and the other one is already in history.

Left to the agent that is a conflict it learns to resolve without reading, in the same
merge where it must resolve real collisions in `DESIGN.md` carefully. So git resolves
it instead: `CHANGE.md merge=ours` in `.gitattributes`, with

```
git config merge.ours.driver true
```

in the agent job. Git does not ship that driver; `driver = true` is the whole
definition (keep our version, exit zero).

**A driver needs a local merge, and the pipeline had none.** Measured 2026-09-16: the only
two `git merge` calls in the repo — the boundary's and the rehearsal's — are `--ff-only`,
which never runs a driver because no merge happens, and the real merge is `host.MergePR`, a
**server-side squash** through GitHub's API that cannot honour a local merge driver.

Left there, the consequence of one overwritten file would not have been a conflict an agent
resolves. It would be a **PR GitHub reports un-mergeable**: `MergePR` returns
`ErrNotMergeable`, `reconcile.go` bounces the ticket to `Ready for rework`, and §12
escalates a second bounce to `Blocked`. Every pair of overlapping tickets would take the
second one down that path — a conflict reported as a rework, to an agent with nothing to
fix, and strictly worse than the label mutex §2 retires it for.

**So the dev job merges the base in, and that is what gives the driver a site.** It belongs
there rather than in reconcile, for the reason stated above: a push during `Reconciling`
restarts CI and re-enters the state machine at `Checks`. It is also overdue independently —
CI already tests `refs/pull/N/merge`, so a branch that has not merged main is not the tree
CI went green on. A merge that conflicts parks under §3's `conflict`, which is why the two
landed together: a flavour with nothing to fire on is the `Reserve`/`Release` shape CLAUDE.md
records, and so is a driver with no merge.

**Probed, including the control run without the driver:**

```
without merge.ours.driver  -> CONFLICT (content): Merge conflict in CHANGE.md
with it                    -> merged cleanly, ours kept
both files diverged        -> systems.md conflicts, CHANGE.md resolved
```

The third run is the one worth keeping. It proves the driver is scoped to the one path
rather than swallowing a real collision — which is the failure that would have made this
mechanism dangerous rather than merely inert.

### 1.3 The file names its ticket, and the harness asserts it

Line one is `# <TICKET-KEY> — <title>`, and `internal/changespec` is its grammar — a
parse/format module rather than a convention, for the reason PLAN §1 gives about marker
comments.

**The assertion lives at finish, not at claim, and the ordering is why.** The agent job
claims *before* it checks out the ticket branch, because the branch is an output of the
claim — so at claim time the tree is still on `main`, carrying the previous ticket's spec.
A claim-time assertion would fail on every ticket for a reason that has nothing to do with
the ticket.

The design finish holds an artifacts pass to both halves: that the file **names this
ticket**, and that **this pass wrote it**. The second is not redundant. The file sits at
the root of every branch and always names somebody, so a pass that edited screens and
systems and left the previous ticket's spec in place would satisfy the first check for the
worst possible reason.

**This is what makes the automatic resolution safe rather than merely convenient.** A
resolution that went the wrong way, a design pass that forgot to overwrite, and a dev
pass that reverted the file all produce the same observable: a spec naming the wrong
ticket. Without the assertion all three are silent, and `merge=ours` is the mechanism
that would hide them.

It also makes `git log -p -- CHANGE.md` self-labelling, which is what a reader of the
ledger needs and what §1.1 hands to an agent.

### 1.4 The author's out-of-band changes carry no spec

Undesigned changes land on `main` without a ticket, on purpose (DESIGN §2.5). They
never touch `CHANGE.md`, so the §1.1 briefing is silent on exactly the class §2.4
exists to warn about. **Named here because it is a real hole and would otherwise be
found by a conflict it failed to explain.**

It inverts into something the pipeline does not have today: **a commit that changes code
and does not change `CHANGE.md` is out-of-band, by construction.** §2.5's category stops
being an assertion and becomes a predicate. So the briefing is *all* intervening
commits, annotated: these carry specs, these are the author's.

### 1.5 Dependencies and consequences

- **Squash merge is load-bearing.** One spec revision per merged change holds only
  because reconcile squashes. Under a merge commit every intermediate rework revision
  enters the ledger and the property quietly stops being true. State this where the
  merge method is chosen, not only here.
- `git blame CHANGE.md` is useless — every line comes from the last ticket.
  `git log -S'<text>' -- CHANGE.md` is the tool, and "which ticket decided this" is one
  pickaxe over one file.
- The record review always sees a 100%-changed diff for it, so for this file the
  reviewer reads the new content rather than the diff.
- **The name.** `spec.md` reads as "the specification", which is `DESIGN.md`'s job.
  `CHANGE.md` is true at every commit — *the change that produced this tree* — which is
  what a reader arriving via `git log` with no other context needs.

---

## 2. Conflicts are the mutex

**Decision.** Textual conflict is the collision detector, and the cost — an agent
reconciling merges — is accepted. Every change carries its spec, so the spec is present
in the merge that has to be reconciled.

The mutex this replaces is DESIGN §6's screen and system labels, which approximate
semantic collision by declaring a touch list up front. §14 already records the limit:
"the mutex's quality is the partition's quality", and files owned by no system are
covered only textually by git anyway. Conflicts cover what the partition covers and the
gap the partition leaves, at the cost of arriving later — at merge rather than at
pickup.

**They arrive later, and that is the trade.** A label collision is caught before an
agent starts; a conflict is caught after the work exists. What makes it acceptable is
§1.1: the agent resolving it now has the arguments for both sides, which the label
mechanism never gave anyone.

---

## 3. The `conflict` flavour

**Decision.** A new abort reason and `Blocked` flavour, `conflict`: *an agent that
cannot resolve a merge*. It parks exactly as `prerequisite` does — the agent changes no
files and names the outcome, and the harness moves the ticket, because an agent has no
credential to move a ticket itself (§2.7).

### 3.1 It is §2.4's other half

DESIGN §2.4 already states the rule: the repo moved, both changes touch the same
behaviour, **repo wins and the ticket stops**, parking in `Blocked` for the author to
route to `Ready for redesign`. That is checked at *pickup*, against base-rev.
`conflict` is the same judgment, the same destination and the same route out, checked at
*merge*.

**Write them as one rule with two check times.** Two rules that say the same thing in
two places drift, and the drift is invisible until they disagree — the hazard Catapult's
conventions record as six consecutive review rounds each correcting one statement of a
rule and leaving its siblings.

### 3.2 The park predicate is mechanical

Judgment decides too much otherwise, and "never reconcile by guessing" (§2.4) needs a
floor that does not depend on the model having a careful day.

- A conflict hunk touching **a rule id, a standing decision, or a `.reasons.md` entry**
  → park. Always, with no resolution attempt.
- Anything else → the agent may resolve it **only if it can restate both sides in one
  sentence** in the hand-back. Otherwise park.

The first clause is decidable by grep, which is the point. Rule ids and `.reasons.md`
siblings already exist (DESIGN §4, Catapult's conventions §12), so the expensive case —
two designs disagreeing about a rule — is detected without asking the model what it
thinks a hunk means.

### 3.3 The comment carries the diagnosis, not the category

Which files, which rule ids, and **what each side asserts**. A comment reading "merge
conflict in DESIGN.md" is `staleClaimFor`'s defect one size down: fixed text that sends
the reader hunting, on a ticket where the reader cannot check it. Naming the two claims
is what lets the author decide without opening a three-way diff.

### 3.4 Every site the word has to land

Pinned in more than one place by construction, and this repo's CLAUDE.md records what a
partially-updated pin costs:

| site | what |
| --- | --- |
| `internal/protocol/protocol.go` | `Labels` |
| `internal/protocol/vocabulary.go` | `AbortReasons` |
| `internal/marker/marker.go` | the flavour list in the blocked-arrival split |
| `internal/marker/prose_test.go` | two flavour lists |
| `prompts/dev.md`, `prompts/reconcile.md` | the outcome an agent may report |
| `DESIGN.md` §8, §12 | the label table and the failure table |

Keep one spelling everywhere. The existing `needs-setup` label against its `setup`
marker flavour is a skew that has to be remembered; do not add a second.

---

## 4. The runner is ours

**Decision.** Everything that invokes the model runs in an environment we control —
GitHub Actions today. No third-party agent runner, including Linear's.

**The reason is the credential.** Agents run on a Claude Code subscription OAuth token,
not an API key, and that is the difference between this project costing $200 a month and
costing thousands. A subscription token only works inside Claude Code in an environment
we control, so any service that would drive an agent for us would have to hand the work
back to Actions regardless — and two dispatchers quietly defeats every single-agent
guarantee downstream (§13).

**This says nothing about what Linear holds.** The credential argument settles who runs
the model and stops there; it is easy to read one step further and conclude the tracker
is therefore only a board, which does not follow. Linear keeps intake, priority,
attention, webhooks **and the state machine** — §11.1 has that decision and its reason.

**The concurrency ceiling is the subscription, not the infrastructure.** This is the
load-bearing consequence and it decides §6 and §8.2: building isolation to run agents the
plan cannot feed buys nothing, and a judge pass spends the same allowance the dev agent
does.

---

## 5. Orchestration has no environment to provision

**Decision.** No per-PR environment for this repository. There is nothing to host.

The deployables are a static Go binary, built inside the job that runs it, and one
Cloudflare Worker. There is no database, no container and no App Platform app — the
DigitalOcean app belongs to Catapult, and this pipeline only reads its deployments API
to confirm a SHA landed (`SETUP.md` provisions no app for this repo).

**Orchestration's own PRs get no AI-driven manual tests, and Catapult's do (§8).** The
asymmetry is deliberate rather than a sequencing accident. This repo's output is a state
machine, and the gates plus the rehearsal already assert it end to end against a real
project in terms the pipeline controls — states, files, marker kinds and marker fields.
Judged assertions would buy nondeterminism for coverage the fixtures already have.
Catapult's output includes generated prose and a running surface, which the fixtures
cannot reach at all; that is the difference, and it is the whole of it.

---

## 6. The rehearsal is the environment, and it is a tenant

**Decision.** `orchestration-dummy` is the only environment, and a "PR environment" for
it is three ids rather than a deployment:

| piece | isolation | cost |
| --- | --- | --- |
| Go binary | built from the branch | already happens |
| Worker | `wrangler deploy --name pipeline-metronome-pr-<n>` | free tier |
| Durable Objects | follow the script name — a separate script is a separate namespace | free, SQLite backend |
| Linear | a project inside the dummy team; states and labels are team-scoped (`SETUP.md` §8) | free |
| GitHub | `orchestration-dummy`, or a repo from a template | free |

**One tenant is enough**, because §4's ceiling and DESIGN's single-dev-agent invariant
both hold concurrency at one. The tenant model is chosen anyway because it scales by
adding ids rather than infrastructure when §14's concurrent-dev decision is taken.

**A per-PR Worker gets no cron trigger.** `wrangler.toml` declares `crons = ["0 * * * *"]`;
a tenant carrying it is a second metronome dispatching into whatever `PROJECTS` it
holds. Deploy it trigger-less and drive it by webhook.

### 6.1 Two changes make the rehearsal a PR gate

1. **A reset that cannot silently do nothing.** `SETUP.md` §7b records three ways the
   current reset has reverted nothing and reported success — markers archived by a
   boundary, a retro note skipped as already-present, a reset that could not read the
   notes. On a disposable tenant, recreating the repository from a template is a
   stronger reset than reverting, and it deletes the "which commits were ours"
   machinery entirely.
2. **A lease.** One Actions `concurrency` group across the rehearsal workflow. That is
   the whole mutex, and it is the same mechanism §13 already uses to keep one
   dispatcher.

---

## 7. Catapult moves to Render, entirely

**Decision.** Catapult's production deployment moves off DigitalOcean App Platform to
Render, and Render's preview environments carry per-PR instances for the §8 gate. One
deployment setup, not two — a preview that differs from production is a gate measuring
something other than what ships, and two platforms is two build paths to keep true.

**The scope boundary, stated because it is the one most easily widened:** this covers
**Catapult's own repository**. It does **not** cover per-PR environments for the projects
Catapult *produces*. Those are the hosted tier — many tenants, customer content, cost
control — and they are a product decision about Catapult's runtime rather than a CI
decision about our own pull requests.

**Kubernetes belongs to that second question and to no part of this one.** The expensive
part of a CI preview is the data — Postgres, EventStore and Oban provisioned, migrated
and seeded — and a cluster supplies pods cheaply while doing nothing about that.

**Fly.io is out**, on the author's own experience of its downtime. Not a price-or-fit
judgment and not open to re-argument on those grounds.

### 7.1 What the cutover costs

Prices read 2026-09-16 from each vendor's own pricing page; the preview figures are
arithmetic over those, not a quote.

| | App Platform today | Render after cutover |
| --- | --- | --- |
| Workspace fee | none | **$25/mo** (Pro — previews require it) |
| Production web | $5–$25/mo by tier | $25/mo (Standard, 2 GB / 1 CPU) |
| Production Postgres | $7/mo (dev database) | $6/mo at the smallest paid tier |
| Per-PR previews | **not offered** | prorated by the second |
| **Fixed floor** | **~$12–$32/mo** | **~$56/mo** |

**Previews are billed as ordinary services, prorated by the second**, so a preview costs
its *lifetime*, not its existence. One Standard web service plus one small Postgres is
about **$0.043 per preview-hour**:

| 40 PRs a month, mean preview life | added cost |
| --- | --- |
| 3 hours | $5 |
| 12 hours | $20 |
| 24 hours | $41 |
| 48 hours | $82 |

**So the cutover is roughly $30–$105/month more than today**, and the spread is almost
entirely preview lifetime. Against the $200/month the model credential already costs
(§4), the fixed part is proportionate; the variable part is worth controlling, and
`previews.expireAfterDays` is the control.

**The finding worth carrying out of this table: a preview's life is mostly the author's
response latency.** A ticket sits in `Design review` waiting for sign-off and in
`Blocked` waiting for a judgment, and the preview bills throughout. The three human
touchpoints §0 of `DESIGN.md` budgets for are now a line item, which is a new fact about
them and not an argument against them.

### 7.2 What is not known, and why the floor is a range

**Catapult's current App Platform instance size is not recorded anywhere in the repo, on
purpose.** `SETUP.md` §2 states that the live app is the authority on its own
configuration and that no app-spec file is committed, because one existed, was read by
nothing, and drifted from reality five times in an afternoon. So the DO column is a tier
range rather than a number, and closing it is a spec export from the dashboard — §10.

**The Render Postgres tier is a guess.** The smallest paid tier is 256 MB of RAM, and
Catapult runs an EventStore alongside Oban and application data. 256 MB is very likely
undersized for that, which would move both the production line and every preview's
hourly rate. Size it against the real workload before the table above is treated as the
answer.

### 7.3 The static storybook retires

**Decision.** `bin/preview-build.sh` and the Cloudflare Pages storybook go; design review
reads the storybook from the running Render preview, which `CatapultWeb.Router` already
serves.

**This removes the reason `bin/preview-build.sh` never exits non-zero.** That contract
exists because the agent action wraps the build in `set -euo pipefail`, so a failing
preview would fail the agent job and stop tickets dispatching. Under Render the preview
build is Render's, out-of-band from the agent job entirely, so a failed preview *cannot*
fail a dispatch. The problem the never-fail rule solved is solved better by moving the
build out of the job than by making it incapable of failing.

**Render previews are per-PR, and the design pass already opens one.** `agent/design.go`
creates the draft PR at finish when the ticket has none, and `prompts/design.md` states it
in the `artifacts` outcome. So the PR exists before `Design review` is entered, which is
when the preview is read.

The match is exact rather than lucky, and worth stating so the next pass does not
re-derive it: **the only outcome that needs a preview is the only outcome that opens a
PR.** `artifacts` commits and hands the ticket to the author, so it opens one.
`decisionless` commits nothing and advances straight to `Ready for dev`, a record-review
decline explicitly opens no PR, and `prerequisite` parks — none of the three reaches
`Design review`, and none needs a preview.

What changes is narrower than it first looks: the preview is published per *branch* today
(Cloudflare Pages), and becomes per *PR*. The PR was always there.

**One thing does need a new rule.** A Render build that fails, or that has not finished
provisioning, leaves design review waiting on a preview that is not coming, and nothing
notices today — the Pages build could not fail by construction, so there was nothing to
report. **A failed or pending preview is reported on the ticket.** Not a new `Blocked`
flavour: `Design review` already hands the author the ball, so this is a comment that
makes the ball they hold one they can act on.

---

## 8. AI-driven manual tests of Catapult

**Decision.** In scope, landing with §7 rather than deferred behind it.

**The defect profile is the argument.** The tickets this pipeline has been fixing on
Catapult are seam defects — a chain handing every agent an empty context, atoms reaching
a projector as strings, `declared_in` paths spelling with underscores what the schema
spells with hyphens. Both sides correct, the crossing unasserted. A green unit suite
cannot see any of them, and this repo's own CLAUDE.md records the same shape from the
other end: breaking the host-to-core outcome mapping printed `ok` because nothing
asserted that crossing at all.

**For the generation chain a judged assertion is the only practical one.** The output is
generated prose, so a deterministic assertion over it is either vacuous or brittle.
`ORC-230` is the measurement rather than the worry: the live suite proved two rounds and
called it quiescence.

### 8.1 Where the gate sits

**In `Checks`, before reconcile.** DESIGN §14 records the intended shape as staging with
a manual test gate "between `Reconciling` and `Merged`". That placement was forced by
there being one environment — nothing ran the branch, so the earliest instance to drive
was post-merge. A per-PR preview removes the constraint, and the difference is what a
failure costs: caught in `Checks` it is a rework, caught after `Merged` it is a revert of
something already deployed. Close or re-scope §14's staging item in the same change.

**Failure routes through the machinery that exists.** A failing gate is CI red: the
failure comment, first on the branch → `Ready for rework`, second → `Blocked` (§12). No
new state and no new flavour.

### 8.2 A manual test is its own file, and it is not `CHANGE.md`

**`CHANGE.md` is overwritten every ticket (§1), so nothing durable can live in it.** The
design pass writes the acceptance criteria into `CHANGE.md` *and* writes or amends the
manual tests they imply, as separate files that persist. The criteria and the test say
the same thing; only one of them survives the next ticket, and it is not the spec.

**So `tests/manual/**` accumulates deliberately, and that is not §1's rejected pile.**
The objection to `specs/<TICKET>.md` was that every entry is a claim about the tree that
nothing checks, so staleness is silent. A manual test is *executed* — the full set runs
at every milestone boundary (rule 6), so a test that has stopped describing the system
goes red. **A pile nothing runs rots silently; a pile something runs cannot.** That
difference is the whole of why one is refused and the other is kept.

The directory is the log. No registry file is needed, and one would be a second thing to
keep true.

### 8.3 The rules

1. **A manual test is a file** — `tests/manual/<id>.md`, added to Catapult's
   `designOwnedPaths`, carrying a rule id with its reason in the `.reasons.md` sibling
   (conventions §12). It states preconditions, steps, expected observations, and **what
   would make this test wrong**.
2. **The design pass writes the criteria once and lands them twice** — into `CHANGE.md`
   as the ticket's own scope, and into the manual test files as the durable form (§8.2).
   Writing the acceptance criteria and the test script as separate acts of authorship is
   the labour this avoids; writing them into one file is impossible.
3. **The evidence is the artifact, not the verdict.** Screenshots, the trace, the HTTP
   transcript, the generated document, kept as run artifacts. **A pass with no evidence
   is a failure** — the same rule as "a probe must assert that it edited something", for
   the same reason: the dangerous outcome is not a judge that fails, it is one that never
   ran and printed `ok`.
4. **The judge never sees the diff.** A separate session holding the test file and the
   running preview, with no access to the implementation. Otherwise this rebuilds
   `awaitingDispatchOf` — the pass that wrote the code writing the assertion that agrees
   with it.
5. **A new manual test is proven by breaking the thing.** Run it against the feature
   commit reverted and record the failure beside the test. A manual test that has never
   failed is unproven, and here that proof is cheap in a way a unit test's is not.
6. **Two tiers.** Per-PR runs only the tests whose seams the diff touches, selected by
   inverting the file map; the **full set runs at the milestone boundary**, beside the
   live suite, and that batch is what keeps §8.2's accumulation honest. The per-PR
   selection is not an optimisation — a judge pass spends the subscription, and §4 makes
   the subscription the ceiling.
7. **Verdicts are GitHub check runs.** Per-commit by construction, history queryable,
   native red and green. That is the ops-free answer to where a verdict lives, and it is
   what makes rule 8 decidable without a store.
8. **A verdict that differs from the one recorded for the same test on the same SHA is a
   finding about the judge**, filed to Triage. It is never a re-run, and **it does not
   count toward §12's two.** Without this, a flapping judge spends a ticket's escalation
   budget and parks work that was never broken.
9. **They replace the brittle integration layer, not the unit suite.** Catapult's fast
   deterministic tests stay exactly as they are.

---

## 9. The check to apply when something is built

**A `DESIGN.md` line count is not the check.** That test would assume the protocol was
mostly going away; §11.1 keeps the state machine in Linear and §11.4 keeps the adapters, so
most of it stays by decision, and a document that did not shrink is the *expected* outcome
rather than a failure signal.

**Nor is "does a ticket reach `Done` faster".** That is the obvious headline and the
baseline refutes it. Measured 2026-09-16, before anything landed
(`docs/baselines/2026-09-16.md`): of 1047.2 h of ticket time, **81.2% is waiting on the
author** — `design_review`, `triage` and `blocked`, all author-held by DESIGN §3's own
table — against 9.6% in the agent and CI states. A ticket's wall-clock is mostly a human's
response time, and nothing in C0–C6 touches it. A cutover that halved every agent state
would move the headline by under five percent, and a fortnight where the author was quick
would swamp it either way.

**So the check is scoped to what the changes reach:**

1. **Time in `designing`, `in_progress`, `reconciling` and `checks`**, per state, against
   the baseline's per-state rows. These are what C1, C2, C3, C5 and C6 move.
2. **Actions minutes**, over a window both captures share — the baseline's run table
   begins 2026-08-26 and the project begins 2026-08-12, so an all-time comparison would
   report a rise that is only the collector having seen more.
3. **Author latency tracked beside those, never inside them.** It is the largest number in
   the system and the one this work does not address; folding it in hides both.

**And that scoping is itself a finding worth keeping**, because it is the same mistake
`agentTotal` makes one level down: on the baseline, 83.8% of that rollup is
`design_review`. A number that sums actors moved by different things cannot answer a
question about one of them.

Take the baseline before the first change lands. It is not reconstructible afterwards.

---

## 10. To be measured

Each of these is a guess until somebody takes it. Recorded as guesses on purpose.

- **The cycle-time and cost baseline (§9).** Take it first and take it before anything
  else changes; every other number here is optional and this one is not recoverable.
- **Catapult's current App Platform instance size.** Deliberately not in the repo
  (`SETUP.md` §2), which is why §7.1's floor is a range. **Probe:** export the app spec
  from the DO dashboard and read the tier.
- **The Render Postgres tier Catapult actually needs**, with EventStore, Oban and
  application data on it. The smallest paid tier is 256 MB and that is very likely
  undersized; it moves both the production line and every preview-hour in §7.1.
- **Render preview provisioning time** for EventStore plus Oban seed and migrate. It is
  the §8 gate's latency on every PR, and §7.1 shows it is also a cost lever.
- **What a judge pass costs against the subscription.** It sets how large §8.3's per-PR
  tier can be. The Actions half of the answer is a query the stats store already serves
  (§11.5); the subscription half needs one real ticket's selection, measured before the
  gate is armed.
- **`merge=ours` in the agent job.** Behaviour under `actions/checkout`, and whether the
  driver survives the merge reconcile performs, is unverified. **Probe:** two branches
  each rewriting `CHANGE.md`, merged in a job, asserting which version survives — and
  asserting the assertion by running it once without the driver configured.
- **Cloudflare preview versions and bindings.** Relevant only while the Worker survives
  (§11.2). A `wrangler versions upload` preview URL is believed to share the production
  script's bindings, which would mean a preview Worker writing into production's
  `ProjectState`. **Probe:** deploy a preview, write a row, read it from production's
  `/state`.

### 10.1 One thing to watch rather than measure

**If Linear ever ships a transactional pre-transition hook, §9's revert machinery becomes
deletable.** Not reduced — deletable. The move record, the writer matrix, `resync` and
`ActAdopt` all exist because a move cannot be refused, only detected and undone (§11.1).
A hook that can reject the write removes the thing they compensate for.

It is a watch item and not a plan, because nobody can schedule another company's
roadmap and because the compensating design works today. Two things make it worth
re-checking rather than forgetting:

- **The capability exists in the category and is monetised, not absent.** Plane ships
  exactly this — sandboxed pre-validation scripts that block a transition when they throw
  — behind its Enterprise tier. Jira's workflow engine has had conditions, validators and
  post-functions for twenty years. The gap is in the *fast* trackers, not in tracking.
- **The reason for the gap is architectural, which is why it may not close.** Linear-class
  trackers are local-first: the client applies a mutation to its own store and syncs
  afterwards, which is what makes them feel instant. A synchronous server-side refusal
  means the UI showed the move and must now roll it back — the one experience that
  architecture exists to avoid. So webhooks, which are after the fact, are the shape the
  sync engine wants to offer. Agent-driven workflows are the pressure that could change
  it, and nothing else obviously is.

**Re-check at a milestone boundary, not on a timer**, and only act if the hook can refuse
a transition *server-side* — a client-side guard in the Linear UI would change nothing,
since the harness and a human both write through the API.

---

## 11. The disposition of what exists

§0's measurement is a complaint until there is somewhere for the 25,092 lines to go.
This is that accounting, and the rows below cover **every line of `internal/`** — the
six groups sum to 18,454, which is the measured total, so nothing is quietly unaccounted
for.

**Status is stated per row on purpose.** Most are now settled; two remain proposals that
nobody has ruled on, and one is in tension with a section above. **A row marked
*proposed* is not a decision and must not be implemented on this document's authority** —
recording a proposal as settled is how a record ends up describing a system nobody agreed
to.

**Read the decided rows together before reading any one of them.** Four of them came back
*keep*, and the pattern in why is the finding: each proposed replacement had confused two
consumers of one thing — a tracker's state with a tracker's queue, a program's reads with
an agent's reads, an instrument with its dashboard. The composition survives where a
component has one consumer, and fails where it has two.

| what | lines | disposition | status |
| --- | --- | --- | --- |
| `core`, `plane`, `state` | 4,813 | **keep** — Linear stays the state machine (§11.1) | decided |
| `agent` | 3,552 | → a Claude Code skill plus hooks | proposed |
| `host`, `tracker` | 3,499 | **keep** — MCP is additive, not a replacement (§11.4) | decided |
| `worker` (TS) | 2,114 | → native events plus one scheduled workflow | **in tension with §6** |
| `stats`, `statsstore` | 1,005 | **keep** — it is the instrument, not a dashboard (§11.5) | decided |
| `citations`, `filemap`, `reasons`, `nonasks`, `decisions`, `promptdoc` | 2,419 | **keep**, as `mix catapult.audit` checks in Catapult | proposed |
| `sim`, `scenario` | 1,424 | **keep** — there is still a protocol to simulate (§11.6) | decided |
| `config`, `marker`, `protocol`, `setup`, `deploy`, `retro` | 1,742 | shrink with whatever above them survives | no independent disposition |

### 11.1 Linear is the tracker, so Linear is the state machine

**Decision.** Ticket state stays in Linear. Moving it to PR state and labels while Linear
remains the tracker would give the pipeline two sources of truth for where a ticket is,
and a board that disagrees with the branch is worse than either alone. This is DESIGN
§2.1's rule applied to its own consequence: the worklist is a tracker, and a tracker that
does not hold state is not one.

**Three records, no overlap, and it is worth stating so nobody re-derives it:** the Linear
description is the immutable argument (§2.3), `CHANGE.md` is the sketch (§1), and the
Linear state is where the ticket is. Each answers a different question.

**So `internal/state` survives, and for its original reason.** The move record exists
because the tracker cannot say who made a write and the harness authenticates as the
author (§13). Nothing about that changed — it would only have gone away if state had
moved to git, and state is not moving.

§4 carries the same fact, because that is where a reader meets the credential argument and
could otherwise read it as deciding this too. It does not: who runs the model and what the
tracker holds are separate questions.

**State the decision in three clauses, not one.** "Linear is the state machine" is right
against the alternative it rejects and misleading on its own:

> **Linear holds the state; the plane holds the authority; §9 is the reconciliation
> between them.**

The flat version reads as *Linear decides*, and a pass that believes that will find the
§9 revert rules redundant and delete them. They are the opposite of redundant — they are
what lets a single authority survive a UI that anyone can drag a card in. Linear's API
offers no way to refuse a transition, so the pipeline cannot prevent a move it disagrees
with; it can only detect one and undo it, which is what the move record and the writer
matrix are for.

**That is the standard compensation for a tracker with no transactional hook, and it is
worth naming as such.** Written down, §9 looks like a pile of special cases; recognised
as detect-and-compensate, it is one mechanism, and the next pass to meet it has a name
for what it is doing. §10 carries the condition under which it could go away.

### 11.2 The Worker row contradicts §6

§6 provisions a per-PR rehearsal tenant whose second component is a Worker deployed under
its own name. This row deletes the Worker. Both cannot be built.

Neither is wrong on its own: §6 describes a tenant for the pipeline **as it is**, and this
row describes the pipeline **as proposed**. Named here because the failure mode is
writing one against a world the other deleted, and discovering it when a tenant loses a
component and gains nothing. Settle them together.

### 11.3 The gates are the strongest row, and they are in the wrong repo

`citations`, `filemap`, `reasons`, `nonasks` and `decisions` check a project's tree
against a project's conventions. They already *run* in the project's CI — Catapult's
`ci.yml` gates the pipeline audit on ticket branches — so moving them is a port, not a
relocation of where they execute.

What the port buys is the end of the cross-repo schema deadlock this repo's CLAUDE.md
records twice: a config field must merge here first because `DisallowUnknownFields`
rejects a key the binary has not declared, and `citationShorthands` landing on Catapult's
side first took that project's **whole audit** down with `json: unknown field`. As mix
tasks they run against the tree they check, in the repo that owns the rules, with no
shared schema for the two sides to deadlock on.

**Consequence to carry into §8.2:** the per-PR manual-test tier selects tests by
inverting the file map. If the map becomes Catapult's, the selection reads Catapult's map
— which is where it should have come from anyway. Say so where that rule lives, not only
here.

### 11.4 MCP and the adapters are for different consumers, so we keep both

**Decision.** `host` and `tracker` stay. MCP servers are added alongside them, for the
agent sessions, and replace nothing.

**The row that proposed swapping them conflated two callers of one service.**

- **The control plane is a program.** `Sweep` is a pure function from a snapshot to a
  list of actions, and the adapters are how that snapshot is built and those actions are
  applied. Its decisions have to be reproducible, which is what the three test rings, the
  in-memory fakes and `internal/sim` exist to exploit. Reading the tracker through a
  model would make every sweep nondeterministic, and it would take the fakes and the
  simulator with it — the same input would stop producing the same output, which is the
  one property the whole design rests on.
- **An agent session is not a program.** A design or dev pass exploring a ticket's
  history, a PR's comments or a run's logs is already nondeterministic, and an MCP server
  is the right shape for it: dynamic, unplanned queries where no fixed adapter surface
  would anticipate what gets asked.

So the question is not which to have. **Deterministic adapters for the decisions the
pipeline makes; MCP for the questions an agent asks.** Naming the two consumers is what
stops a later pass reading "we have MCP now" as a reason to retire either one.

**And the cost that would have been paid quietly:** the tracker adapter carries the
heaviest test coverage in the repo, deliberately (PLAN §1 — Linear has no Go SDK, so it
is hand-written GraphQL and is tested accordingly). Replacing it would have moved that
surface out of all three rings at once, and §9's check would not have noticed, because
§9 does not measure adapter coverage.

### 11.5 Stats is the baseline, and this is the worst possible moment to lose it

**Keep the collector and the store.** What they hold is not a dashboard's backing data:

- **`interval`** — how long each ticket spent in each state, sliceable by milestone, by
  label, and by whether the boundary was open.
- **`run`** plus `BillableMillis` — Actions minutes under the host's own per-minute
  rounding model, which is the rounding every cost argument in `DESIGN.md` already leans
  on (the kill switch's 730 idle minutes a month, the seven sweeps in 107 seconds).
- A watermarked backfill built to survive the hourly rate limit over Catapult's 5,634
  runs, which is the part that would be expensive to rebuild and tedious to get right
  twice.

**That is cycle time and cost — the outcome §9 only proxies for.** §9 asks whether
`DESIGN.md` shrank, which measures complexity and infers the rest. This measures the
thing itself: whether a ticket reaches `Done` faster and for fewer minutes. Deleting the
instrument immediately before the largest change this pipeline has had would destroy the
before-measurement that the change is supposed to be judged against, and the comparison
is not reconstructible afterwards — the history it reads is in a tracker whose role
§11.1 may be about to change.

It is also the half of §10 that can be measured without a new probe. "What a judge pass
costs" is asked there against the subscription; the Actions half of that answer is a
query this store already serves.

**Two things follow, and neither is deletion:**

1. **The dashboard is a separate question from the data.** The rendering at `/dashboard`
   lives in the Worker and its fate is §11.2's to settle, not this row's. Keeping the
   instrument does not commit us to keeping that surface, and a query against the store
   is a fine answer for one reader.
2. **The collector's adapter follows wherever state goes.** It reads transition history
   from the tracker today. If §11.1 moves the state machine, the collector reads the new
   source and its aggregate is unchanged — the intervals are the same intervals. Worth
   saying because it makes stats a *consumer* of §11.1 rather than a blocker on it.

### 11.6 What the accounting does not claim

**No total for what a composed design would delete.** Summing the *proposed* rows would
produce exactly the invented figure §0 declines to give and §9 exists to replace: the
number is read off a real diff or it is not known.

**Three costs had no row, and the decided rows removed them.** Recorded because the
disappearance is evidence, not because the costs are live:

- **`internal/sim` was going to be the largest loss.** A composed pipeline cannot be
  simulated, and proving protocol behaviour deterministically in milliseconds is this
  repo's best testing asset. It survives intact: the state machine stays in Linear
  (§11.1) and the adapters stay behind their fakes (§11.4), so there is still a snapshot
  to simulate and still a protocol to prove.
- **DESIGN §9's expressiveness was going to narrow.** Branch protection can enforce "this
  token cannot push here"; it cannot state "a design pass promoting past `Design review`
  is a violation". Since state stays in Linear, the revert rules and the move record stay
  with it, and the vocabulary is unchanged.
- **Determinism and ownership.** What remains outsourced is the run harness (still
  proposed) and the platforms under §7 — services that can be down, none of them ours.
  That is a real and bounded exposure rather than the systemic one a full composition
  carried.

**That all three evaporated together is the point.** They were three symptoms of one
proposal — replacing the plane's own reads and writes — and they went when it did.

# Ops-free pipeline — decisions

**What this is.** Decisions taken in a design conversation about changing how this
pipeline is built and verified. Nothing here is built. `DESIGN.md` stays the protocol
and the single source of truth for what the pipeline *does*; each decision below amends
it in the commit that implements it, and this file loses the entry at the same time.
`PLAN.md` owns build order and test rings; this file owns neither.

**Style requirement, inherited:** every rule states its rationale inline, and records
the failure it exists to prevent. A rule without its reason is a rule the next session
violates reasonably.

**What it does not cover.** Catapult is out of scope as a feature target. Orchestration
is made to write code well first; Catapult's own delivery needs (per-PR environments,
AI-driven manual tests) are deferred with their reasons in §8 and §9.

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

**The number is a motivation, not a target.** Nobody has measured how much of the
25,092 a composed pipeline would actually delete, and an estimate here would be exactly
the invented threshold this repo's CLAUDE.md warns about. What §10 gives instead is a
falsifiable check to apply once something is built.

---

## 1. `CHANGE.md` — one spec file, overwritten per ticket

**Decision.** A single `CHANGE.md` at the repository root. Every ticket's design pass
overwrites it **wholly** with that ticket's spec. It is never deleted and never
accumulates: one file, one revision per merged change.

**What it holds: the sketch.** DESIGN §4's sketch — the structural output of a design
pass — has no file today. It is a diff against `systems/*.md` plus a touch list carried
in a marker comment on the ticket. Giving it a file is what makes everything below
possible; there is otherwise nothing whose history could be read.

It does not duplicate the Linear description. The description is the immutable argument
(§2.3) and stays canonical for *why*. `CHANGE.md` is *what will be built and where*.

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

### 1.3 The file names its ticket, and the harness asserts it

Line one is `# <TICKET-KEY> — <title>`. `pipeline agent claim` and
`pipeline agent finish` both assert it matches the ticket being worked.

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

## 4. The runner is ours, and Linear is a board

**Decision.** Everything that invokes the model runs in an environment we control —
GitHub Actions today. No third-party agent runner, including Linear's.

**The reason is the credential.** Agents run on a Claude Code subscription OAuth token,
not an API key, and that is the difference between this project costing $200 a month and
costing thousands. A subscription token only works inside Claude Code in an environment
we control, so any service that would drive an agent for us would have to hand the work
back to Actions regardless — and two dispatchers quietly defeats every single-agent
guarantee downstream (§13).

**So Linear keeps intake, priority, attention and webhooks, and nothing else.**

**The concurrency ceiling is the subscription, not the infrastructure.** This is the
load-bearing consequence and it decides §6 and §7: building isolation to run agents the
plan cannot feed buys nothing.

---

## 5. Orchestration has no environment to provision

**Decision.** No per-PR environment for this repository. There is nothing to host.

The deployables are a static Go binary, built inside the job that runs it, and one
Cloudflare Worker. There is no database, no container and no App Platform app — the
DigitalOcean app belongs to Catapult, and this pipeline only reads its deployments API
to confirm a SHA landed (`SETUP.md` provisions no app for this repo).

**Orchestration's own PRs get no AI-driven manual tests either.** The gates plus the
rehearsal are the verification, and the rehearsal already exercises the real agents
against a real project end to end. Adding judged assertions to a repo whose output is a
state machine would buy nondeterminism for coverage the fixtures already have.

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

## 7. Per-PR environments, for when Catapult needs them

Deferred with Catapult, recorded so the decision is not re-derived.

- **Render.** Preview environments provision the whole blueprint, including a database,
  per PR.
- **Fly.io is out**, on the author's own experience of its downtime. Not a
  price-or-fit judgment and not open to re-argument on those grounds.
- **A branching Postgres (Neon and similar) probably drops out** if Render's blueprint
  supplies a per-preview database. Unmeasured — see §10.
- **Kubernetes is not the answer for CI environments.** The expensive part of a per-PR
  environment is the data — Postgres, EventStore and Oban provisioned, migrated and
  seeded — and a cluster supplies pods cheaply while doing nothing about that. The case
  for Kubernetes arrives with the hosted tier running customers' generated projects,
  which is a product decision about Catapult and not a CI decision about our own PRs.
  Conflating the two is how the cluster arrives two years early.

---

## 8. AI-driven manual tests, deferred

Deferred deliberately, not dropped. Two reasons, both about sequencing:

1. Orchestration should be reliably writing code before a nondeterministic gate is added
   to the thing that judges whether it did.
2. The target that needs them is Catapult, and Catapult needs a running instance per PR
   to drive — which is §7, which is deferred.

The defect profile that motivates them is recorded so the argument survives: the
tickets this pipeline has been fixing on Catapult are seam defects — a chain handing
every agent an empty context, atoms reaching a projector as strings, `declared_in` paths
spelling with underscores what the schema spells with hyphens. Both sides correct, the
crossing unasserted. A green unit suite cannot see any of them, and this repo's own
CLAUDE.md records the same shape from the other end: breaking the host-to-core outcome
mapping printed `ok` because nothing asserted that crossing at all.

---

## 9. The check to apply when something is built

**If a redesigned `DESIGN.md` does not come in materially shorter than today's 2,956
lines, the composition failed.** Rules that survive at their current count — merely
spread across workflow YAML, hooks and skill frontmatter — are the same protocol in a
substrate that cannot be simulated, which trades away `internal/sim` and buys nothing.

No target line count is set here, because one would be invented. The test is
directional and applied to a real diff, not asserted in advance.

---

## 10. To be measured

Each of these is a guess until somebody takes it. Recorded as guesses on purpose.

- **Cloudflare preview versions and bindings.** A `wrangler versions upload` preview URL
  is believed to share the production script's bindings, which would mean a preview
  Worker writing into production's `ProjectState` and `SweepDebounce` — silently, and
  in the class of defect that took the §9 invariants off for a milestone. Deploying
  under a distinct `--name` is believed to sidestep it because Durable Object namespaces
  are per script and class. **Probe:** deploy a preview, write a row, read it from
  production's `/state`.
- **`merge=ours` in the agent job.** Behaviour under `actions/checkout`, and whether the
  driver survives the merge reconcile performs, is unverified. **Probe:** two branches
  each rewriting `CHANGE.md`, merged in a job, asserting which version survives — and
  asserting the assertion by running it once without the driver configured.
- **Render preview provisioning time** for Catapult's EventStore plus Oban seed and
  migrate. This is the number that decides whether previews are pleasant or a wait, and
  it decides §7's Neon question with it.

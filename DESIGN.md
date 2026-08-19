# Automated design→dev pipeline — design specification

**What this is.** The spec for an automated ticket pipeline spanning a design agent, a dev
agent, and a reconciliation agent, coordinated entirely through Linear state and executed on
GitHub Actions. It is the single source of truth for the workflow: agent instruction files in
individual projects reference it rather than restating it.

**Style requirement:** every rule states its rationale inline. The agent roles are
re-instantiated per session with no memory, so a rule without its reason is a rule the next
session will violate reasonably. Preserve this when editing.

**Human touchpoints, by design, are three:** design sign-off, the milestone boundary, and
anything reaching `Blocked` or `Boundary review`. Everything else runs unattended.

---

## 0. Context

**The problem.** A design conversation and an implementation session want different context
and iterate at different speeds, so they are separate agents. Everything hard about that
follows from the handover: what passes between them, who is allowed to write what, and how
two pieces of in-flight work avoid overwriting each other.

**Assumed stack.** Phoenix / LiveView with daisyUI; `phoenix_storybook` for component
variations; Linear as the tracker; GitHub for code and automation; Cloudflare for the
scheduler and branch previews (§13, §4); DigitalOcean App Platform with auto-deploy on push
to main. One environment — main is production.

**Assumed scale.** One human author, one project at a time per pipeline instance, one dev
agent. Several assumptions here are load-bearing and are called out where they appear.

**Vocabulary used throughout:**

| Term | Meaning |
|---|---|
| **The argument** | The prose in an issue explaining what was wrong and why the change is worth making. Not the markup. It is the part an implementation session cannot reconstruct, and the thing reconciliation measures against. |
| **Standing decision** | A rule recorded in a screen's narrative doc that outlives any one ticket. The thing nobody re-derives later, and so the thing worth catching when a change contradicts it. |
| **Hand-back** | The comment a dev agent writes when it finishes: what landed, the commit, anything deliberately not done and why, and any open question it resolved. An issue that moves without one is a state change nobody can audit. |
| **Triage** | Linear's intake state. Where an agent's findings go so the author can accept or decline them in one keystroke, rather than the finding evaporating in a chat log. |
| **Gating debt** | Tech debt whose absence makes the *next* product milestone materially harder. Scheduled ahead of that milestone rather than remembered. |
| **The sketch** | A design pass's structural output (§4): the diff against `systems/*.md` on the ticket branch, plus the declared touch list that becomes mutex labels. The part of architecture a human reviews before implementation exists. |
| **In flight** | Any state from `Ready for dev` through `Merged` inclusive. |

---

## 1. Actors and stores

| Actor | Owns |
|---|---|
| **Author** (human) | Scope. Design sign-off. Milestone boundaries. Issue creation. |
| **Design agent** | Designing, claimed from Ready for design. Produces stories, components, narrative docs. |
| **Dev agent** | All pipeline repository work that isn't design-owned. Singular. Out-of-band fixes are the author's (§5). |
| **Reconcile agent** | Pre-merge verification against intent. Merges. |
| **Boundary agent** | Archive, debt scan and grooming at a milestone boundary. |

| Store | Canonical for |
|---|---|
| **Linear** | Intent, scope, state, decisions, the argument |
| **The project repo** | Everything that ships, including all design source |
| **The pipeline repo** | This protocol, agent prompts, the composite actions the project stubs call |
| **Static preview** | Nothing. A rendering of a branch. |

**Artifacts never pass through an external store.** Design writes to a branch. A file-sharing
service as transport was the obvious alternative and costs more than it looks: transfer
integrity checks, ticket-scoped filenames to keep revisions apart, "most recent upload wins"
as a versioning scheme, and a file id on the issue that orphans the artifact if mistyped. Git
supplies ordering, diffs, provenance and review for free.

**The protocol has one home: the pipeline repo,** versioned alongside the agent prompts. Each
project appends only its own conventions. Two copies of a shared vocabulary drift silently,
and the drift shows up as agents disagreeing about what a state means.

---

## 2. Load-bearing principles

1. **The worklist is a tracker, never a document.** Prioritising, moving and closing are
   operations a markdown list can't do, and a list in prose goes stale with no signal. The
   repo keeps the *argument* for why the milestones exist in that order; the tracker keeps the
   roster of what exists.
2. **The queue is a state, scoped by a project.** Both filters are required on pickup: the
   state is the queue, the project is the scope. An issue outside the project is not the dev
   agent's to act on, whatever its state.
3. **Descriptions are immutable; comments carry deltas.** The description is the original
   argument and the measuring stick reconciliation verifies against, so nobody edits it after
   the fact. On a returned ticket the **newest comment is the scope** — re-implementing the
   description re-lands work that already merged and conflicts with itself.
4. **Base-rev is optimistic concurrency control, checked at pickup.** Not provenance. Git
   surfaces conflict at merge, which is after the implementation exists, and only textual
   conflict at that. The resolution rule is semantic and has no git equivalent: the repo moved
   and both changes touch the same behavior → **repo wins, and the ticket stops** with a
   comment naming what moved. It stops as a push-back (§2.7), so it parks in `Blocked` and the
   author sends it back to `Ready for design` — the design has to be re-decided either way, and an
   agent routing it there itself is the loop §2.7 describes. Never reconcile by guessing; that
   produces a design nobody agreed to.
5. **Undesigned changes remain legal.** Small fixes and vulnerability patches land on main
   without a ticket, on purpose — the author's, made outside the pipeline (§5). The base check
   is the only thing that warns the next design pass that the ground moved.
6. **Rejected proposals are `Canceled`, never `Done`.** `Done` stays a clean record of what
   shipped.
7. **Push-back has a channel.** A design that can't be built as drawn stops, with the argument
   why — never a silently worse version, never a silent third thing. An agent takes it by
   changing no files and naming the outcome (§12); it has no credential with which to move a
   ticket itself. It lands in `Blocked` under a `pushback` label, and the author decides
   whether it is redesigned or rescoped.

   **It parks rather than returning to the design queue, and that is a correction.** Routing it
   straight back is the obvious shape and it has a cycle in it: a design pass runs on every
   entry to `Ready for design`, so a decisionless pass that finds nothing to decide and a push-back
   that finds nothing to build can hand one ticket between them indefinitely, each pass correct
   on its own terms and neither able to see the loop. Counting the bounces was the alternative
   — a third counter beside the CI and reconcile ones (§12) — and it is more machinery to stop
   something a human should be told about the first time. The author is the only participant
   who can break the cycle, so they are the one it stops at.
8. **A new component, token, context, table or dependency is a decision, not a port.** It
   must be named — components and tokens in the issue, structure in the sketch (§4) — because
   a decision nobody named is a decision nobody reviewed. Enforced in CI (§9), not by
   convention.
9. **Milestones alternate debt and product.** A tech-debt milestone precedes each product
   milestone, so debt that gates a milestone is scheduled rather than remembered. A thin debt
   milestone is fine; an honest empty beats a padded one.
10. **State admission test:** each state answers *who has the ball* differently.
    **Label admission test:** anything you'd remove when work changes hands is a state wearing
    a label. `needs-design` is `Ready for design`; `deferred` is `Backlog`; `blocked` is `Blocked`.

---

## 3. The state machine

| State | Who has it | Written by |
|---|---|---|
| `Backlog` | Nobody. Not committed to. | author, grooming pass |
| `Todo` | Nobody. Committed, not started. | author, milestone pull |
| `Ready for design` | The queue. Scope is the **description**. | author |
| `Designing` | Design agent, now. | design (claim) |
| `Design review` | **Author.** Artifacts ready, not yet approved. | design |
| `Ready for dev` | The queue. Scope is the **description**. | author (sign-off) |
| `In progress` | Dev agent, now. | dev (claim) |
| `Checks` | CI. Draft flag off, gates running. | dev |
| `Reconciling` | Reconcile agent, now. Merges on pass. | CI (on green) |
| `Ready for rework` | The queue. Scope is the **newest comment**. | reconcile, control plane (CI red, §12) |
| `Reworking` | Dev agent, now. | dev (claim) |
| `Merged` | The deploy. Post-deploy verification pending. | reconcile (on merge) |
| `Boundary review` | Author, reviewing the boundary agent's proposals. Used only by the boundary ticket (§10). | boundary agent |
| `Blocked` | Author, now. Either something failed or a judgment is needed. | any agent, control plane (escalation and stale claims, §12) |
| `Done` / `Canceled` | Nobody. | post-deploy check / author |

`Blocked` and `Boundary review` both hand the ball to the author. `Blocked` is any ticket
needing attention, at any point; `Boundary review` is only ever the boundary ticket, waiting on
triage.

**Assignment mirrors the ball.** The tracker's assignee is derived, not chosen: the control
plane assigns the author exactly when a state hands them the ball — `Design review`, `Blocked`,
`Boundary review`, and the boundary ticket's `Todo` (§10) — and unassigns everywhere else. The
state table already answers *who has the ball*; without this, the answer is readable only by
someone who has memorised the table, and the author's own "assigned to me" view — the one place
they actually look — says nothing. Agent-held states go unassigned rather than to an agent
identity, because at one shared credential an agent assignee would be indistinguishable from
the author's.

The cost, stated plainly: **assignment stops being a field a human can use.** Self-assigning a
`Todo` ticket as a personal reminder is undone on the next sweep. That is the price of making
the field mean one thing reliably, and the reminder belongs in priority or a comment. It is deliberately not named `Needs review`: the `needs-review` *label* (§8) means
something unrelated — reconciliation couldn't tell — and a state and a label one hyphen apart
is a confusion every agent prompt would have to fight.

**Why `Design review` exists.** Without it there is no signal for *design is finished and
waiting on the author* as distinct from *design is still working*. Sign-off is the transition
`Design review` → `Ready for dev`, and it is the author's, always. **Declining is
`Design review` → `Ready for design` with a comment**, for the same reason the dev agent's push-back
carries its argument: a rejection without one is a rejection the next pass repeats. The author
may route straight to `Ready for design` because they are the one who would notice a ticket going
round; an agent's push-back parks instead (§2.7).

**The decisionless exception:** a design pass that ends with no screen labels, no artifacts,
and no diff to any `systems/*.md` — no new system, table, dependency, component or token —
advances straight to `Ready for dev` (§6), recording its reasoning and touch list in the
marker comment. Sign-off exists to approve decisions; with none to approve it is a rubber
stamp, and rubber stamps train the author to skim the reviews that matter.

**Design gets a queue state for the same reason dev has one, and did not have one for far too
long.** `Designing` used to mean both "queued for design" and "a design agent is working on
this", so no writer rule could be true of it — the author moved tickets in to queue them and the
agent worked in it — and a run that died before claiming left a ticket asserting an agent was on
it. Catapult's `ORC-7` sat that way for 23 minutes. Now the author moves `Todo` → `Ready for
design`, and the design claim writes `Designing`, which makes it the design agent's claim
exactly as `In progress` is the dev agent's (§6, §9). A dead run in `Designing` is now
unambiguously a stale claim, because nothing else can put a ticket there.

Everything that meant "queue this for design" moves with it: the author's send-back from `Design
review`, a re-evaluate flag on a `Design review` ticket, and a design pass's own `demote`
outcome all target `Ready for design`. Demoting into `Designing` would park a ticket in a state
nothing dispatches from — sent back for redesign, and never redesigned.

**Why the queue and the agent get separate states on both sides.** `Ready for dev` /
`In progress` and `Ready for rework` / `Reworking` are the same split for the same reason:
there is one dev agent, so something sent back has to wait somewhere visible. A ticket sitting
in a queue and a ticket being worked are different answers to *who has the ball*.

**Why rework is not just `Ready for dev`.** They differ in where the scope lives. A ticket in
rework is one where reading the description rebuilds the wrong thing — a rule the dev session
would otherwise have to remember is instead a state it can read.

**Nothing waits on a human to merge.** Merge fires from `Reconciling` on a pass, so there is
no state meaning "a PR is open, awaiting a person."

**Invariant: no ticket moves forward carrying a `re-evaluate` label** (§7).

**A ticket in an agent's own state that no agent ever claimed is queued rather than left there.**
Agent states are written by a claim, so a hand-move into one is a §9 violation and is reverted
to where it came from — but only when the pipeline has a record of the ticket to judge the
arrival against, and *no record means not judged* is deliberate (§9). A ticket created and
dragged straight into `Designing` before the pipeline had ever written to it was therefore
judged by nothing, dispatched by nothing — no agent state is a dispatch source — and timed out
by nothing, since the stale-claim rule needs a run to have died. It sat.

So a ticket in `Designing`, `In progress` or `Reworking` with **no run at all and no record**
moves to the queue that feeds that agent. Both conditions are what keep the rule from
overlapping the ones that already work: a record means the revert owns it and sending it back to
its origin is the more precise answer, and a run means an agent is either working or dead and
neither is this. `Checks` and `Reconciling` are excluded — neither is claimed from a queue, so
there is nowhere to return a ticket to that a PR would back.

**A state the config does not name is read by its category when the category settles it.** The
tracker has states the protocol never mapped — Linear ships built-in `Duplicate` and `Canceled`
alongside whatever `pipeline setup` created — and they are two taps away in the UI. A resolved
category answers the only question the pipeline has about such a ticket: it is finished and not
in the queue. `canceled` reads as `Canceled`, `completed` as `Done`, `triage` is skipped (§10).

Anything else still fails the snapshot loudly, and that half of the rule is not softening:
guessing at an unmapped `started` or `unstarted` state would put a ticket in the queue nobody
put there. **What was wrong was the blast radius, not the strictness.** One issue in an
unreadable state failed the *whole project's* snapshot — marking `ORC-47` as `Duplicate` on
Catapult took every sweep down for about two hours and thirty runs, on a tracker action that
looks like housekeeping.

---

## 4. Artifacts and issues

### What design produces

Per screen, in the project repo:

- **A stateless function component** — presentational, hardcoded assigns, daisyUI classes.
- **A `.story.exs`** with one variation per state, each carrying its description.
- **A narrative doc** (`screens/<name>.md`) for rules, standing decisions and the argument,
  with **no state sections at all**, and a front-matter **file map** naming the screen's
  component module and story file — the map is what lets CI audit per screen rather than per
  "some design path" (§9).

The state list lives in exactly one place. Splitting it across a prose spec and a set of
rendered states creates a gap nothing can test, and that gap is where undesigned work hides —
a screen can look complete because its doc and its stories agree, while a third document has
twenty states drawn that neither mentions. Here nothing overlaps, so nothing can drift, and no
pinning test is needed to hold two files in agreement.

**A state name is a storybook variation name.** Name it as one (`cap_reached`, not "the
cap-reached state"), because renaming it after implementation is two files and a test.

**Design iterates in a scratch HTML artifact** (Tailwind + daisyUI via CDN) for speed, then
transcribes to `.heex` once signed off. The class strings are identical, so transcription is
mechanical. The HTML is never committed.

**Review is a static storybook export.** CI boots the app, crawls the story routes, and
publishes HTML plus assets per branch. This works only because design artifacts are stateless
— no socket required. The interactive playground is lost; the visual review is not.

### Architecture in the repo, and the sketch

Architecture lives in `systems/<name>.md` — one doc per system, the structural mirror of
`screens/<name>.md`: standing decisions (which system owns a concept, why a boundary sits
where it does), their rationale, and a front-matter **file map** declaring the paths the
system owns. One doc per system for the same reason as one doc per screen: a monolithic
architecture document goes stale as a whole, and nobody can tell which ticket last verified
which paragraph. No inventory of what the code contains — the code is that inventory.

A design pass's structural output — **the sketch** — is a *diff against these docs*, committed
on the ticket branch like every other design artifact. A new system is a new doc; a moved
boundary is a changed doc; a new table or dependency is named in the owning doc's diff. §1
already made this argument: git supplies ordering, diffs, provenance and review for free, and
a sketch living in a ticket comment would be the one design artifact outside it. Two reasons
the sketch exists at all. First, structure was the least-reviewed, highest-blast-radius
decision class in the pipeline: reconciliation checks the diff against the argument, never the
architecture, so a wrong schema previously reached production with no human eyes on it — and a
schema is expensive precisely when it is wrong. Second, the declared touch list is what makes
system labels trustworthy (§6); a mutex fed by guesses is not a mutex.

**Touching is not deciding.** A ticket that works inside a system without changing its
structure declares the touch — `system:<name>` labels from the pass's outcome — and diffs no
doc. The decisionless exception (§3) is exactly this shape with no screens either: labels
declared, docs untouched, no artifacts, reasoning in the marker comment.

The dev agent implements against the doc diff as part of scope, and may amend file maps when
implementation discovers something — the same rule as amending any design-owned file (§5).
Deviating from the sketched structure is legal exactly as renaming a state is (§9): fine if
the hand-back argues why, a finding if nothing does. A sketch that cannot survive contact with
the code is a push-back (§2.7), never a silent improvisation.

Sign-off approves the storybook export and the doc diff in one review — deliberately not two
states: consecutive reviews both answer *who has the ball* with "the author," which fails the
state admission test, and a second per-ticket touchpoint is a cost the three-touchpoint budget
doesn't have.

**Adopting an existing repo is a bootstrap pass**, run once by the author, attended, with the
bootstrap prompt (`prompts/bootstrap.md` in the pipeline repo): split the existing
architecture document into `systems/<name>.md` with file maps, stub `screens/<name>.md` for
existing surfaces, and surface what maps to no system. Its output is a PR the author reviews
like any design — the bootstrap proposes, the author decides. A prompt rather than pipeline
machinery because it runs once per project, with a human watching.

**Previews are deleted when their PR closes.** A branch preview exists to be reviewed; once
the PR is merged or abandoned it is clutter that also consumes a metered deployment allowance.
Deletion is keyed to PR close rather than a scheduled sweep, because "the branch this belonged
to is gone" is a fact the host already publishes and a cron would only rediscover late. The
production deployment is never touched — a rule the cleanup enforces explicitly rather than
relying on the branch filter, since deleting production would be the one unrecoverable
mistake in an otherwise janitorial job.

**The preview URL goes on the ticket, from the publisher.** A ticket arriving in `Design
review` is a ticket asking to be looked at, so it says where — the design pass posts a `preview`
marker carrying the URL wrangler reported, in the same breath as the transition. Reported rather
than derived: Cloudflare's branch-alias slugging is its own rule, truncation and hashing
included, so building the URL from a branch name and a project name would be guessing at
someone else's algorithm and handing the author the guess as a link. A project with no preview
wired posts nothing, which is silence rather than a dead link.

**The publishing target is Cloudflare Pages.** Every branch gets a stable preview URL with no
machinery of ours — per-branch previews are the platform's own feature, and glue code we don't
write is glue code that can't silently break. The alternatives all cost more than they look:
GitHub Pages is one site per repo, so per-branch previews mean serialization and cleanup code
we then own; Actions artifacts aren't browsable, which kills the one-click review; a bucket
means constructing URLs and cleaning up by hand. Preview URLs are unauthenticated — at one
author, obscure is acceptable, and Cloudflare Access is the upgrade path when it isn't.

### What an issue contains

Every issue lives in the configured team and project; one outside it is invisible to the
queue. Title is what changes, in the imperative. Description, in this order:

1. **What changes and why** — the argument. What was wrong with the current screen; what a
   reader or author couldn't do. This is what decides whether the change is worth making and
   the only place it is recorded.
2. **`Base: <sha>`** — the merge-base the design was drawn against, per §2.4.
3. **What it touches** — screens, components, and anything new proposed for the design system,
   stated as a decision in as many words.
4. **What you're unsure about.** A design handed over with its open questions removed is one
   the dev agent will resolve by guessing.

**Issues are created only when the author asks,** the milestone boundary ticket excepted (§10). An agent that reads the whole product in an
afternoon is very good at spotting gaps and very bad at judging whether a gap is news. Findings
go to Triage; an unasked-for ticket costs a read and a decision nobody wanted, and a backlog
nobody trusts is worse than one with a hole in it.

**A confirmed non-asks document** records what the design deliberately doesn't want, each with
its reason. It is a file in the repo (`non-asks.md` by default) beside the screen and system
docs it constrains — the rest of the design lives in the repo, and a record of refused
decisions kept anywhere else is indirection with no reviewer and nobody maintaining it. The
design agent maintains it like any other design artifact: entries are added and amended in the
same commit as the artifacts, reviewed in the same Design review sign-off, and never deleted,
because a refusal that quietly disappears is one the pipeline proposes again next quarter.

The harness inlines it into the prompt of every pass that proposes — design and boundary — for
the same reason it inlines the scope: a prompt whose most important input is "go read this
file" is a prompt whose most important input is optional. The section says which of three
things happened: here it is, the repo records none, or the read failed. The last two look
identical in an empty section and license very different confidence. Proposing a recorded
non-ask isn't forbidden, but do it knowing you are arguing against a recorded decision, and
say so in the issue.

---

## 5. Repository ownership

### Within the project repo

| Path | Owner |
|---|---|
| `storybook/**` | Design |
| Presentational component modules | Design |
| `screens/*.md` | Design |
| `systems/*.md` | Design (the sketch writes them; dev amends with a note, §4) |
| Theme tokens | Design (by proposal; see §9) |
| LiveViews, contexts, schemas, tests, everything else | Dev |
| `.github/workflows/**` | **Author only** — no agent can land a change there |
| `pipeline.config.json` | **Author only** — it declares what the run is judged by |

**The last two rows are not a policy, they are a fact, and they are the reason `author-only`
exists (§8).** An agent's push token carries no `workflow` scope on any GitHub repository, so a
commit touching a workflow file is rejected by GitHub — and the rejected push takes the whole
run down with it, hand-back included, which is the worst way to learn it. The config declares
the gates, states and ownership the run is being scored against, so an agent editing it
mid-ticket is an agent changing its own marking scheme.

**A ticket whose work lives in either is labelled `author-only` and never dispatched.** Two
things apply the label. A boundary proposal naming one of these paths as its `subject` is
labelled when it is filed, which is mechanical and needs no judgment. A dev run that discovers
it mid-work says so through its outcome file (§12) and parks — it must not commit the change to
find out, because finding out costs the run.

Neither route is complete on its own and neither is meant to be: a subject is free text and may
name the work some other way, and a dev run only reaches the question if the ticket got that
far. Between them they cover the cases anyone has hit. The author applies the label directly
whenever they already know.

**One PR per ticket, and it stays open through rework.** Design opens it as a draft; dev pushes
to the same branch; reconciliation reviews it; a bounce is more commits on the same branch
rather than a second PR against an already-merged change. Dev may amend design-owned files when
implementation discovers something, which keeps storybook current instead of letting it lag.

**The branch name carries the issue key** (Linear's suggested branch name format works as-is),
and the PR URL is recorded on the issue when the draft opens. Linear's auto-linking makes this
feel free, but it is a requirement rather than a habit: every trigger that maps a PR event to a
ticket (§13) resolves through this link, and a branch named without the key is a PR the control
plane cannot route.

**One dev agent within the pipeline, no exceptions.** Every pipeline repository change that
isn't design-owned goes through it — `In progress` and `Reworking`. That is what lets
`In progress` mean "the dev agent, now" without an owner dimension on states. At most one
ticket exists across `In progress` and `Reworking` at any moment.

**Out-of-band changes are the author's, not the dev agent's.** The small fixes and security
patches of §2.5 land directly on main under the author's own access, carry no ticket, and owe
the pipeline nothing — the base check is what tells the next design pass the ground moved.
Within the pipeline, only reconciliation writes to main (§9, §11): main takes PRs only, merged
by the reconcile agent's identity, enforced by branch protection rather than convention. The
author's direct pushes go through deliberately as an admin bypass — the pipeline neither grants
nor polices them.

### The pipeline repo

A **library, not a runner.** It holds this protocol, the agent base prompts, and reusable
workflows; the workflows *execute* in each project repo via
`uses: <org>/pipeline/.github/workflows/<name>.yml@v1`.

Executing in the project repo gives the agent a native checkout, a correctly-scoped
`GITHUB_TOKEN`, PRs that open where they belong, and logs beside the work. Running centrally
would need a GitHub App with write access to every project and checkouts of foreign repos —
worse, for nothing.

Consequences:

- **`on:` can't be inherited,** so each project carries a ten-line stub workflow. This is where
  per-project bindings live, which is a feature rather than a tax.
- **Projects pin a version** (`@v1`), so a pipeline change doesn't hit every project at once.
  For something that routes tickets unattended, staged rollout is worth having.
- **Everything project-specific is one config file:** tracker team and project ids, state name
  mapping, design-owned paths, quality gate commands, deploy detection endpoint, deploy
  timeout and stale-claim grace period (§12), static preview target (the Cloudflare Pages
  project, §4), milestone naming convention. Anything not in that file is the protocol and
  belongs here. The system and screen **file maps are deliberately not config**: they live in
  the docs they govern (§4), so renaming a boundary is one reviewed file, not a file plus a
  config edit that can drift from it.
- **The pipeline repo needs its own tests** against a scratch tracker project. A bug here
  mis-routes tickets silently, which is the failure mode hardest to notice.

---

## 6. Concurrency

Three mechanisms, each covering what the others can't.

**Mutex labels are a file-level mutex.** Two kinds, one rule: *no two in-flight tickets may
share a `screen:<name>` or `system:<name>` label.* Screen labels cover design artifacts, which
are per-screen files; system labels cover the structural units the sketch declares — for a
Phoenix app, contexts — each mapped to its paths by its own doc's file map (§4). Enforced at
promotion into `Ready for dev`: a ticket whose mutex label is already in flight does not
promote. This prevents most collisions rather than detecting them. The declaration is
trustworthy for the same reason in both cases: design *creates* the screen files, and the
sketch *is* the system touch list — with CI auditing both against the file maps (§9), because
a diff that wanders outside its declared labels is a mutex nobody took.

**The label's spelling is the doc's filename, and the design pass is refused if it is not.**
CI derives the label it requires as `system:` plus the doc's filename without `.md`, so the
name in the touch list and the name on disk are one string living in two places. A pass
declaring a doc that does not exist now fails at the moment it declares it, naming the near
miss. It used to succeed: the label was created from whatever was declared, took part in the
mutex like any other, and could not be satisfied by any diff. Catapult's `ORC-5` declared
`core-dsl` against `systems/core_dsl.md` and the mismatch survived design, the author's
sign-off and a full dev run — 22 modules, 60 tests, three commits — before surfacing as 28
audit violations on every path the ticket was about, with a push-back asking a human to rename
a label as the only outcome left. The information needed to refuse existed at the moment the
label was created.

**"In flight" for this rule stops at `Merged`.** A merged ticket's branch is gone and its
commits are on main, so a ticket starting afterwards contains that work rather than racing it —
there is no concurrent edit left to prevent. Counting `Merged` held the labels for the whole
deploy-detection window instead, which on a platform with no deploy webhook is up to an hour of
a queue held by a ticket that was finished. If the deploy fails and the author sends it back,
it re-enters the queue and re-takes the mutex then, which is ordinary contention rather than a
special case.

**The dispatcher asks the same question the pickup assertion does**, through the same
function. It used not to ask at all, so a ticket whose label was held was dispatched into an
assertion that could only refuse — and because the refusal leaves the ticket in the queue, the
next beat did it again. Each of those was a full billed job with a checkout, a toolchain and a
service container, spent to be told no. Two places asking one question is exactly the shape
that drifts, so there is one `MutexHolder` and three callers: the promotion revert, the
pickup assertion, and the dispatcher.

**Files owned by no system** — the router, the mix manifest — are named in the sketch when
touched and left to git's textual conflict detection. Giving them labels would serialize
every ticket through them, which is the mutex failing in the other direction.

**Every ticket gets a design pass** — including tech debt, backend work and bugs. The pass is
cheap and it is the only thing positioned to notice a ticket touching a screen or a system
nobody predicted. The boundary ticket is the sole exception, with the rest of its exceptions,
in §10. A pass that finds nothing to decide — no screens, no artifacts, and a sketch
introducing no new structure — records that finding and its sketch in a marker comment (§9)
and advances the ticket directly to `Ready for dev`, skipping `Design review`: the pass exists
to catch missed screens and unreviewed decisions, not to manufacture a sign-off with nothing
to sign.

**Every ticket is reconciled,** screen labels or not, the boundary ticket again excepted. Reconciliation reads the PR diff against
the argument, not only the rendered surfaces, so a backend ticket has something to verify:
whether the change that landed is the change that was asked for.

**The base check covers what the mutex can't:** an undesigned fix landing on main while a
branch is open. Recorded as the merge-base SHA, compared at pickup. Resolution per §2.4.

**Claim is idempotent.** Every agent run carries its dispatch id, refuses to act if the ticket
is not in its expected state, and records the id in a comment. State-transition-as-claim plus
expected-state assertion is the whole mechanism; nothing more is needed at one dev agent.

**The pickup assertion also re-verifies the §9 invariants** — screen mutex, no `re-evaluate`,
sign-off actor — not just the state. Tracker enforcement is detect-and-revert (§9), so an agent
can briefly see a state the control plane is about to undo; refusing to act on anything that
fails the invariants is what makes that window harmless.

**Singularity is asked twice, and about two different things.** "Is an agent of this kind live
on another ticket" is the dispatcher's question and the pickup assertion's. "Is another run of
this kind live on *this* ticket" is only the pickup assertion's, and it is the one that was
missing: the first check skips the ticket's own run, deliberately, so a claim re-entering after
a resume is not read as a second agent — and with both runs on one ticket, each skipped the
other as itself. `ORC-45` was dispatched twice 82 seconds apart and both runs scanned the tree,
filed proposals and aborted the ticket, at about twenty-two minutes of duplicate model spend.

The two are told apart by **run id, not by ticket**: a resumed claim carries the id its run was
dispatched under, and a second agent does not. The snapshot therefore keeps every live run per
ticket alongside the single collapsed one that every other rule reads — the collapse is what hid
this, since whichever run it picked, the other recognised it as itself.

---

## 7. Precedence and re-evaluation

### The precedence rule

One rule, used everywhere the pipeline has to choose:

> **Urgent first. Then the ticket furthest along the pipeline. Oldest breaks ties.**

It governs dev pickup order (`Ready for rework` before `Ready for dev`, because rework is
closer to done), collision resolution, and any other contention. The rationale is the same in
every case: work already invested is worth more than work not yet started, and finishing things
beats starting them.

### Re-evaluation

When a thread discovers scope nobody predicted, it **finishes the step it's on** and **reports**
the newly-discovered mutex label — `screen:<name>` or `system:<name>`. The harness attaches it
to the ticket and adds `re-evaluate` to every ticket it now collides with.

**Reported, not written, and the distinction is why this used to be a sentence with no
implementation.** An agent holds the model credential and nothing else; every role prompt
forbids it from touching labels, for the same reason it cannot move a ticket (§9). So the
thread that is best placed to notice could not act, and the closest outcome available to it was
a push-back — which parks finished work and asks a human to add a label by hand. Measured on
Catapult's `ORC-5`: a complete, green, reviewed diff had to register its new component in
`config/config.exs`, a path `systems/foundation.md` owns deliberately, and the run's only
channel was to stop.

**The harness verifies before it attaches**, so declaring is not acquiring: the name has to
resolve to a real doc, and something in the run's own diff has to be mapped by that doc. A
label locks a system for every other ticket, and one taken by a run that does not touch it is a
queue held for nothing. A name that fails either check is refused, recorded on the ticket, and
does not stop the run finishing — the audit is the backstop, and landing complete work in
`Blocked` over a label is the failure this path exists to remove.

**A label reports a fact the file maps already decided; it is never permission to widen the
diff.** The path was touched because the work required it and some doc owns that path. An agent
that wants a label in order to touch more files is describing a push-back (§2.7).

By the precedence rule, the ticket further along **holds the ground** and the earlier one
**absorbs**. So `re-evaluate` on a further-along ticket is a *check* — does what I'm doing still
hold — and on an earlier one it is a *revision*. This is what stops a mostly-implemented ticket
being sent back to design by something that only just started.

Collision requires a shared mutex label, so a ticket carrying none can never be flagged. The
only adjacent case is a ticket *acquiring* a label mid-flight, at which point it is no longer
unlabelled. System labels widen this machinery to backend collisions: a design pass sketching
against a system another in-flight ticket is rewriting now produces a signal instead of a
surprise at merge.

**Behavior by the flagged ticket's state:**

| State | What happens | Cleared by |
|---|---|---|
| `Backlog` / `Todo` | Nothing. Cleared automatically on entry to `Ready for design`. | — |
| `Ready for design` / `Designing` | Folded into the pass. | design |
| `Design review` | Returns to `Ready for design`. Nothing is built; revision is cheap. | design |
| `Ready for dev` | **Blocks pickup.** Design re-reads: clear and hold, or demote to `Ready for design`. | design |
| `Ready for rework` | **Blocks pickup.** As above; scope is the newest comment. | design |
| `In progress` / `Reworking` | Dev finishes the current step, then reads. Does **not** restart. Clear and note in the hand-back, or push back if genuinely unbuildable (§2.7). | dev |
| `Checks` | **Holds promotion.** A green verdict does not advance the ticket while the flag is set, so nothing merges under an unresolved collision. No agent evaluates it here — `Checks` has none. | author, or design once a red or conflicted verdict has sent the ticket to `Ready for rework` |
| `Reconciling` | Evaluated in place as an additional reconcile item. **Blocks the merge.** | reconcile |
| `Merged` | Deferred — the change is merged and past recall. Becomes a finding → Triage. | — |
| `Done` / `Canceled` | No action. If the collision matters it is a new finding → Triage. | — |

**A `re-evaluate` label is cleared only by the thread owning the flagged ticket's current
state** — never by the thread that noticed. Otherwise the noticing thread clears its own flag
and nothing is re-evaluated.

**`Checks` is the one state with no such thread, and the row above says so rather than
pretending otherwise.** It used to read "evaluated in place by dev", which described nobody: the
dev run ended when the ticket entered `Checks`, and nothing dispatches an agent to a ticket
sitting there. What is implemented is the hold — a green verdict stops at the gate — and two
ways out of it. A red or conflicted verdict moves the ticket to `Ready for rework` carrying the
flag, where it becomes an ordinary queue re-read and design resolves it. A green one waits for
the author.

That asymmetry is the honest shape rather than a gap to fill. `Checks` is the last point before
a merge, so holding is the conservative answer and an automatic clear would be the pipeline
deciding a collision no longer matters at exactly the moment that judgment is most expensive to
get wrong. The cost is a green ticket that waits on a human, which the boundary gate then
surfaces: no milestone completes while a `re-evaluate` label is outstanding (§10).

**All `re-evaluate` labels must be clear before a milestone can complete** (§10).

---

## 8. Labels and priority

| Label | Meaning |
|---|---|
| `frontend` / `backend` | Where the work happens. Not a scoping constraint — one ticket may contain both. |
| `tech-debt` | Work on the shape of the code rather than what it does. Survives the label admission test because debt doesn't stop being debt when it changes hands; it gets paid. |
| `bug` | Defect. Runs the normal pipeline; `Urgent` is what makes it preempt. |
| `design-inbox` | Provenance: this came from the design agent. The question you'll want answered later when something looks odd. |
| `screen:<name>` | The design half of the mutex (§6). |
| `system:<name>` | The structural half of the mutex (§6). Declared by the sketch; mapped to paths in the project config. |
| `re-evaluate` | Unresolved collision (§7). |
| `needs-review` | Reconciliation couldn't tell. Deployed, clean, awaiting the author's eye (§11). |
| `needs-setup` | Parked on a human doing something the automation can't — a secret, an API, an account (§12). Blocked, but not broken. |
| `scope-satisfied` | The run found the whole scope already on `main` and changed nothing (§12). Almost always a duplicate to cancel. |
| `pushback` | The design can't be built as drawn (§2.7). Parked for the author to redesign or rescope. |
| `author-only` | This work is legal for nobody else. The pipeline routes around it entirely: no dispatch, no gates, no mutex, no revert — it moves only when the author moves it. |
| `harness` | A problem with the pipeline itself rather than with the project, filed by the run that hit it (§10). |
| `milestone-boundary` | Pipeline machinery. Routes the ticket to the boundary agent and away from the dev agent (§10). |

**`author-only` exists because some tickets have no agent-legal path to completion.** The
quality gates live in `pipeline.config.json` and `ci.yml`, both author-owned (§5), so a ticket
scoped to change the gate set could be claimed by the dev agent and then finished by nobody:
every file it needed was closed to it. That is a full run — checkout, toolchain, model — spent
to be told no, and repeated on every beat, because a refusal leaves the ticket in the queue.

A label rather than a state, by the admission test: it says who owns the work, not where the
work is, and the ticket still travels the ordinary states as the author does it.

**The pipeline routes around a labelled ticket rather than handling it specially.** Skipping
the dispatch alone was not enough: every other rule still applied to a ticket no agent would
ever touch, and each one broke in its own way.

| Rule | Author-only | Because |
|---|---|---|
| Dispatch (§13) | Skipped, in every state | Nothing an agent can land |
| Pickup assertion (§9) | Refuses, every agent kind | The half that holds when a run arrives by hand or from a dispatch planned a beat before the label |
| Writer matrix (§9) | Not judged | The workflow is Todo → Done and only the post-deploy check writes Done, so the author's close read as a violation and got reverted — and the revert is an arrival, so the next sweep judged it again |
| Mutex (§6) | Not held | No agent ever observes it finishing, so a shared screen or system label would park the queue behind a human's calendar |
| Dev singularity (§6) | Not counted | The dev agent is busy if any ticket sits in a dev-owned state, which infers a run from a state. An author dragging theirs into In progress — the obvious thing to do while working on it — would stop the whole queue |
| CI and reconcile (§13) | Not dispatched | The gates are the pipeline's. Reconcile would spend a model pass judging a diff no agent wrote against a scope no agent was given |

The boundary archive still sweeps them up. Its filter is the milestone and the state, not the
labels, so a Done author-only ticket is archived and named in the retro note with the rest
(§10) — routing around a ticket is not forgetting it.

**Three things apply it**, and the first two are the ones that matter, because a label nothing
writes is a label nobody remembers:

1. **Filing.** A boundary proposal whose `subject` names a path in §5's author-only rows is
   labelled as it is filed. Mechanical, no judgment, and it catches the common case — the debt
   scan finding a gate that is declared and not armed.
2. **A dev run**, on a ticket it files rather than on its own. A run that discovers mid-work
   that its scope needs one of those paths says so through its outcome file; the harness files
   the author-only half as its own ticket under this label, links it as a blocker, and parks
   the original in `Blocked` (§12). It must not commit the change to find out: the push is
   rejected and the run dies with the hand-back still in it. This is an escape hatch and
   should stay a rare one — see §12 for why.
3. **The author**, directly, whenever they already know.

Removing the label is how the ticket goes back to the pipeline, and that is deliberate — the
skip applies in every state, so a ticket left labelled after the author has done the
author-only half will sit still rather than being picked up for the rest.

**Ordering is derived, never stored.** Which ticket to start next, and what can run beside it,
is a pure function of the graph the tracker already holds: open blocking relations, mutex
labels, states, priority, age. `pipeline order` computes it and prints it in layers — in
flight, ready now, freed by what is in flight, freed by those two together, and everything
whose depth is not yet decidable — each ticket with its blockers and what it blocks.

Nothing writes that ordering down, and the reason is not tidiness. The inputs change from
several directions at once, and one of them is decisive: **the mutex labels do not exist until
the design pass produces them** (§4). An order computed when tickets are pulled into `Todo`
therefore cannot know what parallelises — it would be wrong by construction rather than by
neglect, and a stale ordering is worse than none because it is the kind of thing that gets
acted on. What is *chosen* rather than derived stays in the two fields that already hold it:
priority for preference between two legal orders, a blocking relation for sequencing that is
real. If a sequencing constraint is worth remembering, it is worth recording as a blocker.

One constraint deliberately lives outside the graph: **milestones are worked in sequence**
(§2.9), and a later milestone's tickets are commonly filed with no dependencies at all, because
the milestone *is* the dependency. Read literally, such a ticket has nothing blocking it. So anything
outside the current milestone is held out of the startable layers and says why — the
alternative is a report confidently recommending work that must not be started yet, which is
the one failure that would make it cost more than it saves.

**The default scope is every milestone**, with that gate doing the work. Scoping to the current
milestone was the older default and hid a case: a ticket accepted out of Triage but not yet
assigned a milestone. **Startable and committed are separate axes.** Nothing sequences a
milestone-less ticket and no dispatcher reads milestones, so the queue takes it as soon as it
reaches `Ready for design` — it belongs in the startable layers. But it is absent from any milestone's
scope, because that scope is the work committed to that milestone and nobody committed this
one. Asking for a milestone by name asks what is in it; asking for nothing asks what can be
started.

It answers a question that gets asked between sessions, away from a keyboard, so each project
also carries a dispatch-only `pipeline-order` workflow that runs it and renders the layers to
the run summary with every key linked. Same computation, same lack of storage: the run is a
snapshot of a moment, and the log of past runs is a log of past moments, not a plan.

**Priority is the tracker's built-in field, not a label** — it's ordered, and an ordered field
is what both the queue and the debt-fill rule need.

**`Urgent` preempts at pickup only.** It never interrupts a ticket mid-implementation: a
half-finished branch is worse than a few minutes' wait. It **does** override the milestone
boundary pause, marked as blocking the boundary ticket or not — anything urgent enough to
carry the flag is urgent enough to outrank a pause, and the boundary's own work is bug-fixing
and ticketing, which is rarely urgent on its own account. A true stop-the-world security patch
bypasses the pipeline entirely and is fixed directly on main.

**Reordering.** No agent reorders mid-milestone. The **grooming pass re-ranks at the milestone
boundary** (§10) — it has to, or the debt-fill rule draws from an ordering set months ago —
and its output is subject to the author's review before the queue resumes.

---

## 9. Enforcement

A workflow of this shape naturally leaves almost everything to convention, which works at one
human's scale and degrades quietly past it. With merge automated there is no human backstop at
all, so each rule is deliberately assigned: enforced, verified on pickup, or left to discipline.

**CI (blocking, in `Checks`):**
- compile, format, warnings-as-errors, type checking, module boundary rules
- static storybook export builds and publishes
- **a new component module or theme token not named in the issue fails the build** — the class
  audit, promoted from convention to enforcement, so a proposed component cannot arrive
  unannounced inside an artifact. Components are enforced: a file added under the project's
  `componentPaths` whose base name appears nowhere in the ticket's title, description or
  comments fails. **Theme tokens are not**, deliberately — a token is a key in a theme
  configuration, no project the pipeline drives has one, and a grammar invented against no real
  file is a check that asserts its author's guess. That half stays convention until there is a
  theme file to read, and the audit prints which halves it did not run rather than reporting a
  clean pass it never made.
- a PR touching files in a screen doc's file map carries **that screen's** label, and a PR
  touching paths in a system doc's file map carries **that system's** label — the mutex audit,
  the same promotion from convention to enforcement as the class audit: a mutex nobody took is
  a collision nobody could prevent. File maps make it per-name; "some design path, some screen
  label" would let the wrong label satisfy the check.
- no path may appear in two system file maps — overlapping ownership is an ambiguous mutex,
  and an ambiguous mutex is two tickets in the same files with a green build
- `screens/*.md` and `systems/*.md` contain no state sections and no code inventory — the doc
  lint, enforced on the docs as they stand rather than on the diff, since a doc that has held a
  banned section since before this ticket is still holding it. It catches both rules in their
  **sectioned** form: a `## States`-style heading, a `## Modules`-style one. Prose is not
  linted, and that is the point — a standing decision naming `farewell/1` is the decision doing
  its job, while a heading with a list under it is the split the rule was written against. A
  check that failed good docs would be switched off, taking the rule with it.

**Reconciliation (blocking, and the last gate before production):**
- the PR diff says what the issue asked for
- every state the issue named exists, named as asked — a state renamed in implementation is
  fine if it's better, but something should say so, and if nothing does it's more likely nobody
  noticed
- the static storybook renders what the narrative doc describes
- standing decisions touched by the change landed, and none were contradicted in passing
- the structure that landed is the structure the sketch named — a deviation is fine if the
  hand-back argues it, and if nothing does it's more likely nobody noticed
- **a surface changed with no storybook variation at all is called out even on a pass** — a tab
  named only in prose, a route nothing renders. Comparing states to variations makes a surface
  with neither invisible, and this is the only place that gets noticed.

**Tracker automation (checked and reverted):** Linear has no pre-transition hook — any state
change succeeds, and automation learns of it afterward. So these rules are enforced as
invariants rather than gates: the control plane checks them on every poll and reverts a
violation to the prior state with a comment naming the rule. The window between violation and
revert is real, and it is closed from the other side — every agent's pickup assertion (§6)
re-verifies these invariants before acting, so a state the control plane is about to undo is
one no agent will act on.

- promotion into `Ready for dev` while the screen label is already in flight → reverted
- any forward transition while `re-evaluate` is set → reverted
- `Designing` → `Ready for dev` without passing through `Design review`, or a sign-off not made
  by the author → reverted — unless the design agent's marker comment declares a decisionless
  pass (§3, §6), which advances directly
- only reconciliation merges; only the post-deploy check writes `Done`, the boundary ticket
  excepted (§10). Merge rights are enforced in GitHub via branch protection (§5), not in the
  tracker — the tracker cannot police the repo.

**Pipeline comments are programmatic, not agent-authored.** Every comment the control plane
relies on later — CI failure comments (§12), dispatch ids (§6), boundary step completions,
live-suite results (§10) — opens with a fixed machine-readable marker and is written by the
control plane or the run harness, never composed by a model. A count or a resume that depends
on prose an agent phrased differently each time is a count that drifts.

**Verified on pickup (agent, flags rather than blocks):** base SHA, blocking relations,
expected-state assertion.

**Left to discipline:** the quality of the argument in a description, the quality of the
sketch's judgment (its *coverage* is audited above; whether the structure it proposes is good
is not), and the §2.4 judgment that two changes touch the same behavior. All are readings, and
nothing can check a reading.

**The trust boundary is the tracker and the PR thread.** Agents read issue text, comments and
diffs, and act with repository write access; reconciliation merges to production unattended.
Anyone who can write to the Linear project or comment on a PR can therefore steer an agent, so
the workspace roster *is* the access control list. Sound at one trusted author; revisit before
anyone else gets write access. What holds regardless: agents treat tracker and PR content as
the work to be judged, never as instructions that override this protocol; CI gates run
unconditionally; the kill switch (§13) stops dispatch. Worth restating in each project README,
where the person about to widen workspace access will actually see it.

---

## 10. The milestone boundary

Every milestone ends with a hard pause — the author's second touchpoint, and the only place
manual testing happens.

**The pause is tracked by a ticket, not by pipeline state.** Every other handoff in this system
is a ticket in a state; modelling this one as a phase of the controller is what leaves it with
no signal for *the author has finished*. A ticket also gives the pass a place to live: the
retro note, the debt scan output and the grooming proposals all become comments on one issue,
which is what somebody goes looking for months later.

### The boundary ticket

Created by automation when the last ticket in the milestone resolves, tagged to that milestone,
labelled `milestone-boundary`, opening in `Todo`.

| State | Meaning | Moved by |
|---|---|---|
| `Todo` | The boundary work hasn't started. The author's manual pass happens here. | automation |
| `In progress` | Boundary agent running archive, debt scan and grooming. | **author** — this transition is the signal that the manual pass is finished |
| `Boundary review` | Proposals filed in Triage; awaiting accept or decline. | boundary agent |
| `Done` | Boundary complete. **Queue resumes.** | author |

Each state means exactly what it means everywhere else. The author setting `In progress` is
unusual only in that it triggers an agent rather than describing one — which is precisely the
signal that was missing.

### The sequence

1. Last ticket in the milestone resolves. **Queue pauses.** Boundary ticket created.
2. **Live suite.** The control plane dispatches the project's live-suite workflow (config
   `agents.live-suite`; unwired = logged and skipped) against the boundary ticket: the
   project's `:live`-tagged tests — real network, real providers, the one deliberately
   non-deterministic check, once per milestone. The run posts a result marker on the ticket;
   the author's pass reads it, and a failure becomes a blocker like any finding of the pass.
   Per-ticket CI stays deterministic and merges never gate on this — the live suite gates the
   milestone, not the ticket, because a live check on every ticket would put network flake
   inside the escalation rules (§12), and a live check that never runs is how "merged and
   green" quietly diverges from "works against the world". A run that dies without posting its
   marker is the author's to re-run — the sweep does not resurrect it, for the same reason
   stale claims are detected rather than silently retried.

   **The job must be able to run the command.** The live suite is the project's CI job with a
   different filter — same dependencies, same services — and a stub carrying only a toolchain
   cannot start it. That failure is worse than it looks: a suite that aborts before loading a
   test is a red `fail`, not the honest `no-tests` below, so the first `:live` test a project
   writes appears to have broken the build it was written to fix, and the workflow edit that
   would fix it is one agents may not push (§5). Land that edit with or before the first live
   test.

   **Three results, not two: `pass`, `fail`, `no-tests`.** A test runner handed a tag filter
   that matches nothing exits non-zero — an `--only` filter with no match is an error, not an
   empty pass — so a project that has not written its first `:live` test reported a *failing*
   live suite at every boundary. The first real boundary spent an investigation on exactly
   that, and a gate that is red for structural reasons is a gate the author learns to skip
   past, which costs more than the missing coverage does. `no-tests` is deliberately neither
   verdict: calling it a pass would claim the world was checked when nothing ran, which is the
   failure this gate exists to prevent. The ticket says plainly that nothing was checked and
   the author's pass decides whether the milestone closes without it; the run itself is not
   marked red, because a project with no live tests yet is not broken.
3. **Gate:** all `re-evaluate` labels clear, and nothing in `Blocked` except tickets carrying
   `needs-review` — those are the author's pass, not a barrier to it. A `needs-setup` ticket
   *is* a barrier: it is work the milestone is waiting on, and closing a boundary over one
   would ship a milestone whose last step nobody took. Anything outstanding is named on the
   boundary ticket.
4. **Author's pass**, with the ticket still in `Todo`: the live-suite result first, then manual
   testing across the milestone, plus every `Blocked` ticket carrying `needs-review`, each of
   which the author closes or sends to `Ready for rework`. Blockers filed during the pass run
   the normal design and dev loop.
5. Author moves the boundary ticket to `In progress`. **This is the signal.**
6. **Archive pass.** The milestone's `Done` issues are archived — which reclaims tracker
   headroom but makes them invisible to the "is this already filed?" check. So the archive pass
   **emits a retro note into the repo**: issue keys, titles and merge shas, one line each.
   Without it, archiving silently breaks duplicate detection.

   **The shas are there for the rehearsal reset, and they are there for the same reason the
   findings in step 7 are.** The reset learns what to revert by reading `merged` markers off
   the tickets it archives, and this step archives those tickets first — so a reset run after a
   boundary found no tickets, wrote an empty merge list, printed "the last rehearsal merged
   nothing" and left the commits on `main`, on a green run. Measured on the dummy project:
   ORC-1 and ORC-18 reverted, ORC-23 (PR #15) did not, with `retro: Rehearsal 1` sitting
   directly above it in the log.

   The general rule, which this section now instances twice: **information that exists at
   exactly one moment is owned by the step that ends that moment.** Anything a later pass needs
   about archived work has to be in the note, because the note is the only thing that survives.
7. **Debt scan**, bounded inputs only: diffs merged since the last boundary, new `TODO`/`FIXME`,
   skipped or deleted tests, dependency and advisory drift, and **the harness findings agents
   recorded this milestone**. Bounded because "did we take on debt?" asked openly produces
   invented findings.

   **The archive step carries the findings out.** They live in comments on the milestone's
   tickets, and step 6 archives exactly those tickets — an archived issue vanishes from
   listings, so a boundary that dies between archiving and scanning resumes into a claim that
   collects nothing and reports a milestone with no findings rather than a milestone whose
   findings it lost. The archive step therefore writes them onto its own step marker before
   removing the tickets, and a resumed claim unions what it can still collect with what that
   marker preserved. Same rule as the merge shas in step 6, and the same failure it was fixed
   for: the information exists at exactly one moment, and the step that ends that moment owns
   preserving it.

   Harness findings are the pipeline's own problems, filed by the runs that hit them
   (`kind: "harness"`, labelled `harness`). Every other input describes the project; this one
   describes the machine, and the author is the only person who can fix it while the agents are
   the only ones who watch it fail. Without the channel the observation went into hand-back
   prose and stopped there: three separate passes reported that no `Base:` sha was recorded and
   nothing aggregated them, which is how a check that never ran survived four rehearsals. The
   boundary is the first pass that sees them together, and together is what makes them legible
   — one run saying it is a shrug, three runs saying it is a missing check.

   Deliberately narrow, and the narrowness is the feature: agents may record harness findings
   and nothing else. Product debt, coverage and refactors stay with the scan above, because an
   agent that reads a product in an afternoon is very good at spotting gaps and very bad at
   judging whether a gap is news (§4) — and one flooded list answers neither "is this codebase
   accruing debt" nor "is the pipeline costing me tickets".
   *Gating test:* does the next product milestone get materially harder without it? Yes →
   propose for the gating debt milestone. No → backlog.
8. **Grooming pass.** Re-ranks existing debt as well as proposing additions.
9. Proposals land in **Triage**. Design findings and debt only, never bugs: a bug parked in a
   queue has been rescheduled rather than repaired.
10. Boundary agent moves the ticket to `Boundary review`.
11. **Author** accepts or declines Triage, confirms the ranking, closes the boundary ticket, and
    pulls the next milestone into `Todo` in one pass. `Done` resumes the queue.

### Blocking work found during the pass

Anything that must be fixed before the milestone closes is filed against the **current**
milestone and marked **blocking the boundary ticket**. Anything that can wait goes to Triage for
the next one. The distinction is drawn once, at filing time, rather than being a judgement the
automation has to make later.

**During the pause the dev agent drains only tickets blocking the boundary ticket.** Otherwise
the queue is either fully stopped, in which case blockers never get fixed, or fully open, in
which case the pause means nothing.

The boundary agent does not begin while a blocker is open.

### Resuming a failed boundary pass

The boundary agent has a lot to do and can fail partway. Recovery is `Blocked` → `In progress`
or `Boundary review` → `In progress`, possibly more than once, and neither is useful if
re-entry means starting over.

**Each step posts a completion comment on the boundary ticket.** On entry to `In progress` the
agent reads its own comments and resumes at the first step without one. That is the whole
mechanism: no extra states, no special-casing of which state it re-entered from, and the
comment thread doubles as a readable record of where it got to — which is what you want when it
stalls overnight.

**A completed pass ends the window, and a fourth comment says so.** The boundary ticket outlives
its pass: the author closes the review, works the proposals through the pipeline, and moves it
back to `In progress` for another look at what has landed since. That is a second pass, not a
resume, and the two are indistinguishable to a reader that just unions every step comment on the
ticket. So the hand-back posts a `close` comment — not a step to resume into, the terminator that
says everything above it belongs to a pass that finished. A claim that sees one starts from
nothing.

Catapult's boundary is where this was found. `ORC-45` ran a full pass, filed five tickets, and
was sent back two days later for a second look at the harness findings the runs since had
recorded. The claim read the old `scan` and `file` comments, the workflow skipped the model step
on `scan_done`, the file step skipped itself, and the run reached `Boundary review` in three
seconds having read nothing and filed nothing — overwriting the previous pass's composition
proposal with an empty one on the way past. Fast and green, which is the worst way for a pass to
do nothing.

The window still has one edge: a run that dies between posting `close` and moving the ticket
leaves a pass that reads as finished, so the next entry redoes it. That costs one debt scan and
files nothing new — the dedupe keys hold — and it is the trade for having the terminator be the
last thing written rather than something a crash could skip.

Deliberately not states. `Archiving`, `Scanning` and `Grooming` would all answer *who has the
ball* identically — the boundary agent — and so fail the state admission test. The problem was
never visibility of the step; it was re-entry.

**Every step must be safe to re-run,** because a comment can be missing when the work landed:

| Step | Re-run safety |
|---|---|
| Archive | Naturally idempotent; already-archived is a no-op. |
| Debt scan | Read-mostly and convergent. |
| Grooming re-rank | Convergent — the same inputs produce the same order. |
| Retro note | Writes a file. **Merged by issue key, not written once.** Existing-wins was the first rule and it made the note whatever the first pass knew, permanently: a second pass archived its tickets and their merge shas with nothing recording them, which is the one thing the note exists to prevent. Merging is idempotent for a resumed pass and additive for a new one, and a later entry wins on conflict because a pass that has shas for a ticket knows more than one that had none. |
| Close comment | Posted last, after the hand-back's composition proposal. It is what makes the steps above belong to a pass rather than to a ticket. |
| Triage proposals | **The dangerous one.** Each proposal carries a dedupe key of milestone plus finding, or a re-run files it twice. The set it dedupes against is every issue in the project carrying a proposal marker — not just the ones still in Triage. Filtering to Triage meant a proposal left the set the moment the author accepted it, so the next pass that found the same thing filed it again. |

### Exclusions

The boundary ticket is special-cased in eight places. Collected here rather than scattered,
because each one is invisible from where it applies:

1. **Excluded from the "last ticket resolves" trigger** — otherwise it retriggers itself forever.
2. **Excluded from the archive pass** — it is the record of that pass.
3. **Excluded from the dev agent's queue.** The `milestone-boundary` label routes it to the
   boundary agent, and the dev agent skips any ticket carrying it.
4. **Excluded from the design pass**, and from the rule that issues are created only on the
   author's ask. It is machinery, not work.
5. **Excluded from reconciliation.** It never opens a PR and never merges.
6. **Excluded from the screen mutex**, trivially: it carries no screen labels, so it cannot
   collide and cannot be flagged for re-evaluation.
7. **Never `Canceled`, never in Triage.** A boundary that shouldn't have happened is still a
   boundary that happened.
8. **Closed by the author, not the post-deploy check.** It has nothing to deploy, so the rule
   that only the post-deploy check writes `Done` cannot apply to it.

**The ticket's description carries its own instructions,** written at creation, so the state
meanings above are readable from the ticket rather than remembered:

```
Milestone boundary — <milestone name>

This ticket is pipeline machinery. Automation created it and will not close it.

  Todo            → your pass. The live suite's result lands below as a comment —
                    read it first; a failure becomes a blocker like any finding.
                    Manual test the milestone, and clear any Blocked tickets
                    labelled needs-review.
  In progress     → YOU move it here when your pass is done. This is the signal.
                    The boundary agent then runs archive / debt scan / grooming,
                    posting a comment per step. If it fails, move it back here and
                    it resumes from the first step with no comment.
  Boundary review → the agent put it back. Proposals are in Triage; accept or
                    decline, confirm the ranking, then close this ticket and pull
                    the next milestone into Todo.
  Done            → you close it. The queue resumes.

The queue is paused while this ticket is open. The dev agent will only pick up
tickets marked as blocking this one — plus anything marked Urgent, which
overrides the pause.

Blocking bug found during your pass?  File it against THIS milestone and mark it
blocking this ticket. Anything that can wait goes to Triage for the next milestone.
```

### Invariants

**Anything in the current milestone that hasn't been started is in `Todo`, not `Backlog`.** The
milestone says *committed* and the state says *not committed*, so a `Backlog` issue carrying the
current milestone is a contradiction — almost always one milestoned after the pull rather than a
deliberate choice. Correcting it is mechanical, not a judgement.

**Tech-debt milestone composition:** all gating debt, **plus a minimum of 5 non-gating tickets
by priority.** Without the floor, a heavy gating set means non-gating debt never runs and
accumulates permanently — the failure mode the alternating-milestone pattern exists to prevent.
The floor is a minimum to draw, not a quota to invent: a backlog holding fewer says so.

**The boundary agent proposes this composition; the author enacts it.** The arithmetic — every
unscheduled gating ticket, then non-gating by priority to the floor — is posted as a comment
before the ticket reaches `Boundary review`, so the author's assignment pass is a review rather
than a reconstruction. It stops at a proposal because **assigning a milestone is a commitment**:
the invariant above turns unstarted current-milestone work into `Todo`, so an agent writing
milestones would be an agent committing scope nobody accepted. Milestone assignment is the
author's, always.

**Candidates include proposals still sitting in Triage,** which is the whole reason the
composition is posted after the file step rather than before it — the tickets it names are the
ones that step just created. Snapshots skip triage-category states, deliberately, because a
proposal nobody has accepted is not dispatchable work; the snapshot therefore carries them in a
separate list that only this pass reads. Without it the composition could not see a single thing
the boundary had filed: Catapult's `ORC-45` printed "Nothing to schedule — no unscheduled
tech-debt tickets" directly beneath "Filed 8 proposals". It went unnoticed while proposals landed
in `Backlog` — the filer's fallback when a team has no Triage state — and surfaced the moment
that team gained one.

An unaccepted proposal is a candidate, not a commitment, which is the same thing every entry in
this list is. The author accepts or declines it and assigns the milestone in one pass either way.

---

## 11. Reconciliation

**What it checks: did the merged work end up saying what the issue asked for?** Not whether the
feature works — nothing here proves that, and a ticket that passes reconciliation is not one
anybody has tested. It is narrower than it sounds and worth doing anyway, because it is the one
drift nobody else is positioned to notice: the author of the argument is the only one who can
tell that the paragraph which landed isn't it.

### Why it runs before the merge

The obvious placement is after, since a rendered surface is the thing you want to look at and
main is what gets deployed. Per-branch static exports remove that constraint, and running
earlier buys three things:

- **Rework stays inside one PR.** A bounce is more commits on an open branch instead of a new
  branch against an already-merged change.
- **Nothing that fails reconciliation reaches production.**
- **CI is not the only pre-production gate.**

**What is genuinely lost:** a branch preview shows the ticket in isolation. A post-merge check
shows it composed with everything else that has landed — merge semantics, other tickets since
the base, deploy-time asset compilation. The screen mutex covers design-owned files, but
dev-owned files have no mutex, so semantic conflict between two tickets is possible and a
pre-merge check cannot see it.

### So it splits in two

**Pre-merge (`Reconciling`) — the substantive pass,** per §9. Three outcomes:

- **Pass** → merge, state → `Merged`.
- **Drift or omission** → `Ready for rework`, with a comment naming exactly what is missing.
  The comment is not optional: the description still describes the original change, so a dev
  session reading only that rebuilds the wrong scope. The commonest finding is a missing
  *paragraph* rather than a missing feature — a standing decision that didn't land, copy that
  got paraphrased.
- **Cannot tell** → merge, state → `Merged`, **plus the `needs-review` label**. Merging anyway
  is deliberate: holding it would keep the screen mutex locked for weeks and stall everything
  behind it, and "cannot tell" was never a finding of fault. Ambiguity must never resolve itself
  as pass.

**Post-deploy — the thin check.** Mechanical, no judgment: did the deploy succeed, and do the
expected surfaces render in production. Its real job is catching *merged but never deployed*,
which is otherwise invisible.

- Clean, no `needs-review` → `Done`.
- Clean, `needs-review` → **`Blocked`.** The work is deployed and nothing failed, but a human
  has to look before it closes, and `Blocked` already means *the author has this*.
- Anything else → `Blocked`.

Routing "cannot tell" through `Merged` rather than parking it earlier is what keeps the
post-deploy check on every merged ticket. A ticket that skipped it would be the one case where
*merged but never deployed* goes undetected.

### Two things reconciliation does not do

It doesn't fix the work itself — that's the dev agent's, and a reconcile that patches its own
findings is no longer a check. And it doesn't reopen over a disagreement it would have lost at
design time: the dev agent may have found a reason the design couldn't work as drawn, which is
the review working rather than failing. That's an argument for a comment, not a state change.

---

## 12. Failure handling

**`Blocked` is global.** Any agent may move any ticket there. It has six flavors, and both
the comment and a label say which:

- **Something failed.** Name what failed and the state it was in.
- **Nothing failed, but a judgment is needed.** The `needs-review` case: deployed, clean, and
  waiting on a human to say whether it landed as asked.
- **Nothing failed and no judgment is owed — a human has to do something the automation
  can't.** The `needs-setup` case: a secret that has to exist in an environment, an API to
  enable, an account to create. An agent files it with `abort --reason needs-setup`, saying
  what has to be done; the harness attaches the label. It is not a new state, because a state
  is protocol vocabulary and this one would dispatch nothing — it would be `Blocked` under
  another name with a duplicate copy of every rule attached to it. It is a label because the
  distinction it carries is "is anything broken", and a `Blocked` column where waiting and
  broken look identical answers that question wrongly.
- **Nothing failed, and the ticket asks for what is already there.** The `scope-satisfied`
  case: the run read the scope, found every clause of it on `main`, and changed nothing.
  Almost always a duplicate of merged work, which is `Canceled` rather than `Done` (§2.6) so
  `Done` stays a record of what actually shipped — but sometimes a scope that went stale and
  wants rewriting, and the pipeline cannot tell which. It parks with the run's hand-back and
  the author decides.
- **Nothing failed, and the design can't be built as drawn.** The `pushback` case (§2.7), which
  parks here rather than looping back to the design queue.
- **Nothing failed, and no agent can land the change.** The `author-only` case: the work is in
  `.github/workflows/**` or `pipeline.config.json` (§5), or in another repository the run is
  not checked out in — the pipeline's own, most often, when what needs fixing is a gate, a
  runner or a prompt. A fact about the run rather than a judgment, and the one flavor reachable
  without anything going wrong at all.

  **This one splits the ticket rather than labelling it.** The harness files the author-only
  half as its own ticket in Triage, labelled `author-only`, and links it as a blocker of the
  ticket that found it; the original parks in `Blocked` unlabelled. Labelling the original was
  the first shape, and it retires a ticket the pipeline can otherwise still do: the skip
  applies in every state (§8), so after the author made the one-line workflow change they
  would have to remember to take the label off before anything could move it. The blocking
  relation says the same thing with machinery the queue already has — an open blocker stops
  the dispatch and the pickup assertion both (§9) — and it closes on its own when the author
  closes their half.

  **It should almost never fire.** Project code has no reason to know about the workflows that
  deliver it, so whether a ticket is author-only is normally knowable when it is written —
  filed at the boundary from the proposal's `subject`, or by the author directly (§8). A dev
  run discovering it mid-flight means the ticket was scoped wrong, and the filed blocker is
  the record of that as much as it is the work.

**A run that changes nothing states which of these it is, in a file.** The model has the model
credential and nothing else — no tracker key, no repository token — so it cannot move a ticket,
and that is the boundary rather than an oversight (§9). It writes `{"outcome": ..., "summary":
...}` alongside its hand-back and the harness routes it.

**Three labels, not one**, because a duplicate ticket, a missing secret and an unbuildable
design want three different actions from a human. A `Blocked` column that renders them
identically is one where every ticket has to be opened before it can be triaged, which is the
same failure as a column where waiting and broken look alike.

**And not four.** A run that changes nothing and names no reason is filed as `scope-satisfied`
too, with a comment saying the label was inferred rather than reported. A fourth label for it
was tried and dropped: it is the same status, read by the same person, answering the same
question, and two labels somebody triages identically are two labels they have to learn the
difference between for nothing. The hedge belongs in the prose, where it can be read, rather
than in a label, which is read at a glance.

**Project-wide reads are fatal to a snapshot; per-ticket reads degrade it.** The agent-run list
and the open-PR list describe the whole project, and a snapshot missing either is not a
snapshot. A CI verdict or a merge state belongs to one ticket, so losing it costs that ticket
its facts and nothing else: it is reported and skipped, and the core reads the absence as "not
judged yet" and waits. A stalled ticket instead of a stalled project.

The rule is stated because it was not obeyed, and the failure did not look like what it was.
One 403 reading `ORC-5`'s checks aborted the snapshot build — and every claim builds a snapshot,
so it aborted `ORC-7`'s design claim too, a ticket with no relation to it. What is given up is
real and is the trade: a ticket whose verdict cannot be read now waits quietly rather than
failing loudly, so the honest report of the failure has to come from somewhere else, which is
what the claim-failure record below is for.

**A run that dies before it claims says so, and a broken harness parks the ticket.** Every other
failure route posts through the abort path, and abort needs the claim file to know what it is
aborting — so a run that never got one skipped it, and the loudest failures, the ones where the
harness broke before the agent started, were the only ones that left nothing on the ticket at
all. Catapult's `ORC-7` sat in `Designing` for 23 minutes showing a healthy state and a
dispatched run, after its claim died on a 403 reading an unrelated ticket's CI verdict.

**Two failures wear the same shape, and they want opposite handling.** A pickup assertion that
refuses is the pipeline working — a held mutex, an agent of that kind already running, an open
blocker — and the ticket is exactly where it should be; parking it would pull work the protocol
deliberately left alone out of the queue. A snapshot that could not be built is the harness
unable to evaluate the question at all, which is a failure like any other and the author's to
see. Both are a claim command exiting non-zero.

So refusal is **a type, not a phrasing**: every branch of the pickup assertion carries a
sentinel, the claim exits `2` for it and `1` for everything else, and the workflow routes on the
status rather than on prose that a reword would silently change. A refusal earns a comment and
nothing more. Anything else earns a comment and `Blocked` — the same place the stale-claim rule
would have reached twenty minutes later, now immediately and carrying the reason instead of "a
run stopped being live". An unreadable status is treated as a failure, because a refusal parked
by mistake is one click to undo and a failure filed as a refusal is a broken pipeline nobody is
told about.

**Only the author moves a ticket out of `Blocked`,** and they choose the state. There is no
automatic return path, because unblocking almost always requires something the automation
can't do — a comment resolving an ambiguity, a code change, a redeploy — and a ticket returned
to the state it bounced from would arrive in a different condition than it left. The author is
the one who knows which.

**Every arrival in `Blocked` records the state it came from,** in a `from` field on the
transition's marker — carried on whichever marker the transition already posts, and on a bare
`blocked` marker when it posts none. The rule above is what makes this necessary rather than
nice: the author is being asked to choose a destination, and a `needs-review` ticket answers
that by sitting at the end of the line while "`Reworking`, until someone sets a secret" does
not. The origin was previously recoverable only from the tracker's history, which no tool can
read and no author reliably remembers.

**Escalation rules:**
- **CI red on a non-draft PR → a failure comment, then `Ready for rework`.** The comment is
  written programmatically by the control plane (§9) with a fixed marker, naming the failing
  jobs and linking the run — it is the newest comment, so it is the scope (§2.3), and the dev
  agent picks the ticket up through the normal rework queue rather than by a side channel.
  **The link is not the evidence.** The dev agent holds no GitHub credential (§9) and cannot
  open the run it is being pointed at, so the rework claim fetches the failing jobs and the
  tail of each one's log and puts them in the prompt, fenced as evidence rather than
  instruction — a CI log carries whatever a test happened to print. The harness reads, the
  agent reasons, exactly as with the ticket body and the non-asks document; "let it read CI"
  would be a credential, not a feature. Logs are bounded — three jobs, a tail each — because
  a whole log is mostly setup and an unbounded one is an unbounded prompt. A fetch that fails
  is stated in the prompt rather than swallowed: a rework with no evidence and a build with
  nothing to say look identical, and only one of them licenses a confident fix.
- **CI red twice on the same branch → `Blocked`.** The count is the count of the control
  plane's own failure-comment markers on the ticket — nothing else needs to be stored, and a
  marker can't be miscounted the way an agent's prose can. Two reds on one branch is rarely a
  flake; the author decides whether it's scope, design, or infrastructure.
- **A passing reconciliation that cannot merge → the conflict comment, then `Ready for rework`.**
  The branch conflicts with something that landed while the ticket was in flight. The verdict
  stood: the work is right and the branch is stale, which are different problems with different
  owners, and merging main into a branch is the one failure here a dev agent is unambiguously
  equipped to fix. Previously the merge error aborted the run and the abort sent the ticket to
  `Blocked` — the author's, exclusively, with no automatic way out. The comment is the scope
  (§2.3) and says in as many words that the work is not in question, because a dev agent handed
  a bounce reads it as a finding about the diff and will otherwise re-litigate a design that
  just passed. Counted under its own marker, never as a reconcile bounce: that count escalates
  because two failures to land the same scope is a design problem, and a conflict is not a
  finding about the work at all. **Three conflicts on one ticket → `Blocked`**, looser than the
  bounce rule on purpose — it counts other people's merges rather than this ticket's faults —
  but bounded, because without a bound an active main and a slow ticket loop between
  `Reconciling` and the queue forever, burning an agent run each pass. That one is sequencing,
  and sequencing is the author's.
- **A pass that changed no files still gets a fresh verdict**, because the harness commits an
  empty one before pushing. CI fires on `pull_request`, which needs a push, which needs a
  commit — so a rework that correctly changed nothing left the head sha exactly where the last
  verdict was already recorded. The ticket entered `Checks`, the sweep read that verdict,
  recognised a run it had already acted on and said nothing, and `Checks` has no agent and no
  stale-claim timeout to catch it.

  It is not a corner, because **the verdict is a function of the diff and the labels, not the
  diff alone**: the mutex audit reads the ticket's labels from the tracker when it runs (§9), so
  the same sha is legitimately red before a label is fixed and green after. Catapult's `ORC-5`
  sat in `Checks` for half an hour on a rework whose entire fix was two label corrections. The
  empty commit is what re-arms the trigger; a host capability to re-run a workflow would buy
  nothing over it, and the port does not have one. Only on the route that reaches `Checks` — a
  parked outcome hands the ticket to a human, and a commit asking for a verdict nobody will read
  is a line in the log that lies about why it is there.
- **A conflicted branch in `Checks` → the same conflict comment, then `Ready for rework`**,
  without waiting for CI. This is the same event as the rule above, caught earlier, and it has
  to be caught earlier because a conflicted PR never reaches the merge attempt at all: GitHub
  builds no merge commit for one, so the `pull_request` run never starts, so no verdict ever
  arrives — and `Checks` has no agent, so the stale-claim rule does not cover it either. A
  ticket in that position had no exit whatsoever. Not blocked, not timing out; parked. It is
  read from the PR's own merge state, which is a **tri-state**: GitHub computes it in the
  background and answers "not yet" the same way it answers nothing at all, so *unknown* must
  never be acted on as a conflict, or every freshly pushed branch bounces the moment it arrives.
  A green-but-conflicted PR is bounced here too rather than promoted, since promoting it spends
  a whole reconcile pass to reach the same state; reconciliation's own bounce stays for the case
  no snapshot can see, main moving between the read and the merge. Both paths write the **same
  marker**, so the three-conflict escalation counts them together — three conflicts on one
  ticket is a sequencing problem whichever half noticed them, and two counters would each stop
  at two.
- **Second bounce from reconciliation on the same ticket → `Blocked`**, not rework again. Two
  failures to land the same scope is a design problem, not an implementation one, and the
  author will usually route it to `Ready for design`. Counted the same way: reconciliation's bounce
  comments carry a marker.
- **A ticket in `Merged` past the deploy timeout → `Blocked`,** which is how a failed production
  build becomes visible rather than a ticket that quietly stops moving. Recovering it is a
  redeploy, not a state change, which is why the author decides where it goes next.

**Stale claims.** Claim is a state transition (§6), and the run that made it can die — runner
loss, timeout, an API outage mid-session. Nothing on the ticket distinguishes "the dev agent,
now" from "the dev agent, until it crashed an hour ago," so the control plane checks on every
poll: a ticket in an agent-owned state (`Designing`, `In progress`, `Reworking`, `Reconciling`,
the boundary ticket's `In progress`) whose dispatched run is no longer live — past a grace
period set in the project config — moves to `Blocked` with a comment naming the state and the
dead run. Detected rather than waited out because at one dev agent a stuck claim halts the
entire queue. Recovery is the normal `Blocked` rule — the author chooses the state — and for
the boundary ticket that is exactly the resume path §10 already defines.

---

## 13. Automation surface

**Runtime: GitHub Actions.** Agent runs are the only real compute — minutes long, needing a
checkout and a toolchain — and Actions already has the repo, the secrets and per-run logs. The
control plane therefore holds no compute at all.

**Control plane: an Actions workflow, metronomed from outside.** The control-plane logic is a
workflow that polls the tracker for tickets in trigger states, polls the host for deploys, and
dispatches. The state machine already lives in the tracker, so there is nothing else to
persist. It is deliberately *not* triggered by GitHub's own `schedule:` — under load those
crons fire 5–15 minutes late, a ticket crosses six or more polled transitions on its way to
`Done`, and the jitter compounds to an hour of dead time on a lifecycle that is otherwise
minutes of compute. Instead a **Cloudflare Worker** — punctual to seconds, holding one
fine-scoped GitHub token that can fire `workflow_dispatch` and nothing else — pings the workflow
on tracker events and, failing those, hourly. The Worker contains no pipeline logic at all: it is
a metronome that also receives, and keeping it dumb is what keeps the control plane in one place.

**The control-plane workflow declares a single `concurrency` group, `cancel-in-progress:
false`.** A delayed cron run and the next one can otherwise overlap, and two dispatchers
running at once quietly defeats every single-agent guarantee downstream — the one-dev-agent
invariant is only as strong as one-dispatcher.

**Triggers**, in order of the loop:

| Condition | Action |
|---|---|
| State → `Ready for design` | Design agent run; its claim writes `Designing` |
| State → `Design review` | Notify author. No agent action. |
| State → `Ready for dev` / `Ready for rework` | Enqueue; dispatch if the dev agent is idle |
| State → `In progress` / `Reworking` | Dev agent run |
| CI green on a non-draft PR | State → `Reconciling`; reconcile agent run |
| CI red on a non-draft PR | Failure comment (fixed marker); first on the branch → `Ready for rework`, second → `Blocked` (§12) |
| Reconcile pass | Merge; state → `Merged` |
| Reconcile cannot tell | Merge; state → `Merged` + `needs-review` label |
| Reconcile fail | State → `Ready for rework` |
| Deployment active, SHA ≥ merge SHA | Post-deploy check → `Done`, or `Blocked` if it failed or carries `needs-review` |
| Last milestone ticket resolves | Pause queue; create the boundary ticket (§10) |
| Boundary ticket in `Todo`, no live-suite result | Dispatch the live-suite run, once; its result marker ends the loop (§10) |
| Boundary ticket → `In progress` | Boundary agent run: archive, debt scan, grooming |
| Boundary ticket → `Done` | Resume queue |
| Agent-owned state, dispatched run dead past grace period | State → `Blocked`, comment naming the dead run (§12) |
| Guarded transition violated (§9) | Revert to prior state, comment naming the rule |

Dispatch order is the precedence rule (§7): urgent, then furthest along, then oldest.

**The CI hops don't wait for the poll.** CI green and CI red are GitHub-native events, so the
project stub also triggers on `workflow_run` completion and performs those two transitions —
into `Reconciling`, or the failure comment and `Ready for rework` — immediately. Everything
with no event to subscribe to stays on the polled loop: tracker state changes, deploy
detection, stale claims, deploy timeouts.

**Deploy detection:** poll the platform's deployments API for an active deployment and compare
its commit against the merge commit. `≥` rather than `==` because several merges may land in one
build; a ticket whose merge SHA is an ancestor of a successful active deployment is deployed.
The `≥` is shorthand for *is an ancestor of* — a compare-API call, not SHA arithmetic.

**Kill switch:** a single flag halting dispatch without revoking credentials or leaving a ticket
mid-claim. In-flight runs finish; nothing new starts.

It gates the sweep *job*, not only the planning inside it. The binary already refuses when the
flag is set, which is correct and is not free: Actions bills each job rounded up to the minute,
so a parked project on the hourly beat spends around 730 minutes a month producing no actions.
A job whose condition is false never starts and costs nothing, which is what makes "parked"
mean parked — a rehearsal repo between rehearsals, or a project paused for a week, stops
consuming an allowance the active project needs. The flag is still passed into the binary as
well, so a hand-dispatched run refuses for the same reason rather than relying on the workflow
alone to guard it.

**A sweep is ordered furthest-down-the-pipeline first, and folds its own decisions forward.**
Corrections and reverts still run before everything, so no later rule acts on a state the sweep
is about to undo. After that the pass takes the tickets nearest the end of the pipeline first,
and each planned transition is applied to a working copy that the remaining rules read.

Without the fold, a pass reasons entirely about the world as it stood when the snapshot was
built, so every hop needs its own beat to become visible — and the effects are not merely slow.
A ticket whose deploy had landed still counted as `Merged` for the whole pass, holding its
mutex labels against a queued ticket that the same pass then dispatched into a pickup assertion
guaranteed to refuse it. Resolution before consumption is the whole ordering: retire what is
finished, then decide what may start.

`Sweep` stays a pure function — no I/O, same input to same output — because the fold happens on
a copy and the caller's snapshot is never written. What makes it safe to act on a prediction is
downstream: `Execute` applies actions in slice order and stops at the first error, so an action
that assumed a transition which then failed to land never runs. **One transition per ticket per
pass** remains, now for a second reason: a ticket's new state is visible to later rules, and
they must not judge a state this pass has just produced.

**The pipeline records its own writes, because the tracker cannot say who made them.** Every
§9 revert turns on telling the author's moves from the pipeline's, and a solo workspace has no
way to do it: the harness authenticates with the author's key, so every write it makes arrives
stamped with the author's identity. Resolving a role from that identity answered "control
plane" for the human's moves too — the one role the revert rules trust — so every invariant in
this document was off, silently, for as long as the two shared an id. The identity fix is a
tracker seat per role per month, to encode something the pipeline already knows about itself.

So the harness writes each move down **before making it**, into a store it owns, and the sweep
compares that record against where the tracker says the ticket is. Agreement means the pipeline
made the last move, and the record carries the edge and the role; disagreement means somebody
else did, from where the pipeline left it to where it now is. **No record means not judged** —
every ticket predating the store is absent from it, and reading absence as "a human did this"
would revert an entire backlog on the first sweep.

Recorded is not excused: an agent's move is recorded under the agent's role and judged like any
other, because a design pass promoting straight past `Design review` is precisely what §9
exists to catch. Write-ahead is what makes it safe — a record for a move that then failed
matches nothing, while a move that landed unrecorded would read as a human's and be reverted,
re-made, and reverted again. Writes fail closed (no record, no move; the sweep is convergent
and retries) and reads fail loud (an unreachable store is not an empty one, and an empty one
turns the invariants off).

**Run summaries carry what a human is meant to read.** Actions renders `$GITHUB_STEP_SUMMARY`
on the run page itself, above the job list, so what goes there has been read by the time
somebody has found the run — where a log is something you go and open. The commands that write
one are the ones whose output is a report for a person and is otherwise buried a step deep:
`order`, `preflight`, `setup`, `audit` when it is red, `sweep`, and the rehearsal's reset, seed
and check.

**The agent commands deliberately write none**, which is the load-bearing half. An agent's
outcome belongs on the ticket, because the tracker is the record (§9) and every state the
pipeline acts on is read back from it. A summary restating that would be a second rendering,
authoritative-looking and not authoritative, and the first time the two disagreed somebody
would have to work out which one lied. The agent's run log stays what it is — a debugging
artifact for when the ticket does not explain itself. A failed run is not an exception: the
abort path puts the failure on the ticket too. The live-suite verdict is excluded for the same
reason, since it lands on the boundary ticket where the author is already looking.

**The tracker hops are webhook-driven** (the escalation this section used to hold in reserve,
taken). The same Worker receives Linear webhooks and dispatches on the event, so a state change
reaches the sweep in seconds rather than averaging half the poll interval. It gained exactly one
capability to do it — verify an HMAC signature, route by project id, POST — and still reads no
state name, no label, no ticket field. Keeping it dumb is what keeps the control plane in one
place, and that rule survives the escalation.

The endpoint is public and holds a token that can start workflows in every project repo, so the
signature check is the door: unsigned bodies, bodies altered after signing, and timestamps outside
a one-minute replay window are dropped before anything is dispatched. A Worker deployed with no
signing secret rejects *everything* rather than accepting everything — the failure mode is a
pipeline that runs at the cron's pace, not one that anyone can drive.

**The polled sweep survives, hourly**, because two conditions have nothing to subscribe to: stale
claims and deploy timeouts are elapsed-time judgments. Both sit behind grace periods of tens of
minutes (§12), so an hourly beat finds them well within tolerance, and it doubles as the backstop
for any webhook that is dropped.

**The agents act as a user, not as Actions.** Everything an agent does with the in-workflow
`GITHUB_TOKEN` appears to work, and silently raises no events: GitHub starts no workflow run from
an event that token created. CI never runs on the branch, the preview never builds, the deploy is
never recorded — and a PR opened by `github-actions[bot]` additionally holds its checks for a
human's approval, a touchpoint §0 does not budget for. Four symptoms, one cause, each looking
like its own bug. So the project stubs hand the agents a scoped user token instead, and the
harness warns when they are running without one.

**The GitHub-side hops arrive by webhook too, and are a backstop rather than the mechanism.** CI green and
deploy detection were originally left to `workflow_run` and `deployment_status` triggers on the
project stub. Those do not fire for the pipeline: GitHub will not start a workflow run from an
event created with `GITHUB_TOKEN` — `workflow_dispatch` and `repository_dispatch` are the only
exceptions — and every agent pushes, merges and records deploys with exactly that token. The
triggers work for a human's commits and never for an agent's, which is the only case they exist
for, so the gap reads as a pipeline that has merely gone slow. Webhook *delivery* is not workflow
triggering, so the same Worker receives them, verified against GitHub's own signature.

One filter there is load-bearing rather than an optimisation: only the project's CI workflow
completing may wake a sweep. A sweep run completing is itself a `workflow_run` event, so waking on
any completed run would have each sweep dispatch the next, indefinitely, against a token that can
start workflows in every project repo.

**If even that is too slow,** Durable Objects are on the free plan with the SQLite backend, and one
Durable Object per project *is* the single-dev-agent mutex, serialized by construction. Watch the
free-plan cap of three cron triggers per Worker, and the absence of retries or failure alerting on
them.

---

## 14. Open items

- **`needs-review` and `needs-setup` tickets have no timeout.** They sit in `Blocked` until a
  milestone boundary or until a human does the thing — potentially weeks, deployed the whole
  time in the `needs-review` case. Deliberate, and sound only while there is one author. Note
  that `needs-setup` is the flavor most likely to want alerting later: it is the one where the
  wait is not the author reviewing at their own pace but the author not yet knowing they were
  asked. A timeout was considered and deliberately not built: the only thing a timeout could
  *do* is move the ticket, and the only honest destination is `Todo` — which discards the
  claim, the branch context and the reason, to buy a nudge. Until someone is waiting on the
  author who isn't the author, the assumption is that they are prompt. Revisit this at the
  same time as alerting, not before; they are the same feature seen from two ends.
- **Semantic conflict in dev-owned files is covered to the extent the system map is honest.**
  System labels (§6) extend the mutex and the re-evaluation machinery to declared structure;
  what remains uncovered is files owned by no system — the router, the manifests — which are
  named in sketches and caught only textually by git. The mutex's quality is the partition's
  quality: revisit the system map when one label starts serializing unrelated work.
- **Staging.** With one environment, the post-deploy check runs against production. The intended
  eventual shape is staging with a manual test gate, which would sit between `Reconciling` and
  `Merged`.
- **Tracker plan limits.** Confirm webhook and API access on whatever plan the project is on
  before the control plane assumes either; free tiers vary and change.
- **A stalled boundary halts the queue silently.** A *crashed* boundary run is now caught by
  the stale-claim check (§12), which moves the ticket to `Blocked`. What remains open is the
  human half: a boundary ticket sitting in `Blocked` or `Todo` is the signal, but only if
  somebody looks — there is no alerting anywhere in this design. The cheapest fix is a
  scheduled check that pings when any ticket has been in `Blocked` past a threshold; it is
  deliberately not specified here.
- **Concurrent dev agents remain a deliberate later decision, but the distance shrank.** The
  mutex now covers declared structure (§6), the run-per-ticket correlation is an owner
  dimension in practice, and the single-dispatcher control plane (§13) makes
  state-transition-as-claim safe for a second dev whose ticket shares no mutex label with
  in-flight work. What flipping the switch still requires: a system map proven against months
  of real sketches, and the author able to absorb the review throughput two agents produce —
  the bottleneck moves to the human, which is the correct failure mode.

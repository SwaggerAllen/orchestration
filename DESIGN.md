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

   **A delta may widen the ticket, and the design pass acts on it.** Immutability is what makes
   the thread the only channel an amendment has: an author who accepts new scope has nowhere to
   put it but a comment. A pass that reads the description as the whole scope is therefore a
   pass that cannot be amended, and the ticket comes back a second time for work agreed the
   first. Design is where this lands, because design is the pass that can fold a delta into the
   sketch — dev implements the sketch, and reconciliation only judges against the thread it is
   already given.

   The distinction is *who decided*, not *where it is written*. A delta the **author** accepted
   in the thread is scope. Scope a pass believes the work needs, that nobody has agreed, is a
   push-back (§2.7) and stays one — the comment channel widens what the author has widened, and
   confers nothing on an agent's own reading.
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
| `Ready for design` | The queue. Scope is the **description**. | author, control plane (§8) |
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

**The prerequisite park:** a design pass whose scope depends on something that is not on `main`
and is not the ticket's to write reports `prerequisite`, and the harness parks the ticket in
`Blocked` under the `prerequisite` label (§12) with the pass's summary naming what it is
waiting on. The author lands the other change and returns the ticket to `Ready for design`.

It is a third outcome rather than a use of the two that existed, because neither is true:
`artifacts` claims there is something to approve and `decisionless` claims the scope was
examined and needs no decision. Without it such a pass had no legal outcome at all — the run
died, and a dead run lands in `Blocked` under `failed`, which says the harness broke. Catapult's
`ORC-157` is the measurement: the same ticket hit it twice, and the follow-up filed to record the
second occurrence was cancelled, so it would have gone unrecorded again.

A `prerequisite` pass declares no screens and no systems, and the harness refuses the outcome if
it does. This is where it differs from the decisionless exception, which still declares its
systems: a decisionless pass read the scope and knows what it will touch, while a prerequisite
pass is reporting that it could not read the scope, so its touch list is a guess. `Blocked` is
in flight as far as the mutex is concerned (§6), so a guessed label there locks every other
ticket naming that system out until a human moves this one.

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
in the queue. `canceled` and `duplicate` read as `Canceled`, `completed` as `Done`, `triage` is
skipped (§10).

**`duplicate` is enumerated because Linear makes it a category of its own**, not a flavour of
`canceled`, and it is the category the incident below was actually about — so a rule reading
only `canceled` and `completed` left the state that caused it still fatal. It reads as
`Canceled` rather than `Done`: a duplicate was discarded, not finished, and the two are read
apart by the revert rules, so reporting discarded work as completed would be a lie the board
carries.

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

<!-- pipeline:list id=design-artifacts -->
- **A stateless function component** — presentational, hardcoded assigns, daisyUI classes. No
  socket, no live data.
- **A `.story.exs`** with one variation per state, each carrying its description. A state name
  IS a storybook variation name — name it as one (`cap_reached`, not "the cap-reached state").
- **A narrative doc** (`screens/<name>.md`) for rules, standing decisions and the argument,
  with **no state sections at all**, and a front-matter **file map** naming the screen's
  component module and story file — the map is what lets CI audit per screen rather than per
  "some design path" (§9). The state list lives in exactly one place, the stories, so nothing
  can drift.
<!-- /pipeline:list -->

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

**The design prompt carries an index of what those docs already decided.** Every heading and
every top-level bullet's lead from `systems/*.md` and `screens/*.md`, selected by the ticket's
scope with the rule the non-asks selection uses, with the unselected docs named and counted so
a pass reaching further knows they are there. Catapult's `ORC-126` is the measurement: a design
pass spent a full run re-verifying "no assignee or role-holder projection exists", which
`systems/dashboard.md` and `screens/my-queue.md` already stated in near-identical words, with
the same evidence and the same conclusion. Nothing put those in front of it, and a re-derived
decision arrives at Design review looking like new work.

An index rather than the text, and that is a measurement too: the `## Standing decisions`
sections in Catapult's system docs come to 362KB, one of them 80KB on its own, against 25.6KB
for the whole index. What is inlined is enough to know a decision exists and which file states
it; the docs are in the checkout. So this is the non-asks argument — a prompt whose most
important input is "go read this file" is a prompt whose most important input is optional —
answered at the size the input actually is.

It indexes headings *and* bullet leads because both are how these docs carry a decision, read
off the tree rather than assumed: all of Catapult's system docs put theirs in a
`## Standing decisions` bullet list, and none of its screen docs do — a screen doc's headings
*are* its decisions. Needing no rule about which heading counts is the part worth having: it is
a prompt input rather than a gate, so nothing here can report a decision the doc does not
contain, and the worst a bad entry costs is a line.

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

**Most refusals do not belong in it.** A refusal about one system or one screen goes in that
doc, beside the decision it is the negative half of — where the pass that could violate it is
already reading, and where it cannot drift from the positive rule it qualifies. The distinction
is placement, not category: this file is inlined into every scoped prompt, while a system doc
is read on the way to changing that system, which is the cheaper and better-aimed moment.

**And it is written differently there, because "never deleted" does not travel with it.** In
the file a refusal is an entry, kept because its disappearance is invisible. In an owning doc
it is the *reason attached to a rule* — "X is the rule, because Y turned out to be false" —
and superseding that rule means rewriting the sentence, not appending a correction beside it.
The asymmetry is what keeps the docs bounded: a rule's reason is one entry per rule, while
alternatives a pass passed over are drawn from an open-ended set, so a doc that records those
grows with the number of reviews rather than the number of rules it states. Measured on
Catapult's ORC-115, where the prompt stated "never delete" ahead of the placement guidance and
a pass reasonably read it as governing both: eight passages narrating prior passes across five
design-owned docs, two of them section headings numbering the review round.

**Two kinds have no such home, and they are what the file is for.** Refusals every pass must
see, and refusals spanning systems — which a per-doc home could serve only by copying into each
one, and a copy is drift with extra steps. The tell for the second kind is two docs citing a
rule that neither of them states. A `scope:` naming exactly one doc is therefore the sign that
an entry is in the wrong place.

**Check the owning doc before writing an entry.** If it already refuses this, that is the
record and a second copy is the drift. An argument that is genuinely new belongs in that doc's
own bullet rather than in a parallel entry beside it.

**A reversed decision is not a refusal**, and its record belongs where the reversal was argued.
A struck-through entry left in the file makes every scoped pass read what is no longer true —
which is the failure "never delete" exists to prevent, arrived at from the other direction.

**A citation that names a section is checked; the rest are not, and the difference is stated so
a clean run is not read as more than it is.** The audit resolves a reference that names its
document by path and a section by number — `docs/v5-design-decisions.md §7.8` against `### 7.8`
in that file — across the whole tree rather than `docs/**`. Whole-tree is the load-bearing half:
a manual sweep of `docs/`, `systems/` and `bundles/` on Catapult reported clean while 22
citations in `lib/`, `components/` and `test/` were dangling, two of them inside error-message
strings, so the project's own audit was telling developers to read an entry that no longer
existed. Text rots without anyone touching it, which is why this is a recurring check and not a
rule a prompt can carry.

**Whole-tree stops at a checkout of another repository.** The workspace holds one: the harness
checks this repo out at `.pipeline` inside the project, and the sweep read its source —
including the citation checker's own test fixtures, which dangle on purpose, since fixtures
that resolved would test nothing. No version of the pipeline repo passes that check, so it was
not a citation to fix: every ticket branch in every project failed on 15 violations, none of
them the project's. A directory carrying its own `.git` is therefore skipped, and skipped *by
that* rather than by the name `.pipeline`, which is one action's `path:` input and would take
the fix with it if it changed. The skip is printed, because a sweep quietly covering less than
the tree is this audit's own worst failure mode.

A citation naming its document by a project shorthand — `v5 §7.8`, `conventions §2` —
outnumbers the explicit form by more than twenty to one, and resolves through
`citationShorthands` in `pipeline.config.json`. One form stays outside the check: a prose
reference carries no section and is not decidable at all. So the audit's citation line is a
statement about two forms, never about the documents being sound.

**Prose that quotes a bad citation is read as making one, and there is no escape comment.** A
document warning against a citation has to write it down to name it, and the adjacency is all
the check reads — two of these sat in one project's tree, both in sentences explaining the very
mistake they were reported for. The project rewrites the sentence to describe the mistake
rather than spell it, which is the deliberate answer and not a workaround waiting on a feature:
an escape comment here would be a suppression on a check whose whole value is that it cannot be
argued with, and the one it copied — `# catapult:allow` — is honest only because an unused tag
is itself reported. Nothing here could report an unused escape, because a suppressed citation
looks exactly like a resolved one.

**The map is a whitelist, and lookup is case-sensitive.** A token before a `§` names a document
only if the project declared it. That is what keeps prose out: 285 bare back-references across
120 ordinary English words — `and §3`, `see §2`, `per §7` — sit before a `§` in one project's
tree, and a resolver reading the preceding word as a document name reports the corpus rather
than its defects. Case-sensitivity is the same guard one level down: folding case resolves "the
design §4 said" against a `DESIGN` entry, and a whitelist whose keys silently widen to every
capitalisation is not a whitelist. A project spelling one shorthand two ways lists both.

A section number may carry a part letter — `§A.1.4`, `§B.3.2` — because a cited document may
number that way. A citation resolves against a heading's own number or any prefix of it, so
`§7` is satisfied by `§7.8`; the reverse does not hold, and `§7.12.1` is dangling when the
document stops at `§7.12`.

Each entry carries either a `path`, the project-relative document the shorthand names, or an
`unchecked` string recording why no path in this repository can resolve it — another
repository's docs, a licence text. Behaviourally `unchecked` matches leaving the shorthand out
of the table; what it buys is the record, so the next pass to notice a shorthand going
unchecked does not invent a path for it and turn correct citations red.

**A shorthand no citation names is proposed for pruning, never gated.** A whitelist that only
grows stops describing the corpus it was written for: an entry outlives the last citation
needing it in silence, and the next pass to read the map takes it as evidence that the
shorthand is in use. Catapult's `# catapult:allow` escape prunes itself exactly this way — a
tag covering no violation is reported — and the reasoning carries over. Who can act is where
the two part, and it is why this one cannot gate: that escape sits in the file the ticket is
already editing, while this map sits in `pipeline.config.json`, author-owned (§5). A gate would
leave a ticket red for a config state it did not create, with every file it needed closed to
it — the dead end `author-only` exists to prevent (§8), reached by accident instead of by
label. Naming a shorthand is not citing it: the check reads the document half of a citation
through the same grammar as the sweep, so a key written down in prose about the map does not
excuse itself, and a check that counted such mentions would report nothing for exactly the
entries worth pruning. `unchecked` entries count, because a record nothing cites has the same
problem as a path nothing cites. A shorthand whose citations all dangle is in use: pruning it
would delete the entry that makes those citations checkable and turn a reported defect into a
silent one.

**The field is declared here before a project config may carry it, which is the opposite of the
rule for a new protocol state.** `config.Load` rejects unknown fields — at every level, not only
the top — so a config naming `citationShorthands` against a binary without it fails to load, and
because validation precedes everything the whole sweep stops rather than one check degrading. A
new protocol state runs the other way: validation requires the complete state mapping, so the
project config must gain the line first. One strictness read from two ends, and which side moves
first depends on which end the change trips.

**What the docs claim about the tree is a proposal, never a gate.** A pass records the documents
its own change falsified as `project` findings, which the boundary aggregates and the author
adjudicates in one sitting — collected at the ticket because that is where the context is
freshest, judged at the boundary because that is where they can be judged together. It cannot
gate: of the "not built"-shaped claims checked across one project's system docs, three were
stale and three were correct descriptions of deliberate gaps, with no textual difference between
them. A gate there fails on true statements, and the fix a pass reaches for under a red build is
to delete the true statement or add a suppression — a silent gate re-blinding itself to the next
finding. A candidate is therefore verified against the tree before anything is removed, and the
document under suspicion is not evidence for its own claim.

**Relocating an entry is the author's move, not a pass's.** The citations that point at it move
with it, and a pass that shuffled entries between documents would break references it cannot
see. A pass that believes an entry is in the wrong place says so in its summary.

The harness inlines it rather than telling a pass where to find it, for the same reason it
inlines the scope: a prompt whose most important input is "go read this file" is a prompt whose
most important input is optional. The section says which of four things happened: here it is,
the file records none yet, none of them are scoped to this ticket, or the read failed. They
look identical in an empty section and license very different confidence — only one of them is
permission. Proposing a recorded non-ask isn't forbidden, but do it
knowing you are arguing against a recorded decision, and say so in the issue.

**Every pass gets it, and each gets only the entries that bind it.** Both halves of that
changed together, and neither works alone.

Every pass, because the document was design's and boundary's alone on the reading that it
constrains what gets *proposed*. But "no client-side validation on the cap form" binds whoever
writes the validation, and that is the dev pass — which was never told. Reconciliation is the
last gate before the merge (§11), so it is the last chance to catch a refusal contradicted in
passing.

Only what binds them, because the document grows by rule. "Never delete" is right and stays —
a refusal that quietly disappears is one the pipeline proposes again — but it means the file
only ever gets longer, and most entries are about one screen or one system. Measured on
Catapult's ORC-84: 83468 bytes, inside a design prompt that was 92825 bytes before it reached
the ticket, from three files, one of which was 65% of the total. That is what made giving it to
every pass affordable rather than three times worse.

**So an entry carries a scope: a list of the screen and system labels it spans, or
`universal`.** A list rather than a per-doc home *for these entries specifically*, and the two
rules fit together rather than competing: a refusal about one doc goes in that doc, and a
refusal spanning several has no such home — a per-doc version would be either marked global,
which puts it back in every prompt, or copied into each doc, which is drift with extra steps. A
list has one representation for exactly that case, and it is the only case left in the file
besides the universal ones.

An entry is selected when it is universal, when the ticket carries one of its labels, or when
one of those labels' names appears in the ticket's own words. That last clause is load-bearing
rather than a convenience: **a first design pass carries no mutex labels at all**, because the
design pass is what creates them (§6), so label matching alone would show design nothing but
the universal set — and design is the pass the document is written for. Matching a name in the
ticket's words is what the class audit already does (§9), for the same reason: at the moment
the question is asked, the words are all there is.

**And a pass can ask again.** Selection runs once, when the prompt is assembled, which is
accurate for dev and reconciliation and is not for the pass this document is written for: a
first design pass carries no labels, and its real scope is not known until it declares
`screens` and `systems` in its outcome — one step after the selection needed it. So the harness
offers `pipeline non-asks --for <scope>`, which the prompt names, and which answers the same
question through the same selection so the two cannot disagree. It reads one local file and
needs no credential, so it is safe at any point in a pass.

A command rather than "the file is in your checkout, go and read it". The argument for inlining
the document at all is that a prompt whose most important input is a pointer is a prompt whose
most important input is optional, and an escape hatch phrased as an invitation reintroduces
exactly what the inlining was written against. A specific thing to run is an instruction.

**Selection fails open.** An entry with no scope is universal, and a file with no headings at
all is one universal entry — which is also the migration path, since every project's existing
flat file keeps behaving exactly as it does now until somebody rewrites it. Over-selecting
costs a pass one paragraph it did not need; under-selecting hides a refusal from the pass that
would have broken it, and the author catches it at Design review having been told it was
checked. And a selected section **says it is a selection**, with the count and the path, because
a filtered list that does not announce itself reads as the whole document — after which "the
non-asks don't mention it" is a conclusion the pass had no grounds for.

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

**The ownership table is two-way.** Dev's side is the amendment right above. Design's side is
the same sentence read backwards: a design pass commits *only* within the project's
`designOwnedPaths`, and everything else — the code that implements what it decided included —
is dev's. Both halves are needed, and only one of them existed for a while: dev's prompt named
the config key, while design's named a hardcoded list of file extensions that assumed every
project's design output is a component template and a story file. One rule stated twice, in two
forms, and the weaker form bound the role it mattered most for. Catapult's ORC-84 is what that
costs — a design pass that committed six implementation modules and several thousand lines of
bundled content alongside its docs, with nothing in its prompt drawing the line and nothing
downstream noticing. The design in that pass was right; an unbounded role simply keeps going.
Enforced at both ends now (§9): the harness holds it when the design pass finishes, and CI holds
it on the PR.

**Widening the boundary is a proposal, not an edit.** A design pass that believes work outside
`designOwnedPaths` is genuinely design's says so in its summary and stops. It must not reach
for the config: `pipeline.config.json` is author-only by the rows above, so the repair that
looks obvious from inside the run is the one that takes the run down with it.

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

**A design pass's touch list is the whole label set, not an addition to it.** The declared
labels are attached and the mutex labels the ticket carries that the pass did *not* declare are
released. They were add-only until ORC-158, and narrowing is routine — the author sends a
ticket back from `Design review` and the next pass draws less — so the label the first pass
took stayed on the ticket holding the mutex against every other ticket naming that system, with
no legal way for any pass to clear it. Catapult's `ORC-141` sat on `system:delivery` that way;
only a direct tracker write got it off.

**A label the branch's own files require is never released**, whatever the touch list says. CI
derives the labels it demands from the diff (§9), so releasing one the diff needs fails the
next push — and the narrowing pass cannot know what an earlier pass or a dev round already put
on the branch. Design's touch list is a prediction and the branch is evidence; evidence wins,
and the kept label is reported on the ticket rather than dropped silently, because a pass and a
branch that disagree about a ticket's scope is a thing for a person to look at.

The release is checked against everything the branch changes against main, which is not the
per-pass file list the ownership audit (§5) reads: that one is scoped to the run so a stray is
billed to the pass that wrote it, while this asks whether the *branch* is done with a system.
An absent list releases nothing and says so — a wiring gap must not read as permission — and a
`prerequisite` pass (§3) declares no touch list at all, so it releases nothing either: a pass
that could not read its scope does not get to decide the mutex.

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

**Agent singularity is enforced twice, at two different moments, and both halves are load
bearing.** Everything that asks "is this agent kind busy" reads the agent-run list, and a run
does not appear there the instant it is dispatched — measured on Catapult at about ninety
seconds between the dispatch and the proof it happened. Any sweep landing inside that window
saw an idle agent and dispatched again: on `ORC-45` that was two boundary agents running one
ticket to completion, twenty-two minutes of model spend and four tickets filed for two
findings.

- **The pickup assertion** refuses the loser at claim, in seconds rather than at completion.
  It reads a snapshot the run already holds, so it works on any project.
- **The dispatch reservation** stops the second dispatch being made at all. The sweep claims
  the agent kind in the move store (§13) *before* it calls the host, so the record exists
  before the next sweep can read it. It is a compare-and-set on one Durable Object per
  project, which is serialized by construction.

Neither replaces the other. The reservation is a write to a store a project may not have
configured and that can be unreachable; losing it costs a duplicate dispatch, which the pickup
assertion then refuses cheaply. Fail-closed on the write and the dispatch does not happen — the
sweep is convergent, so declining costs a beat.

**The reservation expires.** A dispatched job can die before it ever claims — a lost runner, a
workflow that will not parse — and a lock nobody releases is an agent kind that never runs
again. It is released at claim rather than at the end of the run: claim is unambiguously past
the window the reservation covers, and holding it for the whole run would force the TTL up,
which is the one direction that hurts. Release is holder-scoped, so a run that just lost the
race cannot hand back the winner's lock.

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
| `Checks` | Nothing. The flag travels with the ticket into `Reconciling`, which is the thread that answers it. | — |
| `Reconciling` | **Answered as a second question, alongside the verdict.** `holds` clears the flag and the ticket proceeds on its outcome; `bites` sends it to `Ready for rework` whatever the outcome said, with the report as the scope. | reconcile |
| `Merged` | Deferred — the change is merged and past recall. Becomes a finding → Triage. | — |
| `Done` / `Canceled` | No action. If the collision matters it is a new finding → Triage. | — |

**A `re-evaluate` label is cleared only by the thread owning the flagged ticket's current
state** — never by the thread that noticed. Otherwise the noticing thread clears its own flag
and nothing is re-evaluated.

**`Checks` is the one state with no thread of its own, and the answer is to carry the question
rather than to stop.** `Checks` has no agent — nothing is dispatched to a ticket sitting there —
so a flag arriving in that state had nobody to evaluate it. Holding promotion was tried and is a
dead end for the same reason: it stops the ticket at a gate nobody is standing at, and a green
one waits on a human indefinitely. Which is what re-evaluation is not supposed to need.

So the flag travels. **Reconciliation is the thread that answers it**, and it is the right one
twice over: it owns the state the ticket lands in, and it is the last thread before the merge,
so nothing has been given up by letting the ticket through. It is also already reading the diff
against the argument, which is most of the work — asking it one more question is cheap.

**The collision is a separate axis from the verdict, not a fourth outcome.** The outcome asks
whether the diff says what the argument asked for; the collision asks whether what changed
around it since means it no longer does. Those are independent: a clean `pass` whose ground has
moved must not merge, and a diff that drifted for unrelated reasons is still a `fail`. One field
carrying both would make the run choose which answer to throw away.

`holds` clears the flag and the ticket proceeds on its outcome. `bites` sends it to `Ready for
rework` whatever the outcome said, with the report as the scope — and that scope has to say
plainly that the diff was not wrong when it was written, or the rework agent re-litigates a
design nothing questioned (the same care §2.4's conflict bounce takes). Either way the flag is
cleared, because the question has been answered and a flag left behind would send the ticket to
a design re-read that judges the same collision twice.

**An unanswered collision bounces rather than merging.** The two ways to be wrong are not equal:
a bounce costs a rework pass on work that was fine, and a merge under a collision nobody judged
is the thing the flag exists to prevent, past recall the moment it lands. Both bounces carry the
reconcile-bounce marker, so the second-bounce escalation counts them together (§12) — two
failures to land one scope is a sequencing problem for the author whichever half noticed it.

**All `re-evaluate` labels must be clear before a milestone can complete** (§10).

---

## 8. Labels and priority

| Label | Meaning |
|---|---|
| `frontend` / `backend` | Where the work happens. Not a scoping constraint — one ticket may contain both. |
| `tech-debt` | Work on the shape of the code rather than what it does. A boundary proposal of kind `debt` files under it (§13). Survives the label admission test because debt doesn't stop being debt when it changes hands; it gets paid. |
| `bug` | Defect: something that does not do what it says, as against `tech-debt`'s shape of the code. Runs the normal pipeline; `Urgent` is what makes it preempt. Filed by the author, or proposed by the boundary (§10). |
| `design-inbox` | Provenance: this came from the design agent, or from a boundary proposal of kind `design` (§13). The question you'll want answered later when something looks odd. |
| `screen:<name>` | The design half of the mutex (§6). |
| `system:<name>` | The structural half of the mutex (§6). Declared by the sketch; mapped to paths in the project config. |
| `re-evaluate` | Unresolved collision (§7). |
| `needs-review` | Reconciliation couldn't tell — the `cannot-tell` verdict (§11, §13). Deployed, clean, awaiting the author's eye. |
| `needs-setup` | Parked on a human doing something the automation can't — a secret, an API, an account. Written by `abort --reason needs-setup` (§12, §13). Blocked, but not broken. |
| `scope-satisfied` | The run found the whole scope already on `main` and changed nothing. Written by `abort --reason scope-satisfied` (§12, §13). Almost always a duplicate to cancel. |
| `pushback` | The design can't be built as drawn. Written by `abort --reason pushback` (§2.7, §13). Parked for the author to redesign or rescope. |
| `prerequisite` | The scope depends on something not on `main` and not this ticket's to write. Written by `abort --reason prerequisite` (§3, §12, §13), and the outcome a design pass reports it with. Land the other change, then return the ticket to its queue. |
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

### Promoting into the design queue

The report says what to start next; the control plane starts it. The sweep moves a ticket from
`Todo` to `Ready for design` when all of the following hold.

- **`Ready for design` is empty.** One at a time, so that promotion order *is* execution order.
  The design agent is singular (§6), so a deeper queue buys no throughput — what it costs is the
  ordering. Two tickets sitting in that queue are separated by the precedence rule alone, which
  at equal state falls through to age, and age is not the layering: a ticket freed later by a
  merge can be older than one that has been startable all along, and would go first. A queue one
  deep cannot disagree with the report.

  `Designing` is not the queue: a ticket the design agent has claimed has left it, and the next
  may take its place. And an `author-only` ticket parked in the queue does not count as occupying
  it, because the dispatcher skips those too (§5) — it would sit there for as long as the author
  left it, and counting it would stop every promotion on the project for that whole time.
- **It carries the current milestone.** Not another milestone — §2.9 works them in sequence — and
  not none. A milestone-less ticket in `Todo` is startable but uncommitted, and assigning the
  milestone is the commitment (§10), so promoting one commits work as a side effect. It does that
  at the exact moment the two cases are indistinguishable: a proposal accepted out of Triage with
  the assignment still lagging sits in `Todo` with no milestone, and so does a ticket left
  uncommitted on purpose. The report names both and says it cannot tell them apart; the
  automation moves neither.
- **Every blocker has reached `Merged`.** `Merged`, `Done` and `Canceled` all satisfy it; nothing
  else does, `Blocked` included — a ticket that merged and then failed its post-deploy check has
  its work on `main` and a human owing a judgment on it, and one of the judgments available is a
  revert. `Merged` rather than `Done` because a design pass reads `main`, and a merged blocker's
  work is on `main` — what remains of that ticket's life is the deploy and the post-deploy check,
  neither of which changes anything the pass would read. **The relaxation is design's alone.** Dev
  pickup keeps `Resolved`, because code built on top of a blocker that fails its check would have
  to be re-examined; a design that described it would only have to be re-read.
- **The queue is not paused, or the ticket is marked as blocking the boundary ticket.** The pause
  exists so a milestone's scope stops changing while it is being audited, and a design pass is the
  one thing that adds to that scope: new artifacts, new mutex labels, in the middle of the archive
  and the debt scan. A ticket blocking the boundary is the exception, and it mirrors the dev drain
  (§10) rather than being narrower than it: filing a ticket against the current milestone and
  marking it a blocker is what declares it part of the scope being audited, so promoting it adds
  nothing the author has not already committed to.

  **The exemption is not optional, because promotion is upstream of the drain.** This condition
  was once absolute, and the asymmetry with pickup read as a deliberately stricter policy. It was
  a deadlock. A blocker filed during the pass opens in `Todo`, so it needs a design pass to reach
  `Ready for dev` — the only queue the drain can see — and with promotion paused it never got one,
  so the drain had nothing to drain, so the blocker never closed, so the boundary never closed, so
  promotion stayed paused. Catapult's ORC-156 sat in `Boundary review` behind thirteen of them,
  and the report said only that the queue was paused.

  **`Urgent` is not a second exemption here**, though it is one at dev pickup. There it means
  finishing work already designed; a ticket still in `Todo` is undesigned work whatever its
  priority, and the answer for a true stop-the-world fix is the one below — bypass the pipeline.
- **It is not the boundary ticket and not `author-only`.** Neither is the pipeline's to move at
  all (§8, §10).

Among the tickets that qualify, the sweep takes the first the ordering gives: the precedence rule,
which is what the report sorts each layer by. Promotion is a control-plane write like any other —
recorded, attributed, and therefore not judged as an author move by §9.

**The report changes with it.** `pipeline order` was advisory: it derived the next move and a
human made it, so a reader who disagreed simply did something else. Once the sweep acts on the
same derivation the report stops being advice and becomes a prediction, and a prediction that
omits the rule it is predicting is worse than none. Three things it owes.

- **It names the ticket the sweep will promote next**, or says why it will promote nothing: the
  design queue is occupied, the pause is holding everything that does not block the boundary,
  the head of *ready now* carries no milestone.
  *Ready now* is a fact about blockers; *next* is a fact about all five conditions above, and
  those are not the same set. A reader looking at three unblocked tickets while the pipeline
  promotes none of them has been told the truth and misled by it.
- **A ticket whose blockers have all reached `Merged` says design can start on it.** It stays in
  *freed by what is in flight* rather than moving to *ready now*, because the layering answers
  for the whole pipeline and promoting it into `Ready for dev` would still be wrong. The note is
  what carries the difference, and without it the report and the sweep disagree on the one case
  where their thresholds do.
- **The layer prose stops addressing the reader as the one who acts.** *Ready now* means "the
  candidates the control plane draws from", not "the candidates to start"; and the
  milestone-less note gains its consequence — the automation will not move this, so it waits for
  the author whether the missing milestone was an oversight or a choice.

**Priority is the tracker's built-in field, not a label** — it's ordered, and an ordered field
is what both the queue and the debt-fill rule need.

**`Urgent` preempts at pickup only.** It never interrupts a ticket mid-implementation: a
half-finished branch is worse than a few minutes' wait. It **does** override the milestone
boundary pause, marked as blocking the boundary ticket or not — anything urgent enough to
carry the flag is urgent enough to outrank a pause at that point, and the boundary's own work is bug-fixing
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
- **a design commit outside the project's `designOwnedPaths` fails the build** — the design
  ownership audit, the mutex audit's mirror image: that one asks whether a path the diff
  touched is claimed by a doc whose label the ticket lacks, this one whether a path the design
  agent wrote is one design owns at all. It needs its own file list rather than the diff,
  because attribution is the question — a PR carries design's commits and dev's on one branch,
  and dev may amend design-owned files on discovery (§5), so "what the diff touched" cannot say
  whose those paths were. `git log --author` splits them: the two agents commit under
  `pipeline-design-agent` and `pipeline-dev-agent`. Given no such list, the audit says it
  skipped the check rather than reporting a clean run it did not make.
- no path may appear in two system file maps — overlapping ownership is an ambiguous mutex,
  and an ambiguous mutex is two tickets in the same files with a green build
- `screens/*.md` and `systems/*.md` contain no state sections and no code inventory — the doc
  lint, enforced on the docs as they stand rather than on the diff, since a doc that has held a
  banned section since before this ticket is still holding it. It catches both rules in their
  **sectioned** form: a `## States`-style heading, a `## Modules`-style one. Prose is not
  linted, and that is the point — a standing decision naming `farewell/1` is the decision doing
  its job, while a heading with a list under it is the split the rule was written against. A
  check that failed good docs would be switched off, taking the rule with it.

**Design finish (blocking, in the harness — the same rule, one gate earlier):**
- a design pass that committed outside `designOwnedPaths` does not open a draft PR and does not
  reach `Design review`. The finish fails, the stray paths land on the ticket as a comment, and
  the run's abort step parks it in `Blocked` (§12).
- both gates exist because they catch different moments, not out of belt and braces. On a first
  pass there is no PR when the design finishes, so CI has not run and cannot: the harness gate
  is what stops the author being asked to review a strayed diff as design. CI is the backstop
  that holds on every later push, whatever produced it.
- the harness gate audits this pass's commits only, diffed from where the pass started, so a
  re-pass is not billed for the artifacts and dev commits the branch already carried. CI audits
  every design commit on the branch, which is the right scope for a merge gate.
- the strays stay on the branch either way. Blocking the *advance* rather than the commit is
  deliberate: the author has to read the diff to strip it, and a run that swallowed its own
  output would leave a `Blocked` ticket with nothing to look at.

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

**And they are not prompt material.** A marker is an address, not an argument, so what reaches a
model is the prose under the header and nothing else — with the comments that carry no prose
dropped whole. Kind alone does not decide it: `blocked` is posted for seven different arrivals
(§12), and a push-back, a needs-setup, an author-only split, a scope-satisfied park and a
prerequisite park each carry the argument that put the ticket there, while a plain failed run
carries a URL and the captured tail of a crash. That last one on a prompt is the previous run's death handed to the
next run as context. So the flavor decides, not the kind.

Two things were wrong before, not one. Bytes: a ticket that fails repeatedly grows its own
prompt, because every failed run appends a dispatch marker and a blocked marker that the next
claim inlines. And correctness: a returned ticket's scope is "the newest comment" (§2.3), which
was read without regard for who wrote it — the bounce is newest at the moment a ticket enters
`Reworking`, so the ordinary path worked, and anything posted in the window between the bounce
and the claim replaced the rework scope with a machine's note about the pipeline.

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

   **A failure parks the ticket in `Blocked`, and the verdict gates the boundary agent.** The
   marker used to be the whole of it, on the reasoning that it "lands on the boundary ticket
   where the author is already looking". That assumption did not hold. A boundary ticket in
   `Todo` looks identical whether the suite passed, failed, ran no tests, or has not run at all
   — four situations, one appearance — and on Catapult's ORC-99 a real failure sat unnoticed
   until somebody went and read the marker deliberately. State is what every listing shows.

   What makes `Blocked` safe here, rather than a state that swallows the manual pass, is the
   gate: **the boundary agent does not start until the newest verdict is `pass` or `no-tests`,
   and a boundary ticket entering `In progress` with the suite unproven re-runs the suite
   first.** So the two ways out of `Blocked` both work and mean different things — `Todo`
   resumes the manual pass, `In progress` says the pass is done and re-runs the suite before
   the agent. Without the gate, clearing the block the obvious way would skip the pass
   entirely.

   `no-tests` satisfies the gate. A project with no `:live` tests yet is not broken, and
   whether the milestone can close without live coverage is the author's call — blocking on it
   would make the first boundary of every new project red for a structural reason, which is how
   a gate becomes one people learn to click past.

   **Each retry costs a deliberate move.** A re-run that fails again parks the ticket again,
   and nothing dispatches from `Blocked` — so a suite failing for an environmental reason
   cannot loop through the milestone's budget on its own. The author decides each time.

   **This is the one state that feeds two agents, and dispatch readiness has to be asked per
   agent because of it.** Everywhere else a state feeds exactly one, so "has anything been
   dispatched since this ticket arrived" is the same question as "has *this* agent been
   dispatched". Here it is not: the live suite re-runs in `In progress` and then the boundary
   agent runs in the same `In progress`, so the suite's own run answers the boundary agent's
   question with a spurious yes. Measured on Catapult's ORC-99 — entered `In progress` at
   19:23:37, the re-run passed at 19:26:09, and nothing dispatched after it. The ticket sat
   until the stale-claim rule parked it, reporting accurately that no boundary run had ever been
   dispatched: the symptom named correctly by a rule that was not the cause.

   **A verdict already reported is not reported twice.** The block fires on a live-suite marker
   with no live-suite `blocked` marker after it, not on "the newest verdict is `fail`". Keyed
   the second way, an author moving `Blocked` → `Todo` — the natural gesture, meaning "seen it,
   back to my pass" — would land on a ticket whose verdict is still `fail` and be parked again
   on the next sweep, and every sweep after it. The author could never reach the state their
   own pass happens in.

   Note what that is not: each sweep still settles, so the convergence check every simulated
   scenario runs would pass it. The failure is that the pass converges on undoing the author.
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
5. Author moves the boundary ticket to `In progress`. **This is the signal.** If the live suite
   has not passed by then — it failed, or its run died without posting a verdict — the suite
   re-runs before the boundary agent starts, and a second failure returns the ticket to
   `Blocked`.
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
7. **Debt scan**, bounded inputs only. Bounded because "did we take on debt?" asked openly
   produces invented findings.

   <!-- pipeline:list id=debt-scan-inputs -->
   - Diffs merged since the last boundary (the previous retro note under `docs/retros/` marks
     where that was; `git log` from there).
   - New `TODO` / `FIXME` markers.
   - Skipped or deleted tests.
   - Dependency and advisory drift. **Both halves of an acknowledged advisory, not just the
     version.** An `ignore_advisories` entry (or its equivalent) usually rests on two
     independent justifications: that there is nothing newer to move to, and that no path to
     the flaw is reachable from this project's own code. The first self-expires — the audit
     tool flags a listed ID matching nothing, so a bump makes it fall out on its own. The
     second is prose, and nothing derives it, tests it or notices when it stops being true. So
     re-read each reachability claim against the code as it stands now, and treat one the diff
     has falsified as a finding.
   <!-- /pipeline:list -->

   **The harness findings agents recorded this milestone are a sixth input, and deliberately
   not in that list.** The list is what a pass goes and looks for; the findings are handed to
   it, rendered into the prompt by the harness. Saying "go and find the harness findings"
   would describe work nobody does. This is the difference the two copies of this list used to
   leave unexplained — DESIGN counted five where the prompt counted four, and nothing recorded
   whether that was a decision.

   **Advisory drift means both halves of an acknowledged advisory.** An ignore entry usually
   rests on two independent justifications, and only one of them can expire by itself. "There is
   nothing newer to move to" self-expires: the audit tool flags a listed ID that matches nothing,
   so a dependency bump makes the acknowledgement fall out. "No path to the flaw is reachable
   from our own code" is prose — nothing derives it, nothing tests it, and no gate notices when a
   new listener makes it false. So the scan re-reads each reachability claim against the code as
   it stands, and a claim the diff has falsified is a finding.

   Measured on Catapult: the cowlib entry said "the plane serves `/health` only", ORC-9's
   `DispatchPlug` gave that listener `/dispatch/*` and updated the README and `SETUP.md` in the
   same commit, and `mix.exs` was the one place the sentence did not get updated. It stayed stale
   for a full milestone with every gate green, and what caught it was a debt scan reading the
   diff rather than any check.

   **Once per milestone rather than once per ticket**, which is why it is here and not a project
   convention. A convention is read by every pass, so it would put a recurring audit into the
   standing cost of every piece of work, for a class of rot that only moves when a listener
   changes. Catching one early is a bonus, not the mechanism.

   The general form is worth stating, because it outlives advisories: the bounded-input list is
   what stops the scan inventing findings, and it is equally what decides which kinds of rot are
   visible at all. **A justification that cannot expire on its own needs a pass that re-reads it,
   or it is permanent by construction.**

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
8. **Grooming pass.** Re-ranks existing debt as well as proposing additions. The re-rank
   resolves ticket keys against the snapshot's scheduled tickets **and its Triage**, which is
   not an implementation detail: step 9 files proposals into Triage and unscheduled debt stays
   there until an author gives it a milestone, so the backlog this step exists to rank lives
   entirely in the half a scheduled-only lookup cannot see. Resolved against scheduled tickets
   alone the step is inert for every candidate and reports "re-ranked 0 tickets" as a success —
   which it did, on every pass, until it was measured.
9. Proposals land in **Triage**, as `debt`, `design`, `harness` or `bug`.
   **Bugs were refused here until this milestone**, on the argument that a bug parked in a queue
   has been rescheduled rather than repaired. The argument is sound; it is not what that rule
   enforced. Parking is prevented by *Blocking work found during the pass* below — work that must be fixed
   before the milestone closes is filed against the *current* milestone — and closing the
   vocabulary on top
   of it did not stop the boundary finding defects. It stopped it naming them, and a defect it
   cannot name it files as debt: `tech-debt` is the label the composition rule draws, so the fix
   inherits the debt milestone's cadence of one milestone in two against a floor of five. That is
   the rescheduling, reached by obeying the rule against it.

   Measured on Catapult's ORC-90, from the twelve findings its archive step carried: an outage
   that failed every sweep on the project from 18:06 to 19:58, two boundary agents running
   concurrently on one ticket at about twenty-two minutes of spend, a preflight check holding the
   expected and the live label sets and comparing neither, and a prompt flag rendered `false`
   whatever the harness had recorded. None of those is work on the shape of the code rather than
   what it does, which is §8's definition of debt. Every one is something that does not do what
   it says. All twelve were recorded under `harness`, the only durable channel that would take them.

   **What makes naming them safe is that a bug needs no milestone to run.** Nothing sequences a
   milestone-less ticket, so the queue takes it as soon as the author accepts it out of Triage
   (§8), and `Urgent` is what makes it preempt. A bug proposal therefore never enters the debt
   composition — it does not need a slot in one. The boundary still does not assign milestones,
   so a defect it believes blocks the milestone is a proposal whose description says so, and the
   assignment stays the author's.
   **Every carried finding leaves the pass adjudicated** — filed as a proposal naming it, or
   declined with a reason recorded on the ticket. Those findings were carried off tickets the
   archive step deleted, so the boundary that sees them is the last one that can: a finding
   neither filed nor declined is not deferred to the next milestone, it is deleted. Declining is
   frequently right and costs a sentence; what the author cannot review is a judgment nobody
   wrote down. Measured on Catapult's ORC-90 — twelve findings carried, one unrelated proposal
   filed, no record that any of the twelve had been read.
   **Adjudication matches on the finding's name, not its whole key**, because the two producers
   write two key shapes and both are correct: a carried finding takes its key from the recorded
   marker's `id=`, which is the bare name, and a scan proposal is keyed by milestone and name.
   Compared whole they never match across that boundary, and the pass reports a finding it filed
   seconds earlier as about to be lost. That warning exists to be acted on, and acting on a false
   one means hand-filing a duplicate — of a ticket already in Triage, with nothing downstream to
   catch it, since proposals are no longer deduplicated (§10's filing step). Normalising belongs
   at the comparison rather than at either producer: neither shape is wrong, and a rule that
   makes one of them wrong has to be enforced in two places forever.
   **A finding says what it is about.** An agent records `harness` for the pipeline and
   `project` for the repository it is working in, and the two reach different judgments: the
   first asks whether the pipeline is costing tickets, the second goes through the debt scan's
   gating test like any other debt. The distinction was stated in the prompts and enforced
   nowhere, and it lost to an incentive — a hand-back is prose in a ticket comment, while a
   finding is collected, deduped, carried past the archive and put in front of the boundary, so
   the only durable channel an agent had for a project defect was the one labelled `harness`.
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

**Promotion into the design queue follows the same rule** (§8), and has to. A blocker filed during
the pass opens in `Todo` and needs a design pass to reach `Ready for dev`, so a drain fed by a
paused queue drains nothing: the pause outlasts the boundary it was protecting, and the author is
left holding tickets that cannot start and a boundary that cannot close.

The boundary agent does not begin while a blocker is open.

### Resuming a failed boundary pass

The boundary agent has a lot to do and can fail partway. Recovery is `Blocked` → `In progress`
or `Boundary review` → `In progress`, possibly more than once, and neither is useful if
re-entry means starting over.

**`Blocked` on a boundary ticket has two causes, and re-entry handles both the same way.** The
agent's own run died, or the live suite failed (§10 step 2). Entering `In progress` re-runs the
live suite when its newest verdict is not `pass` or `no-tests`, and dispatches the agent when
it is — so an author who cannot tell which of the two parked it does not have to: the same
gesture does the right thing either way, and the flavor on the `blocked` marker says which it
was for anyone who wants to know.

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
leaves a pass that reads as finished, so the next entry redoes it. That costs one debt scan and re-files
whatever it finds again, for a human to decline at `Boundary review`, and it is the trade for
having the terminator be the last thing written rather than something a crash could skip.

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
| Triage proposals | **Not deduplicated, deliberately — see below.** Safe to re-run *within* a pass, because the file step is guarded by its own step marker and that marker is written even when individual proposals failed. A *second* pass over the same tree files its findings again, and that is the accepted cost. |

**Nothing deduplicates a proposal, and that is a decision rather than a gap.** Two automatic
keys were tried and both failed, in opposite directions.

The first keyed on the model's own `dedupe` string. Phrasing is a choice rather than a fact, so
two scans of one tree wrote two keys for one finding and filed it twice. The second derived the
key from the proposal's `subject` — the concrete thing it is about, named as the repository
names it — on the reasoning that a subject is a repository fact. It is, but it is not a key.
Measured on Catapult's `ORC-118`: seventeen proposals became sixteen tickets, because a stale
`sobelow` ignore and a compile-cache gap both named `.github/workflows/ci.yml` and the second
was dropped — while a decline note on the same ticket told the author it had been filed. In the
same run, two proposals naming `lib/catapult/engine/commands/approve_gate.ex` and
`Catapult.Engine.Commands.ApproveGate` — one thing, written two ways — did *not* collide. The
rule merged what it should not have and missed what it should have caught, and which it did
depended on whether the model happened to write a path or a module name.

The costs are not symmetric. A duplicate ticket is visible at `Boundary review` and costs a
sentence to decline. A dropped finding is invisible: it lived on the archive step's comment and
nowhere else, and it is deleted when the next milestone opens a new boundary ticket. A
non-deterministic key should not be the thing choosing between those. The subject still rides
on the filed ticket's marker, because deduplication is now the author's and the subject is what
they sort by.

**A gating proposal is named in the file step's own comment.** Gating is read by the composition,
which draws `tech-debt` — so a gating *bug*, which never enters the composition because it needs
no milestone to run, was recorded in a marker field on one issue and reported nowhere. On
`ORC-118` two proposals were marked gating and the author's summary read `gating=0`. The count in
the composition is scoped to what the composition schedules; the file step is where every gating
judgment is named, whatever its kind.

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
                    it resumes from the first step with no comment. Once it has
                    finished a pass, moving it back here starts a NEW one — the
                    scan runs again over whatever has landed since.
  Boundary review → the agent put it back. Proposals are in Triage; accept or
                    decline, confirm the ranking, then close this ticket and pull
                    the next milestone into Todo. The comment above proposes the
                    next debt milestone's contents — assigning those milestones is
                    yours, because assigning one commits the work.
  Done            → you close it. The queue resumes.

The queue is paused while this ticket is open. Tickets marked as blocking this one
still run — design and dev both, which is how this pass's own findings get fixed.
Everything else waits. Urgent additionally overrides the pause at dev pickup.

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

**`Blocked` is global.** Any agent may move any ticket there. It has seven flavors, and both
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
- **Nothing failed, and the thing this ticket builds on isn't there yet.** The `prerequisite`
  case (§3): the scope depends on a change that is not on `main` and is not this ticket's to
  write, so there is nothing to decide. It fits the `needs-setup` sentence — a human has to do
  something the run cannot — and gets its own label anyway, because the action and the clearing
  condition are different: `needs-setup` wants a secret provisioned now, this wants another
  change merged and then this ticket put back in its queue. The design pass reaches it as an
  outcome rather than an abort (§13); without it such a pass had no legal outcome, died, and
  parked under `failed`, reading as a harness fault.
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

**Four labels, not one**, because a duplicate ticket, a missing secret, an unlanded
prerequisite and an unbuildable design want four different actions from a human. A `Blocked` column that renders them
identically is one where every ticket has to be opened before it can be triaged, which is the
same failure as a column where waiting and broken look alike.

**And not one more.** A run that changes nothing and names no reason is filed as
`scope-satisfied` too, with a comment saying the label was inferred rather than reported. Its
own label was tried and dropped: it is the same status, read by the same person, answering the same
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

**A failure that reaches the ticket brings what the run printed, not just where to find it.**
The abort comment was the run URL and nothing else, so a run whose model pass exited `249` —
the CLI's code, not the pipeline's, and not one this project can decode — arrived as a bare
number with nothing to act on. The output that could have explained it was written to a
collapsed step and discarded. So both bracketing steps keep their output as well as showing it:
the model run captures each attempt's stdout and stderr, and the finish step captures its own,
into one well-known file the abort path reads back and pastes onto the ticket, bounded and
fenced. Fenced because it is output from a process nobody vetted landing on a ticket later
passes read as input — the same trust boundary as a CI failure (§9).

Evidence is cleared when the run recovers. The model pass fails over from the subscription
credential to the API key, and a first attempt's death left behind is a cause of death attached
to a run that went on to succeed, or worse, to one that later failed somewhere else entirely.

**And it brings the argument the run had already written, when there is one.** A run that fails
*validation* — the outcome parsed and the harness refused its shape — has done the thinking the
ticket needs before it died. That reasoning lives in the outcome file and used to go to the
floor with the rejected outcome, so the ticket returned carrying a stack trace and nothing else
and the next pass began from the description again. Measured on Catapult's ORC-133: a design
pass answered `decisionless` with a screen in its touch list, which §3 makes a contradiction,
and its account of why it thought nothing was being decided was discarded.

Read leniently, because by then the outcome has usually already failed its own validation — a
strict read drops the argument in exactly the case it is most wanted. Not fenced as evidence,
because unlike the captured output the model *wrote* this: it is prose, arriving on a ticket a
later design pass reads as input, and §2.3 makes comments the channel accepted deltas travel
on. So it is posted under a heading that says the outcome carrying it was refused, and states
that nothing in it has been agreed. A rejected argument offered as context is continuity; the
same text offered as a decision is a delta nobody accepted.

**A failed run says whether its work survived, and the retry is told what is on the branch.** A
pass that dies *after* pushing leaves a complete piece of work on the ticket branch, and the
comment recording the failure used to read the same either way — "Agent run failed: <url>",
whether the branch carried a finished design or nothing at all. Those want different decisions
from the author, so the `blocked` marker carries `pushed=<sha>` when the run had already
pushed. Written by the push step once the push has landed, rather than inferred afterwards from
a branch that may have moved.

The retry is the harder case, because it is not a reader who can go and look. The role prompt
tells the agent it is re-instantiated with no memory and that everything it needs is in the
prompt and the repository, and an agent following that literally starts fresh and redraws
artifacts already committed — or re-litigates a refusal already recorded in the non-asks, the
one document whose stated reason for existing is that a refusal which quietly disappears gets
proposed again. So the prompt carries the branch's own log, read after checkout, and says the
pass is resuming rather than starting. Read off the branch rather than out of the record of the
push, because a run can die without ever reaching its abort step and the branch is true either
way.

**Both working agents, because both claim before their checkout.** The branch to check out is
an output of the claim, so design and dev alike assemble their first prompt against `main` and
rebuild it once the branch is under them. That rebuild is also what gives each of them the
branch's copy of the non-asks rather than main's — a pass shown a document its predecessor has
already added entries to, minus those entries, is the same failure in a different file.
Reconcile and the boundary need no rebuild: reconcile argues from the ticket and the PR, and the
boundary reads the pipeline's own repository.

Measured on ORC-69, run 32048439216: it committed a complete design pass, pushed `209fc9d`, and
then failed. Nothing handed to the retry distinguished that from a first pass — the only signal
was the branch's own git log, which the agent had to think to look at before it started.

**What the exit code is not.** The harness decodes `129`–`159` as a process killed by signal
`n-128`, which is the shell's own convention and therefore a fact. Every other code belongs to
the program that exited, and the pipeline does not guess at another tool's table — inventing a
meaning for `249` is how an opaque number becomes a misleading one. The captured output is the
answer to "what does this mean"; the number is only where to start.

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
  flake; the author decides whether it's scope, design, or infrastructure. **One failure read
  twice is not two**, and a verdict is identified by the run *and the attempt of it*: a re-run
  keeps the run's id and its URL and increments only the attempt, so keying on the URL alone
  reads a re-run's own failure as the one already recorded and says nothing about a real
  failure. That silence is the worse half — the ticket sits in `Checks` with red CI and no
  comment naming it.
- **A label the harness attaches invalidates the verdict, so the harness re-runs it.** The
  audit reads the ticket's labels as well as the diff, so a mutex label attached from a run's
  outcome is an input the standing verdict never saw. Nothing in the commit changed, so
  nothing re-triggers CI on its own — and a rework whose whole remedy is metadata is a
  legitimate outcome (§7) with no commit to produce. Left alone the pre-label failure is
  published as a second red and a single real failure escalates to `Blocked`, which is where
  ORC-148 sat with nothing inside the pipeline able to clear it.
  The re-run happens where the label is attached, before the transition, for the reason the
  label is attached there: on the other side of it the sweep reads the verdict, and one
  refreshed afterwards is refreshed too late. **A re-run, never a fresh dispatch** — a re-run
  replays the original `pull_request` event, and the audit's gate reads `github.head_ref`,
  which is empty outside that event, so a dispatched run skips the audit and reports a green
  that checked nothing. Only a red verdict is refreshed: the audit fails on a mapped path
  whose label is absent, so attaching one removes violations and never adds any, and a green
  verdict stays green under a larger label set.
  **Accepted is not started.** The re-run is confirmed by watching the attempt move, not by
  the HTTP status, because a call that returns success and starts nothing is the failure worth
  catching. When it cannot be confirmed the ticket is told so in the same comment that records
  the label, and the finish continues — failing a run that did exactly the right thing is the
  outcome this route exists to prevent.
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

**Agent runs are read one workflow file at a time.** The run's name — `pipeline: <kind>
<ticket-key>` — is what correlates a run to a ticket, but the *listing* is per workflow file
from the config's `agents` map rather than the repository's runs at large. One page of a
repository is one page of whatever that repository mostly runs, and on a pipeline repo that is
the metronome: the sweep is itself a workflow run, woken by CI completions as well as hourly, so
its volume rises with the very activity that produces agent runs. A live agent run crowded out
of that page reads as no run at all, which is a second agent dispatched onto a ticket that
already has one. Per file the window is what it claims to be, because the singular agents run
one at a time. A file the `agents` map names and the host does not have fails the read rather
than returning nothing: a kind whose runs cannot be listed looks permanently idle, and
permanently idle is permanently dispatchable.

**Triggers**, in order of the loop:

| Condition | Action |
|---|---|
| Ticket in `Todo`, current milestone, every blocker at `Merged` or later, design queue empty, not paused — or paused and marked blocking the boundary ticket | State → `Ready for design`, first by the precedence rule (§8) |
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
| Boundary ticket in `Todo`, no live-suite result | Dispatch the live-suite run, once (§10) |
| Live-suite verdict `fail`, run finished, not already reported | Boundary ticket → `Blocked`, marker flavor `live-suite` (§10) |
| Boundary ticket → `In progress`, newest verdict not `pass`/`no-tests` | Re-dispatch the live suite; the boundary agent waits (§10) |
| Boundary ticket → `In progress`, live suite satisfied | Boundary agent run: archive, debt scan, grooming |
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

**The GitHub-side hops arrive by webhook, and the project stub no longer triggers on them at
all.** CI green and deploy detection were originally left to `workflow_run` and
`deployment_status` triggers on the stub. Those did not fire for the pipeline: GitHub will not
start a workflow run from an event created with `GITHUB_TOKEN` — `workflow_dispatch` and
`repository_dispatch` are the only exceptions — and an agent that pushes, merges and records
deploys with that token starts nothing. The triggers worked for a human's commits and never for
an agent's, which is the only case they existed for, so the gap read as a pipeline that had
merely gone slow. Webhook *delivery* is not workflow triggering, so the same Worker receives
them, verified against GitHub's own signature.

**That is why the Worker route exists; it is not why the triggers were removed, and the two are
worth keeping apart.** SETUP 2 has every project check out with a scoped user token (see just
above), precisely so that an agent's push does start CI and does record deploys. Under that
token the stub triggers fire perfectly well. What they fire is a *second* sweep.

**They were kept as a backstop, and the backstop cost more than it covered.** A trigger declared
in the stub is scheduled by GitHub and never reaches the Worker, so it bypasses the debounce
(below): one CI completion started two sweeps — one immediately from the stub, one debounced
from the metronome — and `concurrency` then evicted the pending one. That is coalescing done
late, after dispatch, on a platform that bills each job rounded up to the minute. Measured on
Catapult: seven sweeps in 107 seconds, two of them cancelled that way.

What made removal safe is that the Worker route is the one correct under *both* token
configurations, and that the backstop was never the floor. **The hourly cron is**, and it
dispatches directly rather than through the debounce — so a Worker outage is already covered by
something that works for agent-created events too, which the stub triggers never did.

**Only a successful deployment wakes a sweep.** `deployment_status` fires on every state
transition, and one deployment walking `queued` → `in_progress` → `success` is three webhooks
minutes apart — far outside a five-second window, so three dispatches. The deploy check reads the
newest *successful* deployment and compares its commit against the merge commit, and a failed
deploy is caught by the deploy timeout (§12) rather than by an event, so the other states can
advance nothing and are dropped at the Worker.

One filter there is load-bearing rather than an optimisation: only the project's CI workflow
completing may wake a sweep. A sweep run completing is itself a `workflow_run` event, so waking on
any completed run would have each sweep dispatch the next, indefinitely, against a token that can
start workflows in every project repo.

**If even that is too slow,** Durable Objects are on the free plan with the SQLite backend, and one
Durable Object per project *is* the single-dev-agent mutex, serialized by construction. Watch the
free-plan cap of three cron triggers per Worker, and the absence of retries or failure alerting on
them.

---

### What an agent's outcome does

The trigger table above is *condition → action* for the control plane. This is the other half:
*outcome → action* for the agents. Every value a pass may emit, and what the harness does with
it.

The edges are the part that was written nowhere. §8's table says what each label means; the
prompts say which value to emit; the code maps one to the other in five separate `switch`
statements. Nothing said which value produced which label.

**Four values do not name what they produce**, and they are bolded below: `needs-setup` writes
a marker field called `setup`, `cannot-tell` sets `needs-review`, `debt` files under
`tech-debt`, `design` under `design-inbox`. A reader who assumes the value *is* the label is
right about `pushback` and `bug` and wrong about those — which is the shape of thing worth
writing down once rather than inferring at each call site. `decisionless` is bolded for a
different reason: it is the one outcome that skips a state, going straight to `Ready for dev`
without passing through `Design review`.

<!-- pipeline:list id=agent-outcomes for=tests -->
| emitter | value | → state | → label | → marker |
|---|---|---|---|---|
| abort | `pushback` | `Blocked` | `pushback` | blocked, `pushback=1` |
| abort | `failed` | `Blocked` | — | blocked, no flavor |
| abort | `needs-setup` | `Blocked` | `needs-setup` | blocked, **`setup=1`** |
| abort | `author-only` | `Blocked` | `author-only` | blocked, `author-only=1` |
| abort | `scope-satisfied` | `Blocked` | `scope-satisfied` | blocked, `scope-satisfied=1` |
| abort | `prerequisite` | `Blocked` | `prerequisite` | blocked, `prerequisite=1` |
| design | `artifacts` | `Design review` | the pass's mutex labels | — |
| design | `decisionless` | **`Ready for dev`** | the pass's mutex labels | decisionless-pass |
| design | `prerequisite` | `Blocked` | `prerequisite` | blocked, `prerequisite=1` |
| design | `clear` | unchanged | removes `re-evaluate` | — |
| design | `demote` | `Ready for design` | — | — |
| reconcile | `pass` | `Merged` | — | merged |
| reconcile | `fail` | `Ready for rework` | — | reconcile-bounce |
| reconcile | `cannot-tell` | `Merged` | **`needs-review`** | merged |
| reconcile | collision `holds` | unchanged | — | — |
| reconcile | collision `bites` | `Ready for rework` | — | reconcile-bounce |
| boundary | kind `debt` | Triage | **`tech-debt`** | triage-proposal |
| boundary | kind `design` | Triage | **`design-inbox`** | triage-proposal |
| boundary | kind `harness` | Triage | `harness` | triage-proposal |
| boundary | kind `bug` | Triage | `bug` | triage-proposal |
| live suite | `pass` | unchanged | — | live-suite |
| live suite | `fail` | **`Blocked`** | — | live-suite, then blocked `live-suite=1` |
| live suite | `no-tests` | unchanged | — | live-suite |
<!-- /pipeline:list -->

`failed` is the only abort reason that takes no label, and that is the rule rather than an
omission: it means the harness broke, which is not a fact about the ticket.

**What is held to the code and what is not.** The **value** column is asserted against
`protocol`'s vocabularies in both directions, so a value added to one and not the other fails
CI naming the document it is missing from. The **label** column is asserted for the boundary
rows, which are a lookup rather than control flow. State and marker are documentation:
accurate when written, not derived. Encoding them would mean rewriting five `switch`
statements into data-driven dispatch, and those switches validate arguments and call helpers
as well as mapping — the rewrite would risk more than the drift it prevents. Saying which is
which beats implying the whole table is machine-checked.

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

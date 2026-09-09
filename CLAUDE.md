# orchestration

The pipeline itself: the Go control plane, the agent prompts, and the
reusable workflows project repos call. `DESIGN.md` is the protocol and
the single source of truth — every rule there states its rationale
inline, and a change to behaviour is a change to that document in the
same commit.

This file is what only this repo can tell you.

## Gates

```sh
gofmt -l .          # CI fails on any output; go vet and go test do not catch it
go vet ./...
go test ./...
cd worker && node --test --experimental-strip-types *.test.ts
```

The worker line is a gate, not a nicety: `go test ./...` cannot see
TypeScript, and the suite it skips covers the webhook signature check —
the only thing between a public URL and a token that starts workflows in
every project repo. It runs in `ci.yml` on the PR and again in
`worker-deploy.yml` before a deploy, so a contributor who skips it here
learns about it from CI rather than from production. Node 22, matching
both.

**The glob is load-bearing, and it is pinned in four places** — here,
`ci.yml`, `worker-deploy.yml` and `worker/README.md`. It named
`index.test.ts` alone until `projectstats.test.ts` was added, and a
second suite under that line runs locally, passes, and is never executed
by CI or by the pre-deploy check: a green pipeline saying nothing about
the file. Pass the glob rather than the directory — Node tries to load a
directory as a module and fails, which is why the explicit name was
there in the first place.

## Comments explain why, including what went wrong before

This codebase records the failure a rule exists to prevent, at the point
the rule is applied. That is not decoration — most of the rules here look
arbitrary without the measurement that produced them, and the ones that
looked arbitrary are the ones that got "simplified" back into bugs.

**Do not assert a measurement you have not taken.** A threshold was
invented once and survived review; it was caught only because somebody
later measured it. If a number is a guess, say it is a guess, or go and
measure it.

**That covers what the pipeline says, not only what the source says.** A
comment the harness posts on a ticket is an assertion made to a reader who
cannot check it. `staleClaimFor`'s message states that the newest run
"ended before this state was entered" as fixed text, on a branch chosen
only by the run's *kind* — so on Catapult's ORC-99 it said exactly that
about a run which had ended two and a half minutes *after* the state was
entered, and that recency was the whole cause. It then named three likely
culprits, none of which was it. Still unfixed at the time of writing, and
the comment beside that code records an earlier version of the same defect
being fixed one clause over.

## Verify a guard by breaking it

A passing test says nothing about a guard until you have watched it fail.
Revert the line the guard lives on, run the test, read the failure, put it
back. Commit messages here record that probe, and the output it produced.

The reason is not diligence. `awaitingDispatchOf` shipped as a regression
behind a scenario that ran the whole boundary loop and asserted the
ticket's *state* at the end — a state the ticket holds whether or not
anything was dispatched. CI was green, the boundary agent never started on
a passing live suite, and the ticket sat until the stale-claim rule parked
it twenty minutes later.

**A probe must assert that it edited something.** The dangerous failure
is not a probe that fails — it is one that never ran and printed `ok`.
Four times in one session a scripted revert left the file untouched (an
escaped regex that did not match, a `sed` anchor appearing twice, a
`git checkout --` that reverted to HEAD and destroyed the uncommitted
change under test) and the `ok` afterwards was indistinguishable from a
guard holding. So the revert asserts its anchor matched *before* the
test runs, and a probe that cannot find its anchor is a failed probe,
not a passed one.

Removing the code a test *reads* is a weaker probe than breaking what it
*means*. Deleting a struct field the test references fails the build,
which proves only that the test mentions it; misspelling that field's
JSON tag fails the test, which is the behaviour under test.

**A probe that passes is a finding, not a formality**, and it is pointing
at one of exactly two things. Either no test covers the line — which is
the `awaitingDispatchOf` hole again, arriving through a different door —
or the line is inert and should not be there. Both showed up in one
probe. Breaking the host-to-core outcome mapping printed `ok` first
because nothing asserted that crossing at all: the adapter test proved
GitHub's conclusion was read, the core tests proved the rule acted on it,
and the link between them was unasserted. Then the retry printed `ok`
again because it had matched the *other* construction site, whose value
no test could ever cover — a live run has not concluded, so the field was
always the zero value. That line is gone rather than left. Read a passing
probe as the question "which of those two is it", never as a green light.

Measured 2026-08-23, because the folk version of this is wrong: the Go
test cache does **not** serve a stale pass when you edit a file the test
reads at runtime. Breaking `.github/actions/agent-dev/action.yml` after a
cached `ok` re-ran the test and failed it. A `(cached)` line is not a
reason to distrust a result, and `go clean -testcache` between probes is
insurance, not a requirement.

## A fake that rejects a real value hides the bug it was built to catch

`tracker.Memory` validates state categories against a fixed list, which is
the right shape — it enforces Linear's one-category-per-state rule so
setup is tested against the real constraint. But the list was a copy of
what `protocol` happened to declare, and it was missing `duplicate`, the
type Linear gives its own built-in Duplicate state.

So the regression test for "a ticket marked Duplicate must not take the
sweep down" could not seed a Duplicate. It named its state `"Duplicate"`
and gave it `CategoryCanceled`, the fake accepted that, the plane read it,
and the test passed — on a state Linear never produces. The fake and the
code under test agreed with each other while both disagreed with the
tracker, and the incident the test was written for stayed live behind a
green suite for a milestone.

Measured against the real tracker, not inferred: the Orchestration team's
own state list returns `{"type":"duplicate","name":"Duplicate"}`, and all
seven of Linear's types appear in it.

The lesson generalises past this one field. **When a fake enumerates what
the real system may return, that enumeration is a claim about the real
system** — and it is the kind of claim that fails silently, because
narrowing it does not break a test, it deletes one. Check such a list
against the live API rather than against the constants beside it.

## The sim asserts what you tell it to assert

`internal/sim` runs a scenario to convergence and checks its `expect`
steps. `expect` carries `state`, `hasLabels`, `lacksLabels`, `assignee`,
`markers`, **`runKind`** and **`runLive`** — and only the last two say an
agent was actually dispatched. Sixteen of the twenty-three scenarios use
them. The one that did not is the one above.

If what is under test is that something *runs*, assert the run.

## Adding a protocol state is a two-repo change, and the order matters

`protocol.AllStates` is the canonical set, and every project's
`pipeline.config.json` maps every member of it to a tracker state name.
Config validation requires the whole mapping, so a state added here
without the corresponding line in a project's config makes that project's
config invalid — and validation runs before anything else, so **every**
`pipeline` command fails, not just the one that needed the new state. The
sweep stops; nothing dispatches, promotes or reverts.

That strictness is deliberate: a state the pipeline will try to write
needs a tracker name, and failing at config load beats failing mid-flight.
The order is the part that has to be got right.

1. Add the state's line to every project config, and merge that first.
2. Then merge the change here.
3. Then `pipeline setup --apply` on each project, which creates the
   tracker state — `setup` enumerates `AllStates`, so it needs no edit.

Adding `ready_for_design` in the other order took Catapult's pipeline
down for about ninety minutes, with a ticket sitting in a queue nothing
was sweeping.

## Adding a config field is that same change in reverse

`config.Load` calls `dec.DisallowUnknownFields()`, so a project config
naming a key this binary does not declare fails to load. Validation runs
before anything else, so **every** `pipeline` command fails, not just the
one that wanted the key — the same blast radius as the state case, from
the opposite end.

So the order inverts:

| the change | merges first | because |
| --- | --- | --- |
| a new protocol state | the project config | validation requires the *whole* state mapping, so a state we know and it lacks is invalid |
| a new config field | this repo | `DisallowUnknownFields` rejects extras, so a key it has and we lack is invalid |

Config validation is strict in both directions; which side moves first
depends on which kind of strictness the change trips. Getting it wrong
does not fail gracefully in either direction.

This is not hypothetical either. Catapult's `citationShorthands` edit
landed on its PR ahead of the field being declared here, and that PR's
CI went red at the `pipeline audit` step with `json: unknown field
"citationShorthands"` — the whole audit down, not the one check.

**Declaring the outer field is not enough.** `DisallowUnknownFields` is
a decoder setting, not a top-level one — measured, with a bogus key
inside an existing nested struct:

```
nested unknown field -> json: unknown field "totallyBogusField"
```

A config using a value shape whose fields this binary has not declared
still fails to load. Declare the whole shape, not the entry point.

## A gate is not built until it has run against a real tree

Three false-positive classes in the citation check were found by running
it over Catapult and reading the output. **None was visible from its unit
tests**, and each would have reported correct citations as dangling:
anchoring on `docs/` matched the substring inside `seed-docs/...` (2);
matching bare filenames read `dsl-syntax.md §15.1` as a repo-root path
(262); and a heading parser accepting only numeric sections missed the
v4 spec's part-lettered `### A.1.4` (17).

Two false failures on a gate is how a suppression gets added, and a
suppressed gate re-blinds itself to the next finding — the reasoning
`mix.exs`'s own `ignore_advisories` already carries. A gate whose output
nobody has read on a real corpus is a guess about that corpus.

Read the output again once the shorthand map landed, and the lesson
repeats one level up: **59 findings on Catapult, 4 of them rot.** The
message is accurate on every one and the diagnosis is still not in it.
34 were correct citations of a *second* document — that project spends
one `v4` shorthand on both a spec numbered `A.1.4` and a bundle
reference numbered `1.4`, and a map entry holds one path, so every
bare-numbered citation resolved against the wrong file. 19 cited a
numbered list item under a heading, which §4 rules dangling on purpose.
2 were prose quoting a bad citation. A count of findings is not a count
of defects, and the first session to read this list took the largest
class for a config that could not resolve rather than one pointed at the
wrong document.

## CI sweeps the merge ref, so the branch head is the wrong thing to reproduce from

`pull_request` checks out `refs/pull/N/merge` — the branch merged with
current `main` — and the project workflows take that default
(`actions/checkout@v4` with no `ref:`; catapult's own comment beside it
says "a shallow PR-merge checkout"). Two things follow, and the first
one cost a wrong prediction: running the audit against a branch head
locally gives a **different answer** than CI, so a reproduction has to
`git merge origin/main` into the branch first, or check out
`refs/pull/N/merge`. A ticket branch predating a base-branch fix
reported 59 dangling citations locally and none in CI, and neither
number was wrong.

The second: a ticket branch does **not** need a merge-from-main to pick
up a fix that has landed on the base. CI already tests the merged
result.

## A re-run is not an event, and GITHUB_TOKEN can start one

Measured on `orchestration-dummy` before anything relied on it, because
the neighbouring rule is real and would have made this a silent no-op:
GitHub does not start a workflow run from an *event* created with
`GITHUB_TOKEN`. A re-run is an API instruction rather than an event and
is not covered. A probe mirroring the sweep's auth exactly — same
`github.token`, same lone `actions: write` — took a completed
`pull_request` run from attempt 1 to attempt 2:

```
attempt_before: 1
http_status: 201
poll 1: run_attempt=1 (was 1)
poll 2: run_attempt=2 (was 1)
RESULT: GITHUB_TOKEN re-run STARTED a new attempt (1 -> 2)
```

The new attempt kept `event: pull_request` and recorded
`github-actions[bot]` as its triggering actor. So the sweep's existing
credential and its already-declared permission are enough; no project's
workflows need editing for it.

**The 201 is not the answer, and neither is a single check afterwards.**
`run_attempt` still read 1 on the poll taken straight after the call and
2 about six seconds later. A caller that reads once and concludes
nothing happened will be wrong, which is why `rerunStaleVerdict`
watches the attempt move rather than trusting the status.

Two incidental facts from taking the measurement: the GitHub App
credential these sessions use cannot dispatch a workflow at all (`403
Resource not accessible by integration`), so the probe was triggered by
a push instead; and `workflow_dispatch` would have been the wrong
primitive three times over anyway — a project's `ci.yml` declares `on:
pull_request` and would not accept it, `github.head_ref` is empty
outside that event so the audit's gate skips and reports a green that
checked nothing, and `runsForSHA` drops `workflow_dispatch` runs on
purpose so the verdict would never be read.

## `status` is not `conclusion`, and the adapter used to read only one

Measured on Catapult's own design-agent runs, 2026-08-31:

```
33431474175  design ORC-181  status=completed  conclusion=cancelled
33430600999  design ORC-177  status=completed  conclusion=success
33429431365  design ORC-181  status=completed  conclusion=failure
```

A cancelled run, a failed one and a successful one are all
`status: "completed"`. `agentRunsAt` read `status` alone, so all three
reached the plane as the single fact `Live: false` and the stale-claim
grace had to guess between them by waiting — while telling the reader to
go looking for a workflow file that failed to parse or a missing secret,
which is the wrong hunt for two of the three.

Only `success`, `failure` and `cancelled` are mapped, because those are
the three anyone here has seen. GitHub documents `neutral`, `skipped`,
`stale`, `timed_out`, `startup_failure` and `action_required` as well;
they map to `OutcomeUnknown`, which no rule acts on, so an unmeasured
value behaves exactly as no value always did. Widening that mapping is a
claim about the API and belongs with the measurement that supports it —
the same rule the `tracker.Memory` section above states for fakes.

## A project's workflows are the author's, and the author works here too

`pipeline.config.json` and `.github/workflows/**` are author-owned in
every project (DESIGN §5): an *agent's* push token carries no
`workflow` scope, so a commit from a run touching them is rejected and
takes the run down with it. That is a fact about agent runs and
nothing else. A session working in this repo with the author edits a
project's stubs freely, and logic goes where it belongs: a composite
action here and the stub that calls it in the project are one change,
landed together, with the project's copy edited in the same session.
Orchestration changes land while the project is drained — nothing
dispatched, nothing mid-flight — so there is no running pipeline for
the pair to straddle.

The failure this stops: the review replay was first built as a
workflow *here*, to leave catapult's workflows alone, and so needed a
second copy of the model credential in this repo's secrets and a read
token onto every project it measured — to read a history and a config
the project's own runner already holds. Actions secrets are per
repository; a workflow here cannot see a project's. It moved to a
project stub the day the secrets question was asked.

The two ordering rules above are not this rule's exceptions. They are
about validation, not ownership: a state or a config field has a side
that must merge first because the other side rejects it, drained or
not.

## The move store holds two pairs, and both are wired now

`internal/state` is the pipeline's record of its own writes. `Record`/`All`
are what §9's revert rules read: on a solo workspace the harness writes as
the author, so without them the sweep cannot tell the author's moves from
the pipeline's.

**Its own writes, literally — and this is where the author-facing
surprises come from.** `Plane.record` has exactly one call site, inside
the loop over the sweep's own actions, write-ahead of a move the sweep is
about to make. A move the *author* makes is never recorded, even one the
sweep looks at and permits. So the record sits where the pipeline last
wrote, and `arrival` computes the edge to judge as "recorded position →
tracker's current state".

Two consequences that cost real time before they were written down.

**The writer matrix judges a standing gap, not an event.** It re-fires on
every sweep until record and tracker agree, so suppressing the judgement
for one pass changes nothing about the gap it resumes judging. Borrowing
`author-only` as a pass for a single move was tried and measured: the
revert holds off while the label is on and lands the moment it comes off.
`resync` exists because closing the gap — adopting the ticket's state
into the record — is the only thing that works, and `ActAdopt` is the one
action in the system that writes the record and touches nothing else.

**An intermediate state the author passes through is invisible.** A
hand-made `Designing` → `Blocked` → `Done` is read as `Designing` →
`Done` and reverted by the done-writer rule, because the `Blocked` was
never recorded and `arrival` never saw it. The same two moves work when
the *pipeline* wrote the `Blocked` — which is why the cancelled-run rule
parking a ticket is also what makes the author's own close land. Catapult
ORC-181 spent an evening on the wrong side of this.

`Reserve`/`Release` — and the Durable Object's `/reserve` compare-and-set
behind them — landed as "Reservations: the storage half of closing the
dispatch race" (`966b5ae`) with **no caller outside their own package**,
and stayed that way for a milestone. The dispatching half is now written:
`plane.reserveDispatch` claims the agent kind before `Execute` calls the
host, and each agent's claim releases it after `core.VerifyPickup` (DESIGN
§6). The TTL is `plane.DispatchReservationTTL`.

**That TTL is a bound, not a measurement**, and it says so where it is
declared. The only figure anyone has taken for the window it covers is the
ninety seconds `core.VerifyPickup` records between a dispatch and the proof
it happened. Five minutes matches the Durable Object's own fallback
deliberately, so the two halves of one mechanism cannot disagree about how
long a lock lasts. If a project ever needs a different number it becomes a
`pipeline.config.json` key: declared here first, then set in the project,
in the order the config-field rule above gives — a key the binary has
not declared fails the project's every command.

Note also that DESIGN §1's store table lists Linear, the project repo, the
pipeline repo and the preview, and does not mention this store at all — its
rules live in §13 instead.

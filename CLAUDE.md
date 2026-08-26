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
```

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

Measured 2026-08-23, because the folk version of this is wrong: the Go
test cache does **not** serve a stale pass when you edit a file the test
reads at runtime. Breaking `.github/actions/agent-dev/action.yml` after a
cached `ok` re-ran the test and failed it. A `(cached)` line is not a
reason to distrust a result, and `go clean -testcache` between probes is
insurance, not a requirement.

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

## The project repos are not this repo

`pipeline.config.json` and `.github/workflows/**` are author-owned in
every project (DESIGN §5): agent push tokens carry no `workflow` scope,
so a commit touching them is rejected and takes the run down with it.
When a change here needs one of those files edited, that edit is the
author's to make and belongs in its own change, ahead of this one.

## The move store holds two pairs, and both are wired now

`internal/state` is the pipeline's record of its own writes. `Record`/`All`
are what §9's revert rules read: on a solo workspace the harness writes as
the author, so without them the sweep cannot tell the author's moves from
the pipeline's.

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
`pipeline.config.json` key, which is an author-owned edit in that project
and belongs ahead of the change here.

Note also that DESIGN §1's store table lists Linear, the project repo, the
pipeline repo and the preview, and does not mention this store at all — its
rules live in §13 instead.

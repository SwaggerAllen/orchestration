# Baselines

`PLAN.md` C0. A baseline is the before-measurement `docs/ops-free-pipeline.md` §9 compares
against: does a ticket reach `Done` faster, and for fewer Actions minutes, than it did
before the cutover landed.

**It cannot be taken afterwards.** Every other step in C0–C6 is reversible with respect to
this one; this one is not, because the history it reads is in a tracker whose contents move
and a run list the host trims.

## A baseline file carries the queries that produced it

Each capture is `docs/baselines/<YYYY-MM-DD>.json`, holding **the request and the response
for every query below**, not a summary of them.

The reason is the rule this repo already applies to prose: a number nobody can re-derive is
a claim, and §9's comparison happens months later, in a session with no memory of which
filters were used. A file that carries its own derivation can be re-run; a file holding
`cycle time: 14h` cannot be, and the next pass cannot tell whether the later figure was
measured the same way.

This is cheap because the store's read side already echoes its own filters — a `/states`
response carries an `excluded` block naming the terminal categories, queue states and
state filters in force, "so a reader can tell an empty bucket from a filtered one". Commit
the response verbatim and the exclusions come with it.

## Take the per-state rows, not `agentTotal`

**The headline figure must be the per-state breakdown.** `/states` also returns an
`agentTotal` row summing five states, and it is the wrong thing to baseline: it mixes three
actors that this cutover moves independently.

| state | who | what moves it |
| --- | --- | --- |
| `designing`, `in_progress`, `reconciling` | agents | C1, C2, C6 |
| `checks` | CI | C3 and C5 — previews and the judge pass both add time here |
| `design_review` | **the author** | nothing in C0–C6 |

Baselined as one number, a cutover that slowed CI and an author who answered faster would
cancel out and report no change. The per-state rows separate them and cost nothing extra —
`/states` returns both.

**`design_review`'s membership in that list is a defect, and it fails the list's own
test.** The comment above `AGENT_STATES` in `worker/projectstats.ts` says the list is named
rather than derived from the tracker's category because `started` "also covers Blocked and
Boundary review — states where nothing is running and no minute is being spent". Nothing is
running in `Design review` either: DESIGN §3 gives that state to the **author**, and no
minute is spent while they read. It is the same shape as the `tracker.Memory` lesson in
CLAUDE.md — an enumeration that disagrees with the thing it claims to enumerate.

Fixing it is not a prerequisite for the baseline, because the baseline does not use the
rollup. Fix it whenever; the per-state rows are unaffected either way.

## Record the watermark, or the baseline is not comparable

`stats.CollectResult.Exhausted` reports a backfill that ran out of budget with pages
remaining — "the normal shape of a backfill spanning nights". A baseline taken over a
partial collection measures the part that had been collected, and a later capture over a
complete one would show a difference that is entirely the collector catching up.

So `/watermark` is the first query and its result is part of the artifact. If
`oldestComplete` has not reached the project's first ticket, say so in the file and take it
again when it has.

## The queries

Against `pipeline.config.json`'s `state.url` and `state.project`, with
`PIPELINE_STATE_TOKEN` as a bearer token. The path is `<url>/stats/<project><op>`.

```sh
BASE="$(jq -r .state.url pipeline.config.json)/stats/$(jq -r .state.project pipeline.config.json)"
AUTH="authorization: Bearer $PIPELINE_STATE_TOKEN"

curl -sS -H "$AUTH" "$BASE/watermark"                          # completeness, first
curl -sS -H "$AUTH" "$BASE/milestones"                          # the windows a later slice needs
curl -sS -H "$AUTH" "$BASE/states?on=completed"                 # per-state totals, all time
curl -sS -H "$AUTH" "$BASE/states?on=completed&bucket=month"    # the trend behind the total
curl -sS -H "$AUTH" "$BASE/minutes?by=workflow"                 # Actions minutes, all time
curl -sS -H "$AUTH" "$BASE/minutes?by=workflow&bucket=month"    # and its trend
```

Two choices in there that a later pass would otherwise re-derive, wrongly:

- **`on=completed`, not `on=created`.** The filter picks which timestamp the window is
  applied to, and the store's own comment notes they are different numbers: created asks
  what a period *produced*, completed asks what it *finished*. "How long did work take" is
  the second.
- **The monthly bucket is not decoration.** A single total cannot say whether the figure
  was already moving before the cutover. If the trend is steep, §9's later comparison has
  to account for it rather than attribute the whole delta to these changes.

The defaults left in force — terminal categories excluded, `backlog` and `todo` excluded,
boundary tickets excluded — are the right ones for "how long does a ticket take", and the
response echoes them, so they need no separate record.

## Re-running it

§9's comparison re-runs the file's own queries against the store as it stands then, and
writes a second dated file beside the first. Nothing is edited in place: a baseline that
gets updated is no longer a baseline.

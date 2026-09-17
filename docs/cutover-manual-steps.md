# The cutover's manual steps

Everything the C0–C6 work has deferred to a human or to a live system.
`PLAN.md` §6 owns the order of the *changes*; this owns the steps that are
not changes — provisioning, secrets, rehearsals, and the measurements that
can only be taken against something running.

**This file is a worklist, not an inventory, and the difference is why it
is allowed to exist.** `SETUP.md` §2 records what a hand-maintained copy
of a live configuration costs: one existed, was read by nothing, and
drifted from reality five times in an afternoon. A worklist is safe from
that because it has an end — every line is checked off or deleted, and
when the cutover is done the file goes. If it is still here with unchecked
lines a milestone from now, the lines are the problem, not the file.

Each entry names **what it is**, **what it blocks**, and **how you know it
worked** — the last because a step whose success nobody can state is a
step somebody will report done on the strength of having tried it.

---

## Blocking a merge

### 1. Provision the Render service from `render.yaml` — blocks `catapult#163`

Create the Blueprint, then set the four `sync: false` secrets in the
dashboard: `DELIVERY_GITHUB_TOKEN`, `DELIVERY_PROVISIONING_TOKEN`,
`FOUNDATION_ENDPOINT_SECRET_KEY_BASE`, `FOUNDATION_OPERATOR_TOKEN`.

*Worked when:* the service is live and `/health` answers with
`foundation: true` and main's real SHA.

### 2. Replace the two `REPLACE-AT-PROVISION` values — blocks `catapult#163`

- `pipeline.config.json`'s `deploy.endpoint` — the `srv-…` service id.
- The public URL, in `config/test.exs`'s `:live_base_url` and the three
  workflows reading `CATAPULT_BASE_URL`. `grep -rn REPLACE-AT-PROVISION`
  finds every site.

*Worked when:* `grep -rn REPLACE-AT-PROVISION` is empty, and `pipeline
setup` prints `deploy check: … answered` rather than failing or skipping.

Merging before this leaves the sweep unable to see deploys and every
`Merged` ticket rides to the deploy timeout — a symptom naming neither the
endpoint nor the token.

### 3. `RENDER_API_KEY` as a repo secret on Catapult — blocks deploy detection

Read-only; deploy detection is a single GET. The sweep, preflight and
admin workflows all read it.

*Worked when:* `pipeline preflight` lists "the service's deploys" as
checked rather than printing the `RENDER_API_KEY not set` line.

### 4. `pipeline setup --apply` per project — blocks C2

Creates the `conflict` label. `setup` enumerates the protocol's labels, so
it needs no edit.

*Worked when:* the label exists on the ORC team and `pipeline setup`
re-run reports nothing to create.

---

## Rehearsals — the exits the code cannot prove

### 5. C1: a squash commit carrying its own spec

Merge a rehearsal ticket on `orchestration-dummy` and check that the
squash commit contains `CHANGE.md`, and that `git log --follow -- CHANGE.md`
returns it.

**This is the one exit with a known open question.** `merge=ours` is a
*local* driver, so it applies in the dev job's `git merge origin/main` but
**not** to GitHub's server-side squash. Whether the squash commit carries
the spec is exactly what this rehearsal settles, and nothing in the tree
can answer it.

*Worked when:* `git show --stat <squash sha>` lists `CHANGE.md`.

### 6. C1: the `merge=ours` probe, including the run without the driver

`ops-free-pipeline.md` §10 asks for both halves. The half that matters is
the one *without* `git config merge.ours.driver true`: the driver is
defined in the agent job, and a probe that only ever runs with it
configured cannot tell "the driver works" from "there was no conflict".

*Worked when:* the driver-less run produces an ordinary conflict and the
configured run does not.

### 7. C2: a rehearsal conflict parks a ticket

Two tickets whose specs collide on the same rule id.

*Worked when:* the ticket carries the `conflict` label and a comment
naming both claims, and the mechanical predicate refused to resolve the
hunk.

### 8. C3: a merge detected through the Render adapter

*Worked when:* a ticket reaching `Merged` advances to `Done` on the
sweep's own poll, and `/health` reports the merge SHA.

### 9. C4: design review reads a storybook from a running preview

*Worked when:* a ticket in `Design review` carries a `preview` marker with
a Render URL that serves the storybook; and, separately, a deliberately
broken preview build produces the `state=failed` comment rather than
silence.

---

## Measurements — take them while the thing is running

### 10. Render's database connection limit — the pool arithmetic depends on it

`SETUP.md` §2's budget (`peak = 2 × containers × (both pools + 2)`, keep
under 19) is measured against **App Platform's 22-connection cluster**.
The formula is platform-independent; the 22 is not. Read the new limit off
the dashboard and redo the arithmetic before `FOUNDATION_POOL_SIZE` and
`ENGINE_EVENT_STORE_POOL_SIZE` are trusted.

*Worked when:* `SETUP.md` §2 names Render's limit instead of App
Platform's, with the peak recomputed.

### 11. Whether a preview can take a smaller database plan than production

A preview copies the datastore, so the database plan is paid per live
preview as well as for production. `render.yaml` picks `0.5c-1g` for
production, which already deviates from `ops-free-pipeline.md` §7.1's cost
table — §7.2 had flagged the smallest tier as a guess and likely
undersized for an EventStore alongside Oban and application data.

*Worked when:* either a preview-specific plan is set, or the cost table is
corrected to price previews at the production plan.

### 12. Preview provisioning time — the gate's latency on every PR

`ops-free-pipeline.md` §10 wants this, and C5's per-PR gate waits on it.
It also decides whether `deploy.timeout` is the right bound for the
"preview is not coming" comment or merely an available one.

*Worked when:* a figure exists, taken from real previews rather than from
Render's documentation.

### 13. What a judge pass costs against the subscription

§10 again, and it sets the per-PR tier's size: the selection exists
because a judge pass spends the subscription, and §4 makes the
subscription the ceiling on all of this.

*Worked when:* a figure exists for one pass, and the per-PR tier's
expected spend per PR follows from it.

### 14. The baseline, re-measured — `ops-free-pipeline.md` §9's check

The only claim any of this makes about whether it worked. Re-run after C5,
against `docs/baselines/2026-09-16.json`.

**The headline is the per-state rows, never `agentTotal`.** That rollup
sums three actors this cutover moves independently, and the baseline
already refuted the naive reading: 81.2% of ticket time is author latency,
which nothing in C0–C6 touches. As one number, slower CI and a faster
author cancel out and report no change.

*Worked when:* a dated file sits beside the baseline with the same queries
and the comparison is stated per state.

---

## Cleanup — after the cutover lands

### 15. Retire the Cloudflare Pages storybook

Delete the `catapult-storybook` Pages project, and the
`CLOUDFLARE_API_TOKEN` / `CLOUDFLARE_ACCOUNT_ID` repo secrets if nothing
else uses them. **Check the Worker first** — it has its own Cloudflare
credentials and they are not these.

### 16. Retire `DIGITALOCEAN_TOKEN` and the App Platform app

Only after §8 above has proved Render's detection works, because the
App Platform app is the rollback.

### 17. C4 merge 3 — remove the `Preview` field from `config.Config`

The third of the three merges. Only once Catapult's `preview` block is
gone from `main`, since `DisallowUnknownFields` makes the field's removal
and the block's presence mutually invalid.

### 18. Close or re-scope DESIGN §14's staging item

`ops-free-pipeline.md` §8.1 requires it "in the same change". §14 records
the intended shape as staging with a manual test gate between
`Reconciling` and `Merged`; the gate now sits in `Checks`, because a
per-PR preview removed the one-environment constraint that forced the
later placement.

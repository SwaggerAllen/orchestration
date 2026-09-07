# The metronome

TypeScript whose only job is punctuality: something happens, one
`workflow_dispatch` per affected project repo. Two triggers —

- **Linear webhook** for tracker changes, which is the common path and
  lands in seconds.
- **Hourly cron** for the two conditions nothing announces: stale
  claims and deploy timeouts are elapsed-time judgments (DESIGN §12).
  It is also the backstop for a dropped webhook.

CI hops need neither: they are GitHub-native events on the project
stub, along with `deployment_status` for the post-deploy check.
GitHub's own `schedule:` trigger is unused because it jitters 5–15
minutes under load, and that compounds across a ticket's six polled
hops (DESIGN §13).

**The Worker stays dumb — permanently** (PLAN §1). The webhook receiver
was the one extension DESIGN §13 held in reserve, and taking it changed
nothing about that rule: the Worker verifies a signature, routes by
project id, and POSTs. It reads no state name, no label, no ticket
field. Every protocol behavior lives in the Go binary.

## The door

The endpoint is public and the Worker holds a token that starts
workflows in every project repo, so verification is not a formality.
Rejected before anything is dispatched: bodies with no valid
`Linear-Signature` HMAC over the *raw* bytes, bodies altered after
signing, and timestamps outside a one-minute replay window.

A Worker with no `LINEAR_WEBHOOK_SECRET` rejects **everything** rather
than accepting everything. The pipeline then runs at the cron's pace —
slow, not open.

Tests are `*.test.ts` — `index.test.ts` and `projectstats.test.ts` — run by
`worker-deploy` before every deploy. The glob rather than a filename, so a
suite added later is not invisible to CI:

```sh
cd worker && node --test --experimental-strip-types *.test.ts
```

No framework and no dependencies — Node strips the types and supplies
the same Web Crypto the Worker runtime does, so the signature check is
exercised against the real primitive.

## Deploy

The `worker-deploy` workflow, on any push to `main` touching this
directory or on demand from the Actions tab. It uploads
`DISPATCH_TOKEN` from the repo secret as part of the deploy, so there
is one value to change rather than two that can disagree.

By hand, when you have a laptop and wrangler auth:

```sh
cd worker
npx wrangler deploy
npx wrangler secret put DISPATCH_TOKEN   # only needed on this path
```

## One Worker, several projects

`PROJECTS` in `wrangler.toml` is a JSON array; each entry is a repo,
its sweep stub, and the Linear project id that routes webhooks to it.
Adding a project is an edit here — merging it redeploys — plus adding
that repo to the token's repository list, which is a separate act in
GitHub's settings. One cron and one Linear webhook serve them all, and
a failing dispatch is logged and skipped rather than stopping the
others — the sweep is convergent, so a missed beat costs latency, never
correctness.

Routing has two fallbacks that lean opposite ways, on purpose. A
payload whose shape hides the project id wakes **every** project: a
wasted sweep is a no-op, a dropped hop is real latency. A payload
naming a project not in the list wakes **nothing**: the ORC team holds
projects this pipeline does not manage, and their activity is not ours
to spend runs on.

`DISPATCH_TOKEN` is a fine-grained PAT with **Actions: read and write**
on exactly the listed repos. That is the least powerful credential in
the system: it can start the sweep and nothing else, while the project
repos' own workflows already hold contents:write, merge rights and the
model key.

**When to split into one Worker per project instead:** only when you
want per-repo tokens, so no single credential can start workflows in
two projects. Then each Worker carries a one-entry `PROJECTS` list and
its own secret. Note that this buys no operational independence beyond
the token — see the kill switch below.

## Coalescing the beat

Webhooks arrive in bursts. One logical change — a ticket moving state —
produces the state change, its comment and its label edit as separate
events a second apart, and a reconcile pass that merges produces several
more. Catapult ran **six sweeps in ninety-nine seconds** one evening.
Actions bills each job rounded up to the minute, so that burst cost six
minutes to compute an answer that barely changed between them.

So webhook-driven sweeps go through `SweepDebounce`, a Durable Object
with one instance per repository. The first event of a quiet period arms
a five-second alarm; every event inside that window is recorded and does
nothing else; the alarm fires one sweep.

**Trailing, not leading**, and that is the part worth understanding. A
leading edge would fire on the *first* event of a burst — which is the
worst moment to read the tracker, because the burst *is* the tracker
mid-change, and a snapshot taken then is the one most likely to catch a
half-applied state. Waiting for the quiet at the end costs a few seconds
and reads a settled world.

The window never rolls forward. Later events in a burst do not push the
alarm back, or a busy enough project could defer its sweep indefinitely
— "wait for quiet" becoming "wait for the end of the workday".

**The cron does not go through it.** The hourly beat is the floor under
everything else: the thing that runs when webhooks are dropped,
misconfigured or rejected. It does not get a dependency on another
moving part to save a minute it spends once an hour. For the same
reason, a debounce that errors or is unreachable falls through to
dispatching directly — the beat matters and the saving does not.

## The move record

`/state/<project>/` is not part of the metronome. It is the harness
talking to a Durable Object holding one row per ticket: where the
pipeline last left it, and as which role.

It lives here because the alternatives cost more than the problem. The
tracker cannot answer "who moved this ticket" — on a solo workspace the
harness holds the author's Linear key, so every pipeline write arrives
wearing the author's identity, and a role read off that identity says
"control plane" for the human's moves too, which is the one role the
revert rules trust. Every DESIGN §9 invariant was off for as long as the
two shared an id. Fixing it with identities means a Linear seat per role
per month, to encode something the pipeline already knows about itself.

So the pipeline writes down what it is about to do, before doing it, and
the sweep compares. Agreement means the pipeline made the last move;
disagreement means somebody else did.

Authenticated with `STATE_TOKEN`, a shared secret, because the harness
runs in Actions and cannot present anything better. The blast radius is
bounded by what the object does: it holds no credentials and can move
nothing. A forged write makes the sweep misjudge one ticket's
provenance, which is bad and is not the same order of bad as a token
that can start workflows. Unset closes the path entirely, which fails
safe — no store means the pipeline records nothing and judges nothing.

SQLite rather than the key-value API, because this is the first tenant
of the store DESIGN §13 has been pointing at, and the next things to
move here — run correlation, and the single-agent mutex a Durable Object
*is* by construction — want queries.

## Kill switch

Halting is not the Worker's job. `PIPELINE_KILL_SWITCH` is a **repo
variable** on each project, read by the sweep the Worker starts: set it
and that project plans nothing, while every other project keeps
running. One flag per project, in the project, and un-killing never
redeploys anything (DESIGN §13).

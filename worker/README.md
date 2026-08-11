# The metronome

~40 lines of TypeScript whose only job is punctuality: cron fires, one
`workflow_dispatch` call to the project repo's sweep stub. GitHub's own
`schedule:` trigger jitters 5–15 minutes under load; a Worker cron fires
within seconds (DESIGN §13).

**The Worker stays dumb — permanently** (PLAN §1). Every protocol
behavior lives in the Go binary. If polling latency ever justifies the
stage-2 escalation, this Worker grows exactly one trick — validating a
Linear webhook signature and forwarding into the same dispatch — and
still never learns what a ticket is.

## Deploy

```sh
cd worker
npx wrangler deploy
npx wrangler secret put DISPATCH_TOKEN
```

The token is a fine-grained PAT scoped to the project repo with
Actions read+write only — it can start the sweep and nothing else. One
Worker per project; edit `[vars]` in `wrangler.toml` per deployment.

## Kill switch

The Worker keeps ticking during a kill: the sweep it starts reads
`PIPELINE_KILL_SWITCH` (a project repo Actions variable) and plans
nothing. Halting dispatch at the sweep rather than the metronome means
one flag in one place, and un-killing needs no redeploy (DESIGN §13).

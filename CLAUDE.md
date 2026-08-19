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

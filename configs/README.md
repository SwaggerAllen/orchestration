# Project configs

Real (non-example) pipeline configs live here, one per target. IDs in
these files are not secrets — the `LINEAR_API_KEY` in the repo's Actions
secrets is what grants access.

## Setting up the scratch config

1. Create the scratch Linear team (and a project in it) by hand, once.
2. Add `LINEAR_API_KEY` to this repo's Actions secrets.
3. Locally (or from any machine with the key):
   `LINEAR_API_KEY=... go run ./cmd/pipeline ids`
   to print your viewer, team and project ids.
4. Copy `examples/pipeline.config.json` to `configs/scratch.config.json`,
   fill in the tracker ids and the `actors` table (your user id under
   `author`, the API key's viewer id under `controlplane`), commit.
5. Run the **verify-live** workflow (Actions → verify-live → Run
   workflow). With `apply` unchecked it dry-runs setup and sweep; with
   `apply` checked it provisions the team, asserts a second setup run is
   a no-op (the M0 gate), and runs a live sweep.

/**
 * The metronome (DESIGN §13, PLAN M7). A Cloudflare Worker cron that
 * fires the project repo's sweep workflow via workflow_dispatch —
 * punctual to seconds, where GitHub's own `schedule:` jitters 5-15
 * minutes under load and the jitter compounds across every hop of a
 * ticket's life.
 *
 * THE WORKER STAYS DUMB — PERMANENTLY (PLAN §1). No pipeline logic here,
 * ever. At the stage-2 escalation it grows exactly one trick: validating
 * a Linear webhook signature and forwarding into this same dispatch. All
 * protocol behavior lives in the Go binary; the Worker never learns what
 * a ticket is.
 */
export interface Env {
  /** owner/repo of the PROJECT repository (e.g. swaggerallen/orchestration-dummy) */
  GITHUB_REPOSITORY: string;
  /** sweep stub workflow filename in that repo */
  SWEEP_WORKFLOW: string;
  /** branch the dispatch targets (the stub lives on main) */
  DISPATCH_REF: string;
  /** fine-grained token, Actions read+write on the project repo ONLY (secret) */
  DISPATCH_TOKEN: string;
}

export default {
  async scheduled(_event: ScheduledEvent, env: Env, _ctx: ExecutionContext): Promise<void> {
    const url = `https://api.github.com/repos/${env.GITHUB_REPOSITORY}/actions/workflows/${env.SWEEP_WORKFLOW}/dispatches`;
    const res = await fetch(url, {
      method: "POST",
      headers: {
        authorization: `Bearer ${env.DISPATCH_TOKEN}`,
        accept: "application/vnd.github+json",
        "content-type": "application/json",
        "user-agent": "pipeline-metronome",
      },
      body: JSON.stringify({ ref: env.DISPATCH_REF || "main", inputs: {} }),
    });
    // 204 is success. Anything else is logged and dropped: the next tick
    // retries by existing, and the sweep is convergent, so a missed beat
    // costs latency, never correctness.
    if (res.status !== 204) {
      console.error(`metronome: dispatch failed: HTTP ${res.status} ${await res.text()}`);
    }
  },
};

/**
 * The metronome (DESIGN §13, PLAN M7). A Cloudflare Worker cron that
 * fires each project repo's sweep workflow via workflow_dispatch —
 * punctual to seconds, where GitHub's own `schedule:` jitters 5-15
 * minutes under load and the jitter compounds across every hop of a
 * ticket's life.
 *
 * THE WORKER STAYS DUMB — PERMANENTLY (PLAN §1). No pipeline logic here,
 * ever: a static list of dispatch targets is still just "cron → POST".
 * At the stage-2 escalation it grows exactly one trick — validating a
 * Linear webhook signature and forwarding into this same dispatch. All
 * protocol behavior lives in the Go binary; the Worker never learns what
 * a ticket is.
 *
 * Halting is not this Worker's job. PIPELINE_KILL_SWITCH is a repo
 * variable the sweep itself reads, so a project can be stopped and
 * restarted without touching or redeploying the metronome — which is
 * why one Worker can safely serve several projects.
 */
export interface Env {
  /**
   * JSON array of dispatch targets, e.g.
   *   [{"repository":"owner/repo","workflow":"pipeline-sweep.yml","ref":"main"}]
   * `ref` is optional and defaults to main.
   */
  PROJECTS: string;
  /**
   * Fine-grained token with Actions read+write on EVERY repository in
   * PROJECTS, and nothing else (secret). Want per-repo tokens instead?
   * Run one Worker per project — each with a one-entry PROJECTS list.
   */
  DISPATCH_TOKEN: string;
}

interface Project {
  repository: string;
  workflow: string;
  ref?: string;
}

function parseProjects(raw: string): Project[] {
  const parsed = JSON.parse(raw) as Project[];
  if (!Array.isArray(parsed) || parsed.length === 0) {
    throw new Error("PROJECTS must be a non-empty JSON array");
  }
  for (const p of parsed) {
    if (!p.repository || !p.workflow) {
      throw new Error(`PROJECTS entry needs repository and workflow: ${JSON.stringify(p)}`);
    }
  }
  return parsed;
}

async function dispatch(p: Project, token: string): Promise<void> {
  const url = `https://api.github.com/repos/${p.repository}/actions/workflows/${p.workflow}/dispatches`;
  const res = await fetch(url, {
    method: "POST",
    headers: {
      authorization: `Bearer ${token}`,
      accept: "application/vnd.github+json",
      "content-type": "application/json",
      "user-agent": "pipeline-metronome",
    },
    body: JSON.stringify({ ref: p.ref || "main", inputs: {} }),
  });
  // 204 is success. Anything else is logged and dropped: the next tick
  // retries by existing, and the sweep is convergent, so a missed beat
  // costs latency, never correctness.
  if (res.status !== 204) {
    console.error(`metronome: ${p.repository}: HTTP ${res.status} ${await res.text()}`);
  }
}

export default {
  async scheduled(_event: ScheduledEvent, env: Env, _ctx: ExecutionContext): Promise<void> {
    let projects: Project[];
    try {
      projects = parseProjects(env.PROJECTS);
    } catch (err) {
      console.error(`metronome: bad PROJECTS config: ${err}`);
      return;
    }
    // Settled, not all-or-nothing: one project's outage must not stop
    // another project's beat.
    await Promise.allSettled(projects.map((p) => dispatch(p, env.DISPATCH_TOKEN)));
  },
};

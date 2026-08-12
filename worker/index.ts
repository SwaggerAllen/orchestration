/**
 * The metronome and webhook receiver (DESIGN §13, PLAN M7). A Cloudflare
 * Worker that fires each project repo's sweep via workflow_dispatch —
 * on a Linear webhook when the tracker changes, and on a slow cron for
 * the conditions no webhook announces.
 *
 * THE WORKER STAYS DUMB — PERMANENTLY (PLAN §1). DESIGN §13 pre-authorised
 * exactly one extension beyond "cron → POST", and this is it: validate a
 * Linear webhook signature and forward into the same dispatch. Note what
 * is still absent — the Worker reads no state name, no label, no ticket
 * field. It verifies, routes by project id, and POSTs. All protocol
 * behavior lives in the Go binary; the Worker never learns what a ticket
 * is.
 *
 * Why both triggers. Webhooks carry the tracker hops, and CI hops are
 * already GitHub-native events on the project stub, so the common path
 * is event-driven and lands in seconds. But stale claims and deploy
 * timeouts are elapsed-time conditions with nothing to subscribe to
 * (DESIGN §13), so the cron survives as their safety net — and as the
 * backstop for any webhook Cloudflare or Linear drops.
 *
 * Halting is not this Worker's job. PIPELINE_KILL_SWITCH is a repo
 * variable the sweep itself reads, so a project can be stopped and
 * restarted without touching or redeploying the metronome.
 */
export interface Env {
  /**
   * JSON array of dispatch targets, e.g.
   *   [{"repository":"owner/repo","workflow":"pipeline-sweep.yml",
   *     "ref":"main","trackerProject":"<linear project uuid>"}]
   * `ref` defaults to main. `trackerProject` routes webhooks; without
   * it every webhook fans out to every project.
   */
  PROJECTS: string;
  /**
   * Fine-grained token with Actions read+write on EVERY repository in
   * PROJECTS, and nothing else (secret).
   */
  DISPATCH_TOKEN: string;
  /**
   * The signing secret Linear shows when the webhook is created
   * (secret). Without it every webhook is rejected, which fails safe:
   * the cron still beats, so the pipeline slows rather than stops.
   */
  LINEAR_WEBHOOK_SECRET: string;
}

interface Project {
  repository: string;
  workflow: string;
  ref?: string;
  trackerProject?: string;
}

/** How far a webhook's own timestamp may be from now. Linear signs the
 * timestamp with the body, so this bounds replay of a captured request
 * to the window rather than forever. */
const REPLAY_TOLERANCE_MS = 60_000;

export function parseProjects(raw: string): Project[] {
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

/** Length-independent compare over two hex digests. Both are the same
 * length in every real call, so this is about not leaking where they
 * first differ, not about hiding the length. */
export function timingSafeEqual(a: string, b: string): boolean {
  if (a.length !== b.length) return false;
  let diff = 0;
  for (let i = 0; i < a.length; i++) diff |= a.charCodeAt(i) ^ b.charCodeAt(i);
  return diff === 0;
}

/**
 * Verifies Linear's HMAC-SHA256 over the RAW body. Raw matters: parsing
 * and re-serialising changes bytes, and the signature is over bytes.
 *
 * This is the whole door. The endpoint is public and holds a token that
 * can start workflows in every project repo, so a request that fails
 * here is dropped before anything is dispatched.
 */
export async function verifySignature(
  rawBody: string,
  header: string | null,
  secret: string,
): Promise<boolean> {
  if (!header || !secret) return false;
  const key = await crypto.subtle.importKey(
    "raw",
    new TextEncoder().encode(secret),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"],
  );
  const mac = await crypto.subtle.sign("HMAC", key, new TextEncoder().encode(rawBody));
  const hex = [...new Uint8Array(mac)].map((b) => b.toString(16).padStart(2, "0")).join("");
  return timingSafeEqual(hex, header.trim().toLowerCase());
}

/** A signed body is still replayable until its timestamp ages out. */
export function isFresh(webhookTimestamp: unknown, now: number): boolean {
  if (typeof webhookTimestamp !== "number" || !Number.isFinite(webhookTimestamp)) return false;
  return Math.abs(now - webhookTimestamp) <= REPLAY_TOLERANCE_MS;
}

/**
 * Digs the Linear project id out of a payload. Linear puts it in
 * different places depending on the resource type, and a shape we do
 * not recognise returns undefined rather than a guess — routeFor treats
 * that as "cannot route" and fans out, which costs a run rather than a
 * missed beat.
 */
export function projectIdOf(payload: any): string | undefined {
  const d = payload?.data;
  return d?.projectId ?? d?.project?.id ?? d?.issue?.projectId ?? d?.issue?.project?.id ?? undefined;
}

/**
 * Chooses which projects a webhook wakes.
 *
 * The two fallbacks differ on purpose. An unroutable payload fans out:
 * dropping a beat costs latency on a real hop, and an extra sweep is a
 * no-op. A payload naming a project we do not manage dispatches
 * nothing: the ORC team holds projects beyond ours, and their activity
 * is not our business.
 */
export function routeFor(projects: Project[], trackerProject?: string): Project[] {
  if (!projects.some((p) => p.trackerProject)) return projects; // routing unconfigured
  if (!trackerProject) return projects; // unrecognised payload shape
  return projects.filter((p) => p.trackerProject === trackerProject);
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
  // 204 is success. Anything else is logged and dropped: the next beat
  // retries by existing, and the sweep is convergent, so a missed beat
  // costs latency, never correctness.
  if (res.status !== 204) {
    console.error(`metronome: ${p.repository}: HTTP ${res.status} ${await res.text()}`);
  }
}

function projectsOrLog(env: Env): Project[] {
  try {
    return parseProjects(env.PROJECTS);
  } catch (err) {
    console.error(`metronome: bad PROJECTS config: ${err}`);
    return [];
  }
}

export default {
  /**
   * The webhook path. Answers Linear promptly and dispatches in the
   * background: a receiver that blocks its 200 on someone else's API
   * is a receiver that gets retried and eventually disabled.
   */
  async fetch(request: Request, env: Env, ctx: ExecutionContext): Promise<Response> {
    if (request.method !== "POST") {
      return new Response("method not allowed\n", { status: 405 });
    }
    const raw = await request.text();
    if (!(await verifySignature(raw, request.headers.get("linear-signature"), env.LINEAR_WEBHOOK_SECRET))) {
      console.error("metronome: rejected a webhook with a bad or missing signature");
      return new Response("bad signature\n", { status: 401 });
    }
    let payload: any;
    try {
      payload = JSON.parse(raw);
    } catch {
      return new Response("bad json\n", { status: 400 });
    }
    if (!isFresh(payload?.webhookTimestamp, Date.now())) {
      console.error("metronome: rejected a webhook outside the replay window");
      return new Response("stale timestamp\n", { status: 401 });
    }

    const targets = routeFor(projectsOrLog(env), projectIdOf(payload));
    if (targets.length === 0) {
      // Not an error: a Linear project we do not manage, or PROJECTS
      // failed to parse — which is already logged above.
      return new Response("no matching project\n", { status: 202 });
    }
    ctx.waitUntil(Promise.allSettled(targets.map((p) => dispatch(p, env.DISPATCH_TOKEN))));
    return new Response("dispatched\n", { status: 202 });
  },

  /**
   * The heartbeat. Hourly, because what is left for it is elapsed-time
   * only — stale claims and deploy timeouts, both past a grace period
   * measured in tens of minutes (DESIGN §12) — plus catching anything
   * the webhook path dropped.
   */
  async scheduled(_event: ScheduledEvent, env: Env, _ctx: ExecutionContext): Promise<void> {
    // Settled, not all-or-nothing: one project's outage must not stop
    // another project's beat.
    await Promise.allSettled(projectsOrLog(env).map((p) => dispatch(p, env.DISPATCH_TOKEN)));
  },
};

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
import { SweepDebounce } from "./debounce.ts";
import { ProjectState } from "./projectstate.ts";

// Re-exported because wrangler binds Durable Object classes from the
// entrypoint module, not from wherever they are defined.
export { SweepDebounce, ProjectState };

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
  /**
   * The secret set on each project repo's GitHub webhook (secret).
   * Unset means GitHub webhooks are rejected, which fails the same way
   * Linear's does: the cron still beats, so the pipeline slows rather
   * than stops.
   *
   * The bare name is not sloppiness next to LINEAR_WEBHOOK_SECRET:
   * Actions reserves the GITHUB_ prefix for secret names, so the
   * obvious GITHUB_WEBHOOK_SECRET cannot exist on the repository this
   * value is set on. Same reason DISPATCH_TOKEN is not GITHUB_DISPATCH_TOKEN.
   */
  WEBHOOK_SECRET: string;
  /**
   * The trailing-debounce Durable Object, one instance per repository.
   * Webhook-driven sweeps go through it; the cron does not (see below).
   */
  SWEEP_DEBOUNCE: DurableObjectNamespace;
  /**
   * The pipeline's record of its own writes, one instance per project.
   * Read and written by the harness over the /state path below.
   */
  PROJECT_STATE: DurableObjectNamespace;
  /**
   * Shared secret the harness presents on /state (secret). Unset closes
   * the path entirely, which fails safe: with no store the pipeline
   * records nothing and judges nothing.
   */
  STATE_TOKEN: string;
}

interface Project {
  repository: string;
  workflow: string;
  ref?: string;
  trackerProject?: string;
  /**
   * The project's CI workflow name, as it appears in `name:`. Only this
   * workflow's completion wakes a sweep — see githubTargets for why
   * that filter is load-bearing rather than an optimisation.
   */
  ciWorkflow?: string;
}

/** Default for Project.ciWorkflow. The stubs' comment already calls the
 * name load-bearing: the sweep correlates on it. */
const DEFAULT_CI_WORKFLOW = "ci";

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

/**
 * Verifies GitHub's HMAC-SHA256, which arrives as `sha256=<hex>` in
 * x-hub-signature-256. Same door as Linear's, different doorframe.
 */
export async function verifyGitHubSignature(
  rawBody: string,
  header: string | null,
  secret: string,
): Promise<boolean> {
  if (!header || !secret) return false;
  const [scheme, digest] = header.trim().split("=");
  if (scheme !== "sha256" || !digest) return false;
  const key = await crypto.subtle.importKey(
    "raw",
    new TextEncoder().encode(secret),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"],
  );
  const mac = await crypto.subtle.sign("HMAC", key, new TextEncoder().encode(rawBody));
  const hex = [...new Uint8Array(mac)].map((b) => b.toString(16).padStart(2, "0")).join("");
  return timingSafeEqual(hex, digest.toLowerCase());
}

/**
 * Names why a signature failed, without printing anything that would
 * help forge one. The digest prefixes are derived from the secret but
 * do not reveal it, and the lengths are what actually catch the common
 * causes: a secret that arrived with a newline on it, or a body that is
 * not the bytes GitHub signed.
 *
 * Worth the surface area. "Bad signature" is true of every one of these
 * and useless for all of them — it sends you to re-read two secrets you
 * cannot see, which is exactly the dead end this cost us.
 */
export async function signatureDiagnosis(
  rawBody: string,
  header: string | null,
  secret: string,
): Promise<string> {
  if (!secret) return "the Worker has no WEBHOOK_SECRET set";
  if (!header) return "the request carried no x-hub-signature-256 header (the webhook has no secret set)";
  const [scheme, digest] = header.trim().split("=");
  if (scheme !== "sha256") return `signature scheme ${scheme}, want sha256`;
  if (!digest) return "signature header has no digest after sha256=";
  const key = await crypto.subtle.importKey(
    "raw",
    new TextEncoder().encode(secret),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"],
  );
  const mac = await crypto.subtle.sign("HMAC", key, new TextEncoder().encode(rawBody));
  const hex = [...new Uint8Array(mac)].map((b) => b.toString(16).padStart(2, "0")).join("");
  const trimmed = secret.trim();
  const whitespace =
    trimmed === secret ? "" : `; the Worker's secret has leading or trailing whitespace (${secret.length} chars, ${trimmed.length} trimmed) — a settings box that keeps a stray newline is the usual cause`;
  return `digest mismatch: got ${digest.slice(0, 10)}…, computed ${hex.slice(0, 10)}… over ${rawBody.length} chars with a ${secret.length}-char secret${whitespace}`;
}

/**
 * Chooses which projects a GitHub webhook wakes, and — more to the
 * point — which it must not.
 *
 * This route exists because GitHub will not do it in-repo. Events
 * created with GITHUB_TOKEN do not start workflow runs (workflow_dispatch
 * and repository_dispatch excepted), so when the dev agent pushes and
 * `ci` goes green, the stub's `workflow_run` trigger never fires. It
 * fires fine for commits a human pushed, which is why this looked
 * healthy: the broken case is the only case that matters. Measured on
 * ORC-1 — CI green at 02:03:17, no sweep, the ticket waiting on the
 * hourly beat. Webhook delivery is not workflow triggering, so the
 * guard does not reach it.
 *
 * THE FILTER IS THE SAFETY PROPERTY, NOT A TUNING CHOICE. A sweep run
 * completing is itself a workflow_run event. Waking a sweep on any
 * completed run would have each sweep dispatch the next one, forever,
 * against a token that can start workflows in every project repo. So a
 * run only counts when it is that project's CI workflow by name, and
 * anything else — the sweep, the agents, the previews — is dropped.
 */
export function githubTargets(projects: Project[], event: string | null, payload: any): Project[] {
  const repo = payload?.repository?.full_name;
  if (typeof repo !== "string" || repo === "") return [];
  // Case-insensitively, because the two sides disagree in practice and
  // always will: GitHub sends full_name in the owner's canonical case
  // ("SwaggerAllen/..."), while PROJECTS is typed by hand and this
  // repo's own config says "swaggerallen/...". An exact compare would
  // route nothing, silently, and look exactly like a webhook that was
  // never delivered.
  const want = repo.toLowerCase();
  const mine = projects.filter((p) => p.repository.toLowerCase() === want);
  if (mine.length === 0) return []; // a repo we do not manage

  switch (event) {
    case "workflow_run": {
      if (payload?.action !== "completed") return [];
      const name = payload?.workflow_run?.name;
      return mine.filter((p) => name === (p.ciWorkflow || DEFAULT_CI_WORKFLOW));
    }
    // The Merged -> Done hop has the same hole: the deploy record is
    // written with GITHUB_TOKEN, so the stub's deployment_status trigger
    // is suppressed too. No loop risk here — nothing the pipeline runs
    // creates a deployment except the recorder itself, which is not a
    // sweep.
    case "deployment_status":
      return mine;
    // ping is what GitHub sends when the webhook is created. Answering
    // it without dispatching is how the setup page shows a green tick.
    default:
      return [];
  }
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

/**
 * Hand a project's sweep to its debounce window instead of dispatching
 * now. One Durable Object per repository, so two projects never queue
 * behind each other.
 *
 * A failure here falls through to dispatching directly. The debounce is
 * an optimisation and the beat is not: if the Durable Object is
 * unreachable, the right outcome is a sweep that costs a minute, not a
 * sweep that never happens.
 */
async function debounced(p: Project, env: Env): Promise<void> {
  try {
    const id = env.SWEEP_DEBOUNCE.idFromName(p.repository);
    const res = await env.SWEEP_DEBOUNCE.get(id).fetch("https://debounce/sweep", {
      method: "POST",
      body: JSON.stringify({
        repository: p.repository,
        workflow: p.workflow,
        ref: p.ref || "main",
      }),
    });
    if (res.ok) {
      return;
    }
    console.error(`metronome: debounce for ${p.repository} answered HTTP ${res.status}; dispatching directly`);
  } catch (err) {
    console.error(`metronome: debounce for ${p.repository} unreachable (${err}); dispatching directly`);
  }
  await dispatch(p, env.DISPATCH_TOKEN);
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
    // The state path, before the webhook handling: it is the harness
    // talking to us rather than a tracker, it authenticates differently,
    // and it answers GET.
    const path = new URL(request.url).pathname;
    if (path.startsWith("/state/")) {
      // /state/<project>/all | /state/<project>/record — the project
      // segment is the Durable Object's name, so two projects never
      // share a row and neither can read the other's.
      const rest = path.slice("/state/".length);
      const slash = rest.indexOf("/");
      if (slash <= 0) {
        return new Response("expected /state/<project>/<op>\n", { status: 404 });
      }
      const project = rest.slice(0, slash);
      const op = rest.slice(slash);
      const id = env.PROJECT_STATE.idFromName(project);
      return env.PROJECT_STATE.get(id).fetch(
        new Request(`https://state${op}`, {
          method: request.method,
          headers: { authorization: request.headers.get("authorization") ?? "" },
          body: request.method === "POST" ? await request.text() : undefined,
        }),
      );
    }

    if (request.method !== "POST") {
      return new Response("method not allowed\n", { status: 405 });
    }
    const raw = await request.text();

    // Which sender, decided before verifying: the two sign differently,
    // and checking a GitHub body against Linear's scheme would reject
    // it for the wrong reason. The header is a routing hint only —
    // nothing is dispatched until that sender's own signature passes.
    const ghEvent = request.headers.get("x-github-event");
    if (ghEvent) {
      const ghSig = request.headers.get("x-hub-signature-256");
      if (!(await verifyGitHubSignature(raw, ghSig, env.WEBHOOK_SECRET))) {
        // Say which failure it was, in the log and in the body. "Bad
        // signature" alone sends you to compare two secrets you cannot
        // see, and the answer is usually neither of them: a body that
        // was not the bytes GitHub signed, or a secret that picked up
        // whitespace on the way into a settings box.
        console.error(
          `metronome: rejected a GitHub webhook — ${await signatureDiagnosis(raw, ghSig, env.WEBHOOK_SECRET)}` +
            ` (event=${ghEvent} bodyBytes=${new TextEncoder().encode(raw).length}` +
            ` contentType=${request.headers.get("content-type")})`,
        );
        return new Response(`bad signature: ${await signatureDiagnosis(raw, ghSig, env.WEBHOOK_SECRET)}\n`, {
          status: 401,
        });
      }
      let payload: any;
      try {
        payload = JSON.parse(raw);
      } catch {
        return new Response("bad json\n", { status: 400 });
      }
      // No freshness check, unlike Linear, and deliberately: GitHub
      // signs no timestamp, and the nearest stand-in — a field off the
      // payload — would drop real events whenever Actions queues. That
      // is not hypothetical: the run that exposed this bug sat queued
      // for eight minutes. A replayed webhook costs one extra sweep,
      // which is a no-op, so the trade runs the other way here.
      const targets = githubTargets(projectsOrLog(env), ghEvent, payload);
      if (targets.length === 0) {
        // ping, a run that is not CI, a repo we do not manage — all
        // ordinary, and all answered 202 so GitHub keeps the hook green.
        return new Response("nothing to dispatch\n", { status: 202 });
      }
      ctx.waitUntil(Promise.allSettled(targets.map((p) => debounced(p, env))));
      return new Response("dispatched\n", { status: 202 });
    }

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
    ctx.waitUntil(Promise.allSettled(targets.map((p) => debounced(p, env))));
    return new Response("dispatched\n", { status: 202 });
  },

  /**
   * The heartbeat. Hourly, because what is left for it is elapsed-time
   * only — stale claims and deploy timeouts, both past a grace period
   * measured in tens of minutes (DESIGN §12) — plus catching anything
   * the webhook path dropped.
   */
  async scheduled(_event: ScheduledEvent, env: Env, _ctx: ExecutionContext): Promise<void> {
    // Direct, not debounced. The hourly beat is the floor under
    // everything else — the thing that runs when webhooks are dropped,
    // misconfigured or rejected — so it does not get a dependency on
    // another moving part to save a minute it only spends once an hour.
    //
    // Settled, not all-or-nothing: one project's outage must not stop
    // another project's beat.
    await Promise.allSettled(projectsOrLog(env).map((p) => dispatch(p, env.DISPATCH_TOKEN)));
  },
};

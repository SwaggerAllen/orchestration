/**
 * Run with: node --test --experimental-strip-types index.test.ts
 * (from worker/ — the same command CI runs; passing the directory
 * instead makes Node try to load `worker` as a module and fail).
 *
 * No test framework and no dependencies, matching the rest of the repo.
 * Node's own runner strips the types and supplies Web Crypto, which is
 * the same API the Worker runtime gives us — so the signature check is
 * exercised against the real primitive rather than a stub.
 */
import { test } from "node:test";
import assert from "node:assert";

import { SweepDebounce } from "./debounce.ts";
import {
  isFresh,
  parseProjects,
  projectIdOf,
  routeFor,
  timingSafeEqual,
  verifyGitHubSignature,
  verifySignature,
  githubTargets,
  signatureDiagnosis,
  projectNames,
} from "./index.ts";
import { CHART_JS_PATH } from "./index.ts";
import { dashboardHTML } from "./dashboard.ts";
import { CHART_JS, CHART_JS_SHA256, CHART_JS_VERSION } from "./vendor/chartjs.ts";

const SECRET = "linear-signing-secret";

async function sign(body: string, secret = SECRET): Promise<string> {
  const key = await crypto.subtle.importKey(
    "raw",
    new TextEncoder().encode(secret),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"],
  );
  const mac = await crypto.subtle.sign("HMAC", key, new TextEncoder().encode(body));
  return [...new Uint8Array(mac)].map((b) => b.toString(16).padStart(2, "0")).join("");
}

test("a correctly signed body verifies", async () => {
  const body = JSON.stringify({ action: "update", type: "Issue" });
  assert.equal(await verifySignature(body, await sign(body), SECRET), true);
});

test("the endpoint is shut without a valid signature", async (t) => {
  const body = JSON.stringify({ action: "update", type: "Issue" });
  const good = await sign(body);

  await t.test("wrong secret", async () => {
    assert.equal(await verifySignature(body, await sign(body, "not-the-secret"), SECRET), false);
  });
  await t.test("body altered after signing", async () => {
    assert.equal(await verifySignature(body + " ", good, SECRET), false);
  });
  await t.test("missing header", async () => {
    assert.equal(await verifySignature(body, null, SECRET), false);
  });
  await t.test("empty header", async () => {
    assert.equal(await verifySignature(body, "", SECRET), false);
  });
  // An unset secret must not turn into "everything verifies". A Worker
  // deployed without LINEAR_WEBHOOK_SECRET has to reject every webhook
  // and let the cron carry the load. Checked against a well-formed
  // header, since the guard has to fire on the secret rather than on
  // the header being obviously junk.
  await t.test("unset secret rejects every webhook", async () => {
    assert.equal(await verifySignature(body, good, ""), false);
    assert.equal(await verifySignature(body, "0".repeat(64), ""), false);
  });
});

test("signatures compare case-insensitively on hex but not loosely", async () => {
  const body = JSON.stringify({ ok: true });
  const hex = await sign(body);
  assert.equal(await verifySignature(body, hex.toUpperCase(), SECRET), true);
  assert.equal(await verifySignature(body, hex.slice(0, -1) + "0", SECRET), false);
});

test("timingSafeEqual is still a correct comparison", () => {
  assert.equal(timingSafeEqual("abc", "abc"), true);
  assert.equal(timingSafeEqual("abc", "abd"), false);
  assert.equal(timingSafeEqual("abc", "ab"), false);
  assert.equal(timingSafeEqual("", ""), true);
});

test("a signed body is only replayable inside the window", () => {
  const now = 1_800_000_000_000;
  assert.equal(isFresh(now, now), true);
  assert.equal(isFresh(now - 59_000, now), true);
  assert.equal(isFresh(now - 61_000, now), false, "an old capture must not replay");
  assert.equal(isFresh(now + 61_000, now), false, "nor one dated into the future");
  assert.equal(isFresh(undefined, now), false);
  assert.equal(isFresh("1800000000000", now), false, "a string timestamp is not a timestamp");
  assert.equal(isFresh(NaN, now), false);
});

test("the project id is found wherever Linear puts it", () => {
  assert.equal(projectIdOf({ data: { projectId: "p1" } }), "p1");
  assert.equal(projectIdOf({ data: { project: { id: "p2" } } }), "p2");
  assert.equal(projectIdOf({ data: { issue: { projectId: "p3" } } }), "p3");
  assert.equal(projectIdOf({ data: { issue: { project: { id: "p4" } } } }), "p4");
  assert.equal(projectIdOf({ data: {} }), undefined);
  assert.equal(projectIdOf({}), undefined);
  assert.equal(projectIdOf(null), undefined);
});

const DUMMY = { repository: "o/dummy", workflow: "s.yml", trackerProject: "p-dummy" };
const REAL = { repository: "o/real", workflow: "s.yml", trackerProject: "p-real" };

test("routing wakes only the project the webhook names", () => {
  assert.deepEqual(routeFor([DUMMY, REAL], "p-real"), [REAL]);
});

// The ORC team holds projects this pipeline does not manage. Their
// activity is not our business, and fanning out on it would spend a run
// per project per edit.
test("a project we do not manage wakes nothing", () => {
  assert.deepEqual(routeFor([DUMMY, REAL], "p-someone-elses"), []);
});

// The opposite bias: when we cannot tell, a wasted sweep is cheaper
// than a dropped hop, because the sweep is a no-op when there is
// nothing to do.
test("an unroutable payload wakes everything", () => {
  assert.deepEqual(routeFor([DUMMY, REAL], undefined), [DUMMY, REAL]);
});

test("routing that is not configured yet wakes everything", () => {
  const bare = [{ repository: "o/dummy", workflow: "s.yml" }];
  assert.deepEqual(routeFor(bare, "p-dummy"), bare);
});

test("PROJECTS must describe something dispatchable", () => {
  assert.deepEqual(parseProjects('[{"repository":"o/r","workflow":"s.yml"}]'), [
    { repository: "o/r", workflow: "s.yml" },
  ]);
  assert.throws(() => parseProjects("[]"), /non-empty/);
  assert.throws(() => parseProjects('[{"repository":"o/r"}]'), /needs repository and workflow/);
  assert.throws(() => parseProjects('{"repository":"o/r"}'), /non-empty/);
});

// ---- GitHub webhooks ------------------------------------------------
//
// This route exists because GitHub will not do the job in-repo: events
// created with GITHUB_TOKEN do not start workflow runs, so when the dev
// agent pushes and `ci` goes green, the stub's own workflow_run trigger
// never fires. It fires for commits a human pushed, which is exactly
// why the gap went unnoticed.

const GH_SECRET = "github-webhook-secret";

async function ghSign(body: string, secret = GH_SECRET): Promise<string> {
  return `sha256=${await sign(body, secret)}`;
}

const GH_PROJECTS = [
  { repository: "acme/app", workflow: "pipeline-sweep.yml", trackerProject: "proj_1" },
  { repository: "acme/other", workflow: "pipeline-sweep.yml", ciWorkflow: "build" },
];

function runEvent(repo: string, name: string, action = "completed") {
  return { action, repository: { full_name: repo }, workflow_run: { name } };
}

test("a correctly signed GitHub body verifies", async () => {
  const body = JSON.stringify(runEvent("acme/app", "ci"));
  assert.equal(await verifyGitHubSignature(body, await ghSign(body), GH_SECRET), true);
});

test("GitHub's door is shut without a valid signature", async (t) => {
  const body = JSON.stringify(runEvent("acme/app", "ci"));

  await t.test("wrong secret", async () => {
    assert.equal(await verifyGitHubSignature(body, await ghSign(body, "nope"), GH_SECRET), false);
  });
  await t.test("body altered after signing", async () => {
    assert.equal(await verifyGitHubSignature(body + " ", await ghSign(body), GH_SECRET), false);
  });
  await t.test("sha1 scheme is not accepted", async () => {
    const digest = (await ghSign(body)).slice("sha256=".length);
    assert.equal(await verifyGitHubSignature(body, `sha1=${digest}`, GH_SECRET), false);
  });
  await t.test("bare digest with no scheme", async () => {
    assert.equal(await verifyGitHubSignature(body, await sign(body, GH_SECRET), GH_SECRET), false);
  });
  await t.test("missing header", async () => {
    assert.equal(await verifyGitHubSignature(body, null, GH_SECRET), false);
  });
  await t.test("unset secret rejects rather than accepts", async () => {
    assert.equal(await verifyGitHubSignature(body, await ghSign(body), ""), false);
  });
});

// The one that matters. A sweep run completing is itself a
// workflow_run event, so waking a sweep on any completed run would have
// each sweep dispatch the next, forever, against a token that can start
// workflows in every project repo.
test("a sweep's own completion never dispatches another sweep", () => {
  for (const name of ["pipeline: sweep", "pipeline: dev ORC-1", "pipeline: design ORC-1", "pipeline: preview"]) {
    assert.deepEqual(
      githubTargets(GH_PROJECTS, "workflow_run", runEvent("acme/app", name)),
      [],
      `${name} must not wake a sweep`,
    );
  }
});

test("CI going green wakes exactly that repo's sweep", () => {
  const targets = githubTargets(GH_PROJECTS, "workflow_run", runEvent("acme/app", "ci"));
  assert.deepEqual(targets.map((p) => p.repository), ["acme/app"]);
});

test("a project can name its own CI workflow", () => {
  assert.deepEqual(
    githubTargets(GH_PROJECTS, "workflow_run", runEvent("acme/other", "build")).map((p) => p.repository),
    ["acme/other"],
  );
  // ...and the default name does not wake a project that renamed it.
  assert.deepEqual(githubTargets(GH_PROJECTS, "workflow_run", runEvent("acme/other", "ci")), []);
});

test("a CI run that has not finished yet is not green", () => {
  assert.deepEqual(githubTargets(GH_PROJECTS, "workflow_run", runEvent("acme/app", "ci", "requested")), []);
});

test("another org's repo dispatches nothing", () => {
  assert.deepEqual(githubTargets(GH_PROJECTS, "workflow_run", runEvent("someone/else", "ci")), []);
});

// The Merged -> Done hop has the same hole: the deploy record is written
// with GITHUB_TOKEN, so the stub's deployment_status trigger is
// suppressed the same way.
test("a deployment status wakes the sweep", () => {
  const payload = { action: "created", repository: { full_name: "acme/app" }, deployment_status: { state: "success" } };
  assert.deepEqual(
    githubTargets(GH_PROJECTS, "deployment_status", payload).map((p) => p.repository),
    ["acme/app"],
  );
});

// The terminal states wake the sweep and the in-flight ones do not.
//
// `success` is what the deploy check reads — the newest successful
// deployment, compared against the merge commit. `failure` and `error` are
// for previews: the platform builds a branch preview out-of-band, and a
// preview that failed is news the author in Design review can act on,
// where waiting for the hourly beat would hold the review open over a link
// that was never going to arrive.
//
// The in-flight half is the one with teeth. One deployment walking
// queued -> in_progress -> success is three webhooks minutes apart, which
// is three sweeps no five-second window can coalesce, and the first two
// carry nothing the sweep would act on. `inactive` is a superseded
// deployment, which is not news either.
const deploymentStatusWakes: Record<string, boolean> = {
  success: true,
  failure: true,
  error: true,
  queued: false,
  in_progress: false,
  pending: false,
  inactive: false,
};

test("a deployment status wakes the sweep only when it is terminal", () => {
  const at = (state: string) => ({
    action: "created",
    repository: { full_name: "acme/app" },
    deployment_status: { state },
  });
  for (const [state, wakes] of Object.entries(deploymentStatusWakes)) {
    assert.deepEqual(
      githubTargets(GH_PROJECTS, "deployment_status", at(state)).map((p) => p.repository),
      wakes ? ["acme/app"] : [],
      state,
    );
  }
});

// A payload with no deployment_status at all must not be read as success.
test("a deployment_status event with no state dispatches nothing", () => {
  const payload = { action: "created", repository: { full_name: "acme/app" } };
  assert.deepEqual(githubTargets(GH_PROJECTS, "deployment_status", payload), []);
});

test("ping and unknown events are accepted but dispatch nothing", () => {
  const payload = { zen: "Keep it logically awesome.", repository: { full_name: "acme/app" } };
  assert.deepEqual(githubTargets(GH_PROJECTS, "ping", payload), []);
  assert.deepEqual(githubTargets(GH_PROJECTS, "issues", payload), []);
});

test("a payload with no repository dispatches nothing", () => {
  assert.deepEqual(githubTargets(GH_PROJECTS, "workflow_run", { action: "completed" }), []);
});

// GitHub sends full_name in the owner's canonical case; PROJECTS is
// typed by hand and this repo's own config is lowercase. An exact
// compare routes nothing and is indistinguishable from a webhook that
// never arrived.
test("repository matching survives the two sides disagreeing on case", () => {
  const projects = [{ repository: "swaggerallen/orchestration-dummy", workflow: "pipeline-sweep.yml" }];
  const payload = runEvent("SwaggerAllen/orchestration-dummy", "ci");
  assert.deepEqual(
    githubTargets(projects, "workflow_run", payload).map((p) => p.repository),
    ["swaggerallen/orchestration-dummy"],
  );
});

// "Bad signature" is true of every failure below and useless for all of
// them: it sends you to compare two secrets you cannot see. Each cause
// gets named instead, which is the difference between a diagnosis and
// an afternoon.
test("a failed signature says which failure it was", async (t) => {
  const body = JSON.stringify(runEvent("acme/app", "ci"));

  await t.test("the Worker has no secret", async () => {
    assert.match(await signatureDiagnosis(body, await ghSign(body), ""), /no WEBHOOK_SECRET/);
  });
  await t.test("the sender set no secret, so signed nothing", async () => {
    assert.match(await signatureDiagnosis(body, null, GH_SECRET), /no x-hub-signature-256/);
  });
  await t.test("wrong scheme", async () => {
    assert.match(await signatureDiagnosis(body, "sha1=abc", GH_SECRET), /scheme sha1/);
  });
  await t.test("a real mismatch reports both digests and neither secret", async () => {
    const got = await signatureDiagnosis(body, await ghSign(body, "the-other-secret"), GH_SECRET);
    assert.match(got, /digest mismatch/);
    assert.ok(!got.includes(GH_SECRET), "the diagnosis must never print the secret");
    assert.ok(!got.includes("the-other-secret"), "nor the one that was tried");
  });
  // The cause I would bet on: a secret pasted into a settings box with a
  // newline riding along. It compares equal to the eye and to nothing
  // else, and GitHub's own Secret field cannot hold one to match it.
  await t.test("whitespace on the secret is called out by name", async () => {
    const got = await signatureDiagnosis(body, await ghSign(body, GH_SECRET), GH_SECRET + "\n");
    assert.match(got, /leading or trailing whitespace/);
  });
});

// ---------------------------------------------------------------------
// The trailing debounce.
//
// A tiny stand-in for the Durable Object storage API: enough to assert
// the two properties that matter, and nothing more. The real runtime is
// exercised by deploying, which no unit test substitutes for.
// ---------------------------------------------------------------------

function fakeState() {
  const map = new Map<string, unknown>();
  let alarm: number | null = null;
  return {
    fired: 0,
    storage: {
      put: async (k: string, v: unknown) => void map.set(k, v),
      get: async (k: string) => map.get(k),
      delete: async (k: string) => void map.delete(k),
      getAlarm: async () => alarm,
      setAlarm: async (t: number) => void (alarm = t),
    },
    clearAlarm() {
      alarm = null;
    },
    hasAlarm() {
      return alarm !== null;
    },
  };
}

const pending = JSON.stringify({
  repository: "owner/repo",
  workflow: "pipeline-sweep.yml",
  ref: "main",
});

function post() {
  return new Request("https://debounce/sweep", { method: "POST", body: pending });
}

test("a burst arms one alarm, not one per event", async () => {
  const state = fakeState();
  const dobj = new SweepDebounce(state as never, { DISPATCH_TOKEN: "t" });

  const first = await dobj.fetch(post());
  assert.equal(await first.text(), "armed\n");
  assert.ok(state.hasAlarm(), "the first event did not arm the window");

  // Five more inside the window. Each is recorded; none arms anything.
  for (let i = 0; i < 5; i++) {
    const res = await dobj.fetch(post());
    assert.equal(await res.text(), "coalesced\n", `event ${i + 2} was not coalesced`);
  }
});

test("the alarm dispatches exactly once for the whole burst", async () => {
  const state = fakeState();
  const dobj = new SweepDebounce(state as never, { DISPATCH_TOKEN: "t" });
  const calls: string[] = [];
  const realFetch = globalThis.fetch;
  globalThis.fetch = (async (url: string) => {
    calls.push(String(url));
    return new Response(null, { status: 204 });
  }) as never;
  try {
    for (let i = 0; i < 6; i++) {
      await dobj.fetch(post());
    }
    await dobj.alarm();
  } finally {
    globalThis.fetch = realFetch;
  }
  assert.equal(calls.length, 1, "six webhooks produced more than one dispatch");
  assert.match(calls[0], /owner\/repo\/actions\/workflows\/pipeline-sweep\.yml\/dispatches/);
});

// A rolling window would let a long enough burst defer the sweep
// indefinitely — "wait for quiet" becoming "wait for the end of the
// workday" on a busy project. The alarm fires a fixed time after the
// FIRST event of a burst and later events must not push it back.
test("later events in a burst do not push the alarm back", async () => {
  const state = fakeState();
  const dobj = new SweepDebounce(state as never, { DISPATCH_TOKEN: "t" });
  await dobj.fetch(post());
  const armedAt = await state.storage.getAlarm();
  for (let i = 0; i < 3; i++) {
    await dobj.fetch(post());
  }
  assert.equal(await state.storage.getAlarm(), armedAt, "the window was extended by a later event");
});

// After the alarm has fired, the next event starts a fresh window
// rather than being swallowed by the one that already went.
test("the window rearms after it fires", async () => {
  const state = fakeState();
  const dobj = new SweepDebounce(state as never, { DISPATCH_TOKEN: "t" });
  const realFetch = globalThis.fetch;
  globalThis.fetch = (async () => new Response(null, { status: 204 })) as never;
  try {
    await dobj.fetch(post());
    await dobj.alarm();
    state.clearAlarm(); // the runtime clears a fired alarm
    const res = await dobj.fetch(post());
    assert.equal(await res.text(), "armed\n", "the next burst was swallowed");
  } finally {
    globalThis.fetch = realFetch;
  }
});

// An alarm with nothing pending must not dispatch. It is how a fired
// alarm and a lost write tell themselves apart.
test("an alarm with no pending sweep dispatches nothing", async () => {
  const state = fakeState();
  const dobj = new SweepDebounce(state as never, { DISPATCH_TOKEN: "t" });
  let called = false;
  const realFetch = globalThis.fetch;
  globalThis.fetch = (async () => {
    called = true;
    return new Response(null, { status: 204 });
  }) as never;
  try {
    await dobj.alarm();
  } finally {
    globalThis.fetch = realFetch;
  }
  assert.equal(called, false, "an empty alarm dispatched a sweep");
});


// ---- the dashboard --------------------------------------------------

// The selector's names are a GUESS: the stats object is addressed by
// `state.project` from a project's own config — any stable string —
// while PROJECTS knows the repository. They are the same word today and
// nothing enforces it, so ?project= has to be able to override.
test("project names are derived from the repositories, as a starting point", () => {
  const names = projectNames({
    PROJECTS: JSON.stringify([
      { repository: "swaggerallen/catapult", workflow: "w.yml" },
      { repository: "swaggerallen/orchestration-dummy", workflow: "w.yml" },
    ]),
  });
  assert.deepEqual(names, ["catapult", "orchestration-dummy"]);
});

// The dashboard must not be able to take the Worker down. Everything
// else this Worker does — webhooks, dispatch, the move record — matters
// more than a chart.
test("an unparseable or absent PROJECTS yields no names rather than throwing", () => {
  assert.deepEqual(projectNames({ PROJECTS: "not json" }), []);
  assert.deepEqual(projectNames({}), []);
});

// The page loads nothing from another origin. It sits behind the Access
// application and shares an origin with the stats API, so a script it
// loads runs with the viewer's identity — a CDN outage would cost the
// charts and a CDN compromise would cost rather more.
//
// This pins the property rather than the absence of a script tag. It
// used to assert `<script src` never appeared at all, and vendoring
// Chart.js turned that red, correctly: the invariant was always "no
// FOREIGN code", and "no script tag" was a proxy that stopped being
// true. Guarded because swapping the vendored path for a CDN URL is a
// one-word edit that looks like a simplification.
test("the dashboard loads no cross-origin code", () => {
  const html = dashboardHTML({ projects: ["catapult"], chartSrc: "/dashboard/chart-4.5.1.js" });
  for (const bad of ["cdn.", "unpkg", "jsdelivr", "@import", "//esm.", "https://"]) {
    assert.ok(!html.includes(bad), `the page reaches out to ${bad}`);
  }
  for (const src of html.matchAll(/<(?:script|link)[^>]*(?:src|href)="([^"]*)"/g)) {
    assert.ok(src[1].startsWith("/"), `${src[1]} is not a same-origin path`);
  }
  // And it holds no credential. A page that carried STATE_TOKEN would
  // hand it to everyone who opened the page, which is the entire reason
  // the Access path exists.
  for (const bad of ["STATE_TOKEN", "Bearer ", "authorization"]) {
    assert.ok(!html.includes(bad), `the page carries ${bad}`);
  }
});

test("the dashboard's project list is the one it was given", () => {
  const html = dashboardHTML({ projects: ["catapult", "dummy"], chartSrc: "/x.js" });
  assert.ok(html.includes(JSON.stringify(["catapult", "dummy"])));
});

// The library's path carries its version, which is what makes the
// immutable cache header honest: a new version is a new URL rather than
// a stale cache nobody can bust.
test("the vendored library is served from a versioned path", () => {
  assert.equal(CHART_JS_PATH, `/dashboard/chart-${CHART_JS_VERSION}.js`);
  assert.ok(CHART_JS_PATH.includes(CHART_JS_VERSION), "the path does not carry the version");
  const html = dashboardHTML({ projects: [], chartSrc: CHART_JS_PATH });
  assert.ok(html.includes(`<script src="${CHART_JS_PATH}">`), "the page does not load it");
});

// THE BLOB IS RE-DERIVABLE, and this is what says so. A vendored
// dependency nobody can reproduce from upstream is worse than a link,
// because it can be neither audited nor updated with confidence: the
// digest is the upstream file's, so this fails if the base64 was
// hand-edited, truncated, or regenerated from something else.
//
// Reproduce it with:
//   npm pack chart.js@<version> && tar xzf chart.js-<version>.tgz
//   sha256sum package/dist/chart.umd.min.js
test("the vendored Chart.js matches the digest recorded beside it", async () => {
  const bytes = Uint8Array.from(CHART_JS, (c) => c.charCodeAt(0));
  const digest = await crypto.subtle.digest("SHA-256", bytes);
  const hex = [...new Uint8Array(digest)]
    .map((b) => b.toString(16).padStart(2, "0")).join("");
  assert.equal(hex, CHART_JS_SHA256);
  // And it is the library rather than some other file that hashes to
  // the same recorded value because both were regenerated together.
  assert.ok(CHART_JS.includes(`Chart.js v${CHART_JS_VERSION}`), "not the Chart.js banner");
  assert.ok(CHART_JS.includes("MIT License"), "the licence notice is gone");
});

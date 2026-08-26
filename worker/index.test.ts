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
} from "./index.ts";

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

// Only `success` can advance anything: the deploy check reads the newest
// successful deployment and compares its commit against the merge commit,
// and a failed deploy is caught by the deploy timeout rather than by an
// event. One deployment walking queued -> in_progress -> success is three
// webhooks minutes apart, which is three sweeps no five-second window can
// coalesce.
test("only a successful deployment status wakes the sweep", () => {
  const at = (state: string) => ({
    action: "created",
    repository: { full_name: "acme/app" },
    deployment_status: { state },
  });
  for (const state of ["queued", "in_progress", "failure", "error", "inactive"]) {
    assert.deepEqual(githubTargets(GH_PROJECTS, "deployment_status", at(state)), [], state);
  }
  assert.deepEqual(
    githubTargets(GH_PROJECTS, "deployment_status", at("success")).map((p) => p.repository),
    ["acme/app"],
  );
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

/**
 * Run with: node --test --experimental-strip-types worker/
 *
 * No test framework and no dependencies, matching the rest of the repo.
 * Node's own runner strips the types and supplies Web Crypto, which is
 * the same API the Worker runtime gives us — so the signature check is
 * exercised against the real primitive rather than a stub.
 */
import { test } from "node:test";
import assert from "node:assert";

import {
  isFresh,
  parseProjects,
  projectIdOf,
  routeFor,
  timingSafeEqual,
  verifySignature,
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

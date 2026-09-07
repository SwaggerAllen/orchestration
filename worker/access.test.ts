/**
 * The Access verification, tested against real RSA signatures.
 *
 * A fake that accepted a fake signature would test nothing: the whole
 * claim here is that a token has to be signed by the team's key, and a
 * stub verifier agreeing with a stub signer would prove only that two
 * stubs agree. So these mint real RS256 tokens with crypto.subtle and
 * serve real JWKs from a stubbed fetch.
 */
import { test } from "node:test";
import assert from "node:assert";

import { accessIdentity, __setAccessKeys } from "./access.ts";

const enc = new TextEncoder();

function b64url(bytes: Uint8Array): string {
  let bin = "";
  for (const b of bytes) bin += String.fromCharCode(b);
  return btoa(bin).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}
function b64urlText(s: string): string {
  return b64url(enc.encode(s));
}

async function keypair() {
  return crypto.subtle.generateKey(
    { name: "RSASSA-PKCS1-v1_5", modulusLength: 2048, publicExponent: new Uint8Array([1, 0, 1]), hash: "SHA-256" },
    true,
    ["sign", "verify"],
  );
}

async function jwkOf(pub: CryptoKey, kid: string) {
  const jwk = (await crypto.subtle.exportKey("jwk", pub)) as any;
  return { kid, kty: jwk.kty, n: jwk.n, e: jwk.e, alg: "RS256" };
}

async function mint(priv: CryptoKey, kid: string, payload: Record<string, unknown>) {
  const head = b64urlText(JSON.stringify({ alg: "RS256", kid, typ: "JWT" }));
  const body = b64urlText(JSON.stringify(payload));
  const sig = new Uint8Array(
    await crypto.subtle.sign("RSASSA-PKCS1-v1_5", priv, enc.encode(`${head}.${body}`)),
  );
  return `${head}.${body}.${b64url(sig)}`;
}

const ENV = { ACCESS_TEAM_DOMAIN: "team", ACCESS_AUD: "aud-1" };
const soon = () => Math.floor(Date.now() / 1000) + 600;

function req(jwt?: string): Request {
  return new Request("https://w/stats/catapult/states", {
    headers: jwt ? { "cf-access-jwt-assertion": jwt } : {},
  });
}

test("a token signed by the team's key, for this application, identifies its viewer", async () => {
  const { privateKey, publicKey } = await keypair();
  __setAccessKeys("team", [await jwkOf(publicKey, "k1")]);
  const jwt = await mint(privateKey, "k1", { aud: "aud-1", exp: soon(), email: "a@b.c" });
  assert.equal(await accessIdentity(req(jwt), ENV), "a@b.c");
});

// THE ONE THAT MATTERS. This Worker answers at its workers.dev origin,
// which nothing fronts — the webhooks post there — so a caller can set
// the header to anything. Reading it instead of verifying it would be
// an open store with an authentication-shaped comment above it.
test("a forged token is not an identity", async () => {
  const { privateKey, publicKey } = await keypair();
  const attacker = await keypair();
  __setAccessKeys("team", [await jwkOf(publicKey, "k1")]);

  // Signed by a key the team never published.
  const forged = await mint(attacker.privateKey, "k1", { aud: "aud-1", exp: soon(), email: "a@b.c" });
  assert.equal(await accessIdentity(req(forged), ENV), null);

  // Unsigned, with a plausible payload.
  const head = b64urlText(JSON.stringify({ alg: "RS256", kid: "k1", typ: "JWT" }));
  const body = b64urlText(JSON.stringify({ aud: "aud-1", exp: soon(), email: "a@b.c" }));
  assert.equal(await accessIdentity(req(`${head}.${body}.`), ENV), null);

  // "alg": "none", which is the classic way in when the verifier
  // believes the algorithm the token names.
  const noneHead = b64urlText(JSON.stringify({ alg: "none", kid: "k1", typ: "JWT" }));
  assert.equal(await accessIdentity(req(`${noneHead}.${body}.`), ENV), null);

  // Not a JWT at all.
  assert.equal(await accessIdentity(req("bearer-ish"), ENV), null);
  assert.equal(await accessIdentity(req(), ENV), null);
  void privateKey;
});

// One Access team signs for every application in it with the same keys,
// so a token minted for some other application is genuinely valid — it
// is simply not for this door.
test("a valid token for another application in the same team is refused", async () => {
  const { privateKey, publicKey } = await keypair();
  __setAccessKeys("team", [await jwkOf(publicKey, "k1")]);
  const other = await mint(privateKey, "k1", { aud: "aud-2", exp: soon(), email: "a@b.c" });
  assert.equal(await accessIdentity(req(other), ENV), null);
});

test("an expired or not-yet-valid token is refused", async () => {
  const { privateKey, publicKey } = await keypair();
  __setAccessKeys("team", [await jwkOf(publicKey, "k1")]);
  const now = Math.floor(Date.now() / 1000);
  const expired = await mint(privateKey, "k1", { aud: "aud-1", exp: now - 1, email: "a@b.c" });
  assert.equal(await accessIdentity(req(expired), ENV), null);
  const early = await mint(privateKey, "k1", { aud: "aud-1", exp: soon(), nbf: now + 600, email: "a@b.c" });
  assert.equal(await accessIdentity(req(early), ENV), null);
  const undated = await mint(privateKey, "k1", { aud: "aud-1", email: "a@b.c" });
  assert.equal(await accessIdentity(req(undated), ENV), null);
});

// The shipped state. Publishing the dashboard before the Access
// application exists must open nothing — otherwise the deploy that
// carries this code is the window.
//
// The fetch count is half the test, and it is the half a probe added.
// Without the configuration check the answer was still null, so
// asserting only the answer passed with the guard deleted — by way of a
// request to `https://undefined.cloudflareaccess.com/` on every call.
test("with Access unconfigured, no token is an identity and nothing is asked", async () => {
  const { privateKey, publicKey } = await keypair();
  __setAccessKeys("team", [await jwkOf(publicKey, "k1")]);
  const jwt = await mint(privateKey, "k1", { aud: "aud-1", exp: soon(), email: "a@b.c" });

  const realFetch = globalThis.fetch;
  let fetches = 0;
  globalThis.fetch = (async (...args: unknown[]) => {
    fetches++;
    return (realFetch as any)(...args);
  }) as typeof fetch;
  try {
    assert.equal(await accessIdentity(req(jwt), {}), null);
    assert.equal(await accessIdentity(req(jwt), { ACCESS_TEAM_DOMAIN: "team" }), null);
    assert.equal(await accessIdentity(req(jwt), { ACCESS_AUD: "aud-1" }), null);
    assert.equal(fetches, 0, "an unconfigured Worker asked the network about a token");
  } finally {
    globalThis.fetch = realFetch;
  }
});

// Algorithm confusion: a token whose header says HS256, signed with a
// real HMAC over the same bytes, keyed on the public modulus an attacker
// already has.
//
// Two lines refuse it independently — the RS256 header check and the
// verify call naming its own algorithm — so breaking either alone leaves
// this green. Measured: both broken together is what turns it red. The
// redundancy is deliberate and the comment in access.ts says so, because
// a guard nothing appears to test is one a later pass deletes.
test("the verifier names its own algorithm rather than reading the token's", async () => {
  const { publicKey } = await keypair();
  const jwk = await jwkOf(publicKey, "k1");
  __setAccessKeys("team", [jwk]);

  const head = b64urlText(JSON.stringify({ alg: "HS256", kid: "k1", typ: "JWT" }));
  const body = b64urlText(JSON.stringify({ aud: "aud-1", exp: soon(), email: "a@b.c" }));
  // Keyed on the public key's own modulus — the value an attacker has,
  // which is the whole trick of the algorithm-confusion attack.
  const hmacKey = await crypto.subtle.importKey(
    "raw", enc.encode(jwk.n), { name: "HMAC", hash: "SHA-256" }, false, ["sign"],
  );
  const sig = new Uint8Array(
    await crypto.subtle.sign("HMAC", hmacKey, enc.encode(`${head}.${body}`)),
  );
  assert.equal(await accessIdentity(req(`${head}.${body}.${b64url(sig)}`), ENV), null);
});

// Cloudflare rotates these keys. A kid the cache has never seen is the
// shape of a rotation, so it refetches once rather than rejecting —
// otherwise every viewer is locked out until the isolate recycles.
test("an unknown key id refetches the key set once", async () => {
  const { privateKey, publicKey } = await keypair();
  __setAccessKeys("team", [await jwkOf(publicKey, "stale")]);
  const fresh = await jwkOf(publicKey, "k2");

  const realFetch = globalThis.fetch;
  let fetches = 0;
  globalThis.fetch = (async () => {
    fetches++;
    return new Response(JSON.stringify({ keys: [fresh] }), {
      headers: { "content-type": "application/json" },
    });
  }) as typeof fetch;
  try {
    const jwt = await mint(privateKey, "k2", { aud: "aud-1", exp: soon(), email: "a@b.c" });
    assert.equal(await accessIdentity(req(jwt), ENV), "a@b.c");
    assert.equal(fetches, 1, "the rotation was not refetched exactly once");
  } finally {
    globalThis.fetch = realFetch;
    __setAccessKeys("team", null);
  }
});

test("a token with no email falls back to its subject", async () => {
  const { privateKey, publicKey } = await keypair();
  __setAccessKeys("team", [await jwkOf(publicKey, "k1")]);
  const jwt = await mint(privateKey, "k1", { aud: "aud-1", exp: soon(), sub: "u-123" });
  assert.equal(await accessIdentity(req(jwt), ENV), "u-123");
});

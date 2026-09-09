/**
 * Cloudflare Access identity, verified rather than trusted.
 *
 * The dashboard is a browser page, and a browser cannot be given the
 * shared secret the harness uses: a page that carries a bearer token in
 * its JavaScript hands that token to everyone who opens it, and
 * STATE_TOKEN grants write access to the move record as well as read
 * access to the stats. So the page authenticates as its viewer instead,
 * with the JWT Cloudflare Access puts on every request it proxies.
 *
 * THE HEADER IS NOT THE PROOF. Access sets Cf-Access-Jwt-Assertion on
 * requests it forwards, but this Worker is also reachable at its
 * workers.dev origin, which nothing fronts — the webhooks post there.
 * Anyone can set a header. So the token is verified: signature against
 * the team's published keys, audience against this application's, and
 * expiry. Reading the header and believing it would be an open store
 * with an authentication-shaped comment above it.
 *
 * FAIL CLOSED WHEN UNCONFIGURED. With no team domain or audience set,
 * nothing is ever accepted and the only way in stays the bearer token.
 * That is the state the Worker ships in, so publishing this change
 * before the Access application exists opens nothing.
 */

export interface AccessConfig {
  /** e.g. "yourteam" in yourteam.cloudflareaccess.com */
  ACCESS_TEAM_DOMAIN?: string;
  /** The Access application's Audience (AUD) tag. */
  ACCESS_AUD?: string;
}

interface Jwk {
  kid: string;
  kty: string;
  n: string;
  e: string;
  alg?: string;
}

/**
 * Keys, cached for the isolate's life.
 *
 * Cloudflare rotates these, and a rotation mid-life would make every
 * verification fail until the isolate is recycled — so a miss on the
 * kid refetches rather than rejecting. The cache is per-isolate and
 * lossy by design; it exists to keep a page's dozen requests from
 * fetching the key set a dozen times, not to be a store.
 */
let cachedKeys: { domain: string; keys: Jwk[] } | null = null;

async function fetchKeys(domain: string, force: boolean): Promise<Jwk[]> {
  if (!force && cachedKeys && cachedKeys.domain === domain) {
    return cachedKeys.keys;
  }
  const res = await fetch(`https://${domain}.cloudflareaccess.com/cdn-cgi/access/certs`);
  if (!res.ok) {
    throw new Error(`access: fetching keys: HTTP ${res.status}`);
  }
  const body = (await res.json()) as { keys?: Jwk[] };
  const keys = body.keys ?? [];
  cachedKeys = { domain, keys };
  return keys;
}

/** Test seam: preload the key cache, or clear it with null. */
export function __setAccessKeys(domain: string, keys: Jwk[] | null): void {
  cachedKeys = keys === null ? null : { domain, keys };
}

function b64urlToBytes(s: string): Uint8Array {
  const pad = s.length % 4 === 0 ? "" : "=".repeat(4 - (s.length % 4));
  const bin = atob(s.replace(/-/g, "+").replace(/_/g, "/") + pad);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

function decodeSegment(seg: string): any {
  return JSON.parse(new TextDecoder().decode(b64urlToBytes(seg)));
}

/**
 * The identity on this request, or null.
 *
 * Null covers every reason equally — no header, no configuration, a bad
 * signature, the wrong audience, an expired token. The caller's answer
 * is the same in all of them, and a caller that could tell them apart
 * would be a caller that could be probed for which one it was.
 */
export async function accessIdentity(
  request: Request,
  env: AccessConfig,
): Promise<string | null> {
  const domain = env.ACCESS_TEAM_DOMAIN;
  const aud = env.ACCESS_AUD;
  // Before anything, and before any network call. Probing this found it
  // only incidentally covered: with the line gone an unconfigured Worker
  // still refused every token, but by way of an outbound fetch to
  // `https://undefined.cloudflareaccess.com/...` on every request that
  // carried a header. Refusing quietly is the correct behaviour and
  // asking the internet about it on each request is not, so the test
  // beside this asserts no fetch is attempted rather than only that the
  // answer is null.
  if (!domain || !aud) return null;

  const jwt = request.headers.get("cf-access-jwt-assertion");
  if (!jwt) return null;
  const parts = jwt.split(".");
  if (parts.length !== 3) return null;

  let header: any, payload: any;
  try {
    header = decodeSegment(parts[0]);
    payload = decodeSegment(parts[1]);
  } catch {
    return null;
  }
  // RS256 only. Accepting the algorithm a token names is the classic way
  // in — "none", or an HMAC keyed on the public key everybody has.
  //
  // This line and the verification below are REDUNDANT WITH EACH OTHER,
  // measured rather than assumed, and the redundancy is why probing
  // either one alone comes back green. Delete this check and an HS256
  // token still fails, because the verify call names
  // RSASSA-PKCS1-v1_5/SHA-256 itself and an HMAC is not an RSA
  // signature. Make that call read `header.alg` instead and the token
  // still fails, because this check already refused it. Both broken
  // together is what turns the algorithm-confusion test red — which is
  // the measurement, taken because two mutually-covering guards are
  // exactly the shape somebody later removes one of "since nothing
  // tests it".
  //
  // `kid` is different only in degree: without it the key lookup below
  // finds nothing and falls through to the same null.
  if (header?.alg !== "RS256" || !header?.kid) return null;

  // Audience before signature: it is free, and it is the check that
  // stops a valid token minted for a DIFFERENT application in the same
  // Access team from being replayed at this one.
  const auds = Array.isArray(payload?.aud) ? payload.aud : [payload?.aud];
  if (!auds.includes(aud)) return null;

  const now = Math.floor(Date.now() / 1000);
  if (typeof payload?.exp !== "number" || payload.exp <= now) return null;
  if (typeof payload?.nbf === "number" && payload.nbf > now) return null;

  const data = new TextEncoder().encode(`${parts[0]}.${parts[1]}`);
  const sig = b64urlToBytes(parts[2]);
  for (const force of [false, true]) {
    let keys: Jwk[];
    try {
      keys = await fetchKeys(domain, force);
    } catch {
      return null;
    }
    const jwk = keys.find((k) => k.kid === header.kid);
    if (!jwk) {
      // A kid we have never seen is the shape of a rotation, so the
      // second pass refetches. A kid that is simply wrong costs one
      // extra fetch and still fails.
      continue;
    }
    let key: CryptoKey;
    try {
      key = await crypto.subtle.importKey(
        "jwk",
        { kty: jwk.kty, n: jwk.n, e: jwk.e, alg: "RS256", ext: true },
        { name: "RSASSA-PKCS1-v1_5", hash: "SHA-256" },
        false,
        ["verify"],
      );
    } catch {
      return null;
    }
    const ok = await crypto.subtle.verify("RSASSA-PKCS1-v1_5", key, sig, data);
    if (!ok) return null;
    return typeof payload.email === "string" && payload.email !== ""
      ? payload.email
      : (payload.sub ?? "access");
  }
  return null;
}

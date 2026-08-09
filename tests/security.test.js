// security.test.js — end-to-end security boundary suite (black-box real backend).
//
// Focuses on the security properties that only show up with a full HTTP/WS backend + a real JWT
// parsing/validation chain. It deliberately does not duplicate the Go unit tests (auth_test /
// middleware_test / handler_test already cover these in-process with forged tokens); instead it
// re-locks the contract from the browser / external-system perspective:
//   - sanitizeUser never leaks password_hash: the four identity paths setup / login / sso_login /
//     me all carry no bcrypt hash end-to-end (AGENTS §10 contract).
//   - JWT alg=none forgery is rejected (§14 red line: Parse's keyFunc enforces *SigningMethodHMAC).
//   - JWT payload tampering (changing uid) is rejected due to a signature mismatch — proving the
//     signature covers every claim.
//   - Cross-backend secret isolation: a token issued by backend A is invalid against backend B
//     (the point of the helpers' random secret).
//   - logout is a stateless no-op: after logout the same token still authenticates (no server-side
//     blacklist, §6 contract).
//   - An already-expired token (jwtTtl=-1 → issued already expired) is rejected end-to-end.
//   - WS garbage / forged query tokens are rejected before the upgrade (1004).
//
// Each test starts an independent backend and stops it in finally, with no cross-pollution.

import { describe, test } from "node:test";
import assert from "node:assert/strict";
import { startBackend, apiFetch } from "./helpers.js";

// b64urlEncodeObj JSON-serializes an object and base64url-encodes it (Node's Buffer supports 'base64url' natively).
function b64urlEncodeObj(obj) {
  return Buffer.from(JSON.stringify(obj), "utf8").toString("base64url");
}

// forgeAlgNoneToken builds a forged JWT with alg=none: the header alg is set to "none" and the
// signature segment is empty. jwt.v5's Parse validates in keyFunc that the method must be
// *SigningMethodHMAC; alg=none maps to *SigningMethodNone — so even though "no signature" would
// logically verify, keyFunc returns an error first, which maps to 1004. This is exactly the
// defense that §14 forbids removing.
function forgeAlgNoneToken(overrides = {}) {
  const now = Math.floor(Date.now() / 1000);
  const header = { alg: "none", typ: "JWT" };
  const payload = {
    uid: "forged-uid",
    usr: "forged",
    adm: true,
    iat: now,
    exp: now + 3600,
    sub: "forged",
    ...overrides,
  };
  return `${b64urlEncodeObj(header)}.${b64urlEncodeObj(payload)}.`;
}

// tamperPayload decodes a real JWT's payload segment, rewrites it with the mutator, re-encodes
// it, and reassembles it with the original header + original signature. Because the payload
// changed while the signature did not, the HMAC check necessarily fails → 1004.
function tamperPayload(token, mutator) {
  const parts = token.split(".");
  assert.equal(parts.length, 3, "JWT must have 3 segments");
  const claims = JSON.parse(Buffer.from(parts[1], "base64url").toString("utf8"));
  mutator(claims);
  const tamperedPayload = b64urlEncodeObj(claims);
  return `${parts[0]}.${tamperedPayload}.${parts[2]}`;
}

// assertNoHash asserts that the user object's password_hash is empty or absent. model.User's
// json tag has no omitempty, and sanitizeUser clears it to "", so both "" and undefined are
// accepted.
function assertNoHash(user, label) {
  const ph = user && user.password_hash;
  assert.ok(
    ph === "" || ph === undefined || ph === null,
    `${label}: password_hash must not leak, actual: ${JSON.stringify(ph)}`,
  );
}

async function fresh() {
  return startBackend();
}

describe("security boundaries (end-to-end)", () => {
  // 1. None of the four identity paths leak password_hash
  test("sanitizeUser: setup/login/sso_login/me never leak password_hash", async () => {
    const b = await fresh();
    try {
      const setup = await apiFetch(b.baseUrl, "POST", "/api/v1/setup", {
        json: { username: "admin", password: "admin123" },
      });
      assert.equal(setup.body.code, 0);
      const adminToken = setup.body.data.token;
      assertNoHash(setup.body.data.user, "setup");

      const login = await apiFetch(b.baseUrl, "POST", "/api/v1/login", {
        json: { username: "admin", password: "admin123" },
      });
      assert.equal(login.body.code, 0);
      assertNoHash(login.body.data.user, "login");

      const minted = await apiFetch(b.baseUrl, "POST", "/api/v1/sso/tokens", {
        token: adminToken,
        json: { username: "admin" },
      });
      assert.equal(minted.body.code, 0);
      const sso = await apiFetch(b.baseUrl, "POST", "/api/v1/sso_login", {
        json: { uuid: minted.body.data.uuid, password: minted.body.data.password },
      });
      assert.equal(sso.body.code, 0);
      assertNoHash(sso.body.data.user, "sso_login");

      const me = await apiFetch(b.baseUrl, "GET", "/api/v1/me", { token: adminToken });
      assert.equal(me.body.code, 0);
      assertNoHash(me.body.data.user, "me");
    } finally {
      await b.stop();
    }
  });

  // 2. alg=none forgery → 1004
  test("JWT: alg=none forged token rejected → 1004", async () => {
    const b = await fresh();
    try {
      await apiFetch(b.baseUrl, "POST", "/api/v1/setup", {
        json: { username: "admin", password: "admin123" },
      });
      const noneToken = forgeAlgNoneToken({ uid: "anyone", usr: "admin", adm: true });
      const { body } = await apiFetch(b.baseUrl, "GET", "/api/v1/me", { token: noneToken });
      assert.equal(body.code, 1004);
    } finally {
      await b.stop();
    }
  });

  // 3. payload tampering (uid changed) → 1004, and the original token still works (rules out a dead backend)
  test("JWT: tampered payload (uid changed) rejected → 1004", async () => {
    const b = await fresh();
    try {
      const setup = await apiFetch(b.baseUrl, "POST", "/api/v1/setup", {
        json: { username: "admin", password: "admin123" },
      });
      const real = setup.body.data.token;
      const tampered = tamperPayload(real, (c) => {
        c.uid = "attacker-uid";
      });
      const { body } = await apiFetch(b.baseUrl, "GET", "/api/v1/me", { token: tampered });
      assert.equal(body.code, 1004);
      // The original token still works, proving the 1004 comes from the signature mismatch, not an environment issue
      const ok = await apiFetch(b.baseUrl, "GET", "/api/v1/me", { token: real });
      assert.equal(ok.body.code, 0);
    } finally {
      await b.stop();
    }
  });

  // 4. Cross-backend secret isolation: A's token → B /me 1004
  test("JWT: cross-backend token is invalid against a different secret → 1004", async () => {
    const a = await fresh();
    const bb = await fresh();
    try {
      const setup = await apiFetch(a.baseUrl, "POST", "/api/v1/setup", {
        json: { username: "admin", password: "admin123" },
      });
      const tokenFromA = setup.body.data.token;
      // Initialize B too, to rule out "not initialized" noise and focus on signature validation
      await apiFetch(bb.baseUrl, "POST", "/api/v1/setup", {
        json: { username: "admin", password: "admin123" },
      });
      const { body } = await apiFetch(bb.baseUrl, "GET", "/api/v1/me", { token: tokenFromA });
      assert.equal(body.code, 1004);
    } finally {
      await Promise.all([a.stop(), bb.stop()]);
    }
  });

  // 5. logout is stateless: the same token still authenticates after logout (no server-side blacklist, §6 contract)
  test("logout: stateless no-op — same token still authenticates after logout", async () => {
    const b = await fresh();
    try {
      const setup = await apiFetch(b.baseUrl, "POST", "/api/v1/setup", {
        json: { username: "admin", password: "admin123" },
      });
      const token = setup.body.data.token;
      const out = await apiFetch(b.baseUrl, "POST", "/api/v1/logout", { token });
      assert.equal(out.body.code, 0);
      // After logout the same token can still drive /me
      const me = await apiFetch(b.baseUrl, "GET", "/api/v1/me", { token });
      assert.equal(me.body.code, 0);
      assert.equal(me.body.data.user.username, "admin");
    } finally {
      await b.stop();
    }
  });

  // 6. An already-expired token is rejected end-to-end (jwtTtl=-1 → exp = now - 60s, issued already expired)
  test("JWT: expired token rejected end-to-end → 1004", async () => {
    const b = await startBackend({ jwtTtl: -1 });
    try {
      const setup = await apiFetch(b.baseUrl, "POST", "/api/v1/setup", {
        json: { username: "admin", password: "admin123" },
      });
      // setup succeeds (Sign only encodes + signs, it does not validate exp), but the token is already expired
      assert.equal(setup.body.code, 0);
      const expired = setup.body.data.token;
      const me = await apiFetch(b.baseUrl, "GET", "/api/v1/me", { token: expired });
      assert.equal(me.body.code, 1004);
    } finally {
      await b.stop();
    }
  });

  // 7. WS garbage / forged query tokens are rejected before the upgrade (1004, fetch receives a JSON envelope)
  test("WS: garbage and alg=none query tokens are rejected before upgrade → 1004", async () => {
    const b = await fresh();
    try {
      const setup = await apiFetch(b.baseUrl, "POST", "/api/v1/setup", {
        json: { username: "admin", password: "admin123" },
      });
      const token = setup.body.data.token;
      // Create a real session to get a valid id, ensuring the 1004 is about the token, not not-found
      const tpl = await apiFetch(b.baseUrl, "POST", "/api/v1/templates", {
        token,
        json: { name: "ws-sectok-tpl", command: "/bin/bash", args: ["-c", "sleep 5"], cwd: "/tmp" },
      });
      const sess = await apiFetch(b.baseUrl, "POST", "/api/v1/sessions", {
        token,
        json: { template_id: tpl.body.data.id, title: "ws-sectok" },
      });
      const sid = sess.body.data.id;

      const r1 = await fetch(`${b.baseUrl}/api/v1/ws/${sid}?token=not-a-jwt`);
      const j1 = await r1.json();
      assert.equal(j1.code, 1004);

      const forged = forgeAlgNoneToken({ uid: "x", usr: "x", adm: true });
      const r2 = await fetch(`${b.baseUrl}/api/v1/ws/${sid}?token=${encodeURIComponent(forged)}`);
      const j2 = await r2.json();
      assert.equal(j2.code, 1004);

      // Cleanup: DELETE with a valid token
      await apiFetch(b.baseUrl, "DELETE", `/api/v1/sessions/${sid}`, { token });
    } finally {
      await b.stop();
    }
  });
});
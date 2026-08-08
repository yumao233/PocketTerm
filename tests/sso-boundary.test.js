// sso-boundary.test.js — Boundaries and negative paths for the SSO single sign-on chain.
//
// Covers:
//   - minting for a non-existent user → 1006
//   - unauthenticated minting → 1004
//   - using the SSO identity obtained via mint (still admin, but through sso_login) to mint in
//     reverse → 1005? Note: sso_login also produces admin (adm=true), so it can mint. To test
//     "non-admin rejected" we rely on the adm claim — and the only user in this system is admin.
//     So that negative path is covered by Go unit tests; here we instead verify the admin mint
//     chain itself (including the redirect variant and query-param variant).
//   - a wrong password consumes the one-time token → a subsequent correct token cannot be
//     reused either (1003)
//   - redirect=1 success → 302; redirect=1 with invalid credentials → 200+1003 (not 302)
//   - the two submission methods (query params and JSON body) are equivalent

import { describe, test, before, after } from "node:test";
import assert from "node:assert/strict";
import { startBackend, apiFetch } from "./helpers.js";

describe("SSO boundary", () => {
  let b;
  let adminToken;

  before(async () => {
    b = await startBackend();
    const setup = await apiFetch(b.baseUrl, "POST", "/api/v1/setup", {
      json: { username: "admin", password: "admin123" },
    });
    adminToken = setup.body.data.token;
  });

  after(async () => {
    if (b) await b.stop();
  });

  test("mint: for a non-existent user → 1006", async () => {
    const { body } = await apiFetch(b.baseUrl, "POST", "/api/v1/sso/tokens", {
      token: adminToken,
      json: { username: "ghost-user" },
    });
    assert.equal(body.code, 1006);
  });

  test("mint: unauthenticated → 1004", async () => {
    const { body } = await apiFetch(b.baseUrl, "POST", "/api/v1/sso/tokens", {
      json: { username: "admin" },
    });
    assert.equal(body.code, 1004);
  });

  test("mint: missing username → 1006 (empty username is equivalent to a non-existent user)", async () => {
    // service.GenSsoToken does not separately validate an empty username; it directly calls
    // GetByUsername("") which finds nothing → ErrNotFound(1006). This is the actual behavior of
    // the current implementation.
    const { body } = await apiFetch(b.baseUrl, "POST", "/api/v1/sso/tokens", {
      token: adminToken,
      json: {},
    });
    assert.equal(body.code, 1006);
  });

  test("sso_login: a wrong password consumes the one-time token (not reusable)", async () => {
    const minted = await apiFetch(b.baseUrl, "POST", "/api/v1/sso/tokens", {
      token: adminToken,
      json: { username: "admin" },
    });
    assert.equal(minted.body.code, 0);
    const { uuid, password } = minted.body.data;

    // First attempt: wrong password → 1003 (the token is already consumed at the Consume stage)
    const wrongPw = await apiFetch(b.baseUrl, "POST", "/api/v1/sso_login", {
      json: { uuid, password: "deadbeef" },
    });
    assert.equal(wrongPw.body.code, 1003);

    // Second attempt: the correct password should also fail 1003 (one-time, already consumed)
    const correct = await apiFetch(b.baseUrl, "POST", "/api/v1/sso_login", {
      json: { uuid, password },
    });
    assert.equal(correct.body.code, 1003);
  });

  test("sso_login: completely invalid credentials → 1003", async () => {
    const { body } = await apiFetch(b.baseUrl, "POST", "/api/v1/sso_login", {
      json: { uuid: "no-such-uuid", password: "whatever" },
    });
    assert.equal(body.code, 1003);
  });

  test("sso_login: query-param submission and JSON body submission are equivalent", async () => {
    // Two independent tokens, submitted via the two methods respectively, both should exchange for a JWT
    const m1 = await apiFetch(b.baseUrl, "POST", "/api/v1/sso/tokens", {
      token: adminToken,
      json: { username: "admin" },
    });
    const m2 = await apiFetch(b.baseUrl, "POST", "/api/v1/sso/tokens", {
      token: adminToken,
      json: { username: "admin" },
    });

    // Body method
    const viaBody = await apiFetch(b.baseUrl, "POST", "/api/v1/sso_login", {
      json: { uuid: m1.body.data.uuid, password: m1.body.data.password },
    });
    assert.equal(viaBody.body.code, 0);
    assert.equal(viaBody.body.data.user.username, "admin");

    // Query-param method: use fetch directly because apiFetch does not build query params
    const directRes = await fetch(
      `${b.baseUrl}/api/v1/sso_login?uuid=${encodeURIComponent(m2.body.data.uuid)}&password=${encodeURIComponent(m2.body.data.password)}`,
      { method: "POST" },
    );
    const direct = await directRes.json();
    assert.equal(direct.code, 0);
    assert.equal(direct.data.user.username, "admin");
    assert.ok(typeof direct.data.token === "string");
  });

  test("redirect=1: success → 302 to /?token=; failed credentials → 200+1003 (not 302)", async () => {
    const m = await apiFetch(b.baseUrl, "POST", "/api/v1/sso/tokens", {
      token: adminToken,
      json: { username: "admin" },
    });
    // Successful redirect
    const ok = await fetch(
      `${b.baseUrl}/api/v1/sso_login?uuid=${encodeURIComponent(m.body.data.uuid)}&password=${encodeURIComponent(m.body.data.password)}&redirect=1`,
      { method: "POST", redirect: "manual" },
    );
    assert.equal(ok.status, 302);
    const loc = ok.headers.get("location") || "";
    assert.ok(loc.includes("/?token="), `Location should contain /?token=, actual: ${loc}`);

    // Failure: no redirect, returns 200 + 1003
    const bad = await fetch(
      `${b.baseUrl}/api/v1/sso_login?uuid=dead&password=beef&redirect=1`,
      { method: "POST", redirect: "manual" },
    );
    assert.equal(bad.status, 200);
    const badBody = await bad.json();
    assert.equal(badBody.code, 1003);
  });

  test("redirect=1 token must be a complete, usable JWT", async () => {
    const m = await apiFetch(b.baseUrl, "POST", "/api/v1/sso/tokens", {
      token: adminToken,
      json: { username: "admin" },
    });
    const ok = await fetch(
      `${b.baseUrl}/api/v1/sso_login?uuid=${encodeURIComponent(m.body.data.uuid)}&password=${encodeURIComponent(m.body.data.password)}&redirect=1`,
      { method: "POST", redirect: "manual" },
    );
    const loc = ok.headers.get("location") || "";
    const tok = new URL(loc, b.baseUrl).searchParams.get("token");
    assert.ok(tok, "redirect URL should carry a token param");
    // The token should be able to drive /me
    const me = await apiFetch(b.baseUrl, "GET", "/api/v1/me", { token: tok });
    assert.equal(me.body.code, 0);
    assert.equal(me.body.data.user.username, "admin");
  });
});
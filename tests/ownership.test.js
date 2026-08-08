// ownership.test.js — Session ownership and identity consistency.
//
// Design note: this system has only one user-creation path — POST /setup registers the single
// admin. There is no registration endpoint for ordinary users, and an SSO token can only be
// bound to an existing user (i.e. that admin). So at the API level there is no legitimate
// "two different userID" scenario, and cross-user privilege escalation cannot be constructed
// through real APIs. The cross-user ownership check (service.Get's rec.UserID != user.ID →
// ErrForbidden / 1005) is already covered by Go unit tests (service_test.go). This file
// verifies the API-reachable identity consistency:
//   - the JWT produced by SSO login shares the same identity as the original admin and can
//     operate on sessions created by admin
//   - using admin's token to access a non-existent session id → 1006 (not 3001/1005)
//   - the sso_login token likewise drives /me to return admin

import { describe, test, before, after } from "node:test";
import assert from "node:assert/strict";
import { startBackend, apiFetch } from "./helpers.js";

describe("session ownership and identity consistency", () => {
  let b;
  let adminToken; // token from a direct login
  let ssoToken; // token obtained via sso_login (same admin identity)

  before(async () => {
    b = await startBackend();
    await apiFetch(b.baseUrl, "POST", "/api/v1/setup", {
      json: { username: "admin", password: "admin123" },
    });
    const login = await apiFetch(b.baseUrl, "POST", "/api/v1/login", {
      json: { username: "admin", password: "admin123" },
    });
    adminToken = login.body.data.token;

    // Mint an SSO one-time token for admin and exchange it for a JWT
    const minted = await apiFetch(b.baseUrl, "POST", "/api/v1/sso/tokens", {
      token: adminToken,
      json: { username: "admin" },
    });
    assert.equal(minted.body.code, 0);
    const sso = await apiFetch(b.baseUrl, "POST", "/api/v1/sso_login", {
      json: { uuid: minted.body.data.uuid, password: minted.body.data.password },
    });
    assert.equal(sso.body.code, 0);
    ssoToken = sso.body.data.token;
  });

  after(async () => {
    if (b) await b.stop();
  });

  test("SSO token shares admin identity: both /me return admin", async () => {
    const meAdmin = await apiFetch(b.baseUrl, "GET", "/api/v1/me", {
      token: adminToken,
    });
    const meSso = await apiFetch(b.baseUrl, "GET", "/api/v1/me", {
      token: ssoToken,
    });
    assert.equal(meAdmin.body.code, 0);
    assert.equal(meSso.body.code, 0);
    assert.equal(meAdmin.body.data.user.username, "admin");
    assert.equal(meSso.body.data.user.username, "admin");
    // Same userID (SSO is bound to the same user)
    assert.equal(meAdmin.body.data.user.id, meSso.body.data.user.id);
  });

  test("SSO identity can operate on sessions created by admin", async () => {
    // admin creates a template and a session
    const tpl = await apiFetch(b.baseUrl, "POST", "/api/v1/templates", {
      token: adminToken,
      json: { name: "shared-tpl", command: "/bin/bash", args: ["-c", "sleep 5"], cwd: "/tmp" },
    });
    const sess = await apiFetch(b.baseUrl, "POST", "/api/v1/sessions", {
      token: adminToken,
      json: { template_id: tpl.body.data.id, title: "shared" },
    });
    const sid = sess.body.data.id;

    // Use the SSO identity to list/get/delete — same userID, all should succeed
    const list = await apiFetch(b.baseUrl, "GET", "/api/v1/sessions", {
      token: ssoToken,
    });
    assert.equal(list.body.code, 0);
    assert.ok(list.body.data.sessions.some((s) => s.id === sid));

    const got = await apiFetch(b.baseUrl, "GET", `/api/v1/sessions/${sid}`, {
      token: ssoToken,
    });
    assert.equal(got.body.code, 0);
    assert.equal(got.body.data.id, sid);

    const del = await apiFetch(b.baseUrl, "DELETE", `/api/v1/sessions/${sid}`, {
      token: ssoToken,
    });
    assert.equal(del.body.code, 0);
  });

  test("accessing a non-existent session → 1006 (service.Get precedes ownership)", async () => {
    const fakeId = "11111111-1111-1111-1111-111111111111";
    const got = await apiFetch(
      b.baseUrl,
      "GET",
      `/api/v1/sessions/${fakeId}`,
      { token: ssoToken },
    );
    assert.equal(got.body.code, 1006);
    const del = await apiFetch(
      b.baseUrl,
      "DELETE",
      `/api/v1/sessions/${fakeId}`,
      { token: ssoToken },
    );
    assert.equal(del.body.code, 1006);
  });
});
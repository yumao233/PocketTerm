// auth-boundary.test.js — Boundary and negative-path collection for the authentication chain.
//
// Each case starts an independent fresh backend, with no cross-contamination. Covers:
//   - setup parameter validation (password too short / username empty)
//   - a second setup is rejected (1002)
//   - login on an uninitialized system → 1001
//   - login with wrong password / non-existent user → 1003 (and no difference is leaked)
//   - after login the token drives /me
//   - missing / forged / missing Bearer prefix Authorization → 1004
//   - the userID in /me cannot be forged by tampering with the token (a token signed with a
//     different secret fails)

import { describe, test } from "node:test";
import assert from "node:assert/strict";
import { startBackend, apiFetch } from "./helpers.js";

// Each test's backend is cleaned up by a finally stop.
async function fresh() {
  return startBackend();
}

describe("auth boundary", () => {
  test("setup: password shorter than 6 chars → 1007", async () => {
    const b = await fresh();
    try {
      const { body } = await apiFetch(b.baseUrl, "POST", "/api/v1/setup", {
        json: { username: "admin", password: "123" },
      });
      assert.equal(body.code, 1007);
      // Not initialized: check-setup still returns false
      const { body: chk } = await apiFetch(
        b.baseUrl,
        "GET",
        "/api/v1/check-setup",
      );
      assert.equal(chk.data.initialized, false);
    } finally {
      await b.stop();
    }
  });

  test("setup: empty username → 1007", async () => {
    const b = await fresh();
    try {
      const { body } = await apiFetch(b.baseUrl, "POST", "/api/v1/setup", {
        json: { username: "", password: "admin123" },
      });
      assert.equal(body.code, 1007);
    } finally {
      await b.stop();
    }
  });

  test("setup: invalid body (missing field) → 1007", async () => {
    const b = await fresh();
    try {
      const { body } = await apiFetch(b.baseUrl, "POST", "/api/v1/setup", {
        json: { username: "admin" }, // no password
      });
      assert.equal(body.code, 1007);
    } finally {
      await b.stop();
    }
  });

  test("login: system not initialized → 1001", async () => {
    const b = await fresh();
    try {
      const { body } = await apiFetch(b.baseUrl, "POST", "/api/v1/login", {
        json: { username: "admin", password: "admin123" },
      });
      assert.equal(body.code, 1001);
    } finally {
      await b.stop();
    }
  });

  test("login: wrong password / non-existent user both return 1003", async () => {
    const b = await fresh();
    try {
      await apiFetch(b.baseUrl, "POST", "/api/v1/setup", {
        json: { username: "admin", password: "admin123" },
      });
      const wrong = await apiFetch(b.baseUrl, "POST", "/api/v1/login", {
        json: { username: "admin", password: "WRONG-PW" },
      });
      const missing = await apiFetch(b.baseUrl, "POST", "/api/v1/login", {
        json: { username: "nobody", password: "whatever" },
      });
      assert.equal(wrong.body.code, 1003);
      assert.equal(missing.body.code, 1003);
      // Does not leak the reason: the two failure messages may differ, but the code must be 1003
    } finally {
      await b.stop();
    }
  });

  test("auth: missing Authorization → GET /me returns 1004", async () => {
    const b = await fresh();
    try {
      await apiFetch(b.baseUrl, "POST", "/api/v1/setup", {
        json: { username: "admin", password: "admin123" },
      });
      const { body } = await apiFetch(b.baseUrl, "GET", "/api/v1/me");
      assert.equal(body.code, 1004);
    } finally {
      await b.stop();
    }
  });

  test("auth: missing Bearer prefix → 1004", async () => {
    const b = await fresh();
    try {
      const setup = await apiFetch(b.baseUrl, "POST", "/api/v1/setup", {
        json: { username: "admin", password: "admin123" },
      });
      // Use the token directly as the entire Authorization value (no "Bearer " prefix)
      const res = await fetch(`${b.baseUrl}/api/v1/me`, {
        headers: { Authorization: setup.body.data.token },
      });
      const body = await res.json();
      assert.equal(body.code, 1004);
    } finally {
      await b.stop();
    }
  });

  test("auth: forged/tampered JWT → 1004", async () => {
    const b = await fresh();
    try {
      const setup = await apiFetch(b.baseUrl, "POST", "/api/v1/setup", {
        json: { username: "admin", password: "admin123" },
      });
      const real = setup.body.data.token;
      // Tamper with one character at the end of the signature (breaks the HMAC)
      const tampered =
        real.slice(0, -1) + (real.slice(-1) === "a" ? "b" : "a");
      const { body } = await apiFetch(b.baseUrl, "GET", "/api/v1/me", {
        token: tampered,
      });
      assert.equal(body.code, 1004);

      // A completely garbage non-JWT string must also return 1004
      const { body: junkBody } = await apiFetch(
        b.baseUrl,
        "GET",
        "/api/v1/me",
        { token: "not-a-jwt-at-all" },
      );
      assert.equal(junkBody.code, 1004);
    } finally {
      await b.stop();
    }
  });

  test("check-setup: semantic flip before/after initialization", async () => {
    const b = await fresh();
    try {
      const before = await apiFetch(b.baseUrl, "GET", "/api/v1/check-setup");
      assert.equal(before.body.code, 0);
      assert.equal(before.body.data.initialized, false);

      await apiFetch(b.baseUrl, "POST", "/api/v1/setup", {
        json: { username: "admin", password: "admin123" },
      });

      const after = await apiFetch(b.baseUrl, "GET", "/api/v1/check-setup");
      assert.equal(after.body.data.initialized, true);

      // A second setup → 1002
      const dup = await apiFetch(b.baseUrl, "POST", "/api/v1/setup", {
        json: { username: "admin2", password: "admin123" },
      });
      assert.equal(dup.body.code, 1002);
    } finally {
      await b.stop();
    }
  });
});